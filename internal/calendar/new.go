package calendar

import (
	"fmt"
	"strings"
	"time"

	"switch-monitor/internal/config"
)

// NewFromConfig returns an Upserter or nil when calendar is disabled.
func NewFromConfig(c *config.CalendarConfig) (Upserter, error) {
	if c == nil || !c.Enabled {
		return nil, nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("calendar timezone: %w", err)
	}
	p := strings.ToLower(strings.TrimSpace(c.Provider))
	switch config.CalendarProvider(p) {
	case config.CalendarGoogle:
		return newGoogle(c, loc)
	case config.CalendarMicrosoft:
		return newMicrosoft(c, loc)
	default:
		return nil, fmt.Errorf("calendar: unknown provider %q", c.Provider)
	}
}
