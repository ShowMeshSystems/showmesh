package cueactivate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
)

// --- fixtures, mirroring internal/coordinator/fppreconcile's and
// internal/coordinator/assetsync's own test helpers of the same names. ---

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func putConfig(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate config revision %s/%s: %v", kind, id, err)
	}
}

func putShow(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	putConfig(t, st, config.ShowConfigKind, id, payload)
}

func putActiveShow(t *testing.T, st *store.Store, showID string) {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	ctx := context.Background()
	revision := int64(1)
	if obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID); err == nil {
		revision = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActiveConfigKind, ObjectID: config.ShowActiveObjectID, Revision: revision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.active/active: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, revision); err != nil {
		t.Fatalf("activate config revision show.active/active: %v", err)
	}
}

// putLTCCue writes a show.cue declaring an audio output plus LTC (LTC
// requires audio — H0.3/ADR-018's one clock domain). It names an asset
// nothing ever uploads, so [assetsync.ExpectedAssetsForNode] resolves no
// expected content hash for it and a test node can be "asset ready"
// without ever uploading anything.
func putLTCCue(t *testing.T, st *store.Store, id, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "asset-" + id},
			LTC:   &config.ShowCueLTCOutput{StartOffsetMillis: 1500},
		},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, id, payload)
}

func putAudioNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

// declareNode gives nodeID a row in the "nodes" table itself — the table
// [store.Store.ListNodes] actually reads (queries.go's nodeQueryFrom),
// which is DISTINCT from node_declarations
// ([store.Store.DeclareNode]'s own table, RES-008's declared-vs-observed
// split). [store.Store.UpsertHello] is the simplest real write that
// creates it.
func declareNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{Label: nodeID}); err != nil {
		t.Fatalf("declare node %q: %v", nodeID, err)
	}
}

// putFreshReport marks nodeID's asset inventory report complete and dated
// now, with nothing expected and nothing held — the simplest path to a
// [assetsync.ManifestReady] node in a test where no Cue in play names an
// asset at all (an LTC-only Cue).
func putFreshReport(t *testing.T, st *store.Store, nodeID string, now time.Time) {
	t.Helper()
	err := st.ReplaceNodeAssetInventory(context.Background(), nodeID, nil, store.NodeAssetReportRecord{
		NodeID: nodeID, ReportedAt: now, Complete: true,
	})
	if err != nil {
		t.Fatalf("replace node asset inventory for %q: %v", nodeID, err)
	}
}

func putPlaylist(t *testing.T, st *store.Store, id string, p config.ShowPlaylistPayload) {
	t.Helper()
	payload, err := config.EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, id, payload)
}

const testInterval = time.Minute

// singleEntryPlaylist builds a minimal fpp-runner show.playlist bound to
// instanceUUID, one entry naming cueID.
func singleEntryPlaylist(showID, instanceUUID, playlistHash, cueID, mismatchPolicy, safeCueRef string) config.ShowPlaylistPayload {
	return config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: mismatchPolicy, SafeCueRef: safeCueRef,
		FPP: &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: playlistHash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
}

func baseObservation(instanceUUID string) store.FPPPlaylistEntryObservationRecord {
	return store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 1, Position: 4200,
		Action: "playing", ObservedAt: time.Unix(2000, 0).UTC(), ReceivedAt: time.Unix(2000, 0).UTC(),
	}
}

// resolvedResult builds the [fppreconcile.Result] shape Reconcile itself
// returns on OutcomeResolved, for showID/cueID/entryID bound by playlistID.
func resolvedResult(showID, playlistID string, playlistRevision int64, entryID, cueID string, cueRevision int64) fppreconcile.Result {
	return fppreconcile.Result{
		Outcome: fppreconcile.OutcomeResolved, Reason: "the observation names exactly one Playlist, entry, and Cue",
		PlaylistID: playlistID, PlaylistRevision: playlistRevision, Show: showID,
		EntryID: entryID, CueID: cueID, CueRevision: cueRevision,
	}
}

// --- Decide: a resolved observation activates every pinned identity ---

