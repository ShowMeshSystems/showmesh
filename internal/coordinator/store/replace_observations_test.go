package store

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file tests [Store.ReplaceObservations]: the scoped-delete mechanism
// behind the collector/Sink completeness contract
// (internal/coordinator/collector.Collector.Poll's complete return value)
// that lets a Sink safely prune a stale row — a removed sensor, a port that
// dropped out of a reconfigured cape, an instance whose ports collapsed
// from 48 elements to none — without ever destroying evidence a merely
// skipped or backed-off poll simply did not re-check. See
// internal/coordinator/apiwiring_test.go's
// TestFPPSinkSkippedPollDeletesNothing for the sink-level half of this
// contract (complete=false routing to plain per-observation upsert,
// touching ReplaceObservations not at all) and
// TestFPPSinkRealPortFixturesPruneGhostRowsOnEmptyDelivery for the
// end-to-end real-fixture reproduction of the defect this exists to fix.

const testSource = "fpp-rest"

func measuredObs(t *testing.T, resourceID string, sig observation.SignalID, value any) observation.Observation {
	t.Helper()
	obs, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: resourceID},
		sig, value, mustTime(t, "2026-08-11T12:00:00Z"),
		observation.WithSource(testSource),
	)
	if err != nil {
		t.Fatalf("build measured observation %q: %v", sig, err)
	}
	return obs
}

func failedObs(t *testing.T, resourceID string, sig observation.SignalID, reason string) observation.Observation {
	t.Helper()
	obs, err := observation.CollectionFailed(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: resourceID},
		sig, reason,
		observation.WithSource(testSource), observation.WithCollectedAt(mustTime(t, "2026-08-11T12:00:00Z")),
	)
	if err != nil {
		t.Fatalf("build collection_failed observation %q: %v", sig, err)
	}
	return obs
}

func signalSet(t *testing.T, ctx context.Context, st *Store, resourceID, source string) map[observation.SignalID]observation.Observation {
	t.Helper()
	got, err := st.ListObservations(ctx, ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: resourceID})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	out := make(map[observation.SignalID]observation.Observation)
	for _, o := range got {
		if o.Source != source {
			continue
		}
		out[o.Signal] = o
	}
	return out
}

// TestReplaceObservationsPrunesSignalsNotInDelivery is the general property
// this method exists for: a second call for the same (resource, source)
// that omits a signal the first call included removes that signal's row
// entirely, not merely leaves it unrefreshed to age toward stale.
func TestReplaceObservationsPrunesSignalsNotInDelivery(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := []observation.Observation{
		measuredObs(t, "remote-04", "fpp.port.16.current_ma", float64(0)),
		measuredObs(t, "remote-04", "fpp.port.17.current_ma", float64(0)),
		measuredObs(t, "remote-04", "fpp.ports.count", int64(2)),
	}
	if err := st.ReplaceObservations(ctx, first); err != nil {
		t.Fatalf("ReplaceObservations (first): %v", err)
	}
	if got := signalSet(t, ctx, st, "remote-04", testSource); len(got) != 3 {
		t.Fatalf("after first delivery: %d signals, want 3: %+v", len(got), got)
	}

	// Second delivery drops fpp.port.17.current_ma entirely (the port no
	// longer exists) and updates the count.
	second := []observation.Observation{
		measuredObs(t, "remote-04", "fpp.port.16.current_ma", float64(0)),
		measuredObs(t, "remote-04", "fpp.ports.count", int64(1)),
	}
	if err := st.ReplaceObservations(ctx, second); err != nil {
		t.Fatalf("ReplaceObservations (second): %v", err)
	}

	got := signalSet(t, ctx, st, "remote-04", testSource)
	if len(got) != 2 {
		t.Fatalf("after second delivery: %d signals, want exactly 2 (port 17 must be pruned): %+v", len(got), got)
	}
	if _, stillThere := got["fpp.port.17.current_ma"]; stillThere {
		t.Errorf("fpp.port.17.current_ma still present after a delivery that omitted it, want pruned")
	}
	if v := got["fpp.ports.count"].Value; v != int64(1) {
		t.Errorf("fpp.ports.count = %v, want 1 (the surviving signal's value must still update normally)", v)
	}
}

