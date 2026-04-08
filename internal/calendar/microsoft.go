// Microsoft Graph (Outlook) backend for repair event upsert.
package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"switch-monitor/internal/config"
)

type msRepair struct {
	cfg    *config.CalendarConfig
	loc    *time.Location
	client *http.Client
}

func newMicrosoft(cfg *config.CalendarConfig, loc *time.Location) (Upserter, error) {
	base, err := HTTPClient(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	oauthCtx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.MicrosoftClientID,
		ClientSecret: cfg.MicrosoftClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", cfg.MicrosoftTenantID),
		},
		Scopes: []string{"https://graph.microsoft.com/Calendars.ReadWrite", "offline_access"},
	}
	ts := oauthCfg.TokenSource(oauthCtx, &oauth2.Token{RefreshToken: cfg.MicrosoftRefreshToken})
	client := oauth2.NewClient(oauthCtx, ts)
	return &msRepair{cfg: cfg, loc: loc, client: client}, nil
}

type graphBody struct {
	ContentType string `json:"contentType"`
	Content     string `json:"content"`
}

type graphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

type graphEventPayload struct {
	Subject    string        `json:"subject"`
	Body       graphBody     `json:"body"`
	Start      graphDateTime `json:"start"`
	End        graphDateTime `json:"end"`
	Categories []string      `json:"categories"`
}

type graphEvent struct {
	ID         string   `json:"id"`
	Categories []string `json:"categories"`
}

type graphListResponse struct {
	Value []graphEvent `json:"value"`
}

func (m *msRepair) UpsertRepairEvent(ctx context.Context, now time.Time, description string) error {
	dayStart, dayEnd := dayBounds(m.loc, now)
	title := fmt.Sprintf("[Switch Monitor] Repair — %s", now.In(m.loc).Format("2006-01-02"))
	rs, re := repairWindow(m.loc, now)
	tz := m.loc.String()

	listURL := m.calendarViewURL(dayStart, dayEnd)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("microsoft graph list: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("microsoft graph list: %s: %s", resp.Status, truncateForErr(body))
	}
	var list graphListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("microsoft graph list decode: %w", err)
	}

	var existingID string
	for _, ev := range list.Value {
		for _, c := range ev.Categories {
			if c == msCategory {
				existingID = ev.ID
				break
			}
		}
		if existingID != "" {
			break
		}
	}

	payload := graphEventPayload{
		Subject: title,
		Body: graphBody{
			ContentType: "text",
			Content:     description,
		},
		Start: graphDateTime{
			DateTime: formatLocalGraphDateTime(rs),
			TimeZone: tz,
		},
		End: graphDateTime{
			DateTime: formatLocalGraphDateTime(re),
			TimeZone: tz,
		},
		Categories: []string{msCategory},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	if existingID != "" {
		u := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/events/%s", url.PathEscape(existingID))
		req2, err := http.NewRequestWithContext(ctx, http.MethodPatch, u, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := m.client.Do(req2)
		if err != nil {
			return fmt.Errorf("microsoft graph patch: %w", err)
		}
		defer resp2.Body.Close()
		rb, _ := io.ReadAll(resp2.Body)
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return fmt.Errorf("microsoft graph patch: %s: %s", resp2.Status, truncateForErr(rb))
		}
		return nil
	}

	postURL := m.createEventURL()
	req3, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := m.client.Do(req3)
	if err != nil {
		return fmt.Errorf("microsoft graph insert: %w", err)
	}
	defer resp3.Body.Close()
	rb, _ := io.ReadAll(resp3.Body)
	if resp3.StatusCode < 200 || resp3.StatusCode >= 300 {
		return fmt.Errorf("microsoft graph insert: %s: %s", resp3.Status, truncateForErr(rb))
	}
	return nil
}

func (m *msRepair) calendarViewURL(dayStart, dayEnd time.Time) string {
	q := url.Values{}
	q.Set("startDateTime", dayStart.Format(time.RFC3339))
	q.Set("endDateTime", dayEnd.Format(time.RFC3339))
	base := "https://graph.microsoft.com/v1.0/me/calendar/calendarView"
	if strings.TrimSpace(m.cfg.CalendarID) != "" {
		base = fmt.Sprintf("https://graph.microsoft.com/v1.0/me/calendars/%s/calendarView", url.PathEscape(m.cfg.CalendarID))
	}
	return base + "?" + q.Encode()
}

func (m *msRepair) createEventURL() string {
	if strings.TrimSpace(m.cfg.CalendarID) != "" {
		return fmt.Sprintf("https://graph.microsoft.com/v1.0/me/calendars/%s/events", url.PathEscape(m.cfg.CalendarID))
	}
	return "https://graph.microsoft.com/v1.0/me/calendar/events"
}

// Graph accepts fractional seconds; use a stable local wall-clock string without zone suffix.
func formatLocalGraphDateTime(t time.Time) string {
	return t.Format("2006-01-02T15:04:05")
}

func truncateForErr(b []byte) string {
	s := string(b)
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}
