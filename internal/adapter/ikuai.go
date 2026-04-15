package adapter

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
)

// IkuaiClient handles communication with the iKuai router API.
type IkuaiClient struct {
	url      string
	username string
	password string
	client   *http.Client
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
		client:   &http.Client{Jar: jar},
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

// SNATRule represents a single SNAT rule in iKuai.
type SNATRule struct {
	ID      int    `json:"id"`
	Enabled string `json:"enabled"` // "yes" or "no"
	Comment string `json:"comment"`
	SrcAddr string `json:"src_addr"`
	OutFace string `json:"out_face"`
}

// GetSNATRules fetches all SNAT rules.
func (c *IkuaiClient) GetSNATRules() ([]SNATRule, error) {
	reqBody := map[string]interface{}{
		"func_name": "snat",
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
			Data []SNATRule `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Result != 30000 {
		return nil, fmt.Errorf("fetch snat failed: %s (code %d)", result.ErrMsg, result.Result)
	}

	return result.Data.Data, nil
}

// ToggleSNATRule enables or disables a specific SNAT rule.
func (c *IkuaiClient) ToggleSNATRule(id int, enabled bool) error {
	rules, err := c.GetSNATRules()
	if err != nil {
		return err
	}

	var target *SNATRule
	for i := range rules {
		if rules[i].ID == id {
			target = &rules[i]
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

	// iKuai edit requires full rule parameters
	reqBody := map[string]interface{}{
		"func_name": "snat",
		"action":    "edit",
		"param": map[string]interface{}{
			"id":       target.ID,
			"enabled":  state,
			"comment":  target.Comment,
			"src_addr": target.SrcAddr,
			"out_face": target.OutFace,
		},
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
		return fmt.Errorf("toggle snat failed: %s (code %d)", result.ErrMsg, result.Result)
	}

	return nil
}
