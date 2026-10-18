package runner

import (
	"regexp"
	"strings"
	"testing"

	"switch-monitor/internal/adapter"
)

func TestIsIkuaiCommand(t *testing.T) {
	yes := []string{
		"/list_dnat", "/enable_dnat 3", "/disable_dnat 3",
		"/add_dnat web 8080 192.168.2.10 80", "/edit_dnat 6 wan=8080",
		"/del_dnat 6", "/del_dnat_confirm 6", "/dnat_help",
	}
	for _, text := range yes {
		if !isIkuaiCommand(text) {
			t.Errorf("%q should be an iKuai command", text)
		}
	}
	no := []string{"/check", "/list_proxy", "/xiaodu_status", "/list_dnatx", "", "   "}
	for _, text := range no {
		if isIkuaiCommand(text) {
			t.Errorf("%q should not be an iKuai command", text)
		}
	}
}

func TestDNATFieldFormatter(t *testing.T) {
	cases := []struct {
		in         string
		max        int
		allowSpace bool
		want       string
	}{
		{"web-ssh", 15, false, "web-ssh"},
		{"web ssh", 15, false, "web-ssh"},
		{"  群晖 ssh  ", 15, false, "群晖-ssh"},
		{"nas.ssh", 15, false, "nasssh"},
		{"a/b:c", 15, false, "abc"},
		{"abcdefghijklmnopqrst", 15, false, "abcdefghijklmno"},
		{"中文备注 with spaces", 64, true, "中文备注 with spaces"},
		{"multi   space", 64, true, "multi space"},
		{"a+b&c", 64, true, "abc"},
		{"", 15, false, ""},
		{"---", 15, false, ""},
	}
	for _, tc := range cases {
		if got := dnatFieldFormatter(tc.in, tc.max, tc.allowSpace); got != tc.want {
			t.Errorf("dnatFieldFormatter(%q, %d, %v) = %q, want %q", tc.in, tc.max, tc.allowSpace, got, tc.want)
		}
	}
}

