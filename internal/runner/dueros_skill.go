package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
)

const (
	// skillMaxRequestBody bounds a webhook read. A DuerOS request is a few hundred
	// bytes; the limit only exists to stop a hostile client from exhausting memory.
	skillMaxRequestBody = 1 << 20
	skillReadTimeout    = 30 * time.Second
	skillWriteTimeout   = 90 * time.Second
	skillIdleTimeout    = 120 * time.Second
	skillPurgeInterval  = time.Minute
)

// skillRepromptText is spoken when the user stays silent mid-session. Keeping the
// session open is what makes multi-turn conversation possible on a device whose
// microphone would otherwise close after every answer.
const skillRepromptText = "我在听，请说。"

var (
	reSpeechLink   = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reSpeechBullet = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)、])[ \t]*`)
	// reSpeechFence removes a code fence together with its language tag: reading
	// "go" or "python" aloud before the code is pure noise.
	reSpeechFence  = regexp.MustCompile("```[a-zA-Z0-9_+#.-]*")
	reSpeechNoise  = regexp.MustCompile(`[*_~#>|` + "`" + `]+`)
	reSpeechSpaces = regexp.MustCompile(`\s{2,}`)
	rePunctuation  = regexp.MustCompile(`[\s，。、！？!?,.~…·:：;；"'"'（）()\[\]【】]+`)
)

// skillSession is the in-memory conversation for one DuerOS session.
type skillSession struct {
	messages []adapter.LLMMessage
	updated  time.Time
}

// skillSessionStore keeps per-session chat history with a TTL.
//
// History lives in memory only: DuerOS sessions are short (a few minutes) and a
// restart losing them costs nothing but a fresh conversation.
type skillSessionStore struct {
	mu          sync.Mutex
	items       map[string]*skillSession
	maxMessages int
	ttl         time.Duration
	now         func() time.Time
}

func newSkillSessionStore(maxMessages int, ttl time.Duration) *skillSessionStore {
	if maxMessages <= 0 {
		maxMessages = 12
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &skillSessionStore{
		items:       map[string]*skillSession{},
		maxMessages: maxMessages,
		ttl:         ttl,
		now:         time.Now,
	}
}

// history returns a copy of the stored turns, oldest first.
func (s *skillSessionStore) history(id string) []adapter.LLMMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.items[id]
	if !ok {
		return nil
	}
	sess.updated = s.now()
	out := make([]adapter.LLMMessage, len(sess.messages))
	copy(out, sess.messages)
	return out
}

// append adds turns and trims from the front so the window stays bounded.
// Trimming keeps pairs aligned: dropping a lone user turn would leave the model
// with an assistant reply that answers nothing.
func (s *skillSessionStore) append(id string, msgs ...adapter.LLMMessage) {
	if id == "" || len(msgs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.items[id]
	if !ok {
		sess = &skillSession{}
		s.items[id] = sess
	}
	sess.messages = append(sess.messages, msgs...)
	sess.updated = s.now()
	if len(sess.messages) > s.maxMessages {
		drop := len(sess.messages) - s.maxMessages
		sess.messages = append([]adapter.LLMMessage(nil), sess.messages[drop:]...)
	}
}

func (s *skillSessionStore) drop(id string) {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
}

func (s *skillSessionStore) reset(id string) {
	s.drop(id)
}

// purge removes sessions idle longer than the TTL.
func (s *skillSessionStore) purge() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	removed := 0
	for id, sess := range s.items {
		if now.Sub(sess.updated) >= s.ttl {
			delete(s.items, id)
			removed++
		}
	}
	return removed
}

