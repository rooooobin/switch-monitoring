package runner

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
)

// LLMUsageReport holds fetched provider data for display.
type LLMUsageReport struct {
	FetchedAt time.Time
	DeepSeek  *adapter.DeepSeekBalance
	DeepSeekErr error
	Bailian   *adapter.DashScopeQuotas
	BailianErr  error
}

// LLMUsageScope selects which providers to query.
type LLMUsageScope int

const (
	LLMUsageAll LLMUsageScope = iota
	LLMUsageDeepSeekOnly
	LLMUsageBailianOnly
)

// FetchLLMUsage loads balance/spend from configured providers.
func FetchLLMUsage(ctx context.Context, cfg *config.LLMUsageConfig) LLMUsageReport {
	return FetchLLMUsageScoped(ctx, cfg, LLMUsageAll)
}

// FetchLLMUsageScoped loads only the providers included in scope.
func FetchLLMUsageScoped(ctx context.Context, cfg *config.LLMUsageConfig, scope LLMUsageScope) LLMUsageReport {
	report := LLMUsageReport{FetchedAt: time.Now()}
	if cfg == nil || !cfg.Enabled {
		return report
	}
	proxy := cfg.Proxy

	if cfg.DeepSeekConfigured() && scope != LLMUsageBailianOnly {
		client, err := adapter.NewDeepSeekClient(cfg.DeepSeek.APIBase, cfg.DeepSeek.APIKey, proxy)
		if err != nil {
			report.DeepSeekErr = err
		} else {
			report.DeepSeek, report.DeepSeekErr = client.GetBalance(ctx)
		}
	}

	if cfg.BailianConfigured() && scope != LLMUsageDeepSeekOnly {
		client, err := adapter.NewDashScopeClient(cfg.Bailian.APIBase, cfg.Bailian.APIKey, proxy)
		if err != nil {
			report.BailianErr = err
		} else {
			report.Bailian, report.BailianErr = client.GetQuotas(ctx)
		}
	}
	return report
}

func llmUsageScopeForCommand(text string) LLMUsageScope {
	first := strings.Fields(strings.TrimSpace(text))[0]
	switch first {
	case "/deepseek_balance":
		return LLMUsageDeepSeekOnly
	case "/bailian_usage":
		return LLMUsageBailianOnly
	default:
		return LLMUsageAll
	}
}

func formatScopeHint(scope LLMUsageScope, cfg *config.LLMUsageConfig) (deepseek, bailian bool) {
	switch scope {
	case LLMUsageDeepSeekOnly:
		return cfg != nil && cfg.DeepSeekConfigured(), false
	case LLMUsageBailianOnly:
		return false, cfg != nil && cfg.BailianConfigured()
	default:
		return cfg != nil && cfg.DeepSeekConfigured(), cfg != nil && cfg.BailianConfigured()
	}
}

// FormatLLMUsagePlain formats a human-readable report for CLI.
func FormatLLMUsagePlain(report LLMUsageReport, cfg *config.LLMUsageConfig) string {
	return FormatLLMUsagePlainScoped(report, cfg, LLMUsageAll)
}

