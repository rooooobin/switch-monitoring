// Package cli implements optional local subcommands (iKuai, Mihomo) without Telegram.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
	"switch-monitor/internal/runner"
)

// PrintSubcommandHelp writes local subcommand usage to w.
func PrintSubcommandHelp(w io.Writer) {
	fmt.Fprint(w, `Local commands (run after global flags such as -config):

	switch-monitor ikuai list-dnat
  switch-monitor ikuai enable-dnat <id>
  switch-monitor ikuai disable-dnat <id>
  switch-monitor ikuai add-dnat <name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]
  switch-monitor ikuai edit-dnat <id> [name=<n>] [wan=<p>] [lan=<ip>] [lanport=<p>] [proto=<p>] [comment=<text>]
  switch-monitor ikuai del-dnat <id> [id...]

  switch-monitor mihomo list-proxy
  switch-monitor mihomo set-proxy <outbound-name>
  switch-monitor mihomo delay-proxy <proxy-name>

  switch-monitor xiaodu status
  switch-monitor xiaodu volume <0-100>
  switch-monitor xiaodu mute | unmute
  switch-monitor xiaodu play <url> [title]
  switch-monitor xiaodu stop | pause | seek <HH:MM:SS|seconds>
  switch-monitor xiaodu tts <text>
  switch-monitor xiaodu say <text>
  switch-monitor xiaodu probe
  switch-monitor xiaodu bduss-check
  switch-monitor xiaodu discover
  switch-monitor xiaodu skill-serve
  switch-monitor xiaodu skill-test <text>
  switch-monitor xiaodu skill-chat
  switch-monitor xiaodu codebuddy-models

  switch-monitor llm usage
  switch-monitor llm deepseek
  switch-monitor llm bailian

Requires ikuai / mihomo / xiaodu / llm_usage sections enabled in the config file (same as Telegram).
Mihomo delay uses latency_test_url and latency_timeout_ms from config when set.
`)
}

