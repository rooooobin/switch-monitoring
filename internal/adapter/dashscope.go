package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultDashScopeBase = "https://dashscope.aliyuncs.com/api/v1"

// DashScopeQuotas is the parsed GET /quotas response (阿里云百炼 / DashScope).
// CN accounts typically return paginated per-model rate/usage *limits* under output.quotas
// (not historical spend). Some regions/products may also return billing fields under data.
type DashScopeQuotas struct {
	// Kind is "model_limits" and/or "billing" depending on what the API returned.
	Kind string

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

	TotalModels int
	PageNo      int
	PageSize    int
	Models      []DashScopeModelQuota

	RawKeys    string
	RawSnippet string
}

// DashScopeModelQuota is one model's limit (and optional used/limit when present).
type DashScopeModelQuota struct {
	Name            string
	WorkspaceID     string
	Used            *float64
	Limit           *float64
	RPM             *int
	TPM             *int
	RequestLimit    *int
	RequestPeriodS  *int
	UsageLimit      *float64
	UsageLimitField string
	UsagePeriodS    *int
}

// HasAnyMetric reports whether any usage/balance/limit field was parsed.
func (q *DashScopeQuotas) HasAnyMetric() bool {
	if q == nil {
		return false
	}
	return q.Available != nil || q.Credits != nil || q.SpendLimit != nil ||
		q.DailySpend != nil || q.MonthlySpend != nil ||
		q.TokensUsed != nil || q.RequestsUsed != nil ||
		q.RPM != nil || q.TPM != nil ||
		q.BillingStart != "" || q.BillingEnd != "" ||
		len(q.Models) > 0
}

// HasBilling reports whether spend/balance style fields were present.
func (q *DashScopeQuotas) HasBilling() bool {
	if q == nil {
		return false
	}
	return q.Available != nil || q.Credits != nil || q.SpendLimit != nil ||
		q.DailySpend != nil || q.MonthlySpend != nil ||
		q.TokensUsed != nil || q.RequestsUsed != nil
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
	key = strings.TrimPrefix(key, "Bearer ")
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("bailian/dashscope: api_key is required")
	}
	hc, err := httpClient(proxy, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &DashScopeClient{base: base, apiKey: key, client: hc}, nil
}

// GetQuotas returns quotas from DashScope (model limits and/or billing aggregates when available).
func (c *DashScopeClient) GetQuotas(ctx context.Context) (*DashScopeQuotas, error) {
	u, err := url.Parse(c.base + "/quotas")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("page_no", "1")
	q.Set("page_size", "50")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
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
	return parseDashScopeQuotasBody(body)
}

func parseDashScopeQuotasBody(body []byte) (*DashScopeQuotas, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode quotas: %w", err)
	}

	if code := jsonString(root["code"]); code != "" && !isDashScopeOKCode(code) {
		msg := jsonString(root["message"])
		if msg == "" {
			msg = "(no message)"
		}
		return nil, fmt.Errorf("dashscope API %s: %s", code, msg)
	}

	payload, payloadKey := pickPayload(root)
	out := &DashScopeQuotas{
		RawKeys: strings.Join(sortedRawKeys(root), ","),
	}
	if payloadKey != "" {
		out.RawKeys += " | payload=" + payloadKey + ":" + strings.Join(sortedRawKeys(payload), ",")
	}

	fillQuotasFromMap(out, payload)

	if out.HasBilling() && len(out.Models) > 0 {
		out.Kind = "billing+model_limits"
	} else if out.HasBilling() {
		out.Kind = "billing"
	} else if len(out.Models) > 0 {
		out.Kind = "model_limits"
	}

	if !out.HasAnyMetric() {
		out.RawSnippet = truncateForLog(body, 400)
	}
	return out, nil
}

func pickPayload(root map[string]json.RawMessage) (map[string]json.RawMessage, string) {
	for _, key := range []string{"data", "output", "result"} {
		if raw, ok := root[key]; ok && len(raw) > 0 && string(raw) != "null" {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err == nil {
				return m, key
			}
		}
	}
	return root, "root"
}

