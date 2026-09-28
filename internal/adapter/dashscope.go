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

const defaultDashScopeBase = "https://dashscope.aliyuncs.com/api/v1"

// DashScopeQuotas is billing and usage from GET /quotas (阿里云百炼 / DashScope).
type DashScopeQuotas struct {
	Available    *float64
	Credits      *float64
	SpendLimit   *float64
	DailySpend   *float64
	MonthlySpend *float64
	TokensUsed   *float64
	RequestsUsed *float64
	RPM          *int
	TPM          *int
	BillingStart string
	BillingEnd   string
	Models       map[string]DashScopeModelQuota
}

// DashScopeModelQuota is per-model quota usage when returned by the API.
type DashScopeModelQuota struct {
	Used  *float64
	Limit *float64
	RPM   *int
	TPM   *int
}

// DashScopeClient queries DashScope quota/billing APIs.
type DashScopeClient struct {
	base   string
	apiKey string
	client *http.Client
}

// NewDashScopeClient creates a client for 百炼 (DashScope). apiBase may be empty for the China default.
func NewDashScopeClient(apiBase, apiKey, proxy string) (*DashScopeClient, error) {
	base := strings.TrimSpace(apiBase)
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		base = defaultDashScopeBase
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("bailian/dashscope: api_key is required")
	}
	hc, err := httpClient(proxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &DashScopeClient{base: base, apiKey: key, client: hc}, nil
}

type dashScopeQuotasResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Available    *float64                       `json:"available"`
		Credits      *float64                       `json:"credits"`
		SpendLimit   *float64                       `json:"spend_limit"`
		DailySpend   *float64                       `json:"daily_spend"`
		MonthlySpend *float64                       `json:"monthly_spend"`
		TokensUsed   *float64                       `json:"tokens_used"`
		RequestsUsed *float64                       `json:"requests_used"`
		RateLimit    *struct {
			RPM *int `json:"rpm"`
			TPM *int `json:"tpm"`
		} `json:"rate_limit"`
		Models        map[string]dashScopeModelQuotaJSON `json:"models"`
		BillingPeriod *struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"billing_period"`
	} `json:"data"`
}

type dashScopeModelQuotaJSON struct {
	RPM   *int     `json:"rpm"`
	TPM   *int     `json:"tpm"`
	Used  *float64 `json:"used"`
	Limit *float64 `json:"limit"`
}

// GetQuotas returns account spend and token usage aggregates from DashScope.
func (c *DashScopeClient) GetQuotas(ctx context.Context) (*DashScopeQuotas, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/quotas", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
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
		return nil, fmt.Errorf("GET /quotas: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed dashScopeQuotasResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode quotas: %w", err)
	}
	if parsed.Code != "" && parsed.Code != "Success" {
		return nil, fmt.Errorf("dashscope API %s: %s", parsed.Code, parsed.Message)
	}

	out := &DashScopeQuotas{
		Available:    parsed.Data.Available,
		Credits:      parsed.Data.Credits,
		SpendLimit:   parsed.Data.SpendLimit,
		DailySpend:   parsed.Data.DailySpend,
		MonthlySpend: parsed.Data.MonthlySpend,
		TokensUsed:   parsed.Data.TokensUsed,
		RequestsUsed: parsed.Data.RequestsUsed,
		Models:       make(map[string]DashScopeModelQuota),
	}
	if parsed.Data.RateLimit != nil {
		out.RPM = parsed.Data.RateLimit.RPM
		out.TPM = parsed.Data.RateLimit.TPM
	}
	if parsed.Data.BillingPeriod != nil {
		out.BillingStart = parsed.Data.BillingPeriod.Start
		out.BillingEnd = parsed.Data.BillingPeriod.End
	}
	for name, m := range parsed.Data.Models {
		out.Models[name] = DashScopeModelQuota{
			Used:  m.Used,
			Limit: m.Limit,
			RPM:   m.RPM,
			TPM:   m.TPM,
		}
	}
	return out, nil
}
