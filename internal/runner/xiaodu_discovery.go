package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"switch-monitor/internal/adapter"
	"switch-monitor/internal/config"
)

// xiaoduDiscoverTTL bounds how often the cloud device list is consulted. Every
// TTS, probe and Telegram command funnels through the resolver, so without a
// cache a single alert storm would hammer Baidu's API and invite rate limiting.
const xiaoduDiscoverTTL = 5 * time.Minute

var xiaoduIPCache struct {
	mu     sync.Mutex
	ip     string
	device adapter.XiaoduDevice
	at     time.Time
}

// InvalidateXiaoduIPCache forces the next resolver call to re-query the cloud.
// Called after a DLNA failure (the speaker may have moved again) and on config
// reload (credentials may have changed).
func InvalidateXiaoduIPCache() {
	xiaoduIPCache.mu.Lock()
	xiaoduIPCache.ip = ""
	xiaoduIPCache.device = adapter.XiaoduDevice{}
	xiaoduIPCache.at = time.Time{}
	xiaoduIPCache.mu.Unlock()
}

func cachedXiaoduIP() (string, adapter.XiaoduDevice, bool) {
	xiaoduIPCache.mu.Lock()
	defer xiaoduIPCache.mu.Unlock()
	if xiaoduIPCache.ip == "" || time.Since(xiaoduIPCache.at) >= xiaoduDiscoverTTL {
		return "", adapter.XiaoduDevice{}, false
	}
	return xiaoduIPCache.ip, xiaoduIPCache.device, true
}

func storeXiaoduIP(ip string, dev adapter.XiaoduDevice) {
	xiaoduIPCache.mu.Lock()
	xiaoduIPCache.ip = ip
	xiaoduIPCache.device = dev
	xiaoduIPCache.at = time.Now()
	xiaoduIPCache.mu.Unlock()
}

// EffectiveXiaoduIP returns the LAN address the speaker answers on right now.
//
// Cloud discovery is preferred but never fatal: if BDUSS is missing, the API
// fails, or the device reports no address, the configured IP is returned so a
// static setup (or a DHCP reservation) keeps working exactly as before.
func EffectiveXiaoduIP(ctx context.Context, cfg *config.XiaoduConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("xiaodu config is missing")
	}
	configured := strings.TrimSpace(cfg.IP)

	if !cfg.AutoDiscoverIPOrDefault() || strings.TrimSpace(cfg.BDUSS) == "" {
		if configured == "" {
			return "", fmt.Errorf("xiaodu ip is required in config (or set bduss to enable discovery)")
		}
		return configured, nil
	}

	if ip, _, ok := cachedXiaoduIP(); ok {
		return ip, nil
	}

	dev, err := newXiaoduClient(cfg).DiscoverDevice(ctx)
	if err != nil {
		slog.Warn("Xiaodu ip discovery failed, falling back to configured ip",
			"configured_ip", configured, "err", err)
		if configured == "" {
			return "", fmt.Errorf("xiaodu ip discovery failed and no ip is configured: %w", err)
		}
		return configured, nil
	}
	if strings.TrimSpace(dev.IP) == "" {
		slog.Warn("Xiaodu device reported no ip address, falling back to configured ip",
			"device", dev.Name, "configured_ip", configured)
		if configured == "" {
			return "", fmt.Errorf("xiaodu device %s has no reported ip and none is configured", dev.Name)
		}
		return configured, nil
	}

	if configured != "" && configured != dev.IP {
		slog.Info("Xiaodu ip changed, following the device",
			"configured_ip", configured, "discovered_ip", dev.IP, "device", dev.Name)
	}
	storeXiaoduIP(dev.IP, *dev)
	return dev.IP, nil
}

// XiaoduClientFor builds a client pointed at the speaker's current address.
func XiaoduClientFor(ctx context.Context, cfg *config.XiaoduConfig) (*adapter.XiaoduClient, error) {
	ip, err := EffectiveXiaoduIP(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c := newXiaoduClient(cfg)
	c.SetEndpoint(ip, cfg.PortOrDefault())
	return c, nil
}

// DiscoverXiaoduNow forces a fresh cloud lookup, bypassing the cache (CLI/Telegram).
func DiscoverXiaoduNow(ctx context.Context, cfg *config.XiaoduConfig) (*adapter.XiaoduDevice, error) {
	if cfg == nil {
		return nil, fmt.Errorf("xiaodu config is missing")
	}
	dev, err := newXiaoduClient(cfg).DiscoverDevice(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(dev.IP) != "" {
		storeXiaoduIP(dev.IP, *dev)
	}
	return dev, nil
}

// FormatXiaoduDevice renders a discovered speaker for CLI and Telegram output.
func FormatXiaoduDevice(d *adapter.XiaoduDevice, configuredIP string) string {
	if d == nil {
		return "no device"
	}
	online := "offline"
	if d.Online {
		online = "online"
	}
	last := "-"
	if !d.OnlineTime.IsZero() {
		last = d.OnlineTime.Local().Format("2006-01-02 15:04:05")
	}
	lines := []string{
		fmt.Sprintf("Name:        %s (%s)", d.Name, online),
		fmt.Sprintf("IP:          %s", valueOrDash(d.IP)),
		fmt.Sprintf("Config IP:   %s", valueOrDash(configuredIP)),
		fmt.Sprintf("MAC:         %s", valueOrDash(d.MAC)),
		fmt.Sprintf("SSID:        %s", valueOrDash(d.SSID)),
		fmt.Sprintf("CUID:        %s", valueOrDash(d.CUID)),
		fmt.Sprintf("Firmware:    %s", valueOrDash(d.DeviceVersion)),
		fmt.Sprintf("Protocol:    %s", valueOrDash(d.ProtocolVersion)),
		fmt.Sprintf("Last online: %s", last),
	}
	if configuredIP != "" && d.IP != "" && configuredIP != d.IP {
		lines = append(lines, "⚠️  config ip is stale, discovery will override it")
	}
	return strings.Join(lines, "\n")
}

func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
