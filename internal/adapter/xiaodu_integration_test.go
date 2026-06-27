//go:build integration

package adapter

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// Live DLNA test against a Xiaodu speaker on the LAN:
//
//	XIAODU_IP=192.168.2.162 go test -tags=integration -v ./internal/adapter/ -run TestLiveXiaodu
func TestLiveXiaoduDLNA(t *testing.T) {
	ip := os.Getenv("XIAODU_IP")
	if ip == "" {
		t.Skip("set XIAODU_IP to run live test")
	}
	port := 49494
	if p := os.Getenv("XIAODU_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}

	c := NewXiaoduClient(ip, port, XiaoduDuerOSConfig{})
	st, err := c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Volume < 0 || st.Volume > 100 {
		t.Fatalf("unexpected volume: %d", st.Volume)
	}
}
