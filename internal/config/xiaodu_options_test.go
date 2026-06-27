package config

import (
	"testing"
	"time"
)

func TestXiaoduAlertTTSShouldSpeak(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 6, 27, 10, 0, 0, 0, loc)

	cfg := &XiaoduAlertTTSConfig{Mode: "off"}
	if cfg.ShouldSpeak(now, 1, "") {
		t.Fatal("off should not speak")
	}

	cfg.Mode = "always"
	if !cfg.ShouldSpeak(now, 1, "") {
		t.Fatal("always should speak")
	}
	if cfg.ShouldSpeak(now, 0, "") {
		t.Fatal("always should respect min issues")
	}

	cfg = &XiaoduAlertTTSConfig{
		Mode:      "window",
		StartTime: "08:00",
		EndTime:   "22:00",
		Timezone:  "Asia/Shanghai",
		MinIssues: 1,
	}
	if !cfg.ShouldSpeak(now, 1, "") {
		t.Fatal("10:00 should be inside window")
	}
	night := time.Date(2026, 6, 27, 23, 0, 0, 0, loc)
	if cfg.ShouldSpeak(night, 1, "") {
		t.Fatal("23:00 should be outside window")
	}

	overnight := &XiaoduAlertTTSConfig{
		Mode:      "window",
		StartTime: "22:00",
		EndTime:   "08:00",
		Timezone:  "Asia/Shanghai",
	}
	if !overnight.ShouldSpeak(night, 1, "") {
		t.Fatal("23:00 should be inside overnight window")
	}
	morning := time.Date(2026, 6, 27, 7, 0, 0, 0, loc)
	if !overnight.ShouldSpeak(morning, 1, "") {
		t.Fatal("07:00 should be inside overnight window")
	}
	daytime := time.Date(2026, 6, 27, 9, 0, 0, 0, loc)
	if overnight.ShouldSpeak(daytime, 1, "") {
		t.Fatal("09:00 should be outside overnight window")
	}
}
