package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// publishAndDecodeRenderReport builds one render report exactly as
// runRenderReport's tick path does, and decodes it back through the wire
// codec, so these tests exercise the same round trip a real coordinator
// would see, not just publishOneRenderReport's in-memory return value.
func publishAndDecodeRenderReport(t *testing.T, sup *pipeline.Supervisor, store *pipeline.AssignmentStore) mqttproto.RenderPayload {
	t.Helper()
	pub := newFakePublisher()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	publishOneRenderReport(ctx, pub, "showmesh/nodes/media-03/observed/render", "media-03", sup, store, newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, discardLogger())

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	return decodeRenderReport(t, calls[0].payload)
}

func surfaceReport(t *testing.T, payload mqttproto.RenderPayload, surfaceID string) mqttproto.RenderSurfaceReport {
	t.Helper()
	for _, s := range payload.Surfaces {
		if s.SurfaceID == surfaceID {
			return s
		}
	}
	t.Fatalf("no surface report for %q in %+v", surfaceID, payload.Surfaces)
	return mqttproto.RenderSurfaceReport{}
}

// TestRenderReportCarriesContentIdentityAfterCueActivation proves the
// periodic render report states the FSEQ filename, its content hash, the
// authorizing cue id, and the catalog revision the node actually applied
// when a cue activation swapped a surface's content: this is the
// node's own evidence for the file it opened, sourced from the persisted
// assignment and its auth tuple, not from anything the coordinator most
// recently asked for.
func TestRenderReportCarriesContentIdentityAfterCueActivation(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	newPath := writeSynthFSEQ(t, dir, "new.fseq", cueActivationRenderChannelCount, 10, 25)
	newHash, err := hashFile(newPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-2", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{newHash}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-render-swap", "cue-2", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", result)
	}

	payload := publishAndDecodeRenderReport(t, sup, assignmentStore)
	sf := surfaceReport(t, payload, "surface-1")

	if sf.FSEQFilename != "new.fseq" {
		t.Errorf("FSEQFilename = %q, want %q", sf.FSEQFilename, "new.fseq")
	}
	if sf.FSEQContentHash != newHash {
		t.Errorf("FSEQContentHash = %q, want the hash the node recorded for the file it opened (%q)", sf.FSEQContentHash, newHash)
	}
	if sf.CueID != "cue-2" {
		t.Errorf("CueID = %q, want %q", sf.CueID, "cue-2")
	}
	if sf.CatalogRevision != "rev-a" {
		t.Errorf("CatalogRevision = %q, want %q", sf.CatalogRevision, "rev-a")
	}
	if sf.Show != "halloween-2026" {
		t.Errorf("Show = %q, want %q", sf.Show, "halloween-2026")
	}
	if sf.Generation != 3 {
		t.Errorf("Generation = %d, want %d", sf.Generation, 3)
	}
}

// TestRenderReportNoAssignmentReportsAbsence proves a surface this node
// currently supervises but holds no persisted assignment for reports
// absence on all four content fields, never a fabricated or stale value.
func TestRenderReportNoAssignmentReportsAbsence(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})
	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// An empty store: this surface is supervised (a running pipeline
	// process), but no render.surface.apply/cue activation has ever
	// persisted an assignment for it.
	store := pipeline.NewAssignmentStore(t.TempDir())

	payload := publishAndDecodeRenderReport(t, sup, store)
	sf := surfaceReport(t, payload, "surface-1")

	if sf.FSEQFilename != "" {
		t.Errorf("FSEQFilename = %q, want empty (no assignment held)", sf.FSEQFilename)
	}
	if sf.FSEQContentHash != "" {
		t.Errorf("FSEQContentHash = %q, want empty (no assignment held)", sf.FSEQContentHash)
	}
	if sf.CueID != "" {
		t.Errorf("CueID = %q, want empty (no assignment held)", sf.CueID)
	}
	if sf.CatalogRevision != "" {
		t.Errorf("CatalogRevision = %q, want empty (no assignment held)", sf.CatalogRevision)
	}
	if sf.Show != "" {
		t.Errorf("Show = %q, want empty (no assignment held)", sf.Show)
	}
	if sf.Generation != 0 {
		t.Errorf("Generation = %d, want 0 (no assignment held)", sf.Generation)
	}
}

// TestRenderReportReappliedWithoutFSEQReportsAbsenceNotStale is the direct
// regression this build item names: a surface that DID carry content, then
// received a fresh render.surface.apply for a test-pattern-only pipeline
// (no fseqFilename at all, the same shape B2a runs), must report absence
// on the next tick, never the filename it used to render. Before this fix
// there was no read route at all for content identity, so this specific
// "a stale name survives a content-less re-apply" defect could not
// previously be observed on the wire; this test pins the fix in place.
func TestRenderReportReappliedWithoutFSEQReportsAbsenceNotStale(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	before := publishAndDecodeRenderReport(t, sup, assignmentStore)
	sfBefore := surfaceReport(t, before, "surface-1")
	if sfBefore.FSEQFilename != "old.fseq" {
		t.Fatalf("setup: FSEQFilename = %q, want old.fseq before the content-less re-apply", sfBefore.FSEQFilename)
	}

	testPatternParams := map[string]any{
		"surfaceId": "surface-1",
		"channelRange": map[string]any{
			"startChannel": float64(1),
			"channelCount": float64(cueActivationRenderChannelCount),
		},
		"geometry": map[string]any{
			"width":       float64(2),
			"height":      float64(2),
			"pixelFormat": "rgb",
		},
		"frameRate": float64(40),
	}
	if _, err := renderOps.applySurface(context.Background(), testPatternParams, clock.now); err != nil {
		t.Fatalf("applySurface (test-pattern, no fseq): %v", err)
	}

	after := publishAndDecodeRenderReport(t, sup, assignmentStore)
	sfAfter := surfaceReport(t, after, "surface-1")
	if sfAfter.FSEQFilename != "" {
		t.Errorf("FSEQFilename = %q after a content-less re-apply, want empty (must not report the previously-assigned filename as stale evidence)", sfAfter.FSEQFilename)
	}
	if sfAfter.FSEQContentHash != "" {
		t.Errorf("FSEQContentHash = %q after a content-less re-apply, want empty", sfAfter.FSEQContentHash)
	}
}
