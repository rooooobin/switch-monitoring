// Package config loads and validates YAML configuration for the switch monitor.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SwitchType identifies the switch vendor/model.
type SwitchType string

const (
	TypeNetgearGS108Ev3 SwitchType = "netgear_gs108ev3"
	TypeMercurySG108Pro SwitchType = "mercury_sg108pro"
)

// CalendarProvider selects the calendar API backend.
type CalendarProvider string

const (
	CalendarGoogle    CalendarProvider = "google"
	CalendarMicrosoft CalendarProvider = "microsoft"
)

// SwitchConfig holds per-switch settings.
type SwitchConfig struct {
	Name           string         `yaml:"name"`
	AdminURL       string         `yaml:"admin_url"`
	Type           SwitchType     `yaml:"type"`
	ConcernedPorts []int          `yaml:"concerned_ports"`
	Password       string         `yaml:"password"`
	Username       string         `yaml:"username"`
	PortAliases    map[int]string `yaml:"port_aliases"`
}

// Host extracts the hostname/IP from AdminURL.
func (s *SwitchConfig) Host() string {
	u, err := url.Parse(s.AdminURL)
	if err != nil || u.Hostname() == "" {
		// Fallback: strip scheme manually
		raw := strings.TrimPrefix(s.AdminURL, "http://")
		raw = strings.TrimPrefix(raw, "https://")
		raw = strings.SplitN(raw, "/", 2)[0]
		if idx := strings.LastIndex(raw, ":"); idx > 0 {
			if _, err2 := fmt.Sscanf(raw[idx+1:], "%d", new(int)); err2 == nil {
				return raw[:idx]
			}
		}
		return raw
	}
	return u.Hostname()
}

// SMTPConfig holds email-send settings.
type SMTPConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Host      string `yaml:"smtp_host"`
	Port      int    `yaml:"smtp_port"`
	UseTLS    bool   `yaml:"smtp_use_tls"`
	FromEmail string `yaml:"from_email"`
	User      string `yaml:"smtp_user"`
	Password  string `yaml:"smtp_password"`
}

// TelegramRecipient holds settings for a single Telegram bot+chat destination.
type TelegramRecipient struct {
	Token  string `yaml:"token"`
	ChatID string `yaml:"chat_id"`
	Proxy  string `yaml:"proxy"`
}

// TelegramConfig holds settings for Telegram bot notifications.
type TelegramConfig struct {
	Enabled        bool                `yaml:"enabled"`
	ListenCommands bool                `yaml:"listen_commands"`
	Recipients     []TelegramRecipient `yaml:"recipients"`
}

// IkuaiConfig holds settings for an iKuai router.
type IkuaiConfig struct {
	Enabled  bool   `yaml:"enabled"`
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// XiaoduConfig holds settings for a Xiaodu smart speaker (DLNA + optional DuerOS cloud).
type XiaoduConfig struct {
	Enabled  bool   `yaml:"enabled"`
	IP       string `yaml:"ip"`
	Port     int    `yaml:"port"`
	ClientID string `yaml:"client_id"`
	CUID     string `yaml:"cuid"`
	BDUSS    string `yaml:"bduss"`
	SceneID  string `yaml:"scene_id"`

	AlertTTS    *XiaoduAlertTTSConfig    `yaml:"alert_tts"`
	Probe       *XiaoduProbeConfig       `yaml:"probe"`
	BDUSSCheck  *XiaoduBDUSSCheckConfig  `yaml:"bduss_check"`
}

// PortOrDefault returns the DLNA port, defaulting to 49494.
func (x *XiaoduConfig) PortOrDefault() int {
	if x == nil || x.Port <= 0 {
		return 49494
	}
	return x.Port
}

// MihomoInstance holds settings for a single Mihomo external-controller API.
type MihomoInstance struct {
	Name     string `yaml:"name"`
	APIBase  string `yaml:"api_base"` // e.g. http://127.0.0.1:9090
	Secret   string `yaml:"secret"`   // optional; matches mihomo secret
	Selector string `yaml:"selector"` // proxy group name to switch via Telegram; empty = GLOBAL
}

// MihomoConfig holds settings for the local Mihomo (Clash Meta) external-controller API.
type MihomoConfig struct {
	Enabled   bool             `yaml:"enabled"`
	Instances []MihomoInstance `yaml:"instances"`
	// LatencyTestURL is passed to GET /proxies/{name}/delay as the test URL (default: gstatic 204).
	LatencyTestURL string `yaml:"latency_test_url"`
	// LatencyTimeoutMS is the delay-test timeout in milliseconds sent to Mihomo (default: 5000).
	LatencyTimeoutMS int `yaml:"latency_timeout_ms"`
}

// LatencyTestURLOrDefault returns the configured latency probe URL or the standard default.
func (m *MihomoConfig) LatencyTestURLOrDefault() string {
	if m == nil || strings.TrimSpace(m.LatencyTestURL) == "" {
		return "http://www.gstatic.com/generate_204"
	}
	return strings.TrimSpace(m.LatencyTestURL)
}

// LatencyTimeoutMSOrDefault returns the configured delay-test timeout or 5000 ms.
func (m *MihomoConfig) LatencyTimeoutMSOrDefault() int {
	if m == nil || m.LatencyTimeoutMS <= 0 {
		return 5000
	}
	return m.LatencyTimeoutMS
}

// CalendarConfig holds Google Calendar or Microsoft Outlook (Graph) settings.
// Obtain OAuth refresh tokens via a one-time browser consent; keep this file private.
type CalendarConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Provider   string `yaml:"provider"`
	Timezone   string `yaml:"timezone"`
	CalendarID string `yaml:"calendar_id"`
	Proxy      string `yaml:"proxy"`

	GoogleClientID     string `yaml:"google_client_id"`
	GoogleClientSecret string `yaml:"google_client_secret"`
	GoogleRefreshToken string `yaml:"google_refresh_token"`

	MicrosoftTenantID     string `yaml:"microsoft_tenant_id"`
	MicrosoftClientID     string `yaml:"microsoft_client_id"`
	MicrosoftClientSecret string `yaml:"microsoft_client_secret"`
	MicrosoftRefreshToken string `yaml:"microsoft_refresh_token"`
}