// TestReplaceObservationsSensorRemovedIsPruned is the task's named "a
// sensor removed between polls has its rows removed" case: the dynamic
// fpp.sensor.<key>.* family behaves exactly like the port family above,
// using sensor-shaped signal IDs specifically so a fix that only handled
// the port family by name (rather than generically, by whatever signals
// are actually delivered) would still be caught.
func TestReplaceObservationsSensorRemovedIsPruned(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	withBothSensors := []observation.Observation{
		measuredObs(t, "main", "fpp.reachable", true),
		measuredObs(t, "main", "fpp.sensor.temp.value", float64(72.5)),
		measuredObs(t, "main", "fpp.sensor.temp.type", "Temperature"),
		measuredObs(t, "main", "fpp.sensor.humidity.value", float64(41)),
		measuredObs(t, "main", "fpp.sensor.humidity.type", "Humidity"),
	}
	if err := st.ReplaceObservations(ctx, withBothSensors); err != nil {
		t.Fatalf("ReplaceObservations (both sensors): %v", err)
	}
	if got := signalSet(t, ctx, st, "main", testSource); len(got) != 5 {
		t.Fatalf("after first delivery: %d signals, want 5: %+v", len(got), got)
	}

	// The humidity sensor is removed from the fppd config; a later poll
	// reports only fpp.reachable and the temp sensor.
	humidityRemoved := []observation.Observation{
		measuredObs(t, "main", "fpp.reachable", true),
		measuredObs(t, "main", "fpp.sensor.temp.value", float64(73.0)),
		measuredObs(t, "main", "fpp.sensor.temp.type", "Temperature"),
	}
	if err := st.ReplaceObservations(ctx, humidityRemoved); err != nil {
		t.Fatalf("ReplaceObservations (humidity removed): %v", err)
	}

	got := signalSet(t, ctx, st, "main", testSource)
	if len(got) != 3 {
		t.Fatalf("after humidity removed: %d signals, want exactly 3: %+v", len(got), got)
	}
	for _, ghost := range []observation.SignalID{"fpp.sensor.humidity.value", "fpp.sensor.humidity.type"} {
		if _, stillThere := got[ghost]; stillThere {
			t.Errorf("%q still present after a delivery that omitted it, want pruned (removed sensor)", ghost)
		}
	}
	if _, ok := got["fpp.sensor.temp.value"]; !ok {
		t.Errorf("fpp.sensor.temp.value pruned, want kept (still delivered every cycle)")
	}
}

// TestReplaceObservationsFailedPollDoesNotDeleteTheSignalsItReports is the
// task's named case: a poll that reports collection_failed for every
// signal it owns (a real, complete attempt — see
// internal/coordinator/collector.Collector.Poll's doc comment) must not
// have those very signals vanish. This guards specifically against an
// upsert-then-prune implementation accidentally ordering the delete before
// (or instead of) the insert, or scoping the "keep" set to something other
// than exactly what was just delivered.
//
// Mutation-checked: temporarily reordering ReplaceObservations to issue the
// prune DELETE before the per-observation upserts (rather than after, as
// implemented) made this test fail with 0 signals surviving, confirming it
// actually exercises delete/insert ordering and not merely presence of any
// row at all.
func TestReplaceObservationsFailedPollDoesNotDeleteTheSignalsItReports(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	failure := []observation.Observation{
		failedObs(t, "remote-01", "fpp.reachable", "connection refused"),
		failedObs(t, "remote-01", "fpp.ports.count", "connection refused"),
		failedObs(t, "remote-01", "fpp.ports.blind_count", "connection refused"),
		failedObs(t, "remote-01", "fpp.version", "connection refused"),
	}
	if err := st.ReplaceObservations(ctx, failure); err != nil {
		t.Fatalf("ReplaceObservations (failed poll): %v", err)
	}

	got := signalSet(t, ctx, st, "remote-01", testSource)
	if len(got) != len(failure) {
		t.Fatalf("after failed poll: %d signals, want exactly %d (the failure must report, not delete, its own signals): %+v", len(got), len(failure), got)
	}
	for _, obs := range failure {
		stored, ok := got[obs.Signal]
		if !ok {
			t.Errorf("signal %q missing after a failed poll that reported it as collection_failed", obs.Signal)
			continue
		}
		if stored.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q Absence = %q, want collection_failed", obs.Signal, stored.Absence)
		}
	}

	// A second consecutive failed poll (the realistic case: an instance
	// stays unreachable across several cycles) must be equally stable —
	// not delete-then-fail-to-reinsert on a repeat call.
	if err := st.ReplaceObservations(ctx, failure); err != nil {
		t.Fatalf("ReplaceObservations (second failed poll): %v", err)
	}
	got = signalSet(t, ctx, st, "remote-01", testSource)
	if len(got) != len(failure) {
		t.Fatalf("after second consecutive failed poll: %d signals, want exactly %d", len(got), len(failure))
	}
}