func (s *skillSessionStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// chatProvider is the narrow contract the skill needs from an LLM backend.
// Both the plain OpenAI-compatible client and the CodeBuddy client satisfy it,
// so provider-specific auth/retry logic stays out of the webhook.
type chatProvider interface {
	Chat(ctx context.Context, messages []adapter.LLMMessage) (string, *adapter.LLMUsage, error)
}

// openAIChatProvider adapts the generic LLM client to chatProvider.
type openAIChatProvider struct{ client *adapter.LLMChatClient }

func (p *openAIChatProvider) Chat(ctx context.Context, msgs []adapter.LLMMessage) (string, *adapter.LLMUsage, error) {
	return p.client.Chat(ctx, msgs)
}

// codeBuddyChatProvider binds a model to the CodeBuddy client, whose Chat has
// a per-call model argument rather than one fixed at construction time.
type codeBuddyChatProvider struct {
	client *adapter.CodeBuddyClient
	model  string
}

func (p *codeBuddyChatProvider) Chat(ctx context.Context, msgs []adapter.LLMMessage) (string, *adapter.LLMUsage, error) {
	return p.client.Chat(ctx, p.model, msgs)
}

// DuerOSSkillServer turns a DuerOS custom skill into a voice front-end for an
// LLM. The skill platform pushes each utterance here; the reply goes back as
// PlainText and the speaker's own TTS reads it aloud.
type DuerOSSkillServer struct {
	cfg      *config.XiaoduSkillConfig
	llm      chatProvider
	model    string
	provider string
	verifier *adapter.DuerOSSignatureVerifier
	sessions *skillSessionStore
	notify   func(string)
}

// newChatProvider builds the backend selected by cfg.Provider.
func newChatProvider(cfg *config.XiaoduSkillConfig) (chatProvider, string, string, error) {
	provider := cfg.ProviderOrDefault()
	model := cfg.LLMModelOrDefault()

	switch provider {
	case config.SkillProviderOpenAI:
		if strings.TrimSpace(cfg.LLMBaseURL) == "" {
			return nil, "", "", fmt.Errorf("xiaodu skill: llm_base_url is required for provider=openai")
		}
		if model == "" {
			return nil, "", "", fmt.Errorf("xiaodu skill: llm_model is required for provider=openai")
		}
		client, err := adapter.NewLLMChatClient(adapter.LLMChatOptions{
			BaseURL:     cfg.LLMBaseURL,
			APIKey:      cfg.LLMAPIKey,
			Model:       model,
			Proxy:       cfg.LLMProxy,
			Timeout:     cfg.LLMTimeoutOrDefault(),
			MaxTokens:   cfg.MaxTokens,
			Temperature: cfg.Temperature,
		})
		if err != nil {
			return nil, "", "", err
		}
		return &openAIChatProvider{client: client}, model, provider, nil

	case config.SkillProviderCodeBuddy:
		if model == "" {
			return nil, "", "", fmt.Errorf("xiaodu skill: llm_model is required for provider=codebuddy")
		}
		client, err := adapter.NewCodeBuddyClient(adapter.CodeBuddyOptions{
			Server:           cfg.LLMBaseURL,
			APIKey:           cfg.LLMAPIKey,
			TokenFile:        cfg.CodeBuddyTokenFile,
			RefreshTokenFile: cfg.CodeBuddyRefreshTokenFile,
			Proxy:            cfg.LLMProxy,
			Timeout:          cfg.LLMTimeoutOrDefault(),
			MaxTokens:        cfg.MaxTokens,
			Temperature:      cfg.Temperature,
		})
		if err != nil {
			return nil, "", "", err
		}
		return &codeBuddyChatProvider{client: client, model: model}, model, provider, nil

	default:
		return nil, "", "", fmt.Errorf("xiaodu skill: unknown provider %q (want %s or %s)",
			provider, config.SkillProviderOpenAI, config.SkillProviderCodeBuddy)
	}
}

// NewDuerOSSkillServer validates config and builds the server.
func NewDuerOSSkillServer(cfg *config.XiaoduSkillConfig, notify func(string)) (*DuerOSSkillServer, error) {
	if cfg == nil {
		return nil, fmt.Errorf("xiaodu skill config is missing")
	}
	llm, model, provider, err := newChatProvider(cfg)
	if err != nil {
		return nil, err
	}
	if !cfg.VerifySignatureOrDefault() {
		slog.Warn("DuerOS signature verification is DISABLED; anyone who finds this URL can drive your speaker and spend your LLM quota")
	}
	return &DuerOSSkillServer{
		cfg:      cfg,
		llm:      llm,
		model:    model,
		provider: provider,
		verifier: adapter.NewDuerOSSignatureVerifier(),
		sessions: newSkillSessionStore(cfg.HistoryMessagesOrDefault(), cfg.SessionTTLOrDefault()),
		notify:   notify,
	}, nil
}

// SessionCount exposes the live session count for status output.
func (s *DuerOSSkillServer) SessionCount() int { return s.sessions.count() }

// Model returns the configured model id.
func (s *DuerOSSkillServer) Model() string { return s.model }

// Provider returns the selected backend id.
func (s *DuerOSSkillServer) Provider() string { return s.provider }

// HandleRequest turns one DuerOS request into a reply. Pure of IO except for the
// LLM call, so it can be unit tested without an HTTP server.
func (s *DuerOSSkillServer) HandleRequest(ctx context.Context, req *adapter.DuerOSRequest) *adapter.DuerOSResponse {
	sessionID := req.Session.SessionID
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "default"
	}

	switch req.Request.Type {
	case adapter.DuerOSTypeSessionEnd:
		s.sessions.drop(sessionID)
		slog.Info("DuerOS session ended", "session", sessionID, "reason", req.Request.Reason)
		// No speech: the session is already over and the device ignores output here.
		return adapter.NewDuerOSGoodbye("")

	case adapter.DuerOSTypeLaunch:
		s.sessions.reset(sessionID)
		slog.Info("DuerOS skill launched", "session", sessionID, "skill", req.ApplicationID())
		return adapter.NewDuerOSReply(s.cfg.GreetingOrDefault(), skillRepromptText, nil)

	case adapter.DuerOSTypeIntent:
		// fallthrough below
	default:
		slog.Warn("Unsupported DuerOS request type", "type", req.Request.Type)
		return adapter.NewDuerOSReply("这个操作我还不支持。", skillRepromptText, nil)
	}

	text := strings.TrimSpace(req.Text())
	adapter.LogDuerOSRequest(req, text)

	if text == "" {
		return adapter.NewDuerOSReply("没听清，可以再说一遍吗？", skillRepromptText, nil)
	}
	if s.isExit(text) {
		s.sessions.drop(sessionID)
		return adapter.NewDuerOSGoodbye(s.cfg.FarewellOrDefault())
	}

	messages := make([]adapter.LLMMessage, 0, len(s.sessions.history(sessionID))+2)
	messages = append(messages, adapter.LLMMessage{
		Role:    adapter.LLMRoleSystem,
		Content: s.cfg.SystemPromptOrDefault(),
	})
	messages = append(messages, s.sessions.history(sessionID)...)
	messages = append(messages, adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: text})

	started := time.Now()
	reply, usage, err := s.llm.Chat(ctx, messages)
	elapsed := time.Since(started)
	if err != nil {
		slog.Error("DuerOS skill LLM call failed", "err", err, "session", sessionID,
			"model", s.Model(), "elapsed", elapsed.Round(time.Millisecond))
		return adapter.NewDuerOSReply("我这边暂时连不上模型，请稍后再试。", skillRepromptText, nil)
	}

	spoken := sanitizeSpeechText(reply, s.cfg.MaxReplyCharsOrDefault())
	if spoken == "" {
		spoken = "模型没有给出可朗读的回答。"
	}
	s.sessions.append(sessionID,
		adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: text},
		adapter.LLMMessage{Role: adapter.LLMRoleAssistant, Content: reply},
	)

	slog.Info("DuerOS skill replied", "session", sessionID, "model", s.Model(),
		"elapsed", elapsed.Round(time.Millisecond), "in_chars", len([]rune(text)),
		"out_chars", len([]rune(reply)), "spoken_chars", len([]rune(spoken)),
		"tokens", usageTokens(usage))

	if s.cfg.NotifyTelegram && s.notify != nil {
		s.notify(fmt.Sprintf("🎙️ 小度: %s\n🤖 %s: %s", text, s.Model(), spoken))
	}

	return adapter.NewDuerOSReply(spoken, skillRepromptText, nil)
}

