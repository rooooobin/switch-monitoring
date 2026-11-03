package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
)

// newFakeLLM serves canned replies and records every request so tests can assert
// what conversation history was actually sent.
type fakeLLM struct {
	srv      *httptest.Server
	replies  []string
	requests [][]byte
	failWith int
}

func newFakeLLM(t *testing.T, replies ...string) *fakeLLM {
	t.Helper()
	f := &fakeLLM{replies: replies}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, body)
		w.Header().Set("Content-Type", "application/json")
		if f.failWith != 0 {
			w.WriteHeader(f.failWith)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream boom"}}`))
			return
		}
		idx := len(f.requests) - 1
		if idx >= len(f.replies) {
			idx = len(f.replies) - 1
		}
		payload, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"message":       map[string]string{"role": "assistant", "content": f.replies[idx]},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"total_tokens": 42},
		})
		_, _ = w.Write(payload)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// lastMessages decodes the messages of the most recent LLM call.
func (f *fakeLLM) lastMessages(t *testing.T) []adapter.LLMMessage {
	t.Helper()
	if len(f.requests) == 0 {
		t.Fatal("the LLM was never called")
	}
	var req struct {
		Messages []adapter.LLMMessage `json:"messages"`
	}
	if err := json.Unmarshal(f.requests[len(f.requests)-1], &req); err != nil {
		t.Fatalf("decode llm request: %v", err)
	}
	return req.Messages
}

func newSkillTestConfig(t *testing.T, llm *fakeLLM) *config.XiaoduSkillConfig {
	t.Helper()
	verify := false // tests drive the handler directly; signing is covered in adapter tests
	maxReply := 100
	return &config.XiaoduSkillConfig{
		Enabled:           true,
		Listen:            "127.0.0.1:0",
		Path:              "/dueros",
		VerifySignature:   &verify,
		LLMBaseURL:        llm.srv.URL,
		LLMAPIKey:         "sk-test",
		LLMModel:          "test-model",
		MaxReplyChars:     maxReply,
		HistoryMessages:   12,
		SessionTTLSeconds: 60,
	}
}

func newSkillTestServer(t *testing.T, llm *fakeLLM) *DuerOSSkillServer {
	t.Helper()
	srv, err := NewDuerOSSkillServer(newSkillTestConfig(t, llm), nil)
	if err != nil {
		t.Fatalf("NewDuerOSSkillServer: %v", err)
	}
	return srv
}

func skillIntentRequest(sessionID, text string) *adapter.DuerOSRequest {
	req := &adapter.DuerOSRequest{Version: "2.0"}
	req.Session.SessionID = sessionID
	req.Request.Type = adapter.DuerOSTypeIntent
	req.Request.Query = &adapter.DuerOSQuery{Type: "TEXT", Original: text}
	req.Request.Intents = []adapter.DuerOSIntent{{
		Name:  "ai.dueros.common.default_intent",
		Slots: map[string]adapter.DuerOSSlot{"query": {Name: "query", Value: text}},
	}}
	return req
}

func TestNewDuerOSSkillServerValidation(t *testing.T) {
	if _, err := NewDuerOSSkillServer(nil, nil); err == nil {
		t.Error("expected an error for a nil config")
	}
	if _, err := NewDuerOSSkillServer(&config.XiaoduSkillConfig{LLMModel: "m"}, nil); err == nil {
		t.Error("expected an error without llm_base_url")
	}
	if _, err := NewDuerOSSkillServer(&config.XiaoduSkillConfig{LLMBaseURL: "https://x.test"}, nil); err == nil {
		t.Error("expected an error without llm_model")
	}
}

