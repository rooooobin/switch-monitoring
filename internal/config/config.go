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

// SwitchConfig holds per-switch settings.
type SwitchConfig struct {
	Name           string            `yaml:"name"`
	AdminURL       string            `yaml:"admin_url"`
	Type           SwitchType        `yaml:"type"`
	ConcernedPorts []int             `yaml:"concerned_ports"`
	Password       string            `yaml:"password"`
	Username       string            `yaml:"username"`
	PortAliases    map[int]string    `yaml:"port_aliases"`
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
	Enabled  bool   `yaml:"enabled"`
	Host     string `yaml:"smtp_host"`
	Port     int    `yaml:"smtp_port"`
	UseTLS   bool   `yaml:"smtp_use_tls"`
	FromEmail string `yaml:"from_email"`
	User     string `yaml:"smtp_user"`
	Password string `yaml:"smtp_password"`
}

// TelegramConfig holds settings for Telegram bot notifications.
type TelegramConfig struct {
	Enabled bool   `yaml:"enabled"`
	Token   string `yaml:"token"`
	ChatID  string `yaml:"chat_id"`
	Proxy   string `yaml:"proxy"`
}

// MonitorConfig is the top-level configuration.
type MonitorConfig struct {
	Switches              []SwitchConfig  `yaml:"switches"`
	AlertEmail            string          `yaml:"alert_email"`
	MinSpeedMbps          int             `yaml:"min_speed_mbps"`
	CheckIntervalSeconds  int             `yaml:"check_interval_seconds"`
	SMTP                  *SMTPConfig     `yaml:"smtp"`
	Telegram              *TelegramConfig `yaml:"telegram"`
	LogDir                string          `yaml:"log_dir"`
	LogFile               string          `yaml:"log_file"`
	HistoryFile           string          `yaml:"history_file"`
	LogLevel              string          `yaml:"log_level"`
}

// rawYAML mirrors the YAML structure for flexible parsing.
type rawYAML struct {
	Switches []struct {
		Name           string         `yaml:"name"`
		AdminURL       string         `yaml:"admin_url"`
		Type           string         `yaml:"type"`
		ConcernedPorts interface{}    `yaml:"concerned_ports"`
		Password       string         `yaml:"password"`
		Username       string         `yaml:"username"`
		PortAliases    map[string]string `yaml:"port_aliases"`
	} `yaml:"switches"`
	AlertEmail           string   `yaml:"alert_email"`
	MinSpeedMbps         int      `yaml:"min_speed_mbps"`
	CheckIntervalSeconds int      `yaml:"check_interval_seconds"`
	SMTP                 *struct {
		Enabled   bool   `yaml:"enabled"`
		Host      string `yaml:"smtp_host"`
		Port      int    `yaml:"smtp_port"`
		UseTLS    bool   `yaml:"smtp_use_tls"`
		FromEmail string `yaml:"from_email"`
		User      string `yaml:"smtp_user"`
		Password  string `yaml:"smtp_password"`
	} `yaml:"smtp"`
	Telegram *struct {
		Enabled bool   `yaml:"enabled"`
		Token   string `yaml:"token"`
		ChatID  string `yaml:"chat_id"`
		Proxy   string `yaml:"proxy"`
	} `yaml:"telegram"`
	LogDir      string `yaml:"log_dir"`
	LogFile     string `yaml:"log_file"`
	HistoryFile string `yaml:"history_file"`
	LogLevel    string `yaml:"log_level"`
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
		AlertEmail:           raw.AlertEmail,
		MinSpeedMbps:         raw.MinSpeedMbps,
		CheckIntervalSeconds: raw.CheckIntervalSeconds,
		LogDir:               orDefault(raw.LogDir, "logs"),
		LogFile:              orDefault(raw.LogFile, "switch_monitor.log"),
		HistoryFile:          raw.HistoryFile,
		LogLevel:             strings.ToUpper(orDefault(raw.LogLevel, "INFO")),
	}
	if cfg.MinSpeedMbps == 0 {
		cfg.MinSpeedMbps = 1000
	}
	if cfg.CheckIntervalSeconds == 0 {
		cfg.CheckIntervalSeconds = 60
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
		// Default to true if not explicitly set but host is provided
		if !raw.SMTP.Enabled && raw.SMTP.Host != "" {
			cfg.SMTP.Enabled = true
		}
	}

	if raw.Telegram != nil {
		cfg.Telegram = &TelegramConfig{
			Enabled: raw.Telegram.Enabled,
			Token:   raw.Telegram.Token,
			ChatID:  raw.Telegram.ChatID,
			Proxy:   raw.Telegram.Proxy,
		}
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
