package runner

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"

	"switch-monitor/internal/adapter"
)

// dnatSender is the slice of the Telegram client used by the iKuai handlers.
// It exists so the command flow can be tested without the network.
type dnatSender interface {
	SendMessage(ctx context.Context, chatID, text string) error
	SendMessageHTML(ctx context.Context, chatID, html string) error
}

// DNAT command help shown on parse errors.
const dnatUsage = `⚠️ iKuai port-mapping usage (策略中心 → NAT → 端口映射)

<b>List / switch</b>
<code>/list_dnat</code>
<code>/enable_dnat &lt;id&gt;</code>  <code>/disable_dnat &lt;id&gt;</code>

<b>Add</b>
<code>/add_dnat &lt;name&gt; &lt;wan_port&gt; &lt;lan_addr&gt; &lt;lan_port&gt; [tcp|udp|tcp+udp] [comment...]</code>

<b>Edit</b>
<code>/edit_dnat &lt;id&gt; [name=&lt;n&gt;] [wan=&lt;p&gt;] [lan=&lt;ip&gt;] [lanport=&lt;p&gt;] [proto=&lt;tcp|udp|tcp+udp&gt;] [comment=&lt;text&gt;]</code>

<b>Delete</b>
<code>/del_dnat &lt;id&gt; [id...]</code>

Notes: name ≤15 chars, no spaces (use '-'); comment ≤64 chars;
wan/lan ports accept <code>8080</code>, <code>8000-8010</code> or <code>8080,8081</code>;
protocol defaults to tcp.`

// isIkuaiCommand reports whether text is an iKuai DNAT command.
func isIkuaiCommand(text string) bool {
	first := strings.Fields(strings.TrimSpace(text))
	if len(first) == 0 {
		return false
	}
	switch first[0] {
	case "/list_dnat", "/enable_dnat", "/disable_dnat", "/add_dnat", "/edit_dnat", "/del_dnat", "/del_dnat_confirm", "/dnat_help":
		return true
	default:
		return false
	}
}

// dnatFieldFormatter normalises free text coming from a chat message so it can
// pass iKuai's own validation: whitespace becomes '-', unsupported characters
// are dropped, and the result is truncated to max runes.
func dnatFieldFormatter(s string, max int, allowSpace bool) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
			lastSpace = false
		case isCJKRune(r):
			b.WriteRune(r)
			lastSpace = false
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if allowSpace {
				if !lastSpace {
					b.WriteRune(' ')
					lastSpace = true
				}
			} else if !lastSpace && b.Len() > 0 {
				b.WriteRune('-')
				lastSpace = true
			}
		}
	}
	out := []rune(strings.Trim(strings.TrimSpace(b.String()), "-"))
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return strings.Trim(strings.TrimSpace(string(out)), "-")
}

func isCJKRune(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0xF900 && r <= 0xFAFF)
}

// formatDNATRule renders one rule as a multi-line HTML block.
func formatDNATRule(rule adapter.DNATRule) string {
	state := "⛔ disabled"
	if rule.Enabled() == "yes" {
		state = "✅ enabled"
	}
	name := rule.Tagname()
	if name == "" {
		name = "(no name)"
	}
	comment := rule.Comment()
	if comment == "" {
		comment = "-"
	}
	iface := rule.String("interface")
	if iface == "" {
		iface = "all"
	}
	proto := rule.String("protocol")
	if proto == "" {
		proto = "tcp"
	}
	return fmt.Sprintf(
		"<b>#%d %s</b>  %s\n<code>%s:%s → %s:%s</code>\ninterface: <code>%s</code> · comment: <code>%s</code>",
		rule.ID(),
		html.EscapeString(name),
		state,
		html.EscapeString(iface),
		html.EscapeString(rule.String("wan_port")),
		html.EscapeString(rule.String("lan_addr")),
		html.EscapeString(rule.String("lan_port")),
		html.EscapeString(proto),
		html.EscapeString(comment),
	)
}

