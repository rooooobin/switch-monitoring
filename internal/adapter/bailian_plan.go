package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// BailianUsageWindow is one quota window (5h / 7d / month) with used/remaining/reset.
type BailianUsageWindow struct {
	Name       string // e.g. "5-hour", "7-day", "monthly"
	Used       float64
	Total      float64
	Remaining  float64
	UsedPct    float64 // 0-100
	ResetsAt   time.Time
	HasReset   bool
	HasTotals  bool
}

// BailianPlanUsage is Token Plan / Coding Plan usage (not model rate-limit quotas).
type BailianPlanUsage struct {
	PlanKind    string // token_plan_personal | coding_plan
	PlanName    string // Lite / Standard / Pro / ...
	Status      string
	RemainingDays *int
	EndsAt      time.Time
	HasEndsAt   bool
	Windows     []BailianUsageWindow
	Source      string // cookie | bl-cli
}

// BailianPlanClient queries Bailian subscription usage via console gateway or bl CLI.
type BailianPlanClient struct {
	plan       string
	cookie     string
	proxy      string
	preferBL   bool
	httpClient *http.Client
}

// NewBailianPlanClient builds a client. plan is token_plan_personal|coding_plan|auto.
func NewBailianPlanClient(plan, cookie, proxy string, preferBL bool) (*BailianPlanClient, error) {
	plan = strings.ToLower(strings.TrimSpace(plan))
	if plan == "" {
		plan = "token_plan_personal"
	}
	hc, err := httpClient(proxy, 45*time.Second)
	if err != nil {
		return nil, err
	}
	return &BailianPlanClient{
		plan:       plan,
		cookie:     strings.TrimSpace(cookie),
		proxy:      proxy,
		preferBL:   preferBL,
		httpClient: hc,
	}, nil
}

// LoadBailianConsoleCookie returns cookie from inline value or file path.
func LoadBailianConsoleCookie(inline, filePath string) (string, error) {
	inline = strings.TrimSpace(inline)
	if inline != "" {
		return normalizeCookieHeader(inline), nil
	}
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", nil
	}
	b, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read console_cookie_file: %w", err)
	}
	return normalizeCookieHeader(string(b)), nil
}

func normalizeCookieHeader(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "Cookie:")
	s = strings.TrimPrefix(s, "cookie:")
	return strings.TrimSpace(s)
}

// GetUsage fetches used/remaining/reset windows for the configured plan.
func (c *BailianPlanClient) GetUsage(ctx context.Context) (*BailianPlanUsage, error) {
	plan := c.plan
	if plan == "auto" {
		plan = "token_plan_personal"
	}

	var errs []string
	if c.preferBL || c.cookie == "" {
		if u, err := c.fetchViaBL(ctx, plan); err == nil {
			return u, nil
		} else if err != nil {
			errs = append(errs, "bl-cli: "+err.Error())
		}
	}
	if c.cookie != "" {
		switch plan {
		case "coding_plan":
			u, err := c.fetchCodingPlanCookie(ctx)
			if err == nil {
				return u, nil
			}
			errs = append(errs, "coding_plan: "+err.Error())
		default:
			u, err := c.fetchTokenPlanPersonalCookie(ctx)
			if err == nil {
				return u, nil
			}
			errs = append(errs, "token_plan: "+err.Error())
		}
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("bailian usage requires console session: set llm_usage.bailian.console_cookie (or console_cookie_file), or run `bl auth login --console` on this host")
	}
	return nil, fmt.Errorf("%s", strings.Join(errs, "; "))
}

func (c *BailianPlanClient) fetchViaBL(ctx context.Context, plan string) (*BailianPlanUsage, error) {
	if _, err := exec.LookPath("bl"); err != nil {
		return nil, fmt.Errorf("bl not installed")
	}
	var args []string
	switch plan {
	case "coding_plan":
		args = []string{"usage", "coding-plan", "--console-region", "cn-beijing", "--console-site", "domestic", "--output", "json"}
	default:
		// Personal Token Plan: console call returns the rolling-window usage payload.
		args = []string{
			"console", "call",
			"--api", "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage",
			"--data", "{}",
			"--console-region", "cn-beijing",
			"--console-site", "domestic",
			"--output", "json",
		}
	}
	cmd := exec.CommandContext(ctx, "bl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", truncateForLog([]byte(msg), 300))
	}
	usage, err := parseTokenPlanPersonalUsageJSON(out, nil, nil)
	if err != nil {
		// coding-plan / token-plan team JSON may differ; try generic windows extract
		if u2, err2 := parseBailianCLIUsageLoose(out, plan); err2 == nil {
			u2.Source = "bl-cli"
			return u2, nil
		}
		return nil, err
	}
	usage.Source = "bl-cli"
	usage.PlanKind = plan
	return usage, nil
}

