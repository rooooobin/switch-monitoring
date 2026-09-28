package adapter

import (
	"os"
	"testing"
)

func TestParseTokenPlanPersonalUsageJSON(t *testing.T) {
	usage, err := os.ReadFile("testdata/bailian_personal_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := os.ReadFile("testdata/bailian_personal_subscription.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile("testdata/bailian_personal_quota_config.json")
	if err != nil {
		t.Fatal(err)
	}
	u, err := parseTokenPlanPersonalUsageJSON(usage, sub, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if u.PlanName != "Pro" {
		t.Fatalf("plan name=%q", u.PlanName)
	}
	if u.Status != "VALID" {
		t.Fatalf("status=%q", u.Status)
	}
	if len(u.Windows) < 2 {
		t.Fatalf("windows=%+v", u.Windows)
	}
	var five, week *BailianUsageWindow
	for i := range u.Windows {
		w := &u.Windows[i]
		switch w.Name {
		case "5-hour":
			five = w
		case "7-day":
			week = w
		}
	}
	if five == nil || week == nil {
		t.Fatalf("missing windows: %+v", u.Windows)
	}
	if !five.HasTotals || five.Total != 12000 {
		t.Fatalf("5h totals: %+v", five)
	}
	if !week.HasTotals || week.Total != 40000 {
		t.Fatalf("7d totals: %+v", week)
	}
	if five.Remaining <= 0 || five.Remaining > five.Total {
		t.Fatalf("5h remaining: %+v", five)
	}
	if !five.HasReset || !week.HasReset {
		t.Fatalf("expected reset times: five=%v week=%v", five.HasReset, week.HasReset)
	}
}

func TestParseTokenPlanPersonalUsage_PercentageOnly(t *testing.T) {
	usage, err := os.ReadFile("testdata/bailian_personal_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	u, err := parseTokenPlanPersonalUsageJSON(usage, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Windows) == 0 {
		t.Fatal("expected windows")
	}
	if u.Windows[0].HasTotals {
		t.Fatal("expected no totals without quota-config")
	}
}