// RunIkuai handles: list-dnat | enable-dnat <id> | disable-dnat <id> |
// add-dnat | edit-dnat | del-dnat
func RunIkuai(_ context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.Ikuai == nil || !cfg.Ikuai.Enabled {
		slog.Error("CLI ikuai: integration disabled in config")
		return fmt.Errorf("ikuai is not enabled in config")
	}
	if len(args) < 1 {
		slog.Error("CLI ikuai: missing subcommand")
		return fmt.Errorf("ikuai: expected subcommand (list-dnat, enable-dnat, disable-dnat, add-dnat, edit-dnat, del-dnat)")
	}
	slog.Info("CLI ikuai command", "subcommand", args[0], "args", args[1:], "router_base", cfg.Ikuai.URL)

	ikuai, err := adapter.NewIkuaiClient(cfg.Ikuai.URL, cfg.Ikuai.Username, cfg.Ikuai.Password)
	if err != nil {
		slog.Error("CLI ikuai: client init failed", "err", err)
		return err
	}
	if err := ikuai.Login(); err != nil {
		slog.Error("CLI ikuai: login failed", "err", err)
		return fmt.Errorf("ikuai login: %w", err)
	}

	switch args[0] {
	case "list-dnat":
		rules, err := ikuai.GetDNATRules()
		if err != nil {
			slog.Error("CLI ikuai: list-dnat failed", "err", err)
			return err
		}
		fmt.Println("iKuai DNAT Rules")
		fmt.Println(runner.FormatDNATRulesTable(rules))
		slog.Info("CLI ikuai: list-dnat done", "rule_count", len(rules))
		return nil

	case "enable-dnat", "disable-dnat":
		if len(args) < 2 {
			slog.Error("CLI ikuai: missing rule id", "subcommand", args[0])
			return fmt.Errorf("%s: missing rule id", args[0])
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			slog.Error("CLI ikuai: invalid rule id", "raw", args[1], "err", err)
			return fmt.Errorf("invalid rule id %q", args[1])
		}
		enable := args[0] == "enable-dnat"
		if err := ikuai.ToggleDNATRule(id, enable); err != nil {
			slog.Error("CLI ikuai: toggle failed", "rule_id", id, "enable", enable, "err", err)
			return err
		}
		if enable {
			fmt.Printf("DNAT rule %d enabled.\n", id)
		} else {
			fmt.Printf("DNAT rule %d disabled.\n", id)
		}
		slog.Info("CLI ikuai: toggle done", "rule_id", id, "enabled", enable)
		return nil

	case "add-dnat":
		if len(args) < 5 {
			return fmt.Errorf("add-dnat: usage: add-dnat <name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]")
		}
		spec, err := dnatSpecFromArgs(args[1:])
		if err != nil {
			return fmt.Errorf("add-dnat: %w", err)
		}
		id, err := ikuai.CreateDNATRule(spec)
		if err != nil {
			slog.Error("CLI ikuai: add-dnat failed", "name", spec.Name, "wan_port", spec.WanPort, "err", err)
			return err
		}
		if id > 0 {
			fmt.Printf("DNAT rule %d created (%s: %s → %s:%s).\n", id, spec.Protocol, spec.WanPort, spec.LanAddr, spec.LanPort)
		} else {
			fmt.Printf("DNAT rule created (%s: %s → %s:%s).\n", spec.Protocol, spec.WanPort, spec.LanAddr, spec.LanPort)
		}
		slog.Info("CLI ikuai: add-dnat done", "rule_id", id, "name", spec.Name, "wan_port", spec.WanPort, "lan_addr", spec.LanAddr, "lan_port", spec.LanPort, "protocol", spec.Protocol)
		return nil

	case "edit-dnat":
		if len(args) < 3 {
			return fmt.Errorf("edit-dnat: usage: edit-dnat <id> [name=<n>] [wan=<p>] [lan=<ip>] [lanport=<p>] [proto=<p>] [comment=<text>]")
		}
		id, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid rule id %q", args[1])
		}
		spec, err := dnatEditSpecFromArgs(args[2:])
		if err != nil {
			return fmt.Errorf("edit-dnat: %w", err)
		}
		if err := ikuai.UpdateDNATRule(id, spec); err != nil {
			slog.Error("CLI ikuai: edit-dnat failed", "rule_id", id, "err", err)
			return err
		}
		fmt.Printf("DNAT rule %d updated.\n", id)
		slog.Info("CLI ikuai: edit-dnat done", "rule_id", id)
		return nil

	case "del-dnat":
		if len(args) < 2 {
			return fmt.Errorf("del-dnat: usage: del-dnat <id> [id...]")
		}
		ids := make([]int, 0, len(args)-1)
		for _, raw := range args[1:] {
			id, err := strconv.Atoi(strings.TrimPrefix(raw, "#"))
			if err != nil {
				return fmt.Errorf("invalid rule id %q", raw)
			}
			ids = append(ids, id)
		}
		if err := ikuai.DeleteDNATRule(ids...); err != nil {
			slog.Error("CLI ikuai: del-dnat failed", "rule_ids", ids, "err", err)
			return err
		}
		fmt.Printf("DNAT rule(s) %v deleted.\n", ids)
		slog.Info("CLI ikuai: del-dnat done", "rule_ids", ids)
		return nil

	default:
		slog.Error("CLI ikuai: unknown subcommand", "subcommand", args[0])
		return fmt.Errorf("unknown ikuai subcommand %q", args[0])
	}
}

// dnatSpecFromArgs parses "<name> <wan_port> <lan_addr> <lan_port> [proto] [comment...]".
// Unlike the Telegram path, CLI values are passed through untouched so the
// adapter's validation can report exactly what the operator typed.
func dnatSpecFromArgs(args []string) (adapter.DNATRuleSpec, error) {
	if len(args) < 4 {
		return adapter.DNATRuleSpec{}, fmt.Errorf("need <name> <wan_port> <lan_addr> <lan_port>")
	}
	spec := adapter.DNATRuleSpec{
		Name:     args[0],
		WanPort:  args[1],
		LanAddr:  args[2],
		LanPort:  args[3],
		Protocol: "tcp",
	}
	rest := args[4:]
	if len(rest) > 0 {
		switch strings.ToLower(rest[0]) {
		case "tcp", "udp", "tcp+udp", "tcp/udp":
			spec.Protocol = strings.ToLower(rest[0])
			rest = rest[1:]
		}
	}
	if len(rest) > 0 {
		spec.Comment = strings.Join(rest, " ")
	}
	return spec, nil
}

