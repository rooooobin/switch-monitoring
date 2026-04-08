package calendar

import (
	"testing"
	"time"
)

func TestDayBounds(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 8, 15, 30, 0, 0, loc)
	start, end := dayBounds(loc, now)
	if got := start.Format(time.RFC3339); got != "2026-04-08T00:00:00-07:00" {
		t.Fatalf("start: got %s", got)
	}
	if want := start.Add(24 * time.Hour); !end.Equal(want) {
		t.Fatalf("end: got %v want %v", end, want)
	}
}

func TestRepairWindow(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 8, 22, 0, 0, 0, loc)
	rs, re := repairWindow(loc, now)
	if rs.Hour() != 8 || re.Hour() != 20 {
		t.Fatalf("repair window: %v .. %v", rs, re)
	}
}

func TestHTTPClientEmptyProxy(t *testing.T) {
	c, err := HTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Transport != nil {
		t.Fatal("expected default transport")
	}
}
