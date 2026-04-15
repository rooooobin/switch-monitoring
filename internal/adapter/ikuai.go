package adapter

import (
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
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

	resp, err := c.client.Post(c.url+"/Action/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed with status: %s", resp.Status)
	}

	var result struct {
		Result int    `json:"result"`
		ErrMsg string `json:"errMsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.Result != 10000 {
		return fmt.Errorf("login failed: %s (code %d)", result.ErrMsg, result.Result)
	}

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

	resp, err := c.client.Post(c.url+"/Action/call", "application/json", bytes.NewReader(b))
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
		return nil, err
	}

	if result.Result != 30000 {
		return nil, fmt.Errorf("fetch %s failed: %s (code %d)", name, result.ErrMsg, result.Result)
	}

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

	resp, err := c.client.Post(c.url+"/Action/call", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		Result int    `json:"result"`
		ErrMsg string `json:"errMsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	if result.Result != 30000 {
		return fmt.Errorf("toggle %s failed: %s (code %d)", c.dnatFuncName, result.ErrMsg, result.Result)
	}

	return nil
}