func TestSpecFromAddArgs(t *testing.T) {
	spec, err := specFromAddArgs([]string{"nas-ssh", "60022", "192.168.2.10", "22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "nas-ssh" || spec.WanPort != "60022" || spec.LanAddr != "192.168.2.10" || spec.LanPort != "22" {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	if spec.Protocol != "tcp" {
		t.Errorf("default protocol = %q, want tcp", spec.Protocol)
	}

	spec, err = specFromAddArgs([]string{"web", "18080", "192.168.2.20", "8080", "tcp+udp", "办公", "web", "服务"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Protocol != "tcp+udp" {
		t.Errorf("protocol = %q, want tcp+udp", spec.Protocol)
	}
	if spec.Comment != "办公 web 服务" {
		t.Errorf("comment = %q", spec.Comment)
	}

	// A token after lan_port that is not a protocol starts the comment.
	spec, err = specFromAddArgs([]string{"web", "18080", "192.168.2.20", "8080", "my", "service"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Protocol != "tcp" || spec.Comment != "my service" {
		t.Fatalf("protocol/comment = %q/%q", spec.Protocol, spec.Comment)
	}

	// Name with spaces is normalised to dashes rather than rejected.
	spec, err = specFromAddArgs([]string{"my nas", "60022", "192.168.2.10", "22"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "my-nas" {
		t.Errorf("name = %q, want my-nas", spec.Name)
	}

	if _, err := specFromAddArgs([]string{"only-name", "8080"}); err == nil {
		t.Error("expected error for too few arguments")
	}
	if _, err := specFromAddArgs([]string{"!!!", "8080", "192.168.2.10", "80"}); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestSpecFromAddArgs_ValidatesThroughAdapter(t *testing.T) {
	spec, err := specFromAddArgs([]string{"web", "8080", "192.168.2.10", "80"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateDNATRuleSpec(spec); err != nil {
		t.Fatalf("adapter rejected parsed spec: %v", err)
	}

	spec, err = specFromAddArgs([]string{"web", "8080", "not-an-ip", "80"})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateDNATRuleSpec(spec); err == nil {
		t.Fatal("expected adapter validation error for bad IP")
	}
}

func TestSplitEditMessage(t *testing.T) {
	got := splitEditMessage("name=new web wan=8080 comment=hello world lan=192.168.2.5")
	want := []string{"name=new web", "wan=8080", "comment=hello world", "lan=192.168.2.5"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("splitEditMessage = %#v, want %#v", got, want)
	}

	got = splitEditMessage("comment=takes everything to the end")
	if len(got) != 1 || got[0] != "comment=takes everything to the end" {
		t.Fatalf("single comment = %#v", got)
	}

	got = splitEditMessage("wan=8080 comment=a b proto=udp")
	if strings.Join(got, "|") != "wan=8080|comment=a b|proto=udp" {
		t.Fatalf("mixed = %#v", got)
	}
}

func TestSpecFromEditArgs(t *testing.T) {
	spec, err := specFromEditArgs([]string{"comment=hello", "wan=8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Comment != "hello" || spec.WanPort != "8080" {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	// Untouched fields must stay empty so the adapter can copy them from the
	// existing rule instead of blanking them.
	if spec.Name != "" || spec.LanAddr != "" || spec.LanPort != "" || spec.Protocol != "" || spec.Enabled != nil {
		t.Fatalf("edit spec must only set supplied fields: %#v", spec)
	}

	spec, err = specFromEditArgs([]string{"name=my rule", "proto=tcp+udp", "enabled=no", "src=203.0.113.0/24"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Name != "my-rule" {
		t.Errorf("name = %q, want my-rule", spec.Name)
	}
	if spec.Protocol != "tcp+udp" || spec.Enabled == nil || *spec.Enabled {
		t.Errorf("proto/enabled = %q/%v", spec.Protocol, spec.Enabled)
	}
	if len(spec.SrcAddr) != 1 || spec.SrcAddr[0] != "203.0.113.0/24" {
		t.Errorf("src = %#v", spec.SrcAddr)
	}

	if _, err := specFromEditArgs([]string{"proto=icmp"}); err == nil {
		t.Error("expected error for invalid protocol")
	}
	if _, err := specFromEditArgs([]string{"bogus=1"}); err == nil {
		t.Error("expected error for unknown field")
	}
	if _, err := specFromEditArgs([]string{"noequals"}); err == nil {
		t.Error("expected error for token without '='")
	}
}

func TestFormatDNATRule(t *testing.T) {
	rule := adapter.DNATRule{
		"id":        float64(6),
		"enabled":   "yes",
		"tagname":   "win11-ssh",
		"comment":   "",
		"interface": "wan1",
		"protocol":  "tcp",
		"wan_port":  "18660",
		"lan_addr":  "192.168.2.170",
		"lan_port":  "22",
	}
	got := formatDNATRule(rule)
	for _, want := range []string{"#6", "win11-ssh", "enabled", "wan1:18660", "192.168.2.170:22", "tcp"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted rule missing %q:\n%s", want, got)
		}
	}

	rule["enabled"] = "no"
	rule["tagname"] = "a<b>c"
	if got := formatDNATRule(rule); !strings.Contains(got, "a&lt;b&gt;c") {
		t.Errorf("tagname not HTML-escaped:\n%s", got)
	}
	if got := formatDNATRule(rule); !strings.Contains(got, "disabled") {
		t.Errorf("disabled state missing:\n%s", got)
	}
}

func TestStripHTML(t *testing.T) {
	in := "<b>List</b> <code>/list_dnat</code>"
	got := stripHTML(in)
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("stripHTML left tags: %q", got)
	}
	if !strings.Contains(got, "/list_dnat") {
		t.Fatalf("stripHTML lost content: %q", got)
	}
}

func TestJoinInts(t *testing.T) {
	if got := joinInts([]int{3, 7, 11}, ","); got != "3,7,11" {
		t.Fatalf("joinInts = %q", got)
	}
	if got := joinInts(nil, ","); got != "" {
		t.Fatalf("joinInts(nil) = %q", got)
	}
}

func TestTelegramCommandsMenu(t *testing.T) {
	cmds := TelegramCommands()
	if len(cmds) == 0 {
		t.Fatal("empty command menu")
	}

	validName := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	seen := map[string]bool{}
	for _, c := range cmds {
		if !validName.MatchString(c.Command) {
			t.Errorf("invalid Telegram command name %q", c.Command)
		}
		if seen[c.Command] {
			t.Errorf("duplicate command %q", c.Command)
		}
		seen[c.Command] = true
		if n := len([]rune(c.Description)); n == 0 || n > 256 {
			t.Errorf("command %q description length %d out of range", c.Command, n)
		}
		if strings.Contains(c.Description, "\n") {
			t.Errorf("command %q description must be single-line", c.Command)
		}
	}

	// Every DNAT command the poller accepts must be discoverable in the menu.
	for _, want := range []string{"add_dnat", "edit_dnat", "del_dnat", "del_dnat_confirm", "dnat_help", "list_dnat", "enable_dnat", "disable_dnat"} {
		if !seen[want] {
			t.Errorf("command %q missing from menu", want)
		}
	}
	// And the menu must not advertise commands the poller ignores.
	for _, c := range cmds {
		if seen[c.Command] && strings.HasSuffix(c.Command, "_dnat") || c.Command == "del_dnat_confirm" || c.Command == "dnat_help" {
			text := "/" + c.Command
			if !isIkuaiCommand(text) {
				t.Errorf("menu advertises %q but isIkuaiCommand does not accept it", c.Command)
			}
		}
	}

	rendered := TelegramCommandsBotFather()
	for _, line := range strings.Split(strings.TrimSpace(rendered), "\n") {
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			t.Errorf("botfather line not in 'command - description' form: %q", line)
			continue
		}
		if !seen[parts[0]] {
			t.Errorf("botfather line for unknown command %q", parts[0])
		}
	}
	if !strings.HasSuffix(rendered, "\n") {
		t.Error("botfather text should end with a newline")
	}
}
