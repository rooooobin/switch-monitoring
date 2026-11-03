package config

import (
	"strings"
	"time"
)

// Skill provider identifiers.
const (
	SkillProviderOpenAI    = "openai"
	SkillProviderCodeBuddy = "codebuddy"
)

// DefaultSkillCodeBuddyModel is subscription-backed (cost 0 in the model list),
// fast, and the same model this machine's pi agent uses by default.
const DefaultSkillCodeBuddyModel = "deepseek-v4.1-flash"

// XiaoduSkillConfig runs a DuerOS custom-skill webhook.
//
// This is the "input" half that the speaker has no public API for: instead of
// polling a conversation log (which Baidu does not expose), the skill platform
// pushes every utterance made inside the skill session to our HTTPS endpoint.
// Pair it with the existing broadcastTTS output path and the speaker becomes a
// voice front-end for any OpenAI-compatible model.
//
// Setup on https://dueros.baidu.com/dbp :
//  1. create a 自定义技能 (custom skill);
//  2. reference the system intent ai.dueros.common.default_intent so free-form
//     text reaches the webhook instead of being rejected by NLU;
//  3. set the Web Service URL to https://<public-host><path>;
//  4. enable 真机测试, then say "小度小度，打开调试技能模式" to the speaker.
//
// The skill never needs to be published: unpublished skills run on your own
// device as long as the developer account matches the account bound to it.
type XiaoduSkillConfig struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"` // default ":9528"
	Path    string `yaml:"path"`   // default "/dueros"

	// VerifySignature checks the RSA-SHA1 signature DuerOS puts on every request.
	// Keep it on for any endpoint reachable from the internet; only disable it for
	// a local curl smoke test. Default true.
	VerifySignature *bool `yaml:"verify_signature"`

	// AllowedSkillIDs optionally pins the webhook to one or more applicationIds,
	// so a leaked URL cannot be driven by somebody else's skill. Empty = allow all.
	AllowedSkillIDs []string `yaml:"allowed_skill_ids"`

	// Provider selects the LLM backend dialect:
	//   openai    — any OpenAI-compatible /chat/completions endpoint (default);
	//   codebuddy — Tencent CodeBuddy (subscription-backed, streaming-only).
	//               Credentials come from llm_api_key or the explicit token
	//               files below; this daemon never reads another tool's store.
	Provider string `yaml:"provider"`

	// LLM backend. For provider=openai this is any OpenAI-compatible
	// /chat/completions service; for provider=codebuddy it is the server root
	// (empty defaults to https://copilot.tencent.com).
	LLMBaseURL        string   `yaml:"llm_base_url"`
	LLMAPIKey         string   `yaml:"llm_api_key"`
	LLMModel          string   `yaml:"llm_model"`
	LLMProxy          string   `yaml:"llm_proxy"`
	LLMTimeoutSeconds int      `yaml:"llm_timeout_seconds"`
	SystemPrompt      string   `yaml:"system_prompt"`
	MaxTokens         int      `yaml:"max_tokens"`
	Temperature       *float64 `yaml:"temperature"`

	// CodeBuddy-only credential files. Paths are explicit (absolute paths are
	// recommended for a systemd unit); there is no implicit home-relative default.
	// The daemon reads these files but never writes to them: rotated tokens stay
	// in memory until restart.
	CodeBuddyTokenFile        string `yaml:"codebuddy_token_file"`
	CodeBuddyRefreshTokenFile string `yaml:"codebuddy_refresh_token_file"`

	// MaxReplyChars truncates the spoken answer. Voice output is linear in time,
	// so an unbounded reply locks the speaker for minutes. Default 300.
	MaxReplyChars int `yaml:"max_reply_chars"`

	// HistoryMessages is how many past turns are replayed per session (default 12).
	HistoryMessages int `yaml:"history_messages"`
	// SessionTTLSeconds drops idle sessions (default 900).
	SessionTTLSeconds int `yaml:"session_ttl_seconds"`

	Greeting     string   `yaml:"greeting"`
	Farewell     string   `yaml:"farewell"`
	ExitKeywords []string `yaml:"exit_keywords"`

	// NotifyTelegram mirrors each voice exchange into the configured chat.
	NotifyTelegram bool `yaml:"notify_telegram"`
}

// DefaultSkillSystemPrompt keeps answers short and speakable: the reply is read
// aloud by the device's own TTS, so markdown, links and code blocks are noise.
const DefaultSkillSystemPrompt = "你是家里的语音助手，回答会通过智能音箱朗读出来。" +
	"请用口语化的简体中文回答，控制在 200 字以内，不要使用 Markdown、列表、链接、表情符号或代码块。"

