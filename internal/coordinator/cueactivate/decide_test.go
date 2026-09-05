package cueactivate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
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

// editLTCCue re-saves id's show.cue payload as a NEW revision (2, 3, ...),
// mirroring what an operator's save of an edited Cue actually does to the
// store: [putLTCCue] itself can only write the FIRST revision (it always
// activates revision 1), so a test that needs to simulate a MID-SHOW edit
// of an already-created Cue calls this instead. startOffsetMillis is
// varied so the resulting payload, and therefore cueObj.CurrentRevision's
// own hash inputs and the cue catalog's computed revision, actually
// differs from whatever revision came before, exactly like a genuine
// operator edit would.
func editLTCCue(t *testing.T, st *store.Store, id, showID string, startOffsetMillis int) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "asset-" + id},
			LTC:   &config.ShowCueLTCOutput{StartOffsetMillis: startOffsetMillis},
		},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, id)
	if err != nil {
		t.Fatalf("get show.cue %q before edit: %v", id, err)
	}
	nextRevision := obj.CurrentRevision + 1
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: id, Revision: nextRevision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.cue/%s rev %d: %v", id, nextRevision, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, id, nextRevision); err != nil {
		t.Fatalf("activate config revision show.cue/%s rev %d: %v", id, nextRevision, err)
	}
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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
	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	outcome, _, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", act, nil)
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
	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	outcome, _, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", act, nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
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

	dec1, err := Decide(context.Background(), st, result, obs1, "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide (1): %v", err)
	}
	dec2, err := Decide(context.Background(), st, result, obs2, "inst-1", nil)
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
	decOther, err := Decide(context.Background(), st, resultOther, obs2, "inst-1", nil)
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
// carrying the SAME EntryOccurrenceSequence (schemaV18's entry-start
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

	dec1, err := Decide(context.Background(), st, result, firstLapTick1, "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide (first lap, tick 1): %v", err)
	}
	dec2, err := Decide(context.Background(), st, result, firstLapTick2, "inst-1", nil)
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

	dec3, err := Decide(context.Background(), st, result, secondLapTick, "inst-1", nil)
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

// putAudioCue writes a show.cue declaring only an audio output naming
// sequence — mirrors putLTCCue one field narrower (no LTC), with the
// sequence id parameterized so a test can name two DIFFERENT sequences for
// two DIFFERENT cues on the same node.
func putAudioCue(t *testing.T, st *store.Store, id, showID, sequence string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: sequence}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, id, payload)
}

// createAsset mirrors internal/coordinator/assetsync's own test helper of
// the same name (manifest_test.go), independently reproduced here per this
// codebase's standing convention for cross-package test fixtures.
func createAsset(t *testing.T, st *store.Store, showID, sequenceID, targetKind, targetID, contentHash, filename string) store.AssetRecord {
	t.Helper()
	rec, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-" + targetKind + "-" + targetID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: targetKind, TargetID: targetID, MediaType: "audio", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return rec
}

