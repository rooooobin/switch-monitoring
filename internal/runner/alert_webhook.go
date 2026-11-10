package runner

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"switch-monitor/internal/alerting"
	"switch-monitor/internal/config"
)

// AlertWebhook accepts alerts pushed by other hosts on the LAN (a headless
// server's health checks, a NAS supervisor, a cron job...) so they reach the
// same Telegram recipients as switch port alerts. Hosts therefore need no bot
// token of their own, and no MTA to send mail.
//
// Request contract (any of these work):
//
//	POST /alert                       body: the alert text (text/plain)
//	POST /alert  {"title":..,"body":..}   body: JSON
//	GET  /alert?title=..&body=..      body: query parameters
//
// The shared secret goes in the X-Alert-Token header or the token query
// parameter. `/health` is always unauthenticated and returns no configuration
// details, so an uptime probe works without leaking anything.
const (
	alertTokenHeader = "X-Alert-Token"
	alertBodyLimit   = 64 << 10 // hard ceiling, config may lower it
	maxAlertTitle    = 200
	maxAlertBody     = 4096
	webhookReadTO    = 10 * time.Second
)

// alertPayload is the accepted JSON shape.
type alertPayload struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Host     string `json:"host"`
}

// AlertWebhookServer is the inbound HTTP listener.
type AlertWebhookServer struct {
	cfg      *config.WebhookConfig
	notify   func(string)
	renderer *alerting.WebhookRenderer
	started  time.Time

	mu       sync.Mutex
	accepted int64
	rejected int64
}

// NewAlertWebhookServer builds the server. notify receives the rendered HTML.
func NewAlertWebhookServer(cfg *config.WebhookConfig, renderer *alerting.WebhookRenderer, notify func(string)) *AlertWebhookServer {
	return &AlertWebhookServer{cfg: cfg, notify: notify, renderer: renderer, started: time.Now()}
}

// authOK compares the presented token in constant time.
func (s *AlertWebhookServer) authOK(r *http.Request) bool {
	want := s.cfg.Token
	if want == "" {
		return s.cfg.AllowUnauthenticated
	}
	got := r.Header.Get(alertTokenHeader)
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	// Pad to equal length so subtle.ConstantTimeCompare does not leak length.
	a := []byte(got)
	b := []byte(want)
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// Handler returns the routes: the alert endpoint plus /health.
func (s *AlertWebhookServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.PathOrDefault(), s.handleAlert)
	mux.HandleFunc("/health", s.handleHealth)
	return mux
}

func (s *AlertWebhookServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	accepted, rejected := s.accepted, s.rejected
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"accepted": accepted,
		"rejected": rejected,
		"uptime_s": int64(time.Since(s.started).Seconds()),
	})
}

func (s *AlertWebhookServer) handleAlert(w http.ResponseWriter, r *http.Request) {
	if !s.authOK(r) {
		s.mu.Lock()
		s.rejected++
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}

	var p alertPayload
	switch r.Method {
	case http.MethodGet:
		p.Title = r.URL.Query().Get("title")
		p.Body = r.URL.Query().Get("body")
		p.Severity = r.URL.Query().Get("severity")
		p.Host = r.URL.Query().Get("host")
	case http.MethodPost:
		limit := s.cfg.MaxBodyBytesOrDefault()
		if limit > alertBodyLimit {
			limit = alertBodyLimit
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
			return
		}
		if int64(len(raw)) > limit {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("body exceeds %d bytes", limit)})
			return
		}
		if json.Valid(raw) {
			if err := json.Unmarshal(raw, &p); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
				return
			}
		} else {
			p.Body = string(raw)
		}
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST required"})
		return
	}

	p.Title = strings.TrimSpace(p.Title)
	p.Body = strings.TrimSpace(p.Body)
	p.Host = strings.TrimSpace(p.Host)
	p.Severity = strings.TrimSpace(p.Severity)
	if p.Host == "" {
		p.Host = remoteHost(r.RemoteAddr)
	}
	if p.Title == "" && p.Body == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty alert: provide title and/or body"})
		return
	}
	p.Title = truncateRunes(p.Title, maxAlertTitle)
	p.Body = truncateRunes(p.Body, maxAlertBody)

	msg := s.renderer.Render(p.Title, p.Body, p.Host, p.Severity)
	if s.notify != nil {
		s.notify(msg)
	}
	s.mu.Lock()
	s.accepted++
	s.mu.Unlock()
	slog.Info("Inbound alert accepted", "host", p.Host, "title", p.Title, "severity", p.Severity)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "queued", "host": p.Host})
}

func remoteHost(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max]))
	}
	return s
}

// ListenAndServe runs the endpoint until ctx is cancelled.
func (s *AlertWebhookServer) ListenAndServe(ctx context.Context) error {
	addr := s.cfg.ListenOrDefault()
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: webhookReadTO,
		ReadTimeout:       webhookReadTO,
		WriteTimeout:      webhookReadTO,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("Alert webhook listening",
			"addr", addr, "path", s.cfg.PathOrDefault(),
			"auth", s.cfg.Token != "")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("alert webhook: %w", err)
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// runAlertWebhook starts the inbound alert endpoint for the monitor's lifetime.
func (r *Runner) runAlertWebhook(ctx context.Context) {
	cfg := r.cfg.Webhook
	if cfg == nil || !cfg.Enabled {
		return
	}
	srv := NewAlertWebhookServer(cfg, alerting.NewWebhookRenderer(), r.notifyTelegramHTML)
	if err := srv.ListenAndServe(ctx); err != nil {
		slog.Error("Alert webhook stopped", "err", err)
		r.notifyTelegramPlain("❌ Alert webhook stopped: " + err.Error())
	}
}
