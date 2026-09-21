package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// waitForSnapshotDrawing polls surfaceID's supervisor snapshot until its
// Drawing equals want, or fails the test after a short deadline.
func waitForSnapshotDrawing(t *testing.T, sup *pipeline.Supervisor, surfaceID, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if snap, ok := sup.Snapshot(surfaceID); ok && snap.Drawing == want {
			return
		}
		if time.Now().After(deadline) {
			snap, _ := sup.Snapshot(surfaceID)
			t.Fatalf("surface %q never reported Drawing=%q within the deadline; last snapshot Drawing=%q", surfaceID, want, snap.Drawing)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestBlackoutSurfaceIsIdempotent proves build item 1's own idempotence
// requirement: calling render.surface.blackout twice in a row succeeds
// both times and leaves the flag set.
func TestBlackoutSurfaceIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.applySurface(context.Background(), minimalRenderApplyParams("surface-1"), clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	for i := 0; i < 2; i++ {
		result, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now)
		if err != nil {
			t.Fatalf("blackoutSurface call %d: %v", i+1, err)
		}
		if !result.Confirmed {
			t.Fatalf("blackoutSurface call %d: Confirmed = false, want true", i+1)
		}
	}
	if !renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false after two blackout calls, want true")
	}
}

// TestBlackoutSurfaceWithNoAssignmentSucceeds proves build item 1's "on a
// surface with no assignment it succeeds and records the flag" rule:
// nothing to draw, nothing to fail.
func TestBlackoutSurfaceWithNoAssignmentSucceeds(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	result, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "never-applied"}, clock.now)
	if err != nil {
		t.Fatalf("blackoutSurface on a never-applied surface: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("Confirmed = false, want true")
	}
	if !renderOps.isHeldBlack("never-applied") {
		t.Fatalf("isHeldBlack = false, want true")
	}
	held, err := pipeline.NewHoldBlackStore(dir).Load()
	if err != nil {
		t.Fatalf("loading held-black state: %v", err)
	}
	if !held["never-applied"] {
		t.Fatalf("held-black state on disk = %v, want surface recorded", held)
	}
}

// TestBlackoutSurfaceWithNoAssignmentReportsBlackoutOutputMode proves the
// node's render report carries a synthetic row for a surface held black
// with no current assignment: blackoutSurface never starts a FrameWriter
// for one, so it has no supervisor snapshot to report it through at all.
// Before this fix, such a surface was silently absent from every render
// report, so the coordinator's collector never had any evidence for it —
// not even a not_collected one — and a weather delay power group
// containing it could never confirm dark.
func TestBlackoutSurfaceWithNoAssignmentReportsBlackoutOutputMode(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "never-applied"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}

	pub := newFakePublisher()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	publishOneRenderReport(ctx, pub, "showmesh/nodes/test-node/observed/render", "test-node", sup, store, renderOps,
		newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), clock.now, discardLogger())

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	payload := decodeRenderReport(t, calls[0].payload)

	var found *mqttproto.RenderSurfaceReport
	for i := range payload.Surfaces {
		if payload.Surfaces[i].SurfaceID == "never-applied" {
			found = &payload.Surfaces[i]
		}
	}
	if found == nil {
		t.Fatalf("surfaces = %+v, want an entry for the held-black surface with no assignment", payload.Surfaces)
	}
	if found.Drawing != mqttproto.RenderDrawingBlackout {
		t.Fatalf("Drawing = %q, want %q", found.Drawing, mqttproto.RenderDrawingBlackout)
	}
}

