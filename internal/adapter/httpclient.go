package adapter

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func httpClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if strings.TrimSpace(proxyURL) == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	proxyStr := strings.ReplaceAll(proxyURL, "localhost", "127.0.0.1")
	u, err := url.Parse(proxyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   timeout,
	}, nil
}
