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

// XiaoduDevice is one speaker registered to the Baidu account, as reported by
// GET https://xiaodu.baidu.com/saiya/device/list.
//
// The speaker's LAN address is not stable: it is handed out by DHCP and changes
// across reboots, which silently breaks every DLNA call. The cloud record carries
// the address the device last reported (NetworkInfos), so it is the authoritative
// source for "where is the speaker right now".
type XiaoduDevice struct {
	DeviceID        string
	Name            string
	MAC             string
	SN              string
	CUID            string
	ClientID        string
	AppID           string
	DeviceVersion   string
	ProtocolVersion string
	LinkType        string
	IP              string
	SSID            string
	RouterMAC       string
	Online          bool
	OnlineTime      time.Time
}

// xiaoduDeviceListResponse mirrors the cloud payload. NetworkInfos is a
// JSON-encoded *string* holding an array, so it needs a second decode pass.
type xiaoduDeviceListResponse struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
	Data   struct {
		List []struct {
			DeviceID        string `json:"device_id"`
			Name            string `json:"name"`
			DeviceVersion   string `json:"device_version"`
			MAC             string `json:"mac"`
			SN              string `json:"sn"`
			CUID            string `json:"cuid"`
			ClientID        string `json:"client_id"`
			AppID           string `json:"appid"`
			ProtocolVersion string `json:"protocol_version"`
			LinkType        string `json:"link_type"`
			OnlineStatus    bool   `json:"online_status"`
			OnlineTime      int64  `json:"online_time"`
			NetworkInfos    string `json:"network_infos"`
		} `json:"list"`
	} `json:"data"`
}

type xiaoduNetworkInfo struct {
	DeviceMac string `json:"deviceMac"`
	RouterMac string `json:"routerMac"`
	IPAddress string `json:"ipAddress"`
	SSID      string `json:"ssid"`
}

// ListDevices returns every speaker bound to the account. Read-only; it is the
// same call CheckBDUSS uses, but keeps the parsed records.
func (c *XiaoduClient) ListDevices(ctx context.Context) ([]XiaoduDevice, error) {
	if strings.TrimSpace(c.dueros.BDUSS) == "" {
		return nil, fmt.Errorf("dueros bduss is required for device discovery")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, duerosDeviceListURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "BDUSS="+c.dueros.BDUSS)
	req.Header.Set("User-Agent", xiaoduUserAgent)
	req.Header.Set("Referer", xiaoduH5Referer)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("device list HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	var parsed xiaoduDeviceListResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode device list response: %w", err)
	}
	if err := duerosStatusError(parsed.Status, parsed.Msg); err != nil {
		return nil, err
	}

	out := make([]XiaoduDevice, 0, len(parsed.Data.List))
	for _, d := range parsed.Data.List {
		dev := XiaoduDevice{
			DeviceID:        d.DeviceID,
			Name:            d.Name,
			MAC:             strings.ToLower(strings.TrimSpace(d.MAC)),
			SN:              d.SN,
			CUID:            strings.ToLower(strings.TrimSpace(d.CUID)),
			ClientID:        d.ClientID,
			AppID:           d.AppID,
			DeviceVersion:   d.DeviceVersion,
			ProtocolVersion: d.ProtocolVersion,
			LinkType:        d.LinkType,
			Online:          d.OnlineStatus,
		}
		if d.OnlineTime > 0 {
			dev.OnlineTime = time.Unix(d.OnlineTime, 0)
		}
		if ip, ssid, router := pickXiaoduNetwork(d.NetworkInfos); ip != "" {
			dev.IP = ip
			dev.SSID = ssid
			dev.RouterMAC = router
		}
		out = append(out, dev)
	}
	return out, nil
}

// pickXiaoduNetwork decodes the nested network_infos blob and returns the first
// entry that carries a usable address. Malformed input yields empty values
// instead of an error: a missing IP only downgrades discovery to the config value.
func pickXiaoduNetwork(blob string) (ip, ssid, routerMAC string) {
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return "", "", ""
	}
	var infos []xiaoduNetworkInfo
	if err := json.Unmarshal([]byte(blob), &infos); err != nil {
		return "", "", ""
	}
	for _, n := range infos {
		addr := strings.TrimSpace(n.IPAddress)
		if addr == "" {
			continue
		}
		return addr, n.SSID, strings.ToLower(strings.TrimSpace(n.RouterMac))
	}
	return "", "", ""
}

// DiscoverDevice resolves the speaker this client is configured for.
//
// Matching is deliberately strict: the CUID from config must equal the cloud
// record's CUID. Only when config carries no CUID do we accept a single-device
// account. With several devices and no CUID we refuse to guess, because pointing
// TTS at the wrong speaker is worse than reporting an error.
func (c *XiaoduClient) DiscoverDevice(ctx context.Context) (*XiaoduDevice, error) {
	devices, err := c.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	return matchXiaoduDevice(devices, strings.TrimSpace(c.dueros.CUID), strings.TrimSpace(c.dueros.ClientID))
}

func matchXiaoduDevice(devices []XiaoduDevice, cuid, clientID string) (*XiaoduDevice, error) {
	if len(devices) == 0 {
		return nil, fmt.Errorf("no xiaodu device bound to this account")
	}

	if cuid != "" {
		for i := range devices {
			if devices[i].CUID == strings.ToLower(cuid) {
				return &devices[i], nil
			}
		}
		// The H5 console hands out a client_id that differs in case from the
		// device record's, so compare case-insensitively as a second key.
		for i := range devices {
			if clientID != "" && strings.EqualFold(devices[i].ClientID, clientID) {
				return &devices[i], nil
			}
		}
		return nil, fmt.Errorf("no device matches cuid %s (account has %d)", cuid, len(devices))
	}

	if len(devices) > 1 {
		names := make([]string, 0, len(devices))
		for _, d := range devices {
			names = append(names, fmt.Sprintf("%s/%s", d.Name, d.CUID))
		}
		return nil, fmt.Errorf("account has %d devices, set xiaodu.cuid to pick one: %s",
			len(devices), strings.Join(names, ", "))
	}
	return &devices[0], nil
}

// DiscoverIP resolves the speaker's current LAN address from the cloud.
func (c *XiaoduClient) DiscoverIP(ctx context.Context) (string, error) {
	dev, err := c.DiscoverDevice(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(dev.IP) == "" {
		return "", fmt.Errorf("device %s has no reported ip address", dev.Name)
	}
	return dev.IP, nil
}

// SetEndpoint retargets the client at a new LAN address, keeping the DLNA port.
// Safe to call while other goroutines are issuing SOAP requests.
func (c *XiaoduClient) SetEndpoint(ip string, port int) {
	if port <= 0 {
		port = 49494
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ip = ip
	c.port = port
	c.baseURL = fmt.Sprintf("http://%s:%d", ip, port)
}

// IP returns the address the client currently talks to.
func (c *XiaoduClient) IP() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ip
}

// BaseURL returns the DLNA control base URL.
func (c *XiaoduClient) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// duerosStatusError maps the cloud envelope's status code onto an error.
func duerosStatusError(status int, msg string) error {
	switch status {
	case 0:
		return nil
	case 2:
		return fmt.Errorf("dueros not logged in (BDUSS expired or invalid)")
	default:
		msg = strings.TrimSpace(msg)
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("dueros API error (status=%d): %s", status, msg)
	}
}
