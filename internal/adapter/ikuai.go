package adapter

import (
	"bytes"
	"crypto/md5" //nolint:gosec // iKuai's login API requires MD5(password)
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
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
	hash := md5.Sum([]byte(c.password)) //nolint:gosec // protocol-defined by iKuai
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
//
// Fields exposed by iKuai OS 4.x (策略中心 → NAT → 端口映射):
//
//	id, enabled (yes/no), tagname, comment, interface (all or wan1,wan2),
//	lproto (kept untouched on edit), protocol (tcp/udp/tcp+udp),
//	wan_port, lan_addr, lan_addr_int (derived, recomputed by the router),
//	src_addr {custom: [], object: []}
//
// tagname and comment are stored percent-encoded by iKuai and must be
// encoded with EncodeIkuaiText before they are sent to add/edit.
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
	return DecodeIkuaiText(r.String("comment"))
}

func (r DNATRule) String(key string) string {
	if v, ok := r[key].(string); ok {
		return v
	}
	return ""
}

// Tagname returns the rule name with iKuai percent-encoding decoded.
func (r DNATRule) Tagname() string {
	return DecodeIkuaiText(r.String("tagname"))
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
			"TYPE": "data,total,enabled_total,disabled_total",
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
	target, err := c.findDNATRule(id)
	if err != nil {
		return err
	}

	state := "no"
	if enabled {
		state = "yes"
	}
	target["enabled"] = state

	// iKuai edit requires full rule parameters, which we preserved in the map
	if err := c.editDNATRule(target); err != nil {
		slog.Warn("iKuai DNAT toggle failed", "rule_id", id, "enabled", state, "err", err)
		return fmt.Errorf("toggle rule %d: %w", id, err)
	}

	slog.Info("iKuai DNAT toggle OK", "rule_id", id, "enabled", state, "func_name", c.dnatFuncName)
	return nil
}

// findDNATRule returns a copy of the rule with the given ID.
func (c *IkuaiClient) findDNATRule(id int) (DNATRule, error) {
	rules, err := c.GetDNATRules()
	if err != nil {
		return nil, err
	}
	for _, r := range rules {
		if r.ID() == id {
			return r, nil
		}
	}
	return nil, fmt.Errorf("rule with ID %d not found", id)
}

// DNATRuleSpec describes a port-mapping rule to create or update.
// Fields are the plain (decoded) values; encoding happens in the adapter.
type DNATRuleSpec struct {
	Name      string   `json:"-"`
	Comment   string   `json:"-"`
	Protocol  string   `json:"-"`
	Interface string   `json:"-"`
	WanPort   string   `json:"-"`
	LanAddr   string   `json:"-"`
	LanPort   string   `json:"-"`
	Enabled   *bool    `json:"-"`
	SrcAddr   []string `json:"-"`
}

const (
	// DNATMinPort is the lowest port number accepted by iKuai.
	DNATMinPort = 1
	// DNATMaxPort is the highest port number accepted by iKuai.
	DNATMaxPort = 65535
	// DNATMaxTagnameLen is the iKuai limit for rule names.
	DNATMaxTagnameLen = 15
	// DNATMaxCommentLen is the iKuai limit for rule comments.
	DNATMaxCommentLen = 64
)

// ikuaiEscape sequences used by the iKuai web UI (encode function "pa").
// Percent must come first so already-escaped values are not double-escaped.
var ikuaiEscape = []struct{ from, to string }{
	{"%", "%25"},
	{"\"", "%22"},
	{"`", "%60"},
	{"$", "%24"},
	{"&", "%26"},
	{";", "%3B"},
	{"\\", "%5C"},
	{"|", "%7C"},
	{"'", "%27"},
}

// ikuaiUnescape reverses EncodeIkuaiText (iKuai decode function).
var ikuaiUnescape = []struct{ from, to string }{
	{"%20", " "},
	{"%25", "%"},
	{"%22", "\""},
	{"%60", "`"},
	{"%24", "$"},
	{"%26", "&"},
	{"%3B", ";"},
	{"%5C", "\\"},
	{"%7C", "|"},
	{"%27", "'"},
	{"%2F", "/"},
	{"%21", "!"},
	{"%28", "("},
	{"%29", ")"},
	{"%2A", "*"},
	{"%5E", "^"},
	{"%3C", "<"},
	{"%3E", ">"},
	{"%5B", "["},
	{"%5D", "]"},
	{"%23", "#"},
}

