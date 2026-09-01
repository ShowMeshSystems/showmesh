package assetsync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// putConfig writes payload as revision 1 of (kind, id) and activates it —
// this package's own minimal fixture for the identity.Service.AuditedWrite
// round trip a real write handler performs, since these tests exercise
// only the read side.
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
	putConfig(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, payload)
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

func createAsset(t *testing.T, st *store.Store, showID, sequenceID, targetKind, targetID, contentHash, filename string) store.AssetRecord {
	t.Helper()
	rec, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-" + targetKind + "-" + targetID, ShowID: showID, SequenceID: sequenceID,
		TargetKind: targetKind, TargetID: targetID, MediaType: "fseq", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return rec
}

func TestResolveActiveShowNoneConfigured(t *testing.T) {
	st := openTestStore(t)
	active, err := ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow() error = %v, want nil", err)
	}
	if active.Configured {
		t.Fatalf("ResolveActiveShow() = %+v, want Configured=false when nothing has ever been activated", active)
	}
}

func TestResolveActiveShowConfigured(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow() error = %v, want nil", err)
	}
	if !active.Configured || active.ShowID != "halloween-2026" {
		t.Fatalf("ResolveActiveShow() = %+v, want Configured=true ShowID=halloween-2026", active)
	}
}

func TestExpectedAssetsForNodeCombinesNodeAndShowTargets(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "render-01")

	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	createAsset(t, st, "halloween-2026", "audio-track", store.AssetTargetKindShow, "", "sha256:bbb", "track.mp3")
	// Targeted at a DIFFERENT node — must not appear in render-01's set.
	createAsset(t, st, "halloween-2026", "other", store.AssetTargetKindNode, "render-02", "sha256:ccc", "Other.fseq")

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v, want nil", err)
	}
	if len(got.Assets) != 2 {
		t.Fatalf("ExpectedAssetsForNode() Assets = %+v, want exactly 2 (node-targeted + show-wide)", got.Assets)
	}
	hashes := map[string]bool{}
	for _, a := range got.Assets {
		hashes[a.ContentHash] = true
	}
	if !hashes["sha256:aaa"] || !hashes["sha256:bbb"] {
		t.Errorf("ExpectedAssetsForNode() Assets = %+v, want sha256:aaa (node-targeted) and sha256:bbb (show-wide)", got.Assets)
	}
	if hashes["sha256:ccc"] {
		t.Errorf("ExpectedAssetsForNode() Assets = %+v, want NOT to include a different node's asset", got.Assets)
	}
}

// TestExpectedAssetsForNodeGapsSurfacedNodeMissingSequence proves §4.1
// point 3's stated-gap rule as this package implements it (see
// [TestExpectedAssetsForNodeGapsScopedToNodesOwnCues] below for the
// narrower scoping this rule is limited to): a node with a surface and
// zero coverage for a sequence its OWN Cue references is named as a gap,
// even when the show's only current asset for that sequence belongs to a
// different node entirely.
func TestExpectedAssetsForNodeGapsSurfacedNodeMissingSequence(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")
	putSurface(t, st, "garage-surface", "halloween-2026", "render-02")

	// render-02's own Cue references "opening" — the sequence this test's
	// gap must legitimately be about.
	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "opening-cue",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "opening"}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "opening-cue", cuePayload)
	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "Main", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		Entries: []config.ShowPlaylistEntry{{ID: "e1", Cue: "opening-cue"}},
	})
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, "playlist-1", playlistPayload)

	// The show has an "opening" sequence, but only for render-01 — render-02
	// has a surface and its own Cue names "opening", so it should read a
	// gap for it.
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-02")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v, want nil", err)
	}
	if len(got.Assets) != 0 {
		t.Fatalf("ExpectedAssetsForNode(render-02) Assets = %+v, want none (nothing targets render-02)", got.Assets)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].SequenceID != "opening" {
		t.Fatalf("ExpectedAssetsForNode(render-02) Gaps = %+v, want one gap naming sequence %q", got.Gaps, "opening")
	}
	found := false
	for _, id := range got.Gaps[0].SurfaceIDs {
		if id == "garage-surface" {
			found = true
		}
	}
	if !found {
		t.Errorf("ExpectedAssetsForNode(render-02) Gaps[0].SurfaceIDs = %v, want it to name %q", got.Gaps[0].SurfaceIDs, "garage-surface")
	}
}

