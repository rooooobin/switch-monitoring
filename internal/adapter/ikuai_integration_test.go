//go:build integration

package adapter

import (
	"os"
	"testing"
)

// Run against a live iKuai router:
//
//	IKUAI_URL=https://192.168.2.1 IKUAI_USER=admin IKUAI_PASS=secret go test -tags=integration -v ./internal/adapter/ -run TestLiveIkuai
func TestLiveIkuaiDNAT(t *testing.T) {
	url := os.Getenv("IKUAI_URL")
	user := os.Getenv("IKUAI_USER")
	pass := os.Getenv("IKUAI_PASS")
	if url == "" || user == "" || pass == "" {
		t.Skip("set IKUAI_URL, IKUAI_USER, IKUAI_PASS to run live test")
	}

	c, err := NewIkuaiClient(url, user, pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}

	rules, err := c.GetDNATRules()
	if err != nil {
		t.Fatalf("GetDNATRules: %v", err)
	}
	if len(rules) == 0 {
		t.Fatal("expected at least one DNAT rule")
	}

	target := rules[0]
	id := target.ID()
	wasEnabled := target.Enabled() == "yes"

	if err := c.ToggleDNATRule(id, !wasEnabled); err != nil {
		t.Fatalf("ToggleDNATRule off: %v", err)
	}
	if err := c.ToggleDNATRule(id, wasEnabled); err != nil {
		t.Fatalf("ToggleDNATRule restore: %v", err)
	}
}