func TestDecideResolvedActivationCarriesEveryPinnedIdentity(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putFreshReport(t, st, "node-1", now)
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateActivated {
		t.Fatalf("State = %q, want %q", dec.State, StateActivated)
	}
	act, ok := dec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1; Activations = %+v", dec.Activations)
	}
	if act.Runner != "fpp" || act.RunnerInstance != "inst-1" {
		t.Errorf("Runner/RunnerInstance = %q/%q, want fpp/inst-1", act.Runner, act.RunnerInstance)
	}
	if act.Show != "show-1" {
		t.Errorf("Show = %q, want show-1", act.Show)
	}
	if act.Generation != 1 {
		t.Errorf("Generation = %d, want 1 (show.active's own first revision)", act.Generation)
	}
	if act.CatalogRevision == "" {
		t.Error("CatalogRevision is empty, want a computed revision")
	}
	if act.Playlist != "playlist-1" || act.PlaylistRevision != 1 {
		t.Errorf("Playlist/PlaylistRevision = %q/%d, want playlist-1/1", act.Playlist, act.PlaylistRevision)
	}
	if act.EntryID != "entry-1" {
		t.Errorf("EntryID = %q, want entry-1", act.EntryID)
	}
	if act.CueID != "cue-1" || act.CueRevision != 1 {
		t.Errorf("CueID/CueRevision = %q/%d, want cue-1/1", act.CueID, act.CueRevision)
	}
	if act.PositionMS != 4200 {
		t.Errorf("PositionMS = %d, want 4200 (obs.Position, never the coordinator clock)", act.PositionMS)
	}
	if !act.EvidenceAt.Equal(obs.ObservedAt) {
		t.Errorf("EvidenceAt = %v, want obs.ObservedAt %v", act.EvidenceAt, obs.ObservedAt)
	}
	if act.ActivationID == "" {
		t.Error("ActivationID is empty, want a stable id")
	}
	if err := act.Validate(); err != nil {
		t.Errorf("built Activation fails its own Validate: %v", err)
	}
}

// --- Authorize: independent refusal, never a partial dispatch ---

func TestAuthorizeCrossShowRefusesDispatchingNothing(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putFreshReport(t, st, "node-1", now)
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)
	obs := baseObservation("inst-1")
	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	act, ok := dec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1")
	}

	// The active show switches to a DIFFERENT show between Decide and the
	// dispatch-time Authorize call — the race Authorize exists to catch.
	putShow(t, st, "show-2", "Show Two")
	putActiveShow(t, st, "show-2")

	outcome, ok, err := Authorize(context.Background(), st, now, testInterval, "node-1", act)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("Authorize ok = true, want false: the active show no longer matches the activation's own Show")
	}
	if outcome != cueauth.OutcomeCrossShow {
		t.Fatalf("outcome = %q, want %q", outcome, cueauth.OutcomeCrossShow)
	}
}

func TestAuthorizeStaleGenerationRefused(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putFreshReport(t, st, "node-1", now)
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)
	obs := baseObservation("inst-1")
	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	act, ok := dec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1")
	}

	// Re-authorizing the SAME show bumps show.active's own revision, which
	// is the generation (TRACK-H-H3-SPEC.md section 2) — act.Generation is
	// now older than what the coordinator holds.
	putActiveShow(t, st, "show-1")

	outcome, ok, err := Authorize(context.Background(), st, now, testInterval, "node-1", act)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("Authorize ok = true, want false: the generation moved forward since the Activation was built")
	}
	if outcome != cueauth.OutcomeStaleGeneration {
		t.Fatalf("outcome = %q, want %q", outcome, cueauth.OutcomeStaleGeneration)
	}
}

// --- H0.2 mismatch policy: same state, different effect ---

func TestDecideMismatchHoldDispatchesNothing(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	// evidence-mismatch: the observation's entryKey never matches anything.
	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeUnknownEntry, Reason: "no entry of the bound playlist derives an entry key matching the observation's",
		PlaylistID: "playlist-1", PlaylistRevision: 1, Show: "show-1",
	}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateMismatched {
		t.Fatalf("State = %q, want %q", dec.State, StateMismatched)
	}
	if dec.MismatchPolicy != config.ShowPlaylistMismatchPolicyHold {
		t.Fatalf("MismatchPolicy = %q, want hold", dec.MismatchPolicy)
	}
	if len(dec.Activations) != 0 || len(dec.ClearNodes) != 0 {
		t.Fatalf("hold dispatched something: Activations=%v ClearNodes=%v", dec.Activations, dec.ClearNodes)
	}
	if dec.Reason == "" {
		t.Error("Reason is empty, want the observed evidence")
	}
}

