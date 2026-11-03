package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// deviceListPayload mirrors a real /saiya/device/list response, including the
// nested JSON-encoded network_infos string that carries the live LAN address.
func deviceListPayload(ip, cuid, mac string, online bool) string {
	infos, _ := json.Marshal([]map[string]string{{
		"deviceMac": mac,
		"routerMac": "f4:84:8d:5f:b8:80",
		"ipAddress": ip,
		"ssid":      "27-1-8B",
	}})
	rec := map[string]any{
		"device_id":        "dev-1",
		"name":             "小度智能音箱",
		"device_version":   "2.30.8.1762308851",
		"mac":              mac,
		"sn":               "91206/3B2N160017828",
		"cuid":             cuid,
		"client_id":        "D4Hq9Q5cXtkNGHOVzudbr8Ki5R7e6w7r",
		"protocol_version": "dcs/v1",
		"online_status":    online,
		"online_time":      1793557658,
		"network_infos":    string(infos),
	}
	body, _ := json.Marshal(map[string]any{
		"status": 0,
		"msg":    "STATUS_OK",
		"data":   map[string]any{"list": []any{rec}},
	})
	return string(body)
}

func newDiscoveryTestClient(t *testing.T, cuid string, handler http.HandlerFunc) *XiaoduClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	prev := duerosDeviceListURL
	duerosDeviceListURL = srv.URL
	t.Cleanup(func() { duerosDeviceListURL = prev })

	return NewXiaoduClient("192.168.2.162", 49494, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     cuid,
		BDUSS:    "bduss-test",
	})
}

func TestXiaoduListDevicesParsesNetworkInfos(t *testing.T) {
	var gotCookie, gotReferer string
	c := newDiscoveryTestClient(t, "418dd4b1b87d0c53f73ad947d5d932b0",
		func(w http.ResponseWriter, r *http.Request) {
			gotCookie = r.Header.Get("Cookie")
			gotReferer = r.Header.Get("Referer")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(deviceListPayload(
				"192.168.2.165", "418dd4b1b87d0c53f73ad947d5d932b0", "88:2D:53:19:E7:32", true)))
		})

	devices, err := c.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	d := devices[0]
	if d.IP != "192.168.2.165" {
		t.Errorf("IP = %q, want 192.168.2.165", d.IP)
	}
	if d.Name != "小度智能音箱" {
		t.Errorf("Name = %q", d.Name)
	}
	// MAC must be normalised to lower case: matching is case-sensitive downstream.
	if d.MAC != "88:2d:53:19:e7:32" {
		t.Errorf("MAC = %q, want lowercase", d.MAC)
	}
	if d.CUID != "418dd4b1b87d0c53f73ad947d5d932b0" {
		t.Errorf("CUID = %q", d.CUID)
	}
	if d.ProtocolVersion != "dcs/v1" {
		t.Errorf("ProtocolVersion = %q", d.ProtocolVersion)
	}
	if !d.Online {
		t.Error("Online = false, want true")
	}
	if d.OnlineTime.IsZero() {
		t.Error("OnlineTime not parsed")
	}
	if d.SSID != "27-1-8B" {
		t.Errorf("SSID = %q", d.SSID)
	}
	if !strings.Contains(gotCookie, "BDUSS=bduss-test") {
		t.Errorf("Cookie header = %q, want BDUSS", gotCookie)
	}
	if gotReferer == "" {
		t.Error("Referer header not set")
	}
}

func TestXiaoduListDevicesRequiresBDUSS(t *testing.T) {
	c := NewXiaoduClient("10.0.0.1", 49494, XiaoduDuerOSConfig{CUID: "x"})
	if _, err := c.ListDevices(context.Background()); err == nil {
		t.Fatal("expected error without bduss")
	}
}

func TestXiaoduListDevicesPropagatesCloudStatus(t *testing.T) {
	c := newDiscoveryTestClient(t, "cuid", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":2,"msg":"not login"}`))
	})
	_, err := c.ListDevices(context.Background())
	if err == nil {
		t.Fatal("expected error for status=2")
	}
	if !strings.Contains(err.Error(), "BDUSS") {
		t.Errorf("error %q should mention BDUSS", err)
	}
}

