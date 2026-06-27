package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"switch-monitor/internal/alerting"
	"switch-monitor/internal/checker"
	"switch-monitor/internal/config"
)

type xiaoduHealthState struct {
	mu              sync.Mutex
	lastProbeRun    time.Time
	lastBDUSSRun    time.Time
	lastProbeOK     *bool
	lastBDUSSOK     *bool
}

func (r *Runner) xiaoduFallbackTZ() string {
	if r.cfg.Calendar != nil {
		return r.cfg.Calendar.Timezone
	}
	return ""
}

func (r *Runner) runXiaoduHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runXiaoduHealthChecks(ctx)
		}
	}
}

func (r *Runner) runXiaoduHealthChecks(ctx context.Context) {
	cfg := r.cfg.Xiaodu
	if cfg == nil || !cfg.Enabled || strings.TrimSpace(cfg.IP) == "" {
		return
	}
	c := newXiaoduClient(cfg)
	now := time.Now()

	if cfg.Probe != nil && cfg.Probe.Enabled && r.probeDue(now, cfg.Probe.IntervalOrDefault()) {
		r.markProbeRun(now)
		err := c.ProbeOnline(ctx)
		r.handleXiaoduProbeResult(cfg, err == nil, err)
	}

	if cfg.BDUSSCheck != nil && cfg.BDUSSCheck.Enabled && r.bdussDue(now, cfg.BDUSSCheck.IntervalOrDefault()) {
		r.markBDUSSRun(now)
		err := c.CheckBDUSS(ctx)
		r.handleXiaoduBDUSSResult(cfg, err == nil, err)
	}
}

func (r *Runner) probeDue(now time.Time, interval time.Duration) bool {
	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	return r.health.lastProbeRun.IsZero() || now.Sub(r.health.lastProbeRun) >= interval
}

func (r *Runner) bdussDue(now time.Time, interval time.Duration) bool {
	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	return r.health.lastBDUSSRun.IsZero() || now.Sub(r.health.lastBDUSSRun) >= interval
}

func (r *Runner) markProbeRun(now time.Time) {
	r.health.mu.Lock()
	r.health.lastProbeRun = now
	r.health.mu.Unlock()
}

func (r *Runner) markBDUSSRun(now time.Time) {
	r.health.mu.Lock()
	r.health.lastBDUSSRun = now
	r.health.mu.Unlock()
}

func (r *Runner) handleXiaoduProbeResult(cfg *config.XiaoduConfig, ok bool, err error) {
	if ok {
		slog.Info("Xiaodu probe OK", "ip", cfg.IP)
	} else {
		slog.Warn("Xiaodu probe failed", "ip", cfg.IP, "err", err)
	}

	changed, prevKnown := r.setProbeOK(ok)
	if !changed || cfg.Probe == nil || !cfg.Probe.NotifyTelegram {
		return
	}
	if !prevKnown {
		return
	}
	if ok {
		r.notifyTelegramPlain(fmt.Sprintf("✅ Xiaodu speaker back online (%s)", cfg.IP))
	} else {
		r.notifyTelegramPlain(fmt.Sprintf("⚠️ Xiaodu speaker offline (%s): %v", cfg.IP, err))
	}
}

func (r *Runner) handleXiaoduBDUSSResult(cfg *config.XiaoduConfig, ok bool, err error) {
	if ok {
		slog.Info("Xiaodu BDUSS check OK")
	} else {
		slog.Warn("Xiaodu BDUSS check failed", "err", err)
	}

	changed, prevKnown := r.setBDUSSOK(ok)
	if !changed || cfg.BDUSSCheck == nil || !cfg.BDUSSCheck.NotifyTelegram {
		return
	}
	if !prevKnown {
		return
	}
	if ok {
		r.notifyTelegramPlain("✅ Xiaodu DuerOS BDUSS is valid again.")
	} else {
		r.notifyTelegramPlain(fmt.Sprintf("⚠️ Xiaodu DuerOS BDUSS invalid: %v", err))
	}
}

func (r *Runner) setProbeOK(ok bool) (changed, prevKnown bool) {
	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	prevKnown = r.health.lastProbeOK != nil
	if r.health.lastProbeOK != nil && *r.health.lastProbeOK == ok {
		return false, prevKnown
	}
	r.health.lastProbeOK = &ok
	return true, prevKnown
}

func (r *Runner) setBDUSSOK(ok bool) (changed, prevKnown bool) {
	r.health.mu.Lock()
	defer r.health.mu.Unlock()
	prevKnown = r.health.lastBDUSSOK != nil
	if r.health.lastBDUSSOK != nil && *r.health.lastBDUSSOK == ok {
		return false, prevKnown
	}
	r.health.lastBDUSSOK = &ok
	return true, prevKnown
}

func (r *Runner) maybeAlertTTS(ctx context.Context, events []checker.AlertEvent, aliases map[string]map[int]string) {
	cfg := r.cfg.Xiaodu
	if cfg == nil || !cfg.Enabled || strings.TrimSpace(cfg.IP) == "" || cfg.AlertTTS == nil {
		return
	}
	if !cfg.AlertTTS.ShouldSpeak(time.Now(), len(events), r.xiaoduFallbackTZ()) {
		return
	}
	text := buildAlertTTSText(events, aliases)
	if text == "" {
		return
	}
	c := newXiaoduClient(cfg)
	used, err := c.TTS(ctx, text)
	if err != nil {
		slog.Error("Xiaodu alert TTS failed", "err", err, "text", text)
		return
	}
	slog.Info("Xiaodu alert TTS sent", "mode", used, "issues", len(events), "text", text)
}

func buildAlertTTSText(events []checker.AlertEvent, aliases map[string]map[int]string) string {
	if len(events) == 0 {
		return ""
	}
	if len(events) == 1 {
		e := events[0]
		portLabel := fmt.Sprintf("端口 %d", e.PortID)
		if a, ok := aliases[e.SwitchName][e.PortID]; ok && a != "" {
			portLabel = a
		}
		switch e.Reason {
		case checker.ReasonDown:
			return fmt.Sprintf("%s %s 链路中断", e.SwitchName, portLabel)
		case checker.ReasonLowSpeed:
			return fmt.Sprintf("%s %s 速度异常", e.SwitchName, portLabel)
		}
	}
	return fmt.Sprintf("网络监控发现 %d 个问题", len(events))
}

func (r *Runner) notifyTelegramPlain(message string) {
	if r.cfg.Telegram == nil || !r.cfg.Telegram.Enabled {
		return
	}
	if err := alerting.NotifyTelegramPlain(r.cfg.Telegram, message); err != nil {
		slog.Warn("Telegram notify failed", "err", err)
	}
}

// RunXiaoduProbeNow runs an immediate DLNA probe (CLI/Telegram).
func RunXiaoduProbeNow(ctx context.Context, cfg *config.XiaoduConfig) error {
	return newXiaoduClient(cfg).ProbeOnline(ctx)
}

// RunXiaoduBDUSSCheckNow runs an immediate BDUSS validation (CLI/Telegram).
func RunXiaoduBDUSSCheckNow(ctx context.Context, cfg *config.XiaoduConfig) error {
	return newXiaoduClient(cfg).CheckBDUSS(ctx)
}
