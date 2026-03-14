// Package runner orchestrates polling, checking, logging, and alerting.
package runner

import (
	"fmt"
	"log/slog"
	"os"
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
	Logout() error
}

// adapterEntry pairs a config with its adapter.
type adapterEntry struct {
	cfg     config.SwitchConfig
	adapter switchAdapter
}

// Runner polls switches, formats tables, and sends alert emails.
type Runner struct {
	cfg          *config.MonitorConfig
	cfgPath      string
	noEmail      bool
	checker      *checker.PortChecker
	alertService *alerting.AlertService
	adapters     []adapterEntry
	historyPath  string
	cfgModTime   time.Time
}

// New creates a Runner from the given config.
func New(cfg *config.MonitorConfig, cfgPath string, noEmail bool) *Runner {
	r := &Runner{
		cfg:     cfg,
		cfgPath: cfgPath,
		noEmail: noEmail,
		checker: checker.New(cfg.MinSpeedMbps),
	}
	r.applyConfig(cfg)
	return r
}

// applyConfig updates the runner's services and adapters from a new config.
func (r *Runner) applyConfig(cfg *config.MonitorConfig) {
	if r.noEmail {
		if cfg.SMTP != nil {
			cfg.SMTP.Enabled = false
		}
		cfg.AlertEmails = nil
		if cfg.Telegram != nil {
			cfg.Telegram.Enabled = false
		}
	}

	r.cfg = cfg
	r.checker = checker.New(cfg.MinSpeedMbps)

	hasEmail := cfg.SMTP != nil && cfg.SMTP.Enabled && len(cfg.AlertEmails) > 0
	hasTg := cfg.Telegram != nil && cfg.Telegram.Enabled && len(cfg.Telegram.Recipients) > 0
	if hasEmail || hasTg {
		r.alertService = alerting.New(cfg.SMTP, cfg.AlertEmails, cfg.Telegram)
	} else {
		r.alertService = nil
	}

	if cfg.HistoryFile != "" {
		r.historyPath = filepath.Join(cfg.LogDir, cfg.HistoryFile)
	} else {
		r.historyPath = ""
	}

	r.adapters = nil
	for _, sw := range cfg.Switches {
		sw := sw
		r.adapters = append(r.adapters, adapterEntry{
			cfg:     sw,
			adapter: makeAdapter(sw),
		})
	}
}

// reloadIfChanged checks whether config.yaml has been modified and reloads it.
func (r *Runner) reloadIfChanged() {
	fi, err := os.Stat(r.cfgPath)
	if err != nil {
		slog.Warn("Config stat failed", "path", r.cfgPath, "error", err)
		return
	}
	if !fi.ModTime().After(r.cfgModTime) {
		return
	}
	slog.Info("Config file changed, reloading", "path", r.cfgPath)
	newCfg, err := config.LoadConfig(r.cfgPath)
	if err != nil {
		slog.Error("Failed to reload config, keeping old config", "error", err)
		return
	}
	r.applyConfig(newCfg)
	r.cfgModTime = fi.ModTime()
	slog.Info("Config reloaded successfully")
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

		var statuses []model.PortStatus
		var err error

		// Retry logic for transient HTTP timeouts
		maxRetries := 3
		for attempt := 1; attempt <= maxRetries; attempt++ {
			statuses, err = entry.adapter.GetPortStatuses()
			
			// Always attempt to logout to free the session
			if logoutErr := entry.adapter.Logout(); logoutErr != nil {
				slog.Debug("Failed to logout", "switch", swName, "error", logoutErr)
			}

			if err == nil {
				break
			}
			
			if attempt < maxRetries {
				slog.Warn("Failed to poll switch, retrying...", "switch", swName, "attempt", attempt, "error", err)
				time.Sleep(time.Duration(attempt*5) * time.Second)
			}
		}

		if err != nil {
			slog.Error("Failed to poll switch after retries", "switch", swName, "error", err)
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
	for _, sw := range r.cfg.Switches {
		rows := rowsBySwitch[sw.Name]
		if len(rows) == 0 {
			continue
		}
		header := fmt.Sprintf("=== %s ===", sw.Name)
		table := FormatStatusTable(rows, false)
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

		// Build pretty alert table
		var alertParts []string
		for _, sw := range r.cfg.Switches {
			rows := rowsBySwitch[sw.Name]
			if len(rows) == 0 {
				continue
			}
			swTable := FormatAlertTable(rows, false)
			alertParts = append(alertParts, fmt.Sprintf("🔌 %s\n%s", sw.Name, swTable))
		}

		aliasesBySwitch := make(map[string]map[int]string, len(r.cfg.Switches))
		for _, sw := range r.cfg.Switches {
			aliasesBySwitch[sw.Name] = sw.PortAliases
		}
		if err := r.alertService.SendSummary(alertParts, runEvents, aliasesBySwitch); err != nil {
			slog.Error("Failed to send summary alert", "error", err)
		} else {
			slog.Info("Sent summary alert", "issues", len(runEvents))
		}
	}
}

// RunLoop runs RunOnce in a loop, sleeping check_interval_seconds between each.
// If once is true, it runs exactly once and returns.
// Config file is checked for changes before every poll cycle.
func (r *Runner) RunLoop(once bool) {
	// Record initial mod time
	if fi, err := os.Stat(r.cfgPath); err == nil {
		r.cfgModTime = fi.ModTime()
	}

	if once {
		r.RunOnce()
		return
	}
	for {
		r.reloadIfChanged()
		r.RunOnce()
		
		sleepSecs := r.cfg.CheckIntervalSeconds
		if r.checker.HasAnyPending() {
			sleepSecs = r.cfg.RecheckIntervalSeconds
			slog.Debug("Pending alerts detected, using recheck interval", "seconds", sleepSecs)
		}
		
		time.Sleep(time.Duration(sleepSecs) * time.Second)
	}
}