// TestAuthorizePerCueAssetGateOnlyRefusesTheCueWithTheMissingAsset proves
// the per-cue asset gate: node-1 holds two Cues. cue-own's own asset is
// uploaded AND present in node-1's own reported inventory. cue-other's own
// asset is a genuinely different, uploaded asset that node-1's report never
// lists. Gating on node-1's WHOLE asset manifest would let cue-other's
// missing asset refuse cue-own too, even though cue-own's own asset is
// present and verified. Authorize instead scopes the asset check to the
// ACTIVATED cue's own resolved outputs.
func TestAuthorizePerCueAssetGateOnlyRefusesTheCueWithTheMissingAsset(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	putAudioCue(t, st, "cue-own", "show-1", "own-seq")
	putAudioCue(t, st, "cue-other", "show-1", "other-seq")

	ownAsset := createAsset(t, st, "show-1", "own-seq", store.AssetTargetKindNode, "node-1", "sha256:own", "Own.wav")
	createAsset(t, st, "show-1", "other-seq", store.AssetTargetKindNode, "node-1", "sha256:other", "Other.wav")

	// node-1's own inventory report is fresh and complete, and holds ONLY
	// cue-own's asset — cue-other's own asset is genuinely missing from it.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "node-1",
		[]store.NodeAssetInventoryRecord{{NodeID: "node-1", ContentHash: ownAsset.ContentHash, RuntimeFilename: ownAsset.RuntimeFilename, SizeBytes: ownAsset.SizeBytes, VerifiedAt: now}},
		store.NodeAssetReportRecord{NodeID: "node-1", ReportedAt: now, Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory: %v", err)
	}

	putPlaylist(t, st, "playlist-own", singleEntryPlaylist("show-1", "inst-own", hash64("a1"), "cue-own", config.ShowPlaylistMismatchPolicyHold, ""))
	putPlaylist(t, st, "playlist-other", singleEntryPlaylist("show-1", "inst-other", hash64("b2"), "cue-other", config.ShowPlaylistMismatchPolicyHold, ""))

	ownResult := resolvedResult("show-1", "playlist-own", 1, "entry-1", "cue-own", 1)
	ownDec, err := Decide(context.Background(), st, ownResult, baseObservation("inst-own"), "inst-own", nil)
	if err != nil {
		t.Fatalf("Decide(cue-own): %v", err)
	}
	ownAct, ok := ownDec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1/cue-own")
	}

	otherResult := resolvedResult("show-1", "playlist-other", 1, "entry-1", "cue-other", 1)
	otherDec, err := Decide(context.Background(), st, otherResult, baseObservation("inst-other"), "inst-other", nil)
	if err != nil {
		t.Fatalf("Decide(cue-other): %v", err)
	}
	otherAct, ok := otherDec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1/cue-other")
	}

	outcome, _, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", ownAct, nil)
	if err != nil {
		t.Fatalf("Authorize(cue-own): %v", err)
	}
	if !ok {
		t.Fatalf("Authorize(cue-own) ok = false (outcome %q), want true: cue-own's own asset is present and verified; "+
			"an unrelated cue's missing asset must never refuse it", outcome)
	}

	outcome, reason, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", otherAct, nil)
	if err != nil {
		t.Fatalf("Authorize(cue-other): %v", err)
	}
	if ok {
		t.Fatal("Authorize(cue-other) ok = true, want false: cue-other's own asset is genuinely missing from node-1's inventory")
	}
	if outcome != cueauth.OutcomeAssetMissing {
		t.Fatalf("outcome = %q, want %q", outcome, cueauth.OutcomeAssetMissing)
	}
	if !strings.Contains(reason, "other-seq") || !strings.Contains(reason, "node-1") || !strings.Contains(reason, "cue-other") {
		t.Fatalf("reason = %q, want it to name the sequence (other-seq), the node (node-1) and the cue (cue-other)", reason)
	}
}

