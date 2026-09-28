package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if m, ok := q.Models["qwen-max"]; !ok || m.Used == nil || *m.Used != 100 {
		t.Fatalf("models: %+v", q.Models)
	}
}