func usageTokens(u *adapter.LLMUsage) int {
	if u == nil {
		return 0
	}
	return u.TotalTokens
}

// isExit reports whether the utterance is a farewell rather than a question.
// Comparison ignores punctuation and case, so "退出。" and "退出" both match.
func (s *DuerOSSkillServer) isExit(text string) bool {
	normalized := strings.ToLower(rePunctuation.ReplaceAllString(text, ""))
	if normalized == "" {
		return false
	}
	for _, kw := range s.cfg.ExitKeywordsOrDefault() {
		if strings.ToLower(rePunctuation.ReplaceAllString(kw, "")) == normalized {
			return true
		}
	}
	return false
}

// sanitizeSpeechText flattens a model reply into something worth reading aloud.
//
// The answer is spoken, not displayed: markdown syntax, links and bullet glyphs
// would be voiced literally, and an unbounded reply would hold the speaker for
// minutes. Truncation is by rune so a multi-byte character is never split.
func sanitizeSpeechText(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = reSpeechFence.ReplaceAllString(s, "")
	s = reSpeechLink.ReplaceAllString(s, "$1")
	s = reSpeechBullet.ReplaceAllString(s, "")
	s = reSpeechNoise.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = reSpeechSpaces.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if maxRunes > 0 {
		if r := []rune(s); len(r) > maxRunes {
			s = strings.TrimSpace(string(r[:maxRunes]))
		}
	}
	return s
}

