package health

import (
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	cases := map[Status]string{StatusOK: "OK", StatusDegradation: "DEGRADATION", StatusDown: "DOWN"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

func sig(service string, bad, partial bool) SignalResult {
	return SignalResult{Service: service, Label: service, Bad: bad, Partial: partial, Reason: "r"}
}

func TestEvaluate_OKStaysSilent(t *testing.T) {
	prev := Snapshot{}
	cfg := Config{DownHysteresis: 2}
	cur, tr := Evaluate(prev, []SignalResult{sig("s", false, false)}, cfg, time.Now())
	if cur["s"].Status != StatusOK {
		t.Fatalf("want OK, got %v", cur["s"].Status)
	}
	if len(tr) != 0 {
		t.Fatalf("want no transitions, got %v", tr)
	}
}

func TestEvaluate_FirstBadIsDegradationNotDown(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	cur, tr := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if cur["s"].Status != StatusDegradation {
		t.Fatalf("first bad must be DEGRADATION (M=2), got %v", cur["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDegradation || tr[0].From != StatusOK {
		t.Fatalf("want one OK->DEGRADATION transition, got %v", tr)
	}
}

func TestEvaluate_SustainedBadBecomesDownAfterHysteresis(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, tr := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if s2["s"].Status != StatusDown {
		t.Fatalf("want DOWN after 2 bad cycles, got %v", s2["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDown {
		t.Fatalf("want DEGRADATION->DOWN transition, got %v", tr)
	}
}

func TestEvaluate_DownIsDedupedNoRepeat(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, _ := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	_, tr := Evaluate(s2, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	if len(tr) != 0 {
		t.Fatalf("DOWN must not re-notify while staying DOWN, got %v", tr)
	}
}

func TestEvaluate_PartialIsImmediateDegradationEvenWithHysteresis(t *testing.T) {
	cfg := Config{DownHysteresis: 5}
	cur, tr := Evaluate(Snapshot{}, []SignalResult{sig("s", false, true)}, cfg, time.Now())
	if cur["s"].Status != StatusDegradation {
		t.Fatalf("partial must be immediate DEGRADATION, got %v", cur["s"].Status)
	}
	if len(tr) != 1 || tr[0].To != StatusDegradation {
		t.Fatalf("want OK->DEGRADATION, got %v", tr)
	}
}

func TestEvaluate_RecoveryEmitsOKTransition(t *testing.T) {
	cfg := Config{DownHysteresis: 2}
	s1, _ := Evaluate(Snapshot{}, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	s2, _ := Evaluate(s1, []SignalResult{sig("s", true, false)}, cfg, time.Now())
	_, tr := Evaluate(s2, []SignalResult{sig("s", false, false)}, cfg, time.Now())
	if len(tr) != 1 || tr[0].From != StatusDown || tr[0].To != StatusOK {
		t.Fatalf("want DOWN->OK recovery transition, got %v", tr)
	}
}
