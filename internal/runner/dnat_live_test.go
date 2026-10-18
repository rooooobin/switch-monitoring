//go:build integration

package runner

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"switch-monitor/internal/config"
)

// fakeSender captures outgoing Telegram messages so the command flow can be
// driven end-to-end without touching the Bot API.
type fakeSender struct {
	mu   sync.Mutex
	msgs []string
}

func (f *fakeSender) SendMessage(_ context.Context, _, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, text)
	return nil
}

func (f *fakeSender) SendMessageHTML(_ context.Context, _, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, html)
	return nil
}

func (f *fakeSender) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) == 0 {
		return ""
	}
	return f.msgs[len(f.msgs)-1]
}

var ruleIDRe = regexp.MustCompile(`rule id:.*?(\d+)`)

// Run against a live iKuai router:
//
//	IKUAI_URL=... IKUAI_USER=... IKUAI_PASS=... \
//	  go test -tags=integration -v ./internal/runner -run TestLiveTelegramDNATFlow
func TestLiveTelegramDNATFlow(t *testing.T) {
	url := os.Getenv("IKUAI_URL")
	user := os.Getenv("IKUAI_USER")
	pass := os.Getenv("IKUAI_PASS")
	if url == "" || user == "" || pass == "" {
		t.Skip("set IKUAI_URL, IKUAI_USER, IKUAI_PASS to run live test")
	}

	cfg := &config.MonitorConfig{
		Ikuai: &config.IkuaiConfig{Enabled: true, URL: url, Username: user, Password: pass},
	}
	r := &Runner{cfg: cfg, pendingDNATDeletes: make(map[int64][]int)}
	ctx := t.Context()
	sender := &fakeSender{}
	const chatStr = "42"
	const chatID int64 = 42

	run := func(text string) string {
		t.Helper()
		r.handleIkuaiTelegram(ctx, sender, chatStr, text, chatID)
		return sender.last()
	}

	if reply := run("/add_dnat tg-flow 19556 192.168.2.178 19556 tcp 集成 测试"); !strings.Contains(reply, "Port mapping created") {
		t.Fatalf("add failed: %s", reply)
	}
	match := ruleIDRe.FindStringSubmatch(sender.last())
	if match == nil {
		t.Fatalf("could not parse rule id from: %s", sender.last())
	}
	id, err := strconv.Atoi(match[1])
	if err != nil || id == 0 {
		t.Fatalf("bad rule id %q: %v", match[1], err)
	}
	t.Cleanup(func() {
		r.pendingDNATDeletes[chatID] = []int{id}
		r.confirmDNATDelete(ctx, sender, chatStr, chatID)
	})

	if reply := run("/list_dnat"); !strings.Contains(reply, "tg-flow") {
		t.Fatalf("list missing new rule: %s", reply)
	}

	if reply := run("/edit_dnat " + strconv.Itoa(id) + " comment=edited by telegram"); !strings.Contains(reply, "updated") {
		t.Fatalf("edit failed: %s", reply)
	}

	if reply := run("/disable_dnat " + strconv.Itoa(id)); !strings.Contains(reply, "disabled") {
		t.Fatalf("disable failed: %s", reply)
	}
	if reply := run("/enable_dnat " + strconv.Itoa(id)); !strings.Contains(reply, "enabled") {
		t.Fatalf("enable failed: %s", reply)
	}

	if reply := run("/del_dnat " + strconv.Itoa(id)); !strings.Contains(reply, "Confirm delete") {
		t.Fatalf("delete prompt missing: %s", reply)
	}
	if !r.handleDNATDeleteConfirmation(ctx, sender, chatStr, "/del_dnat_confirm "+strconv.Itoa(id), chatID) {
		t.Fatal("confirm handler did not run")
	}
	if reply := sender.last(); !strings.Contains(reply, "Deleted") {
		t.Fatalf("delete failed: %s", reply)
	}

	// A repeated confirm must be a no-op, not an error in the poll loop.
	if !r.handleDNATDeleteConfirmation(ctx, sender, chatStr, "/del_dnat_confirm", chatID) {
		t.Fatal("empty confirm not handled")
	}
	if reply := sender.last(); !strings.Contains(reply, "Nothing to confirm") {
		t.Fatalf("unexpected empty-confirm reply: %s", reply)
	}

	// Bad input is rejected locally, before any router call.
	if reply := run("/add_dnat web 8080 192.168.2.10"); !strings.Contains(reply, "need at least") {
		t.Fatalf("short add args not rejected: %s", reply)
	}
}