// specFromAddArgs parses:
//
//	<name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]
//
// The trailing comment is optional; a token after lan_port is treated as the
// protocol only when it looks like one (tcp / udp / tcp+udp).
func specFromAddArgs(args []string) (adapter.DNATRuleSpec, error) {
	if len(args) < 4 {
		return adapter.DNATRuleSpec{}, fmt.Errorf("need at least 4 arguments: <name> <wan_port> <lan_addr> <lan_port>")
	}
	spec := adapter.DNATRuleSpec{
		Name:     dnatFieldFormatter(args[0], adapter.DNATMaxTagnameLen, false),
		WanPort:  strings.TrimSpace(args[1]),
		LanAddr:  strings.TrimSpace(args[2]),
		LanPort:  strings.TrimSpace(args[3]),
		Protocol: "tcp",
	}
	rest := args[4:]
	if len(rest) > 0 && isDNATProtocol(rest[0]) {
		spec.Protocol = strings.ToLower(rest[0])
		rest = rest[1:]
	}
	if len(rest) > 0 {
		spec.Comment = dnatFieldFormatter(strings.Join(rest, " "), adapter.DNATMaxCommentLen, true)
	}
	if spec.Name == "" {
		return adapter.DNATRuleSpec{}, fmt.Errorf("name is required and must contain letters, digits, CJK, '-' or '_'")
	}
	return spec, nil
}

func isDNATProtocol(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tcp", "udp", "tcp+udp", "tcp/udp":
		return true
	default:
		return false
	}
}

// specFromEditArgs parses "key=value" pairs into a partial spec. Only the
// fields present in the message are set, so everything else keeps its value.
func specFromEditArgs(args []string) (adapter.DNATRuleSpec, error) {
	var spec adapter.DNATRuleSpec
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			if isDNATProtocol(arg) {
				spec.Protocol = strings.ToLower(arg)
				continue
			}
			return spec, fmt.Errorf("unexpected token %q (use key=value, e.g. wan=8080)", arg)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name", "tagname":
			spec.Name = dnatFieldFormatter(value, adapter.DNATMaxTagnameLen, false)
		case "wan", "wan_port":
			spec.WanPort = value
		case "lan", "lan_addr":
			spec.LanAddr = value
		case "lanport", "lan_port":
			spec.LanPort = value
		case "proto", "protocol":
			if !isDNATProtocol(value) {
				return spec, fmt.Errorf("invalid protocol %q (use tcp, udp or tcp+udp)", value)
			}
			spec.Protocol = strings.ToLower(value)
		case "comment":
			spec.Comment = dnatFieldFormatter(value, adapter.DNATMaxCommentLen, true)
		case "iface", "interface":
			spec.Interface = value
		case "src":
			if value == "" {
				spec.SrcAddr = []string{}
			} else {
				spec.SrcAddr = strings.Split(value, ",")
			}
		case "enabled", "enable":
			on := value == "" || value == "1" || strings.EqualFold(value, "yes") || strings.EqualFold(value, "true") || strings.EqualFold(value, "on")
			spec.Enabled = &on
		default:
			return spec, fmt.Errorf("unknown field %q (name, wan, lan, lanport, proto, comment, iface, src, enabled)", key)
		}
	}
	return spec, nil
}

// splitEditMessage re-joins free-text values (comment=, name=, src=) that were
// split on whitespace, so "/edit_dnat 6 name=my nas comment=hello world" parses
// as two fields instead of five. Everything else stays one token per field.
func splitEditMessage(rest string) []string {
	fields := strings.Fields(rest)
	textKeys := []string{"comment=", "name=", "tagname=", "src="}
	known := []string{"name=", "tagname=", "wan=", "wan_port=", "lan=", "lan_addr=", "lanport=", "lan_port=", "proto=", "protocol=", "iface=", "interface=", "src=", "enabled=", "enable=", "comment="}
	isKey := func(tok string) bool {
		lower := strings.ToLower(tok)
		for _, k := range known {
			if strings.HasPrefix(lower, k) {
				return true
			}
		}
		return false
	}

	var out []string
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		lower := strings.ToLower(field)
		var matched string
		for _, k := range textKeys {
			if strings.HasPrefix(lower, k) {
				matched = k
				break
			}
		}
		if matched == "" {
			out = append(out, field)
			continue
		}
		value := field[len(matched):]
		for j := i + 1; j < len(fields); j++ {
			if isKey(fields[j]) {
				break
			}
			value += " " + fields[j]
			i = j
		}
		out = append(out, matched+value)
	}
	return out
}

