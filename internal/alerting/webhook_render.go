package alerting

import (
	"fmt"
	"html"
	"strings"
)

// WebhookRenderer turns an inbound alert from another host into a Telegram HTML
// message. Telegram's HTML parse mode supports a deliberately small tag set
// (<b> <i> <u> <s> <code> <pre> <blockquote> <a> <tg-spoiler>), so the renderer
// only ever emits those and escapes everything else.
//
// Layout goals, in priority order:
//  1. The first line alone must convey "is this something I must act on".
//  2. Detail must not blow up the notification preview: long bodies go into a
//     <blockquote expandable> so the chat stays readable.
//  3. Log-ish bodies keep their alignment, so they go in <pre>; prose does not.
type WebhookRenderer struct{}

// NewWebhookRenderer returns a renderer.
func NewWebhookRenderer() *WebhookRenderer { return &WebhookRenderer{} }

// severityStyle maps a free-form severity to an emoji badge and a label.
func severityStyle(sev, title, body string) (badge, label string) {
	norm := strings.ToLower(strings.TrimSpace(sev))
	if norm == "" {
		// Infer from the text so a caller that forgets the field still gets a
		// sensible badge: triage depends on this line.
		lower := strings.ToLower(title + " " + body)
		switch {
		case containsAny(lower, "critical", "fatal", "panic", "emerg"):
			norm = "critical"
		case containsAny(lower, "error", "fail", "down", "refused", "unreachable"):
			norm = "error"
		case containsAny(lower, "warn", "degrad", "slow", "soon", "expire"):
			norm = "warning"
		case containsAny(lower, "recover", "ok", "pass", "resolved", "up"):
			norm = "ok"
		default:
			norm = "info"
		}
	}
	switch norm {
	case "critical", "fatal", "emerg", "emergency":
		return "🛑", "CRITICAL"
	case "error", "high":
		return "❌", "ERROR"
	case "warning", "warn", "medium":
		return "⚠️", "WARNING"
	case "ok", "info_ok", "resolved", "recovered", "success":
		return "✅", "OK"
	case "info", "low", "notice":
		return "ℹ️", "INFO"
	default:
		return "ℹ️", strings.ToUpper(norm)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// looksPreformatted reports whether the body is log-like enough that preserving
// line breaks and column alignment beats reflowing it into prose.
//
// Reflowing is only ever right for genuinely wrapped prose. Anything that looks
// like key=value pairs, a table, a stack trace or journal output must keep its
// own line structure, otherwise unrelated facts get glued into one sentence.
func looksPreformatted(body string) bool {
	trimmed := strings.TrimSpace(body)
	lines := strings.Split(trimmed, "\n")
	if len(lines) == 1 {
		return false
	}

	indented, long, keyValue := 0, 0, 0
	for _, l := range lines {
		if strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "\t") {
			indented++
		}
		if len(l) > 78 {
			long++
		}
		if hasKeyValuePrefix(l) {
			keyValue++
		}
	}
	// Two or more lines that each start with "key: value" (cert dumps, df output,
	// systemctl status) are a record, not prose.
	if keyValue >= 2 {
		return true
	}
	return indented >= 2 || long >= 1
}

// hasKeyValuePrefix reports whether a line begins with "key=value" or
// "key: value", the shapes produced by openssl/df/systemctl-style output.
func hasKeyValuePrefix(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	eq := strings.Index(t, "=")
	colon := strings.Index(t, ": ")
	if eq > 0 && (colon < 0 || eq < colon) {
		key := strings.TrimSpace(t[:eq])
		return key != "" && !strings.ContainsAny(key, " \t")
	}
	if colon > 0 {
		key := strings.TrimSpace(t[:colon])
		return key != "" && !strings.ContainsAny(key, " \t")
	}
	return false
}

// Render builds the Telegram HTML message for one inbound alert.
func (r *WebhookRenderer) Render(title, body, host, severity string) string {
	badge, label := severityStyle(severity, title, body)

	if title == "" {
		// Derive a title from the first body line so the header is never empty.
		first, rest, _ := strings.Cut(strings.TrimSpace(body), "\n")
		title = strings.TrimSpace(first)
		body = strings.TrimSpace(rest)
	}

	var b strings.Builder
	b.WriteString(badge)
	b.WriteString(" <b>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</b>")

	// Meta line: severity and origin, kept on one short line.
	meta := []string{label}
	if host != "" {
		meta = append(meta, "from <code>"+html.EscapeString(host)+"</code>")
	}
	b.WriteString("\n")
	b.WriteString("<i>")
	b.WriteString(strings.Join(meta, " · "))
	b.WriteString("</i>")

	if strings.TrimSpace(body) == "" {
		return b.String()
	}

	b.WriteString("\n")
	if looksPreformatted(body) {
		// <pre> keeps the alignment of tables and journal dumps.
		b.WriteString("<pre>")
		b.WriteString(html.EscapeString(collapseExcessBlankLines(body)))
		b.WriteString("</pre>")
		return b.String()
	}

	// Prose: collapse whitespace, then hide long detail behind an expandable
	// quote so the collapsed notification stays short.
	text := html.EscapeString(collapseSpaces(body))
	if runeLen(text) > 260 {
		b.WriteString("<blockquote expandable>")
		b.WriteString(text)
		b.WriteString("</blockquote>")
		return b.String()
	}
	b.WriteString(text)
	return b.String()
}

// collapseSpaces joins wrapped prose lines into one paragraph, preserving
// intentional blank lines as paragraph breaks.
func collapseSpaces(s string) string {
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	out := strings.Join(paras, "\n\n")
	return strings.TrimSpace(out)
}

// collapseExcessBlankLines trims runs of blank lines so a <pre> block does not
// waste vertical space in the chat.
func collapseExcessBlankLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return strings.Join(out, "\n")
}

func runeLen(s string) int { return len([]rune(s)) }

// RenderHealthSummary renders a compact multi-check result (used by hosts that
// push a batch of checks in one call, e.g. a daily health script).
func (r *WebhookRenderer) RenderHealthSummary(host string, checks map[string]string) string {
	var b strings.Builder
	b.WriteString("🩺 <b>Health check</b>\n")
	if host != "" {
		b.WriteString("<i>from <code>")
		b.WriteString(html.EscapeString(host))
		b.WriteString("</code></i>\n")
	}
	var failed, ok int
	var failLines strings.Builder
	for _, name := range sortedKeys(checks) {
		status := checks[name]
		if isFailureStatus(status) {
			failed++
			fmt.Fprintf(&failLines, "%s: %s\n", name, status)
		} else {
			ok++
		}
	}
	fmt.Fprintf(&b, "<b>%d ok</b>, <b>%d failed</b>", ok, failed)
	if failed > 0 {
		b.WriteString("\n<blockquote expandable><pre>")
		b.WriteString(html.EscapeString(collapseExcessBlankLines(failLines.String())))
		b.WriteString("</pre></blockquote>")
	}
	return b.String()
}

// isFailureStatus decides whether a check result needs to appear in the detail
// block. Anything that is not an explicit success is treated as failure-ish, so
// conditions like "expires soon" or "stale" are never silently hidden.
func isFailureStatus(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	if l == "" {
		return false
	}
	switch l {
	case "ok", "pass", "passed", "up", "healthy", "fine", "true", "yes":
		return false
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small map: insertion sort keeps this dependency-free and deterministic.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
