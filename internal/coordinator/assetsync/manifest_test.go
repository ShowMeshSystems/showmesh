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
	rec, err := st.CreateAsset(context.Background(), store.AssetRecord{
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
// point 3's stated-gap rule as this package implements it (see doc.go's
// own note on why it is inferred from the show's asset rows rather than a
// stored surface-to-sequence link, which does not exist in the built
// schema): a node with a surface and zero coverage for a sequence the
// show already has elsewhere is named as a gap.
func TestExpectedAssetsForNodeGapsSurfacedNodeMissingSequence(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")
	putSurface(t, st, "garage-surface", "halloween-2026", "render-02")

	// The show has an "opening" sequence, but only for render-01 — render-02
	// has a surface and should read a gap for "opening".
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

func TestComputeNodeManifestStaleReportNeverRendersNotReady(t *testing.T) {
	active := ActiveShow{Configured: true, ShowID: "halloween-2026"}
	expected := ExpectedSet{Assets: []ExpectedAsset{{AssetID: "a1", ContentHash: "sha256:aaa"}}}
	report := &store.NodeAssetReportRecord{ReportedAt: time.Now().Add(-time.Hour), Complete: true}

	// reportFresh=false: even though the report is Complete and holds
	// nothing (so a naive comparison would call this not_ready), a stale
	// report must render unknown, never not_ready — a stale report is not
	// evidence of absence.
	m := ComputeNodeManifest("render-01", active, expected, report, false, nil)
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