// TestExpectedAssetsForNodeGapsScopedToNodesOwnCues proves gap detection
// is scoped to the surfaced node's OWN cues: render-01 has a surface and
// its own Cue, referencing sequence "own-seq", whose own asset IS uploaded
// and covered. The show ALSO has a completely unrelated audio-only
// sequence, "audio-only-elsewhere", with a current asset uploaded to a
// DIFFERENT node — no Cue anywhere references it for render-01, and
// render-01 (no audio.node) could never even use an audio sequence.
// Scanning every sequence the show has ANY asset for, regardless of which
// node's cues actually reference it, would flag render-01 with zero
// coverage of "audio-only-elsewhere" even though nothing render-01 does
// ever needs it.
func TestExpectedAssetsForNodeGapsScopedToNodesOwnCues(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "audio-01")
	putSurface(t, st, "render-01-surface", "halloween-2026", "render-01")

	renderCuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "own",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "own-seq"}},
	})
	if err != nil {
		t.Fatalf("encode render cue: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "cue-own", renderCuePayload)

	audioCuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "elsewhere",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "audio-only-elsewhere"}},
	})
	if err != nil {
		t.Fatalf("encode audio cue: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "cue-elsewhere", audioCuePayload)

	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "Main", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		Entries: []config.ShowPlaylistEntry{
			{ID: "e1", Cue: "cue-own"},
			{ID: "e2", Cue: "cue-elsewhere"},
		},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, "playlist-1", playlistPayload)

	createAsset(t, st, "halloween-2026", "own-seq", store.AssetTargetKindNode, "render-01", "sha256:own", "Own.fseq")
	createAsset(t, st, "halloween-2026", "audio-only-elsewhere", store.AssetTargetKindNode, "audio-01", "sha256:elsewhere", "elsewhere.wav")

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	for _, gap := range got.Gaps {
		if gap.SequenceID == "audio-only-elsewhere" {
			t.Fatalf("Gaps = %+v, want NO gap for %q: render-01 has no Cue referencing an audio-only sequence that belongs to a different node", got.Gaps, "audio-only-elsewhere")
		}
	}
	if len(got.Gaps) != 0 {
		t.Errorf("Gaps = %+v, want none: render-01's own cue's sequence (own-seq) is covered", got.Gaps)
	}
}

// TestExpectedAssetsForNodeNoGapWithoutSurface proves a node with no
// surface at all never reports a gap, even when the show has sequences it
// holds nothing for — a node nobody assigned to render anything is not
// missing anything.
func TestExpectedAssetsForNodeNoGapWithoutSurface(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")

	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-02")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v, want nil", err)
	}
	if len(got.Gaps) != 0 {
		t.Fatalf("ExpectedAssetsForNode(render-02) Gaps = %+v, want none: render-02 has no surface", got.Gaps)
	}
}

