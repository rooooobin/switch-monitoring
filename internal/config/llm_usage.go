package config

import "strings"

// LLMProviderConfig holds API credentials for one LLM billing provider.
type LLMProviderConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`
	APIBase string `yaml:"api_base"`

	// Bailian-only fields (ignored for DeepSeek).
	// Plan: auto | token_plan_personal | coding_plan
	Plan             string `yaml:"plan"`
	ConsoleCookie    string `yaml:"console_cookie"`
	ConsoleCookieFile string `yaml:"console_cookie_file"`
	PreferBLCLI      bool   `yaml:"prefer_bl_cli"`
}

// LLMUsageConfig enables querying 百炼 and DeepSeek spend/balance via API.
type LLMUsageConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Proxy    string            `yaml:"proxy"`
	DeepSeek LLMProviderConfig `yaml:"deepseek"`
	Bailian  LLMProviderConfig `yaml:"bailian"`
}

func (c *LLMUsageConfig) DeepSeekConfigured() bool {
	if c == nil || !c.Enabled {
		return false
	}
	return c.DeepSeek.Enabled && strings.TrimSpace(c.DeepSeek.APIKey) != ""
}

func (c *LLMUsageConfig) BailianConfigured() bool {
	if c == nil || !c.Enabled || !c.Bailian.Enabled {
		return false
	}
	if strings.TrimSpace(c.Bailian.APIKey) != "" {
		return true
	}
	if strings.TrimSpace(c.Bailian.ConsoleCookie) != "" || strings.TrimSpace(c.Bailian.ConsoleCookieFile) != "" {
		return true
	}
	return c.Bailian.PreferBLCLI
}

func (c *LLMUsageConfig) BailianPlan() string {
	if c == nil {
		return "token_plan_personal"
	}
	p := strings.ToLower(strings.TrimSpace(c.Bailian.Plan))
	switch p {
	case "", "auto":
		key := strings.TrimSpace(c.Bailian.APIKey)
		if strings.HasPrefix(key, "sk-ws-") {
			return "coding_plan"
		}
		return "token_plan_personal"
	case "token_plan", "token-plan", "token_plan_personal", "personal", "solo":
		return "token_plan_personal"
	case "coding_plan", "coding-plan", "coding":
		return "coding_plan"
	default:
		return p
	}
}