// defaultSkillExitKeywords ends a voice session. Matching is exact after
// punctuation is stripped, so "退出这个方案" is a question, not a farewell. The
// two English words cover what the device's ASR emits for a spoken "bye".
const defaultSkillExitKeywords = "退出,结束,再见,拜拜,不用了,没事了,bye,exit"

// ProviderOrDefault returns the selected backend. Any unrecognised value is
// treated as an error by the factory rather than silently falling back: a typo
// like "codebudy" should not quietly send voice traffic to a paid endpoint.
func (s *XiaoduSkillConfig) ProviderOrDefault() string {
	if s == nil || strings.TrimSpace(s.Provider) == "" {
		return SkillProviderOpenAI
	}
	return strings.ToLower(strings.TrimSpace(s.Provider))
}

// LLMModelOrDefault supplies a model only for CodeBuddy; OpenAI-compatible
// services have no sensible universal default, so the caller validates there.
func (s *XiaoduSkillConfig) LLMModelOrDefault() string {
	if s == nil {
		return ""
	}
	if m := strings.TrimSpace(s.LLMModel); m != "" {
		return m
	}
	if s.ProviderOrDefault() == SkillProviderCodeBuddy {
		return DefaultSkillCodeBuddyModel
	}
	return ""
}

func (s *XiaoduSkillConfig) ListenOrDefault() string {
	if s == nil || strings.TrimSpace(s.Listen) == "" {
		return ":9528"
	}
	return strings.TrimSpace(s.Listen)
}

func (s *XiaoduSkillConfig) PathOrDefault() string {
	if s == nil {
		return "/dueros"
	}
	p := strings.TrimSpace(s.Path)
	if p == "" {
		return "/dueros"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// VerifySignatureOrDefault defaults to true: an unauthenticated public webhook
// would let anyone spend your LLM quota.
func (s *XiaoduSkillConfig) VerifySignatureOrDefault() bool {
	if s == nil || s.VerifySignature == nil {
		return true
	}
	return *s.VerifySignature
}

func (s *XiaoduSkillConfig) LLMTimeoutOrDefault() time.Duration {
	if s == nil || s.LLMTimeoutSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(s.LLMTimeoutSeconds) * time.Second
}

func (s *XiaoduSkillConfig) MaxReplyCharsOrDefault() int {
	if s == nil || s.MaxReplyChars <= 0 {
		return 300
	}
	return s.MaxReplyChars
}

func (s *XiaoduSkillConfig) HistoryMessagesOrDefault() int {
	if s == nil || s.HistoryMessages <= 0 {
		return 12
	}
	return s.HistoryMessages
}

func (s *XiaoduSkillConfig) SessionTTLOrDefault() time.Duration {
	if s == nil || s.SessionTTLSeconds <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(s.SessionTTLSeconds) * time.Second
}

func (s *XiaoduSkillConfig) SystemPromptOrDefault() string {
	if s == nil || strings.TrimSpace(s.SystemPrompt) == "" {
		return DefaultSkillSystemPrompt
	}
	return strings.TrimSpace(s.SystemPrompt)
}

func (s *XiaoduSkillConfig) GreetingOrDefault() string {
	if s == nil || strings.TrimSpace(s.Greeting) == "" {
		return "我在，想问什么？"
	}
	return strings.TrimSpace(s.Greeting)
}

func (s *XiaoduSkillConfig) FarewellOrDefault() string {
	if s == nil || strings.TrimSpace(s.Farewell) == "" {
		return "好的，再见。"
	}
	return strings.TrimSpace(s.Farewell)
}

// ExitKeywordsOrDefault lists the phrases that end a session.
func (s *XiaoduSkillConfig) ExitKeywordsOrDefault() []string {
	raw := defaultSkillExitKeywords
	if s != nil && len(s.ExitKeywords) > 0 {
		raw = strings.Join(s.ExitKeywords, ",")
	}
	out := make([]string, 0, 6)
	for _, kw := range strings.Split(raw, ",") {
		if kw = strings.TrimSpace(kw); kw != "" {
			out = append(out, kw)
		}
	}
	return out
}

// AutoDiscoverIPOrDefault reports whether the speaker's LAN address should be
// resolved from the cloud instead of trusted from config. Default true: the
// address is DHCP-assigned and a stale value silently breaks DLNA and TTS.
func (x *XiaoduConfig) AutoDiscoverIPOrDefault() bool {
	if x == nil || x.AutoDiscoverIP == nil {
		return true
	}
	return *x.AutoDiscoverIP
}

// trimStringSlice drops empty and whitespace-only entries from a YAML list.
func trimStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
