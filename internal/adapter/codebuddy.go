package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// CodeBuddy (Tencent IOA coding assistant) exposes an OpenAI-compatible
// chat endpoint, but with its own auth and gateway quirks. This client speaks
// that dialect so the DuerOS skill can answer through a CodeBuddy subscription
// instead of a paid API key.
//
// Deployment contract: this binary runs as a standalone systemd service on a
// small ARM box. There is NO Pi installation, no home-directory convention and
// no browser login flow available. Every credential must therefore come from
// the config file, explicitly. This client never derives a path from HOME,
// never reads another tool's credential store, and never writes a token file.
//
// Credential sources, in priority order:
//  1. APIKey                — an API key (ck_...) or a static bearer token;
//  2. TokenFile             — a file holding a bearer token (plain text or the
//     {"access":...,"refresh":...,"expires":...} shape);
//  3. RefreshTokenFile      — optional companion for refreshing a token from
//     TokenFile. A refreshed token lives in memory only:
//     a daemon should not mutate another process's files.
//
// Protocol notes:
//   - endpoint: {server}/v2/chat/completions, OpenAI request/response shape;
//   - auth: Bearer access token / API key;
//   - streaming is REQUIRED (a non-stream request gets code 11101);
//   - a pile of X-* headers identifying the (pretend) VSCode client;
//   - the gateway occasionally wraps an upstream transient failure as
//     HTTP 400 {"code":11133} — retrying the same request later succeeds;
//   - refresh: POST {server}/v2/plugin/auth/token/refresh with the refresh
//     token as Bearer, returns {code, data:{accessToken, refreshToken, expiresIn}}.
//
// Cost note: CodeBuddy model entries list cost 0 — they are covered by the
// subscription, which is the main reason to prefer this provider.
const (
	codebuddyDefaultServer  = "https://copilot.tencent.com"
	codebuddyChatPath       = "/v2/chat/completions"
	codebuddyRefreshPath    = "/v2/plugin/auth/token/refresh"
	codebuddyRefreshSkew    = 5 * time.Minute
	codebuddyRefreshTimeout = 10 * time.Second
	codebuddyAppVersion     = "4.9.29177644"
	codebuddyIDEVersion     = "1.119.0"
	codebuddyDomainCN       = "www.codebuddy.cn"
	// Gateway-internal error code meaning "upstream hiccup, retry me".
	codebuddyTransientCode = 11133
)

// codebuddyRetryDelays backs off the transient-400 retry: 1s, 4s, 10s, 25s.
// Four retries, ≤40s total wait.
var codebuddyRetryDelays = []time.Duration{time.Second, 4 * time.Second, 10 * time.Second, 25 * time.Second}

// codebuddyTokenFile mirrors a bearer-token file with optional rotation data.
// Plain-text token files are also accepted (see loadTokenFile).
type codebuddyTokenFile struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires"` // epoch milliseconds
}

// CodeBuddyOptions configures a CodeBuddy client. All credential fields are
// supplied explicitly by the caller from config; none are inferred.
type CodeBuddyOptions struct {
	// Server is the API root; empty means https://copilot.tencent.com.
	Server string
	// APIKey is a bearer token from config (ck_... API key or a static access
	// token). When set, no token file is read and no refresh is attempted.
	APIKey string
	// TokenFile is an explicit path to a file containing a bearer token.
	// Empty means "not configured" — there is no implicit default path.
	TokenFile string
	// RefreshTokenFile optionally supplies a separate refresh token file.
	// When empty, a refresh token embedded in TokenFile is used instead.
	RefreshTokenFile string
	Proxy            string
	Timeout          time.Duration
	MaxTokens        int
	Temperature      *float64
}

// CodeBuddyClient chats through CodeBuddy's OpenAI-compatible gateway.
type CodeBuddyClient struct {
	server      string
	apiKey      string
	maxTokens   int
	temperature *float64
	client      *http.Client

	mu      sync.Mutex
	access  string
	refresh string
	expire  time.Time
}

