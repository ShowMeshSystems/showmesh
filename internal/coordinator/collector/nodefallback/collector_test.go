package nodefallback

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func pollBySignal(t *testing.T, store *Store) map[observation.SignalID]observation.Observation {
	t.Helper()
	obs, ok := New(store).Poll(context.Background())
	if !ok {
		t.Fatal("Poll reported failure")
	}
	out := make(map[observation.SignalID]observation.Observation, len(obs))
	for _, o := range obs {
		if o.Resource.Kind != observation.ResourceNode || o.Resource.ID != "node-a" {
			t.Fatalf("observation %s is for %+v, want node node-a", o.Signal, o.Resource)
		}
		out[o.Signal] = o
	}
	return out
}

func TestRefusalIsVisibleAsTheLastRefusal(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 5, 0, 0, time.UTC)
	store := NewStore()
	store.Put("node-a", mqttproto.FallbackPayload{
		ObservedAt: &at, CoordinatorKeyLoaded: true, Accepted: 1, Refused: 1,
		Programs: []mqttproto.FallbackHeldProgram{{FPPInstanceUUID: "fpp-1", PackageID: "pkg-1", Revision: "rev-1", ExpiresAt: at.Add(10 * time.Minute), InstalledAt: at, ExecutorEnrolled: true}},
		Decisions: []mqttproto.FallbackDecision{
			{At: at, Route: "activation", Outcome: "signature-invalid", Reason: "A fallback request was not signed by the paired FPP player. Nothing was started.", FPPInstanceUUID: "fpp-1", ExecutionID: "id-1", Count: 1},
			{At: at.Add(time.Second), Route: "activation", Accepted: true, Outcome: "authorized", Reason: "The Cue was started from the FPP player's fallback program.", FPPInstanceUUID: "fpp-1", ExecutionID: "id-2", Count: 1},
		},
	}, at)

	got := pollBySignal(t, store)
	if len(got) != len(AllSignalIDs) {
		t.Fatalf("emitted %d signals, want all %d", len(got), len(AllSignalIDs))
	}
	if got[SignalLastRefusalOutcome].Value != "signature-invalid" || got[SignalLastRefusalFPP].Value != "fpp-1" {
		t.Fatalf("last refusal = %v for %v, want signature-invalid for fpp-1", got[SignalLastRefusalOutcome].Value, got[SignalLastRefusalFPP].Value)
	}
	if got[SignalLastRefusalReason].Value != "A fallback request was not signed by the paired FPP player. Nothing was started." {
		t.Fatalf("last refusal reason = %v, want the node's own sentence", got[SignalLastRefusalReason].Value)
	}
	if got[SignalLastOutcome].Value != "authorized" {
		t.Fatalf("last outcome = %v, want authorized", got[SignalLastOutcome].Value)
	}
	if got[SignalRefusedCount].Value != int64(1) || got[SignalAcceptedCount].Value != int64(1) {
		t.Fatalf("counts = %v accepted, %v refused, want 1 and 1", got[SignalAcceptedCount].Value, got[SignalRefusedCount].Value)
	}
	if got[SignalProgramCount].Value != int64(1) {
		t.Fatalf("program count = %v, want 1", got[SignalProgramCount].Value)
	}
	if got[SignalEnrolledProgramCount].Value != int64(1) || got[SignalUnenrolledFPP].Value != "" {
		t.Fatalf("enrolled = %v, unenrolled = %q, want 1 and none", got[SignalEnrolledProgramCount].Value, got[SignalUnenrolledFPP].Value)
	}
	if got[SignalExecutionRecordProblem].Value != "" {
		t.Fatalf("execution record problem = %q, want none", got[SignalExecutionRecordProblem].Value)
	}
}

// A node holding a program with no executor key will refuse every
// activation under it, and nothing else about that program looks wrong.
func TestHeldProgramWithNoExecutorKeyIsNamed(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 5, 0, 0, time.UTC)
	store := NewStore()
	store.Put("node-a", mqttproto.FallbackPayload{
		ObservedAt: &at, CoordinatorKeyLoaded: true,
		ExecutionRecordProblem: "This node's record of handled fallback requests is damaged.",
		Programs: []mqttproto.FallbackHeldProgram{
			{FPPInstanceUUID: "fpp-2", PackageID: "pkg-2", ExpiresAt: at, InstalledAt: at},
			{FPPInstanceUUID: "fpp-1", PackageID: "pkg-1", ExpiresAt: at, InstalledAt: at, ExecutorEnrolled: true},
			{FPPInstanceUUID: "fpp-0", PackageID: "pkg-0", ExpiresAt: at, InstalledAt: at},
		},
	}, at)

	got := pollBySignal(t, store)
	if got[SignalProgramCount].Value != int64(3) || got[SignalEnrolledProgramCount].Value != int64(1) {
		t.Fatalf("programs = %v, enrolled = %v, want 3 and 1", got[SignalProgramCount].Value, got[SignalEnrolledProgramCount].Value)
	}
	if got[SignalUnenrolledFPP].Value != "fpp-0,fpp-2" {
		t.Fatalf("unenrolled = %q, want fpp-0,fpp-2", got[SignalUnenrolledFPP].Value)
	}
	if got[SignalExecutionRecordProblem].Value != "This node's record of handled fallback requests is damaged." {
		t.Fatalf("execution record problem = %q, want the node's own sentence", got[SignalExecutionRecordProblem].Value)
	}
}

func TestNodeWithNoAnswersReportsThatNothingWasCollected(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 5, 0, 0, time.UTC)
	store := NewStore()
	store.Put("node-a", mqttproto.FallbackPayload{ObservedAt: &at}, at)

	got := pollBySignal(t, store)
	for _, sig := range []observation.SignalID{SignalLastOutcome, SignalLastRefusalOutcome} {
		if got[sig].Value != nil {
			t.Fatalf("%s carries a value %+v for a node that answered nothing", sig, got[sig].Value)
		}
	}
	if got[SignalCoordinatorKeyLoaded].Value != false {
		t.Fatalf("coordinator key loaded = %v, want false", got[SignalCoordinatorKeyLoaded].Value)
	}
}