// TestBlackoutSurfaceThenCueActivateResumesSameGenerationAndClearsFlag
// proves build items 1 and 2 together, at the agent layer, against a real
// pipeline.Supervisor: blackout forces black without touching the
// assignment or restarting the pipeline, and the next authorized
// cue.activate resumes content, clears the flag, and never bumps the
// surface's own process-attempt generation (proving no restart happened).
func TestBlackoutSurfaceThenCueActivateResumesSameGenerationAndClearsFlag(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	genBefore := sup.Generation("surface-1")

	if _, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)

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
	act := testActivation("act-render-resume", "cue-2", 1, "halloween-2026", 3, "rev-a", 0)
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", result)
	}

	genAfter := sup.Generation("surface-1")
	if genAfter != genBefore {
		t.Fatalf("pipeline generation changed from %d to %d; cue.activate after a blackout must never restart the pipeline", genBefore, genAfter)
	}
	if renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = true after cue.activate, want false: an authorized swap must clear the flag")
	}
	held, err := pipeline.NewHoldBlackStore(dir).Load()
	if err != nil {
		t.Fatalf("loading held-black state: %v", err)
	}
	if held["surface-1"] {
		t.Fatalf("held-black state on disk still carries surface-1 after cue.activate")
	}
	// No MultiSync packet was ever fed to this timeline (this test is not
	// about playback position), so the writer's own idle-content-states
	// table keeps it drawing Idle once forced black clears - the point
	// proven here is that it is no longer DrawingBlackout, not that it is
	// drawing any particular non-blackout state.
	waitFor := time.Now().Add(2 * time.Second)
	for {
		snap, ok := sup.Snapshot("surface-1")
		if ok && snap.Drawing != pipeline.DrawingBlackout {
			break
		}
		if time.Now().After(waitFor) {
			t.Fatalf("surface-1 still reports Drawing=%q after cue.activate cleared the held-black flag", snap.Drawing)
		}
		time.Sleep(5 * time.Millisecond)
	}

	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].SurfaceID != "surface-1" {
		t.Fatalf("persisted assignments after resume = %+v, want exactly one for surface-1: the assignment must never have been torn down", reloaded)
	}
}

// TestBlackoutSurfaceThenCueActivateRefusedForMissingAssetStaysBlack proves
// that a cue.activate naming an FSEQ this node never received (here,
// "missing.fseq") is refused by cueauth.CheckLazy's own assetsPresent gate
// (cueactivationops.go) before renderOperations.activateRender is ever
// called, and that refusal leaves a blacked-out surface exactly as it was:
// black, with the held-black flag still set. It does not exercise
// activateRender's own validate-before-clear ordering — see
// TestActivateRenderWithUnopenableFSEQStaysBlackAndFlagStaysSet below for a
// test that reaches that path.
func TestBlackoutSurfaceThenCueActivateRefusedForMissingAssetStaysBlack(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	if _, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-2", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			// "missing.fseq" was never uploaded to this node's asset dir:
			// buildAssignedSpec fails opening it, exactly the "missing
			// FSEQ" failure this build item names.
			Render: &cuecatalog.RenderOutput{Sequence: "seq-missing", Filename: "missing.fseq", AssetHashes: []string{"deadbeef"}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-render-missing", "cue-2", 1, "halloween-2026", 3, "rev-a", 0)
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("activate against a missing FSEQ unexpectedly confirmed: %+v", result)
	}

	if !renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false after a failed cue.activate, want true: a failed activation must never relight a blacked-out surface")
	}
	held, err := pipeline.NewHoldBlackStore(dir).Load()
	if err != nil {
		t.Fatalf("loading held-black state: %v", err)
	}
	if !held["surface-1"] {
		t.Fatalf("held-black state on disk lost surface-1 after a failed cue.activate")
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)
}

