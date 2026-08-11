package main

import (
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time.Parse(%q): %v", s, err)
	}
	return tm
}

func TestAgeAgainstNilObservedAtIsUnknown(t *testing.T) {
	serverTime := mustParse(t, "2026-08-10T21:00:00Z")
	got := ageAgainst(nil, serverTime)
	if got != "age unknown" {
		t.Errorf("ageAgainst(nil, ...) = %q, want %q (contract §3.3: never fabricate an observation time)", got, "age unknown")
	}
}

// TestAgeAgainstUsesServerTimeNotWallClock is the load-bearing test for
// task spec §3's core requirement: ages are computed against the
// response's serverTime, which this function takes as an explicit
// parameter rather than calling time.Now() itself. There is deliberately
// no path in this function that can reach the local clock at all.
func TestAgeAgainstUsesServerTimeNotWallClock(t *testing.T) {
	observedAt := mustParse(t, "2026-08-10T20:59:30Z")
	serverTime := mustParse(t, "2026-08-10T21:00:00Z")

	got := ageAgainst(&observedAt, serverTime)
	if got != "30s ago" {
		t.Errorf("ageAgainst = %q, want %q", got, "30s ago")
	}
}

func TestAgeAgainstFutureObservationIsLabelled(t *testing.T) {
	observedAt := mustParse(t, "2026-08-10T21:00:10Z")
	serverTime := mustParse(t, "2026-08-10T21:00:00Z")

	got := ageAgainst(&observedAt, serverTime)
	if !strings.Contains(got, "in the future") {
		t.Errorf("ageAgainst = %q, want it to flag observedAt after serverTime rather than print a bare negative duration", got)
	}
}

func TestStateGlyphCurrentIsBareNoOtherStateIs(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{stateCurrent, "current"},
		{stateStale, "STALE"},
		{stateUnknownAge, "AGE-UNKNOWN"},
		{stateNotCollected, "NOT-COLLECTED"},
		{stateCollectionFailed, "COLLECTION-FAILED"},
		{stateUnsupported, "UNSUPPORTED"},
	}
	for _, tc := range cases {
		got := stateGlyph(tc.state, nil)
		if got != tc.want {
			t.Errorf("stateGlyph(%q, nil) = %q, want %q", tc.state, got, tc.want)
		}
		if tc.state != stateCurrent && got == "current" {
			t.Errorf("stateGlyph(%q, ...) rendered as \"current\"; task spec §3 requires every non-current state be visibly distinguishable", tc.state)
		}
	}
}

func TestStateGlyphIncludesReason(t *testing.T) {
	reason := "MultiSync disabled on this FPP"
	got := stateGlyph(stateNotCollected, &reason)
	if !strings.Contains(got, reason) {
		t.Errorf("stateGlyph with a reason = %q, want it to include %q", got, reason)
	}
}

// TestControlPlaneColumnNeverBarelyOffline pins task spec §3's rule in
// code: the rendered string for an offline node must never be exactly
// "OFFLINE" or "offline" on its own — it must carry the "control-plane"
// qualifier every time, so an operator glancing at a column cannot read
// it as "the node/show is dead."
func TestControlPlaneColumnNeverBarelyOffline(t *testing.T) {
	reason := "no heartbeat within the staleness window"
	got := controlPlaneColumn(controlPlane{State: "offline", Reason: &reason})

	if got == "OFFLINE" || got == "offline" {
		t.Fatalf("controlPlaneColumn rendered a bare %q", got)
	}
	if !strings.Contains(got, "control-plane") {
		t.Errorf("controlPlaneColumn(%v) = %q, want it to name \"control-plane\" explicitly", "offline", got)
	}
	if !strings.Contains(strings.ToLower(got), "may still be running") {
		t.Errorf("controlPlaneColumn(%v) = %q, want it to say the show may still be running", "offline", got)
	}
}

func TestControlPlaneColumnOnline(t *testing.T) {
	got := controlPlaneColumn(controlPlane{State: "online"})
	if !strings.Contains(got, "online") {
		t.Errorf("controlPlaneColumn(online) = %q, want it to mention online", got)
	}
}

// TestClockSkewWarningFixedClock is the test task spec §3 explicitly asks
// for: "Test it with a fixed clock." No time.Now() appears anywhere in
// this test; both the local time and the server time are literals.
func TestClockSkewWarningFixedClock(t *testing.T) {
	serverTime := mustParse(t, "2026-08-10T21:00:00Z")

	t.Run("within threshold: no warning", func(t *testing.T) {
		localNow := serverTime.Add(2 * time.Second)
		if got := clockSkewWarning(serverTime, localNow); got != "" {
			t.Errorf("clockSkewWarning = %q, want empty for a 2s skew", got)
		}
	})

	t.Run("beyond threshold: warning naming both clocks", func(t *testing.T) {
		localNow := serverTime.Add(1 * time.Minute)
		got := clockSkewWarning(serverTime, localNow)
		if got == "" {
			t.Fatal("clockSkewWarning = \"\", want a warning for a 1m skew")
		}
		if !strings.Contains(got, "serverTime") {
			t.Errorf("clockSkewWarning = %q, want it to mention serverTime", got)
		}
	})

	t.Run("skew is symmetric", func(t *testing.T) {
		localNow := serverTime.Add(-1 * time.Minute)
		if got := clockSkewWarning(serverTime, localNow); got == "" {
			t.Error("clockSkewWarning = \"\", want a warning when the local clock is behind serverTime too")
		}
	})
}

func TestStringOrDash(t *testing.T) {
	if got := stringOrDash(nil); got != "-" {
		t.Errorf("stringOrDash(nil) = %q, want \"-\"", got)
	}
	s := "hello"
	if got := stringOrDash(&s); got != "hello" {
		t.Errorf("stringOrDash(&\"hello\") = %q, want \"hello\"", got)
	}
}
