package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLLMCompletionsURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://api.deepseek.com", "https://api.deepseek.com/chat/completions"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/chat/completions"},
		{"https://x.test/v1/chat/completions", "https://x.test/v1/chat/completions"},
		{"  https://y.test  ", "https://y.test/chat/completions"},
	}
	for _, tc := range tests {
		if got := llmCompletionsURL(tc.in); got != tc.want {
			t.Errorf("llmCompletionsURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewLLMChatClientValidation(t *testing.T) {
	if _, err := NewLLMChatClient(LLMChatOptions{Model: "m"}); err == nil {
		t.Error("expected error without base_url")
	}
	if _, err := NewLLMChatClient(LLMChatOptions{BaseURL: "https://x.test"}); err == nil {
		t.Error("expected error without model")
	}
	if _, err := NewLLMChatClient(LLMChatOptions{BaseURL: "https://x.test", Model: "m", Proxy: "://bad"}); err == nil {
		t.Error("expected error for a malformed proxy")
	}
}

// newLLMTestServer serves a canned chat completion and records what arrived.
func newLLMTestServer(t *testing.T, status int, body string) (*LLMChatClient, *httptest.Server, *[]byte, *http.Header) {
	t.Helper()
	var lastBody []byte
	var lastHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastBody, _ = io.ReadAll(r.Body)
		h := r.Header.Clone()
		lastHeader = h
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := NewLLMChatClient(LLMChatOptions{
		BaseURL: srv.URL,
		APIKey:  "sk-test",
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("NewLLMChatClient: %v", err)
	}
	return c, srv, &lastBody, &lastHeader
}

func TestLLMChatSuccess(t *testing.T) {
	resp := `{"choices":[{"message":{"role":"assistant","content":"  你好，我是助手  "},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`
	c, _, body, header := newLLMTestServer(t, http.StatusOK, resp)

	text, usage, err := c.Chat(context.Background(), []LLMMessage{
		{Role: LLMRoleSystem, Content: "be brief"},
		{Role: LLMRoleUser, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if text != "你好，我是助手" {
		t.Errorf("text = %q, want trimmed reply", text)
	}
	if usage == nil || usage.TotalTokens != 18 {
		t.Errorf("usage = %+v, want total 18", usage)
	}
	if got := (*header).Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if got := (*header).Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}

	var sent llmChatRequest
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("decode sent payload: %v", err)
	}
	if sent.Model != "test-model" {
		t.Errorf("model = %q", sent.Model)
	}
	if sent.Stream {
		t.Error("stream must stay off: the reply is spoken, so partial output is useless")
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != LLMRoleSystem {
		t.Errorf("messages = %+v", sent.Messages)
	}
}

func TestLLMChatErrors(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantInErr string
	}{
		{
			name:      "openai error envelope",
			status:    http.StatusUnauthorized,
			body:      `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`,
			wantInErr: "Incorrect API key",
		},
		{
			name:      "flat msg field",
			status:    http.StatusBadRequest,
			body:      `{"msg":"model not found"}`,
			wantInErr: "model not found",
		},
		{
			name:      "no choices",
			status:    http.StatusOK,
			body:      `{"choices":[]}`,
			wantInErr: "no choices",
		},
		{
			name:      "empty content",
			status:    http.StatusOK,
			body:      `{"choices":[{"message":{"content":"   "},"finish_reason":"length"}]}`,
			wantInErr: "empty reply",
		},
		{
			name:      "non-json 5xx",
			status:    http.StatusBadGateway,
			body:      `<html>upstream down</html>`,
			wantInErr: "502",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _, _ := newLLMTestServer(t, tc.status, tc.body)
			_, _, err := c.Chat(context.Background(), []LLMMessage{{Role: LLMRoleUser, Content: "hi"}})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q should contain %q", err, tc.wantInErr)
			}
		})
	}
}

func TestLLMChatRejectsEmptyInput(t *testing.T) {
	c, _, _, _ := newLLMTestServer(t, http.StatusOK, `{"choices":[{"message":{"content":"x"}}]}`)
	if _, _, err := c.Chat(context.Background(), nil); err == nil {
		t.Error("expected error for empty message list")
	}
}

func TestLLMChatPropagatesContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c, err := NewLLMChatClient(LLMChatOptions{BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatalf("NewLLMChatClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Chat(ctx, []LLMMessage{{Role: LLMRoleUser, Content: "hi"}}); err == nil {
		t.Error("expected the cancelled context to abort the call")
	}
}