// TestExpectedAssetsForNodeGapsReachAudioSequenceThroughAudioBranch proves
// [NodeCueSequenceIDs]'s audio branch (added alongside PR #209's target
// resolution -- resolving a Cue's audio, LTC and announcement outputs to
// their target node) is a real, exercised consumer of "which sequences
// does this node's own Cues reference": a node holding a show.surface
// (required to reach gap detection at all -- see
// [TestExpectedAssetsForNodeNoGapWithoutSurface]) that ALSO holds an
// audio.node, whose own audio-only Cue names a sequence with NO current
// asset anywhere, reads that sequence as a Gap. Dropping or misordering
// NodeCueSequenceIDs' audio branch removes this Gap silently, which is
// exactly the failure this test exists to catch: this is the only place
// NodeCueSequenceIDs' return value is ever consumed (ExpectedAssetsForNode's
// own Gaps computation), and Gaps itself is read only by the asset-manifest
// API route and showmeshctl assets output (internal/coordinator/api/
// assetmanifest.go, cmd/showmeshctl/cmd_assets.go) -- readiness
// (fppreconcile.assetsMissingReadiness) deliberately never reads Gaps at
// all, so it is not and cannot be this branch's consumer.
func TestExpectedAssetsForNodeGapsReachAudioSequenceThroughAudioBranch(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "hybrid-01")
	putSurface(t, st, "hybrid-01-surface", "halloween-2026", "hybrid-01")
	putAudioNode(t, st, "hybrid-01")

	audioCuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "audio-only",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "audio-seq-never-uploaded"}},
	})
	if err != nil {
		t.Fatalf("encode audio cue: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, "cue-audio", audioCuePayload)

	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "Main", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		Entries: []config.ShowPlaylistEntry{{ID: "e1", Cue: "cue-audio"}},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, "playlist-1", playlistPayload)

	// No asset is ever created for "audio-seq-never-uploaded" -- the Gap
	// this test asserts on comes entirely from the audio branch adding it
	// to hybrid-01's own referenced-sequence set, never from any asset row.

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "hybrid-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v, want nil", err)
	}
	if len(got.Gaps) != 1 || got.Gaps[0].SequenceID != "audio-seq-never-uploaded" {
		t.Fatalf("ExpectedAssetsForNode(hybrid-01) Gaps = %+v, want one gap naming %q", got.Gaps, "audio-seq-never-uploaded")
	}
}

func TestStalenessWindowIsThreeTimesInventoryInterval(t *testing.T) {
	got := StalenessWindow(2 * time.Minute)
	want := 6 * time.Minute
	if got != want {
		t.Errorf("StalenessWindow(2m) = %s, want %s", got, want)
	}
}

// --- ComputeNodeManifest: every state and every unknown cause ---

func TestComputeNodeManifestNoActiveShow(t *testing.T) {
	m := ComputeNodeManifest("render-01", ActiveShow{Configured: false}, ExpectedSet{}, nil, false, nil)
	if m.State != ManifestUnknown || m.UnknownCause != UnknownCauseNoActiveShow {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=unknown Cause=no_active_show", m)
	}
}

func TestComputeNodeManifestNeverReported(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	m := ComputeNodeManifest("render-01", active, ExpectedSet{}, nil, false, nil)
	if m.State != ManifestUnknown || m.UnknownCause != UnknownCauseNeverReported {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=unknown Cause=never_reported", m)
	}
}

// W1: inventory is deliberately NON-EMPTY (and does not match expected, so
// it would render as Extra if the stale-report short-circuit were skipped).
// The original version of this test passed inventory=nil, which made
// "len(m.Extra) != 0" trivially always false regardless of whether the
// stale-report check actually ran before populating Extra — a mutation
// that removed or reordered that check would still pass. A real inventory
// makes the assertion mean what it says: what a stale report claims a node
// HOLDS is exactly as unreliable as what it claims a node LACKS.
func TestComputeNodeManifestStaleReportNeverRendersNotReady(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa"}}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now().Add(-time.Hour), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:unexpected", RuntimeFilename: "Leftover.fseq"}}

	// reportFresh=false: even though the report is Complete and holds
	// nothing EXPECTED (so a naive comparison would call this not_ready,
	// and inventory here even has something UNEXPECTED that would render as
	// Extra), a stale report must render unknown, never not_ready — a stale
	// report is not evidence of absence.
	m := ComputeNodeManifest("render-01", active, expected, report, false, inventory)
	if m.State != ManifestUnknown || m.UnknownCause != UnknownCauseStaleReport {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=unknown Cause=stale_report", m)
	}
	if len(m.Missing) != 0 || len(m.Extra) != 0 {
		t.Errorf("ComputeNodeManifest() = %+v, want no Missing/Extra populated from a stale report", m)
	}
}

