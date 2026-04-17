package adapter

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// IkuaiClient handles communication with the iKuai router API.
type IkuaiClient struct {
	url          string
	username     string
	password     string
	client       *http.Client
	dnatFuncName string
}

// NewIkuaiClient creates a new iKuai API client.
func NewIkuaiClient(url, username, password string) (*IkuaiClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &IkuaiClient{
		url:      strings.TrimSuffix(url, "/"),
		username: username,
		password: password,
		client: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}, nil
}

// postJSON sends a POST with JSON body to c.url+path and logs request/response timing.
func (c *IkuaiClient) postJSON(path, op string, body []byte) (*http.Response, error) {
	urlStr := c.url + path
	start := time.Now()
	slog.Info("iKuai HTTP request", "op", op, "method", http.MethodPost, "url", urlStr, "body_bytes", len(body))
	resp, err := c.client.Post(urlStr, "application/json", bytes.NewReader(body))
	dur := time.Since(start)
	if err != nil {
		slog.Error("iKuai HTTP transport error", "op", op, "url", urlStr, "duration_ms", dur.Milliseconds(), "err", err)
		return nil, err
	}
	slog.Info("iKuai HTTP response", "op", op, "url", urlStr, "status", resp.StatusCode, "duration_ms", dur.Milliseconds())
	return resp, nil
}

// Login authenticates with the iKuai router.
func (c *IkuaiClient) Login() error {
	hash := md5.Sum([]byte(c.password))
	passMD5 := hex.EncodeToString(hash[:])

	payload := map[string]string{
		"username": c.username,
		"passwd":   passMD5,
		"remember": "0",
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := c.postJSON("/Action/login", "login", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("iKuai login HTTP non-OK", "status", resp.StatusCode)
		return fmt.Errorf("login failed with status: %s", resp.Status)
	}

	var result struct {
		Result int    `json:"result"`
		ErrMsg string `json:"errMsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("iKuai login JSON decode failed", "err", err)
		return err
	}

	if result.Result != 10000 {
		slog.Warn("iKuai login rejected by API", "result", result.Result, "err_msg", result.ErrMsg)
		return fmt.Errorf("login failed: %s (code %d)", result.ErrMsg, result.Result)
	}

	slog.Info("iKuai login OK", "result", result.Result)
	return nil
}

// DNATRule represents a single DNAT rule in iKuai.
// Using a map ensures we don't lose fields when sending it back for edits.
type DNATRule map[string]interface{}

func (r DNATRule) ID() int {
	if id, ok := r["id"].(float64); ok {
		return int(id)
	}
	return 0
}

func (r DNATRule) Enabled() string {
	if e, ok := r["enabled"].(string); ok {
		return e
	}
	return "no"
}

func (r DNATRule) Comment() string {
	if c, ok := r["comment"].(string); ok {
		if decoded, err := url.QueryUnescape(c); err == nil {
			return decoded
		}
		return c
	}
	return ""
}

func (r DNATRule) String(key string) string {
	if v, ok := r[key].(string); ok {
		return v
	}
	return ""
}

// GetDNATRules fetches all DNAT rules.
func (c *IkuaiClient) GetDNATRules() ([]DNATRule, error) {
	names := []string{"dnat"}
	if c.dnatFuncName != "" {
		names = []string{c.dnatFuncName}
	}

	var lastErr error
	for _, name := range names {
		rules, err := c.getDNATRulesWithName(name)
		if err == nil {
			c.dnatFuncName = name
			return rules, nil
		}
		lastErr = err
		// If code is 30002 (Not found funcname), try the next name if we had more
		if strings.Contains(err.Error(), "30002") {
			continue
		}
		break
	}
	return nil, lastErr
}

func (c *IkuaiClient) getDNATRulesWithName(name string) ([]DNATRule, error) {
	reqBody := map[string]interface{}{
		"func_name": name,
		"action":    "show",
		"param": map[string]interface{}{
			"TYPE": "data,total",
		},
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	resp, err := c.postJSON("/Action/call", "dnat_show", b)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Result int    `json:"result"`
		ErrMsg string `json:"errMsg"`
		Data   struct {
			Data []DNATRule `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("iKuai DNAT list JSON decode failed", "func_name", name, "err", err)
		return nil, err
	}

	if result.Result != 30000 {
		slog.Warn("iKuai DNAT list API error", "func_name", name, "result", result.Result, "err_msg", result.ErrMsg)
		return nil, fmt.Errorf("fetch %s failed: %s (code %d)", name, result.ErrMsg, result.Result)
	}

	slog.Info("iKuai DNAT rules loaded", "func_name", name, "count", len(result.Data.Data))
	return result.Data.Data, nil
}

// ToggleDNATRule enables or disables a specific DNAT rule.
func (c *IkuaiClient) ToggleDNATRule(id int, enabled bool) error {
	rules, err := c.GetDNATRules()
	if err != nil {
		return err
	}

	var target DNATRule
	for _, r := range rules {
		if r.ID() == id {
			target = r
			break
		}
	}

	if target == nil {
		return fmt.Errorf("rule with ID %d not found", id)
	}

	state := "no"
	if enabled {
		state = "yes"
	}
	target["enabled"] = state

	// iKuai edit requires full rule parameters, which we preserved in the map
	reqBody := map[string]interface{}{
		"func_name": c.dnatFuncName,
		"action":    "edit",
		"param":     target,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	slog.Info("iKuai DNAT toggle request", "rule_id", id, "enabled", state, "func_name", c.dnatFuncName)
	resp, err := c.postJSON("/Action/call", "dnat_edit", b)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Result int    `json:"result"`
		ErrMsg string `json:"errMsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("iKuai DNAT toggle JSON decode failed", "rule_id", id, "err", err)
		return err
	}

	if result.Result != 30000 {
		slog.Warn("iKuai DNAT toggle API error", "rule_id", id, "result", result.Result, "err_msg", result.ErrMsg, "func_name", c.dnatFuncName)
		return fmt.Errorf("toggle %s failed: %s (code %d)", c.dnatFuncName, result.ErrMsg, result.Result)
	}

	slog.Info("iKuai DNAT toggle OK", "rule_id", id, "enabled", state, "func_name", c.dnatFuncName)
	return nil
}