// EncodeIkuaiText applies iKuai's custom percent-encoding to a string field.
// Whitespace is first collapsed to single spaces the way the UI's
// normalizeWhitespace option does, then escaped.
func EncodeIkuaiText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	for _, pair := range ikuaiEscape {
		s = strings.ReplaceAll(s, pair.from, pair.to)
	}
	return strings.ReplaceAll(s, " ", "%20")
}

// DecodeIkuaiText reverses EncodeIkuaiText, mirroring iKuai's own decode()
// helper pair-by-pair (it does not touch '+', so neither do we).
func DecodeIkuaiText(s string) string {
	if s == "" || !strings.Contains(s, "%") {
		return s
	}
	for _, pair := range ikuaiUnescape {
		s = strings.ReplaceAll(s, pair.from, pair.to)
	}
	return s
}

// ValidateDNATRuleSpec checks the fields against iKuai's own UI/server limits
// and returns a user-facing error message on the first problem found.
func ValidateDNATRuleSpec(spec DNATRuleSpec) error {
	tag := strings.TrimSpace(spec.Name)
	if tag == "" {
		return fmt.Errorf("name (tagname) is required")
	}
	if n := len([]rune(tag)); n > DNATMaxTagnameLen {
		return fmt.Errorf("name %q is too long: %d characters (iKuai max %d)", tag, n, DNATMaxTagnameLen)
	}
	if !validIkuaiTagname(tag) {
		return fmt.Errorf("invalid name %q: iKuai allows letters, digits, CJK, '-' and '_' only (must start with a letter/digit/CJK, no spaces or dots)", tag)
	}

	proto := strings.ToLower(strings.TrimSpace(spec.Protocol))
	switch proto {
	case "", "tcp", "udp", "tcp+udp":
	default:
		return fmt.Errorf("invalid protocol %q (use tcp, udp or tcp+udp)", spec.Protocol)
	}

	if spec.Interface != "" && !validIkuaiInterface(spec.Interface) {
		return fmt.Errorf("invalid interface %q (use 'all' or a comma-separated list like wan1,wan2)", spec.Interface)
	}

	if err := validatePortSpec("wan_port", spec.WanPort); err != nil {
		return err
	}
	if err := validatePortSpec("lan_port", spec.LanPort); err != nil {
		return err
	}
	if dn, wn := countPortSpec(spec.LanPort), countPortSpec(spec.WanPort); dn != wn {
		return fmt.Errorf("wan_port (%d ports) and lan_port (%d ports) must have the same count", wn, dn)
	}

	if strings.TrimSpace(spec.LanAddr) == "" {
		return fmt.Errorf("lan_addr is required")
	}
	for _, addr := range strings.Split(spec.LanAddr, ",") {
		if net.ParseIP(strings.TrimSpace(addr)) == nil {
			return fmt.Errorf("invalid lan_addr %q (use an IPv4/IPv6 address)", spec.LanAddr)
		}
	}

	if n := len([]rune(spec.Comment)); n > DNATMaxCommentLen {
		return fmt.Errorf("comment is too long: %d characters (iKuai max %d)", n, DNATMaxCommentLen)
	}
	if c := strings.TrimSpace(spec.Comment); c != "" && (strings.HasPrefix(c, "-") || strings.HasPrefix(c, "_")) {
		return fmt.Errorf("comment must not start with '-' or '_'")
	}

	for _, src := range spec.SrcAddr {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		if strings.Contains(src, "/") {
			if _, _, err := net.ParseCIDR(src); err != nil {
				return fmt.Errorf("invalid src_addr %q (use CIDR like 203.0.113.0/24, or a range)", src)
			}
			continue
		}
		if net.ParseIP(src) == nil {
			return fmt.Errorf("invalid src_addr %q (use an IP, CIDR or range)", src)
		}
	}
	return nil
}

// validIkuaiTagname mirrors the router's own tagname rule: 1-15 characters of
// letters/digits/CJK/'-'/'_', and the first character must be alphanumeric or
// CJK. Dots and spaces are rejected by iKuai (verified against 4.0.313).
func validIkuaiTagname(s string) bool {
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		case isCJK(r):
		default:
			return false
		}
		if i == 0 && (r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0xF900 && r <= 0xFAFF)
}

func validIkuaiInterface(s string) bool {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '@') {
				return false
			}
		}
	}
	return true
}

