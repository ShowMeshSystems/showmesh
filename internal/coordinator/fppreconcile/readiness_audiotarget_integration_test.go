package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// twoTargetAudioPlaylist builds a minimal fpp-runner show.playlist bound to
// a single cue declaring outputs.audio.targets = targets, wiring every
// store row [PlaylistReadiness] needs upstream of conditions 10-12 (a
// stored definition, a matching observation) so a test can isolate the
// audio-target/asset/clock conditions instead of also exercising 1-9.
func twoTargetAudioPlaylist(t *testing.T, st *store.Store, showID, cueID, asset string, targets []string) config.ShowPlaylistPayload {
	t.Helper()
	putCueWithOutputs(t, st, cueID, showID, config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: asset, Targets: targets},
	})
	hash := hash64("a1")
	p := config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)

	obs := baseObservation("inst-1")
	obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash, "mainPlaylist", 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	putObservation(t, st, obs)
	return p
}

// ackNodeCatalog acknowledges nodeID's current catalog revision for the
// active show, satisfying condition 9 for a node this playlist's cue
// resolves any output to, so the tests below can isolate conditions
// 10-12 rather than also proving condition 9.
func ackNodeCatalog(t *testing.T, ctx context.Context, st *store.Store, nodeID string) {
	t.Helper()
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("ResolveCueCatalog (%s): %v", nodeID, err)
	}
	if err := st.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
		NodeID: nodeID, Revision: catalog.Revision, ShowID: active.ShowID, Generation: active.Generation,
	}); err != nil {
		t.Fatalf("put node cue catalog ack (%s): %v", nodeID, err)
	}
}

// putNodeAudioAsset creates a current node-targeted asset for sequence on
// nodeID, and, when holds, gives that node a fresh complete inventory
// report actually holding it -- exactly one of the two things
// [assetsync.ComputeNodeManifest] needs to report the asset Held rather
// than Missing.
func putNodeAudioAsset(t *testing.T, ctx context.Context, st *store.Store, showID, nodeID, sequence, contentHash string, holds bool) {
	t.Helper()
	if _, _, err := st.CreateAsset(ctx, store.AssetRecord{
		ID: "asset-" + nodeID + "-" + sequence, ShowID: showID, SequenceID: sequence,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: contentHash, RuntimeFilename: sequence + ".wav", SizeBytes: 2048,
		Backend: "volume", StorageKey: contentHash,
	}); err != nil {
		t.Fatalf("create asset for %s: %v", nodeID, err)
	}
	inventory := []store.NodeAssetInventoryRecord{}
	if holds {
		inventory = []store.NodeAssetInventoryRecord{{
			NodeID: nodeID, ContentHash: contentHash, RuntimeFilename: sequence + ".wav", SizeBytes: 2048, VerifiedAt: time.Now(),
		}}
	}
	if err := st.ReplaceNodeAssetInventory(ctx, nodeID, inventory,
		store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory for %s: %v", nodeID, err)
	}
}

// TestPlaylistReadinessAssetsMissingNamesExactTargetAmongTwo proves ADR-049
// decision 5's asset rule at the full [PlaylistReadiness] level: a Cue
// naming two audio targets, one of which lacks the asset, fails naming
// exactly that node -- the other, fully-provisioned target is never named.
func TestPlaylistReadinessAssetsMissingNamesExactTargetAmongTwo(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")

	p := twoTargetAudioPlaylist(t, st, "show-1", "cue-1", "shared-audio", []string{"m4", "pi"})

	putNodeAudioAsset(t, ctx, st, "show-1", "m4", "shared-audio", "sha256:m4-ok", true)
	putNodeAudioAsset(t, ctx, st, "show-1", "pi", "shared-audio", "sha256:pi-ok", false)

	ackNodeCatalog(t, ctx, st, "m4")
	ackNodeCatalog(t, ctx, st, "pi")

	report, err := PlaylistReadiness(ctx, st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatalf("Ready = true, want false: pi lacks the asset m4 already holds")
	}
	if report.FailingCondition != ReadinessAssetsMissing {
		t.Fatalf("FailingCondition = %q, want %q (reason: %s)", report.FailingCondition, ReadinessAssetsMissing, report.Reason)
	}
	if !strings.Contains(report.Reason, "pi") {
		t.Errorf("Reason = %q, want it to name pi", report.Reason)
	}
	if strings.Contains(report.Reason, "m4") {
		t.Errorf("Reason = %q, want it to NOT name m4: m4 already holds its asset", report.Reason)
	}
}

// TestPlaylistReadinessUnlockedClockNeverHidesAssetFailureOnSameNode
// proves ADR-049 decision 5's own ordering guarantee: an unlocked clock on
// a target never masks a missing-asset failure on that SAME node -- the
// failure is what PlaylistReadiness reports.
func TestPlaylistReadinessUnlockedClockNeverHidesAssetFailureOnSameNode(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")

	p := twoTargetAudioPlaylist(t, st, "show-1", "cue-1", "shared-audio", []string{"m4", "pi"})

	putNodeAudioAsset(t, ctx, st, "show-1", "m4", "shared-audio", "sha256:m4-ok", true)
	putNodeAudioAsset(t, ctx, st, "show-1", "pi", "shared-audio", "sha256:pi-ok", false)

	ackNodeCatalog(t, ctx, st, "m4")
	ackNodeCatalog(t, ctx, st, "pi")

	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", "unsynchronized", now, 45*time.Second)},
	}

	report, err := PlaylistReadiness(ctx, st, nil, clock, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: pi's asset is missing regardless of its clock state")
	}
	if report.FailingCondition != ReadinessAssetsMissing {
		t.Fatalf("FailingCondition = %q, want %q (reason: %s)", report.FailingCondition, ReadinessAssetsMissing, report.Reason)
	}
	if !strings.Contains(report.Reason, "pi") {
		t.Errorf("Reason = %q, want it to name pi", report.Reason)
	}
}

// TestPlaylistReadinessUnlockedClockWarnsWithoutFailingWhenAssetsAreHeld
// proves the warning form on its own: both targets hold their asset, but
// pi's clock is unlocked, so PlaylistReadiness stays Ready and reports the
// clock warning naming pi.
func TestPlaylistReadinessUnlockedClockWarnsWithoutFailingWhenAssetsAreHeld(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")

	p := twoTargetAudioPlaylist(t, st, "show-1", "cue-1", "shared-audio", []string{"m4", "pi"})

	putNodeAudioAsset(t, ctx, st, "show-1", "m4", "shared-audio", "sha256:m4-ok", true)
	putNodeAudioAsset(t, ctx, st, "show-1", "pi", "shared-audio", "sha256:pi-ok", true)

	ackNodeCatalog(t, ctx, st, "m4")
	ackNodeCatalog(t, ctx, st, "pi")

	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", "unsynchronized", now, 45*time.Second)},
	}

	report, err := PlaylistReadiness(ctx, st, nil, clock, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s): an unlocked clock is a warning, not a failure", report.FailingCondition, report.Reason)
	}
	if !strings.Contains(report.Warning, "pi") {
		t.Errorf("Warning = %q, want it to name pi", report.Warning)
	}
}