// TestShowPinFreezesActivationIdentityAcrossAMidShowCueEdit is a
// regression test for the incident this type exists to close: an operator
// edits the PLAYING Cue's show.cue object mid-show, and, WITHOUT a pin,
// the coordinator's own next Decide/Authorize pass mints a changed
// CatalogRevision/CueRevision for every Cue in the Show, which is exactly
// what a node's own held (only-explicitly-deployed) catalog cannot follow,
// producing the reported stale-catalog refusal storm. WITH a pin (ADR-033
// show mode), the identical edit must be invisible to Decide and Authorize
// until a fresh pin starts, per Eric's ruling: "the live activation
// identity re-snapshots each show loop" (no pin) versus "the identity
// captured at show start stays authoritative until the show is fully
// stopped and restarted" (pinned).
func TestShowPinFreezesActivationIdentityAcrossAMidShowCueEdit(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	// putLTCCue's own audio output names "asset-cue-1"; genuinely upload
	// and hold it, so this pin-focused test exercises a healthy
	// installation rather than tripping cueAssetsPresent's own
	// never-uploaded-sequence refusal.
	ltcCueAsset := createAsset(t, st, "show-1", "asset-cue-1", store.AssetTargetKindNode, "node-1", "sha256:ltc-cue-1", "Cue1.wav")
	if err := st.ReplaceNodeAssetInventory(context.Background(), "node-1",
		[]store.NodeAssetInventoryRecord{{NodeID: "node-1", ContentHash: ltcCueAsset.ContentHash, RuntimeFilename: ltcCueAsset.RuntimeFilename, SizeBytes: ltcCueAsset.SizeBytes, VerifiedAt: now}},
		store.NodeAssetReportRecord{NodeID: "node-1", ReportedAt: now, Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory: %v", err)
	}
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)
	obs := baseObservation("inst-1")

	t.Run("pinned (show mode): the edit is staged", func(t *testing.T) {
		active, err := assetsync.ResolveActiveShow(context.Background(), st)
		if err != nil {
			t.Fatalf("ResolveActiveShow: %v", err)
		}
		pin := NewShowPin(active)

		decBefore, err := Decide(context.Background(), st, result, obs, "inst-1", pin)
		if err != nil {
			t.Fatalf("Decide (before edit): %v", err)
		}
		before, ok := decBefore.Activations["node-1"]
		if !ok {
			t.Fatalf("no activation built for node-1 before the edit")
		}
		outcome, _, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", before, pin)
		if err != nil {
			t.Fatalf("Authorize (before edit): %v", err)
		}
		if !ok {
			t.Fatalf("Authorize (before edit) refused with outcome %q, want authorized", outcome)
		}

		// The operator edits the PLAYING cue.
		editLTCCue(t, st, "cue-1", "show-1", 9999)

		decAfter, err := Decide(context.Background(), st, result, obs, "inst-1", pin)
		if err != nil {
			t.Fatalf("Decide (after edit): %v", err)
		}
		after, ok := decAfter.Activations["node-1"]
		if !ok {
			t.Fatalf("no activation built for node-1 after the edit")
		}
		if after.CatalogRevision != before.CatalogRevision {
			t.Fatalf("CatalogRevision changed under a pin: before %q, after %q; a mid-show cue edit must be invisible until the show restarts", before.CatalogRevision, after.CatalogRevision)
		}
		if after.CueRevision != before.CueRevision {
			t.Fatalf("CueRevision changed under a pin: before %d, after %d; a mid-show cue edit must be invisible until the show restarts", before.CueRevision, after.CueRevision)
		}

		// Authorize, called again (a fresh independent re-check, exactly as
		// a real dispatch would), must still authorize the SAME pinned
		// identity; this is the stale-catalog refusal storm's own
		// non-occurrence, proven directly rather than inferred from the
		// tuple alone.
		outcome, _, _, ok, err = Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", after, pin)
		if err != nil {
			t.Fatalf("Authorize (after edit, pinned): %v", err)
		}
		if !ok {
			t.Fatalf("Authorize (after edit, pinned) refused with outcome %q, want authorized: a mid-show cue edit must never trigger a stale-catalog refusal under a pin", outcome)
		}
	})

	t.Run("unpinned (program mode): the edit takes effect at the next tick", func(t *testing.T) {
		decBefore, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
		if err != nil {
			t.Fatalf("Decide (before edit): %v", err)
		}
		before := decBefore.Activations["node-1"]

		editLTCCue(t, st, "cue-1", "show-1", 12345)

		decAfter, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
		if err != nil {
			t.Fatalf("Decide (after edit): %v", err)
		}
		after := decAfter.Activations["node-1"]

		if after.CueRevision == before.CueRevision {
			t.Fatalf("CueRevision did not change across an unpinned edit: still %d; program mode must re-snapshot live on its own next tick", before.CueRevision)
		}
		if after.CatalogRevision == before.CatalogRevision {
			t.Fatalf("CatalogRevision did not change across an unpinned edit: still %q; program mode must re-snapshot live on its own next tick", before.CatalogRevision)
		}
	})
}

