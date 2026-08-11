package observation

import (
	"testing"
	"time"
)

// TestDeriveHealthNeverHealthyWhenNotCurrent drives DeriveHealth over an
// observation built into every non-current State this package can produce,
// with a whenCurrent that always answers Healthy — the most permissive,
// most dangerous implementation a caller could write. DeriveHealth must
// still return Unknown for every one of them, and must never even invoke
// whenCurrent to get there. If a seventh State value existed, it would
// necessarily be "not StateCurrent" from DeriveHealth's point of view (the
// gate is `!= StateCurrent`, not an enumerated switch), so this test's
// property holds for it too without needing to be told about it — the
// cases below exist to prove that generically, not to special-case each
// one.
func TestDeriveHealthNeverHealthyWhenNotCurrent(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)

	alwaysHealthy := func(value any) Health { return HealthHealthy }
	panicsIfCalled := func(value any) Health {
		t.Fatalf("whenCurrent was called for a non-current observation")
		return HealthHealthy
	}

	nonCurrent := []struct {
		name string
		obs  Observation
	}{
		{name: "stale", obs: Observation{
			Resource: testResource(), Signal: "s", Value: int64(1),
			ObservedAt: &old, CollectedAt: now, ValidFor: time.Minute,
		}},
		{name: "unknown_age", obs: Observation{
			Resource: testResource(), Signal: "s", Value: int64(1),
			ObservedAt: nil, CollectedAt: now,
		}},
		{name: "not_collected", obs: Observation{
			Resource: testResource(), Signal: "s",
			Absence: StateNotCollected, Reason: "x", CollectedAt: now,
		}},
		{name: "collection_failed", obs: Observation{
			Resource: testResource(), Signal: "s",
			Absence: StateCollectionFailed, Reason: "x", CollectedAt: now,
		}},
		{name: "unsupported", obs: Observation{
			Resource: testResource(), Signal: "s",
			Absence: StateUnsupported, Reason: "x", CollectedAt: now,
		}},
		{name: "no value no absence (defensive not_collected)", obs: Observation{
			Resource: testResource(), Signal: "s", CollectedAt: now,
		}},
	}

	for _, tt := range nonCurrent {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveHealth(tt.obs, now, panicsIfCalled); got != HealthUnknown {
				t.Errorf("DeriveHealth() = %q, want unknown", got)
			}
			// Same assertion with an always-Healthy whenCurrent, to prove
			// the gate rejects the value on its own merits and is not
			// merely relying on panicsIfCalled never being reached.
			if got := DeriveHealth(tt.obs, now, alwaysHealthy); got != HealthUnknown {
				t.Errorf("DeriveHealth() = %q, want unknown even when whenCurrent would say healthy", got)
			}
		})
	}
}

func TestDeriveHealthCallsWhenCurrentOnlyWhenCurrent(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	o := Observation{
		Resource: testResource(), Signal: "s", Value: true,
		ObservedAt: &now, CollectedAt: now,
	}
	called := false
	got := DeriveHealth(o, now, func(value any) Health {
		called = true
		if value != true {
			t.Errorf("whenCurrent received value = %v, want true", value)
		}
		return HealthDegraded
	})
	if !called {
		t.Fatalf("whenCurrent was never called for a current observation")
	}
	if got != HealthDegraded {
		t.Errorf("DeriveHealth() = %q, want degraded (whenCurrent's answer passed through)", got)
	}
}

// TestAggregateHealthEmptyIsUnknown pins finding 1.7's fix: zero members is
// zero evidence, and ADR-011 makes "unknown" the honest answer for that,
// not the vacuously-healthy result an earlier version of AggregateHealth
// returned.
func TestAggregateHealthEmptyIsUnknown(t *testing.T) {
	if got := AggregateHealth(nil); got != HealthUnknown {
		t.Errorf("AggregateHealth(nil) = %q, want unknown", got)
	}
	if got := AggregateHealth([]AggregateMember{}); got != HealthUnknown {
		t.Errorf("AggregateHealth([]) = %q, want unknown", got)
	}
}

