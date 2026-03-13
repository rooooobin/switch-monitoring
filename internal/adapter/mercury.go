// Package adapter provides switch-specific HTTP scrapers.
package adapter

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"switch-monitor/internal/model"
)

// MercuryAdapter communicates with a Mercury SG108 Pro switch.
type MercuryAdapter struct {
	host     string
	username string
	password string
	client   *http.Client
	cookie   *http.Cookie
}

// NewMercuryAdapter creates a new adapter.
func NewMercuryAdapter(host, username, password string) *MercuryAdapter {
	return &MercuryAdapter{
		host:     host,
		username: username,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (a *MercuryAdapter) baseURL() string {
	return "http://" + a.host
}

// login performs the Mercury logon and stores any session cookie.
// Mercury switches often allow unauthenticated access; if login fails we still
// check whether the info page is reachable.
func (a *MercuryAdapter) login() error {
	form := url.Values{
		"username": {a.username},
		"password": {a.password},
		"logon":    {"登录"},
	}
	resp, err := a.client.PostForm(a.baseURL()+"/logon.cgi", form)
	if err != nil {
		// Network error — attempt unauthenticated access
		return a.checkUnauthenticated()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Store any session cookie
	for _, c := range resp.Cookies() {
		a.cookie = c
		break
	}

	// Parse logonInfo[0]: 0 = success
	if errType := parseLogonInfo(string(body)); errType == 0 {
		return nil
	}
	// Non-zero or not found — try unauthenticated
	return a.checkUnauthenticated()
}

// checkUnauthenticated verifies the switch is reachable without a cookie.
func (a *MercuryAdapter) checkUnauthenticated() error {
	resp, err := a.client.Get(a.baseURL() + "/SystemInfoRpm.htm")
	if err != nil {
		return fmt.Errorf("mercury %s: unreachable: %w", a.host, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && strings.Contains(string(body), "info_ds") {
		return nil // pages accessible without auth
	}
	return fmt.Errorf("mercury %s: login failed and pages inaccessible", a.host)
}

func (a *MercuryAdapter) ensureConnected() error {
	if a.cookie != nil {
		return nil
	}
	return a.login()
}

func (a *MercuryAdapter) get(path string) (string, error) {
	req, err := http.NewRequest("GET", a.baseURL()+path, nil)
	if err != nil {
		return "", err
	}
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

// GetPortStatuses returns port data for all 8 ports.
func (a *MercuryAdapter) GetPortStatuses() ([]model.PortStatus, error) {
	if err := a.ensureConnected(); err != nil {
		return nil, err
	}

	// Port status and speed come from PortSettingRpm.htm (all_info.state + spd_act)
	settingBody, err := a.get("/PortSettingRpm.htm")
	if err != nil {
		return nil, fmt.Errorf("mercury PortSettingRpm.htm: %w", err)
	}
	state, spdAct := parseMercuryPortSetting(settingBody)

	// Packet counts come from PortStatisticsRpm.htm (all_info.pkts + link_status)
	statsBody, err := a.get("/PortStatisticsRpm.htm")
	if err != nil {
		return nil, fmt.Errorf("mercury PortStatisticsRpm.htm: %w", err)
	}
	linkStatus, pkts := parseMercuryPortStatistics(statsBody)

	const ports = 8
	result := make([]model.PortStatus, 0, ports)
	for i := 0; i < ports; i++ {
		portNum := i + 1

		// Link up: use link_status from statistics page when available
		linkUp := false
		if i < len(linkStatus) {
			linkUp = linkStatus[i] > 0
		} else if i < len(state) {
			linkUp = state[i] == 1
		}

		var speedPtr *int
		if linkUp && i < len(spdAct) {
			if spd := mercurySpeed(spdAct[i]); spd > 0 {
				v := spd
				speedPtr = &v
			}
		}

		ps := model.PortStatus{
			PortID:    portNum,
			LinkUp:    linkUp,
			SpeedMbps: speedPtr,
		}

		// 4 packet values per port: [tx_good, tx_bad, rx_good, rx_bad]
		base := i * 4
		if base+3 < len(pkts) {
			txOk := int64(pkts[base])
			txFail := int64(pkts[base+1])
			rxOk := int64(pkts[base+2])
			rxFail := int64(pkts[base+3])
			ps.TxOk = &txOk
			ps.TxFail = &txFail
			ps.RxOk = &rxOk
			ps.RxFail = &rxFail
		}

		result = append(result, ps)
	}
	return result, nil
}

// Logout terminates the current session on the switch.
func (a *MercuryAdapter) Logout() error {
	if a.cookie == nil {
		return nil
	}
	
	_, err := a.get("/LogoutRpm.htm")
	a.cookie = nil
	return err
}

// ─── HTML/JS parsers ────────────────────────────────────────────────────────

// parseLogonInfo extracts errType from:  var logonInfo = new Array(1, 0, 0);
func parseLogonInfo(body string) int {
	re := regexp.MustCompile(`var\s+logonInfo\s*=\s*new\s+Array\s*\(\s*(\d+)`)
	if m := re.FindStringSubmatch(body); len(m) > 1 {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return -1
}

// parseMercuryPortSetting returns (state[], spd_act[]) arrays from PortSettingRpm.htm.
// The page embeds a JS object: var all_info = { state: [...], spd_act: [...] };
func parseMercuryPortSetting(body string) (state []int, spdAct []int) {
	allInfo := parseJSObject(body, "all_info")
	state = parseIntArray(allInfo["state"])
	spdAct = parseIntArray(allInfo["spd_act"])
	return
}

// parseMercuryPortStatistics returns (link_status[], pkts[]) from PortStatisticsRpm.htm.
func parseMercuryPortStatistics(body string) (linkStatus []int, pkts []int) {
	allInfo := parseJSObject(body, "all_info")
	linkStatus = parseIntArray(allInfo["link_status"])
	pkts = parseIntArray(allInfo["pkts"])
	return
}

// speedMapping mirrors py-mercury-switch-api's LINK_STATUS_TO_SPEED.
var mercuryLinkToSpeed = map[int]int{
	1: 10, 2: 100, 3: 1000,
	4: 10, 5: 100, 6: 1000, // half-duplex variants
}

// mercurySpeed converts a spd_act value to Mbps.  Also handles link_status values.
func mercurySpeed(v int) int {
	if spd, ok := mercuryLinkToSpeed[v]; ok {
		return spd
	}
	return 0
}

// ─── JS Object parser ───────────────────────────────────────────────────────

// parseJSObject parses  var <name> = { key: value, key2: [v1, v2, ...] };
// Returns a map of key → raw string value (either scalar or "[...]").
func parseJSObject(body, name string) map[string]string {
	result := make(map[string]string)

	// Find the object literal
	pat := regexp.MustCompile(`(?s)var\s+` + regexp.QuoteMeta(name) + `\s*=\s*\{([^}]*)\}`)
	m := pat.FindStringSubmatch(body)
	if len(m) < 2 {
		// Try multi-line with nested arrays
		pat2 := regexp.MustCompile(`(?s)var\s+` + regexp.QuoteMeta(name) + `\s*=\s*\{(.+?)\};`)
		m = pat2.FindStringSubmatch(body)
		if len(m) < 2 {
			return result
		}
	}
	content := m[1]

	// Match key: value  or  key: [...]
	kvRe := regexp.MustCompile(`(?s)(\w+)\s*:\s*(\[[^\]]*\]|[^,}]+)`)
	for _, kv := range kvRe.FindAllStringSubmatch(content, -1) {
		key := kv[1]
		val := strings.TrimSpace(kv[2])
		val = strings.TrimRight(val, ",;")
		result[key] = val
	}
	return result
}

// parseIntArray parses a JS array string like "[1, 0, 2, 3]" into []int.
// Also handles hex values like 0x1A.
func parseIntArray(s string) []int {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return nil
	}
	s = strings.Trim(s, "[]")
	parts := strings.Split(s, ",")
	var out []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		var v int64
		var err error
		if strings.HasPrefix(p, "0x") || strings.HasPrefix(p, "0X") {
			v, err = strconv.ParseInt(p[2:], 16, 64)
		} else {
			v, err = strconv.ParseInt(p, 10, 64)
		}
		if err != nil {
			v = 0
		}
		out = append(out, int(v))
	}
	return out
}

