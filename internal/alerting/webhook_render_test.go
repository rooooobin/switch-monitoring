package alerting

import (
	"strings"
	"testing"
)

func TestSeverityStyleExplicit(t *testing.T) {
	cases := map[string]string{
		"critical":  "🛑",
		"error":     "❌",
		"warning":   "⚠️",
		"ok":        "✅",
		"info":      "ℹ️",
		"ERROR":     "❌",  // case-insensitive
		"  warning": "⚠️", // trimmed
	}
	for in, wantBadge := range cases {
		badge, _ := severityStyle(in, "", "")
		if badge != wantBadge {
			t.Errorf("severityStyle(%q) badge = %q, want %q", in, badge, wantBadge)
		}
	}
}

// When the caller omits severity, the badge must be inferred from the text so
// the first line still answers "do I need to act on this".
func TestSeverityStyleInferred(t *testing.T) {
	cases := []struct{ title, body, wantBadge string }{
		{"disk /data critical", "", "🛑"},
		{"mihomo listener is DOWN", "", "❌"},
		{"certificate expires soon", "", "⚠️"},
		{"all checks passed", "", "✅"},
		{"nightly summary", "nothing to report", "ℹ️"},
		{"", "connection refused on 7894", "❌"},
	}
	for _, c := range cases {
		badge, _ := severityStyle("", c.title, c.body)
		if badge != c.wantBadge {
			t.Errorf("infer(%q / %q) badge = %q, want %q", c.title, c.body, badge, c.wantBadge)
		}
	}
}

func TestRenderEscapesHTML(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.Render(`watch <script>alert("x")</script> & more`, "body <b>not bold</b> & <pre>", "surface", "error")
	if strings.Contains(msg, "<script>") {
		t.Fatalf("script tag not escaped: %q", msg)
	}
	if strings.Contains(msg, "<b>not bold</b>") {
		t.Fatalf("body markup not escaped: %q", msg)
	}
	if !strings.Contains(msg, "&lt;script&gt;") {
		t.Fatalf("expected escaped title, got %q", msg)
	}
	if !strings.Contains(msg, "&amp;") {
		t.Fatalf("expected escaped ampersand, got %q", msg)
	}
}

func TestRenderHeaderAndMeta(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.Render("unit failed: mihomo", "service exited 1", "surface", "error")
	first := strings.SplitN(msg, "\n", 2)[0]
	if !strings.HasPrefix(first, "❌ <b>unit failed: mihomo</b>") {
		t.Fatalf("unexpected first line: %q", first)
	}
	if !strings.Contains(msg, "ERROR") || !strings.Contains(msg, "<code>surface</code>") {
		t.Fatalf("meta line missing severity/host: %q", msg)
	}
}

// A title-less alert (very common from shell scripts piping to curl --data-binary)
// must still get a meaningful header taken from the first body line.
func TestRenderDerivesTitleFromBody(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.Render("", "disk /data at 94%\nplease free space", "", "")
	if !strings.Contains(msg, "<b>disk /data at 94%</b>") {
		t.Fatalf("title not derived: %q", msg)
	}
	if strings.Contains(msg, "<b>disk /data at 94%\n") {
		t.Fatalf("title must be one line: %q", msg)
	}
	if !strings.Contains(msg, "please free space") {
		t.Fatalf("remaining body lost: %q", msg)
	}
}

// Log-like bodies (indented tables, long journal lines) keep alignment in <pre>.
func TestRenderPreformattedForLogs(t *testing.T) {
	r := NewWebhookRenderer()
	body := "  CPU: 12%\n  MEM: 40%\n  DISK: 91%"
	msg := r.Render("host stats", body, "surface", "warning")
	if !strings.Contains(msg, "<pre>") {
		t.Fatalf("expected <pre> for indented body: %q", msg)
	}
	if !strings.Contains(msg, "  CPU: 12%") {
		t.Fatalf("indentation not preserved: %q", msg)
	}
}