// MonitorConfig is the top-level configuration.
type MonitorConfig struct {
	Switches               []SwitchConfig  `yaml:"switches"`
	AlertEmails            []string        `yaml:"alert_emails"`
	MinSpeedMbps           int             `yaml:"min_speed_mbps"`
	CheckIntervalSeconds   int             `yaml:"check_interval_seconds"`
	RecheckIntervalSeconds int             `yaml:"recheck_interval_seconds"`
	SMTP                   *SMTPConfig     `yaml:"smtp"`
	Telegram               *TelegramConfig `yaml:"telegram"`
	Ikuai                  *IkuaiConfig    `yaml:"ikuai"`
	Xiaodu                 *XiaoduConfig   `yaml:"xiaodu"`
	Mihomo                 *MihomoConfig   `yaml:"mihomo"`
	Calendar               *CalendarConfig `yaml:"calendar"`
	LogDir                 string          `yaml:"log_dir"`
	LogFile                string          `yaml:"log_file"`
	HistoryFile            string          `yaml:"history_file"`
	LogLevel               string          `yaml:"log_level"`
}

// rawYAML mirrors the YAML structure for flexible parsing.
type rawYAML struct {
	Switches []struct {
		Name           string            `yaml:"name"`
		AdminURL       string            `yaml:"admin_url"`
		Type           string            `yaml:"type"`
		ConcernedPorts interface{}       `yaml:"concerned_ports"`
		Password       string            `yaml:"password"`
		Username       string            `yaml:"username"`
		PortAliases    map[string]string `yaml:"port_aliases"`
	} `yaml:"switches"`
	// alert_email (single, legacy) and alert_emails (list) are both accepted.
	AlertEmail             string      `yaml:"alert_email"`
	AlertEmails            interface{} `yaml:"alert_emails"`
	MinSpeedMbps           int         `yaml:"min_speed_mbps"`
	CheckIntervalSeconds   int         `yaml:"check_interval_seconds"`
	RecheckIntervalSeconds int         `yaml:"recheck_interval_seconds"`
	SMTP                   *struct {
		Enabled   bool   `yaml:"enabled"`
		Host      string `yaml:"smtp_host"`
		Port      int    `yaml:"smtp_port"`
		UseTLS    bool   `yaml:"smtp_use_tls"`
		FromEmail string `yaml:"from_email"`
		User      string `yaml:"smtp_user"`
		Password  string `yaml:"smtp_password"`
	} `yaml:"smtp"`
	Telegram *struct {
		Enabled        bool `yaml:"enabled"`
		ListenCommands bool `yaml:"listen_commands"`
		// Single recipient (legacy)
		Token  string `yaml:"token"`
		ChatID string `yaml:"chat_id"`
		Proxy  string `yaml:"proxy"`
		// Multiple recipients
		Recipients []struct {
			Token  string `yaml:"token"`
			ChatID string `yaml:"chat_id"`
			Proxy  string `yaml:"proxy"`
		} `yaml:"recipients"`
	} `yaml:"telegram"`
	Ikuai *struct {
		Enabled  bool   `yaml:"enabled"`
		URL      string `yaml:"url"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"ikuai"`
	Xiaodu *struct {
		Enabled  bool   `yaml:"enabled"`
		IP       string `yaml:"ip"`
		Port     int    `yaml:"port"`
		ClientID string `yaml:"client_id"`
		CUID     string `yaml:"cuid"`
		BDUSS    string `yaml:"bduss"`
		SceneID  string `yaml:"scene_id"`
		AlertTTS *struct {
			Mode      string `yaml:"mode"`
			StartTime string `yaml:"start_time"`
			EndTime   string `yaml:"end_time"`
			Timezone  string `yaml:"timezone"`
			MinIssues int    `yaml:"min_issues"`
		} `yaml:"alert_tts"`
		Probe *struct {
			Enabled         bool `yaml:"enabled"`
			IntervalSeconds int  `yaml:"interval_seconds"`
			NotifyTelegram  bool `yaml:"notify_telegram"`
		} `yaml:"probe"`
		BDUSSCheck *struct {
			Enabled         bool `yaml:"enabled"`
			IntervalSeconds int  `yaml:"interval_seconds"`
			NotifyTelegram  bool `yaml:"notify_telegram"`
		} `yaml:"bduss_check"`
	} `yaml:"xiaodu"`
	Mihomo *struct {
		Enabled   bool   `yaml:"enabled"`
		LatencyTestURL   string `yaml:"latency_test_url"`
		LatencyTimeoutMS int    `yaml:"latency_timeout_ms"`
		// legacy single config
		APIBase  string `yaml:"api_base"`
		Secret   string `yaml:"secret"`
		Selector string `yaml:"selector"`
		Instances []struct {
			Name     string `yaml:"name"`
			APIBase  string `yaml:"api_base"`
			Secret   string `yaml:"secret"`
			Selector string `yaml:"selector"`
		} `yaml:"instances"`
	} `yaml:"mihomo"`
	Calendar *struct {
		Enabled               bool   `yaml:"enabled"`
		Provider              string `yaml:"provider"`
		Timezone              string `yaml:"timezone"`
		CalendarID            string `yaml:"calendar_id"`
		Proxy                 string `yaml:"proxy"`
		GoogleClientID        string `yaml:"google_client_id"`
		GoogleClientSecret    string `yaml:"google_client_secret"`
		GoogleRefreshToken    string `yaml:"google_refresh_token"`
		MicrosoftTenantID     string `yaml:"microsoft_tenant_id"`
		MicrosoftClientID     string `yaml:"microsoft_client_id"`
		MicrosoftClientSecret string `yaml:"microsoft_client_secret"`
		MicrosoftRefreshToken string `yaml:"microsoft_refresh_token"`
	} `yaml:"calendar"`
	LogDir      string `yaml:"log_dir"`
	LogFile     string `yaml:"log_file"`
	HistoryFile string `yaml:"history_file"`
	LogLevel    string `yaml:"log_level"`
}

func validateCalendar(c *CalendarConfig) error {
	if c == nil || !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Timezone) == "" {
		return fmt.Errorf("calendar: timezone is required when calendar is enabled")
	}
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	if p != string(CalendarGoogle) && p != string(CalendarMicrosoft) {
		return fmt.Errorf("calendar: invalid provider %q (use google or microsoft)", c.Provider)
	}
	switch CalendarProvider(p) {
	case CalendarGoogle:
		if strings.TrimSpace(c.GoogleClientID) == "" || strings.TrimSpace(c.GoogleClientSecret) == "" || strings.TrimSpace(c.GoogleRefreshToken) == "" {
			return fmt.Errorf("calendar: google_client_id, google_client_secret, and google_refresh_token are required")
		}
	case CalendarMicrosoft:
		if strings.TrimSpace(c.MicrosoftTenantID) == "" || strings.TrimSpace(c.MicrosoftClientID) == "" ||
			strings.TrimSpace(c.MicrosoftClientSecret) == "" || strings.TrimSpace(c.MicrosoftRefreshToken) == "" {
			return fmt.Errorf("calendar: microsoft_tenant_id, microsoft_client_id, microsoft_client_secret, and microsoft_refresh_token are required")
		}
	}
	return nil
}

