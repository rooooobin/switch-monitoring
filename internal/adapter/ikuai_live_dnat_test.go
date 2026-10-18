//go:build integration

package adapter

import (
	"os"
	"testing"
)

func TestLiveIkuaiDNATCreateDelete(t *testing.T) {
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

	before, err := c.GetDNATRules()
	if err != nil {
		t.Fatalf("GetDNATRules: %v", err)
	}
	spec := DNATRuleSpec{
		Tagname:  "pi-live-test",
		Comment:  "in+out 中文 test",
		Protocol: "tcp",
		WanPort:  "19999",
		LanAddr:  "192.168.2.178",
		LanPort:  "19999",
	}
	id, err := c.CreateDNATRule(spec)
	if err != nil {
		t.Fatalf("CreateDNATRule: %v", err)
	}
	t.Cleanup(func() {
		if err := c.DeleteDNATRule(id); err != nil {
			t.Logf("cleanup delete: %v", err)
		}
	})

	rules, err := c.GetDNATRules()
	if err != nil {
		t.Fatalf("GetDNATRules after add: %v", err)
	}
	if len(rules) != len(before)+1 {
		t.Fatalf("rule count = %d, want %d", len(rules), len(before)+1)
	}
	var created DNATRule
	for _, r := range rules {
		if r.ID() == id {
			created = r
		}
	}
	if created == nil {
		t.Fatalf("created rule %d not found", id)
	}
	if created.Tagname() != spec.Tagname {
		t.Fatalf("tagname = %q, want %q", created.Tagname(), spec.Tagname)
	}
	if created.Comment() != spec.Comment {
		t.Fatalf("comment = %q, want %q", created.Comment(), spec.Comment)
	}
	if created.String("wan_port") != spec.WanPort || created.String("lan_port") != spec.LanPort {
		t.Fatalf("ports = %s/%s, want %s/%s", created.String("wan_port"), created.String("lan_port"), spec.WanPort, spec.LanPort)
	}

	if err := c.UpdateDNATRule(id, DNATRuleSpec{Comment: "edited comment"}); err != nil {
		t.Fatalf("UpdateDNATRule: %v", err)
	}
	after, err := c.findDNATRule(id)
	if err != nil {
		t.Fatalf("find after edit: %v", err)
	}
	if after.Comment() != "edited comment" {
		t.Fatalf("comment after edit = %q", after.Comment())
	}
	if after.String("wan_port") != spec.WanPort || after.String("lan_addr") != spec.LanAddr {
		t.Fatalf("edit changed unrelated fields: %#v", after)
	}
	if after.Enabled() != "yes" {
		t.Fatalf("edit changed enabled = %q", after.Enabled())
	}

	if err := c.ToggleDNATRule(id, false); err != nil {
		t.Fatalf("ToggleDNATRule off: %v", err)
	}
	off, err := c.findDNATRule(id)
	if err != nil {
		t.Fatal(err)
	}
	if off.Enabled() != "no" {
		t.Fatalf("toggle off did not disable: %q", off.Enabled())
	}

	if err := c.DeleteDNATRule(id); err != nil {
		t.Fatalf("DeleteDNATRule: %v", err)
	}
	final, err := c.GetDNATRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != len(before) {
		t.Fatalf("rule count after delete = %d, want %d", len(final), len(before))
	}
}
