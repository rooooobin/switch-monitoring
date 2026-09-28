package runner

import "strings"

func isLLMUsageCommand(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	first := strings.Fields(text)[0]
	switch first {
	case "/llm_usage", "/ai_usage", "/deepseek_balance", "/bailian_usage":
		return true
	default:
		return false
	}
}