// LoadConfig reads and validates a YAML config file.
func LoadConfig(path string) (*MonitorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw rawYAML
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	cfg := &MonitorConfig{
		MinSpeedMbps:           raw.MinSpeedMbps,
		CheckIntervalSeconds:   raw.CheckIntervalSeconds,
		RecheckIntervalSeconds: raw.RecheckIntervalSeconds,
		LogDir:                 orDefault(raw.LogDir, "logs"),
		LogFile:                orDefault(raw.LogFile, "switch_monitor.log"),
		HistoryFile:            raw.HistoryFile,
		LogLevel:               strings.ToUpper(orDefault(raw.LogLevel, "INFO")),
		AlertEmails:            parseEmailList(raw.AlertEmail, raw.AlertEmails),
	}
	if cfg.MinSpeedMbps == 0 {
		cfg.MinSpeedMbps = 1000
	}
	if cfg.CheckIntervalSeconds == 0 {
		cfg.CheckIntervalSeconds = 60
	}
	if cfg.RecheckIntervalSeconds == 0 {
		cfg.RecheckIntervalSeconds = 5
	}

	for _, e := range raw.Switches {
		st := SwitchType(e.Type)
		if st != TypeNetgearGS108Ev3 && st != TypeMercurySG108Pro {
			return nil, fmt.Errorf("invalid switch type %q for switch %q", e.Type, e.Name)
		}
		ports, err := parsePorts(e.ConcernedPorts)
		if err != nil {
			return nil, fmt.Errorf("concerned_ports for switch %q: %w", e.Name, err)
		}
		aliases := make(map[int]string)
		for k, v := range e.PortAliases {
			var n int
			if _, err2 := fmt.Sscan(k, &n); err2 == nil {
				aliases[n] = v
			}
		}
		username := e.Username
		if st == TypeNetgearGS108Ev3 {
			username = ""
		}
		cfg.Switches = append(cfg.Switches, SwitchConfig{
			Name:           e.Name,
			AdminURL:       e.AdminURL,
			Type:           st,
			ConcernedPorts: ports,
			Password:       e.Password,
			Username:       username,
			PortAliases:    aliases,
		})
	}

	if raw.SMTP != nil {
		cfg.SMTP = &SMTPConfig{
			Enabled:   raw.SMTP.Enabled,
			Host:      raw.SMTP.Host,
			Port:      raw.SMTP.Port,
			UseTLS:    raw.SMTP.UseTLS,
			FromEmail: raw.SMTP.FromEmail,
			User:      raw.SMTP.User,
			Password:  raw.SMTP.Password,
		}
	}

	if raw.Telegram != nil {
		tg := &TelegramConfig{
			Enabled:        raw.Telegram.Enabled,
			ListenCommands: raw.Telegram.ListenCommands,
		}
		// Collect recipients from list first, then fall back to single legacy fields.
		for _, r := range raw.Telegram.Recipients {
			if r.Token != "" && r.ChatID != "" {
				tg.Recipients = append(tg.Recipients, TelegramRecipient{
					Token:  r.Token,
					ChatID: r.ChatID,
					Proxy:  r.Proxy,
				})
			}
		}
		if len(tg.Recipients) == 0 && raw.Telegram.Token != "" && raw.Telegram.ChatID != "" {
			tg.Recipients = append(tg.Recipients, TelegramRecipient{
				Token:  raw.Telegram.Token,
				ChatID: raw.Telegram.ChatID,
				Proxy:  raw.Telegram.Proxy,
			})
		}
		cfg.Telegram = tg
	}

	if raw.Ikuai != nil {
		cfg.Ikuai = &IkuaiConfig{
			Enabled:  raw.Ikuai.Enabled,
			URL:      raw.Ikuai.URL,
			Username: raw.Ikuai.Username,
			Password: raw.Ikuai.Password,
		}
	}

	if raw.Xiaodu != nil {
		x := &XiaoduConfig{
			Enabled:  raw.Xiaodu.Enabled,
			IP:       strings.TrimSpace(raw.Xiaodu.IP),
			Port:     raw.Xiaodu.Port,
			ClientID: strings.TrimSpace(raw.Xiaodu.ClientID),
			CUID:     strings.TrimSpace(raw.Xiaodu.CUID),
			BDUSS:    strings.TrimSpace(raw.Xiaodu.BDUSS),
			SceneID:  strings.TrimSpace(raw.Xiaodu.SceneID),
		}
		if raw.Xiaodu.AlertTTS != nil {
			x.AlertTTS = &XiaoduAlertTTSConfig{
				Mode:      strings.TrimSpace(raw.Xiaodu.AlertTTS.Mode),
				StartTime: strings.TrimSpace(raw.Xiaodu.AlertTTS.StartTime),
				EndTime:   strings.TrimSpace(raw.Xiaodu.AlertTTS.EndTime),
				Timezone:  strings.TrimSpace(raw.Xiaodu.AlertTTS.Timezone),
				MinIssues: raw.Xiaodu.AlertTTS.MinIssues,
			}
		}
		if raw.Xiaodu.Probe != nil {
			x.Probe = &XiaoduProbeConfig{
				Enabled:         raw.Xiaodu.Probe.Enabled,
				IntervalSeconds: raw.Xiaodu.Probe.IntervalSeconds,
				NotifyTelegram:  raw.Xiaodu.Probe.NotifyTelegram,
			}
		}
		if raw.Xiaodu.BDUSSCheck != nil {
			x.BDUSSCheck = &XiaoduBDUSSCheckConfig{
				Enabled:         raw.Xiaodu.BDUSSCheck.Enabled,
				IntervalSeconds: raw.Xiaodu.BDUSSCheck.IntervalSeconds,
				NotifyTelegram:  raw.Xiaodu.BDUSSCheck.NotifyTelegram,
			}
		}
		cfg.Xiaodu = x
	}

	if raw.Mihomo != nil {
		cfg.Mihomo = &MihomoConfig{
			Enabled:          raw.Mihomo.Enabled,
			LatencyTestURL:   strings.TrimSpace(raw.Mihomo.LatencyTestURL),
			LatencyTimeoutMS: raw.Mihomo.LatencyTimeoutMS,
		}
		for _, inst := range raw.Mihomo.Instances {
			if strings.TrimSpace(inst.APIBase) != "" {
				name := strings.TrimSpace(inst.Name)
				if name == "" {
					name = "mihomo"
				}
				cfg.Mihomo.Instances = append(cfg.Mihomo.Instances, MihomoInstance{
					Name:     name,
					APIBase:  strings.TrimSpace(inst.APIBase),
					Secret:   strings.TrimSpace(inst.Secret),
					Selector: strings.TrimSpace(inst.Selector),
				})
			}
		}
		// Fallback to legacy fields if instances list is empty
		if len(cfg.Mihomo.Instances) == 0 && strings.TrimSpace(raw.Mihomo.APIBase) != "" {
			cfg.Mihomo.Instances = append(cfg.Mihomo.Instances, MihomoInstance{
				Name:     "default",
				APIBase:  strings.TrimSpace(raw.Mihomo.APIBase),
				Secret:   strings.TrimSpace(raw.Mihomo.Secret),
				Selector: strings.TrimSpace(raw.Mihomo.Selector),
			})
		}
	}

	if raw.Calendar != nil {
		cfg.Calendar = &CalendarConfig{
			Enabled:               raw.Calendar.Enabled,
			Provider:              strings.TrimSpace(raw.Calendar.Provider),
			Timezone:              strings.TrimSpace(raw.Calendar.Timezone),
			CalendarID:            strings.TrimSpace(raw.Calendar.CalendarID),
			Proxy:                 strings.TrimSpace(raw.Calendar.Proxy),
			GoogleClientID:        strings.TrimSpace(raw.Calendar.GoogleClientID),
			GoogleClientSecret:    strings.TrimSpace(raw.Calendar.GoogleClientSecret),
			GoogleRefreshToken:    strings.TrimSpace(raw.Calendar.GoogleRefreshToken),
			MicrosoftTenantID:     strings.TrimSpace(raw.Calendar.MicrosoftTenantID),
			MicrosoftClientID:     strings.TrimSpace(raw.Calendar.MicrosoftClientID),
			MicrosoftClientSecret: strings.TrimSpace(raw.Calendar.MicrosoftClientSecret),
			MicrosoftRefreshToken: strings.TrimSpace(raw.Calendar.MicrosoftRefreshToken),
		}
	}

	if err := validateCalendar(cfg.Calendar); err != nil {
		return nil, err
	}

	return cfg, nil
}

func parsePorts(v interface{}) ([]int, error) {
	switch val := v.(type) {
	case int:
		return []int{val}, nil
	case []interface{}:
		var ports []int
		for _, p := range val {
			switch pv := p.(type) {
			case int:
				ports = append(ports, pv)
			default:
				return nil, fmt.Errorf("unexpected port value type %T", p)
			}
		}
		return ports, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected concerned_ports type %T", v)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// parseEmailList merges a legacy single alert_email and the alert_emails list,
// deduplicating and ignoring blank entries.
func parseEmailList(single string, raw interface{}) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(single)
	switch v := raw.(type) {
	case string:
		add(v)
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}
