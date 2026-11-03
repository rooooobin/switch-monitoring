package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

func TestXiaoduSkillDefaults(t *testing.T) {
	// A nil config must still yield usable values: the runner asks for defaults
	// before it knows whether the section exists at all.
	var nilCfg *XiaoduSkillConfig
	if got := nilCfg.ListenOrDefault(); got != ":9528" {
		t.Errorf("nil ListenOrDefault() = %q", got)
	}
	if got := nilCfg.PathOrDefault(); got != "/dueros" {
		t.Errorf("nil PathOrDefault() = %q", got)
	}
	if !nilCfg.VerifySignatureOrDefault() {
		t.Error("nil VerifySignatureOrDefault() = false; an unauthenticated webhook must never be the default")
	}
	if got := nilCfg.MaxReplyCharsOrDefault(); got != 300 {
		t.Errorf("nil MaxReplyCharsOrDefault() = %d", got)
	}
	if got := nilCfg.HistoryMessagesOrDefault(); got != 12 {
		t.Errorf("nil HistoryMessagesOrDefault() = %d", got)
	}
	if got := nilCfg.SessionTTLOrDefault(); got != 15*time.Minute {
		t.Errorf("nil SessionTTLOrDefault() = %v", got)
	}
	if got := nilCfg.LLMTimeoutOrDefault(); got != time.Minute {
		t.Errorf("nil LLMTimeoutOrDefault() = %v", got)
	}
	if got := nilCfg.GreetingOrDefault(); got == "" {
		t.Error("nil GreetingOrDefault() is empty")
	}
	if got := nilCfg.FarewellOrDefault(); got == "" {
		t.Error("nil FarewellOrDefault() is empty")
	}
	if got := nilCfg.SystemPromptOrDefault(); got == "" {
		t.Error("nil SystemPromptOrDefault() is empty")
	}
	if got := nilCfg.ExitKeywordsOrDefault(); len(got) == 0 {
		t.Error("nil ExitKeywordsOrDefault() is empty")
	}

	// Zero values must fall back to the same defaults, not to 0 or "".
	zero := &XiaoduSkillConfig{}
	if zero.ListenOrDefault() != ":9528" || zero.PathOrDefault() != "/dueros" {
		t.Errorf("zero listen/path = %q %q", zero.ListenOrDefault(), zero.PathOrDefault())
	}
	if zero.MaxReplyCharsOrDefault() != 300 || zero.HistoryMessagesOrDefault() != 12 {
		t.Error("zero numeric limits did not fall back to defaults")
	}
	if zero.LLMTimeoutOrDefault() != time.Minute || zero.SessionTTLOrDefault() != 15*time.Minute {
		t.Error("zero durations did not fall back to defaults")
	}
}

func TestXiaoduSkillOverrides(t *testing.T) {
	cfg := &XiaoduSkillConfig{
		Listen:            " 127.0.0.1:8080 ",
		Path:              "dueros-hook",
		VerifySignature:   boolPtr(false),
		LLMTimeoutSeconds: 5,
		MaxReplyChars:     42,
		HistoryMessages:   3,
		SessionTTLSeconds: 30,
		SystemPrompt:      " 你是测试助手 ",
		Greeting:          " 说 ",
		Farewell:          " 走 ",
		ExitKeywords:      []string{" 停 ", "", "收工"},
	}
	if got := cfg.ListenOrDefault(); got != "127.0.0.1:8080" {
		t.Errorf("ListenOrDefault() = %q, want it trimmed", got)
	}
	// A path without a leading slash would never match http.ServeMux.
	if got := cfg.PathOrDefault(); got != "/dueros-hook" {
		t.Errorf("PathOrDefault() = %q, want a leading slash added", got)
	}
	if cfg.VerifySignatureOrDefault() {
		t.Error("an explicit false must be honoured")
	}
	if got := cfg.LLMTimeoutOrDefault(); got != 5*time.Second {
		t.Errorf("LLMTimeoutOrDefault() = %v", got)
	}
	if cfg.MaxReplyCharsOrDefault() != 42 || cfg.HistoryMessagesOrDefault() != 3 {
		t.Error("numeric overrides were ignored")
	}
	if got := cfg.SessionTTLOrDefault(); got != 30*time.Second {
		t.Errorf("SessionTTLOrDefault() = %v", got)
	}
	if got := cfg.SystemPromptOrDefault(); got != "你是测试助手" {
		t.Errorf("SystemPromptOrDefault() = %q", got)
	}
	if got := cfg.GreetingOrDefault(); got != "说" {
		t.Errorf("GreetingOrDefault() = %q", got)
	}
	if got := cfg.FarewellOrDefault(); got != "走" {
		t.Errorf("FarewellOrDefault() = %q", got)
	}
	kw := cfg.ExitKeywordsOrDefault()
	if len(kw) != 2 || kw[0] != "停" || kw[1] != "收工" {
		t.Errorf("ExitKeywordsOrDefault() = %q, want empties dropped and entries trimmed", kw)
	}
}

