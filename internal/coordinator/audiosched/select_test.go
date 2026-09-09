package audiosched

import (
	"errors"
	"strings"
	"testing"
)

const ms = int64(1_000_000)

func clockHolder(nowNs int64) Readiness {
	return Readiness{NodeID: "node-a", HoldsMediaClock: true, MediaClockValid: true, MediaClockNowNs: nowNs}
}

// TestSelectSendsOneInstantDerivedFromTheClockHolder pins the property the
// whole seam exists for: one instant, derived from the node that holds the
// clock, with the lead time being exactly the terms that went into it.
func TestSelectSendsOneInstantDerivedFromTheClockHolder(t *testing.T) {
	const now = int64(1_789_012_345_678_901_234)
	sel, err := Select([]Readiness{
		clockHolder(now),
		{NodeID: "node-b"},
	}, 2000, 1000)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.ClockNodeID != "node-a" {
		t.Errorf("ClockNodeID = %q, want node-a", sel.ClockNodeID)
	}
	wantLead := 3000 * ms
	if sel.LeadNs != wantLead {
		t.Errorf("LeadNs = %d, want %d", sel.LeadNs, wantLead)
	}
	if sel.ScheduledAtNs != now+wantLead {
		t.Errorf("ScheduledAtNs = %d, want %d", sel.ScheduledAtNs, now+wantLead)
	}
}

// TestSelectPreservesNanosecondExactness proves the chosen instant is not
// rounded anywhere in this package. A UnixNano-scale reading is past
// float64's exact integer range, so a selection that had gone through a
// float64 would come back with its bottom digits changed.
func TestSelectPreservesNanosecondExactness(t *testing.T) {
	const now = int64(1_789_012_345_678_901_233) // odd, and unrepresentable as an exact float64
	sel, err := Select([]Readiness{clockHolder(now)}, 0, 0)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.ScheduledAtNs != now {
		t.Fatalf("ScheduledAtNs = %d, want exactly %d: the instant was rounded", sel.ScheduledAtNs, now)
	}
}

// TestSelectRefusesWhenTheClockHolderHasNoValidReading is the rule that
// matters most: an invalid reading is never used, and its NowNs (which is
// commonly zero) is never mistaken for an instant.
func TestSelectRefusesWhenTheClockHolderHasNoValidReading(t *testing.T) {
	_, err := Select([]Readiness{{
		NodeID: "node-a", HoldsMediaClock: true,
		MediaClockValid: false, MediaClockReason: "provider is not locked",
		MediaClockNowNs: 0,
	}}, 2000, 1000)

	var noClock *ErrNoUsableClock
	if !errors.As(err, &noClock) {
		t.Fatalf("err = %v, want *ErrNoUsableClock", err)
	}
	if !strings.Contains(err.Error(), "provider is not locked") {
		t.Errorf("err = %q, want the node's own reason carried through", err)
	}
}

// TestSelectNeverBorrowsAnotherNodesClock proves a valid reading on a node
// that does NOT hold the shared clock is not silently substituted: the
// readings are on different clocks, and borrowing one would hide exactly
// the misconfiguration this seam exposes.
func TestSelectNeverBorrowsAnotherNodesClock(t *testing.T) {
	_, err := Select([]Readiness{
		{NodeID: "node-a", HoldsMediaClock: true, MediaClockValid: false, MediaClockReason: "no provider"},
		{NodeID: "node-b", HoldsMediaClock: false, MediaClockValid: true, MediaClockNowNs: 999},
	}, 0, 0)

	var noClock *ErrNoUsableClock
	if !errors.As(err, &noClock) {
		t.Fatalf("err = %v, want *ErrNoUsableClock", err)
	}
	if strings.Contains(err.Error(), "node-b") {
		t.Errorf("err = %q, want no mention of the non-holder's reading", err)
	}
}

func TestSelectRefusesWhenNoTargetHoldsTheClock(t *testing.T) {
	_, err := Select([]Readiness{{NodeID: "node-a"}, {NodeID: "node-b"}}, 0, 0)
	var noClock *ErrNoUsableClock
	if !errors.As(err, &noClock) {
		t.Fatalf("err = %v, want *ErrNoUsableClock", err)
	}
}