// handleIkuaiTelegram dispatches all iKuai DNAT commands.
func (r *Runner) handleIkuaiTelegram(ctx context.Context, client dnatSender, chatIDStr, text string, chatID int64) {
	if r.cfg.Ikuai == nil || !r.cfg.Ikuai.Enabled {
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ iKuai integration is not enabled in config.")
		return
	}

	first := strings.Fields(text)[0]
	if first == "/dnat_help" {
		_ = client.SendMessageHTML(ctx, chatIDStr, dnatUsage)
		return
	}

	ikuai, err := adapter.NewIkuaiClient(r.cfg.Ikuai.URL, r.cfg.Ikuai.Username, r.cfg.Ikuai.Password)
	if err != nil {
		_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to init iKuai client: "+err.Error())
		return
	}
	if err := ikuai.Login(); err != nil {
		_ = client.SendMessage(ctx, chatIDStr, "❌ iKuai login failed: "+err.Error())
		return
	}

	switch first {
	case "/list_dnat":
		rules, err := ikuai.GetDNATRules()
		if err != nil {
			slog.Error("Failed to fetch iKuai DNAT rules via Telegram", "chat_id", chatID, "error", err)
			_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to fetch DNAT rules: "+err.Error())
			return
		}
		table := FormatDNATRulesTable(rules)
		msg := fmt.Sprintf("📋 <b>iKuai DNAT Rules</b> (%d)\n<pre>%s</pre>", len(rules), html.EscapeString(table))
		_ = client.SendMessageHTML(ctx, chatIDStr, msg)

	case "/add_dnat":
		args := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, first)))
		if len(args) == 0 {
			_ = client.SendMessageHTML(ctx, chatIDStr, dnatUsage)
			return
		}
		spec, err := specFromAddArgs(args)
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ "+err.Error()+"\n\n"+stripHTML(dnatUsage))
			return
		}
		id, err := ikuai.CreateDNATRule(spec)
		if err != nil {
			slog.Error("Failed to create iKuai DNAT rule via Telegram", "chat_id", chatID, "name", spec.Name, "wan_port", spec.WanPort, "error", err)
			_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to add DNAT rule: "+err.Error())
			return
		}
		slog.Info("Created iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_id", id, "name", spec.Name, "wan_port", spec.WanPort, "lan", spec.LanAddr+":"+spec.LanPort)
		msg := "✅ <b>Port mapping created</b>\n"
		if id > 0 {
			msg += fmt.Sprintf("rule id: <code>%d</code>\n", id)
		}
		msg += fmt.Sprintf("name: <code>%s</code>\n<code>%s:%s → %s:%s</code> (<code>%s</code>)",
			html.EscapeString(spec.Name),
			html.EscapeString(spec.WanPort), html.EscapeString(spec.WanPort),
			html.EscapeString(spec.LanAddr), html.EscapeString(spec.LanPort),
			html.EscapeString(spec.Protocol))
		if spec.Comment != "" {
			msg += fmt.Sprintf("\ncomment: <code>%s</code>", html.EscapeString(spec.Comment))
		}
		_ = client.SendMessageHTML(ctx, chatIDStr, msg)

	case "/edit_dnat":
		rest := strings.TrimSpace(strings.TrimPrefix(text, first))
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			_ = client.SendMessageHTML(ctx, chatIDStr, dnatUsage)
			return
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "❌ Invalid ID: "+fields[0])
			return
		}
		spec, err := specFromEditArgs(splitEditMessage(strings.Join(fields[1:], " ")))
		if err != nil {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ "+err.Error()+"\n\n"+stripHTML(dnatUsage))
			return
		}
		if err := ikuai.UpdateDNATRule(id, spec); err != nil {
			slog.Error("Failed to edit iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_id", id, "error", err)
			_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("❌ Failed to edit rule %d: %v", id, err))
			return
		}
		slog.Info("Edited iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_id", id)
		msg := fmt.Sprintf("✅ Rule %d updated.", id)
		if rule, err := ikuai.GetDNATRules(); err == nil {
			for _, cand := range rule {
				if cand.ID() == id {
					msg += "\n" + formatDNATRule(cand)
					break
				}
			}
		}
		_ = client.SendMessageHTML(ctx, chatIDStr, msg)

	case "/del_dnat":
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, first)))
		if len(parts) == 0 {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /del_dnat <id> [id...]  (confirm with /del_dnat_confirm <id> ...)")
			return
		}
		ids := make([]int, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimPrefix(p, "#")
			id, err := strconv.Atoi(p)
			if err != nil {
				_ = client.SendMessage(ctx, chatIDStr, "❌ Invalid ID: "+p)
				return
			}
			ids = append(ids, id)
		}
		r.pendingDNATDeletes[chatID] = ids
		msg := "⚠️ <b>Confirm delete</b>\n"
		if rules, err := ikuai.GetDNATRules(); err == nil {
			for _, rule := range rules {
				for _, id := range ids {
					if rule.ID() == id {
						msg += formatDNATRule(rule) + "\n"
					}
				}
			}
		}
		msg += fmt.Sprintf("\nThis cannot be undone. Send <code>/del_dnat_confirm %s</code> to delete.", joinInts(ids, " "))
		_ = client.SendMessageHTML(ctx, chatIDStr, msg)

	case "/enable_dnat", "/disable_dnat":
		parts := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, first)))
		if len(parts) == 0 {
			_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /enable_dnat <id> or /disable_dnat <id>")
			return
		}
		enable := first == "/enable_dnat"
		action := "disabling"
		if enable {
			action = "enabling"
		}
		var sb strings.Builder
		failed := false
		for _, p := range parts {
			p = strings.TrimPrefix(p, "#")
			id, err := strconv.Atoi(p)
			if err != nil {
				_ = client.SendMessage(ctx, chatIDStr, "❌ Invalid ID: "+p)
				failed = true
				continue
			}
			if err := ikuai.ToggleDNATRule(id, enable); err != nil {
				slog.Error("Failed to toggle iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_id", id, "enable", enable, "error", err)
				sb.WriteString(fmt.Sprintf("❌ Error %s rule %d: %v\n", action, id, err))
				failed = true
				continue
			}
			status := "disabled"
			if enable {
				status = "enabled"
			}
			slog.Info("Toggled iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_id", id, "status", status)
			sb.WriteString(fmt.Sprintf("✅ Rule %d has been %s.\n", id, status))
		}
		msg := sb.String()
		if !failed {
			msg = strings.TrimRight(msg, "\n")
		}
		_ = client.SendMessage(ctx, chatIDStr, msg)
	}
}

