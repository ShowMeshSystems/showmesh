package fppreconcile

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Track H seam H6 named several readiness conditions with no
// representation in the coordinator at all: a Playlist whose node has not
// acknowledged the active show's cue catalog, or whose Show authors a
// colliding H0.5 exclusive claim, reported ready. These tests are the
// pre-fix reproduction (kept as permanent regression coverage): each
// documents the exact H3/H0.5 evidence that already existed and proves
// PlaylistReadiness now reads it.

func TestPlaylistReadinessNodeCatalogStale(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	// cue-1 (from singleEntryPlaylist) declares only render, so give
	// node-1 a surface so it actually participates in this show's catalog.
	putSurface(t, st, "surface-1", "show-1", "node-1")

	// node-1 NEVER acknowledges any cue-catalog revision at all.

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: node-1 has never acknowledged any cue-catalog revision")
	}
	if report.FailingCondition != ReadinessNodeCatalogStale {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeCatalogStale)
	}
	if !containsAll(report.Reason, "node-1", "catalog-unacknowledged") {
		t.Fatalf("Reason = %q, want it to name node-1 and its catalog-unacknowledged status", report.Reason)
	}
}

func TestPlaylistReadinessNodeCatalogStaleWrongAcknowledgedRevision(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putSurface(t, st, "surface-1", "show-1", "node-1")

	// node-1 acknowledges SOME revision, but not the one the coordinator
	// currently resolves for this show/generation/node.
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "node-1", Revision: "stale-revision-from-a-prior-edit", ShowID: "show-1", Generation: 1,
	}); err != nil {
		t.Fatalf("put node cue catalog ack: %v", err)
	}

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: node-1's acknowledged revision does not match the currently resolved catalog revision")
	}
	if report.FailingCondition != ReadinessNodeCatalogStale {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessNodeCatalogStale)
	}
	if !containsAll(report.Reason, "node-1", "catalog-stale", "stale-revision-from-a-prior-edit") {
		t.Fatalf("Reason = %q, want it to name node-1, catalog-stale, and its stale acknowledged revision", report.Reason)
	}
}

// TestPlaylistReadinessNodeCatalogCurrentPasses proves a node that HAS
// acknowledged the exact revision the coordinator currently resolves does
// not fail this condition — the positive case, so a fix that always
// fails (rather than genuinely comparing revisions) cannot pass silently.
func TestPlaylistReadinessNodeCatalogCurrentPasses(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
	putPlaylist(t, st, "playlist-1", p)
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")
	putSurface(t, st, "surface-1", "show-1", "node-1")

	active, err := assetsync.ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := assetsync.ResolveCueCatalog(context.Background(), st, active, "node-1")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "node-1", Revision: catalog.Revision, ShowID: "show-1", Generation: active.Generation,
	}); err != nil {
		t.Fatalf("put node cue catalog ack: %v", err)
	}

	obs := baseObservation("inst-1")
	obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash, "mainPlaylist", 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	putObservation(t, st, obs)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false, want true (failing condition %q: %s)", report.FailingCondition, report.Reason)
	}
}

// TestPlaylistReadinessNodeCatalogStaleSkippedWhenPlaylistShowNotActive
// proves the condition is skipped (not falsely failed) when p.Show is not
// the currently active show: nodes only ever hold a catalog for the
// active show, so there is nothing correct to compare against. show-2 (the
// REAL active show) has its own node-2, which participates in show-2's
// catalog and has never acknowledged anything -- a mutation that dropped
// the "p.Show == active.ShowID" guard would have this genuinely-stale,
// but entirely unrelated, node incorrectly fail playlist-1's readiness.
func TestPlaylistReadinessNodeCatalogStaleSkippedWhenPlaylistShowNotActive(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putShow(t, st, "show-2", "Show Two")
	putActiveShow(t, st, "show-2")
	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)
	// No node ever declared or acknowledged anything for show-1.

	// show-2 (the REAL active show) has its own participating, never-
	// acknowledged node -- proof this test would fail without the guard.
	putCue(t, st, "cue-2", "show-2")
	putAudioNode(t, st, "node-2")
	declareNode(t, st, "node-2")
	putSurface(t, st, "surface-2", "show-2", "node-2")
	p2 := simpleFPPPlaylist("show-2", "inst-2", hash64("a2"), "cue-2")
	putPlaylist(t, st, "playlist-2", p2)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.FailingCondition == ReadinessNodeCatalogStale {
		t.Fatalf("node-catalog-stale must not fire for a Playlist whose Show is not the currently active show: reason %q", report.Reason)
	}
}

// simpleFPPPlaylist builds a minimal one-entry fpp-runner show.playlist
// payload referencing an already-stored Cue, without also writing a Cue
// the way singleEntryPlaylist does (its caller owns Cue setup).
func simpleFPPPlaylist(showID, instanceUUID, playlistHash, cueID string) config.ShowPlaylistPayload {
	return config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: playlistHash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
}

func TestPlaylistReadinessExclusiveClaimConflict(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	// cue-1 (from singleEntryPlaylist) declares only render; give it an
	// audio output too, and a second, unrelated Cue that collides with it
	// on program-audio-route (both declare audio, neither declares
	// announcement, and they share no playlist).
	putCueWithAudio(t, st, "cue-1", "show-1")
	putCueWithAudio(t, st, "cue-2", "show-1")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)

	p2 := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Second", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Second", PlaylistHash: hash64("a2")},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-2", Cue: "cue-2",
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putPlaylist(t, st, "playlist-2", p2)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.Ready {
		t.Fatal("Ready = true, want false: cue-1 and cue-2 collide on program-audio-route:node-1:usb-interface")
	}
	if report.FailingCondition != ReadinessExclusiveClaimConflict {
		t.Fatalf("FailingCondition = %q, want %q", report.FailingCondition, ReadinessExclusiveClaimConflict)
	}
	if !containsAll(report.Reason, "cue-1", "cue-2", "program-audio-route:node-1:usb-interface") {
		t.Fatalf("Reason = %q, want it to name both cues and the exact claim string", report.Reason)
	}
}