// TestReplaceObservationsEmptyBatchIsNoOp confirms ReplaceObservations
// never infers "zero observations delivered" as "delete everything for
// every resource and source" — see the method's own doc comment. A
// zero-length batch carries no (resource, source) pair to scope a delete
// to at all.
func TestReplaceObservationsEmptyBatchIsNoOp(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	seed := []observation.Observation{
		measuredObs(t, "main", "fpp.reachable", true),
		measuredObs(t, "main", "fpp.version", "9.4"),
	}
	if err := st.ReplaceObservations(ctx, seed); err != nil {
		t.Fatalf("ReplaceObservations (seed): %v", err)
	}

	if err := st.ReplaceObservations(ctx, nil); err != nil {
		t.Fatalf("ReplaceObservations (empty): %v", err)
	}

	got := signalSet(t, ctx, st, "main", testSource)
	if len(got) != len(seed) {
		t.Fatalf("after an empty ReplaceObservations call: %d signals, want unchanged %d", len(got), len(seed))
	}
}

// TestReplaceObservationsScopesPruningPerSource proves the prune is scoped
// to (resource_kind, resource_id, source), never merely (resource_kind,
// resource_id): schema v4 exists specifically so two collector sources
// reporting the same resource coexist (see migrations.go's schemaV4 doc
// comment), and a ReplaceObservations call for one source must never prune
// the other source's rows for the same resource, even when that other
// source is not mentioned in this call at all.
func TestReplaceObservationsScopesPruningPerSource(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	// Deliberately a DIFFERENT signal than the fpp-rest call below delivers
	// (fpp.power.bad, an MQTT-only signal fpp-rest never reports at all —
	// see contract section 4.3): if pruning were ever scoped by
	// (resource_kind, resource_id) alone instead of including source, this
	// row's signal would look exactly like "a signal fpp-rest's delivery
	// omitted" and get deleted, which is the specific mistake this test
	// exists to catch. Using the same signal name for both sources (as an
	// earlier version of this test did) would pass even with source
	// scoping broken, since the shared signal name would coincidentally
	// satisfy any NOT IN check regardless of which source's row it belongs
	// to — see this test's mutation check.
	mqttObs, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: "main"},
		"fpp.power.bad", false, mustTime(t, "2026-08-11T12:00:00Z"),
		observation.WithSource("fpp-mqtt"),
	)
	if err != nil {
		t.Fatalf("build fpp-mqtt observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, mqttObs); err != nil {
		t.Fatalf("seed fpp-mqtt observation: %v", err)
	}

	// A fpp-rest ReplaceObservations call for the same resource, never
	// mentioning fpp-mqtt or fpp.power.bad at all.
	restObs := []observation.Observation{measuredObs(t, "main", "fpp.reachable", true)}
	if err := st.ReplaceObservations(ctx, restObs); err != nil {
		t.Fatalf("ReplaceObservations (fpp-rest): %v", err)
	}

	got, err := st.ListObservations(ctx, ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "main"})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	var sawMQTT, sawREST bool
	for _, o := range got {
		switch o.Source {
		case "fpp-mqtt":
			sawMQTT = true
		case testSource:
			sawREST = true
		}
	}
	if !sawMQTT {
		t.Errorf("fpp-mqtt's row was pruned by a fpp-rest ReplaceObservations call that never mentioned it, want untouched")
	}
	if !sawREST {
		t.Errorf("fpp-rest's own delivered row is missing")
	}
}