func TestSanitizeSpeechText(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxRunes int
		want     string
	}{
		{"plain text unchanged", "今天深圳多云。", 100, "今天深圳多云。"},
		{"strips heading and emphasis", "## 标题\n**重点**内容", 100, "标题 重点内容"},
		{"strips bullets", "- 第一条\n- 第二条", 100, "第一条 第二条"},
		{"strips numbered bullets", "1. 甲\n2) 乙", 100, "甲 乙"},
		{"keeps link text drops url", "看 [文档](https://example.com/a) 就行", 100, "看 文档 就行"},
		{"strips code fences", "```go\nfmt.Println()\n```", 100, "fmt.Println()"},
		{"collapses whitespace", "a   b\n\n\nc", 100, "a b c"},
		{"truncates on a rune boundary", "一二三四五六", 3, "一二三"},
		{"zero max means no truncation", "一二三", 0, "一二三"},
		{"empty input", "   ", 100, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeSpeechText(tc.in, tc.maxRunes); got != tc.want {
				t.Errorf("sanitizeSpeechText(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
			}
		})
	}
}

// TestSanitizeSpeechTextNeverSplitsARune is the regression that matters for TTS:
// a byte-wise cut would emit invalid UTF-8 and the speaker would garble or drop
// the tail of every long answer.
func TestSanitizeSpeechTextNeverSplitsARune(t *testing.T) {
	in := strings.Repeat("测", 50)
	for maxRunes := 1; maxRunes < 10; maxRunes++ {
		got := sanitizeSpeechText(in, maxRunes)
		if len([]rune(got)) != maxRunes {
			t.Fatalf("maxRunes=%d produced %d runes", maxRunes, len([]rune(got)))
		}
		if !utf8.Valid([]byte(got)) {
			t.Fatalf("maxRunes=%d produced invalid UTF-8", maxRunes)
		}
	}
}

func TestSkillSessionStore(t *testing.T) {
	now := time.Now()
	store := newSkillSessionStore(4, time.Minute)
	store.now = func() time.Time { return now }

	if got := store.history("s1"); got != nil {
		t.Errorf("history of an unknown session = %+v, want nil", got)
	}

	store.append("s1",
		adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "q1"},
		adapter.LLMMessage{Role: adapter.LLMRoleAssistant, Content: "a1"},
	)
	store.append("s1",
		adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "q2"},
		adapter.LLMMessage{Role: adapter.LLMRoleAssistant, Content: "a2"},
	)
	hist := store.history("s1")
	if len(hist) != 4 {
		t.Fatalf("history length = %d, want 4", len(hist))
	}

	// Exceeding the window trims from the front so the newest turns survive, and
	// drops whole pairs: a lone assistant reply with no question would confuse the
	// model on the next turn.
	store.append("s1",
		adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "q3"},
		adapter.LLMMessage{Role: adapter.LLMRoleAssistant, Content: "a3"},
	)
	hist = store.history("s1")
	if len(hist) != 4 {
		t.Fatalf("history length after trim = %d, want 4", len(hist))
	}
	if hist[0].Content != "q2" || hist[0].Role != adapter.LLMRoleUser {
		t.Errorf("oldest kept turn = %+v, want the q2/a2 pair (q1/a1 dropped)", hist[0])
	}
	if hist[len(hist)-1].Content != "a3" {
		t.Errorf("newest turn = %+v, want a3", hist[len(hist)-1])
	}

	// history() must hand back a copy, or a caller could corrupt the store.
	hist[0].Content = "mutated"
	if store.history("s1")[0].Content == "mutated" {
		t.Error("history() returned the internal slice")
	}

	if store.count() != 1 {
		t.Errorf("count = %d, want 1", store.count())
	}
	store.drop("s1")
	if store.count() != 0 {
		t.Errorf("count after drop = %d, want 0", store.count())
	}

	// An empty session id must not create a phantom entry.
	store.append("", adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "x"})
	if store.count() != 0 {
		t.Error("append with an empty session id created an entry")
	}
}

func TestSkillSessionStorePurge(t *testing.T) {
	now := time.Now()
	store := newSkillSessionStore(12, time.Minute)
	store.now = func() time.Time { return now }

	store.append("fresh", adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "a"})
	store.append("stale", adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "b"})

	now = now.Add(2 * time.Minute)
	store.append("fresh", adapter.LLMMessage{Role: adapter.LLMRoleUser, Content: "c"})

	if removed := store.purge(); removed != 1 {
		t.Errorf("purge removed %d, want 1", removed)
	}
	if store.count() != 1 {
		t.Errorf("count = %d, want 1", store.count())
	}
	if len(store.history("stale")) != 0 {
		t.Error("the stale session survived the purge")
	}
}