// validatePortSpec accepts a port, a "start-end" range, or a comma-separated
// list of those (e.g. "8080", "8000-8010", "8080,8081").
func validatePortSpec(field, spec string) error {
	if strings.TrimSpace(spec) == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("invalid %s %q: empty port entry", field, spec)
		}
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return fmt.Errorf("invalid %s %q: use PORT or START-END", field, spec)
		}
		var lo, hi int
		var err error
		if lo, err = strconv.Atoi(strings.TrimSpace(bounds[0])); err != nil {
			return fmt.Errorf("invalid %s %q: not a number", field, spec)
		}
		hi = lo
		if len(bounds) == 2 {
			if hi, err = strconv.Atoi(strings.TrimSpace(bounds[1])); err != nil {
				return fmt.Errorf("invalid %s %q: not a number", field, spec)
			}
		}
		if lo < DNATMinPort || lo > DNATMaxPort || hi < DNATMinPort || hi > DNATMaxPort {
			return fmt.Errorf("invalid %s %q: ports must be %d-%d", field, spec, DNATMinPort, DNATMaxPort)
		}
		if hi < lo {
			return fmt.Errorf("invalid %s %q: range end must be >= start", field, spec)
		}
	}
	return nil
}

// countPortSpec returns how many ports a port spec covers, mirroring the
// iKuai UI's "内外网端口数量不相同" check.
func countPortSpec(spec string) int {
	n := 0
	for _, part := range strings.Split(spec, ",") {
		bounds := strings.Split(strings.TrimSpace(part), "-")
		lo, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
		if err != nil {
			continue
		}
		ht := lo
		if len(bounds) == 2 {
			if hi, err := strconv.Atoi(strings.TrimSpace(bounds[1])); err == nil {
				ht = hi
			}
		}
		if ht >= lo {
			n += ht - lo + 1
		}
	}
	return n
}

// CreateDNATRule adds a port-mapping rule and returns its new ID (0 if the
// router did not report one).
func (c *IkuaiClient) CreateDNATRule(spec DNATRuleSpec) (int, error) {
	param, err := c.dnatParamFromSpec(spec, nil)
	if err != nil {
		return 0, err
	}
	enabled := true
	if spec.Enabled != nil {
		enabled = *spec.Enabled
	}
	if enabled {
		param["enabled"] = "yes"
	} else {
		param["enabled"] = "no"
	}
	delete(param, "id")

	resp, err := c.callDNAT("add", param)
	if err != nil {
		return 0, err
	}
	return resp.RowID, nil
}

// UpdateDNATRule edits an existing rule. Fields left empty in the spec are
// taken from the current rule, so callers can change only what they care about.
func (c *IkuaiClient) UpdateDNATRule(id int, spec DNATRuleSpec) error {
	target, err := c.findDNATRule(id)
	if err != nil {
		return err
	}
	param, err := c.dnatParamFromSpec(spec, target)
	if err != nil {
		return err
	}
	param["id"] = id
	// Keep the operator-controlled switch state unless explicitly overridden.
	if spec.Enabled != nil {
		if *spec.Enabled {
			param["enabled"] = "yes"
		} else {
			param["enabled"] = "no"
		}
	} else {
		param["enabled"] = target.Enabled()
	}
	if err := c.editDNATRule(param); err != nil {
		return fmt.Errorf("edit rule %d: %w", id, err)
	}
	return nil
}

