package runner

import (
	"testing"

	"switch-monitor/internal/checker"
)

func TestBuildAlertTTSText(t *testing.T) {
	aliases := map[string]map[int]string{
		"mercury-sg108": {6: "Mine"},
	}
	one := []checker.AlertEvent{{
		SwitchName: "mercury-sg108",
		PortID:     6,
		Reason:     checker.ReasonDown,
	}}
	got := buildAlertTTSText(one, aliases)
	want := "mercury-sg108 Mine 链路中断"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	many := []checker.AlertEvent{
		{SwitchName: "a", PortID: 1, Reason: checker.ReasonDown},
		{SwitchName: "b", PortID: 2, Reason: checker.ReasonLowSpeed},
	}
	got = buildAlertTTSText(many, nil)
	if got != "网络监控发现 2 个问题" {
		t.Fatalf("got %q", got)
	}
}