// NewCodeBuddyClient builds a client from explicitly configured credentials.
// A missing credential is not a construction error (the caller may only be
// listing models or printing usage); Chat reports it with an actionable message.
func NewCodeBuddyClient(opts CodeBuddyOptions) (*CodeBuddyClient, error) {
	server := strings.TrimSuffix(strings.TrimSpace(opts.Server), "/")
	if server == "" {
		server = codebuddyDefaultServer
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	hc, err := httpClient(opts.Proxy, timeout)
	if err != nil {
		return nil, err
	}
	c := &CodeBuddyClient{
		server:      server,
		apiKey:      strings.TrimSpace(opts.APIKey),
		maxTokens:   opts.MaxTokens,
		temperature: opts.Temperature,
		client:      hc,
	}
	if c.apiKey != "" {
		return c, nil
	}

	c.mu.Lock()
	access, embeddedRefresh, expires, err := loadCodeBuddyToken(opts.TokenFile)
	c.access, c.refresh, c.expire = access, embeddedRefresh, expires
	c.mu.Unlock()
	if err != nil {
		// Remember nothing; the error is reported on first use so the daemon
		// still starts and /health stays useful when credentials are absent.
		slog.Warn("CodeBuddy credentials not usable", "err", err)
	}
	if strings.TrimSpace(opts.RefreshTokenFile) != "" {
		if tok, err := readCodeBuddyToken(opts.RefreshTokenFile); err != nil {
			slog.Warn("CodeBuddy refresh token file not readable", "err", err)
		} else {
			c.mu.Lock()
			c.refresh = tok
			c.mu.Unlock()
		}
	}
	return c, nil
}

// readCodeBuddyToken reads a token file: either a plain-text bearer token or a
// JSON object with an "access" field. Whitespace is trimmed either way.
func readCodeBuddyToken(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("no token file configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file %s: %w", path, err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	if strings.HasPrefix(s, "{") {
		var f codebuddyTokenFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return "", fmt.Errorf("decode token file %s: %w", path, err)
		}
		if strings.TrimSpace(f.Access) == "" {
			return "", fmt.Errorf("token file %s has no access token", path)
		}
		return strings.TrimSpace(f.Access), nil
	}
	return s, nil
}

// loadCodeBuddyToken resolves the access token, optional embedded refresh
// token and expiry from an explicitly configured token file.
func loadCodeBuddyToken(path string) (access, refresh string, expire time.Time, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", time.Time{}, fmt.Errorf("no token file configured")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("read token file %s: %w", path, err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "", "", time.Time{}, fmt.Errorf("token file %s is empty", path)
	}
	if !strings.HasPrefix(s, "{") {
		return s, "", time.Time{}, nil // plain text, no expiry known
	}
	var f codebuddyTokenFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode token file %s: %w", path, err)
	}
	if strings.TrimSpace(f.Access) == "" {
		return "", "", time.Time{}, fmt.Errorf("token file %s has no access token", path)
	}
	if f.Expires > 0 {
		expire = time.UnixMilli(f.Expires)
	}
	return strings.TrimSpace(f.Access), strings.TrimSpace(f.Refresh), expire, nil
}

// token returns the current bearer token, refreshing ahead of expiry when a
// refresh token was configured. Refreshes are kept in memory for this process
// only: a systemd daemon must never rewrite credentials on disk.
func (c *CodeBuddyClient) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	access, refresh, expire := c.access, c.refresh, c.expire
	c.mu.Unlock()

	if c.apiKey != "" {
		return c.apiKey, nil
	}
	if access == "" {
		return "", fmt.Errorf("codebuddy: no credential configured — set xiaodu.skill.llm_api_key (API key or static bearer token), or xiaodu.skill.codebuddy_token_file")
	}
	// Refresh with skew so a request never races the expiry. A refresh token
	// can come from the token file itself or from a dedicated file.
	if refresh != "" && !expire.IsZero() && time.Until(expire) < codebuddyRefreshSkew {
		if err := c.doRefresh(ctx, refresh); err != nil {
			slog.Warn("CodeBuddy token refresh failed; trying the cached token", "err", err)
			return access, nil
		}
		c.mu.Lock()
		access = c.access
		c.mu.Unlock()
	}
	return access, nil
}

// doRefresh exchanges the refresh token and updates in-memory state only.
func (c *CodeBuddyClient) doRefresh(ctx context.Context, refreshToken string) error {
	ctx, cancel := context.WithTimeout(ctx, codebuddyRefreshTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+codebuddyRefreshPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+refreshToken)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int64  `json:"expiresIn"` // seconds
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("decode refresh response: %w", err)
	}
	if parsed.Code != 0 || strings.TrimSpace(parsed.Data.AccessToken) == "" {
		return fmt.Errorf("refresh failed (code=%d)", parsed.Code)
	}
	refresh := parsed.Data.RefreshToken
	if strings.TrimSpace(refresh) == "" {
		refresh = refreshToken // some servers omit it; keep the old one
	}
	expiresAt := time.Now().Add(time.Duration(parsed.Data.ExpiresIn) * time.Second)
	c.mu.Lock()
	c.access = parsed.Data.AccessToken
	c.refresh = refresh
	c.expire = expiresAt
	c.mu.Unlock()
	// Deliberately no file write. This daemon does not own any credential store;
	// a rotated token stays in memory until the process restarts. The operator
	// updates the configured token/key when it expires.
	slog.Info("CodeBuddy token refreshed in memory", "expires_at", expiresAt.Format(time.RFC3339))
	return nil
}

