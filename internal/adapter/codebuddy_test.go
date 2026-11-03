package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// codebuddySSE builds a stream the way the real gateway does: a role frame,
// several content deltas, a terminal frame carrying usage, then [DONE].
func codebuddySSE(deltas ...string) string {
	var b strings.Builder
	b.WriteString("data: " + `{"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n")
	for _, d := range deltas {
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]string{"content": d, "reasoning_content": "internal thought"},
			}},
		})
		b.WriteString("data: " + string(payload) + "\n\n")
	}
	b.WriteString("data: " + `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// newCodeBuddyTestClient points a client at a test server with an explicit,
// temp-dir token file. Nothing is read from HOME or any other tool's store.
// It returns the token file path so tests can assert the daemon never writes it.
func newCodeBuddyTestClient(t *testing.T, handler http.HandlerFunc) (*CodeBuddyClient, *httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token.json")
	creds := codebuddyTokenFile{
		Access:  "access-old",
		Refresh: "refresh-old",
		Expires: time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	raw, _ := json.Marshal(creds)
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: srv.URL, TokenFile: tokenFile})
	if err != nil {
		t.Fatalf("NewCodeBuddyClient: %v", err)
	}
	return c, srv, tokenFile
}

func TestCodeBuddyChatAggregatesStream(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody = readAll(t, r)
		gotHeaders = r.Header.Clone()
		if r.URL.Path != codebuddyChatPath {
			t.Errorf("path = %q, want %q", r.URL.Path, codebuddyChatPath)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, codebuddySSE("你好", "，", "我是助手。"))
	})

	text, usage, err := c.Chat(context.Background(), "deepseek-v4.1-flash", []LLMMessage{
		{Role: LLMRoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// reasoning_content must never leak into the spoken reply.
	if text != "你好，我是助手。" {
		t.Errorf("text = %q, want only delta.content concatenated", text)
	}
	if usage == nil || usage.TotalTokens != 15 {
		t.Errorf("usage = %+v, want total 15", usage)
	}

	if got := gotHeaders.Get("Accept"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("Accept = %q, must request SSE (non-stream is rejected by the gateway)", got)
	}
	if got := gotHeaders.Get("X-Model-ID"); got != "deepseek-v4.1-flash" {
		t.Errorf("X-Model-ID = %q", got)
	}
	if got := gotHeaders.Get("X-Agent-Intent"); got != "craft" {
		t.Errorf("X-Agent-Intent = %q", got)
	}
	if got := gotHeaders.Get("Authorization"); got != "Bearer access-old" {
		t.Errorf("Authorization = %q", got)
	}

	var sent llmChatRequest
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	// The regression this guards: CodeBuddy returns 11101 for stream:false.
	if !sent.Stream {
		t.Error("stream must be true; the gateway rejects non-stream requests")
	}
	if sent.Model != "deepseek-v4.1-flash" {
		t.Errorf("model = %q", sent.Model)
	}
}

func TestReadCodeBuddyStream(t *testing.T) {
	tests := []struct {
		name      string
		stream    string
		want      string
		wantUsage bool
		wantErr   string
	}{
		{
			name:      "normal stream",
			stream:    codebuddySSE("甲", "乙"),
			want:      "甲乙",
			wantUsage: true,
		},
		{
			name: "ignores comments and blank lines",
			stream: ": ping\n\nid: 1\n\nevent: message\n\ndata: " +
				`{"choices":[{"delta":{"content":"ok"}}]}` + "\n\ndata: [DONE]\n",
			want: "ok",
		},
		{
			name:   "skips a malformed frame",
			stream: "data: {oops\n\ndata: " + `{"choices":[{"delta":{"content":"ok"}}]}` + "\n\ndata: [DONE]\n",
			want:   "ok",
		},
		{
			name:   "ignores reasoning content",
			stream: "data: " + `{"choices":[{"delta":{"reasoning_content":"先想想","content":"答案"}}]}` + "\n\ndata: [DONE]\n",
			want:   "答案",
		},
		{
			name:    "no content",
			stream:  "data: " + `{"choices":[{"delta":{"content":""},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n",
			wantErr: "no content",
		},
		{
			name:    "stream-level error",
			stream:  "data: " + `{"code":11133,"msg":"transient"}` + "\n\n",
			wantErr: "11133",
		},
		{
			name:    "error envelope",
			stream:  "data: " + `{"error":{"message":"quota exceeded","code":42}}` + "\n\n",
			wantErr: "quota exceeded",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, usage, err := readCodeBuddyStream(strings.NewReader(tc.stream))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error = %q, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if text != tc.want {
				t.Errorf("text = %q, want %q", text, tc.want)
			}
			if (usage != nil) != tc.wantUsage {
				t.Errorf("usage = %+v, wantUsage %v", usage, tc.wantUsage)
			}
		})
	}
}

func TestCodeBuddyChatErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantInErr string
	}{
		{
			name:      "non-stream rejection is surfaced verbatim",
			status:    http.StatusBadRequest,
			body:      `{"code":11101,"msg":"Non-stream chat request is currently not supported"}`,
			wantInErr: "11101",
		},
		{
			name:      "generic 500",
			status:    http.StatusInternalServerError,
			body:      `{"msg":"upstream down"}`,
			wantInErr: "500",
		},
		{
			name:      "other 400 is not retried as transient",
			status:    http.StatusBadRequest,
			body:      `{"code":12345,"msg":"bad model"}`,
			wantInErr: "12345",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error = %q, want %q", err, tc.wantInErr)
			}
		})
	}
}

func TestCodeBuddyTransient400Retry(t *testing.T) {
	// The real gateway wraps an upstream hiccup as 400 {"code":11133}.
	// Retrying the identical request succeeds. Keep delays tiny in tests.
	orig := codebuddyRetryDelays
	codebuddyRetryDelays = []time.Duration{0, 0, 0, 0}
	t.Cleanup(func() { codebuddyRetryDelays = orig })

	var mu sync.Mutex
	calls := 0
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"code":11133,"msg":"transient"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, codebuddySSE("重试成功"))
	})

	text, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if text != "重试成功" {
		t.Errorf("text = %q", text)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 2 failures + 1 success", calls)
	}
}

func TestCodeBuddyTransient400ExhaustsRetries(t *testing.T) {
	orig := codebuddyRetryDelays
	codebuddyRetryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { codebuddyRetryDelays = orig })

	calls := 0
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"code":11133}`)
	})

	_, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "persisted") {
		t.Errorf("error = %v, want exhaustion", err)
	}
	// 1 initial + 2 retries.
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestCodeBuddy401RefreshesAndReplays(t *testing.T) {
	var mu sync.Mutex
	chatCalls := 0
	var refreshAuth string
	c, srv, tokenFile := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == codebuddyRefreshPath {
			refreshAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"accessToken":"access-new",`+
				`"refreshToken":"refresh-new","expiresIn":3600}}`)
			return
		}
		mu.Lock()
		chatCalls++
		n := chatCalls
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer access-new" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"msg":"expired"}`)
			return
		}
		if n < 2 {
			t.Errorf("success on call %d before a 401", n)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, codebuddySSE("刷新后成功"))
	})
	_ = srv

	// Deployment contract: a systemd daemon must NOT rewrite credential files.
	// The refreshed token is in-memory only; the on-disk file stays byte-identical
	// so an operator-owned secret is never mutated behind their back.
	before, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read token file before refresh: %v", err)
	}
	text, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if text != "刷新后成功" {
		t.Errorf("text = %q", text)
	}
	if refreshAuth != "Bearer refresh-old" {
		t.Errorf("refresh Authorization = %q, want the refresh token", refreshAuth)
	}
	if chatCalls != 2 {
		t.Errorf("chat calls = %d, want 1 failure + 1 replay", chatCalls)
	}

	after, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("read token file after refresh: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("token file was rewritten by the daemon:\nbefore: %s\nafter:  %s", before, after)
	}
	// ...but the in-memory state DID rotate, so this process keeps working.
	tok, err := c.token(context.Background())
	if err != nil {
		t.Fatalf("token after refresh: %v", err)
	}
	if tok != "access-new" {
		t.Errorf("in-memory token = %q, want the refreshed token", tok)
	}
}

