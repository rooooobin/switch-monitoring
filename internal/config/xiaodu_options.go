package config

import (
	"fmt"
	"strings"
	"time"
)

// XiaoduAlertTTSConfig controls voice alerts on confirmed port issues.
// mode: off (disabled), always (any time), window (only between start_time and end_time).
type XiaoduAlertTTSConfig struct {
	Mode      string `yaml:"mode"`
	StartTime string `yaml:"start_time"`
	EndTime   string `yaml:"end_time"`
	Timezone  string `yaml:"timezone"`
	MinIssues int    `yaml:"min_issues"`
}

// XiaoduProbeConfig periodically checks speaker DLNA reachability.
type XiaoduProbeConfig struct {
	Enabled          bool `yaml:"enabled"`
	IntervalSeconds  int  `yaml:"interval_seconds"`
	NotifyTelegram   bool `yaml:"notify_telegram"`
}

// XiaoduBDUSSCheckConfig periodically validates DuerOS BDUSS credentials.
type XiaoduBDUSSCheckConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	NotifyTelegram  bool `yaml:"notify_telegram"`
}

func (p *XiaoduProbeConfig) IntervalOrDefault() time.Duration {
	if p == nil || p.IntervalSeconds <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(p.IntervalSeconds) * time.Second
}

func (b *XiaoduBDUSSCheckConfig) IntervalOrDefault() time.Duration {
	if b == nil || b.IntervalSeconds <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(b.IntervalSeconds) * time.Second
}

func (a *XiaoduAlertTTSConfig) MinIssuesOrDefault() int {
	if a == nil || a.MinIssues <= 0 {
		return 1
	}
	return a.MinIssues
}

// ShouldSpeak reports whether alert TTS should run for issueCount at now.
func (a *XiaoduAlertTTSConfig) ShouldSpeak(now time.Time, issueCount int, fallbackTZ string) bool {
	if a == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" || mode == "off" {
		return false
	}
	if issueCount < a.MinIssuesOrDefault() {
		return false
	}
	switch mode {
	case "always":
		return true
	case "window":
		return xiaoduInTimeWindow(now, a.Timezone, fallbackTZ, a.StartTime, a.EndTime)
	default:
		return false
	}
}

func xiaoduInTimeWindow(now time.Time, tzName, fallbackTZ, start, end string) bool {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		start = "08:00"
	}
	if end == "" {
		end = "22:00"
	}
	loc := resolveTimezone(tzName, fallbackTZ)
	local := now.In(loc)
	startMin, err1 := parseHHMM(start)
	endMin, err2 := parseHHMM(end)
	if err1 != nil || err2 != nil {
		return false
	}
	nowMin := local.Hour()*60 + local.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin
	}
	// Overnight window, e.g. 22:00 - 08:00
	return nowMin >= startMin || nowMin < endMin
}

func resolveTimezone(primary, fallback string) *time.Location {
	for _, name := range []string{primary, fallback, "Asia/Shanghai"} {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	return time.Local
}

func parseHHMM(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil {
		return 0, err
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil {
		return 0, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return h*60 + m, nil
}