// TestActivateRenderWithUnopenableFSEQStaysBlackAndFlagStaysSet proves build
// item 2's actual ordering fix: renderOperations.activateRender validates
// the NEW file (buildAssignedSpec) before it ever clears
// render.surface.blackout's held-black flag, so a failure discovered only
// there — the file's content matches its declared hash but is not a valid
// FSEQ — leaves the surface black and the flag set. This calls
// activateRender directly rather than through cueActivationOperation.activate,
// because cueauth.CheckLazy's assetsPresent gate (cueactivationops.go) reads
// this same hash and would already refuse a missing file or a hash
// mismatch before activateRender is ever reached; opening the file is the
// one check buildAssignedSpec alone makes past that gate.
func TestActivateRenderWithUnopenableFSEQStaysBlackAndFlagStaysSet(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	if _, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)

	badPath := dir + "/bad.fseq"
	// Not a valid FSEQ (no "PSEQ" header, too short for one): fseq.Open
	// fails on it even though its declared hash below is honest, so this
	// reaches buildAssignedSpec's open failure rather than its hash check.
	badContent := []byte("not a valid fseq file")
	if err := os.WriteFile(badPath, badContent, 0o644); err != nil {
		t.Fatalf("write bad.fseq: %v", err)
	}
	badHash, err := hashFile(badPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	act := testActivation("act-render-unopenable", "cue-3", 1, "halloween-2026", 3, "rev-a", 0)
	out := cuecatalog.RenderOutput{Sequence: "seq-bad", Filename: "bad.fseq", AssetHashes: []string{badHash}}

	if err := renderOps.activateRender(act, out, clock.now); err == nil {
		t.Fatalf("activateRender against an unopenable fseq unexpectedly succeeded")
	}

	if !renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false after activateRender failed to open the new file, want true: a failed activation must never relight a blacked-out surface")
	}
	held, err := pipeline.NewHoldBlackStore(dir).Load()
	if err != nil {
		t.Fatalf("loading held-black state: %v", err)
	}
	if !held["surface-1"] {
		t.Fatalf("held-black state on disk lost surface-1 after activateRender failed to open the new file")
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)
}

// TestRenderSurfaceClearRemovesAStaleHeldBlackEntry proves build item 5:
// render.surface.clear removes a held-black flag left over from an earlier
// blackout, on disk and in memory, so it never resurfaces on a later,
// unrelated assignment to the same surface id.
func TestRenderSurfaceClearRemovesAStaleHeldBlackEntry(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.applySurface(context.Background(), minimalRenderApplyParams("surface-1"), clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}
	if _, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	if !renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false right after blackoutSurface, want true")
	}

	if _, err := renderOps.clearSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("clearSurface: %v", err)
	}

	if renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = true after render.surface.clear, want false: clear must remove the held-black flag")
	}
	held, err := pipeline.NewHoldBlackStore(dir).Load()
	if err != nil {
		t.Fatalf("loading held-black state: %v", err)
	}
	if held["surface-1"] {
		t.Fatalf("held-black state on disk still carries surface-1 after render.surface.clear")
	}
}

// TestBlackoutSurfacePersistsAcrossAgentRestart proves build item 3: a
// held-black flag survives a simulated agent restart (a fresh
// renderOperations built over the same asset directory) with the
// assignment intact, and the newly started frame writer comes up drawing
// black from its very first tick.
func TestBlackoutSurfacePersistsAcrossAgentRestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}

	// "Session 1": apply, then blackout.
	sup1 := pipeline.NewSupervisor(clock.now, (&fakeRenderStarter{}).Start, discardLogger())
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps1 := newTestRenderOperations(sup1, assignmentStore, dir, clock)
	if _, err := renderOps1.applySurface(context.Background(), minimalRenderApplyParams("surface-1"), clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}
	if _, err := renderOps1.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	sup1.Shutdown(shutdownCtx)
	cancel()

	// "Session 2": a fresh Supervisor and a fresh renderOperations over the
	// SAME assetDir, mirroring agent.go's own boot-resume construction
	// order (holdBlackStore loaded before ResumeAssignment runs).
	sup2 := pipeline.NewSupervisor(clock.now, (&fakeRenderStarter{}).Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup2.Shutdown(ctx)
	})
	holdBlackStore2 := pipeline.NewHoldBlackStore(dir)
	renderOps2 := newRenderOperations(sup2, assignmentStore, holdBlackStore2, dir, multisync.NewTimeline(clock.now, multisync.Config{}), nil, "", discardLogger())

	if !renderOps2.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false on a fresh renderOperations over the same asset dir, want true (persisted flag)")
	}

	persisted, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("loading persisted assignments: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted assignments after restart = %+v, want exactly one (the assignment must still be intact)", persisted)
	}
	var params map[string]any
	if err := json.Unmarshal(persisted[0].RawParams, &params); err != nil {
		t.Fatalf("decode persisted params: %v", err)
	}
	if err := renderOps2.ResumeAssignment("surface-1", params); err != nil {
		t.Fatalf("ResumeAssignment: %v", err)
	}

	waitForSnapshotDrawing(t, sup2, "surface-1", pipeline.DrawingBlackout)
}

