// Package alerting sends email summaries via SMTP.
package alerting

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strings"
	"time"

	"switch-monitor/internal/checker"
	"switch-monitor/internal/config"
	"switch-monitor/internal/telegram"
)

// AlertService sends alerts via configured channels (SMTP, Telegram).
type AlertService struct {
	smtp     *config.SMTPConfig
	toEmails []string
	telegram *config.TelegramConfig
}

// New creates an AlertService.
func New(smtpCfg *config.SMTPConfig, toEmails []string, tgCfg *config.TelegramConfig) *AlertService {
	return &AlertService{
		smtp:     smtpCfg,
		toEmails: toEmails,
		telegram: tgCfg,
	}
}

// BuildSummaryBody returns the plain-text body used for email and calendar descriptions.
func BuildSummaryBody(
	alertParts []string,
	events []checker.AlertEvent,
	portAliasesBySwitch map[string]map[int]string,
) string {
	var body strings.Builder
	body.WriteString("PORT STATUS\n")
	body.WriteString(strings.Repeat("=", 40))
	body.WriteString("\n\n")
	body.WriteString(strings.Join(alertParts, "\n\n"))
	body.WriteString("\n")
	body.WriteString(strings.Repeat("=", 40))

	if len(events) > 0 {
		fmt.Fprintf(&body, "\nIssues (%d):\n", len(events))
		for _, e := range events {
			var reason string
			if e.Reason == checker.ReasonDown {
				reason = "DOWN"
			} else if e.SpeedMbps != nil {
				reason = fmt.Sprintf("LOW SPEED (%d Mbps)", *e.SpeedMbps)
			} else {
				reason = "LOW SPEED"
			}
			portLabel := fmt.Sprintf("port %d", e.PortID)
			if aliases, ok := portAliasesBySwitch[e.SwitchName]; ok {
				if alias, ok2 := aliases[e.PortID]; ok2 && alias != "" {
					portLabel = fmt.Sprintf("port %d (%s)", e.PortID, alias)
				}
			}
			fmt.Fprintf(&body, "  - %s %s: %s\n", e.SwitchName, portLabel, reason)
		}
	}
	return body.String()
}

