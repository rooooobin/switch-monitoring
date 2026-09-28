package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDashScopeClient_GetQuotas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/quotas" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-ds" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code":"Success",
			"data":{
				"available":12.5,
				"daily_spend":0.42,
				"monthly_spend":3.14,
				"tokens_used":1500000,
				"requests_used":820,
				"billing_period":{"start":"2026-09-01","end":"2026-09-30"},
				"models":{"qwen-max":{"used":100,"limit":1000}}
			}
		}`))
	}))
	defer srv.Close()

	c, err := NewDashScopeClient(srv.URL, "sk-ds", "")
	if err != nil {
		t.Fatal(err)
	}
	q, err := c.GetQuotas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if q.MonthlySpend == nil || *q.MonthlySpend != 3.14 {
		t.Fatalf("monthly: %+v", q.MonthlySpend)
	}
	if q.TokensUsed == nil || *q.TokensUsed != 1500000 {
		t.Fatalf("tokens: %+v", q.TokensUsed)
	}
	if len(q.Models) != 1 || q.Models[0].Name != "qwen-max" || q.Models[0].Used == nil || *q.Models[0].Used != 100 {
		t.Fatalf("models: %+v", q.Models)
	}
}

func TestParseDashScopeQuotas_CNModelLimits(t *testing.T) {
	body, err := os.ReadFile("/tmp/dashscope_quotas_r2s.json")
	if err != nil {
		t.Skip("no R2S sample:", err)
	}
	q, err := parseDashScopeQuotasBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if q.Kind != "model_limits" {
		t.Fatalf("kind=%q", q.Kind)
	}
	if q.HasBilling() {
		t.Fatal("expected no billing fields")
	}
	if q.TotalModels != 518 {
		t.Fatalf("total=%d", q.TotalModels)
	}
	if len(q.Models) == 0 {
		t.Fatal("no models")
	}
	found := false
	for _, m := range q.Models {
		if m.Name == "decision-model-preview" {
			found = true
			if m.RequestLimit == nil || *m.RequestLimit != 120 {
				t.Fatalf("request_limit: %+v", m.RequestLimit)
			}
			if m.UsageLimit == nil || *m.UsageLimit != 200000 {
				t.Fatalf("usage_limit: %+v", m.UsageLimit)
			}
			if m.UsagePeriodS == nil || *m.UsagePeriodS != 6 {
				t.Fatalf("usage_period: %+v", m.UsagePeriodS)
			}
		}
	}
	if !found {
		t.Fatal("decision-model-preview not found")
	}
}

func TestParseDashScopeQuotas_OutputWrapperAndStrings(t *testing.T) {
	body := []byte(`{
		"code": null,
		"message": null,
		"success": true,
		"output": {
			"available": "12.5",
			"tokens_used": "99",
			"models": [
				{"model":"qwen-plus","used":10,"limit":100}
			]
		}
	}`)
	q, err := parseDashScopeQuotasBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if q.Available == nil || *q.Available != 12.5 {
		t.Fatalf("available: %+v", q.Available)
	}
	if q.TokensUsed == nil || *q.TokensUsed != 99 {
		t.Fatalf("tokens: %+v", q.TokensUsed)
	}
	if len(q.Models) != 1 || q.Models[0].Used == nil || *q.Models[0].Used != 10 {
		t.Fatalf("models: %+v", q.Models)
	}
}

func TestParseDashScopeQuotas_EmptyKeepsSnippet(t *testing.T) {
	body := []byte(`{"code":"Success","data":{"foo":1}}`)
	q, err := parseDashScopeQuotasBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if q.HasAnyMetric() {
		t.Fatalf("expected empty metrics, got %+v", q)
	}
	if q.RawSnippet == "" || q.RawKeys == "" {
		t.Fatalf("expected diagnostic keys/snippet, got keys=%q snippet=%q", q.RawKeys, q.RawSnippet)
	}
}
