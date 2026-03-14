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
	pending      map[stateKey]AlertEvent
}

// New creates a PortChecker with the given minimum speed threshold (Mbps).
func New(minSpeedMbps int) *PortChecker {
	return &PortChecker{
		minSpeedMbps: minSpeedMbps,
		last:         make(map[stateKey]model.PortStatus),
		pending:      make(map[stateKey]AlertEvent),
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

		isDown := !status.LinkUp
		isLowSpeed := status.LinkUp && status.SpeedMbps != nil && *status.SpeedMbps < c.minSpeedMbps

		if isDown {
			if !hasPrev || prev.LinkUp {
				// Transition to down. Mark pending.
				c.pending[key] = AlertEvent{
					SwitchName: switchName,
					PortID:     portID,
					Reason:     ReasonDown,
					LinkUp:     false,
				}
			} else if pendingEvent, ok := c.pending[key]; ok && pendingEvent.Reason == ReasonDown {
				// Double confirmed.
				events = append(events, pendingEvent)
				delete(c.pending, key)
			}
		} else if isLowSpeed {
			prevOK := !hasPrev || prev.SpeedMbps == nil || *prev.SpeedMbps >= c.minSpeedMbps
			if prevOK || (hasPrev && !prev.LinkUp) {
				// Transition to low speed. Mark pending.
				sp := *status.SpeedMbps
				c.pending[key] = AlertEvent{
					SwitchName: switchName,
					PortID:     portID,
					Reason:     ReasonLowSpeed,
					LinkUp:     true,
					SpeedMbps:  &sp,
				}
			} else if pendingEvent, ok := c.pending[key]; ok && pendingEvent.Reason == ReasonLowSpeed {
				// Double confirmed.
				events = append(events, pendingEvent)
				delete(c.pending, key)
			}
		} else {
			// Normal state
			delete(c.pending, key)
		}

		c.last[key] = status
	}
	return events
}

// HasAnyPending returns true if there are any pending alert events.
func (c *PortChecker) HasAnyPending() bool {
	return len(c.pending) > 0
}