func TestSkillIsExit(t *testing.T) {
	srv := newSkillTestServer(t, newFakeLLM(t, "ok"))
	exits := []string{"退出", "退出。", "再见！", "  拜拜 ", "结束", "不用了", "没事了", "bye", "BYE"}
	for _, text := range exits {
		if !srv.isExit(text) {
			t.Errorf("isExit(%q) = false, want true", text)
		}
	}
	keeps := []string{"退出这个方案行不行", "帮我查天气", "再见了老朋友说说历史", "", "。"}
	for _, text := range keeps {
		if srv.isExit(text) {
			t.Errorf("isExit(%q) = true, want false", text)
		}
	}
}

func TestSkillHandleRequestLifecycle(t *testing.T) {
	llm := newFakeLLM(t, "第一次回答。", "第二次回答。")
	srv := newSkillTestServer(t, llm)
	ctx := context.Background()

	// Launch greets and opens the session without calling the model.
	launch := &adapter.DuerOSRequest{}
	launch.Session.SessionID = "s1"
	launch.Request.Type = adapter.DuerOSTypeLaunch
	resp := srv.HandleRequest(ctx, launch)
	if resp.Response.OutputSpeech == nil || resp.Response.OutputSpeech.Text != srv.cfg.GreetingOrDefault() {
		t.Errorf("launch speech = %+v", resp.Response.OutputSpeech)
	}
	if resp.Response.ShouldEndSession {
		t.Error("launch must keep the session open")
	}
	if len(llm.requests) != 0 {
		t.Error("launch must not call the LLM")
	}

	// First question.
	resp = srv.HandleRequest(ctx, skillIntentRequest("s1", "第一个问题"))
	if got := resp.Response.OutputSpeech.Text; got != "第一次回答。" {
		t.Errorf("reply = %q", got)
	}
	if resp.Response.ShouldEndSession {
		t.Error("the session should stay open for a follow-up")
	}
	msgs := llm.lastMessages(t)
	if len(msgs) != 2 || msgs[0].Role != adapter.LLMRoleSystem {
		t.Fatalf("messages = %+v, want system + user", msgs)
	}
	if msgs[1].Content != "第一个问题" {
		t.Errorf("user message = %q", msgs[1].Content)
	}
	if !strings.Contains(msgs[0].Content, "口语化") {
		t.Errorf("system prompt = %q, want the default voice-oriented prompt", msgs[0].Content)
	}

	// Follow-up must carry the previous turn.
	srv.HandleRequest(ctx, skillIntentRequest("s1", "第二个问题"))
	msgs = llm.lastMessages(t)
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want system + user + assistant + user", len(msgs))
	}
	if msgs[1].Content != "第一个问题" || msgs[2].Content != "第一次回答。" || msgs[3].Content != "第二个问题" {
		t.Errorf("history not replayed: %+v", msgs)
	}
	if msgs[2].Role != adapter.LLMRoleAssistant {
		t.Errorf("role = %q, want assistant", msgs[2].Role)
	}

	// An explicit farewell ends the session and forgets the history.
	resp = srv.HandleRequest(ctx, skillIntentRequest("s1", "退出"))
	if !resp.Response.ShouldEndSession {
		t.Error("a farewell must end the session")
	}
	if resp.Response.OutputSpeech.Text != srv.cfg.FarewellOrDefault() {
		t.Errorf("farewell = %q", resp.Response.OutputSpeech.Text)
	}
	if srv.SessionCount() != 0 {
		t.Errorf("sessions = %d, want the history dropped", srv.SessionCount())
	}
	callsBefore := len(llm.requests)
	srv.HandleRequest(ctx, skillIntentRequest("s1", "重新开始"))
	msgs = llm.lastMessages(t)
	if len(msgs) != 2 {
		t.Errorf("messages = %d, want a fresh history (2)", len(msgs))
	}
	if len(llm.requests) != callsBefore+1 {
		t.Error("the farewell itself should not have called the LLM")
	}
}