func TestAggregateHealthCriticalUnknownBlocksHealthy(t *testing.T) {
	members := []AggregateMember{
		{Health: HealthHealthy, Critical: false},
		{Health: HealthUnknown, Critical: true},
	}
	if got := AggregateHealth(members); got != HealthUnknown {
		t.Errorf("AggregateHealth() = %q, want unknown when a critical child is unknown", got)
	}
}

func TestAggregateHealthNonCriticalUnknownDoesNotBlockHealthy(t *testing.T) {
	members := []AggregateMember{
		{Health: HealthHealthy, Critical: true},
		{Health: HealthUnknown, Critical: false},
	}
	if got := AggregateHealth(members); got != HealthHealthy {
		t.Errorf("AggregateHealth() = %q, want healthy: only a CRITICAL unknown child should block it", got)
	}
}

func TestAggregateHealthPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		members []AggregateMember
		want    Health
	}{
		{
			name: "failed beats degraded",
			members: []AggregateMember{
				{Health: HealthDegraded, Critical: true},
				{Health: HealthFailed, Critical: true},
			},
			want: HealthFailed,
		},
		{
			name: "failed beats critical unknown",
			members: []AggregateMember{
				{Health: HealthUnknown, Critical: true},
				{Health: HealthFailed, Critical: false},
			},
			want: HealthFailed,
		},
		{
			name: "degraded beats critical unknown",
			members: []AggregateMember{
				{Health: HealthUnknown, Critical: true},
				{Health: HealthDegraded, Critical: false},
			},
			want: HealthDegraded,
		},
		{
			name: "non-critical failed still counts at full weight",
			members: []AggregateMember{
				{Health: HealthHealthy, Critical: true},
				{Health: HealthFailed, Critical: false},
			},
			want: HealthFailed,
		},
		{
			name: "all healthy is healthy",
			members: []AggregateMember{
				{Health: HealthHealthy, Critical: true},
				{Health: HealthHealthy, Critical: false},
			},
			want: HealthHealthy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AggregateHealth(tt.members); got != tt.want {
				t.Errorf("AggregateHealth() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAggregateHealthSuppressedExcludedFromComputation(t *testing.T) {
	// A suppressed critical child must not block healthy (it is a policy
	// overlay, not a severity, per AggregateHealth's doc comment) and
	// must not itself surface as the aggregate's health either.
	members := []AggregateMember{
		{Health: HealthHealthy, Critical: false},
		{Health: HealthSuppressed, Critical: true},
	}
	if got := AggregateHealth(members); got != HealthHealthy {
		t.Errorf("AggregateHealth() = %q, want healthy: a suppressed child must not count against the aggregate", got)
	}
}

// TestAggregateHealthAllSuppressedIsUnknown is finding 1.7's other named
// gap: a suppressed child is real evidence excluded from the computation by
// policy (see AggregateHealth's doc comment), but when EVERY child is
// excluded that way, nothing counted towards the result at all — the same
// "no current evidence" case the empty-list test above proves, reached by
// a different route. Before the fix this returned healthy, unguarded
// anywhere in the one caller this package has today.
func TestAggregateHealthAllSuppressedIsUnknown(t *testing.T) {
	members := []AggregateMember{
		{Health: HealthSuppressed, Critical: true},
		{Health: HealthSuppressed, Critical: true},
	}
	if got := AggregateHealth(members); got != HealthUnknown {
		t.Errorf("AggregateHealth() = %q, want unknown when every member is suppressed (no evidence counted)", got)
	}
}

// TestAggregateHealthAllNonCriticalUnknownIsUnknown is the third route to
// "nothing counted": every member is HealthUnknown and non-critical, so
// each is individually excluded by the non-critical-unknown carve-out, and
// none are HealthSuppressed. Distinct from
// TestAggregateHealthNonCriticalUnknownDoesNotBlockHealthy, which mixes in
// a genuinely healthy critical member so something DOES count; here
// nothing does, and the result must be unknown, not the healthy default an
// all-excluded loop would fall through to.
func TestAggregateHealthAllNonCriticalUnknownIsUnknown(t *testing.T) {
	members := []AggregateMember{
		{Health: HealthUnknown, Critical: false},
		{Health: HealthUnknown, Critical: false},
	}
	if got := AggregateHealth(members); got != HealthUnknown {
		t.Errorf("AggregateHealth() = %q, want unknown when every member is excluded as non-critical-unknown", got)
	}
}