// DeleteDNATRule removes rules by ID.
func (c *IkuaiClient) DeleteDNATRule(ids ...int) error {
	if len(ids) == 0 {
		return fmt.Errorf("no rule id given")
	}
	// Hard-deletes happen on the router; make sure the rule exists so a typo
	// cannot silently succeed.
	if len(ids) == 1 {
		if _, err := c.findDNATRule(ids[0]); err != nil {
			return err
		}
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	if _, err := c.callDNAT("del", map[string]interface{}{"id": strings.Join(parts, ",")}); err != nil {
		return fmt.Errorf("delete rule(s) %s: %w", strings.Join(parts, ","), err)
	}
	return nil
}

// dnatParamFromSpec builds the add/edit param map. When current is non-nil the
// existing rule is used as the base so unrelated fields survive the edit.
func (c *IkuaiClient) dnatParamFromSpec(spec DNATRuleSpec, current DNATRule) (map[string]interface{}, error) {
	name := spec.Name
	comment := spec.Comment
	proto := spec.Protocol
	iface := spec.Interface
	wanPort := spec.WanPort
	lanAddr := spec.LanAddr
	lanPort := spec.LanPort
	srcAddr := spec.SrcAddr
	srcExplicit := spec.SrcAddr != nil

	if current != nil {
		if name == "" {
			name = current.Tagname()
		}
		if comment == "" {
			comment = current.Comment()
		}
		if proto == "" {
			proto = current.String("protocol")
		}
		if iface == "" {
			iface = current.String("interface")
		}
		if wanPort == "" {
			wanPort = current.String("wan_port")
		}
		if lanAddr == "" {
			lanAddr = current.String("lan_addr")
		}
		if lanPort == "" {
			lanPort = current.String("lan_port")
		}
		if !srcExplicit {
			srcAddr = currentSrcAddrs(current)
		}
	}

	if proto == "" {
		proto = "tcp"
	}
	if iface == "" {
		iface = "all"
	}
	if lanPort == "" {
		lanPort = wanPort
	}

	check := DNATRuleSpec{
		Name:      name,
		Comment:   comment,
		Protocol:  proto,
		Interface: iface,
		WanPort:   wanPort,
		LanAddr:   lanAddr,
		LanPort:   lanPort,
		SrcAddr:   srcAddr,
	}
	if err := ValidateDNATRuleSpec(check); err != nil {
		return nil, err
	}

	custom := make([]interface{}, 0, len(srcAddr))
	for _, src := range srcAddr {
		src = strings.TrimSpace(src)
		if src != "" {
			custom = append(custom, src)
		}
	}
	objects := []interface{}{}
	if current != nil && !srcExplicit {
		// Caller did not touch src_addr: keep any IP-group objects as they are
		// (they reference router-side groups we cannot reconstruct).
		if raw, ok := current["src_addr"].(map[string]interface{}); ok {
			if objs, ok := raw["object"].([]interface{}); ok {
				objects = objs
			}
		}
	}

	param := map[string]interface{}{
		"tagname":   EncodeIkuaiText(name),
		"comment":   EncodeIkuaiText(comment),
		"protocol":  proto,
		"interface": iface,
		"wan_port":  wanPort,
		"lan_addr":  lanAddr,
		"lan_port":  lanPort,
		"src_addr": map[string]interface{}{
			"custom": custom,
			"object": objects,
		},
	}
	return param, nil
}

// currentSrcAddrs extracts the src_addr.custom list from an existing rule.
func currentSrcAddrs(rule DNATRule) []string {
	raw, ok := rule["src_addr"].(map[string]interface{})
	if !ok {
		return nil
	}
	switch v := raw["custom"].(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

func (c *IkuaiClient) editDNATRule(param map[string]interface{}) error {
	_, err := c.callDNAT("edit", param)
	return err
}

// ikuaiCallResponse carries the bits of an iKuai call response we care about.
type ikuaiCallResponse struct {
	RowID int
}

// callDNAT posts one dnat action and maps API errors onto Go errors, keeping
// the router's own message (e.g. "端口冲突") intact for the user.
func (c *IkuaiClient) callDNAT(action string, param map[string]interface{}) (ikuaiCallResponse, error) {
	if c.dnatFuncName == "" {
		// Resolve the working func_name first; also primes the rule cache.
		if _, err := c.GetDNATRules(); err != nil {
			return ikuaiCallResponse{}, err
		}
		if c.dnatFuncName == "" {
			c.dnatFuncName = "dnat"
		}
	}

	reqBody := map[string]interface{}{
		"func_name": c.dnatFuncName,
		"action":    action,
		"param":     param,
	}
	b, err := json.Marshal(reqBody)
	if err != nil {
		return ikuaiCallResponse{}, err
	}

	slog.Info("iKuai DNAT request", "action", action, "func_name", c.dnatFuncName, "param", param)
	resp, err := c.postJSON("/Action/call", "dnat_"+action, b)
	if err != nil {
		return ikuaiCallResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ikuaiCallResponse{}, err
	}

	env, fields, err := decodeIkuaiEnvelope(bytes.NewReader(raw))
	if err != nil {
		slog.Error("iKuai DNAT JSON decode failed", "action", action, "err", err)
		return ikuaiCallResponse{}, err
	}

	if !callSuccess(env, fields) {
		msg := callErrorMessage(env)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		slog.Warn("iKuai DNAT API error", "action", action, "result", env.Result, "code", env.Code, "err_msg", msg, "func_name", c.dnatFuncName)
		return ikuaiCallResponse{}, fmt.Errorf("%s failed: %s (code %d)", c.dnatFuncName, msg, callErrorCode(env))
	}

	var out struct {
		RowID int `json:"rowid"`
		ID    int `json:"id"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.RowID == 0 {
		out.RowID = out.ID
	}
	if out.RowID == 0 {
		var nested struct {
			Results struct {
				RowID int `json:"rowid"`
				ID    int `json:"id"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &nested); err == nil {
			out.RowID = nested.Results.RowID
			if out.RowID == 0 {
				out.RowID = nested.Results.ID
			}
		}
	}

	slog.Info("iKuai DNAT request OK", "action", action, "func_name", c.dnatFuncName, "rowid", out.RowID)
	return ikuaiCallResponse{RowID: out.RowID}, nil
}
