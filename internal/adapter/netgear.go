package adapter

import (
	"crypto/md5" //nolint:gosec
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"switch-monitor/internal/model"

	"golang.org/x/net/html"
)

// NetgearAdapter communicates with a Netgear GS108Ev3 switch.
type NetgearAdapter struct {
	host      string
	password  string
	client    *http.Client
	rawCookie string // raw "Name=Value" stored verbatim to avoid Go's cookie sanitisation
	hashID    string // client hash from switch_info.htm
}

// NewNetgearAdapter creates a new adapter.
func NewNetgearAdapter(host, password string) *NetgearAdapter {
	return &NetgearAdapter{
		host:     host,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// No cookie jar — we manage cookies manually to preserve
			// non-standard characters that Netgear firmware generates.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				// Follow redirects but don't carry cookies automatically.
				return nil
			},
		},
	}
}

func (a *NetgearAdapter) baseURL() string {
	return "http://" + a.host
}

// login fetches login.cgi, computes the hashed password, POSTs it, and
// captures the raw Set-Cookie header verbatim.
func (a *NetgearAdapter) login() error {
	loginURL := a.baseURL() + "/login.cgi"

	// Step 1: GET login page to extract rand hidden input
	resp, err := a.client.Get(loginURL)
	if err != nil {
		return fmt.Errorf("netgear %s: GET login.cgi: %w", a.host, err)
	}
	body, err := func() ([]byte, error) {
		defer func() { _ = resp.Body.Close() }()
		return io.ReadAll(resp.Body)
	}()
	if err != nil {
		return err
	}

	// Compute password: if page has <input id="rand"> → merge+md5, else plain
	loginPassword := netgearLoginPassword(string(body), a.password)
	slog.Debug("netgear login", "host", a.host, "password_len", len(loginPassword), "is_hashed", len(loginPassword) == 32)

	// Step 2: POST credentials — use a raw request so we can intercept
	// Set-Cookie before Go's http package sanitises it.
	form := url.Values{"password": {loginPassword}}
	req, err := http.NewRequest("POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	postResp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("netgear %s: POST login.cgi: %w", a.host, err)
	}
	defer func() { _ = postResp.Body.Close() }()

	slog.Debug("netgear login response", "status", postResp.StatusCode)
	for k, v := range postResp.Header {
		slog.Debug("netgear login header", "key", k, "value", v)
	}

	// Extract cookie from raw Set-Cookie header — Go's Cookies() strips
	// non-RFC characters, so we parse the header ourselves.
	if raw := extractRawCookie(postResp.Header["Set-Cookie"]); raw != "" {
		a.rawCookie = raw
		slog.Debug("netgear captured raw cookie", "cookie", a.rawCookie)
		return nil
	}

	bodyBytes, _ := io.ReadAll(postResp.Body)
	slog.Debug("netgear login failed body", "body", string(bodyBytes))
	return fmt.Errorf("netgear %s: login did not return a session cookie", a.host)
}

// extractRawCookie scans Set-Cookie header lines for GS108SID or SID and
// returns the raw "Name=Value" string, preserving all characters.
func extractRawCookie(setCookieHeaders []string) string {
	for _, line := range setCookieHeaders {
		// line looks like: "GS108SID=abc\def; path=/;HttpOnly"
		// Take only the first "Name=Value" segment before the first ";"
		segment := strings.SplitN(line, ";", 2)[0]
		eqIdx := strings.IndexByte(segment, '=')
		if eqIdx < 0 {
			continue
		}
		name := strings.TrimSpace(segment[:eqIdx])
		if name == "SID" || name == "GS108SID" {
			return strings.TrimSpace(segment) // "GS108SID=raw...value"
		}
	}
	return ""
}

func (a *NetgearAdapter) ensureConnected() error {
	if a.rawCookie != "" {
		return nil
	}
	return a.login()
}

// addCookieHeader attaches the raw session cookie directly to the request
// header, bypassing Go's sanitisation entirely.
func (a *NetgearAdapter) addCookieHeader(req *http.Request) {
	if a.rawCookie != "" {
		req.Header.Set("Cookie", a.rawCookie)
	}
}