// TestPlaylistReadinessExclusiveClaimConflictExemptedWithinOnePlaylist
// proves two Cues of the SAME Playlist (never concurrently active with
// each other) do not falsely collide -- assetsync's own sameSinglePlaylist
// exemption, which this condition must not bypass.
func TestPlaylistReadinessExclusiveClaimConflictExemptedWithinOnePlaylist(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	hash := hash64("a1")
	putCueWithAudio(t, st, "cue-1", "show-1")
	putCueWithAudio(t, st, "cue-2", "show-1")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	p := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: "cue-1", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-2", Cue: "cue-2", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	}
	putPlaylist(t, st, "playlist-1", p)

	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if report.FailingCondition == ReadinessExclusiveClaimConflict {
		t.Fatalf("exclusive-claim-conflict must not fire between two entries of the SAME Playlist (never concurrently active): reason %q", report.Reason)
	}
}

// TestPlaylistReadinessUndecodableCueDoesNotFailUnrelatedPlaylist is
// DEFECT 1's regression coverage (PR #163 review): exclusiveClaimReadiness
// used to propagate assetsync.ResolveCueCatalog's decode error straight
// out of PlaylistReadiness as a caller-visible error (an HTTP 500 at the
// read route) whenever ANY show.cue anywhere in the store failed to
// decode -- including a cue belonging to a completely different Show than
// the one being asked about. Readiness must degrade, not explode: a
// corrupted cue this Playlist's Show has nothing to do with must not stop
// this Playlist's readiness from answering at all.
func TestPlaylistReadinessUndecodableCueDoesNotFailUnrelatedPlaylist(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putShow(t, st, "show-2", "Show Two")
	// show-1 is deliberately left NOT the active show: exclusiveClaimReadiness
	// is evaluated against p.Show directly regardless of show.active, but
	// leaving show.active unconfigured keeps nodeCatalogReadiness (which
	// DOES depend on the real active show, and does not carry this same
	// fix) out of this test entirely, so this test exercises exactly the
	// one condition DEFECT 1 is about.
	declareNode(t, st, "node-1")

	hash := hash64("a1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	putPlaylist(t, st, "playlist-1", p)

	// A Cue belonging to an ENTIRELY DIFFERENT Show, corrupted the same
	// "an interrupted write could really leave this behind" way
	// TestPlaylistReadinessCorruptedCueRevisionLogsWarnAndStillReportsCueNotReady
	// (readiness_test.go) uses. assetsync.ResolveCueCatalog decodes every
	// stored show.cue in the WHOLE store before it ever filters by show,
	// so this cue -- which playlist-1 does not reference and whose Show is
	// not even p.Show -- still trips the SAME decode failure every node's
	// catalog resolution hits.
	ctx := context.Background()
	putCue(t, st, "cue-corrupt", "show-2")
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: "cue-corrupt", Revision: 2,
		PayloadJSON: `{"show":"show-2","name":"Corrupted","outputs":{`, Source: "api",
	}); err != nil {
		t.Fatalf("create corrupted cue revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, "cue-corrupt", 2); err != nil {
		t.Fatalf("activate corrupted cue revision: %v", err)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	report, err := PlaylistReadiness(ctx, st, logger, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v, want no error -- one corrupted cue in an unrelated Show must not fail every OTHER playlist's readiness", err)
	}
	if report.FailingCondition == ReadinessExclusiveClaimConflict {
		t.Fatalf("FailingCondition = %q, want anything but a false-positive conflict report: a corrupted cue proves nothing about a real collision", report.FailingCondition)
	}
	if !containsAll(report.Warning, "cue-corrupt", "could not be decoded") {
		t.Fatalf("Warning = %q, want it to name the offending cue cue-corrupt and explain it could not be decoded", report.Warning)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("log output = %q, want a WARN-level entry", logged)
	}
	if !strings.Contains(logged, "cue-corrupt") {
		t.Errorf("log output = %q, want it to name cue id %q", logged, "cue-corrupt")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func putCueWithAudio(t *testing.T, st *store.Store, cueID, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	// Overwrite whatever putCue/singleEntryPlaylist already wrote (both
	// write revision 1 for a fresh cue id).
	ctx := context.Background()
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	nextRev := int64(1)
	if err == nil {
		nextRev = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowCueConfigKind, ObjectID: cueID, Revision: nextRev, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.cue/%s: %v", cueID, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowCueConfigKind, cueID, nextRev); err != nil {
		t.Fatalf("activate config revision show.cue/%s: %v", cueID, err)
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

func putSurface(t *testing.T, st *store.Store, surfaceID, showID, nodeID string) {
	t.Helper()
	payload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: surfaceID, Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode show.surface payload: %v", err)
	}
	putConfig(t, st, config.ShowSurfaceConfigKind, surfaceID, payload)
}

func declareNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if _, err := st.DeclareNode(context.Background(), store.NodeDeclarationRecord{NodeID: nodeID, Label: nodeID}); err != nil {
		t.Fatalf("declare node %q: %v", nodeID, err)
	}
}
