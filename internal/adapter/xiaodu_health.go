package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var duerosDeviceListURL = "https://xiaodu.baidu.com/saiya/device/list"

// ProbeOnline checks DLNA reachability by querying volume.
func (c *XiaoduClient) ProbeOnline(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.GetVolume(ctx)
	return err
}

// CheckBDUSS validates DuerOS credentials via the device list API.
func (c *XiaoduClient) CheckBDUSS(ctx context.Context) error {
	ready, _ := c.duerosConfigState()
	if !ready {
		return fmt.Errorf("dueros credentials not configured (client_id, cuid, bduss required)")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, duerosDeviceListURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", "BDUSS="+c.dueros.BDUSS)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("device list HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var result struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decode device list response: %w", err)
	}
	switch result.Status {
	case 0:
		return nil
	case 2:
		return fmt.Errorf("dueros not logged in (BDUSS expired or invalid)")
	default:
		msg := strings.TrimSpace(result.Msg)
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("dueros device list failed (status=%d): %s", result.Status, msg)
	}
}
