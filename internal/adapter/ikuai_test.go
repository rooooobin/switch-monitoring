package adapter

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
)

func newTestIkuaiClient(t *testing.T, srv *httptest.Server) *IkuaiClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &IkuaiClient{
		url:      srv.URL,
		username: "admin",
		password: "secret",
		client: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

func TestLogin_v3Response(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Action/login" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"result": 10000,
			"errMsg": "Success",
		})
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatalf("Login() error: %v", err)
	}
}

func TestLogin_v4Response(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"Result":  10000,
			"code":    0,
			"message": "成功",
		})
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatalf("Login() error: %v", err)
	}
}

func TestGetDNATRules_v3Response(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Action/login":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": 10000})
		case "/Action/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"result": 30000,
				"data": map[string]interface{}{
					"data": []map[string]interface{}{
						{"id": 1, "enabled": "yes", "comment": "test", "protocol": "tcp", "wan_port": "8080", "lan_addr": "192.168.1.10", "lan_port": "80"},
					},
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	rules, err := c.GetDNATRules()
	if err != nil {
		t.Fatalf("GetDNATRules() error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].ID() != 1 || rules[0].Enabled() != "yes" {
		t.Fatalf("unexpected rule: id=%d enabled=%s", rules[0].ID(), rules[0].Enabled())
	}
}

func TestGetDNATRules_v4Response(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Action/login":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"Result": 10000, "code": 0, "message": "成功"})
		case "/Action/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    0,
				"message": "Success",
				"results": map[string]interface{}{
					"data": []map[string]interface{}{
						{"id": 2, "enabled": "no", "comment": "remote%20port%20forward", "protocol": "tcp", "wan_port": "60080", "lan_addr": "192.168.2.1", "lan_port": "443"},
					},
					"total": 1,
				},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	rules, err := c.GetDNATRules()
	if err != nil {
		t.Fatalf("GetDNATRules() error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	if rules[0].ID() != 2 || rules[0].Comment() != "remote port forward" {
		t.Fatalf("unexpected rule: id=%d comment=%q", rules[0].ID(), rules[0].Comment())
	}
}

func TestGetDNATRules_v3FuncNotFound(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Action/login":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"result": 10000})
		case "/Action/call":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"result": 30002,
				"errMsg": "Not found funcname",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetDNATRules(); err == nil {
		t.Fatal("expected error for missing func_name")
	}
}

func TestToggleDNATRule_v4Response(t *testing.T) {
	var editBody map[string]interface{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Action/login":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"Result": 10000, "code": 0})
		case "/Action/call":
			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)
			action, _ := req["action"].(string)
			switch action {
			case "show":
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"code": 0,
					"results": map[string]interface{}{
						"data": []map[string]interface{}{
							{"id": 5, "enabled": "yes", "protocol": "tcp", "wan_port": "8080", "lan_addr": "10.0.0.1", "lan_port": "80"},
						},
					},
				})
			case "edit":
				editBody = req
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "message": "Success"})
			default:
				t.Fatalf("unexpected action: %s", action)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := newTestIkuaiClient(t, srv)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if err := c.ToggleDNATRule(5, false); err != nil {
		t.Fatalf("ToggleDNATRule() error: %v", err)
	}
	param, ok := editBody["param"].(map[string]interface{})
	if !ok {
		t.Fatalf("edit param missing: %#v", editBody)
	}
	if param["enabled"] != "no" {
		t.Fatalf("enabled=%v, want no", param["enabled"])
	}
}
