// Google Calendar backend for repair event upsert.
package calendar

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	"switch-monitor/internal/config"
)

type googleRepair struct {
	loc   *time.Location
	svc   *calendar.Service
	calID string
}

func newGoogle(cfg *config.CalendarConfig, loc *time.Location) (Upserter, error) {
	base, err := HTTPClient(cfg.Proxy)
	if err != nil {
		return nil, err
	}
	oauthCtx := context.WithValue(context.Background(), oauth2.HTTPClient, base)
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{calendar.CalendarScope},
	}
	ts := oauthCfg.TokenSource(oauthCtx, &oauth2.Token{RefreshToken: cfg.GoogleRefreshToken})
	client := oauth2.NewClient(oauthCtx, ts)
	svc, err := calendar.NewService(oauthCtx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("google calendar service: %w", err)
	}
	calID := cfg.CalendarID
	if calID == "" {
		calID = "primary"
	}
	return &googleRepair{loc: loc, svc: svc, calID: calID}, nil
}

func (g *googleRepair) UpsertRepairEvent(ctx context.Context, now time.Time, description string) error {
	dayStart, dayEnd := dayBounds(g.loc, now)
	title := fmt.Sprintf("[Switch Monitor] Repair — %s", now.In(g.loc).Format("2006-01-02"))
	rs, re := repairWindow(g.loc, now)

	list, err := g.svc.Events.List(g.calID).
		TimeMin(dayStart.Format(time.RFC3339)).
		TimeMax(dayEnd.Format(time.RFC3339)).
		SingleEvents(true).
		OrderBy("startTime").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("google calendar list: %w", err)
	}

	var existing *calendar.Event
	for _, ev := range list.Items {
		if ev.ExtendedProperties != nil && ev.ExtendedProperties.Private != nil &&
			ev.ExtendedProperties.Private[extPrivateKey] == extPrivateVal {
			existing = ev
			break
		}
	}

	ev := &calendar.Event{
		Summary:     title,
		Description: description,
		Start: &calendar.EventDateTime{
			DateTime: rs.Format(time.RFC3339),
			TimeZone: g.loc.String(),
		},
		End: &calendar.EventDateTime{
			DateTime: re.Format(time.RFC3339),
			TimeZone: g.loc.String(),
		},
		ExtendedProperties: &calendar.EventExtendedProperties{
			Private: map[string]string{extPrivateKey: extPrivateVal},
		},
	}

	if existing != nil {
		_, err = g.svc.Events.Patch(g.calID, existing.Id, ev).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("google calendar patch: %w", err)
		}
		return nil
	}
	_, err = g.svc.Events.Insert(g.calID, ev).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("google calendar insert: %w", err)
	}
	return nil
}