// TestSelectAddsAKnownErrorBoundAndNotAnUnknownOne is the manager's
// explicit rule: unknown means unknown, never zero.
func TestSelectAddsAKnownErrorBoundAndNotAnUnknownOne(t *testing.T) {
	const now = int64(5_000_000_000)

	known := clockHolder(now)
	known.ErrorBoundKnown = true
	known.ErrorBoundNs = 250_000
	withBound, err := Select([]Readiness{known}, 10, 10)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if withBound.ScheduledAtNs != now+20*ms+250_000 {
		t.Errorf("ScheduledAtNs = %d, want the known bound folded into the lead", withBound.ScheduledAtNs)
	}
	if !withBound.ClockErrorBoundKnown || withBound.ClockErrorBoundNs != 250_000 {
		t.Errorf("bound reported as known=%v ns=%d, want true/250000", withBound.ClockErrorBoundKnown, withBound.ClockErrorBoundNs)
	}

	// An unknown bound contributes nothing AND says it contributed
	// nothing, so a caller cannot mistake it for an accounted-for zero.
	unknown := clockHolder(now)
	unknown.ErrorBoundKnown = false
	unknown.ErrorBoundNs = 999_999 // present but not known: must be ignored
	withoutBound, err := Select([]Readiness{unknown}, 10, 10)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if withoutBound.ScheduledAtNs != now+20*ms {
		t.Errorf("ScheduledAtNs = %d, want no allowance for an unknown bound", withoutBound.ScheduledAtNs)
	}
	if withoutBound.ClockErrorBoundKnown || withoutBound.ClockErrorBoundNs != 0 {
		t.Errorf("bound reported as known=%v ns=%d, want false/0", withoutBound.ClockErrorBoundKnown, withoutBound.ClockErrorBoundNs)
	}
}

// TestSelectCoversTheSlowestReportedPreroll proves the instant is not
// named before the slowest target could be playing at it, and that a
// target reporting no preroll contributes nothing rather than a zero.
func TestSelectCoversTheSlowestReportedPreroll(t *testing.T) {
	const now = int64(1_000_000_000)
	sel, err := Select([]Readiness{
		clockHolder(now),
		{NodeID: "node-b", PrerollKnown: true, PrerollMs: 120},
		{NodeID: "node-c", PrerollKnown: true, PrerollMs: 45},
		{NodeID: "node-d"}, // reported none
	}, 100, 0)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.PrerollNs != 120*ms {
		t.Errorf("PrerollNs = %d, want the slowest reported preroll (120 ms)", sel.PrerollNs)
	}
	if sel.PrerollReportedBy != 2 {
		t.Errorf("PrerollReportedBy = %d, want 2: node-d reported none and must not count as a zero", sel.PrerollReportedBy)
	}
	if sel.ScheduledAtNs != now+220*ms {
		t.Errorf("ScheduledAtNs = %d, want now + 120 ms preroll + 100 ms bound", sel.ScheduledAtNs)
	}
}

func TestSelectRefusesEmptyTargetsAndNegativeSettings(t *testing.T) {
	if _, err := Select(nil, 2000, 1000); err == nil {
		t.Error("Select(nil) = nil error, want a refusal")
	}
	if _, err := Select([]Readiness{clockHolder(1)}, -1, 0); err == nil {
		t.Error("negative delivery bound accepted, want a refusal")
	}
	if _, err := Select([]Readiness{clockHolder(1)}, 0, -1); err == nil {
		t.Error("negative margin accepted, want a refusal")
	}
}

func TestDescribeUnscheduledNeverReadsAsSuccess(t *testing.T) {
	got := DescribeUnscheduled(&ErrNoUsableClock{Reason: "provider is not locked"})
	if !strings.Contains(got, "NOT aligned") || !strings.Contains(got, "provider is not locked") {
		t.Errorf("DescribeUnscheduled = %q, want it to say plainly that the start was not aligned, and why", got)
	}
}
