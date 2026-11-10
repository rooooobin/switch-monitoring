package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"switch-monitor/internal/alerting"
	"switch-monitor/internal/config"
)

// RunAlert handles: preview [title] [body...] | show <title> <body...>
//
// It exists so the Telegram HTML rendering can be inspected locally (in a
// terminal, or pasted into Telegram's own preview) without sending a real
// message or waiting for an inbound alert.
func RunAlert(_ context.Context, cfg *config.MonitorConfig, args []string) error {
	// Bare `alert` prints a sample of every severity so the formatting can be
	// eyeballed in one run.
	if len(args) == 0 {
		return alertPreview(cfg, nil, os.Stdout)
	}
	switch args[0] {
	case "preview", "show":
		return alertPreview(cfg, args[1:], os.Stdout)
	case "render-webhook":
		// Render the exact message an inbound webhook call would produce.
		return alertPreview(cfg, args[1:], os.Stdout)
	default:
		return fmt.Errorf("unknown alert subcommand %q", args[0])
	}
}

func alertPreview(cfg *config.MonitorConfig, args []string, w io.Writer) error {
	renderer := alerting.NewWebhookRenderer()

	var title, body, host, severity string
	if len(args) == 0 {
		// No arguments: emit a few canned samples covering each severity so the
		// formatting can be eyeballed in one run.
		samples := []struct{ title, body, host, sev string }{
			{"unit failed: mihomo.service", "Main process exited, code=exited, status=1/FAILURE", "surface", "error"},
			{"disk /data at 94%", "/dev/nvme0n1p2  73G  68G  1.1G  99% /data", "surface", "critical"},
			{"证书将在 21 天内到期", "subject=CN=rooooobin.me\nnotAfter=Feb  7 05:51:57 2027 GMT", "r2s", "warning"},
			{"all checks passed", "", "surface", "ok"},
			{"nightly summary", "2 switches polled, 0 issues, 16 ports up.", "r2s", ""},
		}
		for i, s := range samples {
			if i > 0 {
				_, _ = fmt.Fprintln(w, "\n"+strings.Repeat("-", 60))
			}
			_, _ = fmt.Fprintln(w, renderer.Render(s.title, s.body, s.host, s.sev))
		}
		return nil
	}

	title = args[0]
	if len(args) > 1 {
		body = strings.Join(args[1:], " ")
	}
	if v := os.Getenv("ALERT_HOST"); v != "" {
		host = v
	} else {
		host = "cli"
	}
	severity = os.Getenv("ALERT_SEVERITY")

	_, _ = fmt.Fprintln(w, renderer.Render(title, body, host, severity))
	return nil
}