// dnatEditSpecFromArgs parses key=value pairs into a partial spec.
func dnatEditSpecFromArgs(args []string) (adapter.DNATRuleSpec, error) {
	var spec adapter.DNATRuleSpec
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return spec, fmt.Errorf("expected key=value, got %q", arg)
		}
		switch strings.ToLower(key) {
		case "name", "tagname":
			spec.Name = value
		case "wan", "wan_port":
			spec.WanPort = value
		case "lan", "lan_addr":
			spec.LanAddr = value
		case "lanport", "lan_port":
			spec.LanPort = value
		case "proto", "protocol":
			spec.Protocol = strings.ToLower(value)
		case "comment":
			spec.Comment = value
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
			return spec, fmt.Errorf("unknown field %q", key)
		}
	}
	return spec, nil
}

// RunMihomo handles: list-proxy | set-proxy <outbound>
func RunMihomo(ctx context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.Mihomo == nil || !cfg.Mihomo.Enabled || len(cfg.Mihomo.Instances) == 0 {
		slog.Error("CLI mihomo: integration disabled or no instances")
		return fmt.Errorf("mihomo is not enabled or has no instances in config")
	}
	if len(args) < 1 {
		slog.Error("CLI mihomo: missing subcommand")
		return fmt.Errorf("mihomo: expected subcommand (list-proxy, set-proxy, delay-proxy)")
	}
	slog.Info("CLI mihomo command", "subcommand", args[0], "args", args[1:], "instance_count", len(cfg.Mihomo.Instances))

	switch args[0] {
	case "list-proxy":
		for i, inst := range cfg.Mihomo.Instances {
			sel := inst.Selector
			if sel == "" {
				sel = "GLOBAL"
			}
			m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
			slog.Info("CLI mihomo list-proxy instance", "instance", inst.Name, "api_base", inst.APIBase, "selector", sel)
			proxies, err := m.GetProxies(ctx)
			if err != nil {
				slog.Error("CLI mihomo list-proxy failed", "instance", inst.Name, "err", err)
				fmt.Printf("%s: error: %v\n\n", inst.Name, err)
				continue
			}
			p, ok := proxies[sel]
			if !ok {
				slog.Warn("CLI mihomo: selector not in /proxies", "instance", inst.Name, "selector", sel)
				fmt.Printf("%s: no proxy group %q\n\n", inst.Name, sel)
				continue
			}
			if len(p.All) == 0 {
				slog.Warn("CLI mihomo: empty selector group", "instance", inst.Name, "selector", sel, "type", p.Type)
				fmt.Printf("%s: group %q is type %s and has no selectable outbounds\n\n", inst.Name, sel, p.Type)
				continue
			}
			if i > 0 {
				fmt.Println()
			}
			fmt.Printf("%s\n", inst.Name)
			fmt.Printf("  group: %s (%s)\n", sel, p.Type)
			fmt.Printf("  current: %s\n", p.Now)
			fmt.Println("  outbounds:")
			for _, name := range p.All {
				fmt.Printf("    %s\n", name)
			}
			slog.Info("CLI mihomo list-proxy instance OK", "instance", inst.Name, "outbound_count", len(p.All), "current", p.Now)
		}
		slog.Info("CLI mihomo: list-proxy done")
		return nil

	case "set-proxy":
		if len(args) < 2 {
			slog.Error("CLI mihomo set-proxy: missing outbound name")
			return fmt.Errorf("set-proxy: missing outbound name (use: mihomo set-proxy <name>)")
		}
		arg := strings.TrimSpace(strings.Join(args[1:], " "))
		if arg == "" {
			slog.Error("CLI mihomo set-proxy: empty outbound name")
			return fmt.Errorf("set-proxy: outbound name is empty")
		}
		slog.Info("CLI mihomo set-proxy", "outbound", arg)
		for _, inst := range cfg.Mihomo.Instances {
			sel := inst.Selector
			if sel == "" {
				sel = "GLOBAL"
			}
			m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
			slog.Info("CLI mihomo set-proxy instance", "instance", inst.Name, "api_base", inst.APIBase, "selector", sel, "outbound", arg)
			if err := m.SetSelector(ctx, sel, arg); err != nil {
				slog.Error("CLI mihomo set-proxy failed", "instance", inst.Name, "selector", sel, "outbound", arg, "err", err)
				fmt.Printf("%s: %v\n", inst.Name, err)
			} else {
				fmt.Printf("%s: %s → %s\n", inst.Name, sel, arg)
				slog.Info("CLI mihomo set-proxy instance OK", "instance", inst.Name, "selector", sel, "outbound", arg)
			}
		}
		slog.Info("CLI mihomo: set-proxy done", "outbound", arg)
		return nil

	case "delay-proxy":
		if len(args) < 2 {
			slog.Error("CLI mihomo delay-proxy: missing proxy name")
			return fmt.Errorf("delay-proxy: missing proxy name (use: mihomo delay-proxy <name>)")
		}
		name := strings.TrimSpace(strings.Join(args[1:], " "))
		if name == "" {
			slog.Error("CLI mihomo delay-proxy: empty proxy name")
			return fmt.Errorf("delay-proxy: proxy name is empty")
		}
		testURL := cfg.Mihomo.LatencyTestURLOrDefault()
		timeoutMS := cfg.Mihomo.LatencyTimeoutMSOrDefault()
		slog.Info("CLI mihomo delay-proxy", "proxy", name, "test_url", testURL, "timeout_ms", timeoutMS)
		for _, inst := range cfg.Mihomo.Instances {
			m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
			slog.Info("CLI mihomo delay-proxy instance", "instance", inst.Name, "api_base", inst.APIBase)
			delay, err := m.GetProxyDelay(ctx, name, testURL, timeoutMS)
			if err != nil {
				slog.Error("CLI mihomo delay-proxy failed", "instance", inst.Name, "proxy", name, "err", err)
				fmt.Printf("%s: error: %v\n", inst.Name, err)
				continue
			}
			if delay <= 0 {
				fmt.Printf("%s: %s — unreachable or failed (0 ms)\n", inst.Name, name)
				slog.Warn("CLI mihomo delay-proxy zero", "instance", inst.Name, "proxy", name)
			} else {
				fmt.Printf("%s: %s → %d ms\n", inst.Name, name, delay)
				slog.Info("CLI mihomo delay-proxy OK", "instance", inst.Name, "proxy", name, "delay_ms", delay)
			}
		}
		slog.Info("CLI mihomo: delay-proxy done", "proxy", name)
		return nil

	default:
		slog.Error("CLI mihomo: unknown subcommand", "subcommand", args[0])
		return fmt.Errorf("unknown mihomo subcommand %q", args[0])
	}
}

// RunLLM handles: usage | deepseek | bailian
func RunLLM(ctx context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.LLMUsage == nil || !cfg.LLMUsage.Enabled {
		return fmt.Errorf("llm_usage is not enabled in config")
	}
	if len(args) < 1 {
		return fmt.Errorf("llm: expected subcommand (usage, deepseek, bailian)")
	}
	var scope runner.LLMUsageScope
	switch args[0] {
	case "usage":
		scope = runner.LLMUsageAll
	case "deepseek":
		scope = runner.LLMUsageDeepSeekOnly
	case "bailian":
		scope = runner.LLMUsageBailianOnly
	default:
		return fmt.Errorf("unknown llm subcommand %q", args[0])
	}
	report := runner.FetchLLMUsageScoped(ctx, cfg.LLMUsage, scope)
	fmt.Print(runner.FormatLLMUsagePlainScoped(report, cfg.LLMUsage, scope))
	return nil
}