// Handler builds the HTTP routes: the webhook itself plus a health endpoint for
// reverse proxies and uptime checks.
func (s *DuerOSSkillServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(s.cfg.PathOrDefault(), s.handleWebhook)
	mux.HandleFunc("/health", s.handleHealth)
	return mux
}

func (s *DuerOSSkillServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeSkillJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"provider": s.Provider(),
		"model":    s.Model(),
		"sessions": s.sessions.count(),
		"verify":   s.cfg.VerifySignatureOrDefault(),
	})
}

func (s *DuerOSSkillServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeSkillJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST required"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, skillMaxRequestBody))
	if err != nil {
		slog.Warn("DuerOS webhook read failed", "err", err)
		writeSkillJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot read body"})
		return
	}

	req, err := adapter.ParseDuerOSRequest(body)
	if err != nil {
		slog.Warn("DuerOS webhook rejected malformed request", "err", err, "remote", r.RemoteAddr)
		writeSkillJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if s.cfg.VerifySignatureOrDefault() {
		if err := s.verifier.Verify(r.Context(), body, r.Header, req); err != nil {
			slog.Warn("DuerOS webhook rejected unsigned request", "err", err, "remote", r.RemoteAddr)
			writeSkillJSON(w, http.StatusUnauthorized, map[string]string{"error": "signature verification failed"})
			return
		}
	}

	if !s.skillAllowed(req.ApplicationID()) {
		slog.Warn("DuerOS webhook rejected unknown skill", "skill", req.ApplicationID(), "remote", r.RemoteAddr)
		writeSkillJSON(w, http.StatusForbidden, map[string]string{"error": "skill not allowed"})
		return
	}

	resp := s.HandleRequest(r.Context(), req)
	writeSkillJSON(w, http.StatusOK, resp)
}

// skillAllowed enforces the optional applicationId allowlist.
func (s *DuerOSSkillServer) skillAllowed(applicationID string) bool {
	allowed := s.cfg.AllowedSkillIDs
	if len(allowed) == 0 {
		return true
	}
	id := strings.TrimSpace(applicationID)
	for _, a := range allowed {
		if strings.TrimSpace(a) == id {
			return true
		}
	}
	return false
}

func writeSkillJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Warn("DuerOS webhook response encode failed", "err", err)
	}
}

// ListenAndServe runs the webhook until ctx is cancelled, then shuts down
// gracefully so an in-flight LLM call can finish.
func (s *DuerOSSkillServer) ListenAndServe(ctx context.Context) error {
	addr := s.cfg.ListenOrDefault()
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: skillReadTimeout,
		ReadTimeout:       skillReadTimeout,
		WriteTimeout:      skillWriteTimeout,
		IdleTimeout:       skillIdleTimeout,
	}

	purgeDone := make(chan struct{})
	go func() {
		defer close(purgeDone)
		ticker := time.NewTicker(skillPurgeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := s.sessions.purge(); n > 0 {
					slog.Debug("DuerOS skill sessions purged", "removed", n, "remaining", s.sessions.count())
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("DuerOS skill webhook listening",
			"addr", addr, "path", s.cfg.PathOrDefault(), "model", s.Model(),
			"verify_signature", s.cfg.VerifySignatureOrDefault())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		<-purgeDone
		if err != nil {
			return fmt.Errorf("dueros skill server: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Deliberately not derived from ctx: it is already cancelled, so a child
		// would carry a zero deadline and abort the graceful drain immediately.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("DuerOS skill server shutdown error", "err", err)
		}
		<-errCh
		<-purgeDone
		return nil
	}
}

// runDuerOSSkillServer starts the webhook for the lifetime of the monitor loop.
func (r *Runner) runDuerOSSkillServer(ctx context.Context) {
	cfg := r.cfg.Xiaodu
	if cfg == nil || !cfg.Enabled || cfg.Skill == nil || !cfg.Skill.Enabled {
		return
	}
	srv, err := NewDuerOSSkillServer(cfg.Skill, r.notifyTelegramPlain)
	if err != nil {
		slog.Error("DuerOS skill server not started", "err", err)
		r.notifyTelegramPlain("❌ Xiaodu skill webhook failed to start: " + err.Error())
		return
	}
	r.skillServer = srv
	if err := srv.ListenAndServe(ctx); err != nil {
		slog.Error("DuerOS skill server stopped", "err", err)
		r.notifyTelegramPlain("❌ Xiaodu skill webhook stopped: " + err.Error())
	}
}