func fillQuotasFromMap(out *DashScopeQuotas, m map[string]json.RawMessage) {
	out.Available = jsonFloat(m, "available", "available_balance", "balance")
	out.Credits = jsonFloat(m, "credits", "credit", "credit_balance")
	out.SpendLimit = jsonFloat(m, "spend_limit", "spendLimit")
	out.DailySpend = jsonFloat(m, "daily_spend", "dailySpend", "day_spend")
	out.MonthlySpend = jsonFloat(m, "monthly_spend", "monthlySpend", "month_spend")
	out.TokensUsed = jsonFloat(m, "tokens_used", "tokensUsed", "token_used", "usage", "total_tokens")
	out.RequestsUsed = jsonFloat(m, "requests_used", "requestsUsed", "request_used", "total_requests")

	if rl := nestedMap(m, "rate_limit", "rateLimit"); rl != nil {
		out.RPM = jsonInt(rl, "rpm", "RPM")
		out.TPM = jsonInt(rl, "tpm", "TPM")
	} else {
		out.RPM = jsonInt(m, "rpm", "RPM")
		out.TPM = jsonInt(m, "tpm", "TPM")
	}

	if bp := nestedMap(m, "billing_period", "billingPeriod", "billing_cycle"); bp != nil {
		out.BillingStart = firstNonEmpty(jsonString(bp["start"]), jsonString(bp["begin"]))
		out.BillingEnd = firstNonEmpty(jsonString(bp["end"]), jsonString(bp["finish"]))
	}

	if v := jsonInt(m, "total"); v != nil {
		out.TotalModels = *v
	}
	if v := jsonInt(m, "page_no", "pageNo"); v != nil {
		out.PageNo = *v
	}
	if v := jsonInt(m, "page_size", "pageSize"); v != nil {
		out.PageSize = *v
	}

	if raw, ok := m["models"]; ok {
		out.Models = append(out.Models, parseModelsList(raw)...)
	}
	if raw, ok := m["quotas"]; ok {
		out.Models = append(out.Models, parseModelsList(raw)...)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Name < out.Models[j].Name })
}

func parseModelsList(raw json.RawMessage) []DashScopeModelQuota {
	var out []DashScopeModelQuota

	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err == nil {
		for name, item := range asMap {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(item, &fields); err != nil {
				continue
			}
			q := modelQuotaFromMap(name, fields)
			out = append(out, q)
		}
		return out
	}

	var asArr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asArr); err == nil {
		for _, fields := range asArr {
			name := firstNonEmpty(jsonString(fields["model"]), jsonString(fields["name"]), jsonString(fields["model_name"]))
			if name == "" {
				continue
			}
			out = append(out, modelQuotaFromMap(name, fields))
		}
	}
	return out
}

func modelQuotaFromMap(name string, fields map[string]json.RawMessage) DashScopeModelQuota {
	q := DashScopeModelQuota{
		Name:        name,
		WorkspaceID: jsonString(fields["workspace_id"]),
		Used:        jsonFloat(fields, "used", "usage"),
		Limit:       jsonFloat(fields, "limit"),
		RPM:         jsonInt(fields, "rpm"),
		TPM:         jsonInt(fields, "tpm"),
	}
	if nested := nestedMap(fields, "model_limit", "workspace_limit"); nested != nil {
		q.RequestLimit = jsonInt(nested, "request_limit")
		q.RequestPeriodS = jsonInt(nested, "request_limit_period")
		q.UsageLimit = jsonFloat(nested, "usage_limit")
		q.UsageLimitField = jsonString(nested["usage_limit_field"])
		q.UsagePeriodS = jsonInt(nested, "usage_limit_period")
		if q.Limit == nil {
			q.Limit = q.UsageLimit
		}
	}
	return q
}

func isDashScopeOKCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "success", "ok", "200":
		return true
	default:
		return false
	}
}

func nestedMap(m map[string]json.RawMessage, keys ...string) map[string]json.RawMessage {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err == nil {
			return out
		}
	}
	return nil
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func jsonFloat(m map[string]json.RawMessage, keys ...string) *float64 {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var f float64
		if err := json.Unmarshal(raw, &f); err == nil {
			return &f
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return &v
			}
		}
	}
	return nil
}

func jsonInt(m map[string]json.RawMessage, keys ...string) *int {
	f := jsonFloat(m, keys...)
	if f == nil {
		return nil
	}
	v := int(*f)
	return &v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