func (c *BailianPlanClient) fetchTokenPlanPersonalCookie(ctx context.Context) (*BailianPlanUsage, error) {
	usageRaw, err := c.consolePersonalAPI(ctx, "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage")
	if err != nil {
		return nil, err
	}
	subRaw, _ := c.consolePersonalAPI(ctx, "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription")
	cfgRaw, _ := c.consolePersonalAPI(ctx, "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/quota-config")
	usage, err := parseTokenPlanPersonalUsageJSON(usageRaw, subRaw, cfgRaw)
	if err != nil {
		return nil, err
	}
	usage.Source = "cookie"
	usage.PlanKind = "token_plan_personal"
	return usage, nil
}

func (c *BailianPlanClient) fetchCodingPlanCookie(ctx context.Context) (*BailianPlanUsage, error) {
	api := "zeldaEasy.broadscope-bailian.codingPlan.queryCodingPlanInstanceInfoV2"
	u := "https://bailian.console.aliyun.com/data/api.json?action=" + url.QueryEscape(api) +
		"&product=broadscope-bailian&api=" + url.QueryEscape(api) + "&currentRegionId=cn-beijing"
	body := map[string]any{
		"queryCodingPlanInstanceInfoRequest": map[string]any{
			"commodityCode":  "sfm_codingplan_public_cn",
			"onlyLatestOne":  true,
		},
	}
	raw, err := c.postJSON(ctx, u, body)
	if err != nil {
		return nil, err
	}
	usage, err := parseCodingPlanUsageJSON(raw)
	if err != nil {
		return nil, err
	}
	usage.Source = "cookie"
	usage.PlanKind = "coding_plan"
	return usage, nil
}

