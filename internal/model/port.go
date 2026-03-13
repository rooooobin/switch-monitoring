// Package model defines shared data types for the switch monitor.
package model

// PortStatus holds normalized status for a single switch port.
type PortStatus struct {
	PortID   int
	LinkUp   bool
	SpeedMbps *int // nil if down or unknown

	// Packet statistics (Mercury)
	TxOk   *int64
	TxFail *int64
	RxOk   *int64
	RxFail *int64

	// Traffic in MB since reboot (Netgear)
	TxMBytes *float64
	RxMBytes *float64
}

// IntPtr is a convenience helper.
func IntPtr(v int) *int { return &v }

// Int64Ptr is a convenience helper.
func Int64Ptr(v int64) *int64 { return &v }

// Float64Ptr is a convenience helper.
func Float64Ptr(v float64) *float64 { return &v }
