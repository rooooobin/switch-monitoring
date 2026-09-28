package config

import "strings"

// LLMProviderConfig holds API credentials for one LLM billing provider.
type LLMProviderConfig struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`
	APIBase string `yaml:"api_base"`
}

// LLMUsageConfig enables querying 百炼 (DashScope) and DeepSeek spend/balance via API.
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
	if c == nil || !c.Enabled {
		return false
	}
	return c.Bailian.Enabled && strings.TrimSpace(c.Bailian.APIKey) != ""
}