// FormatLLMUsagePlainScoped formats plain text for a subset of providers.
func FormatLLMUsagePlainScoped(report LLMUsageReport, cfg *config.LLMUsageConfig, scope LLMUsageScope) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("LLM usage (fetched %s)\n", report.FetchedAt.Format(time.RFC3339)))
	if cfg == nil || !cfg.Enabled {
		sb.WriteString("llm_usage is disabled in config.\n")
		return sb.String()
	}
	wantDS, wantBL := formatScopeHint(scope, cfg)
	if !wantDS && !wantBL {
		sb.WriteString("No providers enabled for this query (check llm_usage config and api_key).\n")
		return sb.String()
	}
	if wantDS {
		sb.WriteString("\n--- DeepSeek ---\n")
		if report.DeepSeekErr != nil {
			sb.WriteString("Error: " + report.DeepSeekErr.Error() + "\n")
		} else if report.DeepSeek != nil {
			b := report.DeepSeek
			sb.WriteString(fmt.Sprintf("API available: %v\n", b.IsAvailable))
			sb.WriteString(fmt.Sprintf("Balance: %s %s (granted %s, topped-up %s)\n", b.Total, b.Currency, b.Granted, b.ToppedUp))
			if b.RateLimitRPM != nil {
				rem := "?"
				if b.RateLimitRPMRemaining != nil {
					rem = fmt.Sprintf("%d", *b.RateLimitRPMRemaining)
				}
				sb.WriteString(fmt.Sprintf("Rate limit: %d RPM remaining (%s)\n", *b.RateLimitRPM, rem))
			}
			sb.WriteString("(DeepSeek API exposes balance only, not historical token spend.)\n")
		}
	}
	if wantBL {
		sb.WriteString("\n--- 阿里云百炼 (DashScope) ---\n")
		if report.BailianErr != nil {
			sb.WriteString("Error: " + report.BailianErr.Error() + "\n")
		} else if report.Bailian != nil {
			appendBailianPlain(&sb, report.Bailian)
		}
	}
	return sb.String()
}

func appendBailianPlain(sb *strings.Builder, q *adapter.DashScopeQuotas) {
	if q.BillingStart != "" || q.BillingEnd != "" {
		sb.WriteString(fmt.Sprintf("Billing period: %s — %s\n", q.BillingStart, q.BillingEnd))
	}
	if q.Available != nil {
		sb.WriteString(fmt.Sprintf("Available balance: $%.4f\n", *q.Available))
	}
	if q.Credits != nil {
		sb.WriteString(fmt.Sprintf("Credits: $%.4f\n", *q.Credits))
	}
	if q.DailySpend != nil {
		sb.WriteString(fmt.Sprintf("Daily spend (1d): $%.4f\n", *q.DailySpend))
	}
	if q.MonthlySpend != nil {
		sb.WriteString(fmt.Sprintf("Monthly spend (30d): $%.4f\n", *q.MonthlySpend))
	}
	if q.TokensUsed != nil {
		sb.WriteString(fmt.Sprintf("Tokens used (period): %.0f\n", *q.TokensUsed))
	}
	if q.RequestsUsed != nil {
		sb.WriteString(fmt.Sprintf("Requests used (period): %.0f\n", *q.RequestsUsed))
	}
	if q.RPM != nil || q.TPM != nil {
		sb.WriteString(fmt.Sprintf("Account rate limits: RPM=%s TPM=%s\n", intOrDash(q.RPM), intOrDash(q.TPM)))
	}
	if len(q.Models) > 0 {
		sb.WriteString("\nPer-model quota usage:\n")
		for _, line := range formatModelQuotaLines(q.Models, 8) {
			sb.WriteString("  " + line + "\n")
		}
		if len(q.Models) > 8 {
			sb.WriteString(fmt.Sprintf("  … and %d more models\n", len(q.Models)-8))
		}
	}
}

// FormatLLMUsageHTML formats the report for Telegram HTML mode.
func FormatLLMUsageHTML(report LLMUsageReport, cfg *config.LLMUsageConfig) string {
	return FormatLLMUsageHTMLScoped(report, cfg, LLMUsageAll)
}

