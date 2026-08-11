package store

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestObservationValueRoundTripsThroughStore is the pin the Step 3 task
// spec calls for: every accepted [observation.Observation.Value] type must
// come back out of the database exactly as stored, byte for byte and type
// for type. The cases below are chosen to break the two encodings this
// package explicitly rejected — a JSON number column (loses precision
// above 2^53, cannot distinguish an integral float from an int) and a
// single untyped text column (cannot distinguish an empty string from no
// value at all) — see encodeObservationValue's doc comment in
// observations.go.
func TestObservationValueRoundTripsThroughStore(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"max int64", int64(math.MaxInt64)},
		{"negative float", -3.5},
		{"integral-valued float stays float64", float64(1920)},
		{"empty string", ""},
		{"string with newline and quote", "line one\nline \"two\""},
		{"bool true", true},
		{"bool false", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t, nil)
			ctx := context.Background()

			obs, err := observation.Measured(
				observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
				"fpp.test.value", tc.value, mustTime(t, "2026-08-10T12:00:00Z"),
			)
			if err != nil {
				t.Fatalf("build observation: %v", err)
			}
			if err := st.UpsertObservation(ctx, obs); err != nil {
				t.Fatalf("upsert observation: %v", err)
			}

			got, err := st.ListObservations(ctx, ObservationFilter{})
			if err != nil {
				t.Fatalf("list observations: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("len(got) = %d, want 1", len(got))
			}

			// reflect.DeepEqual so a type mismatch (e.g. int64(1920) coming
			// back instead of float64(1920)) fails loudly instead of
			// passing under Go's == across differently-typed interface
			// values, which itself would report false for a type
			// mismatch — DeepEqual makes the failure message legible about
			// which of value vs. type differed.
			if !reflect.DeepEqual(got[0].Value, tc.value) {
				t.Errorf("Value = %#v (%T), want %#v (%T)", got[0].Value, got[0].Value, tc.value, tc.value)
			}
		})
	}
}

// TestReopenedObservationPreservesObservedAtAndDerivesStale is the
// restart test the Step 3 task spec singles out as worth more than the
// rest of the task: an observation stored with an old ObservedAt must come
// back with that exact ObservedAt after a coordinator restart (a fresh
// Store opened against the same database file), never restamped to "now"
// by the read path. A restamping bug would not show up as a wrong value —
// it would show up as an observation that is actually 10 minutes stale
// silently reporting StateCurrent instead, which is precisely the false
// freshness ADR-011 exists to prevent.
func TestReopenedObservationPreservesObservedAtAndDerivesStale(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	observedAt := mustTime(t, "2026-08-10T12:00:00Z")
	closingClock := &fakeClock{t: observedAt.Add(10 * time.Minute)}

	st1, err := open(ctx, dir, nil, closingClock.now)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	obs, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
		"fpp.multisync.enabled", false, observedAt,
		observation.WithValidFor(15*time.Second),
	)
	if err != nil {
		t.Fatalf("build observation: %v", err)
	}
	if err := st1.UpsertObservation(ctx, obs); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen with a clock that has advanced a further 5 minutes, simulating
	// a coordinator restart well after the observation was originally
	// recorded. The store's own bookkeeping clock advancing must not touch
	// the stored evidence's ObservedAt in any way.
	reopenClock := &fakeClock{t: observedAt.Add(15 * time.Minute)}
	st2, err := open(ctx, dir, nil, reopenClock.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st2.Close() }()

	got, err := st2.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations after reopen: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}

	if got[0].ObservedAt == nil || !got[0].ObservedAt.Equal(observedAt) {
		t.Fatalf("ObservedAt = %v, want the original %v preserved across reopen, never restamped", got[0].ObservedAt, observedAt)
	}

	now := reopenClock.now()
	if state := got[0].StateAt(now); state != observation.StateStale {
		t.Errorf("StateAt(now) = %q, want %q: the observation is 15 minutes old against a 15s ValidFor", state, observation.StateStale)
	}
}

// TestUpsertObservationRejectsInvalidObservation proves UpsertObservation
// calls observation.Observation.Validate itself rather than trusting the
// caller: a hand-built Observation that violates the package's invariants
// (here, no Value and no Absence) must be rejected before it ever reaches
// SQL, not stored as some partial or defaulted row.
func TestUpsertObservationRejectsInvalidObservation(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	invalid := observation.Observation{
		Resource:    observation.ResourceRef{Kind: observation.ResourceNode, ID: "node-a"},
		Signal:      "node.test.invalid",
		CollectedAt: time.Now(),
		// Value and Absence both left empty: Validate must reject this.
	}

	err := st.UpsertObservation(ctx, invalid)
	if err == nil {
		t.Fatalf("UpsertObservation succeeded, want an error for an invalid Observation")
	}
	if !errors.Is(err, observation.ErrObservationNoValueOrAbsence) {
		t.Errorf("error = %v, want it to wrap ErrObservationNoValueOrAbsence", err)
	}

	got, err := st.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0: a rejected observation must not be stored", len(got))
	}
}