func TestSkillHandleRequestSessionEnded(t *testing.T) {
	llm := newFakeLLM(t, "ok")
	srv := newSkillTestServer(t, llm)

	srv.HandleRequest(context.Background(), skillIntentRequest("s9", "问题"))
	if srv.SessionCount() != 1 {
		t.Fatalf("sessions = %d, want 1", srv.SessionCount())
	}

	end := &adapter.DuerOSRequest{}
	end.Session.SessionID = "s9"
	end.Request.Type = adapter.DuerOSTypeSessionEnd
	end.Request.Reason = adapter.DuerOSEndUserInitiated
	resp := srv.HandleRequest(context.Background(), end)

	if srv.SessionCount() != 0 {
		t.Error("SessionEndedRequest must drop the history")
	}
	if !resp.Response.ShouldEndSession {
		t.Error("the reply must end the session")
	}
	if resp.Response.OutputSpeech != nil && resp.Response.OutputSpeech.Text != "" {
		t.Errorf("SessionEndedRequest should not speak, got %q", resp.Response.OutputSpeech.Text)
	}
	if len(llm.requests) != 1 {
		t.Error("SessionEndedRequest must not call the LLM")
	}
}

func TestSkillHandleRequestDegradedInputs(t *testing.T) {
	t.Run("empty utterance asks again", func(t *testing.T) {
		llm := newFakeLLM(t, "ok")
		srv := newSkillTestServer(t, llm)
		resp := srv.HandleRequest(context.Background(), skillIntentRequest("s1", "   "))
		if resp.Response.ShouldEndSession {
			t.Error("the session should stay open")
		}
		if !strings.Contains(resp.Response.OutputSpeech.Text, "再说一遍") {
			t.Errorf("speech = %q", resp.Response.OutputSpeech.Text)
		}
		if len(llm.requests) != 0 {
			t.Error("an empty utterance must not reach the LLM")
		}
	})

	t.Run("llm failure degrades gracefully", func(t *testing.T) {
		llm := newFakeLLM(t, "unused")
		llm.failWith = http.StatusInternalServerError
		srv := newSkillTestServer(t, llm)
		resp := srv.HandleRequest(context.Background(), skillIntentRequest("s1", "问题"))
		if resp.Response.ShouldEndSession {
			t.Error("a transient LLM failure should keep the session open for a retry")
		}
		if !strings.Contains(resp.Response.OutputSpeech.Text, "连不上模型") {
			t.Errorf("speech = %q, want a spoken-safe apology", resp.Response.OutputSpeech.Text)
		}
	})

	t.Run("long reply is truncated before speaking", func(t *testing.T) {
		llm := newFakeLLM(t, strings.Repeat("长", 500))
		srv := newSkillTestServer(t, llm)
		resp := srv.HandleRequest(context.Background(), skillIntentRequest("s1", "问题"))
		got := resp.Response.OutputSpeech.Text
		if n := len([]rune(got)); n != 100 {
			t.Errorf("spoken length = %d runes, want the 100-rune cap", n)
		}
		// The untruncated reply is what goes into history, so a follow-up can
		// still refer to content the speaker never read aloud.
		if len(srv.sessions.history("s1")) != 2 {
			t.Error("history should hold the user turn and the full reply")
		}
		if got := srv.sessions.history("s1")[1].Content; len([]rune(got)) != 500 {
			t.Errorf("history kept %d runes, want the full 500", len([]rune(got)))
		}
	})

	t.Run("unsupported request type", func(t *testing.T) {
		srv := newSkillTestServer(t, newFakeLLM(t, "ok"))
		req := &adapter.DuerOSRequest{}
		req.Request.Type = "AudioPlayer.PlaybackFinished"
		resp := srv.HandleRequest(context.Background(), req)
		if resp.Response.OutputSpeech == nil || !strings.Contains(resp.Response.OutputSpeech.Text, "不支持") {
			t.Errorf("speech = %+v", resp.Response.OutputSpeech)
		}
	})

	t.Run("missing session id falls back to a shared one", func(t *testing.T) {
		srv := newSkillTestServer(t, newFakeLLM(t, "ok"))
		srv.HandleRequest(context.Background(), skillIntentRequest("", "问题"))
		if srv.SessionCount() != 1 {
			t.Errorf("sessions = %d, want 1 under the fallback id", srv.SessionCount())
		}
	})
}

