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

// Evaluate computes the new snapshot and the transitions to notify.
// Rules: a "bad" signal increments FailStreak; first bad (or any Partial)
// is immediate DEGRADATION; DOWN only after FailStreak >= DownHysteresis.
// Good signal resets to OK. A Transition is emitted only when Status
// actually changes (dedup). Recovery (->OK) is always emitted.
func Evaluate(prev Snapshot, signals []SignalResult, cfg Config, now time.Time) (Snapshot, []Transition) {
	if cfg.DownHysteresis < 1 {
		cfg.DownHysteresis = 1
	}
	cur := make(Snapshot, len(signals))
	var transitions []Transition

	for _, sg := range signals {
		old, seen := prev[sg.Service]
		if !seen {
			old = ServiceState{Status: StatusOK}
		}

		st := ServiceState{Since: old.Since, Reason: sg.Reason}

		switch {
		case !sg.Bad && !sg.Partial:
			st.Status = StatusOK
			st.FailStreak = 0
			if old.Status == StatusOK {
				st.Reason = ""
			}
		default:
			st.FailStreak = old.FailStreak + 1
			if sg.Bad && st.FailStreak >= cfg.DownHysteresis {
				st.Status = StatusDown
			} else {
				st.Status = StatusDegradation
			}
		}

		if st.Status != old.Status {
			st.Since = now
			transitions = append(transitions, Transition{
				Service: sg.Service,
				Label:   sg.Label,
				From:    old.Status,
				To:      st.Status,
				Reason:  sg.Reason,
				Since:   st.Since,
			})
		} else if st.Since.IsZero() {
			st.Since = now
		}

		cur[sg.Service] = st
	}
	return cur, transitions
}