// Model is a CodeBuddy model id plus its display metadata.
type CodeBuddyModel struct {
	ID        string
	Name      string
	Reasoning bool
	MaxTokens int
}

// ListModels returns the craft-agent model list from GET /v3/config.
func (c *CodeBuddyClient) ListModels(ctx context.Context) ([]CodeBuddyModel, error) {
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.server+"/v3/config", nil)
	if err != nil {
		return nil, err
	}
	c.setBaseHeaders(req)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("config HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var parsed struct {
		Code int `json:"code"`
		Data struct {
			// agents[].models holds model *ids* only; the display metadata lives in
			// the top-level models array.
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
			Models []struct {
				ID              string `json:"id"`
				Name            string `json:"name"`
				OnlyReasoning   bool   `json:"onlyReasoning"`
				MaxOutputTokens int    `json:"maxOutputTokens"`
				// reasoning is a config object, not a bool; keep it raw so an
				// unexpected shape never breaks model listing.
				Reasoning json.RawMessage `json:"reasoning"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode config response: %w", err)
	}
	meta := make(map[string]struct {
		Name      string
		Reasoning bool
		MaxTokens int
	}, len(parsed.Data.Models))
	for _, m := range parsed.Data.Models {
		meta[m.ID] = struct {
			Name      string
			Reasoning bool
			MaxTokens int
		}{Name: m.Name, Reasoning: m.OnlyReasoning, MaxTokens: m.MaxOutputTokens}
	}

	var craft []CodeBuddyModel
	for _, a := range parsed.Data.Agents {
		if a.Name != "craft" {
			continue
		}
		for _, id := range a.Models {
			info := meta[id]
			name := info.Name
			if name == "" {
				name = id
			}
			craft = append(craft, CodeBuddyModel{
				ID: id, Name: name, Reasoning: info.Reasoning, MaxTokens: info.MaxTokens,
			})
		}
	}
	if len(craft) == 0 {
		return nil, fmt.Errorf("no craft models in config response")
	}
	return craft, nil
}

// setBaseHeaders adds the client-identification headers the gateway expects.
// CodeBuddy fronts a VSCode plugin; the gateway appears to key on these, so we
// present the same shape the official client sends.
func (c *CodeBuddyClient) setBaseHeaders(req *http.Request) {
	h := req.Header
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Content-Type", "application/json")
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("X-Agent-Intent", "craft")
	h.Set("X-IDE-Type", "VSCode")
	h.Set("X-IDE-Name", "VSCode")
	h.Set("X-IDE-Version", codebuddyIDEVersion)
	h.Set("X-Product-Version", codebuddyAppVersion)
	h.Set("X-Env-ID", "production")
	h.Set("X-Domain", codebuddyDomainCN)
	h.Set("X-Product", "SaaS")
	h.Set("User-Agent", "VSCode/"+codebuddyIDEVersion+" CodeBuddy/"+codebuddyAppVersion)
}

// codebuddyStreamChunk is one SSE event's payload. Reasoning models put their
// chain-of-thought in reasoning_content; that must NEVER reach the speaker, so
// we deliberately only accumulate delta.content.
type codebuddyStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *LLMUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
	Code    int    `json:"code"`
	Message string `json:"msg"`
}

// readCodeBuddyStream consumes a text/event-stream body and concatenates the
// spoken content. It stops at [DONE] or a terminal finish_reason, and keeps the
// final usage object. A body larger than 8 MiB is treated as a runaway stream.
func readCodeBuddyStream(r io.Reader) (string, *LLMUsage, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1<<20) // a single delta can be long
	var sb strings.Builder
	var usage *LLMUsage
	var finish string
	var total int

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		total += len(line)
		if total > 8<<20 {
			return "", nil, fmt.Errorf("codebuddy stream exceeded 8 MiB")
		}
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue // SSE comments, event:, id:, blank separators
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if payload == "[DONE]" {
				break
			}
			continue
		}
		var chunk codebuddyStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A malformed event is not fatal as long as other events are sound;
			// skipping keeps a single bad frame from losing the whole answer.
			slog.Debug("CodeBuddy skipping malformed SSE frame", "err", err)
			continue
		}
		if chunk.Error != nil {
			return "", nil, fmt.Errorf("codebuddy stream error: %s (code=%v)",
				chunk.Error.Message, chunk.Error.Code)
		}
		if chunk.Code != 0 || chunk.Message != "" {
			if len(chunk.Choices) == 0 {
				return "", nil, fmt.Errorf("codebuddy stream error code=%d: %s", chunk.Code, chunk.Message)
			}
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, ch := range chunk.Choices {
			sb.WriteString(ch.Delta.Content)
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("read codebuddy stream: %w", err)
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", usage, fmt.Errorf("codebuddy stream produced no content (finish_reason=%s)", finish)
	}
	return text, usage, nil
}

// Chat sends the conversation and returns the assistant reply.
//
// The gateway REQUIRES streaming (a non-stream request returns code 11101), so
// we ask for SSE and aggregate the deltas: the skill needs a complete sentence
// before the speaker can read it aloud anyway.
//
// It also retries a transient gateway 400 (code 11133) with backoff, and
// retries once through a token refresh on 401/403.
func (c *CodeBuddyClient) Chat(ctx context.Context, model string, messages []LLMMessage) (string, *LLMUsage, error) {
	if strings.TrimSpace(model) == "" {
		return "", nil, fmt.Errorf("codebuddy: model is required")
	}
	payload := llmChatRequest{
		Model:       model,
		Messages:    messages,
		MaxTokens:   c.maxTokens,
		Temperature: c.temperature,
		Stream:      true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}

	for attempt := 0; ; attempt++ {
		token, err := c.token(ctx)
		if err != nil {
			return "", nil, err
		}
		resp, err := c.post(ctx, token, body)
		if err != nil {
			return "", nil, err
		}
		status := resp.StatusCode

		// Non-2xx replies are plain JSON (never SSE), so they can be read whole.
		if status >= 400 {
			out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()

			if (status == http.StatusUnauthorized || status == http.StatusForbidden) && c.apiKey == "" {
				c.mu.Lock()
				refresh := c.refresh
				c.mu.Unlock()
				if refresh != "" {
					if err := c.doRefresh(ctx, refresh); err == nil {
						continue
					}
				}
				return "", nil, fmt.Errorf("codebuddy: unauthorized — the configured token/key is invalid or expired; update xiaodu.skill.llm_api_key or codebuddy_token_file: %s", truncate(string(out), 200))
			}

			if status == http.StatusBadRequest {
				var probe struct {
					Code int `json:"code"`
				}
				if json.Unmarshal(out, &probe) == nil && probe.Code == codebuddyTransientCode {
					if attempt < len(codebuddyRetryDelays) {
						delay := codebuddyRetryDelays[attempt]
						slog.Warn("CodeBuddy transient 400 (11133), retrying",
							"attempt", attempt+1, "delay", delay.String())
						select {
						case <-time.After(delay):
						case <-ctx.Done():
							return "", nil, ctx.Err()
						}
						continue
					}
					return "", nil, fmt.Errorf("codebuddy: transient 400 persisted after %d retries", len(codebuddyRetryDelays))
				}
			}
			return "", nil, fmt.Errorf("codebuddy HTTP %d: %s", status, truncate(string(out), 300))
		}

		text, usage, streamErr := readCodeBuddyStream(resp.Body)
		resp.Body.Close()
		if streamErr != nil {
			return "", nil, streamErr
		}
		return text, usage, nil
	}
}

// post performs one chat request and returns the response with its body still
// open. The caller must close it. Deferring body handling to the caller lets a
// successful SSE stream be consumed incrementally instead of buffered.
func (c *CodeBuddyClient) post(ctx context.Context, token string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server+codebuddyChatPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setBaseHeaders(req)
	req.Header.Set("Accept", "text/event-stream, application/json, text/plain, */*")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Model-ID", jsonSafeModelID(body))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy request failed: %w", err)
	}
	return resp, nil
}

// jsonSafeModelID extracts the model field for the X-Model-ID header.
func jsonSafeModelID(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	return probe.Model
}

// Model returns nothing meaningful here; CodeBuddy binds the model per call.
func (c *CodeBuddyClient) Server() string { return c.server }