// SendSummary sends one summary alert with the status table and list of issues.
func (s *AlertService) SendSummary(
	isManual bool,
	alertParts []string,
	events []checker.AlertEvent,
	portAliasesBySwitch map[string]map[int]string,
) error {
	var subject string
	if len(events) > 0 {
		subject = fmt.Sprintf("⚠️ [Switch Monitor] Summary: %d issue(s)", len(events))
	} else if isManual {
		subject = "✅ [Switch Monitor] Manual Check: All OK"
	} else {
		subject = "✅ [Switch Monitor] Summary: All OK"
	}
	bodyText := BuildSummaryBody(alertParts, events, portAliasesBySwitch)

	var errs []string

	if s.smtp != nil && s.smtp.Enabled && len(s.toEmails) > 0 {
		for _, addr := range s.toEmails {
			if err := s.sendEmail(subject, bodyText, addr); err != nil {
				errs = append(errs, fmt.Sprintf("email(%s): %v", addr, err))
			}
		}
	}

	if s.telegram != nil && s.telegram.Enabled && len(s.telegram.Recipients) > 0 {
		tgHTML := buildTelegramSummaryHTML(subject, alertParts, events, portAliasesBySwitch)
		for _, r := range s.telegram.Recipients {
			if err := s.sendTelegramHTML(tgHTML, r); err != nil {
				errs = append(errs, fmt.Sprintf("telegram(%s): %v", r.ChatID, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("alert failures: %s", strings.Join(errs, "; "))
	}
	return nil
}

// tgSwitchLinePrefix matches runner alertParts lines: "🔌 <switch-name>".
const tgSwitchLinePrefix = "🔌 "

// buildTelegramSummaryHTML formats the summary like DNAT: monospace <pre> tables so columns align in Telegram.
func buildTelegramSummaryHTML(
	subject string,
	alertParts []string,
	events []checker.AlertEvent,
	portAliasesBySwitch map[string]map[int]string,
) string {
	var b strings.Builder
	b.WriteString("<b>")
	b.WriteString(html.EscapeString(subject))
	b.WriteString("</b>")
	if len(events) > 0 {
		fmt.Fprintf(&b, "\n\n<b>Issues: %d</b>", len(events))
	}
	b.WriteString("\n\n")

	for i, part := range alertParts {
		if i > 0 {
			b.WriteString("\n\n")
		}
		title, table, ok := strings.Cut(part, "\n")
		if !ok {
			b.WriteString(html.EscapeString(part))
			continue
		}
		if strings.HasPrefix(title, tgSwitchLinePrefix) {
			name := strings.TrimSpace(strings.TrimPrefix(title, tgSwitchLinePrefix))
			b.WriteString(tgSwitchLinePrefix)
			b.WriteString("<b>")
			b.WriteString(html.EscapeString(name))
			b.WriteString("</b>\n<pre>")
			b.WriteString(html.EscapeString(table))
			b.WriteString("</pre>")
		} else {
			b.WriteString(html.EscapeString(part))
		}
	}

	if len(events) > 0 {
		b.WriteString("\n\n<b>Issue Details:</b>\n<pre>")
		var detail strings.Builder
		for _, e := range events {
			var reason string
			if e.Reason == checker.ReasonDown {
				reason = "DOWN"
			} else if e.SpeedMbps != nil {
				reason = fmt.Sprintf("LOW SPEED (%d Mbps)", *e.SpeedMbps)
			} else {
				reason = "LOW SPEED"
			}
			portLabel := fmt.Sprintf("port %d", e.PortID)
			if aliases, ok := portAliasesBySwitch[e.SwitchName]; ok {
				if alias, ok2 := aliases[e.PortID]; ok2 && alias != "" {
					portLabel = fmt.Sprintf("port %d (%s)", e.PortID, alias)
				}
			}
			fmt.Fprintf(&detail, "  - %s %s: %s\n", e.SwitchName, portLabel, reason)
		}
		b.WriteString(html.EscapeString(detail.String()))
		b.WriteString("</pre>")
	}

	return b.String()
}

func (s *AlertService) sendTelegramHTML(htmlMsg string, r config.TelegramRecipient) error {
	client, err := telegram.NewClient(r.Token, r.Proxy)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return client.SendMessageHTML(ctx, r.ChatID, htmlMsg)
}

// sendEmail connects to SMTP, sends one message to toAddr, and disconnects.
func (s *AlertService) sendEmail(subject, body, toAddr string) error {
	fromAddr := s.smtp.User
	if fromAddr == "" {
		fromAddr = s.smtp.FromEmail
	}

	msg := buildMessage(fromAddr, toAddr, subject, body)
	addr := fmt.Sprintf("%s:%d", s.smtp.Host, s.smtp.Port)

	var auth smtp.Auth
	if s.smtp.User != "" && s.smtp.Password != "" {
		auth = smtp.PlainAuth("", s.smtp.User, s.smtp.Password, s.smtp.Host)
	}

	if s.smtp.Port == 465 {
		return s.sendSSL(addr, auth, fromAddr, toAddr, msg)
	}
	return s.sendSTARTTLS(addr, auth, fromAddr, toAddr, msg, s.smtp.UseTLS)
}

func (s *AlertService) sendSSL(addr string, auth smtp.Auth, from, to string, msg []byte) error {
	tlsCfg := &tls.Config{ServerName: s.smtp.Host} //nolint:gosec
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 30 * time.Second}, "tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("SMTP SSL dial: %w", err)
	}
	client, err := smtp.NewClient(conn, s.smtp.Host)
	if err != nil {
		return fmt.Errorf("SMTP SSL new client: %w", err)
	}
	defer client.Quit() //nolint:errcheck

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP SSL auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP Mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP Rcpt: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP Data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("SMTP write body: %w", err)
	}
	return wc.Close()
}

func (s *AlertService) sendSTARTTLS(addr string, auth smtp.Auth, from, to string, msg []byte, useTLS bool) error {
	client, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP dial: %w", err)
	}
	defer client.Quit() //nolint:errcheck

	if useTLS {
		tlsCfg := &tls.Config{ServerName: s.smtp.Host} //nolint:gosec
		if err := client.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP Mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP Rcpt: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP Data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		return fmt.Errorf("SMTP write body: %w", err)
	}
	return wc.Close()
}

// buildMessage creates a minimal RFC 2822 email message.
func buildMessage(from, to, subject, body string) []byte {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}
