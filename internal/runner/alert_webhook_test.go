package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"switch-monitor/internal/alerting"
	"switch-monitor/internal/config"
)

// captureNotifier records every rendered message the webhook hands to Telegram.
type captureNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureNotifier) notify(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
}

func (c *captureNotifier) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return ""
	}
	return c.msgs[len(c.msgs)-1]
}

func (c *captureNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

func newTestWebhookServer(t *testing.T, cfg *config.WebhookConfig) (*AlertWebhookServer, *captureNotifier) {
	t.Helper()
	n := &captureNotifier{}
	return NewAlertWebhookServer(cfg, alerting.NewWebhookRenderer(), n.notify), n
}

func doRequest(h http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.RemoteAddr = "192.168.2.178:41234"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func testWebhookCfg() *config.WebhookConfig {
	return &config.WebhookConfig{
		Enabled: true,
		Path:    "/alert",
		Token:   "s3cret-token",
	}
}

func TestAlertWebhookRejectsMissingToken(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "port 3 down", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if n.count() != 0 {
		t.Fatal("unauthorized request must not notify")
	}
}

func TestAlertWebhookRejectsWrongToken(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "x",
		map[string]string{alertTokenHeader: "nope"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	if n.count() != 0 {
		t.Fatal("wrong token must not notify")
	}
}

// A token whose length differs must be rejected too (guards the length check
// that keeps the constant-time compare from leaking the secret length).
func TestAlertWebhookRejectsTokenOfDifferentLength(t *testing.T) {
	srv, _ := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "x",
		map[string]string{alertTokenHeader: "s3cret-token-longer"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestAlertWebhookAcceptsHeaderToken(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "disk /data at 91%",
		map[string]string{alertTokenHeader: "s3cret-token"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	if n.count() != 1 {
		t.Fatalf("want 1 notification, got %d", n.count())
	}
	if !strings.Contains(n.last(), "disk /data at 91%") {
		t.Fatalf("body missing from message: %q", n.last())
	}
}

func TestAlertWebhookAcceptsQueryToken(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodGet,
		"/alert?token=s3cret-token&title=cert+expiry&body=21+days+left", "", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	if n.count() != 1 {
		t.Fatalf("want 1 notification, got %d", n.count())
	}
	if !strings.Contains(n.last(), "cert expiry") {
		t.Fatalf("title missing: %q", n.last())
	}
}

func TestAlertWebhookAcceptsJSON(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	body, _ := json.Marshal(alertPayload{
		Title: "unit failed", Body: "mihomo.service exited", Severity: "error", Host: "surface",
	})
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", string(body),
		map[string]string{alertTokenHeader: "s3cret-token"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	msg := n.last()
	if !strings.Contains(msg, "unit failed") || !strings.Contains(msg, "❌") {
		t.Fatalf("expected error badge and title, got %q", msg)
	}
}

// The host reported in JSON must win over the connecting IP.
func TestAlertWebhookPrefersExplicitHost(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	body, _ := json.Marshal(alertPayload{Title: "t", Body: "b", Host: "datutu"})
	doRequest(srv.Handler(), http.MethodPost, "/alert", string(body),
		map[string]string{alertTokenHeader: "s3cret-token"})
	if !strings.Contains(n.last(), "datutu") {
		t.Fatalf("explicit host not used: %q", n.last())
	}
}

func TestAlertWebhookRejectsEmptyAlert(t *testing.T) {
	srv, n := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "   ",
		map[string]string{alertTokenHeader: "s3cret-token"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	if n.count() != 0 {
		t.Fatal("empty alert must not notify")
	}
}

func TestAlertWebhookRejectsOversizedBody(t *testing.T) {
	cfg := testWebhookCfg()
	cfg.MaxBodyBytes = 64
	srv, _ := newTestWebhookServer(t, cfg)
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", strings.Repeat("x", 200),
		map[string]string{alertTokenHeader: "s3cret-token"})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("want 413, got %d", w.Code)
	}
}

func TestAlertWebhookRejectsUnsupportedMethod(t *testing.T) {
	srv, _ := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodDelete, "/alert", "",
		map[string]string{alertTokenHeader: "s3cret-token"})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", w.Code)
	}
}

func TestAlertWebhookHealthNeedsNoToken(t *testing.T) {
	srv, _ := newTestWebhookServer(t, testWebhookCfg())
	w := doRequest(srv.Handler(), http.MethodGet, "/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	// Health must not leak the configured token or path.
	if strings.Contains(w.Body.String(), "s3cret-token") {
		t.Fatal("health response leaks the token")
	}
}

func TestAlertWebhookAllowUnauthenticated(t *testing.T) {
	cfg := &config.WebhookConfig{Enabled: true, Path: "/alert", AllowUnauthenticated: true}
	srv, n := newTestWebhookServer(t, cfg)
	w := doRequest(srv.Handler(), http.MethodPost, "/alert", "no token needed", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", w.Code)
	}
	if n.count() != 1 {
		t.Fatalf("want 1 notification, got %d", n.count())
	}
}

// The listener must stop when the parent context is cancelled.
func TestAlertWebhookShutsDownOnContextCancel(t *testing.T) {
	cfg := &config.WebhookConfig{Enabled: true, Listen: "127.0.0.1:0", Path: "/alert", Token: "t"}
	srv, _ := newTestWebhookServer(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down after context cancel")
	}
}
