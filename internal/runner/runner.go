// Package runner orchestrates polling, checking, logging, and alerting.
package runner

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/alerting"
	"switch-monitor/internal/calendar"
	"switch-monitor/internal/checker"
	"switch-monitor/internal/config"
	"switch-monitor/internal/logging"
	"switch-monitor/internal/model"
	"switch-monitor/internal/telegram"
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
	noCalendar   bool
	checker      *checker.PortChecker
	alertService *alerting.AlertService
	calendar     calendar.Upserter
	adapters     []adapterEntry
	historyPath  string
	cfgModTime   time.Time
	triggerChan  chan struct{}
}

// New creates a Runner from the given config.
func New(cfg *config.MonitorConfig, cfgPath string, noEmail, noCalendar bool) *Runner {
	r := &Runner{
		cfg:         cfg,
		cfgPath:     cfgPath,
		noEmail:     noEmail,
		noCalendar:  noCalendar,
		checker:     checker.New(cfg.MinSpeedMbps),
		triggerChan: make(chan struct{}, 1),
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

	r.calendar = nil
	if !r.noCalendar && cfg.Calendar != nil && cfg.Calendar.Enabled {
		cal, err := calendar.NewFromConfig(cfg.Calendar)
		if err != nil {
			slog.Error("Calendar init failed", "error", err)
		} else {
			r.calendar = cal
		}
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
func (r *Runner) RunOnce(isManual bool) {
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
				time.Sleep(time.Duration(attempt*2) * time.Second)
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

	if len(runEvents) == 0 && !isManual {
		return
	}

	var alertParts []string
	for _, sw := range r.cfg.Switches {
		rows := rowsBySwitch[sw.Name]
		if len(rows) == 0 {
			continue
		}
		swTable := FormatStatusTable(rows, false)
		alertParts = append(alertParts, fmt.Sprintf("🔌 %s\n%s", sw.Name, swTable))
	}

	aliasesBySwitch := make(map[string]map[int]string, len(r.cfg.Switches))
	for _, sw := range r.cfg.Switches {
		aliasesBySwitch[sw.Name] = sw.PortAliases
	}

	if r.alertService != nil {
		if err := r.alertService.SendSummary(isManual, alertParts, runEvents, aliasesBySwitch); err != nil {
			slog.Error("Failed to send summary alert", "error", err)
		} else {
			slog.Info("Sent summary alert", "issues", len(runEvents), "manual", isManual)
		}
	} else {
		slog.Warn("No SMTP/Telegram configured; issues not alerted", "count", len(runEvents))
	}

	if r.calendar != nil && len(runEvents) > 0 {
		desc := alerting.BuildSummaryBody(alertParts, runEvents, aliasesBySwitch)
		if err := r.calendar.UpsertRepairEvent(context.Background(), time.Now(), desc); err != nil {
			slog.Error("Calendar upsert failed", "error", err)
		} else {
			slog.Info("Updated calendar repair event", "issues", len(runEvents))
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
		r.RunOnce(false)
		return
	}

	// Start telegram polling in the background if enabled
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.pollTelegramCommands(ctx)

	isManual := false
	for {
		r.reloadIfChanged()
		r.RunOnce(isManual)

		sleepSecs := r.cfg.CheckIntervalSeconds
		if r.checker.HasAnyPending() {
			sleepSecs = r.cfg.RecheckIntervalSeconds
			slog.Debug("Pending alerts detected, using recheck interval", "seconds", sleepSecs)
		}

		timer := time.NewTimer(time.Duration(sleepSecs) * time.Second)
		select {
		case <-timer.C:
			isManual = false
			// Regular interval check
		case <-r.triggerChan:
			timer.Stop()
			isManual = true
			slog.Info("Manual check triggered via Telegram")
		}
	}
}

func (r *Runner) isAuthorizedTelegramChat(chatID int64) bool {
	id := strconv.FormatInt(chatID, 10)
	for _, rcfg := range r.cfg.Telegram.Recipients {
		if id == rcfg.ChatID {
			return true
		}
	}
	return false
}

func (r *Runner) pollTelegramCommands(ctx context.Context) {
	if r.cfg.Telegram == nil || !r.cfg.Telegram.Enabled || !r.cfg.Telegram.ListenCommands || len(r.cfg.Telegram.Recipients) == 0 {
		return
	}

	// Create clients for all configured bots that we should listen to
	// To avoid complexity, we just listen to the first configured bot for commands.
	recipient := r.cfg.Telegram.Recipients[0]
	client, err := telegram.NewClient(recipient.Token, recipient.Proxy)
	if err != nil {
		slog.Error("Failed to initialize telegram client for polling", "error", err)
		return
	}

	offset := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		updates, err := client.GetUpdates(ctx, offset)
		if err != nil {
			slog.Debug("Error polling telegram updates", "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}

			if update.Message == nil || update.Message.Text == "" {
				continue
			}

			text := strings.TrimSpace(update.Message.Text)
			chatIDStr := strconv.FormatInt(update.Message.Chat.ID, 10)

			if strings.HasPrefix(text, "/check") {
				if !r.isAuthorizedTelegramChat(update.Message.Chat.ID) {
					slog.Warn("Received /check command from unauthorized telegram chat", "chat_id", update.Message.Chat.ID)
					continue
				}
				slog.Info("Received /check command from authorized telegram chat", "chat_id", update.Message.Chat.ID)
				select {
				case r.triggerChan <- struct{}{}:
					_ = client.SendMessage(ctx, chatIDStr, "🔄 Manual check triggered...")
				default:
				}
			} else if strings.HasPrefix(text, "/list_dnat") || strings.HasPrefix(text, "/enable_dnat") || strings.HasPrefix(text, "/disable_dnat") {
				if !r.isAuthorizedTelegramChat(update.Message.Chat.ID) {
					slog.Warn("Received iKuai command from unauthorized telegram chat", "chat_id", update.Message.Chat.ID, "command", text)
					continue
				}

				if r.cfg.Ikuai == nil || !r.cfg.Ikuai.Enabled {
					_ = client.SendMessage(ctx, chatIDStr, "⚠️ iKuai integration is not enabled in config.")
					continue
				}

				ikuai, err := adapter.NewIkuaiClient(r.cfg.Ikuai.URL, r.cfg.Ikuai.Username, r.cfg.Ikuai.Password)
				if err != nil {
					_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to init iKuai client: "+err.Error())
					continue
				}

				if err := ikuai.Login(); err != nil {
					_ = client.SendMessage(ctx, chatIDStr, "❌ iKuai login failed: "+err.Error())
					continue
				}

				if strings.HasPrefix(text, "/list_dnat") {
					rules, err := ikuai.GetDNATRules()
					if err != nil {
						_ = client.SendMessage(ctx, chatIDStr, "❌ Failed to fetch DNAT rules: "+err.Error())
						continue
					}

					table := FormatDNATRulesTable(rules)
					msg := "📋 <b>iKuai DNAT Rules</b>\n<pre>" + html.EscapeString(table) + "</pre>"
					_ = client.SendMessageHTML(ctx, chatIDStr, msg)

				} else {
					parts := strings.Fields(text)
					if len(parts) < 2 {
						_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /enable_dnat <id> or /disable_dnat <id>")
						continue
					}
					id, err := strconv.Atoi(parts[1])
					if err != nil {
						_ = client.SendMessage(ctx, chatIDStr, "❌ Invalid ID: "+parts[1])
						continue
					}

					enable := strings.HasPrefix(text, "/enable_dnat")
					action := "disabling"
					if enable {
						action = "enabling"
					}

					if err := ikuai.ToggleDNATRule(id, enable); err != nil {
						slog.Error("Failed to toggle iKuai DNAT rule via Telegram", "chat_id", update.Message.Chat.ID, "rule_id", id, "enable", enable, "error", err)
						_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("❌ Error %s rule %d: %v", action, id, err))
					} else {
						status := "disabled"
						if enable {
							status = "enabled"
						}
						slog.Info("Successfully toggled iKuai DNAT rule via Telegram", "chat_id", update.Message.Chat.ID, "rule_id", id, "status", status)
						_ = client.SendMessage(ctx, chatIDStr, fmt.Sprintf("✅ Rule %d has been %s.", id, status))
					}
				}
			} else if strings.HasPrefix(text, "/list_proxy") || strings.HasPrefix(text, "/set_proxy") || strings.HasPrefix(text, "/delay_proxy") {
				if !r.isAuthorizedTelegramChat(update.Message.Chat.ID) {
					slog.Warn("Received mihomo command from unauthorized telegram chat", "chat_id", update.Message.Chat.ID, "command", text)
					continue
				}
				if r.cfg.Mihomo == nil || !r.cfg.Mihomo.Enabled || len(r.cfg.Mihomo.Instances) == 0 {
					_ = client.SendMessage(ctx, chatIDStr, "⚠️ Mihomo integration is not enabled in config.")
					continue
				}

				if strings.HasPrefix(text, "/delay_proxy") {
					arg := strings.TrimSpace(strings.TrimPrefix(text, "/delay_proxy"))
					if arg == "" {
						_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /delay_proxy <proxy name>")
						continue
					}
					testURL := r.cfg.Mihomo.LatencyTestURLOrDefault()
					timeoutMS := r.cfg.Mihomo.LatencyTimeoutMSOrDefault()
					var resultSb strings.Builder
					for _, inst := range r.cfg.Mihomo.Instances {
						m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
						delay, err := m.GetProxyDelay(ctx, arg, testURL, timeoutMS)
						if err != nil {
							slog.Error("Mihomo delay test failed via Telegram", "chat_id", update.Message.Chat.ID, "instance", inst.Name, "proxy", arg, "error", err)
							resultSb.WriteString(fmt.Sprintf("❌ <b>%s</b>: %s\n", html.EscapeString(inst.Name), html.EscapeString(err.Error())))
							continue
						}
						if delay <= 0 {
							resultSb.WriteString(fmt.Sprintf("⚠️ <b>%s</b>: <code>%s</code> — unreachable or failed (0 ms)\n", html.EscapeString(inst.Name), html.EscapeString(arg)))
						} else {
							slog.Info("Mihomo delay OK via Telegram", "chat_id", update.Message.Chat.ID, "instance", inst.Name, "proxy", arg, "delay_ms", delay)
							resultSb.WriteString(fmt.Sprintf("✅ <b>%s</b>: <code>%s</code> → <b>%d ms</b>\n", html.EscapeString(inst.Name), html.EscapeString(arg), delay))
						}
					}
					header := fmt.Sprintf("⏱ <b>Proxy delay</b>\n<code>%s</code>\n<i>timeout %d ms</i>\n\n", html.EscapeString(arg), timeoutMS)
					_ = client.SendMessageHTML(ctx, chatIDStr, header+resultSb.String())
				} else if strings.HasPrefix(text, "/list_proxy") {
					var allSb strings.Builder
					for i, inst := range r.cfg.Mihomo.Instances {
						sel := inst.Selector
						if sel == "" {
							sel = "GLOBAL"
						}
						m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
						proxies, err := m.GetProxies(ctx)
						if err != nil {
							allSb.WriteString(fmt.Sprintf("❌ <b>%s</b>: %v\n\n", inst.Name, err))
							continue
						}
						p, ok := proxies[sel]
						if !ok {
							allSb.WriteString(fmt.Sprintf("❌ <b>%s</b>: No proxy group %q\n\n", inst.Name, sel))
							continue
						}
						if len(p.All) == 0 {
							allSb.WriteString(fmt.Sprintf("⚠️ <b>%s</b>: Group %q is type %s and has no selectable outbounds\n\n", inst.Name, sel, p.Type))
							continue
						}
						if i > 0 {
							allSb.WriteString("\n")
						}
						allSb.WriteString(fmt.Sprintf("🔌 <b>%s</b>\n<pre>group: %s (%s)\ncurrent: %s\n\n", inst.Name, sel, p.Type, p.Now))
						for _, name := range p.All {
							allSb.WriteString(fmt.Sprintf("  %s\n", name))
						}
						allSb.WriteString("</pre>")
					}
					_ = client.SendMessageHTML(ctx, chatIDStr, allSb.String())
				} else if strings.HasPrefix(text, "/set_proxy") {
					arg := strings.TrimSpace(strings.TrimPrefix(text, "/set_proxy"))
					if arg == "" {
						_ = client.SendMessage(ctx, chatIDStr, "⚠️ Usage: /set_proxy <outbound name> (use /list_proxy to see names)")
						continue
					}
					var resultSb strings.Builder
					for _, inst := range r.cfg.Mihomo.Instances {
						sel := inst.Selector
						if sel == "" {
							sel = "GLOBAL"
						}
						m := adapter.NewMihomoClient(inst.APIBase, inst.Secret)
						if err := m.SetSelector(ctx, sel, arg); err != nil {
							slog.Error("Failed to switch Mihomo proxy via Telegram", "chat_id", update.Message.Chat.ID, "instance", inst.Name, "selector", sel, "target", arg, "error", err)
							resultSb.WriteString(fmt.Sprintf("❌ <b>%s</b>: %v\n", inst.Name, err))
						} else {
							slog.Info("Successfully switched Mihomo proxy via Telegram", "chat_id", update.Message.Chat.ID, "instance", inst.Name, "selector", sel, "target", arg)
							resultSb.WriteString(fmt.Sprintf("✅ <b>%s</b>: %s → %s\n", inst.Name, sel, arg))
						}
					}
					_ = client.SendMessageHTML(ctx, chatIDStr, resultSb.String())
				}
			}
		}

		time.Sleep(2 * time.Second)
	}
}
