package runner

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
)

// LLMUsageReport holds fetched provider data for display.
type LLMUsageReport struct {
	FetchedAt   time.Time
	DeepSeek    *adapter.DeepSeekBalance
	DeepSeekErr error
	Bailian     *adapter.BailianPlanUsage
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
		cookie, err := adapter.LoadBailianConsoleCookie(cfg.Bailian.ConsoleCookie, cfg.Bailian.ConsoleCookieFile)
		if err != nil {
			report.BailianErr = err
		} else {
			client, err := adapter.NewBailianPlanClient(cfg.BailianPlan(), cookie, proxy, cfg.Bailian.PreferBLCLI || cookie == "")
			if err != nil {
				report.BailianErr = err
			} else {
				report.Bailian, report.BailianErr = client.GetUsage(ctx)
			}
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
		sb.WriteString("\n--- 阿里云百炼 ---\n")
		if report.BailianErr != nil {
			sb.WriteString("Error: " + report.BailianErr.Error() + "\n")
		} else if report.Bailian != nil {
			appendBailianPlanPlain(&sb, report.Bailian)
		}
	}
	return sb.String()
}

func appendBailianPlanPlain(sb *strings.Builder, u *adapter.BailianPlanUsage) {
	sb.WriteString(fmt.Sprintf("Plan: %s (%s)\n", u.PlanName, u.PlanKind))
	if u.Status != "" {
		sb.WriteString("Status: " + u.Status + "\n")
	}
	if u.RemainingDays != nil {
		sb.WriteString(fmt.Sprintf("Subscription remaining days: %d\n", *u.RemainingDays))
	}
	if u.HasEndsAt {
		sb.WriteString("Subscription ends: " + u.EndsAt.Local().Format("2006-01-02 15:04 MST") + "\n")
	}
	if u.Source != "" {
		sb.WriteString("Source: " + u.Source + "\n")
	}
	if len(u.Windows) == 0 {
		sb.WriteString("No usage windows returned.\n")
		return
	}
	sb.WriteString("\n")
	for _, w := range u.Windows {
		sb.WriteString(formatBailianWindowPlain(w) + "\n")
	}
}

func formatBailianWindowPlain(w adapter.BailianUsageWindow) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[%s] used %.1f%%", w.Name, w.UsedPct))
	if w.HasTotals {
		b.WriteString(fmt.Sprintf(" | used %s / total %s | remaining %s",
			formatCredits(w.Used), formatCredits(w.Total), formatCredits(w.Remaining)))
	}
	if w.HasReset {
		b.WriteString(" | resets " + w.ResetsAt.Local().Format("2006-01-02 15:04 MST"))
	}
	return b.String()
}

func formatCredits(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.2f", v)
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
		return "⚠️ <b>LLM usage</b>\nNo providers configured for this command. Enable <code>llm_usage.deepseek</code> or <code>llm_usage.bailian</code> with an API key / console cookie."
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
			u := report.Bailian
			block.WriteString(fmt.Sprintf("Plan: <b>%s</b> (<code>%s</code>)\n", html.EscapeString(u.PlanName), html.EscapeString(u.PlanKind)))
			if u.Status != "" {
				block.WriteString("Status: <code>" + html.EscapeString(u.Status) + "</code>\n")
			}
			if u.RemainingDays != nil {
				block.WriteString(fmt.Sprintf("Days left: <code>%d</code>\n", *u.RemainingDays))
			}
			if u.HasEndsAt {
				block.WriteString("Ends: <code>" + html.EscapeString(u.EndsAt.Local().Format("2006-01-02 15:04")) + "</code>\n")
			}
			block.WriteString("<pre>")
			for _, w := range u.Windows {
				block.WriteString(html.EscapeString(formatBailianWindowPlain(w)) + "\n")
			}
			block.WriteString("</pre>")
		}
		parts = append(parts, block.String())
	}
	return strings.Join(parts, "")
}

func intOrDash(v *int) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}