// TestBlackoutSurfaceSetsFlagInMemoryEvenWhenPersistenceFails proves build
// item 3's ordering fix: the in-memory held-black flag is set BEFORE
// persistence is even attempted, so a surface is already drawing black
// (and stays that way) even when the disk write that follows fails. The
// old order failed lit: a persistence error returned before the flag was
// ever set in memory, so the running frame writer never learned to hold
// black at all.
func TestBlackoutSurfaceSetsFlagInMemoryEvenWhenPersistenceFails(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	// A regular file where the held-black state directory belongs makes
	// every later write to it fail (os.MkdirAll refuses to turn a file
	// into a directory) regardless of the process's own privilege level —
	// unlike a chmod-based permission denial, which a build running as
	// root (as this project's CI does) ignores.
	if err := os.WriteFile(dir+"/.render-state", []byte("block"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	_, err := renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now)
	if err == nil {
		t.Fatalf("blackoutSurface against a blocked state directory unexpectedly succeeded")
	}
	if !renderOps.isHeldBlack("surface-1") {
		t.Fatalf("isHeldBlack = false after a persistence failure, want true: the in-memory flag must be set before persistence is even attempted, so a stop never fails lit")
	}
}

// TestNewRenderOperationsHoldsEveryUntouchedSurfaceBlackOnACorruptFile
// proves build item 3's other half: an unreadable or corrupt held-black
// file must come up with every surface held black, not "nothing is held
// black" — black is the safe direction for a stop, and a lost file cannot
// honestly say which surfaces it used to name. A later render.surface.apply
// or cue.activate still clears the flag per surface.
func TestNewRenderOperationsHoldsEveryUntouchedSurfaceBlackOnACorruptFile(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	stateDir := dir + "/.render-state"
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(stateDir+"/held-black.json", []byte("not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt held-black file: %v", err)
	}

	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if !renderOps.isHeldBlack("surface-never-mentioned") {
		t.Fatalf("isHeldBlack = false for a surface never named anywhere, want true: a corrupt held-black file must default every surface to black")
	}

	// An explicit clear still wins over the corrupt-file default, per
	// surface.
	if err := renderOps.clearHeldBlack("surface-never-mentioned"); err != nil {
		t.Fatalf("clearHeldBlack: %v", err)
	}
	if renderOps.isHeldBlack("surface-never-mentioned") {
		t.Fatalf("isHeldBlack = true after an explicit clear, want false: an explicit entry must win over the corrupt-file default")
	}
	if !renderOps.isHeldBlack("surface-still-defaulted") {
		t.Fatalf("isHeldBlack = false, want true: clearing ONE surface must never lift the default for another untouched surface")
	}
}

// TestCorruptHeldBlackFileDefaultSurvivesTheFirstClearAndARestart proves
// build item 3's corrupt-file default is not lost at the first write: two
// surfaces (A and B) are both assigned and both default black on a corrupt
// held-black file, clearHeldBlack(A) must persist B's black default too —
// not just A's own clear — and that must still hold after a further
// simulated restart, a fresh renderOperations over the same asset
// directory. Before this fix, clearHeldBlack(A) rewrote the held-black
// file as containing only A (cleared), losing B's default entirely, so a
// fresh renderOperations came up with B reading lit.
func TestCorruptHeldBlackFileDefaultSurvivesTheFirstClearAndARestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}

	// "Session 1": assign both surfaces, no blackout yet.
	sup1 := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps1 := newTestRenderOperations(sup1, assignmentStore, dir, clock)
	if _, err := renderOps1.applySurface(context.Background(), minimalRenderApplyParams("surface-a"), clock.now); err != nil {
		t.Fatalf("applySurface surface-a: %v", err)
	}
	if _, err := renderOps1.applySurface(context.Background(), minimalRenderApplyParams("surface-b"), clock.now); err != nil {
		t.Fatalf("applySurface surface-b: %v", err)
	}

	// Corrupt the held-black file AFTER both applies (which themselves
	// write a valid one via their own clearHeldBlack call) — this is the
	// unreadable file "session 2" below must recover from.
	stateDir := dir + "/.render-state"
	if err := os.WriteFile(stateDir+"/held-black.json", []byte("not valid json"), 0o644); err != nil {
		t.Fatalf("write corrupt held-black file: %v", err)
	}

	// "Session 2": a fresh renderOperations loads the corrupt file, so both
	// A and B default black. Clearing A alone must not lose B's default.
	sup2 := newRenderTestSupervisor(t, clock)
	renderOps2 := newTestRenderOperations(sup2, assignmentStore, dir, clock)
	if !renderOps2.isHeldBlack("surface-a") || !renderOps2.isHeldBlack("surface-b") {
		t.Fatalf("both surfaces must default black on a corrupt held-black file before either is touched")
	}
	if err := renderOps2.clearHeldBlack("surface-a"); err != nil {
		t.Fatalf("clearHeldBlack surface-a: %v", err)
	}
	if renderOps2.isHeldBlack("surface-a") {
		t.Fatalf("isHeldBlack(surface-a) = true right after clearing it, want false")
	}
	if !renderOps2.isHeldBlack("surface-b") {
		t.Fatalf("isHeldBlack(surface-b) = false right after clearing ONLY surface-a, want true: the corrupt-file default for an untouched surface must survive the other surface's clear")
	}

	// "Session 3": simulate a restart. B's black default must have
	// actually been persisted by session 2's clear, not merely held true
	// in that now-discarded process's memory.
	sup3 := newRenderTestSupervisor(t, clock)
	renderOps3 := newTestRenderOperations(sup3, assignmentStore, dir, clock)
	if renderOps3.isHeldBlack("surface-a") {
		t.Fatalf("isHeldBlack(surface-a) = true after a restart, want false: the clear must have persisted")
	}
	if !renderOps3.isHeldBlack("surface-b") {
		t.Fatalf("isHeldBlack(surface-b) = false after a restart, want true: the corrupt-file default for surface-b must have been persisted by surface-a's clear, not lost")
	}
}

