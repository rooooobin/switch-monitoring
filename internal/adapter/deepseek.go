package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultDeepSeekBase = "https://api.deepseek.com"

// DeepSeekBalance is the account balance from GET /user/balance.
type DeepSeekBalance struct {
	IsAvailable  bool
	Currency     string
	Total        string
	Granted      string
	ToppedUp     string
	RateLimitRPM *int
	RateLimitRPMRemaining *int
}

// DeepSeekClient queries DeepSeek account APIs.
type DeepSeekClient struct {
	base   string
	apiKey string
	client *http.Client
}

// NewDeepSeekClient creates a client. apiBase may be empty for the default host.
func NewDeepSeekClient(apiBase, apiKey, proxy string) (*DeepSeekClient, error) {
	base := strings.TrimSpace(apiBase)
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		base = defaultDeepSeekBase
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("deepseek: api_key is required")
	}
	hc, err := httpClient(proxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &DeepSeekClient{base: base, apiKey: key, client: hc}, nil
}

type deepSeekBalanceResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

// GetBalance returns prepaid balance. DeepSeek does not expose historical token spend via API.
func (c *DeepSeekClient) GetBalance(ctx context.Context) (*DeepSeekBalance, error) {
	var resp deepSeekBalanceResponse
	if err := c.getJSON(ctx, c.base+"/user/balance", &resp); err != nil {
		return nil, err
	}
	out := &DeepSeekBalance{IsAvailable: resp.IsAvailable}
	if len(resp.BalanceInfos) > 0 {
		info := resp.BalanceInfos[0]
		out.Currency = info.Currency
		if out.Currency == "" {
			out.Currency = "CNY"
		}
		out.Total = info.TotalBalance
		out.Granted = info.GrantedBalance
		out.ToppedUp = info.ToppedUpBalance
	}
	if lim, rem, err := c.probeRateLimits(ctx); err == nil {
		out.RateLimitRPM = lim
		out.RateLimitRPMRemaining = rem
	}
	return out, nil
}

func (c *DeepSeekClient) probeRateLimits(ctx context.Context) (*int, *int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/models", nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	httpResp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = httpResp.Body.Close() }()
	_, _ = io.Copy(io.Discard, httpResp.Body)
	if httpResp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("models: %s", httpResp.Status)
	}
	var lim, rem *int
	if v := httpResp.Header.Get("x-ratelimit-limit-requests"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			lim = &n
		}
	}
	if v := httpResp.Header.Get("x-ratelimit-remaining-requests"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rem = &n
		}
	}
	return lim, rem, nil
}

func (c *DeepSeekClient) getJSON(ctx context.Context, urlStr string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s: %s", urlStr, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