func TestDecideMismatchBlackAndSilenceClearsParticipatingNodes(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyBlackAndSilence, ""))

	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeCrossShow, Reason: "the bound playlist's show is not the currently active show",
		PlaylistID: "playlist-other", PlaylistRevision: 1, Show: "show-other",
	}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateMismatched {
		t.Fatalf("State = %q, want %q", dec.State, StateMismatched)
	}
	if dec.MismatchPolicy != config.ShowPlaylistMismatchPolicyBlackAndSilence {
		t.Fatalf("MismatchPolicy = %q, want blackAndSilence", dec.MismatchPolicy)
	}
	if len(dec.Activations) != 0 {
		t.Fatalf("blackAndSilence built an Activation: %v", dec.Activations)
	}
	if len(dec.ClearNodes) != 1 || dec.ClearNodes[0] != "node-1" {
		t.Fatalf("ClearNodes = %v, want [node-1]", dec.ClearNodes)
	}
}

func TestDecideMismatchSafeCueActivatesTheSafeCue(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")    // the mismatched entry's own (unauthorized-in-context) cue
	putLTCCue(t, st, "cue-safe", "show-1") // the configured safe cue
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicySafeCue, "cue-safe"))

	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeEvidenceMismatch, Reason: "expected sequence filename does not match the observed",
		PlaylistID: "playlist-1", PlaylistRevision: 1, Show: "show-1", EntryID: "entry-1", CueID: "cue-1", CueRevision: 1,
	}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateMismatched {
		t.Fatalf("State = %q, want %q", dec.State, StateMismatched)
	}
	if dec.MismatchPolicy != config.ShowPlaylistMismatchPolicySafeCue {
		t.Fatalf("MismatchPolicy = %q, want safeCue", dec.MismatchPolicy)
	}
	act, ok := dec.Activations["node-1"]
	if !ok {
		t.Fatalf("safeCue built no activation for node-1: %v", dec.Activations)
	}
	if act.CueID != "cue-safe" {
		t.Fatalf("CueID = %q, want cue-safe (never the mismatched entry's own cue)", act.CueID)
	}
	if act.EntryID != "" {
		t.Errorf("EntryID = %q, want empty: a safeCue substitution is not tied to the mismatched entry", act.EntryID)
	}
}

// --- unbound ---

func TestDecideUnboundWhenFppreconcileFoundNoBindingAnywhere(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	result := fppreconcile.Result{Outcome: fppreconcile.OutcomeUnbound, Reason: "no ShowMesh output was ever authorized by this instance"}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateUnbound {
		t.Fatalf("State = %q, want %q", dec.State, StateUnbound)
	}
	if len(dec.Activations) != 0 || len(dec.ClearNodes) != 0 {
		t.Fatalf("unbound dispatched something: Activations=%v ClearNodes=%v", dec.Activations, dec.ClearNodes)
	}
}

func TestDecideUnboundWhenActiveShowHasNoOwnBindingForThisInstance(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	// The binding fppreconcile matched belongs to a DIFFERENT show, and
	// the ACTIVE show has no fpp-runner Playlist of its own bound to this
	// instance — H0.2's own "nothing to hold" case, even though
	// fppreconcile itself reported cross-show rather than unbound.
	putShow(t, st, "show-other", "Show Other")
	putPlaylist(t, st, "playlist-other", singleEntryPlaylist("show-other", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeCrossShow, Reason: "the bound playlist's show is not the currently active show",
		PlaylistID: "playlist-other", PlaylistRevision: 1, Show: "show-other",
	}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateUnbound {
		t.Fatalf("State = %q, want %q", dec.State, StateUnbound)
	}
}

// --- identity-unavailable ---

func TestDecideIdentityUnavailable(t *testing.T) {
	st := openTestStore(t)
	result := fppreconcile.Result{Outcome: fppreconcile.OutcomeIdentityUnavailable, Reason: "FPP could not establish identity"}
	obs := baseObservation("inst-1")

	dec, err := Decide(context.Background(), st, result, obs, "inst-1")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateIdentityUnavailable {
		t.Fatalf("State = %q, want %q", dec.State, StateIdentityUnavailable)
	}
}

// --- ActivationID stability: an entry change dispatches exactly one ---

func TestActivationIDStableAcrossRepeatedDecideForTheSameEntry(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putFreshReport(t, st, "node-1", now)
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)

	// Two observations at different FPP sequence numbers, same entry: a
	// MultiSync position tick, not an entry change.
	obs1 := baseObservation("inst-1")
	obs1.Sequence, obs1.Position = 1, 1000
	obs2 := baseObservation("inst-1")
	obs2.Sequence, obs2.Position = 2, 5000

	dec1, err := Decide(context.Background(), st, result, obs1, "inst-1")
	if err != nil {
		t.Fatalf("Decide (1): %v", err)
	}
	dec2, err := Decide(context.Background(), st, result, obs2, "inst-1")
	if err != nil {
		t.Fatalf("Decide (2): %v", err)
	}
	id1, id2 := dec1.Activations["node-1"].ActivationID, dec2.Activations["node-1"].ActivationID
	if id1 == "" || id1 != id2 {
		t.Fatalf("ActivationID changed across a position-only tick for the same entry: %q vs %q — this would dispatch a new activation on every tick instead of exactly one per entry change", id1, id2)
	}

	// A DIFFERENT entry/cue must mint a different id.
	otherCueID := "cue-other"
	putLTCCue(t, st, otherCueID, "show-1")
	resultOther := resolvedResult("show-1", "playlist-1", 1, "entry-2", otherCueID, 1)
	decOther, err := Decide(context.Background(), st, resultOther, obs2, "inst-1")
	if err != nil {
		t.Fatalf("Decide (other entry): %v", err)
	}
	idOther := decOther.Activations["node-1"].ActivationID
	if idOther == id1 {
		t.Fatal("ActivationID did not change for a different entry/cue")
	}
}

