package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MihomoClient talks to a local Mihomo (Clash Meta) external-controller HTTP API.
type MihomoClient struct {
	base   string
	secret string
	client *http.Client
}

// NewMihomoClient creates a client. apiBase is the controller URL (e.g. http://127.0.0.1:9090).
func NewMihomoClient(apiBase, secret string) *MihomoClient {
	base := strings.TrimSpace(apiBase)
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		base = "http://127.0.0.1:9090"
	}
	return &MihomoClient{
		base:   base,
		secret: strings.TrimSpace(secret),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *MihomoClient) setAuth(req *http.Request) {
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
}

// ProxyEntry is a subset of GET /proxies response for one proxy or group.
type ProxyEntry struct {
	Type string   `json:"type"`
	Now  string   `json:"now"`
	All  []string `json:"all"`
}

type proxiesResponse struct {
	Proxies map[string]ProxyEntry `json:"proxies"`
}

// GetProxies returns the full /proxies map.
func (c *MihomoClient) GetProxies(ctx context.Context) (map[string]ProxyEntry, error) {
	urlStr := c.base + "/proxies"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	start := time.Now()
	slog.Info("Mihomo HTTP request", "op", "get_proxies", "method", http.MethodGet, "url", urlStr)
	resp, err := c.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		slog.Error("Mihomo HTTP transport error", "op", "get_proxies", "url", urlStr, "duration_ms", dur.Milliseconds(), "err", err)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	slog.Info("Mihomo HTTP response", "op", "get_proxies", "url", urlStr, "status", resp.StatusCode, "duration_ms", dur.Milliseconds())
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("Mihomo get_proxies non-OK", "status", resp.StatusCode, "body_snip", truncateForLog(body, 200))
		return nil, fmt.Errorf("GET /proxies: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out proxiesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		slog.Error("Mihomo get_proxies decode failed", "err", err)
		return nil, fmt.Errorf("decode /proxies: %w", err)
	}
	slog.Info("Mihomo proxies decoded", "proxy_keys", len(out.Proxies))
	return out.Proxies, nil
}

// SetSelector switches a Selector (or URLTest, etc.) group to the given outbound name.
func (c *MihomoClient) SetSelector(ctx context.Context, groupName, outboundName string) error {
	payload := map[string]string{"name": outboundName}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	u := c.base + "/proxies/" + url.PathEscape(groupName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	slog.Info("Mihomo HTTP request", "op", "set_selector", "method", http.MethodPut, "url", u, "group", groupName, "outbound", outboundName, "body_bytes", len(b))
	resp, err := c.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		slog.Error("Mihomo HTTP transport error", "op", "set_selector", "url", u, "duration_ms", dur.Milliseconds(), "err", err)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	slog.Info("Mihomo HTTP response", "op", "set_selector", "url", u, "status", resp.StatusCode, "duration_ms", dur.Milliseconds())
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		slog.Warn("Mihomo set_selector non-OK", "group", groupName, "outbound", outboundName, "status", resp.StatusCode, "body_snip", truncateForLog(body, 200))
		return fmt.Errorf("PUT /proxies/%s: %s: %s", groupName, resp.Status, strings.TrimSpace(string(body)))
	}
	slog.Info("Mihomo selector updated", "group", groupName, "outbound", outboundName)
	return nil
}

func truncateForLog(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