// TestUpsertObservationReplacesPreviousValue proves observations is an
// upsert target, never a history: a second UpsertObservation for the same
// (resource, signal) must replace the first, leaving exactly one row.
func TestUpsertObservationReplacesPreviousValue(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	ref := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}

	first, err := observation.Measured(ref, "fpp.multisync.enabled", false, mustTime(t, "2026-08-10T12:00:00Z"))
	if err != nil {
		t.Fatalf("build first observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}

	second, err := observation.Measured(ref, "fpp.multisync.enabled", true, mustTime(t, "2026-08-10T12:05:00Z"))
	if err != nil {
		t.Fatalf("build second observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, second); err != nil {
		t.Fatalf("upsert second: %v", err)
	}

	got, err := st.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1 (upsert, not history)", len(got))
	}
	if got[0].Value != true {
		t.Errorf("Value = %v, want the second, latest value (true)", got[0].Value)
	}
}

// TestObservationRoundTripsUnknownAge proves the retained-MQTT case (
// contract section 3.3) round-trips through this table exactly like it
// already does for node_health/node_lwt: a nil ObservedAt must come back
// nil, never some receipt time.
func TestObservationRoundTripsUnknownAge(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	obs, err := observation.MeasuredUnknownAge(
		observation.ResourceRef{Kind: observation.ResourceNode, ID: "node-a"},
		"node.agent.uptime", int64(42),
	)
	if err != nil {
		t.Fatalf("build observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, obs); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	got, err := st.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil for unknown age", *got[0].ObservedAt)
	}
	if state := got[0].StateAt(time.Now()); state != observation.StateUnknownAge {
		t.Errorf("StateAt = %q, want %q", state, observation.StateUnknownAge)
	}
}

// TestObservationRoundTripsAbsence proves an absence Observation (no
// value at all) round-trips its Absence state and Reason, never a
// fabricated Value.
func TestObservationRoundTripsAbsence(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	obs, err := observation.Unsupported(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"},
		"fpp.multisync.enabled", "FPP too old for this REST field",
	)
	if err != nil {
		t.Fatalf("build observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, obs); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	got, err := st.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Value != nil {
		t.Errorf("Value = %#v, want nil for an absence observation", got[0].Value)
	}
	if got[0].Absence != observation.StateUnsupported {
		t.Errorf("Absence = %q, want %q", got[0].Absence, observation.StateUnsupported)
	}
	if got[0].Reason == "" {
		t.Errorf("Reason is empty, want the stored reason preserved")
	}
}

// TestListObservationsFilters proves ResourceKind/ResourceID/Signal filter
// independently and in combination, and that an unfiltered call returns
// everything.
func TestListObservationsFilters(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	at := mustTime(t, "2026-08-10T12:00:00Z")

	mustUpsert := func(kind observation.ResourceKind, id string, signal observation.SignalID, value any) {
		obs, err := observation.Measured(observation.ResourceRef{Kind: kind, ID: id}, signal, value, at)
		if err != nil {
			t.Fatalf("build observation: %v", err)
		}
		if err := st.UpsertObservation(ctx, obs); err != nil {
			t.Fatalf("upsert observation: %v", err)
		}
	}

	mustUpsert(observation.ResourceFPP, "player-01", "fpp.multisync.enabled", false)
	mustUpsert(observation.ResourceFPP, "player-02", "fpp.multisync.enabled", true)
	mustUpsert(observation.ResourceNode, "node-a", "node.agent.uptime", int64(1))

	all, err := st.ListObservations(ctx, ObservationFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d, want 3", len(all))
	}

	byKind, err := st.ListObservations(ctx, ObservationFilter{ResourceKind: observation.ResourceFPP})
	if err != nil {
		t.Fatalf("list by kind: %v", err)
	}
	if len(byKind) != 2 {
		t.Errorf("len(byKind) = %d, want 2", len(byKind))
	}

	byResource, err := st.ListObservations(ctx, ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "player-01"})
	if err != nil {
		t.Fatalf("list by resource: %v", err)
	}
	if len(byResource) != 1 {
		t.Fatalf("len(byResource) = %d, want 1", len(byResource))
	}

	bySignal, err := st.ListObservations(ctx, ObservationFilter{Signal: "node.agent.uptime"})
	if err != nil {
		t.Fatalf("list by signal: %v", err)
	}
	if len(bySignal) != 1 || bySignal[0].Resource.ID != "node-a" {
		t.Errorf("list by signal = %+v, want exactly node-a's observation", bySignal)
	}
}
