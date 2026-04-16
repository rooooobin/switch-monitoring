package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	req, err := http.NewRequestWithContext(ctx, "GET", c.base+"/proxies", nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /proxies: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out proxiesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode /proxies: %w", err)
	}
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
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PUT /proxies/%s: %s: %s", groupName, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