func TestComputeNodeManifestReportIncompleteCarriesAgentReason(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: false, Reason: "asset directory does not exist"}

	m := ComputeNodeManifest("render-01", active, ExpectedSet{}, report, true, nil)
	if m.State != ManifestUnknown || m.UnknownCause != UnknownCauseReportIncomplete {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=unknown Cause=report_incomplete", m)
	}
	if m.Reason == "" || !strings.Contains(m.Reason, "asset directory does not exist") {
		t.Errorf("ComputeNodeManifest().Reason = %q, want it to carry the agent's own reason through", m.Reason)
	}
}

func TestComputeNodeManifestReady(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa", Filename: "Opening.fseq", SequenceID: "opening"}}}
	reportedAt := time.Now()
	report := &store.NodeAssetReportRecord{ReportedAt: reportedAt, Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq"}}

	m := ComputeNodeManifest("render-01", active, expected, report, true, inventory)
	if m.State != ManifestReady {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=ready", m)
	}
	if !m.ObservedAt.Equal(reportedAt) {
		t.Errorf("ComputeNodeManifest().ObservedAt = %s, want %s", m.ObservedAt, reportedAt)
	}
}

func TestComputeNodeManifestNotReadyNamesMissing(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{
		{AssetID: "a1", ContentHash: "sha256:aaa", Filename: "Opening.fseq", SequenceID: "opening"},
	}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}

	m := ComputeNodeManifest("render-01", active, expected, report, true, nil)
	if m.State != ManifestNotReady {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=not_ready", m)
	}
	if len(m.Missing) != 1 || m.Missing[0].ContentHash != "sha256:aaa" || m.Missing[0].SequenceID != "opening" || m.Missing[0].Filename != "Opening.fseq" {
		t.Fatalf("ComputeNodeManifest().Missing = %+v, want one entry naming sequence/filename/hash", m.Missing)
	}
}

// TestComputeNodeManifestGapAloneMakesNotReady pins W2: manifest.go's
// "len(missing) > 0 || len(expected.Gaps) > 0" was only ever exercised from
// the api PACKAGE's own tests (TestNodeAssetManifestGapNamesUncoveredSequence),
// so running `go test ./internal/coordinator/assetsync/...` alone reported
// success on a broken readiness rule. Every expected asset is HELD here
// (Missing is empty) and only Gaps is non-empty, isolating the "||" from
// the "missing" half entirely.
func TestComputeNodeManifestGapAloneMakesNotReady(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{
		Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa", Filename: "Opening.fseq", SequenceID: "opening"}},
		Gaps:   []SurfaceGap{{SequenceID: "closing", SurfaceIDs: []string{"garage-surface"}}},
	}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq"}}

	m := ComputeNodeManifest("render-01", active, expected, report, true, inventory)
	if m.State != ManifestNotReady {
		t.Fatalf("ComputeNodeManifest() State = %q, want %q: zero missing assets but a non-empty Gaps must still be not_ready", m.State, ManifestNotReady)
	}
	if len(m.Missing) != 0 {
		t.Errorf("Missing = %+v, want empty: every expected asset is held", m.Missing)
	}
	if len(m.Gaps) != 1 || m.Gaps[0].SequenceID != "closing" {
		t.Errorf("Gaps = %+v, want exactly one entry naming %q", m.Gaps, "closing")
	}
}

