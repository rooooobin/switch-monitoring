package calendar

import (
	"context"
	"time"
)

const (
	extPrivateKey = "switchMonitor"
	extPrivateVal = "repair"
	msCategory    = "SwitchMonitor.Repair"
)

// Upserter creates or updates one repair reminder event per local calendar day.
type Upserter interface {
	UpsertRepairEvent(ctx context.Context, now time.Time, description string) error
}