func TestSkillTelegramMirror(t *testing.T) {
	llm := newFakeLLM(t, "回答内容")
	cfg := newSkillTestConfig(t, llm)
	cfg.NotifyTelegram = true
	var sent []string
	srv, err := NewDuerOSSkillServer(cfg, func(msg string) { sent = append(sent, msg) })
	if err != nil {
		t.Fatalf("NewDuerOSSkillServer: %v", err)
	}
	srv.HandleRequest(context.Background(), skillIntentRequest("s1", "问题"))
	if len(sent) != 1 {
		t.Fatalf("notified %d times, want 1", len(sent))
	}
	if !strings.Contains(sent[0], "问题") || !strings.Contains(sent[0], "回答内容") {
		t.Errorf("notification = %q, want both the question and the answer", sent[0])
	}
}

func skillHTTPTest(t *testing.T, srv *DuerOSSkillServer, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	} else {
		reader = http.NoBody
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSkillHTTPHandler(t *testing.T) {
	llm := newFakeLLM(t, "回答。")
	srv := newSkillTestServer(t, llm)

	t.Run("health", func(t *testing.T) {
		rec := skillHTTPTest(t, srv, http.MethodGet, "/health", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["status"] != "ok" || body["model"] != "test-model" {
			t.Errorf("body = %v", body)
		}
	})

	t.Run("intent round trip", func(t *testing.T) {
		rec := skillHTTPTest(t, srv, http.MethodPost, "/dueros",
			`{"version":"2.0","session":{"sessionId":"h1"},`+
				`"request":{"type":"IntentRequest","timestamp":"`+strconv.FormatInt(time.Now().Unix(), 10)+`",`+
				`"query":{"type":"TEXT","original":"你好"}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		var resp adapter.DuerOSResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Version != "2.0" {
			t.Errorf("version = %q", resp.Version)
		}
		if resp.Response.OutputSpeech == nil || resp.Response.OutputSpeech.Text != "回答。" {
			t.Errorf("speech = %+v", resp.Response.OutputSpeech)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("content type = %q", rec.Header().Get("Content-Type"))
		}
	})

	t.Run("rejects GET on the webhook", func(t *testing.T) {
		if rec := skillHTTPTest(t, srv, http.MethodGet, "/dueros", ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", rec.Code)
		}
	})

	t.Run("rejects a malformed body", func(t *testing.T) {
		if rec := skillHTTPTest(t, srv, http.MethodPost, "/dueros", "{not json"); rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("rejects an unsigned request when verification is on", func(t *testing.T) {
		verify := true
		cfg := newSkillTestConfig(t, llm)
		cfg.VerifySignature = &verify
		strict, err := NewDuerOSSkillServer(cfg, nil)
		if err != nil {
			t.Fatalf("NewDuerOSSkillServer: %v", err)
		}
		// llm is shared with the subtests above, so compare against the call count
		// rather than zero.
		before := len(llm.requests)
		rec := skillHTTPTest(t, strict, http.MethodPost, "/dueros",
			`{"version":"2.0","session":{"sessionId":"h2"},"request":{"type":"LaunchRequest"}}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if len(llm.requests) != before {
			t.Error("an unsigned request must not reach the LLM")
		}
	})

	t.Run("enforces the skill allowlist", func(t *testing.T) {
		cfg := newSkillTestConfig(t, llm)
		cfg.AllowedSkillIDs = []string{"bot-allowed"}
		srv, err := NewDuerOSSkillServer(cfg, nil)
		if err != nil {
			t.Fatalf("NewDuerOSSkillServer: %v", err)
		}
		rec := skillHTTPTest(t, srv, http.MethodPost, "/dueros",
			`{"version":"2.0","session":{"sessionId":"h3","application":{"applicationId":"bot-other"}},`+
				`"request":{"type":"LaunchRequest"}}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}

		rec = skillHTTPTest(t, srv, http.MethodPost, "/dueros",
			`{"version":"2.0","session":{"sessionId":"h4","application":{"applicationId":"bot-allowed"}},`+
				`"request":{"type":"LaunchRequest"}}`)
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 for an allowed skill", rec.Code)
		}
	})
}

func TestSkillProviderDispatch(t *testing.T) {
	t.Run("openai is the default", func(t *testing.T) {
		llm := newFakeLLM(t, "ok")
		cfg := newSkillTestConfig(t, llm)
		if got := cfg.ProviderOrDefault(); got != config.SkillProviderOpenAI {
			t.Errorf("ProviderOrDefault() = %q, want openai", got)
		}
		srv, err := NewDuerOSSkillServer(cfg, nil)
		if err != nil {
			t.Fatalf("NewDuerOSSkillServer: %v", err)
		}
		if _, ok := srv.llm.(*openAIChatProvider); !ok {
			t.Errorf("llm = %T, want *openAIChatProvider", srv.llm)
		}
		if srv.Provider() != config.SkillProviderOpenAI {
			t.Errorf("Provider() = %q", srv.Provider())
		}
		if srv.Model() != "test-model" {
			t.Errorf("Model() = %q", srv.Model())
		}
	})

	t.Run("codebuddy needs no base url and defaults its model", func(t *testing.T) {
		// A CodeBuddy client is constructed without contacting the network, so a
		// config with no llm_base_url must still build successfully.
		cfg := &config.XiaoduSkillConfig{
			Provider:        config.SkillProviderCodeBuddy,
			VerifySignature: boolFalse(),
		}
		srv, err := NewDuerOSSkillServer(cfg, nil)
		if err != nil {
			t.Fatalf("NewDuerOSSkillServer: %v", err)
		}
		if _, ok := srv.llm.(*codeBuddyChatProvider); !ok {
			t.Errorf("llm = %T, want *codeBuddyChatProvider", srv.llm)
		}
		if srv.Provider() != config.SkillProviderCodeBuddy {
			t.Errorf("Provider() = %q", srv.Provider())
		}
		if srv.Model() != config.DefaultSkillCodeBuddyModel {
			t.Errorf("Model() = %q, want the CodeBuddy default", srv.Model())
		}
	})

	t.Run("unknown provider fails instead of silently falling back", func(t *testing.T) {
		cfg := &config.XiaoduSkillConfig{
			Provider:   "codebudy",
			LLMBaseURL: "https://x.test",
			LLMModel:   "m",
		}
		if _, err := NewDuerOSSkillServer(cfg, nil); err == nil {
			t.Fatal("expected an error for an unknown provider")
		}
	})

	t.Run("openai still requires a base url and a model", func(t *testing.T) {
		if _, err := NewDuerOSSkillServer(&config.XiaoduSkillConfig{Provider: "openai", LLMModel: "m"}, nil); err == nil {
			t.Error("expected an error without llm_base_url")
		}
		if _, err := NewDuerOSSkillServer(&config.XiaoduSkillConfig{Provider: "openai", LLMBaseURL: "https://x.test"}, nil); err == nil {
			t.Error("expected an error without llm_model")
		}
	})

	t.Run("health reports the provider", func(t *testing.T) {
		llm := newFakeLLM(t, "ok")
		srv := newSkillTestServer(t, llm)
		rec := skillHTTPTest(t, srv, http.MethodGet, "/health", "")
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["provider"] != config.SkillProviderOpenAI {
			t.Errorf("provider = %v, want openai", body["provider"])
		}
	})
}

func boolFalse() *bool {
	v := false
	return &v
}