func TestComputeNodeManifestExtraReportedNeverError(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:unexpected", RuntimeFilename: "Leftover.fseq"}}

	m := ComputeNodeManifest("render-01", active, ExpectedSet{}, report, true, inventory)
	if m.State != ManifestReady {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=ready: an unexpected extra file is never itself a readiness fault", m)
	}
	if len(m.Extra) != 1 || m.Extra[0].ContentHash != "sha256:unexpected" {
		t.Fatalf("ComputeNodeManifest().Extra = %+v, want one entry naming the unexpected hash", m.Extra)
	}
}

// --- D-016 item 2: AssetVerdict (held/superseded/absent) ---

// TestComputeNodeManifestVerdictHeld proves the fact that does not exist
// today: an expected asset the node's inventory actually holds gets an
// AssetVerdictHeld verdict, not just silent omission from Missing.
func TestComputeNodeManifestVerdictHeld(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{
		{AssetID: "a1", ContentHash: "sha256:aaa", Filename: "Opening.fseq", SequenceID: "opening"},
	}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq"}}

	m := ComputeNodeManifest("render-01", active, expected, report, true, inventory)
	if len(m.Verdicts) != 1 || m.Verdicts[0].AssetID != "a1" || m.Verdicts[0].State != AssetVerdictHeld {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want one entry for a1 with State=held", m.Verdicts)
	}
}

// TestComputeNodeManifestVerdictSuperseded proves a node holding OLDER
// bytes for an identity it is missing the current bytes for reads
// distinctly from a node holding nothing at all: expected.SupersededHashes
// names sha256:old as a hash this asset's identity used to serve, and the
// node's inventory holds exactly that hash instead of the current one.
func TestComputeNodeManifestVerdictSuperseded(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{
		Assets: []ExpectedAsset{
			{AssetID: "a-new", ContentHash: "sha256:new", Filename: "Opening.fseq", SequenceID: "opening"},
		},
		SupersededHashes: map[string]map[string]bool{
			"a-new": {"sha256:old": true},
		},
	}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:old", RuntimeFilename: "Opening.fseq"}}

	m := ComputeNodeManifest("render-01", active, expected, report, true, inventory)
	if len(m.Verdicts) != 1 || m.Verdicts[0].AssetID != "a-new" || m.Verdicts[0].State != AssetVerdictSuperseded {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want one entry for a-new with State=superseded", m.Verdicts)
	}
	if len(m.Missing) != 1 || m.Missing[0].AssetID != "a-new" {
		t.Errorf("ComputeNodeManifest().Missing = %+v, want a-new still named missing: a superseded verdict is not held", m.Missing)
	}
}

// TestComputeNodeManifestVerdictAbsent proves a node holding NEITHER the
// current bytes nor any hash the identity has ever superseded reads as
// absent, never superseded.
func TestComputeNodeManifestVerdictAbsent(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{
		Assets: []ExpectedAsset{
			{AssetID: "a-new", ContentHash: "sha256:new", Filename: "Opening.fseq", SequenceID: "opening"},
		},
		SupersededHashes: map[string]map[string]bool{
			"a-new": {"sha256:old": true},
		},
	}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true}

	m := ComputeNodeManifest("render-01", active, expected, report, true, nil)
	if len(m.Verdicts) != 1 || m.Verdicts[0].AssetID != "a-new" || m.Verdicts[0].State != AssetVerdictAbsent {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want one entry for a-new with State=absent", m.Verdicts)
	}
}

// TestComputeNodeManifestVerdictsNilNeverReported proves the never-reported
// case names no verdict for evidence that does not exist.
func TestComputeNodeManifestVerdictsNilNeverReported(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa"}}}

	m := ComputeNodeManifest("render-01", active, expected, nil, false, nil)
	if len(m.Verdicts) != 0 {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want none: no report has ever been received", m.Verdicts)
	}
}

