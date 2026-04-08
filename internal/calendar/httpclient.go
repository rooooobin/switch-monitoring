package calendar

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient returns an *http.Client that uses the given HTTP(S) proxy when proxyURL
// is non-empty (same idea as Telegram: replace localhost with 127.0.0.1).
func HTTPClient(proxyURL string) (*http.Client, error) {
	if strings.TrimSpace(proxyURL) == "" {
		return &http.Client{Timeout: 60 * time.Second}, nil
	}
	proxyStr := strings.ReplaceAll(proxyURL, "localhost", "127.0.0.1")
	u, err := url.Parse(proxyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	t := &http.Transport{Proxy: http.ProxyURL(u)}
	return &http.Client{Transport: t, Timeout: 60 * time.Second}, nil
}