// TestAuthorizeRefusesASequenceThatWasNeverUploaded proves this coordinator
// refuses a Cue output naming a sequence with NO asset ever uploaded for it
// anywhere — not merely missing from this node's own inventory, but absent
// from the asset store entirely (createAsset is deliberately never called
// for "never-uploaded-seq") — exactly like any other missing asset, never
// treated as present. This is what keeps this coordinator's own dispatch
// gate agreeing with the node's own assetPresent (internal/agent/
// cueactivationops.go), which has always refused an empty filename: before
// this fix, this coordinator authorized the activation and the refusal
// only ever surfaced later, at the node, past the point a readiness check
// could have told an operator anything.
func TestAuthorizeRefusesASequenceThatWasNeverUploaded(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(3000, 0).UTC()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	putAudioCue(t, st, "cue-1", "show-1", "never-uploaded-seq")
	// node-1's own inventory report is fresh and complete, but nothing was
	// ever uploaded for "never-uploaded-seq" anywhere.
	putFreshReport(t, st, "node-1", now)

	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := resolvedResult("show-1", "playlist-1", 1, "entry-1", "cue-1", 1)
	dec, err := Decide(context.Background(), st, result, baseObservation("inst-1"), "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	act, ok := dec.Activations["node-1"]
	if !ok {
		t.Fatalf("no activation built for node-1")
	}

	outcome, reason, _, ok, err := Authorize(context.Background(), st, now, testInterval, time.Time{}, "node-1", act, nil)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if ok {
		t.Fatal("Authorize ok = true, want false: never-uploaded-seq has no asset anywhere, so this activation can never resolve to a runtime file on the node")
	}
	if outcome != cueauth.OutcomeAssetMissing {
		t.Fatalf("outcome = %q, want %q", outcome, cueauth.OutcomeAssetMissing)
	}
	if !strings.Contains(reason, "never-uploaded-seq") || !strings.Contains(reason, "node-1") || !strings.Contains(reason, "cue-1") {
		t.Fatalf("reason = %q, want it to name the sequence (never-uploaded-seq), the node (node-1) and the cue (cue-1)", reason)
	}
}

// --- StateEvidenceBroken: owner ruling 2026-09-02, cue-deactivate-on-jump ---

// TestDecideEvidenceBrokenOutranksResolvedOutcome proves the marker's own
// precedence rule (StateEvidenceBroken's own doc comment): with
// obs.EvidenceBrokenAt set, Decide reaches StateEvidenceBroken even though
// result.Outcome is OutcomeResolved, and Decision.EvidenceBroken carries
// exactly the SAME per-node identity decideResolved would have built from
// this same frozen result — the Cue this coordinator actually activated
// before evidence broke — never Decision.Activations.
func TestDecideEvidenceBrokenOutranksResolvedOutcome(t *testing.T) {
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
	brokenAt := time.Unix(3500, 0).UTC()
	obs.EvidenceBrokenAt = &brokenAt

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateEvidenceBroken {
		t.Fatalf("State = %q, want %q (the marker must outrank OutcomeResolved)", dec.State, StateEvidenceBroken)
	}
	if len(dec.Activations) != 0 {
		t.Fatalf("Activations = %+v, want empty: an evidence-broken decision must never be dispatched as a cue.activate", dec.Activations)
	}
	if len(dec.ClearNodes) != 0 {
		t.Fatalf("ClearNodes = %+v, want empty: this is not an H0.2 mismatch effect", dec.ClearNodes)
	}
	act, ok := dec.EvidenceBroken["node-1"]
	if !ok {
		t.Fatalf("no EvidenceBroken entry for node-1; EvidenceBroken = %+v", dec.EvidenceBroken)
	}
	if act.CueID != "cue-1" || act.Show != "show-1" {
		t.Errorf("EvidenceBroken[node-1] = %+v, want the Cue actually resolved before evidence broke (cue-1/show-1)", act)
	}
	if !strings.Contains(dec.Reason, "sequence-regression") {
		t.Errorf("Reason = %q, want it to name the sequence-regression discontinuity", dec.Reason)
	}
}

// TestDecideEvidenceBrokenOutranksIdentityUnavailable proves the marker
// check runs before EVEN the identity-unavailable/unbound early returns,
// not merely before the mismatch/resolved routing further down.
func TestDecideEvidenceBrokenOutranksIdentityUnavailable(t *testing.T) {
	st := openTestStore(t)
	result := fppreconcile.Result{Outcome: fppreconcile.OutcomeIdentityUnavailable, Reason: "FPP could not establish identity"}
	obs := baseObservation("inst-1")
	brokenAt := time.Unix(3500, 0).UTC()
	obs.EvidenceBrokenAt = &brokenAt

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateEvidenceBroken {
		t.Fatalf("State = %q, want %q", dec.State, StateEvidenceBroken)
	}
	if len(dec.EvidenceBroken) != 0 {
		t.Fatalf("EvidenceBroken = %+v, want empty: OutcomeIdentityUnavailable never resolved a Cue to undo", dec.EvidenceBroken)
	}
}

// TestDecideEvidenceBrokenWithNonResolvedOutcomeHasNothingToUndo proves the
// "nothing was cleanly activated to begin with" case: a broken marker on a
// row whose own last resolution was a mismatch outcome (never resolved to
// an authorized Cue) reaches StateEvidenceBroken with an empty
// EvidenceBroken map, not an error and not a guess.
func TestDecideEvidenceBrokenWithNonResolvedOutcomeHasNothingToUndo(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putLTCCue(t, st, "cue-1", "show-1")
	putPlaylist(t, st, "playlist-1", singleEntryPlaylist("show-1", "inst-1", hash64("a1"), "cue-1", config.ShowPlaylistMismatchPolicyHold, ""))

	result := fppreconcile.Result{
		Outcome: fppreconcile.OutcomeStaleImport, Reason: "the FPP playlist was edited and re-imported",
		PlaylistID: "playlist-1", PlaylistRevision: 1, Show: "show-1",
	}
	obs := baseObservation("inst-1")
	brokenAt := time.Unix(3500, 0).UTC()
	obs.EvidenceBrokenAt = &brokenAt

	dec, err := Decide(context.Background(), st, result, obs, "inst-1", nil)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.State != StateEvidenceBroken {
		t.Fatalf("State = %q, want %q", dec.State, StateEvidenceBroken)
	}
	if len(dec.EvidenceBroken) != 0 {
		t.Fatalf("EvidenceBroken = %+v, want empty: nothing was resolved from this evidence before it broke", dec.EvidenceBroken)
	}
}

// --- cueAssetsPresent: reconnect-staleness allowance (SM-521) ---
//
// The reported incident: the coordinator lost its own broker connection for
// several minutes, reconnected cleanly, and then refused Wake Up for about
// a minute because it counted its OWN disconnected time against
// showmesh-node-01's inventory-report staleness window — the node never
// lost a file and kept the show running throughout. These tests exercise
// cueAssetsPresent directly (a synthetic cuecatalog.Entry with no declared
// Render/Audio output) so only the report-freshness branch this fix
// touches is under test, never the asset-hash branches beneath it.

func TestCueAssetsPresentStaleWhileConnectedStillRefused(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(10000, 0).UTC()
	reportedAt := now.Add(-4 * testInterval) // older than StalenessWindow (3x)

	// The coordinator's own last reconnect was long before this report was
	// even received: it was connected the whole time the node stopped
	// reporting, so this is a genuine staleness, not an outage artifact,
	// and must refuse exactly as it always has.
	reconnectedAt := reportedAt.Add(-10 * testInterval)
	putFreshReport(t, st, "node-1", reportedAt)

	present, reason, err := cueAssetsPresent(context.Background(), st, now, testInterval, reconnectedAt, "node-1", cuecatalog.Entry{CueID: "cue-1"})
	if err != nil {
		t.Fatalf("cueAssetsPresent: %v", err)
	}
	if present {
		t.Fatal("present = true, want false: the report is genuinely stale and the coordinator was connected throughout")
	}
	want := fmt.Sprintf("cue %q: node %q's last asset inventory report (%s) is older than the staleness window; a stale report is not evidence of what the node currently holds",
		"cue-1", "node-1", reportedAt.Format(time.RFC3339))
	if reason != want {
		t.Fatalf("reason = %q, want %q (unchanged text)", reason, want)
	}
}

func TestCueAssetsPresentNeverReportedStillRefused(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(10000, 0).UTC()

	// A very recent reconnect must not turn "never reported" into
	// "trusted" — the allowance only ever extends a freshness deadline
	// that a report must already exist to have.
	reconnectedAt := now.Add(-1 * time.Second)

	present, reason, err := cueAssetsPresent(context.Background(), st, now, testInterval, reconnectedAt, "node-1", cuecatalog.Entry{CueID: "cue-1"})
	if err != nil {
		t.Fatalf("cueAssetsPresent: %v", err)
	}
	if present {
		t.Fatal("present = true, want false: node-1 has never reported its asset inventory at all")
	}
	want := `cue "cue-1": node "node-1" has never reported its asset inventory, so it cannot be trusted to hold anything`
	if reason != want {
		t.Fatalf("reason = %q, want %q (unchanged text)", reason, want)
	}
}

func TestCueAssetsPresentNeverConnectedNoOpenEndedAllowance(t *testing.T) {
	st := openTestStore(t)
	now := time.Unix(10000, 0).UTC()
	reportedAt := now.Add(-4 * testInterval)
	putFreshReport(t, st, "node-1", reportedAt)

	// Zero value: this coordinator has never connected to the broker at
	// all (a fresh start against an unreachable broker). It must not read
	// as "just reconnected" and grant an allowance.
	present, reason, err := cueAssetsPresent(context.Background(), st, now, testInterval, time.Time{}, "node-1", cuecatalog.Entry{CueID: "cue-1"})
	if err != nil {
		t.Fatalf("cueAssetsPresent: %v", err)
	}
	if present {
		t.Fatal("present = true, want false: a coordinator that has never connected must not get an open-ended staleness allowance")
	}
	want := fmt.Sprintf("cue %q: node %q's last asset inventory report (%s) is older than the staleness window; a stale report is not evidence of what the node currently holds",
		"cue-1", "node-1", reportedAt.Format(time.RFC3339))
	if reason != want {
		t.Fatalf("reason = %q, want %q (unchanged text)", reason, want)
	}
}

// TestCueAssetsPresentReconnectAllowanceGrantsOneIntervalThenExpires is the
// fix itself: a report received before an outage, now individually stale,
// is not refused for one inventoryInterval after this coordinator's own
// reconnect — but the allowance is not permanent immunity, and a node that
// stays silent past that one interval is refused again.
func TestCueAssetsPresentReconnectAllowanceGrantsOneIntervalThenExpires(t *testing.T) {
	st := openTestStore(t)
	base := time.Unix(10000, 0).UTC()
	reportedAt := base.Add(-4 * testInterval) // already stale by base
	reconnectedAt := base.Add(-30 * time.Second)
	putFreshReport(t, st, "node-1", reportedAt)

	// Grant: only 30s of this coordinator's own one-interval grace period
	// has elapsed since reconnect.
	present, reason, err := cueAssetsPresent(context.Background(), st, base, testInterval, reconnectedAt, "node-1", cuecatalog.Entry{CueID: "cue-1"})
	if err != nil {
		t.Fatalf("cueAssetsPresent (within grace): %v", err)
	}
	if !present {
		t.Fatalf("present = false (reason %q), want true: the node's own report predates a reconnect that happened within one inventoryInterval of now", reason)
	}

	// Expire: the SAME reconnect, now two intervals in the past — the
	// node still never reported, so the allowance has run out.
	later := reconnectedAt.Add(2 * testInterval)
	present, reason, err = cueAssetsPresent(context.Background(), st, later, testInterval, reconnectedAt, "node-1", cuecatalog.Entry{CueID: "cue-1"})
	if err != nil {
		t.Fatalf("cueAssetsPresent (after grace expires): %v", err)
	}
	if present {
		t.Fatal("present = true, want false: the reconnect allowance grants one inventoryInterval, not permanent immunity")
	}
	want := fmt.Sprintf("cue %q: node %q's last asset inventory report (%s) is older than the staleness window; a stale report is not evidence of what the node currently holds",
		"cue-1", "node-1", reportedAt.Format(time.RFC3339))
	if reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}