// handleDNATDeleteConfirmation runs /del_dnat_confirm after /del_dnat.
func (r *Runner) handleDNATDeleteConfirmation(ctx context.Context, client dnatSender, chatIDStr, text string, chatID int64) bool {
	if !strings.HasPrefix(text, "/del_dnat_confirm") {
		return false
	}
	return r.confirmDNATDelete(ctx, client, chatIDStr, chatID)
}

// confirmDNATDelete executes the delete staged by /del_dnat.
func (r *Runner) confirmDNATDelete(ctx context.Context, client dnatSender, chatIDStr string, chatID int64) bool {
	if r.cfg.Ikuai == nil || !r.cfg.Ikuai.Enabled {
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ iKuai integration is not enabled in config.")
		return true
	}
	pending, ok := r.pendingDNATDeletes[chatID]
	if !ok || len(pending) == 0 {
		_ = client.SendMessage(ctx, chatIDStr, "⚠️ Nothing to confirm. Use /del_dnat <id> first.")
		return true
	}
	delete(r.pendingDNATDeletes, chatID)

	ikuai, err := adapter.NewIkuaiClient(r.cfg.Ikuai.URL, r.cfg.Ikuai.Username, r.cfg.Ikuai.Password)
	if err != nil {
		_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to init iKuai client: "+err.Error())
		return true
	}
	if err := ikuai.Login(); err != nil {
		_ = client.SendMessage(ctx, chatIDStr, "❌ iKuai login failed: "+err.Error())
		return true
	}
	if err := ikuai.DeleteDNATRule(pending...); err != nil {
		slog.Error("Failed to delete iKuai DNAT rule via Telegram", "chat_id", chatID, "rule_ids", pending, "error", err)
		_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to delete: "+err.Error())
		return true
	}
	slog.Info("Deleted iKuai DNAT rules via Telegram", "chat_id", chatID, "rule_ids", pending)
	_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("✅ Deleted rule(s): %s", joinInts(pending, ", ")))
	return true
}

func joinInts(v []int, sep string) string {
	parts := make([]string, 0, len(v))
	for _, n := range v {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, sep)
}

// stripHTML removes the tags used in dnatUsage so it can be sent as plain text.
func stripHTML(s string) string {
	replacer := strings.NewReplacer("<b>", "", "</b>", "", "<code>", "", "</code>", "", "<pre>", "", "</pre>", "")
	return replacer.Replace(s)
}
