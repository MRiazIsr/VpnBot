package health

import "testing"

func TestStatusString(t *testing.T) {
	cases := map[Status]string{StatusOK: "OK", StatusDegradation: "DEGRADATION", StatusDown: "DOWN"}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}