func TestXiaoduAutoDiscoverIPDefault(t *testing.T) {
	var nilCfg *XiaoduConfig
	if !nilCfg.AutoDiscoverIPOrDefault() {
		t.Error("nil config should default to discovery on: a stale static IP silently breaks DLNA")
	}
	if !(&XiaoduConfig{}).AutoDiscoverIPOrDefault() {
		t.Error("unset flag should default to on")
	}
	if (&XiaoduConfig{AutoDiscoverIP: boolPtr(false)}).AutoDiscoverIPOrDefault() {
		t.Error("an explicit false must be honoured")
	}
	if !(&XiaoduConfig{AutoDiscoverIP: boolPtr(true)}).AutoDiscoverIPOrDefault() {
		t.Error("an explicit true must be honoured")
	}
}

// TestLoadConfigXiaoduSkill guards the raw→typed mapping. A field missing from the
// anonymous raw struct parses as zero without any error, so the only way to catch
// it is a round trip through LoadConfig.
func TestLoadConfigXiaoduSkill(t *testing.T) {
	yaml := `
xiaodu:
  enabled: true
  ip: "192.168.2.162"
  port: 49494
  client_id: " cid "
  cuid: "cuid-value"
  bduss: "bduss-value"
  scene_id: "scene-value"
  auto_discover_ip: false
  skill:
    enabled: true
    listen: " 0.0.0.0:9999 "
    path: "/xiaodu-hook"
    verify_signature: false
    allowed_skill_ids: [" bot-1 ", "", "bot-2"]
    llm_base_url: " https://api.deepseek.com "
    llm_api_key: "sk-secret"
    llm_model: " deepseek-chat "
    llm_proxy: "http://127.0.0.1:7890"
    llm_timeout_seconds: 25
    system_prompt: " 用一句话回答 "
    max_tokens: 512
    temperature: 0.3
    max_reply_chars: 120
    history_messages: 6
    session_ttl_seconds: 120
    greeting: " 在 "
    farewell: " 拜 "
    exit_keywords: ["停", " 再见 "]
    notify_telegram: true
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	x := cfg.Xiaodu
	if x == nil {
		t.Fatal("xiaodu section missing")
	}
	if x.AutoDiscoverIPOrDefault() {
		t.Error("auto_discover_ip: false was not honoured")
	}
	s := x.Skill
	if s == nil {
		t.Fatal("skill section missing from the parsed config")
	}
	if !s.Enabled {
		t.Error("skill.enabled not parsed")
	}
	if got := s.ListenOrDefault(); got != "0.0.0.0:9999" {
		t.Errorf("listen = %q, want it trimmed", got)
	}
	if got := s.PathOrDefault(); got != "/xiaodu-hook" {
		t.Errorf("path = %q", got)
	}
	if s.VerifySignatureOrDefault() {
		t.Error("verify_signature: false was not honoured")
	}
	if len(s.AllowedSkillIDs) != 2 || s.AllowedSkillIDs[0] != "bot-1" || s.AllowedSkillIDs[1] != "bot-2" {
		t.Errorf("allowed_skill_ids = %q, want empties dropped and entries trimmed", s.AllowedSkillIDs)
	}
	if s.LLMBaseURL != "https://api.deepseek.com" {
		t.Errorf("llm_base_url = %q, want it trimmed", s.LLMBaseURL)
	}
	if s.LLMAPIKey != "sk-secret" {
		t.Error("llm_api_key not parsed")
	}
	if s.LLMModel != "deepseek-chat" {
		t.Errorf("llm_model = %q, want it trimmed", s.LLMModel)
	}
	if s.LLMProxy != "http://127.0.0.1:7890" {
		t.Error("llm_proxy not parsed")
	}
	if got := s.LLMTimeoutOrDefault(); got != 25*time.Second {
		t.Errorf("llm timeout = %v", got)
	}
	if s.SystemPromptOrDefault() != "用一句话回答" {
		t.Errorf("system_prompt = %q", s.SystemPromptOrDefault())
	}
	if s.MaxTokens != 512 {
		t.Errorf("max_tokens = %d", s.MaxTokens)
	}
	if s.Temperature == nil || *s.Temperature != 0.3 {
		t.Errorf("temperature = %v, want the pointer preserved (0 is a valid value)", s.Temperature)
	}
	if s.MaxReplyCharsOrDefault() != 120 || s.HistoryMessagesOrDefault() != 6 {
		t.Error("max_reply_chars / history_messages not parsed")
	}
	if got := s.SessionTTLOrDefault(); got != 120*time.Second {
		t.Errorf("session ttl = %v", got)
	}
	if s.GreetingOrDefault() != "在" || s.FarewellOrDefault() != "拜" {
		t.Errorf("greeting/farewell = %q/%q", s.GreetingOrDefault(), s.FarewellOrDefault())
	}
	kw := s.ExitKeywordsOrDefault()
	if len(kw) != 2 || kw[0] != "停" || kw[1] != "再见" {
		t.Errorf("exit_keywords = %q", kw)
	}
	if !s.NotifyTelegram {
		t.Error("notify_telegram not parsed")
	}
	if x.ClientID != "cid" {
		t.Errorf("client_id = %q, want it trimmed", x.ClientID)
	}
}

// TestLoadConfigXiaoduSkillDefaultsWhenOmitted checks the section is optional:
// existing configs without xiaodu.skill must keep loading and keep verification on.
func TestLoadConfigXiaoduSkillDefaultsWhenOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("xiaodu:\n  enabled: true\n  ip: 10.0.0.5\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Xiaodu == nil {
		t.Fatal("xiaodu section missing")
	}
	if cfg.Xiaodu.Skill != nil {
		t.Errorf("skill should stay nil when omitted, got %+v", cfg.Xiaodu.Skill)
	}
	if !cfg.Xiaodu.AutoDiscoverIPOrDefault() {
		t.Error("auto_discover_ip should default to on")
	}
	// Defaults must be reachable through the nil-safe accessors.
	var s *XiaoduSkillConfig
	if !s.VerifySignatureOrDefault() || s.ListenOrDefault() != ":9528" {
		t.Error("nil skill defaults are wrong")
	}
}

func TestXiaoduSkillProviderDefaults(t *testing.T) {
	var nilCfg *XiaoduSkillConfig
	if got := nilCfg.ProviderOrDefault(); got != SkillProviderOpenAI {
		t.Errorf("nil ProviderOrDefault() = %q, want openai", got)
	}
	if got := nilCfg.LLMModelOrDefault(); got != "" {
		t.Errorf("nil LLMModelOrDefault() = %q, want empty", got)
	}

	cb := &XiaoduSkillConfig{Provider: " CODEBUDDY "}
	if got := cb.ProviderOrDefault(); got != SkillProviderCodeBuddy {
		t.Errorf("ProviderOrDefault() = %q, want codebuddy (trimmed + lowercased)", got)
	}
	if got := cb.LLMModelOrDefault(); got != DefaultSkillCodeBuddyModel {
		t.Errorf("LLMModelOrDefault() = %q, want %q", got, DefaultSkillCodeBuddyModel)
	}

	explicit := &XiaoduSkillConfig{Provider: "codebuddy", LLMModel: "glm-5.3"}
	if got := explicit.LLMModelOrDefault(); got != "glm-5.3" {
		t.Errorf("explicit model = %q, want it honoured", got)
	}

	withSpace := &XiaoduSkillConfig{Provider: "", LLMModel: " gpt-4o-mini "}
	if got := withSpace.ProviderOrDefault(); got != SkillProviderOpenAI {
		t.Errorf("empty provider = %q", got)
	}
}

func TestLoadConfigXiaoduSkillProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
xiaodu:
  enabled: true
  ip: 10.0.0.5
  skill:
    enabled: true
    provider: CODEBUDDY
    llm_api_key: ""
    llm_model: " hy3 "
    verify_signature: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	s := cfg.Xiaodu.Skill
	if s == nil {
		t.Fatal("skill section missing")
	}
	if s.Provider != SkillProviderCodeBuddy {
		t.Errorf("provider = %q, want lowercased codebuddy", s.Provider)
	}
	if s.ProviderOrDefault() != SkillProviderCodeBuddy {
		t.Errorf("ProviderOrDefault() = %q", s.ProviderOrDefault())
	}
	if s.LLMModelOrDefault() != "hy3" {
		t.Errorf("model = %q, want trimmed hy3", s.LLMModelOrDefault())
	}
	// provider=codebuddy must not require llm_base_url.
	if s.LLMBaseURL != "" {
		t.Errorf("llm_base_url = %q, want empty", s.LLMBaseURL)
	}
}

func TestLoadConfigCodeBuddyCredentialFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
xiaodu:
  enabled: true
  ip: 10.0.0.5
  skill:
    enabled: true
    provider: codebuddy
    llm_api_key: ""
    codebuddy_token_file: " /home/rhuang/.config/switch-monitor/codebuddy.token "
    codebuddy_refresh_token_file: "/home/rhuang/.config/switch-monitor/codebuddy.refresh"
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	s := cfg.Xiaodu.Skill
	if s == nil {
		t.Fatal("skill section missing")
	}
	wantToken := "/home/rhuang/.config/switch-monitor/codebuddy.token"
	if s.CodeBuddyTokenFile != wantToken {
		t.Errorf("codebuddy_token_file = %q, want it trimmed", s.CodeBuddyTokenFile)
	}
	wantRefresh := "/home/rhuang/.config/switch-monitor/codebuddy.refresh"
	if s.CodeBuddyRefreshTokenFile != wantRefresh {
		t.Errorf("codebuddy_refresh_token_file = %q", s.CodeBuddyRefreshTokenFile)
	}
}

// TestLoadConfigNoImplicitCredentials guards the deployment contract: an
// existing config without any CodeBuddy credential field must still load, with
// empty strings — the daemon must never invent a path from HOME at runtime.
func TestLoadConfigNoImplicitCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := `
xiaodu:
  enabled: true
  ip: 10.0.0.5
  skill:
    enabled: true
    provider: codebuddy
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	s := cfg.Xiaodu.Skill
	if s.LLMAPIKey != "" || s.CodeBuddyTokenFile != "" || s.CodeBuddyRefreshTokenFile != "" {
		t.Errorf("credential fields must stay empty when not configured: %+v", s)
	}
}
