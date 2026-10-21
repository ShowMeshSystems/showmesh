package cueactivate

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func putSequenceTest(t *testing.T, st *store.Store, showID string, entries ...config.SequenceTestEntry) {
	t.Helper()
	payload, err := config.EncodeSequenceTestPayload(config.SequenceTestPayload{Show: showID, Entries: entries})
	if err != nil {
		t.Fatalf("encode show.sequencetest payload: %v", err)
	}
	putConfig(t, st, config.ShowSequenceTestConfigKind, showID, payload)
}

func openNightSession(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.CreateNightSession(context.Background(), store.NightSessionRecord{ID: "night-1", State: "live", StateEnteredAt: time.Unix(2000, 0).UTC()}, time.Unix(2000, 0).UTC()); err != nil {
		t.Fatalf("create night session: %v", err)
	}
}

func sequenceTestFixture(t *testing.T) (*store.Store, fppreconcile.Result, store.FPPPlaylistEntryObservationRecord) {
	t.Helper()
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putLTCCue(t, st, "cue-test", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))
	putSequenceTest(t, st, "show-1", config.SequenceTestEntry{SequenceFilename: "Test.fseq", Cue: "cue-test"})
	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeUnknownEntry, Reason: "no entry of the bound playlist derives an entry key matching the observation's",
		PlaylistID: "playlist-1", PlaylistRevision: 1, Show: "show-1",
	}
	obs := baseObservation("inst-1")
	obs.SequenceFilename = "Test.fseq"
	return st, result, obs
}

func TestDecideSequenceTestActivatesTheMappedCueInsteadOfMismatch(t *testing.T) {
	st, result, obs := sequenceTestFixture(t)

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil, nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateActivated {
		t.Fatalf("State = %q, want %q", dec.State, StateActivated)
	}
	act, ok := dec.Activations["node-1"]
	if !ok || act.CueID != "cue-test" {
		t.Fatalf("Activations = %v, want cue-test on node-1", dec.Activations)
	}
}

func TestDecideSequenceTestAppliesWithNoBindingAtAll(t *testing.T) {
	st, _, obs := sequenceTestFixture(t)
	obs.InstanceUUID = "inst-other"

	dec, err := Decide(context.Background(), st, fppreconcile.Result{Outcome: fppreconcile.OutcomeUnbound, Reason: "unbound"}, obs, "inst-other", nil, nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateActivated || dec.Activations["node-1"].CueID != "cue-test" {
		t.Fatalf("Decision = %+v, want cue-test activated", dec)
	}
}

func TestDecideSequenceTestOffDuringANightSession(t *testing.T) {
	st, result, obs := sequenceTestFixture(t)
	openNightSession(t, st)

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil, nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateMismatched || dec.MismatchPolicy != config.ShowPlaylistMismatchPolicyHold {
		t.Fatalf("State = %q policy %q, want the playlist's hold mismatch", dec.State, dec.MismatchPolicy)
	}
	if len(dec.Activations) != 0 {
		t.Fatalf("Activations = %v, want none during a night", dec.Activations)
	}
}

func TestDecideSequenceTestNeverOverridesAResolvedEntry(t *testing.T) {
	st, _, obs := sequenceTestFixture(t)

	dec, err := Decide(context.Background(), st, resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1), obs, "inst-1", nil, nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Activations["node-1"].CueID != "cue-1" {
		t.Fatalf("Activations = %v, want the playlist entry's cue-1", dec.Activations)
	}
}