func FormatLLMUsageHTMLScoped(report LLMUsageReport, cfg *config.LLMUsageConfig, scope LLMUsageScope) string {
	if cfg == nil || !cfg.Enabled {
		return "⚠️ <b>LLM usage</b>\nllm_usage is disabled in config."
	}
	wantDS, wantBL := formatScopeHint(scope, cfg)
	if !wantDS && !wantBL {
		return "⚠️ <b>LLM usage</b>\nNo providers configured for this command. Enable <code>llm_usage.deepseek</code> or <code>llm_usage.bailian</code> with an API key."
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("📊 <b>LLM usage</b>\n<i>%s</i>", html.EscapeString(report.FetchedAt.Format("2006-01-02 15:04:05 MST"))))

	if wantDS {
		var block strings.Builder
		block.WriteString("\n\n🤖 <b>DeepSeek</b>\n")
		if report.DeepSeekErr != nil {
			block.WriteString("❌ " + html.EscapeString(report.DeepSeekErr.Error()))
		} else if report.DeepSeek != nil {
			b := report.DeepSeek
			status := "✅"
			if !b.IsAvailable {
				status = "⚠️"
			}
			block.WriteString(fmt.Sprintf("%s API ok: <code>%v</code>\n", status, b.IsAvailable))
			block.WriteString(fmt.Sprintf("Balance: <b>%s %s</b>\n", html.EscapeString(b.Total), html.EscapeString(b.Currency)))
			block.WriteString(fmt.Sprintf("Granted: <code>%s</code> · Topped-up: <code>%s</code>\n", html.EscapeString(b.Granted), html.EscapeString(b.ToppedUp)))
			if b.RateLimitRPM != nil {
				rem := "?"
				if b.RateLimitRPMRemaining != nil {
					rem = fmt.Sprintf("%d", *b.RateLimitRPMRemaining)
				}
				block.WriteString(fmt.Sprintf("RPM limit: <code>%d</code> (remaining <code>%s</code>)\n", *b.RateLimitRPM, rem))
			}
			block.WriteString("<i>No token spend history via API; balance only.</i>")
		}
		parts = append(parts, block.String())
	}

	if wantBL {
		var block strings.Builder
		block.WriteString("\n\n☁️ <b>阿里云百炼</b>\n")
		if report.BailianErr != nil {
			block.WriteString("❌ " + html.EscapeString(report.BailianErr.Error()))
		} else if report.Bailian != nil {
			q := report.Bailian
			if q.BillingStart != "" || q.BillingEnd != "" {
				block.WriteString(fmt.Sprintf("Period: <code>%s</code> → <code>%s</code>\n",
					html.EscapeString(q.BillingStart), html.EscapeString(q.BillingEnd)))
			}
			if q.MonthlySpend != nil {
				block.WriteString(fmt.Sprintf("Monthly spend: <b>$%.4f</b>\n", *q.MonthlySpend))
			}
			if q.DailySpend != nil {
				block.WriteString(fmt.Sprintf("Daily spend: <code>$%.4f</code>\n", *q.DailySpend))
			}
			if q.TokensUsed != nil {
				block.WriteString(fmt.Sprintf("Tokens used: <b>%.0f</b>\n", *q.TokensUsed))
			}
			if q.RequestsUsed != nil {
				block.WriteString(fmt.Sprintf("Requests: <code>%.0f</code>\n", *q.RequestsUsed))
			}
			if q.Available != nil {
				block.WriteString(fmt.Sprintf("Available: <code>$%.4f</code>\n", *q.Available))
			}
			if len(q.Models) > 0 {
				block.WriteString("\n<pre>")
				for _, line := range formatModelQuotaLines(q.Models, 12) {
					block.WriteString(html.EscapeString(line) + "\n")
				}
				block.WriteString("</pre>")
			}
		}
		parts = append(parts, block.String())
	}
	return strings.Join(parts, "")
}

func formatModelQuotaLines(models map[string]adapter.DashScopeModelQuota, max int) []string {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	if max > 0 && len(names) > max {
		names = names[:max]
	}
	var lines []string
	for _, name := range names {
		m := models[name]
		if m.Used != nil && m.Limit != nil && *m.Limit > 0 {
			pct := (*m.Used / *m.Limit) * 100
			lines = append(lines, fmt.Sprintf("%s: %.0f/%.0f (%.1f%%)", name, *m.Used, *m.Limit, pct))
		} else if m.Used != nil {
			lines = append(lines, fmt.Sprintf("%s: used %.0f", name, *m.Used))
		}
	}
	return lines
}

func intOrDash(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}