// TestComputeNodeManifestVerdictsNilOnStaleReport is the one most worth
// getting right: a stale report is not evidence of what a node currently
// holds, and that must be exactly as true of Verdicts as it already is of
// Missing and Extra. The inventory here DOES hold the expected hash — a
// naive implementation that forgot to gate Verdicts behind reportFresh
// would render this asset "held" from stale evidence.
func TestComputeNodeManifestVerdictsNilOnStaleReport(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa", SequenceID: "opening"}}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now().Add(-time.Hour), Complete: true}
	inventory := []store.NodeAssetInventoryRecord{{ContentHash: "sha256:aaa", RuntimeFilename: "Opening.fseq"}}

	m := ComputeNodeManifest("render-01", active, expected, report, false, inventory)
	if m.State != ManifestUnknown || m.UnknownCause != UnknownCauseStaleReport {
		t.Fatalf("ComputeNodeManifest() = %+v, want State=unknown Cause=stale_report", m)
	}
	if len(m.Verdicts) != 0 {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want none: a stale report is not evidence of what a node currently holds", m.Verdicts)
	}
}

// TestComputeNodeManifestVerdictsNilOnIncompleteReport proves the
// report_incomplete case also names no verdict — a node's own report
// saying it could not fully enumerate its asset directory is exactly as
// unreliable a basis for a per-asset verdict as it is for Missing/Extra.
func TestComputeNodeManifestVerdictsNilOnIncompleteReport(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa"}}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: false, Reason: "asset directory does not exist"}

	m := ComputeNodeManifest("render-01", active, expected, report, true, nil)
	if len(m.Verdicts) != 0 {
		t.Fatalf("ComputeNodeManifest().Verdicts = %+v, want none: an incomplete report names no verdict", m.Verdicts)
	}
}

// TestExpectedAssetsForNodeSupersededHashesKeyedByCurrentAssetID proves
// [supersededHashesByAssetID]'s derivation end to end: uploading a second
// asset for the identical (show, sequence, targetKind, target) identity
// supersedes the first, and ExpectedAssetsForNode's result must key the
// superseded content hash by the NEW (current) asset's own AssetID —
// never by filename, never by the old asset's own id.
func TestExpectedAssetsForNodeSupersededHashesKeyedByCurrentAssetID(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "render-01")

	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:old", "Opening.fseq")
	current := createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:new", "Opening.fseq")

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	if len(got.Assets) != 1 || got.Assets[0].AssetID != current.ID {
		t.Fatalf("ExpectedAssetsForNode() Assets = %+v, want the current asset only", got.Assets)
	}
	hashes := got.SupersededHashes[current.ID]
	if len(hashes) != 1 || !hashes["sha256:old"] {
		t.Fatalf("ExpectedAssetsForNode() SupersededHashes[%q] = %+v, want {sha256:old}", current.ID, hashes)
	}
}

// --- D2: surfaceIDsForNode's show-AND-node filter ---

// TestSurfaceIDsForNodeFiltersByShowAndNode pins D2: dropping either the
// show clause or the node clause in surfaceIDsForNode leaves the suite
// green, because every OTHER fixture in this file uses exactly one show and
// one surface. Three surfaces are seeded — a different node in this show, a
// different show for this node, and this exact (show, node) pair — and only
// the third may come back. This is the case ADR-027 exists for: Halloween's
// surfaces must never leak into Christmas' manifest, or vice versa.
func TestSurfaceIDsForNodeFiltersByShowAndNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putShow(t, st, "christmas-2026", "Christmas 2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")

	putSurface(t, st, "other-node-this-show", "halloween-2026", "render-02")
	putSurface(t, st, "this-node-other-show", "christmas-2026", "render-01")
	putSurface(t, st, "this-node-this-show", "halloween-2026", "render-01")

	got, err := surfaceIDsForNode(context.Background(), st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatalf("surfaceIDsForNode() error = %v", err)
	}
	if len(got) != 1 || got[0] != "this-node-this-show" {
		t.Fatalf("surfaceIDsForNode(halloween-2026, render-01) = %v, want exactly [this-node-this-show]", got)
	}
}
