package adapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekClient_GetBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user/balance":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"42.50","granted_balance":"10.00","topped_up_balance":"32.50"}]}`))
		case "/v1/models":
			w.Header().Set("x-ratelimit-limit-requests", "60")
			w.Header().Set("x-ratelimit-remaining-requests", "55")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := NewDeepSeekClient(srv.URL, "sk-test", "")
	if err != nil {
		t.Fatal(err)
	}
	bal, err := c.GetBalance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bal.Total != "42.50" || bal.Currency != "CNY" {
		t.Fatalf("balance: %+v", bal)
	}
	if bal.RateLimitRPM == nil || *bal.RateLimitRPM != 60 {
		t.Fatalf("rpm: %+v", bal.RateLimitRPM)
	}
}
