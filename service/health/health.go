// Package health detects when inbounds/telemt stop passing connections
// and computes OK/DEGRADATION/DOWN transitions. Pure: no Telegram/network.
package health

import "time"

type Status int

const (
	StatusOK Status = iota
	StatusDegradation
	StatusDown
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusDegradation:
		return "DEGRADATION"
	case StatusDown:
		return "DOWN"
	default:
		return "UNKNOWN"
	}
}

// SignalResult is one service's observation for one cycle.
type SignalResult struct {
	Service string // stable id, e.g. "inbound:2056" or "telemt"
	Label   string // human name for messages, e.g. "VLESS 2056 (xhttp-vk)"
	Bad     bool   // connections are NOT passing (active failure / unreachable)
	Partial bool   // one leg up, the other down (early warning)
	Reason  string // human reason, e.g. "RuVDS-цепочка недоступна"
}

// ServiceState is the remembered state between cycles.
type ServiceState struct {
	Status     Status
	FailStreak int
	Reason     string
	Since      time.Time
}

// Snapshot maps Service id -> state.
type Snapshot map[string]ServiceState

// Transition is an emitted state change (one per change, deduped).
type Transition struct {
	Service string
	Label   string
	From    Status
	To      Status
	Reason  string
	Since   time.Time
}

// Config holds tuning knobs.
type Config struct {
	DownHysteresis int // consecutive bad cycles required to confirm DOWN
}