func TestCodeBuddyTokenPreemptiveRefresh(t *testing.T) {
	// A token inside the 5-minute skew window is refreshed *before* the chat.
	dir := t.TempDir()
	snap := filepath.Join(dir, "auth.json")
	creds := codebuddyTokenFile{
		Access:  "expiring",
		Refresh: "refresh-soon",
		Expires: time.Now().Add(2 * time.Minute).UnixMilli(),
	}
	raw, _ := json.Marshal(creds)
	if err := os.WriteFile(snap, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	chatAuth := ""
	refreshed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == codebuddyRefreshPath {
			mu.Lock()
			refreshed = true
			mu.Unlock()
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"accessToken":"fresh","expiresIn":3600}}`)
			return
		}
		chatAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, codebuddySSE("ok"))
	}))
	t.Cleanup(srv.Close)

	c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: srv.URL, TokenFile: snap})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !refreshed {
		t.Error("token inside the skew window was not refreshed")
	}
	if chatAuth != "Bearer fresh" {
		t.Errorf("chat Authorization = %q, want the refreshed token", chatAuth)
	}
}

func TestCodeBuddyAPIKeyTakesPriority(t *testing.T) {
	c, err := NewCodeBuddyClient(CodeBuddyOptions{
		Server:    "https://example.invalid",
		APIKey:    "ck_test",
		TokenFile: filepath.Join(t.TempDir(), "does-not-exist.json"),
	})
	if err != nil {
		t.Fatalf("NewCodeBuddyClient: %v", err)
	}
	token, err := c.token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token != "ck_test" {
		t.Errorf("token = %q, want the api key", token)
	}
}

func TestCodeBuddyMissingCredentials(t *testing.T) {
	c, err := NewCodeBuddyClient(CodeBuddyOptions{
		Server:    "https://example.invalid",
		TokenFile: filepath.Join(t.TempDir(), "absent.json"),
	})
	if err != nil {
		t.Fatalf("NewCodeBuddyClient: %v", err)
	}
	_, err = c.token(context.Background())
	if err == nil {
		t.Fatal("expected an error without a configured credential")
	}
	// The message must point at config keys, never at a tool-specific login flow
	// that does not exist on the deployment target.
	for _, want := range []string{"llm_api_key", "codebuddy_token_file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	for _, forbidden := range []string{"/login", ".pi/agent", "pi "} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("error %q must not reference %q (no Pi on the deployment host)", err, forbidden)
		}
	}
}

func TestCodeBuddyChatRequiresModel(t *testing.T) {
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, _, err := c.Chat(context.Background(), "  ", nil); err == nil {
		t.Error("expected an error for an empty model")
	}
}

func TestCodeBuddyListModels(t *testing.T) {
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// Mirrors the real shape: agents[].models are id strings, metadata is
		// in the top-level models array, and reasoning is an OBJECT.
		_, _ = fmt.Fprint(w, `{"code":0,"data":{
			"agents":[{"name":"ask","models":["hidden"]},
			          {"name":"craft","models":["auto","deepseek-v4.1-flash"]}],
			"models":[
			  {"id":"auto","name":"Auto","onlyReasoning":true,"reasoning":{"effort":"high"},"maxOutputTokens":32768},
			  {"id":"deepseek-v4.1-flash","name":"Deepseek-V4.1-Flash","onlyReasoning":false,"maxOutputTokens":393216}
			]}}`)
	})

	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d, want only craft models", len(models))
	}
	if models[0].ID != "auto" || models[0].Name != "Auto" || !models[0].Reasoning {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[1].ID != "deepseek-v4.1-flash" || models[1].Reasoning {
		t.Errorf("models[1] = %+v", models[1])
	}
	if models[1].MaxTokens != 393216 {
		t.Errorf("MaxTokens = %d", models[1].MaxTokens)
	}
}

func TestCodeBuddyListModelsNoCraft(t *testing.T) {
	c, _, _ := newCodeBuddyTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"code":0,"data":{"agents":[{"name":"ask","models":["x"]}],"models":[]}}`)
	})
	if _, err := c.ListModels(context.Background()); err == nil {
		t.Error("expected an error when craft is missing")
	}
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return raw
}

