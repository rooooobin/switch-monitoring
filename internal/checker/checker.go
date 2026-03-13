// Package checker detects port state transitions and emits alert events.
package checker

import "switch-monitor/internal/model"

// AlertReason describes why an alert was raised.
type AlertReason string

const (
	ReasonDown     AlertReason = "down"
	ReasonLowSpeed AlertReason = "low_speed"
)

// AlertEvent is one alert: port down or low link speed.
type AlertEvent struct {
	SwitchName string
	PortID     int
	Reason     AlertReason
	LinkUp     bool
	SpeedMbps  *int
}

// stateKey uniquely identifies a (switch, port) pair.
type stateKey struct {
	switchName string
	portID     int
}

// PortChecker holds last-known state and emits alerts only on transitions.
type PortChecker struct {
	minSpeedMbps int
	last         map[stateKey]model.PortStatus
}

// New creates a PortChecker with the given minimum speed threshold (Mbps).
func New(minSpeedMbps int) *PortChecker {
	return &PortChecker{
		minSpeedMbps: minSpeedMbps,
		last:         make(map[stateKey]model.PortStatus),
	}
}

// Check compares current port statuses to the previous snapshot and returns
// transition-based alert events for concerned ports only.
func (c *PortChecker) Check(switchName string, concernedPorts []int, current []model.PortStatus) []AlertEvent {
	byPort := make(map[int]model.PortStatus, len(current))
	for _, s := range current {
		byPort[s.PortID] = s
	}

	var events []AlertEvent
	for _, portID := range concernedPorts {
		status, ok := byPort[portID]
		if !ok {
			continue
		}
		key := stateKey{switchName, portID}
		prev, hasPrev := c.last[key]

		if !status.LinkUp {
			// Alert only on down transition
			if !hasPrev || prev.LinkUp {
				events = append(events, AlertEvent{
					SwitchName: switchName,
					PortID:     portID,
					Reason:     ReasonDown,
					LinkUp:     false,
				})
			}
		} else if status.SpeedMbps != nil && *status.SpeedMbps < c.minSpeedMbps {
			// Alert only on transition to low speed
			prevOK := !hasPrev || prev.SpeedMbps == nil || *prev.SpeedMbps >= c.minSpeedMbps
			if prevOK {
				sp := *status.SpeedMbps
				events = append(events, AlertEvent{
					SwitchName: switchName,
					PortID:     portID,
					Reason:     ReasonLowSpeed,
					LinkUp:     true,
					SpeedMbps:  &sp,
				})
			}
		}

		c.last[key] = status
	}
	return events
}
