package fallbackreconcile

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const signalTestInterval = 2 * time.Minute

func executorKeySignal(t *testing.T, st *store.Store) observation.Observation {
	t.Helper()
	all, err := st.ListObservations(context.Background(), store.ObservationFilter{ResourceKind: observation.ResourceFallbackProgram})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	var found []observation.Observation
	for _, o := range all {
		if o.Signal == SignalExecutorKeyPresent && o.Resource.ID == testInstanceUUID {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("found %d %s rows for %s, want exactly one", len(found), SignalExecutorKeyPresent, testInstanceUUID)
	}
	return found[0]
}

func signalTestService(st *store.Store, now *time.Time) *Service {
	svc := NewService(st, fakeSigner{}, nil, nil, signalTestInterval)
	svc.now = func() time.Time { return *now }
	return svc
}

func breakCatalogAcknowledgement(t *testing.T, st *store.Store, at time.Time) {
	t.Helper()
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: "a-catalog-the-coordinator-does-not-resolve", ShowID: "halloween", Generation: 1, AcknowledgedAt: at,
	}); err != nil {
		t.Fatalf("overwrite catalog ack: %v", err)
	}
}

// A program published with no executor key verifies, installs and
// acknowledges like any other, so this signal is the only thing that says
// no node will accept an activation under it.
func TestExecutorKeySignalReadsFalseUntilAKeyIsRegistered(t *testing.T) {
	st, now := newPublishableFixture(t)
	svc := signalTestService(st, &now)

	svc.reconcileOnce(context.Background())
	got := executorKeySignal(t, st)
	if got.Value != false || got.Source != ObservationSource {
		t.Fatalf("with no key registered: value %v source %q, want false from %s", got.Value, got.Source, ObservationSource)
	}

	if _, _, err := st.PutFallbackExecutorKey(context.Background(), store.FallbackExecutorKeyRecord{
		FPPInstanceUUID: testInstanceUUID, PublicKeyB64: "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", RegisteredAt: now,
	}); err != nil {
		t.Fatalf("put executor key: %v", err)
	}
	svc.reconcileOnce(context.Background())
	if got := executorKeySignal(t, st); got.Value != true {
		t.Fatalf("after a key is registered: value %v, want true", got.Value)
	}
}

func TestExecutorKeySignalStaysCurrentBetweenPassesAndGoesStaleWhenTheyStop(t *testing.T) {
	st, now := newPublishableFixture(t)
	svc := signalTestService(st, &now)
	svc.reconcileOnce(context.Background())
	got := executorKeySignal(t, st)

	if got.ObservedAt == nil || !got.ObservedAt.Equal(now) {
		t.Fatalf("observed at %v, want the pass time %v", got.ObservedAt, now)
	}
	if state := got.StateAt(now.Add(signalTestInterval)); state != observation.StateCurrent {
		t.Fatalf("one interval after a pass the signal is %q, want current", state)
	}
	if state := got.StateAt(now.Add(2 * signalTestInterval)); state != observation.StateCurrent {
		t.Fatalf("after one missed pass the signal is %q, want still current", state)
	}
	if state := got.StateAt(now.Add(3 * signalTestInterval)); state != observation.StateStale {
		t.Fatalf("three intervals after the last pass the signal is %q, want stale", state)
	}

	later := now.Add(signalTestInterval)
	now = later
	svc.reconcileOnce(context.Background())
	if got := executorKeySignal(t, st); got.ObservedAt == nil || !got.ObservedAt.Equal(later) {
		t.Fatalf("a second pass left observed at %v, want it moved to %v", got.ObservedAt, later)
	}
}

// Once the published program has lapsed and nothing replaces it, the
// signal must not go on reading true from the last good pass.
func TestExecutorKeySignalDoesNotKeepReadingTrueForALapsedProgram(t *testing.T) {
	st, now := newPublishableFixture(t)
	if _, _, err := st.PutFallbackExecutorKey(context.Background(), store.FallbackExecutorKeyRecord{
		FPPInstanceUUID: testInstanceUUID, PublicKeyB64: "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=", RegisteredAt: now,
	}); err != nil {
		t.Fatalf("put executor key: %v", err)
	}
	svc := signalTestService(st, &now)
	svc.reconcileOnce(context.Background())
	if got := executorKeySignal(t, st); got.Value != true {
		t.Fatalf("published with a key: value %v, want true", got.Value)
	}

	breakCatalogAcknowledgement(t, st, now)
	now = now.Add(time.Hour)
	svc.reconcileOnce(context.Background())

	got := executorKeySignal(t, st)
	if got.Value != nil || got.StateAt(now) != observation.StateNotCollected {
		t.Fatalf("after the program lapsed: value %v state %q, want no value and not collected", got.Value, got.StateAt(now))
	}
	if got.Reason == "" {
		t.Fatal("the lapsed program's row carries no reason")
	}
}

func TestExecutorKeySignalSaysWhenNoProgramIsPublished(t *testing.T) {
	st, now := newPublishableFixture(t)
	breakCatalogAcknowledgement(t, st, now)
	svc := signalTestService(st, &now)

	svc.reconcileOnce(context.Background())

	got := executorKeySignal(t, st)
	if got.Value != nil || got.StateAt(now) != observation.StateNotCollected {
		t.Fatalf("with nothing published: value %v state %q, want no value and not collected", got.Value, got.StateAt(now))
	}
}