// TestCodeBuddyNoImplicitCredentialPath is the deployment regression: on r2s
// there is no Pi installation, so the client must never derive a path from HOME
// or silently read another tool's credential store. With nothing configured it
// simply has no token.
func TestCodeBuddyNoImplicitCredentialPath(t *testing.T) {
	dir := t.TempDir()
	// Plant a Pi-shaped file under a fake HOME. It must NOT be picked up.
	plant := filepath.Join(dir, ".pi", "agent", "codebuddy-auth.json")
	if err := os.MkdirAll(filepath.Dir(plant), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plant, []byte(`{"access":"leaked-from-another-tool"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)

	c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: "https://example.invalid"})
	if err != nil {
		t.Fatalf("NewCodeBuddyClient: %v", err)
	}
	if _, err := c.token(context.Background()); err == nil {
		t.Fatal("a credential was implicitly discovered; the daemon must be config-driven only")
	}
	c.mu.Lock()
	got := c.access
	c.mu.Unlock()
	if got != "" {
		t.Errorf("access = %q, must stay empty without explicit config", got)
	}
}

func TestCodeBuddyExplicitTokenFile(t *testing.T) {
	t.Run("plain text token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "token.txt")
		if err := os.WriteFile(path, []byte("  ck_plain_token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: "https://example.invalid", TokenFile: path})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := c.token(context.Background())
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "ck_plain_token" {
			t.Errorf("token = %q, want trimmed plain text", tok)
		}
	})

	t.Run("json token with embedded refresh", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "token.json")
		raw, _ := json.Marshal(codebuddyTokenFile{
			Access:  "access-from-file",
			Refresh: "refresh-from-file",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: "https://example.invalid", TokenFile: path})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := c.token(context.Background())
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "access-from-file" {
			t.Errorf("token = %q", tok)
		}
		c.mu.Lock()
		refresh := c.refresh
		c.mu.Unlock()
		if refresh != "refresh-from-file" {
			t.Errorf("refresh = %q, want the embedded refresh token", refresh)
		}
	})

	t.Run("separate refresh token file overrides embedded", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "token.json")
		raw, _ := json.Marshal(codebuddyTokenFile{Access: "access", Refresh: "embedded", Expires: time.Now().Add(time.Hour).UnixMilli()})
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		refreshPath := filepath.Join(dir, "refresh.txt")
		if err := os.WriteFile(refreshPath, []byte("separate-refresh\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := NewCodeBuddyClient(CodeBuddyOptions{
			Server: "https://example.invalid", TokenFile: path, RefreshTokenFile: refreshPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		refresh := c.refresh
		c.mu.Unlock()
		if refresh != "separate-refresh" {
			t.Errorf("refresh = %q, want the dedicated file to win", refresh)
		}
	})

	t.Run("empty token file is reported", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.txt")
		if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: "https://example.invalid", TokenFile: path})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.token(context.Background()); err == nil {
			t.Error("expected an error for an empty token file")
		}
	})
}

// TestCodeBuddyRefreshNeverWritesFiles complements the 401 test: even after a
// successful rotation, no file is created or modified in the config directory.
func TestCodeBuddyRefreshNeverWritesFiles(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token.json")
	raw, _ := json.Marshal(codebuddyTokenFile{
		Access:  "expiring",
		Refresh: "refresh-soon",
		Expires: time.Now().Add(time.Minute).UnixMilli(),
	})
	if err := os.WriteFile(tokenPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == codebuddyRefreshPath {
			_, _ = fmt.Fprint(w, `{"code":0,"data":{"accessToken":"rotated","refreshToken":"rotated-refresh","expiresIn":3600}}`)
			return
		}
		_, _ = fmt.Fprint(w, codebuddySSE("ok"))
	}))
	t.Cleanup(srv.Close)

	c, err := NewCodeBuddyClient(CodeBuddyOptions{Server: srv.URL, TokenFile: tokenPath})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Chat(context.Background(), "m", []LLMMessage{{Role: LLMRoleUser, Content: "hi"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	after, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Errorf("token file changed after refresh:\nbefore: %s\nafter:  %s", raw, after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("config dir gained files: %v", entries)
	}
}