func TestPickXiaoduNetwork(t *testing.T) {
	tests := []struct {
		name    string
		blob    string
		wantIP  string
		wantSSI string
	}{
		{"empty", "", "", ""},
		{"malformed json", "not-json", "", ""},
		{"empty array", "[]", "", ""},
		{"no address", `[{"deviceMac":"aa","ssid":"s"}]`, "", ""},
		{"first usable wins", `[{"ipAddress":""},{"ipAddress":"10.1.2.3","ssid":"wifi"}]`, "10.1.2.3", "wifi"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ip, ssid, _ := pickXiaoduNetwork(tc.blob)
			if ip != tc.wantIP || ssid != tc.wantSSI {
				t.Errorf("pickXiaoduNetwork(%q) = (%q,%q), want (%q,%q)",
					tc.blob, ip, ssid, tc.wantIP, tc.wantSSI)
			}
		})
	}
}

func TestMatchXiaoduDevice(t *testing.T) {
	a := XiaoduDevice{Name: "A", CUID: "aaa", IP: "10.0.0.1"}
	b := XiaoduDevice{Name: "B", CUID: "bbb", IP: "10.0.0.2"}

	t.Run("cuid match", func(t *testing.T) {
		got, err := matchXiaoduDevice([]XiaoduDevice{a, b}, "BBB", "")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Name != "B" {
			t.Errorf("matched %q, want B", got.Name)
		}
	})

	t.Run("cuid match is case insensitive", func(t *testing.T) {
		upper := []XiaoduDevice{{Name: "U", CUID: "abcdef", IP: "10.0.0.9"}}
		got, err := matchXiaoduDevice(upper, "ABCDEF", "")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Name != "U" {
			t.Errorf("matched %q, want U", got.Name)
		}
	})

	t.Run("single device without cuid", func(t *testing.T) {
		got, err := matchXiaoduDevice([]XiaoduDevice{a}, "", "")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Name != "A" {
			t.Errorf("matched %q, want A", got.Name)
		}
	})

	t.Run("refuses to guess with several devices", func(t *testing.T) {
		_, err := matchXiaoduDevice([]XiaoduDevice{a, b}, "", "")
		if err == nil {
			t.Fatal("expected error when several devices and no cuid")
		}
		if !strings.Contains(err.Error(), "cuid") {
			t.Errorf("error %q should tell the user to set cuid", err)
		}
	})

	t.Run("unknown cuid", func(t *testing.T) {
		if _, err := matchXiaoduDevice([]XiaoduDevice{a, b}, "zzz", ""); err == nil {
			t.Fatal("expected error for unknown cuid")
		}
	})

	t.Run("no devices", func(t *testing.T) {
		if _, err := matchXiaoduDevice(nil, "", ""); err == nil {
			t.Fatal("expected error for empty list")
		}
	})
}

func TestXiaoduSetEndpointKeepsIPAndPortInSync(t *testing.T) {
	c := NewXiaoduClient("192.168.2.162", 49494, XiaoduDuerOSConfig{})
	if c.IP() != "192.168.2.162" || c.Port() != 49494 {
		t.Fatalf("initial endpoint = %s:%d", c.IP(), c.Port())
	}

	c.SetEndpoint("192.168.2.165", 0)
	if c.IP() != "192.168.2.165" {
		t.Errorf("IP() = %q, want the new address", c.IP())
	}
	if c.Port() != 49494 {
		t.Errorf("Port() = %d, want the default kept", c.Port())
	}
	if !strings.Contains(c.BaseURL(), "192.168.2.165:49494") {
		t.Errorf("BaseURL() = %q", c.BaseURL())
	}

	// An empty address must be ignored rather than blanking a working endpoint.
	c.SetEndpoint("   ", 1234)
	if c.IP() != "192.168.2.165" || c.Port() != 49494 {
		t.Errorf("empty SetEndpoint changed the endpoint to %s:%d", c.IP(), c.Port())
	}
}

func TestDuerOSStatusError(t *testing.T) {
	if err := duerosStatusError(0, ""); err != nil {
		t.Errorf("status 0 should be nil, got %v", err)
	}
	err := duerosStatusError(2, "")
	if err == nil || !strings.Contains(err.Error(), "BDUSS") {
		t.Errorf("status 2 = %v, want a BDUSS hint", err)
	}
	err = duerosStatusError(7, "boom")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("status 7 = %v, want the server message", err)
	}
}