// TestActivationIDChangesAcrossALoopingEntryOccurrence is defect 1's own
// regression test: a looping FPP playlist re-activates its Cues. Two ticks
// carrying the SAME EntryOccurrenceSequence (schemaV17's entry-start
// identity, computed at ingestion from action/entryKey — see
// fppobservations.go and TestActivationIDStableAcrossRepeatedDecideForTheSameEntry
// above for the "one dispatch per occurrence" half) must still dedup to one
// ActivationID; a THIRD tick reporting a new EntryOccurrenceSequence for the
// IDENTICAL node/show/generation/catalog/playlist/entry/cue tuple — exactly
// what a playlist looping E1->E2->E3->E1 produces on its second lap, since
// every other identity field is unchanged — must mint a different one, or
// [store.InsertCommand] reports a duplicate and the loop's second lap is
// silently never dispatched.
func TestActivationIDChangesAcrossALoopingEntryOccurrence(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putFreshReport(t, st, "node-1", now)
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)

	// First lap through entry-1: two ticks of the SAME occurrence (the
	// stored observation's own entry-start identity), a wire sequence bump
	// with no new occurrence in between.
	firstLapTick1 := baseObservation("inst-1")
	firstLapTick1.Sequence, firstLapTick1.EntryOccurrenceSequence, firstLapTick1.Position = 1, 1, 1000
	firstLapTick2 := baseObservation("inst-1")
	firstLapTick2.Sequence, firstLapTick2.EntryOccurrenceSequence, firstLapTick2.Position = 2, 1, 5000

	dec1, err := Decide(context.Background(), st, result, firstLapTick1, "inst-1")
	if err != nil {
		t.Fatalf("Decide (first lap, tick 1): %v", err)
	}
	dec2, err := Decide(context.Background(), st, result, firstLapTick2, "inst-1")
	if err != nil {
		t.Fatalf("Decide (first lap, tick 2): %v", err)
	}
	id1, id2 := dec1.Activations["node-1"].ActivationID, dec2.Activations["node-1"].ActivationID
	if id1 == "" || id1 != id2 {
		t.Fatalf("ActivationID changed across two ticks of one occurrence: %q vs %q, want a single dispatch per occurrence", id1, id2)
	}

	// The playlist loops back to entry-1: a fresh EntryOccurrenceSequence
	// for the identical node/show/generation/catalog/playlist/entry/cue
	// identity — every field [Decide] pins is unchanged except the
	// occurrence itself, exactly as a real loop's second lap reports it.
	secondLapTick := baseObservation("inst-1")
	secondLapTick.Sequence, secondLapTick.EntryOccurrenceSequence, secondLapTick.Position = 3, 3, 1000

	dec3, err := Decide(context.Background(), st, result, secondLapTick, "inst-1")
	if err != nil {
		t.Fatalf("Decide (second lap): %v", err)
	}
	id3 := dec3.Activations["node-1"].ActivationID
	if id3 == "" {
		t.Fatal("second lap built no ActivationID")
	}
	if id3 == id1 {
		t.Fatalf("ActivationID did not change on a loop re-entry (same entry, new occurrence): stayed %q — the second lap would silently never dispatch", id3)
	}
}

// hash64 mirrors internal/coordinator/fppreconcile's own test helper:
// produces a syntactically valid 64-lowercase-hex hash from a short label.
func hash64(label string) string {
	h := strings.Repeat("0", 64-len(label)) + label
	return h[len(h)-64:]
}
