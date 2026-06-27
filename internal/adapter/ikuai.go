package adapter

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

type ikuaiEnvelope struct {
	Result  int             `json:"result"`
	Code    int             `json:"code"`
	ErrMsg  string          `json:"errMsg"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Results json.RawMessage `json:"results"`
}

func decodeIkuaiEnvelope(body io.Reader) (ikuaiEnvelope, map[string]json.RawMessage, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return ikuaiEnvelope{}, nil, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ikuaiEnvelope{}, nil, err
	}

	var env ikuaiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ikuaiEnvelope{}, nil, err
	}

	// v4 uses capitalized Result while v3 often uses lowercase result.
	if env.Result == 0 {
		var alt struct {
			Result int `json:"Result"`
		}
		if err := json.Unmarshal(raw, &alt); err == nil && alt.Result != 0 {
			env.Result = alt.Result
		}
	}
	if env.ErrMsg == "" {
		var alt struct {
			ErrMsg string `json:"ErrMsg"`
		}
		if err := json.Unmarshal(raw, &alt); err == nil {
			env.ErrMsg = alt.ErrMsg
		}
	}
	return env, fields, nil
}

func hasField(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

func loginSuccess(env ikuaiEnvelope, fields map[string]json.RawMessage) bool {
	if env.Result == 10000 {
		return true
	}
	return hasField(fields, "code") && env.Code == 0
}

func loginErrorMessage(env ikuaiEnvelope) string {
	if env.Message != "" {
		return env.Message
	}
	return env.ErrMsg
}

func callSuccess(env ikuaiEnvelope, fields map[string]json.RawMessage) bool {
	if env.Result == 30000 {
		return true
	}
	return hasField(fields, "code") && env.Code == 0
}

func callErrorMessage(env ikuaiEnvelope) string {
	if env.Message != "" {
		return env.Message
	}
	return env.ErrMsg
}

func callFuncNotFound(env ikuaiEnvelope, fields map[string]json.RawMessage) bool {
	if env.Result == 30002 {
		return true
	}
	if !hasField(fields, "code") || env.Code == 0 {
		return false
	}
	msg := strings.ToLower(callErrorMessage(env))
	return strings.Contains(msg, "not found")
}

func decodeCallDataList(raw json.RawMessage) ([]DNATRule, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var direct []DNATRule
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}

	var wrapped struct {
		Data []DNATRule `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Data, nil
}

func callDataList(env ikuaiEnvelope) ([]DNATRule, error) {
	if len(env.Results) > 0 {
		return decodeCallDataList(env.Results)
	}
	return decodeCallDataList(env.Data)
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

	env, fields, err := decodeIkuaiEnvelope(resp.Body)
	if err != nil {
		slog.Error("iKuai login JSON decode failed", "err", err)
		return err
	}

	if !loginSuccess(env, fields) {
		slog.Warn("iKuai login rejected by API", "result", env.Result, "code", env.Code, "err_msg", loginErrorMessage(env))
		return fmt.Errorf("login failed: %s (result %d code %d)", loginErrorMessage(env), env.Result, env.Code)
	}

	slog.Info("iKuai login OK", "result", env.Result, "code", env.Code)
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
		if callFuncNotFoundFromError(err) {
			continue
		}
		break
	}
	return nil, lastErr
}

func callFuncNotFoundFromError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "30002") || strings.Contains(strings.ToLower(msg), "not found")
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

	env, fields, err := decodeIkuaiEnvelope(resp.Body)
	if err != nil {
		slog.Error("iKuai DNAT list JSON decode failed", "func_name", name, "err", err)
		return nil, err
	}

	if !callSuccess(env, fields) {
		slog.Warn("iKuai DNAT list API error", "func_name", name, "result", env.Result, "code", env.Code, "err_msg", callErrorMessage(env))
		return nil, fmt.Errorf("fetch %s failed: %s (code %d)", name, callErrorMessage(env), callErrorCode(env))
	}

	rules, err := callDataList(env)
	if err != nil {
		slog.Error("iKuai DNAT list data decode failed", "func_name", name, "err", err)
		return nil, err
	}

	slog.Info("iKuai DNAT rules loaded", "func_name", name, "count", len(rules))
	return rules, nil
}

func callErrorCode(env ikuaiEnvelope) int {
	if env.Code != 0 {
		return env.Code
	}
	return env.Result
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

	env, fields, err := decodeIkuaiEnvelope(resp.Body)
	if err != nil {
		slog.Error("iKuai DNAT toggle JSON decode failed", "rule_id", id, "err", err)
		return err
	}

	if !callSuccess(env, fields) {
		slog.Warn("iKuai DNAT toggle API error", "rule_id", id, "result", env.Result, "code", env.Code, "err_msg", callErrorMessage(env), "func_name", c.dnatFuncName)
		return fmt.Errorf("toggle %s failed: %s (code %d)", c.dnatFuncName, callErrorMessage(env), callErrorCode(env))
	}

	slog.Info("iKuai DNAT toggle OK", "rule_id", id, "enabled", state, "func_name", c.dnatFuncName)
	return nil
}
