// Package runner orchestrates polling, checking, logging, and alerting.
package runner

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/alerting"
	"switch-monitor/internal/checker"
	"switch-monitor/internal/config"
	"switch-monitor/internal/logging"
	"switch-monitor/internal/model"
)

// switchAdapter is the common interface for all switch adapters.
type switchAdapter interface {
	GetPortStatuses() ([]model.PortStatus, error)
}

// adapterEntry pairs a config with its adapter.
type adapterEntry struct {
	cfg     config.SwitchConfig
	adapter switchAdapter
}

// Runner polls switches, formats tables, and sends alert emails.
type Runner struct {
	cfg          *config.MonitorConfig
	checker      *checker.PortChecker
	alertService *alerting.AlertService
	adapters     []adapterEntry
	historyPath  string
}

// New creates a Runner from the given config.
func New(cfg *config.MonitorConfig) *Runner {
	r := &Runner{
		cfg:     cfg,
		checker: checker.New(cfg.MinSpeedMbps),
	}
	hasEmail := cfg.SMTP != nil && cfg.SMTP.Enabled && cfg.AlertEmail != ""
	hasTg := cfg.Telegram != nil && cfg.Telegram.Enabled && cfg.Telegram.Token != "" && cfg.Telegram.ChatID != ""
	if hasEmail || hasTg {
		r.alertService = alerting.New(cfg.SMTP, cfg.AlertEmail, cfg.Telegram)
	}
	if cfg.HistoryFile != "" {
		r.historyPath = filepath.Join(cfg.LogDir, cfg.HistoryFile)
	}
	for _, sw := range cfg.Switches {
		sw := sw
		r.adapters = append(r.adapters, adapterEntry{
			cfg:     sw,
			adapter: makeAdapter(sw),
		})
	}
	return r
}

func makeAdapter(sw config.SwitchConfig) switchAdapter {
	switch sw.Type {
	case config.TypeNetgearGS108Ev3:
		return adapter.NewNetgearAdapter(sw.Host(), sw.Password)
	case config.TypeMercurySG108Pro:
		username := sw.Username
		if username == "" {
			username = "admin"
		}
		return adapter.NewMercuryAdapter(sw.Host(), username, sw.Password)
	default:
		panic(fmt.Sprintf("unknown switch type: %s", sw.Type))
	}
}

// RunOnce performs a single poll-and-check cycle.
func (r *Runner) RunOnce() {
	rowsBySwitch := make(map[string][]statusRow, len(r.adapters))
	var runEvents []checker.AlertEvent

	for _, entry := range r.adapters {
		swName := entry.cfg.Name
		rowsBySwitch[swName] = nil

		statuses, err := entry.adapter.GetPortStatuses()
		if err != nil {
			slog.Error("Failed to poll switch", "switch", swName, "error", err)
			continue
		}

		concerned := make(map[int]bool, len(entry.cfg.ConcernedPorts))
		for _, p := range entry.cfg.ConcernedPorts {
			concerned[p] = true
		}

		for _, s := range statuses {
			if !concerned[s.PortID] {
				continue
			}
			alias := ""
			if entry.cfg.PortAliases != nil {
				alias = entry.cfg.PortAliases[s.PortID]
			}
			row := statusRow{
				switchName: swName,
				portID:     s.PortID,
				linkUp:     s.LinkUp,
				speedMbps:  s.SpeedMbps,
				alias:      alias,
				txOk:       s.TxOk,
				txFail:     s.TxFail,
				rxOk:       s.RxOk,
				rxFail:     s.RxFail,
				txMBytes:   s.TxMBytes,
				rxMBytes:   s.RxMBytes,
			}
			rowsBySwitch[swName] = append(rowsBySwitch[swName], row)

			var speedVal any
			if s.SpeedMbps != nil {
				speedVal = *s.SpeedMbps
			}

			slog.Info("Port status",
				"switch", swName,
				"port", s.PortID,
				"link_up", s.LinkUp,
				"speed_mbps", speedVal,
			)
			if r.historyPath != "" {
				if err2 := logging.AppendHistory(r.historyPath, swName, s.PortID, s.LinkUp, s.SpeedMbps); err2 != nil {
					slog.Warn("History write failed", "error", err2)
				}
			}
		}

		events := r.checker.Check(swName, entry.cfg.ConcernedPorts, statuses)
		runEvents = append(runEvents, events...)
	}

	// Print per-switch tables
	var allTableParts []string
	for _, sw := range r.cfg.Switches {
		rows := rowsBySwitch[sw.Name]
		if len(rows) == 0 {
			continue
		}
		header := fmt.Sprintf("=== %s ===", sw.Name)
		table := FormatStatusTable(rows, false)
		allTableParts = append(allTableParts, header+"\n"+table)
		fmt.Println(header)
		fmt.Println(table)
	}

	total := 0
	for _, rows := range rowsBySwitch {
		total += len(rows)
	}
	slog.Info("Status check complete",
		"ports", total,
		"switches", len(rowsBySwitch),
	)

	// Send summary alert when there are events
	if len(runEvents) > 0 {
		if r.alertService == nil {
			slog.Warn("No SMTP/Telegram configured; issues not alerted", "count", len(runEvents))
			return
		}
		combined := ""
		for i, part := range allTableParts {
			if i > 0 {
				combined += "\n\n"
			}
			combined += part
		}
		aliasesBySwitch := make(map[string]map[int]string, len(r.cfg.Switches))
		for _, sw := range r.cfg.Switches {
			aliasesBySwitch[sw.Name] = sw.PortAliases
		}
		if err := r.alertService.SendSummary(combined, runEvents, aliasesBySwitch); err != nil {
			slog.Error("Failed to send summary alert", "error", err)
		} else {
			slog.Info("Sent summary alert", "issues", len(runEvents))
		}
	}
}

// RunLoop runs RunOnce in a loop, sleeping check_interval_seconds between each.
// If once is true, it runs exactly once and returns.
func (r *Runner) RunLoop(once bool) {
	if once {
		r.RunOnce()
		return
	}
	for {
		r.RunOnce()
		time.Sleep(time.Duration(r.cfg.CheckIntervalSeconds) * time.Second)
	}
}
