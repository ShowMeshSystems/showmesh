package readiness

import (
	"testing"
	"time"
)

type fakeSource struct {
	report Report
}

func (f fakeSource) Readiness() Report { return f.report }

func TestAggregateReadyWhenAllMembersReady(t *testing.T) {
	t1 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(5 * time.Second)

	agg := Aggregate{
		fakeSource{Report{Ready: true, ObservedAt: t2}},
		fakeSource{Report{Ready: true, ObservedAt: t1}},
	}

	got := agg.Readiness()
	if !got.Ready {
		t.Fatalf("Ready = false, want true when every member is ready")
	}
	if !got.ObservedAt.Equal(t1) {
		t.Errorf("ObservedAt = %v, want the oldest member's %v (weakest-link freshness)", got.ObservedAt, t1)
	}
}

func TestAggregateNotReadyWhenAnyMemberNotReady(t *testing.T) {
	notReady := Report{Ready: false, Reason: "mqtt broker not connected", ObservedAt: time.Now()}
	agg := Aggregate{
		fakeSource{Report{Ready: true, ObservedAt: time.Now()}},
		fakeSource{notReady},
	}

	got := agg.Readiness()
	if got.Ready {
		t.Fatalf("Ready = true, want false when a member is not ready")
	}
	if got.Reason != notReady.Reason {
		t.Errorf("Reason = %q, want the failing member's reason %q", got.Reason, notReady.Reason)
	}
}

func TestAggregateNotReadyReturnsFirstFailureVerbatim(t *testing.T) {
	// When more than one member is not ready, the first one encountered
	// wins rather than being merged with the others: a single confident
	// verdict, not an averaged one, per ADR-011.
	first := Report{Ready: false, Reason: "store unreachable", Details: map[string]any{"x": 1}}
	second := Report{Ready: false, Reason: "broker not connected"}
	agg := Aggregate{fakeSource{first}, fakeSource{second}}

	got := agg.Readiness()
	if got.Reason != first.Reason {
		t.Errorf("Reason = %q, want the first failing member's %q", got.Reason, first.Reason)
	}
	if got.Details["x"] != 1 {
		t.Errorf("Details = %v, want the first failing member's Details carried through", got.Details)
	}
}

func TestAggregateEmptyIsReady(t *testing.T) {
	// An empty Aggregate is a degenerate case (coordinator.Run always
	// supplies at least the broker and the store); document the behavior
	// rather than leave it as an accident: vacuously, every member (zero of
	// them) is ready.
	got := Aggregate{}.Readiness()
	if !got.Ready {
		t.Errorf("Ready = false for an empty Aggregate, want true (vacuous truth)")
	}
	if !got.ObservedAt.IsZero() {
		t.Errorf("ObservedAt = %v, want zero (no member evidence to report)", got.ObservedAt)
	}
}

// TestAggregateZeroObservedAtPropagatesAsWeakestLink replaces an earlier
// version of this test (TestAggregateSkipsZeroObservedAtWhenComputingOldest)
// that enshrined the opposite, and wrong, behavior: skipping a ready
// member's zero ObservedAt when computing the oldest. A ready member with
// no freshness evidence at all is the weakest possible contributor, not one
// to ignore — see Aggregate's doc comment — so the aggregate's own
// ObservedAt must come out zero too, regardless of how fresh any other
// member's evidence is, rather than reporting a freshness bound the
// zero-evidence member cannot actually support.
func TestAggregateZeroObservedAtPropagatesAsWeakestLink(t *testing.T) {
	fresh := time.Now()
	agg := Aggregate{
		fakeSource{Report{Ready: true}}, // no evidence at all, ObservedAt zero
		fakeSource{Report{Ready: true, ObservedAt: fresh}},
	}
	got := agg.Readiness()
	if !got.Ready {
		t.Fatalf("Ready = false, want true")
	}
	if !got.ObservedAt.IsZero() {
		t.Errorf("ObservedAt = %v, want zero: a member with no freshness evidence is the weakest link and must not be papered over by another member's fresher timestamp", got.ObservedAt)
	}
}

// TestAggregateZeroObservedAtPropagatesRegardlessOfMemberOrder checks the
// same rule with the zero-evidence member last, so the fix cannot
// accidentally depend on iteration order (e.g. an implementation that only
// checks the first report for zero-ness).
func TestAggregateZeroObservedAtPropagatesRegardlessOfMemberOrder(t *testing.T) {
	fresh := time.Now()
	agg := Aggregate{
		fakeSource{Report{Ready: true, ObservedAt: fresh}},
		fakeSource{Report{Ready: true}}, // no evidence at all, ObservedAt zero
	}
	got := agg.Readiness()
	if !got.Ready {
		t.Fatalf("Ready = false, want true")
	}
	if !got.ObservedAt.IsZero() {
		t.Errorf("ObservedAt = %v, want zero regardless of member order", got.ObservedAt)
	}
}
