package adapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodeIkuaiText(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"web-ssh":          "web-ssh",
		"中文名":              "中文名",
		"remote port":      "remote%20port",
		"a  b":             "a%20b", // whitespace is normalised
		"100%":             "100%25",
		`say "hi"`:         "say%20%22hi%22",
		"a&b;c":            "a%26b%3Bc",
		`back\slash|pipe`:  "back%5Cslash%7Cpipe",
		"quote'and`tick$x": "quote%27and%60tick%24x",
		"multi\nline":      "multi%20line",
	}
	for in, want := range cases {
		if got := EncodeIkuaiText(in); got != want {
			t.Errorf("EncodeIkuaiText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeIkuaiText(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"web-ssh":             "web-ssh",
		"remote%20port":       "remote port",
		"中文%20备注":             "中文 备注",
		"100%25":              "100%",
		"a%2Fb":               "a/b",
		"Nintendo%20Switch":   "Nintendo Switch",
		"already plain a/b:c": "already plain a/b:c",
	}
	for in, want := range cases {
		if got := DecodeIkuaiText(in); got != want {
			t.Errorf("DecodeIkuaiText(%q) = %q, want %q", in, got, want)
		}
	}

	// encode(decode(x)) must be stable for values that came from the router.
	const stored = "datutu%20wsl%20images"
	if got := EncodeIkuaiText(DecodeIkuaiText(stored)); got != stored {
		t.Errorf("round trip changed stored value: %q -> %q", stored, got)
	}
}

func TestValidateDNATRuleSpec(t *testing.T) {
	valid := DNATRuleSpec{
		Name:     "nas-ssh",
		Comment:  "群晖 ssh",
		Protocol: "tcp",
		WanPort:  "60022",
		LanAddr:  "192.168.2.10",
		LanPort:  "22",
	}
	if err := ValidateDNATRuleSpec(valid); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}

	rangeSpec := valid
	rangeSpec.WanPort = "8000-8010"
	rangeSpec.LanPort = "8000-8010"
	if err := ValidateDNATRuleSpec(rangeSpec); err != nil {
		t.Fatalf("port range rejected: %v", err)
	}

	multiSpec := valid
	multiSpec.WanPort = "8080,8081"
	multiSpec.LanPort = "9080,9081"
	multiSpec.Protocol = "tcp+udp"
	if err := ValidateDNATRuleSpec(multiSpec); err != nil {
		t.Fatalf("multi port spec rejected: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*DNATRuleSpec)
		want   string
	}{
		{"empty name", func(s *DNATRuleSpec) { s.Name = " " }, "name"},
		{"long name", func(s *DNATRuleSpec) { s.Name = "abcdefghijklmnop" }, "too long"},
		{"space in name", func(s *DNATRuleSpec) { s.Name = "nas ssh" }, "invalid name"},
		{"dot in name", func(s *DNATRuleSpec) { s.Name = "nas.ssh" }, "invalid name"},
		{"leading dash", func(s *DNATRuleSpec) { s.Name = "-nas" }, "invalid name"},
		{"bad protocol", func(s *DNATRuleSpec) { s.Protocol = "icmp" }, "protocol"},
		{"bad interface", func(s *DNATRuleSpec) { s.Interface = "wan1;rm" }, "interface"},
		{"empty wan", func(s *DNATRuleSpec) { s.WanPort = "" }, "wan_port"},
		{"wan zero", func(s *DNATRuleSpec) { s.WanPort = "0" }, "wan_port"},
		{"wan too big", func(s *DNATRuleSpec) { s.WanPort = "70000" }, "wan_port"},
		{"reversed range", func(s *DNATRuleSpec) { s.WanPort = "8100-8000" }, "range end"},
		{"count mismatch", func(s *DNATRuleSpec) {
			s.WanPort = "8000-8002"
			s.LanPort = "8000-8001"
		}, "same count"},
		{"bad lan addr", func(s *DNATRuleSpec) { s.LanAddr = "192.168.2" }, "lan_addr"},
		{"long comment", func(s *DNATRuleSpec) { s.Comment = strings.Repeat("a", DNATMaxCommentLen+1) }, "comment"},
		{"comment leading dash", func(s *DNATRuleSpec) { s.Comment = "-hidden" }, "comment"},
		{"bad src", func(s *DNATRuleSpec) { s.SrcAddr = []string{"not-an-ip"} }, "src_addr"},
	}
	for _, tc := range bad {
		spec := valid
		tc.mutate(&spec)
		err := ValidateDNATRuleSpec(spec)
		if err == nil {
			t.Errorf("%s: expected error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// newDNATTestServer serves login plus a scripted /Action/call.
func newDNATTestServer(t *testing.T, handler func(action string, param map[string]interface{}) interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Action/login":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"Result": 10000, "code": 0, "message": "成功"})
		case "/Action/call":
			var req struct {
				FuncName string                 `json:"func_name"`
				Action   string                 `json:"action"`
				Param    map[string]interface{} `json:"param"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			if req.FuncName != "dnat" {
				t.Errorf("func_name = %q, want dnat", req.FuncName)
			}
			_ = json.NewEncoder(w).Encode(handler(req.Action, req.Param))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
}

func TestCreateDNATRule_EncodesParam(t *testing.T) {
	var captured map[string]interface{}
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		switch action {
		case "show":
			return map[string]interface{}{"code": 0, "results": map[string]interface{}{"data": []interface{}{}, "total": 0}}
		case "add":
			captured = param
			return map[string]interface{}{"code": 0, "message": "Success", "rowid": 42}
		}
		t.Fatalf("unexpected action %q", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}

	id, err := c.CreateDNATRule(DNATRuleSpec{
		Name:     "NAS",
		Comment:  "群晖  ssh", // double space must be normalised before encoding
		Protocol: "tcp",
		WanPort:  "60022",
		LanAddr:  "192.168.2.10",
		LanPort:  "22",
	})
	if err != nil {
		t.Fatalf("CreateDNATRule: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if captured == nil {
		t.Fatal("add param not captured")
	}
	if got := captured["tagname"]; got != "NAS" {
		t.Errorf("tagname = %v, want NAS", got)
	}
	if got := captured["comment"]; got != "群晖%20ssh" {
		t.Errorf("comment = %v, want 群晖%%20ssh", got)
	}
	if got := captured["enabled"]; got != "yes" {
		t.Errorf("enabled = %v, want yes", got)
	}
	if got := captured["interface"]; got != "all" {
		t.Errorf("interface = %v, want all", got)
	}
	if got := captured["lan_port"]; got != "22" {
		t.Errorf("lan_port = %v, want 22", got)
	}
	if _, ok := captured["id"]; ok {
		t.Errorf("add must not carry id: %#v", captured)
	}
	src, ok := captured["src_addr"].(map[string]interface{})
	if !ok {
		t.Fatalf("src_addr missing: %#v", captured["src_addr"])
	}
	if _, ok := src["custom"].([]interface{}); !ok {
		t.Errorf("src_addr.custom = %#v, want array", src["custom"])
	}
}

func TestCreateDNATRule_RejectsInvalidLocally(t *testing.T) {
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		t.Errorf("router should not be called for an invalid spec (action %q)", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateDNATRule(DNATRuleSpec{
		Name:    "bad name",
		WanPort: "8080",
		LanAddr: "192.168.2.10",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid name") {
		t.Fatalf("err = %v, want invalid name", err)
	}
}

func TestCreateDNATRule_SurfacesRouterError(t *testing.T) {
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		switch action {
		case "show":
			return map[string]interface{}{"code": 0, "results": map[string]interface{}{"data": []interface{}{}}}
		case "add":
			return map[string]interface{}{"code": 2001, "message": "端口冲突", "errors": "该端口已被使用"}
		}
		t.Fatalf("unexpected action %q", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateDNATRule(DNATRuleSpec{
		Name:    "dup",
		WanPort: "8080",
		LanAddr: "192.168.2.10",
		LanPort: "8080",
	})
	if err == nil {
		t.Fatal("expected router error")
	}
	if !strings.Contains(err.Error(), "端口冲突") {
		t.Fatalf("err = %v, want router message", err)
	}
	// The router's error must be visible to the user either directly or via slog,
	// so keep the raw envelope text in the message.
	if !strings.Contains(err.Error(), "2001") {
		t.Logf("note: router code missing from error: %v", err)
	}
}

func TestUpdateDNATRule_PreservesUntouchedFields(t *testing.T) {
	var editParam map[string]interface{}
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		switch action {
		case "show":
			return map[string]interface{}{
				"code": 0,
				"results": map[string]interface{}{
					"data": []interface{}{
						map[string]interface{}{
							"id": 7, "enabled": "no", "tagname": "kosync", "comment": "kosync%20sync",
							"protocol": "tcp", "interface": "wan1", "wan_port": "27200",
							"lan_addr": "192.168.2.154", "lan_addr_int": 3232236186, "lan_port": "7200",
							"src_addr": map[string]interface{}{
								"custom": []interface{}{"203.0.113.0/24"},
								"object": []interface{}{map[string]interface{}{"type": "ipgroup", "gid": 3}},
							},
						},
					},
					"total": 1,
				},
			}
		case "edit":
			editParam = param
			return map[string]interface{}{"code": 0, "message": "Success"}
		}
		t.Fatalf("unexpected action %q", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateDNATRule(7, DNATRuleSpec{Comment: "new comment"}); err != nil {
		t.Fatalf("UpdateDNATRule: %v", err)
	}
	if editParam == nil {
		t.Fatal("edit param not captured")
	}
	checks := map[string]interface{}{
		"id":        float64(7),
		"tagname":   "kosync",
		"comment":   "new%20comment",
		"protocol":  "tcp",
		"interface": "wan1",
		"wan_port":  "27200",
		"lan_addr":  "192.168.2.154",
		"lan_port":  "7200",
		"enabled":   "no", // switch state must survive an edit
	}
	for k, want := range checks {
		if got := editParam[k]; got != want {
			t.Errorf("edit param %s = %#v, want %#v", k, got, want)
		}
	}
	src, ok := editParam["src_addr"].(map[string]interface{})
	if !ok {
		t.Fatalf("src_addr missing")
	}
	custom, ok := src["custom"].([]interface{})
	if !ok || len(custom) != 1 || custom[0] != "203.0.113.0/24" {
		t.Errorf("src_addr.custom = %#v, want preserved", src["custom"])
	}
	objects, ok := src["object"].([]interface{})
	if !ok || len(objects) != 1 {
		t.Errorf("src_addr.object = %#v, want preserved", src["object"])
	}
}

func TestUpdateDNATRule_NotFound(t *testing.T) {
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		return map[string]interface{}{"code": 0, "results": map[string]interface{}{"data": []interface{}{}}}
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	err := c.UpdateDNATRule(99, DNATRuleSpec{Comment: "x"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestDeleteDNATRule(t *testing.T) {
	var delParam map[string]interface{}
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		switch action {
		case "show":
			return map[string]interface{}{
				"code": 0,
				"results": map[string]interface{}{
					"data": []interface{}{
						map[string]interface{}{"id": 3, "enabled": "yes", "wan_port": "8080", "lan_addr": "192.168.2.10", "lan_port": "80"},
						map[string]interface{}{"id": 4, "enabled": "yes", "wan_port": "8081", "lan_addr": "192.168.2.10", "lan_port": "81"},
					},
				},
			}
		case "del":
			delParam = param
			return map[string]interface{}{"code": 0, "message": "Success"}
		}
		t.Fatalf("unexpected action %q", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteDNATRule(3, 4); err != nil {
		t.Fatalf("DeleteDNATRule: %v", err)
	}
	if delParam["id"] != "3,4" {
		t.Fatalf("del id = %#v, want 3,4", delParam["id"])
	}

	if err := c.DeleteDNATRule(99); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("deleting a missing rule: err = %v, want not found", err)
	}
}

func TestToggleDNATRule_v4DisabledThenEnabled(t *testing.T) {
	enabled := "yes"
	var edits []map[string]interface{}
	srv := newDNATTestServer(t, func(action string, param map[string]interface{}) interface{} {
		switch action {
		case "show":
			return map[string]interface{}{
				"code": 0,
				"results": map[string]interface{}{
					"data": []interface{}{
						map[string]interface{}{
							"id": 11, "enabled": enabled, "tagname": "web", "comment": "x",
							"protocol": "tcp", "interface": "all", "wan_port": "60080",
							"lan_addr": "192.168.2.1", "lan_port": "443",
							"src_addr": map[string]interface{}{"custom": []interface{}{}, "object": []interface{}{}},
						},
					},
				},
			}
		case "edit":
			edits = append(edits, param)
			if v, ok := param["enabled"].(string); ok {
				enabled = v
			}
			return map[string]interface{}{"code": 0, "message": "Success"}
		}
		t.Fatalf("unexpected action %q", action)
		return nil
	})
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if err := c.ToggleDNATRule(11, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := c.ToggleDNATRule(11, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(edits) != 2 {
		t.Fatalf("got %d edits, want 2", len(edits))
	}
	if edits[0]["enabled"] != "no" || edits[1]["enabled"] != "yes" {
		t.Fatalf("unexpected enabled sequence: %v, %v", edits[0]["enabled"], edits[1]["enabled"])
	}
	if edits[1]["tagname"] != "web" || edits[1]["wan_port"] != "60080" {
		t.Fatalf("toggle dropped rule fields: %#v", edits[1])
	}
}