func (a *NetgearAdapter) post(path string, vals url.Values) (string, error) {
	var body io.Reader
	if vals != nil {
		body = strings.NewReader(vals.Encode())
	}
	req, err := http.NewRequest("POST", a.baseURL()+path, body)
	if err != nil {
		return "", err
	}
	if vals != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	a.addCookieHeader(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func (a *NetgearAdapter) get(path string) (string, error) {
	req, err := http.NewRequest("GET", a.baseURL()+path, nil)
	if err != nil {
		return "", err
	}
	a.addCookieHeader(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// Logout terminates the current session on the switch.
func (a *NetgearAdapter) Logout() error {
	if a.rawCookie == "" {
		return nil
	}

	logoutURL := "/logout.cgi"
	if a.hashID != "" {
		logoutURL += "?id=" + a.hashID
	}

	slog.Debug("netgear logout request", "host", a.host)
	_, err := a.get(logoutURL)

	// Always clear session state locally
	a.rawCookie = ""
	a.hashID = ""

	return err
}

// GetPortStatuses returns status for all 8 ports.
func (a *NetgearAdapter) GetPortStatuses() ([]model.PortStatus, error) {
	if err := a.ensureConnected(); err != nil {
		return nil, err
	}

	// Load switch_info.htm once to get client hash
	if a.hashID == "" {
		infoBody, err := a.get("/switch_info.htm")
		if err == nil {
			a.hashID = extractInputValue(infoBody, "hash")
			slog.Debug("netgear extracted hash", "hash", a.hashID)
		} else {
			slog.Debug("netgear failed to get switch_info.htm", "error", err)
		}
	}

	statsVals := url.Values{}
	if a.hashID != "" {
		statsVals.Set("hash", a.hashID)
	}

	slog.Debug("netgear portStatistics.cgi request", "hash", a.hashID)
	statsBody, err := a.post("/portStatistics.cgi", statsVals)
	if err != nil {
		return nil, fmt.Errorf("netgear portStatistics.cgi: %w", err)
	}
	slog.Debug("netgear portStatistics.cgi response", "body_len", len(statsBody), "body_preview", statsBody[:minInt(len(statsBody), 200)])

	statusBody, err := a.post("/status.htm", statsVals)
	if err != nil {
		return nil, fmt.Errorf("netgear status.htm: %w", err)
	}
	slog.Debug("netgear status.htm response", "body_len", len(statusBody), "body_preview", statusBody[:minInt(len(statusBody), 200)])

	rxBytes, txBytes := parseNetgearStats(statsBody)
	portStatuses := parseNetgearPortStatus(statusBody)

	slog.Debug("netgear parsed stats", "rx", rxBytes, "tx", txBytes)
	slog.Debug("netgear parsed status", "statuses", portStatuses)

	const ports = 8
	result := make([]model.PortStatus, 0, ports)
	for i := 0; i < ports; i++ {
		portNum := i + 1
		ps := model.PortStatus{PortID: portNum}

		if i < len(portStatuses) {
			ps.LinkUp = portStatuses[i].up
			if ps.LinkUp && portStatuses[i].speedMbps > 0 {
				v := portStatuses[i].speedMbps
				ps.SpeedMbps = &v
			}
		}

		if i < len(rxBytes) && rxBytes[i] > 0 {
			mb := float64(rxBytes[i]) * 1e-6
			ps.RxMBytes = &mb
		}
		if i < len(txBytes) && txBytes[i] > 0 {
			mb := float64(txBytes[i]) * 1e-6
			ps.TxMBytes = &mb
		}

		result = append(result, ps)
	}
	return result, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Auth helpers ───────────────────────────────────────────────────────────

func netgearLoginPassword(loginPage, password string) string {
	rand := extractInputValue(loginPage, "rand")
	if rand == "" {
		return password
	}
	merged := mergeStrings(password, rand)
	sum := md5.Sum([]byte(merged)) //nolint:gosec
	return fmt.Sprintf("%x", sum)
}

func mergeStrings(s1, s2 string) string {
	r1, r2 := []rune(s1), []rune(s2)
	var b strings.Builder
	for i, j := 0, 0; i < len(r1) || j < len(r2); {
		if i < len(r1) {
			b.WriteRune(r1[i])
			i++
		}
		if j < len(r2) {
			b.WriteRune(r2[j])
			j++
		}
	}
	return b.String()
}

// ─── HTML parsers ───────────────────────────────────────────────────────────

func extractInputValue(body, id string) string {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}
	var find func(*html.Node) string
	find = func(n *html.Node) string {
		if n.Type == html.ElementNode && n.Data == "input" {
			var nodeID, nodeVal string
			for _, a := range n.Attr {
				switch a.Key {
				case "id":
					nodeID = a.Val
				case "value":
					nodeVal = a.Val
				}
			}
			if nodeID == id {
				return nodeVal
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v := find(c); v != "" {
				return v
			}
		}
		return ""
	}
	return find(doc)
}

type portStatEntry struct {
	up        bool
	speedMbps int
}

func parseNetgearPortStatus(body string) []portStatEntry {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var rows [][]string
	var collectRows func(*html.Node)
	collectRows = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			for _, a := range n.Attr {
				if a.Key == "class" && a.Val == "portID" {
					var cells []string
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Type == html.ElementNode && c.Data == "td" {
							cells = append(cells, textContent(c))
						}
					}
					rows = append(rows, cells)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collectRows(c)
		}
	}
	collectRows(doc)

	if len(rows) > 0 {
		return parseStatusRows(rows)
	}
	return parseNetgearPortStatusFromScript(body)
}

func parseStatusRows(rows [][]string) []portStatEntry {
	var result []portStatEntry
	for _, cells := range rows {
		if len(cells) < 5 {
			result = append(result, portStatEntry{})
			continue
		}
		up := isConnected(strings.TrimSpace(cells[2]))
		speedMbps := parseSpeedText(strings.TrimSpace(cells[4]))
		result = append(result, portStatEntry{up: up, speedMbps: speedMbps})
	}
	return result
}

func parseNetgearPortStatusFromScript(body string) []portStatEntry {
	re := regexp.MustCompile(`portInfo\[(\d+)\]\s*=\s*"([^"]*)"`)
	matches := re.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	result := make([]portStatEntry, 8)
	for _, m := range matches {
		idx, _ := strconv.Atoi(m[1])
		if idx >= 8 {
			continue
		}
		parts := strings.Split(m[2], ",")
		var entry portStatEntry
		if len(parts) > 0 {
			entry.up = isConnected(parts[0])
		}
		if len(parts) > 1 {
			entry.speedMbps = parseSpeedText(parts[1])
		}
		result[idx] = entry
	}
	return result
}

func parseNetgearStats(body string) (rx, tx []int64) {
	// The portStatistics.cgi page puts <input name="rxPkt"> and <input name="txpkt">
	// as direct children of <tr>, which causes Go's HTML parser to relocate them
	// outside the table. Parse them with regex directly on the raw HTML instead.
	rxVals := reInputValue(body, "rxPkt")
	txVals := reInputValue(body, "txpkt")
	if len(rxVals) > 0 {
		for _, v := range rxVals {
			rx = append(rx, parseHexOrDec(v))
		}
		for _, v := range txVals {
			tx = append(tx, parseHexOrDec(v))
		}
		return
	}

	// Fallback: table rows (older firmware)
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return
	}
	var rows [][]string
	collectPortIDRows(doc, &rows)
	for _, cells := range rows {
		if len(cells) >= 3 {
			rx = append(rx, parseHexOrDec(cells[1]))
			tx = append(tx, parseHexOrDec(cells[2]))
		}
	}
	return
}

// reInputValue finds all value attributes from <input> tags with the given name,
// in document order. Handles both attribute orderings.
func reInputValue(body, name string) []string {
	qn := regexp.QuoteMeta(name)
	// Match the whole <input> tag, then extract value from it
	tagRe := regexp.MustCompile(`(?i)<input\s[^>]*name=['"]?` + qn + `['"]?[^>]*>`)
	valRe := regexp.MustCompile(`(?i)value=['"]?([0-9a-fA-F]*)['"]?`)
	var vals []string
	for _, tag := range tagRe.FindAllString(body, -1) {
		m := valRe.FindStringSubmatch(tag)
		if len(m) > 1 {
			vals = append(vals, m[1])
		}
	}
	return vals
}

func collectPortIDRows(n *html.Node, rows *[][]string) {
	if n.Type == html.ElementNode && n.Data == "tr" {
		for _, a := range n.Attr {
			if a.Key == "class" && a.Val == "portID" {
				var cells []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && c.Data == "td" {
						cells = append(cells, textContent(c))
					}
				}
				*rows = append(*rows, cells)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectPortIDRows(c, rows)
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func isConnected(s string) bool {
	switch strings.TrimSpace(s) {
	case "Up", "up", "UP", "Aktiv", "aktiv":
		return true
	}
	return false
}

func parseSpeedText(s string) int {
	re := regexp.MustCompile(`^(\d+)`)
	if m := re.FindString(strings.TrimSpace(s)); m != "" {
		v, _ := strconv.Atoi(m)
		return v
	}
	return 0
}

func parseHexOrDec(s string) int64 {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		v, _ := strconv.ParseInt(s[2:], 16, 64)
		return v
	}
	// Netgear portStatistics.cgi always returns hex without 0x prefix.
	// Try hex first; fall back to decimal only if hex fails.
	if v, err := strconv.ParseInt(s, 16, 64); err == nil {
		return v
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}
