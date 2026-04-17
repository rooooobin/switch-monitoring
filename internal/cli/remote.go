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

  switch-monitor mihomo list-proxy
  switch-monitor mihomo set-proxy <outbound-name>

Requires ikuai / mihomo sections enabled in the config file (same as Telegram).
`)
}

// RunIkuai handles: list-dnat | enable-dnat <id> | disable-dnat <id>
func RunIkuai(_ context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.Ikuai == nil || !cfg.Ikuai.Enabled {
		slog.Error("CLI ikuai: integration disabled in config")
		return fmt.Errorf("ikuai is not enabled in config")
	}
	if len(args) < 1 {
		slog.Error("CLI ikuai: missing subcommand")
		return fmt.Errorf("ikuai: expected subcommand (list-dnat, enable-dnat, disable-dnat)")
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

	default:
		slog.Error("CLI ikuai: unknown subcommand", "subcommand", args[0])
		return fmt.Errorf("unknown ikuai subcommand %q", args[0])
	}
}

// RunMihomo handles: list-proxy | set-proxy <outbound>
func RunMihomo(ctx context.Context, cfg *config.MonitorConfig, args []string) error {
	if cfg.Mihomo == nil || !cfg.Mihomo.Enabled || len(cfg.Mihomo.Instances) == 0 {
		slog.Error("CLI mihomo: integration disabled or no instances")
		return fmt.Errorf("mihomo is not enabled or has no instances in config")
	}
	if len(args) < 1 {
		slog.Error("CLI mihomo: missing subcommand")
		return fmt.Errorf("mihomo: expected subcommand (list-proxy, set-proxy)")
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

	default:
		slog.Error("CLI mihomo: unknown subcommand", "subcommand", args[0])
		return fmt.Errorf("unknown mihomo subcommand %q", args[0])
	}
}