// TestWeatherDelayForcesBlackOnHoldIdleOutputSurfaceEvenWhilePlaying proves
// build item 4: routing the weather delay gate through the same
// HoldBlackSource path forces literal black even for a surface whose
// configured idle output is Hold and whose timeline reports Playing.
func TestWeatherDelayForcesBlackOnHoldIdleOutputSurfaceEvenWhilePlaying(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	weatherDelay := NewWeatherDelayHolder(dir, discardLogger())
	renderOps.weatherDelay = weatherDelay

	path := writeSynthFSEQ(t, dir, "seq.fseq", cueActivationRenderChannelCount, 10, 25)
	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	params := fseqApplyParams("surface-1", "seq.fseq", hash, 1, cueActivationRenderChannelCount, 2, 2, "rgb", 40)
	params["idleOutput"] = "hold"
	if _, err := renderOps.applySurface(context.Background(), params, clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	// MultiSync reports this exact sequence Playing: content is genuinely
	// available and the timeline says so.
	renderOps.timeline.Observe(multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence, Filename: "seq.fseq",
	}, "fpp-01")
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingContent)

	if err := weatherDelay.SetActiveLocal(weatherDelayKindDelay, clock.now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingBlackout)
	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot for surface-1")
	}
	if snap.IdleMode != "" {
		t.Fatalf("IdleMode = %q while forced black by a weather delay, want empty (never the surface's configured Hold)", snap.IdleMode)
	}

	if err := weatherDelay.ClearLocal(); err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}
	waitForSnapshotDrawing(t, sup, "surface-1", pipeline.DrawingContent)
}
