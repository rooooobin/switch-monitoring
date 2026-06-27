package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeOnline(t *testing.T) {
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<CurrentVolume>30</CurrentVolume>`))
	})
	defer srv.Close()

	if err := c.ProbeOnline(context.Background()); err != nil {
		t.Fatalf("ProbeOnline: %v", err)
	}
}

func TestCheckBDUSS_ok(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "BDUSS=test-bduss") {
			t.Fatalf("missing cookie: %q", r.Header.Get("Cookie"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 0})
	}))
	defer srv.Close()

	orig := duerosDeviceListURL
	duerosDeviceListURL = srv.URL
	t.Cleanup(func() { duerosDeviceListURL = orig })

	c := NewXiaoduClient("127.0.0.1", 49494, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     "cuid1234567890ab",
		BDUSS:    "test-bduss",
	})
	if err := c.CheckBDUSS(context.Background()); err != nil {
		t.Fatalf("CheckBDUSS: %v", err)
	}
}

func TestCheckBDUSS_expired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 2, "msg": "not login"})
	}))
	defer srv.Close()

	orig := duerosDeviceListURL
	duerosDeviceListURL = srv.URL
	t.Cleanup(func() { duerosDeviceListURL = orig })

	c := NewXiaoduClient("127.0.0.1", 49494, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     "cuid1234567890ab",
		BDUSS:    "test-bduss",
	})
	if err := c.CheckBDUSS(context.Background()); err == nil {
		t.Fatal("expected error for expired BDUSS")
	}
}

func TestCheckBDUSS_notConfigured(t *testing.T) {
	c := NewXiaoduClient("127.0.0.1", 49494, XiaoduDuerOSConfig{})
	if err := c.CheckBDUSS(context.Background()); err == nil {
		t.Fatal("expected error when credentials missing")
	}
}