func (c *BailianPlanClient) consolePersonalAPI(ctx context.Context, api string) ([]byte, error) {
	endpoint := "https://bailian-cs.console.aliyun.com/data/api.json?action=BroadScopeAspnGateway&product=sfm_bailian&api=" +
		url.QueryEscape(api) + "&_v=undefined"
	corner := map[string]any{
		"feTraceId":         fmt.Sprintf("%d", time.Now().UnixNano()),
		"feURL":             "https://bailian.console.aliyun.com/cn-beijing?tab=plan#/efm/subscription/token-plan/personal",
		"protocol":          "V2",
		"console":           "ONE_CONSOLE",
		"productCode":       "p_efm",
		"domain":            "bailian.console.aliyun.com",
		"consoleSite":       "BAILIAN_ALIYUN",
		"userNickName":      "",
		"userPrincipalName": "",
		"xsp_lang":          "zh-CN",
	}
	paramsObj := map[string]any{
		"Api":  api,
		"V":    "1.0",
		"Data": map[string]any{"cornerstoneParam": corner},
	}
	paramsJSON, _ := json.Marshal(paramsObj)
	form := url.Values{}
	form.Set("product", "sfm_bailian")
	form.Set("action", "BroadScopeAspnGateway")
	form.Set("region", "cn-beijing")
	form.Set("params", string(paramsJSON))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json,*/*")
	req.Header.Set("Origin", "https://bailian.console.aliyun.com")
	req.Header.Set("Referer", "https://bailian.console.aliyun.com/cn-beijing?tab=plan")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")
	req.Header.Set("Cookie", c.cookie)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, truncateForLog(body, 200))
	}
	if err := checkBailianGatewayLogin(body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *BailianPlanClient) postJSON(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://bailian.console.aliyun.com")
	req.Header.Set("Referer", "https://bailian.console.aliyun.com/cn-beijing/?tab=model#/efm/coding_plan")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Cookie", c.cookie)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, truncateForLog(body, 200))
	}
	if err := checkBailianGatewayLogin(body); err != nil {
		return nil, err
	}
	return body, nil
}

func checkBailianGatewayLogin(body []byte) error {
	s := string(body)
	if strings.Contains(s, "ConsoleNeedLogin") || strings.Contains(s, "NotLogined") || strings.Contains(s, "请登录") {
		return fmt.Errorf("console login required (cookie expired or missing); refresh llm_usage.bailian.console_cookie or run `bl auth login --console`")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		if strings.Contains(strings.ToLower(s), "<html") {
			return fmt.Errorf("console returned HTML (not logged in)")
		}
		return nil
	}
	if code := jsonString(root["code"]); strings.Contains(strings.ToLower(code), "login") {
		return fmt.Errorf("console login required: %s", code)
	}
	return nil
}

func parseTokenPlanPersonalUsageJSON(usageRaw, subRaw, cfgRaw []byte) (*BailianPlanUsage, error) {
	usageObj, err := unwrapBailianData(usageRaw)
	if err != nil {
		return nil, err
	}
	pct5 := jsonFloat(usageObj, "per5HourPercentage")
	pctW := jsonFloat(usageObj, "per1WeekPercentage")
	pctM := jsonFloat(usageObj, "per1MonthPercentage")
	if pct5 == nil && pctW == nil && pctM == nil {
		return nil, fmt.Errorf("no usage windows in response")
	}

	out := &BailianPlanUsage{PlanKind: "token_plan_personal", PlanName: "Token Plan"}

	var planCode string
	if len(subRaw) > 0 {
		if sub, err := unwrapBailianData(subRaw); err == nil {
			planCode = strings.ToLower(firstNonEmpty(jsonString(sub["specCode"]), jsonString(sub["spec_code"]), jsonString(sub["planName"])))
			if planCode != "" {
				out.PlanName = strings.ToUpper(planCode[:1]) + planCode[1:]
			}
			out.Status = jsonString(sub["status"])
			if d := jsonInt(sub, "remainingDays", "remaining_days"); d != nil {
				out.RemainingDays = d
			}
			if ms := jsonFloat(sub, "endTime", "end_time"); ms != nil && *ms > 0 {
				out.EndsAt = time.UnixMilli(int64(*ms))
				out.HasEndsAt = true
			}
		}
	}

	var tot5, totW, totM *float64
	if len(cfgRaw) > 0 && planCode != "" {
		if cfg, err := unwrapBailianData(cfgRaw); err == nil {
			if raw, ok := cfg[planCode]; ok {
				var tier map[string]json.RawMessage
				if json.Unmarshal(raw, &tier) == nil {
					tot5 = jsonFloat(tier, "five_hour", "fiveHour")
					totW = jsonFloat(tier, "weekly")
					totM = jsonFloat(tier, "monthly")
				}
			}
		}
	}

	if pct5 != nil {
		out.Windows = append(out.Windows, makeWindow("5-hour", *pct5, tot5, jsonFloat(usageObj, "per5HourResetTime")))
	}
	if pctW != nil {
		out.Windows = append(out.Windows, makeWindow("7-day", *pctW, totW, jsonFloat(usageObj, "per1WeekResetTime")))
	}
	if pctM != nil {
		out.Windows = append(out.Windows, makeWindow("monthly", *pctM, totM, jsonFloat(usageObj, "per1MonthResetTime")))
	}
	return out, nil
}

func makeWindow(name string, ratio float64, total *float64, resetMS *float64) BailianUsageWindow {
	// API returns fraction 0-1
	pct := ratio * 100
	if ratio > 1 {
		pct = ratio // already percentage points
	}
	w := BailianUsageWindow{Name: name, UsedPct: pct}
	if total != nil && *total > 0 {
		w.HasTotals = true
		w.Total = *total
		w.Used = *total * (pct / 100)
		w.Remaining = w.Total - w.Used
		if w.Remaining < 0 {
			w.Remaining = 0
		}
	}
	if resetMS != nil && *resetMS > 0 {
		w.ResetsAt = time.UnixMilli(int64(*resetMS))
		w.HasReset = true
	}
	return w
}

func parseCodingPlanUsageJSON(raw []byte) (*BailianPlanUsage, error) {
	root, err := unwrapBailianData(raw)
	if err != nil {
		// coding plan may nest under codingPlanInstanceInfos
		var top map[string]json.RawMessage
		if json.Unmarshal(raw, &top) != nil {
			return nil, err
		}
		root = top
	}
	quota := findQuotaMap(root)
	if quota == nil {
		return nil, fmt.Errorf("coding plan quota info not found")
	}
	out := &BailianPlanUsage{PlanKind: "coding_plan", PlanName: "Coding Plan"}
	out.Windows = appendCodingWindow(out.Windows, "5-hour",
		jsonFloat(quota, "per5HourUsedQuota", "perFiveHourUsedQuota"),
		jsonFloat(quota, "per5HourTotalQuota", "perFiveHourTotalQuota"),
		jsonFloat(quota, "per5HourQuotaNextRefreshTime", "perFiveHourQuotaNextRefreshTime"))
	out.Windows = appendCodingWindow(out.Windows, "weekly",
		jsonFloat(quota, "perWeekUsedQuota"),
		jsonFloat(quota, "perWeekTotalQuota"),
		jsonFloat(quota, "perWeekQuotaNextRefreshTime"))
	out.Windows = appendCodingWindow(out.Windows, "monthly",
		jsonFloat(quota, "perBillMonthUsedQuota", "perMonthUsedQuota"),
		jsonFloat(quota, "perBillMonthTotalQuota", "perMonthTotalQuota"),
		jsonFloat(quota, "perBillMonthQuotaNextRefreshTime", "perMonthQuotaNextRefreshTime"))
	if len(out.Windows) == 0 {
		return nil, fmt.Errorf("no coding plan windows")
	}
	return out, nil
}

func appendCodingWindow(ws []BailianUsageWindow, name string, used, total, reset *float64) []BailianUsageWindow {
	if used == nil && total == nil {
		return ws
	}
	w := BailianUsageWindow{Name: name}
	if total != nil {
		w.Total = *total
		w.HasTotals = true
	}
	if used != nil {
		w.Used = *used
	}
	if w.HasTotals {
		w.Remaining = w.Total - w.Used
		if w.Remaining < 0 {
			w.Remaining = 0
		}
		if w.Total > 0 {
			w.UsedPct = (w.Used / w.Total) * 100
		}
	}
	if reset != nil && *reset > 0 {
		// may be epoch ms or seconds
		ms := *reset
		if ms < 1e12 {
			ms *= 1000
		}
		w.ResetsAt = time.UnixMilli(int64(ms))
		w.HasReset = true
	}
	return append(ws, w)
}

func findQuotaMap(root map[string]json.RawMessage) map[string]json.RawMessage {
	if q := nestedMap(root, "codingPlanQuotaInfo", "coding_plan_quota_info"); q != nil {
		return q
	}
	// search instance arrays
	for _, key := range []string{"codingPlanInstanceInfos", "coding_plan_instance_infos", "data", "Data", "output"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		var arr []map[string]json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			for _, item := range arr {
				if q := nestedMap(item, "codingPlanQuotaInfo", "coding_plan_quota_info"); q != nil {
					return q
				}
				if q := findQuotaMap(item); q != nil {
					return q
				}
			}
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil {
			if q := findQuotaMap(obj); q != nil {
				return q
			}
		}
	}
	return nil
}

func parseBailianCLIUsageLoose(raw []byte, plan string) (*BailianPlanUsage, error) {
	u, err := parseTokenPlanPersonalUsageJSON(raw, nil, nil)
	if err == nil {
		u.PlanKind = plan
		return u, nil
	}
	u2, err2 := parseCodingPlanUsageJSON(raw)
	if err2 == nil {
		return u2, nil
	}
	return nil, err
}

func unwrapBailianData(raw []byte) (map[string]json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	v = expandEmbeddedJSON(v)
	obj, ok := findObjectContaining(v, []string{
		"per5HourPercentage", "per1WeekPercentage", "per1MonthPercentage",
		"specCode", "lite", "standard", "pro",
		"per5HourUsedQuota", "codingPlanQuotaInfo",
	})
	if !ok {
		// fallback: walk to deepest data map
		if m, ok := asRawMap(v); ok {
			return m, nil
		}
		return nil, fmt.Errorf("unexpected payload shape")
	}
	return obj, nil
}

func expandEmbeddedJSON(v any) any {
	switch t := v.(type) {
	case string:
		var inner any
		if json.Unmarshal([]byte(t), &inner) == nil {
			return expandEmbeddedJSON(inner)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = expandEmbeddedJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = expandEmbeddedJSON(val)
		}
		return out
	default:
		return v
	}
}

func findObjectContaining(v any, keys []string) (map[string]json.RawMessage, bool) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range keys {
			if _, ok := t[k]; ok {
				b, _ := json.Marshal(t)
				var m map[string]json.RawMessage
				_ = json.Unmarshal(b, &m)
				return m, true
			}
		}
		for _, val := range t {
			if m, ok := findObjectContaining(val, keys); ok {
				return m, true
			}
		}
	case []any:
		for _, val := range t {
			if m, ok := findObjectContaining(val, keys); ok {
				return m, true
			}
		}
	}
	return nil, false
}

func asRawMap(v any) (map[string]json.RawMessage, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return m, true
}

// Format helpers used by tests
func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}