func TestRenderProseNotPreformatted(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.Render("notice", "The proxy certificate was renewed successfully and the listener reloaded.", "r2s", "ok")
	if strings.Contains(msg, "<pre>") {
		t.Fatalf("prose should not be in <pre>: %q", msg)
	}
}

// Long prose must be collapsed behind an expandable blockquote so the chat
// preview stays short.
func TestRenderLongBodyUsesExpandableQuote(t *testing.T) {
	r := NewWebhookRenderer()
	body := strings.Repeat("this alert has a very long explanation. ", 20)
	msg := r.Render("verbose", body, "surface", "warning")
	if !strings.Contains(msg, "<blockquote expandable>") {
		t.Fatalf("expected expandable blockquote for long body: %q", msg)
	}
	if !strings.Contains(msg, "</blockquote>") {
		t.Fatalf("unclosed blockquote: %q", msg)
	}
}

// Wrapped prose is reflowed into paragraphs; intentional blank lines survive.
func TestCollapseSpacesReflowsParagraphs(t *testing.T) {
	got := collapseSpaces("line one\nline two\n\nsecond paragraph\n  indented tail  ")
	want := "line one line two\n\nsecond paragraph indented tail"
	if got != want {
		t.Fatalf("collapseSpaces = %q, want %q", got, want)
	}
}

func TestCollapseExcessBlankLines(t *testing.T) {
	got := collapseExcessBlankLines("a\n\n\n\nb\n\n")
	want := "a\n\nb"
	if got != want {
		t.Fatalf("collapseExcessBlankLines = %q, want %q", got, want)
	}
}

func TestRenderHealthSummary(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.RenderHealthSummary("surface", map[string]string{
		"disk":     "ok",
		"cert":     "expires soon",
		"listener": "DOWN",
	})
	if !strings.Contains(msg, "1 ok") || !strings.Contains(msg, "2 failed") {
		t.Fatalf("counts wrong: %q", msg)
	}
	// "expires soon" is not an explicit success, so it must be surfaced rather
	// than silently counted as healthy.
	for _, want := range []string{"cert: expires soon", "listener: DOWN"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("failure detail %q missing: %q", want, msg)
		}
	}
	if strings.Contains(msg, "disk: ok") {
		t.Fatalf("passing checks should not be listed: %q", msg)
	}
}

func TestRenderHealthSummaryAllOK(t *testing.T) {
	r := NewWebhookRenderer()
	msg := r.RenderHealthSummary("surface", map[string]string{"disk": "ok", "cert": "ok"})
	if strings.Contains(msg, "blockquote") {
		t.Fatalf("no failures should mean no detail block: %q", msg)
	}
	if !strings.Contains(msg, "2 ok") {
		t.Fatalf("counts wrong: %q", msg)
	}
}

// Telegram rejects messages over 4096 characters; the webhook truncates the body
// well before that, but the renderer must not amplify a body into a longer message.
func TestRenderDoesNotAmplifyBody(t *testing.T) {
	r := NewWebhookRenderer()
	body := strings.Repeat("x", 4000)
	msg := r.Render("t", body, "h", "info")
	if len(msg) > 4000+500 {
		t.Fatalf("message grew unexpectedly: %d bytes", len(msg))
	}
}

// Multi-line key=value records (openssl/df/systemctl output) must keep their own
// lines instead of being reflowed into one sentence.
func TestRenderKeyValueRecordsKeepLines(t *testing.T) {
	r := NewWebhookRenderer()
	body := "subject=CN=rooooobin.me\nnotAfter=Feb  7 05:51:57 2027 GMT"
	msg := r.Render("证书即将到期", body, "r2s", "warning")
	if !strings.Contains(msg, "<pre>") {
		t.Fatalf("expected <pre> for key=value record: %q", msg)
	}
	if !strings.Contains(msg, "subject=CN=rooooobin.me\nnotAfter=") {
		t.Fatalf("lines were merged: %q", msg)
	}
}
