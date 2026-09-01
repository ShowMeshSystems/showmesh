package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

const cueActivationRenderChannelCount = 12 // 2x2 rgb: width*height*3 = 12

// setupActivatedSurface applies an initial render.surface.apply for
// "surface-1" against oldFilename, so activateRender has something
// already running to swap away from.
func setupActivatedSurface(t *testing.T, renderOps *renderOperations, dir, oldFilename string, clock *fakeClock) string {
	t.Helper()
	oldPath := writeSynthFSEQ(t, dir, oldFilename, cueActivationRenderChannelCount, 10, 25)
	oldHash, err := hashFile(oldPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	params := fseqApplyParams("surface-1", oldFilename, oldHash, 1, cueActivationRenderChannelCount, 2, 2, "rgb", 40)
	if _, err := renderOps.applySurface(context.Background(), params, clock.now); err != nil {
		t.Fatalf("initial applySurface: %v", err)
	}
	return oldHash
}

// TestActivateRenderSwapsFSEQ proves the live "cue.activate" operation
// selects the activated Cue's resolved FSEQ and makes the running surface
// render it, persisting the swap with the activation's own authorization
// tuple (renderassignmentauth_test.go's AssignmentAuth, so H3's
// boot-clearing rule keeps working across this seam too).
func TestActivateRenderSwapsFSEQ(t *testing.T) {
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
			// Sequence is deliberately a LOGICAL id distinct from the real
			// runtime filename, so this test cannot pass by Sequence
			// coincidentally equaling the file a node must actually open
			// (see cueactivationrender.go's own Filename fix).
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

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, want exactly one", reloaded)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if params["fseqFilename"] != "new.fseq" {
		t.Fatalf("persisted fseqFilename = %v, want new.fseq", params["fseqFilename"])
	}
	if params["fseqContentHash"] != newHash {
		t.Fatalf("persisted fseqContentHash = %v, want %s", params["fseqContentHash"], newHash)
	}
	if reloaded[0].Auth == nil {
		t.Fatalf("persisted assignment carries no authorization tuple")
	}
	wantAuth := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"}
	if *reloaded[0].Auth != wantAuth {
		t.Fatalf("persisted authorization tuple = %+v, want %+v", *reloaded[0].Auth, wantAuth)
	}
}

// establishApplyParams builds a render.surface.apply params map carrying
// NO fseqFilename/fseqContentHash — the "declared, no content yet" shape a
// catalog-deploy-triggered establishment sends
// (internal/coordinator/api/renderdispatch.go's
// resolveRenderEstablishParams), including the H3 authorization tuple so
// the resulting persisted assignment is one activateSurfaceRender can
// later swap an FSEQ onto. Unlike fseqApplyParams (renderfseq_test.go),
// channelRange/geometry/frameRate are NOT validated at establishment time
// (buildFSEQAssignment's ok==false branch never looks at them — see that
// function's own doc comment), but they are still included and persisted
// here: a LATER activateSurfaceRender re-validates them once a real
// fseqFilename is merged in, and this is the one place that first has to
// supply them.
func establishApplyParams(surfaceID, show string, generation int64, catalogRevision string, startChannel1Based, channelCount, width, height int, pixelFormat string, frameRate int) map[string]any {
	return map[string]any{
		"surfaceId": surfaceID,
		"show":      show,
		"channelRange": map[string]any{
			"startChannel": float64(startChannel1Based),
			"channelCount": float64(channelCount),
		},
		"geometry": map[string]any{
			"width":       float64(width),
			"height":      float64(height),
			"pixelFormat": pixelFormat,
		},
		"frameRate":       float64(frameRate),
		"idleOutput":      pipeline.IdleOutputBlack,
		"generation":      float64(generation),
		"catalogRevision": catalogRevision,
	}
}

// TestActivateRenderSucceedsOnAnEstablishedNoSequenceAssignment is the
// acceptance proof at the agent unit-test level, using the SAME
// production code the coordinator-side fix invokes over MQTT
// (render.surface.apply, applySurface — renderops.go) rather than a mock
// of it: the exact defect this half closes was that NOTHING ever created a
// node's persisted render assignment except a manual, sequence-carrying
// render.surface.apply, so a rebooted node (ADR-043 H0.7 clears
// assignments at boot) had nothing for its first cue activation to
// activate onto until an operator remembered to apply one by hand.
//
// First reproduces that defect directly (a completely fresh node — no
// assignment, exactly the post-reboot state — refuses a cue activation
// with the exact stated reason), then proves establishing the assignment
// with NO sequence selected (exactly what cuecatalogdeploy.go's
// establishRenderAssignments now dispatches on every confirmed
// cuecatalog.deploy) is sufficient on its own for the SAME activation to
// then succeed, swapping in the Cue's resolved FSEQ — activateSurfaceRender
// itself is unmodified by this half; this proves it needed nothing more
// than what establishment now supplies.
func TestActivateRenderSucceedsOnAnEstablishedNoSequenceAssignment(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)

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
	act := testActivation("act-render-establish", "cue-2", 1, "halloween-2026", 3, "rev-a", 0)

	// --- THE REPRODUCTION: a completely fresh node (no assignment at
	// all — the post-reboot state) refuses the activation outright,
	// naming the exact defect this half closes. ---
	before, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate on a node with no assignment at all: unexpected error: %v", err)
	}
	if before.Confirmed {
		t.Fatalf("activate on a node with no assignment at all: Confirmed = true, want false")
	}
	value, ok := before.Value.(map[string]any)
	if !ok {
		t.Fatalf("activate result Value = %#v (%T), want a map", before.Value, before.Value)
	}
	reasons, _ := value["reasons"].([]string)
	if len(reasons) != 1 || reasons[0] != `cue.activate: no surface is currently assigned on this node; nothing to activate Cue "cue-2"'s render output onto` {
		t.Fatalf("activate refusal reasons = %#v, want exactly the no-assignment refusal", value["reasons"])
	}

	// --- establish, with NO sequence selected — exactly what a confirmed
	// cuecatalog.deploy now dispatches. ---
	establishParams := establishApplyParams("surface-1", "halloween-2026", 3, "rev-a", 1, cueActivationRenderChannelCount, 2, 2, "rgb", 40)
	if _, err := renderOps.applySurface(context.Background(), establishParams, clock.now); err != nil {
		t.Fatalf("establish (applySurface with no sequence): %v", err)
	}

	// The established assignment carries no FSEQ — nothing for
	// activateRender to have swapped away from, unlike
	// TestActivateRenderSwapsFSEQ's setupActivatedSurface.
	established, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments after establish: %v", err)
	}
	if len(established) != 1 {
		t.Fatalf("persisted assignments after establish = %+v, want exactly one", established)
	}
	var establishedParams map[string]any
	if err := json.Unmarshal(established[0].RawParams, &establishedParams); err != nil {
		t.Fatalf("decoding persisted establish params: %v", err)
	}
	if _, has := establishedParams["fseqFilename"]; has {
		t.Fatalf("persisted establish params = %+v, want no fseqFilename key at all", establishedParams)
	}

	// --- the SAME activation that was refused a moment ago now succeeds
	// on top of nothing but the establishment above. ---
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate after establishment: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate after establishment did not confirm: %+v", result)
	}

	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments after activation: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments after activation = %+v, want exactly one", reloaded)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if params["fseqFilename"] != "new.fseq" {
		t.Fatalf("persisted fseqFilename = %v, want new.fseq", params["fseqFilename"])
	}
	if params["fseqContentHash"] != newHash {
		t.Fatalf("persisted fseqContentHash = %v, want %s", params["fseqContentHash"], newHash)
	}
}

// TestActivateRenderBadNewFSEQLeavesOldRunningWithStatedFailure proves the
// "validate before stopping the old writer" ordering this seam's own
// cueactivationrender.go doc comment states: a new FSEQ whose content hash
// does not match what the catalog declares must never take down the
// surface currently rendering correctly.
func TestActivateRenderBadNewFSEQLeavesOldRunningWithStatedFailure(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	oldHash := setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	// "new.fseq" exists on disk, but the catalog's declared hash for it is
	// wrong — buildAssignedSpec's own content-hash check (ADR-028) must
	// refuse this swap before anything about the old surface is touched.
	writeSynthFSEQ(t, dir, "new.fseq", cueActivationRenderChannelCount, 10, 25)
	const wrongHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-3", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{wrongHash}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-render-bad-new-fseq", "cue-3", 1, "halloween-2026", 3, "rev-a", 0)

	// This activation is refused as asset-missing (the wrong catalog hash
	// means CheckLazy's own assetsPresent probe never finds "new.fseq"
	// present under the DECLARED hash) — authorization itself is refused
	// before the render swap is ever attempted, which is the node's first
	// line of defense against exactly this kind of coordinator/catalog
	// disagreement. The old surface must still be provably untouched.
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("activate confirmed a swap onto a content-hash-mismatched FSEQ: %+v", result)
	}
	if got := outcomeOf(t, result); got != "asset-missing" {
		t.Fatalf("outcome = %q, want asset-missing", got)
	}

	assertSurfaceStillOnOldFSEQ(t, dir, "old.fseq", oldHash)
}

// TestActivateRenderSurfaceApplyValidateBeforeStopDirectly exercises
// activateSurfaceRender directly (bypassing authorization) against a
// resolved output whose declared hash does not match the file actually on
// disk under that name — buildAssignedSpec's own content-hash check
// refuses, and the old frame writer/persisted assignment must be
// untouched. This is the unit-level proof behind
// TestActivateRenderBadNewFSEQLeavesOldRunningWithStatedFailure's
// end-to-end one (that test is refused earlier, at authorization, and
// never reaches this code path at all).
func TestActivateRenderSurfaceApplyValidateBeforeStopDirectly(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	oldHash := setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	writeSynthFSEQ(t, dir, "new.fseq", cueActivationRenderChannelCount, 10, 25)
	const wrongHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	act := testActivation("act-direct-bad-new-fseq", "cue-3", 1, "halloween-2026", 3, "rev-a", 0)
	out := cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{wrongHash}}

	if err := renderOps.activateRender(act, out, clock.now); err == nil {
		t.Fatalf("activateRender accepted a content-hash-mismatched new FSEQ")
	}

	assertSurfaceStillOnOldFSEQ(t, dir, "old.fseq", oldHash)
}

// TestActivateRenderRepairsADarkSurfaceEvenWhenTheStoreAlreadyMatches is
// defect 4's own regression test: store.Upsert (activateSurfaceRender)
// persists the new assignment BEFORE the old writer stops and the new one
// starts, so a startFrameWriter failure leaves a surface that is dark
// while its persisted assignment already names the new file/hash under
// the correct authorization tuple. Before the fix, surfaceAlreadyActivated
// read the store alone, so a LATER activation of the identical Cue on that
// surface would see "already correct" and skip the repair forever — the
// node would keep reporting Confirmed:true, outcome "authorized", for a
// surface that is actually dark.
func TestActivateRenderRepairsADarkSurfaceEvenWhenTheStoreAlreadyMatches(t *testing.T) {
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
	out := cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{newHash}}
	act := testActivation("act-dark-repair", "cue-6", 1, "halloween-2026", 3, "rev-a", 0)

	// Simulate exactly the state a startFrameWriter failure leaves behind:
	// the persisted assignment already names the new file/hash under act's
	// own authorization tuple (as activateSurfaceRender's store.Upsert
	// would have left it), but no writer is actually running for the
	// surface.
	renderOps.stopFrameWriter("surface-1")
	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	params["fseqFilename"] = out.Filename
	params["fseqContentHash"] = newHash
	params["show"] = act.Show
	params["generation"] = float64(act.Generation)
	params["catalogRevision"] = act.CatalogRevision
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encoding params: %v", err)
	}
	auth := &pipeline.AssignmentAuth{Show: act.Show, Generation: act.Generation, CatalogRevision: act.CatalogRevision}
	if err := assignmentStore.Upsert(pipeline.Assignment{
		SurfaceID: "surface-1", RawParams: rawParams, AppliedAt: clock.now(), Auth: auth,
	}); err != nil {
		t.Fatalf("upsert dark-surface assignment: %v", err)
	}

	if renderOps.hasRunningFrameWriter("surface-1") {
		t.Fatalf("test setup error: surface-1 must be dark (no running writer) before the repair is exercised")
	}

	// Activating the IDENTICAL Cue/authorization tuple again: the store
	// already matches out's filename/hash exactly, but the surface is
	// dark. The fix must repair it rather than report "already activated".
	if err := renderOps.activateRender(act, out, clock.now); err != nil {
		t.Fatalf("activateRender did not repair the dark surface: %v", err)
	}
	if !renderOps.hasRunningFrameWriter("surface-1") {
		t.Fatalf("surface-1 is still dark after activateRender: the store-already-matches shortcut skipped the repair")
	}
}

func assertSurfaceStillOnOldFSEQ(t *testing.T, dir, oldFilename, oldHash string) {
	t.Helper()
	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, want exactly one (the untouched original)", reloaded)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if params["fseqFilename"] != oldFilename {
		t.Fatalf("persisted fseqFilename = %v, want the untouched original %q", params["fseqFilename"], oldFilename)
	}
	if params["fseqContentHash"] != oldHash {
		t.Fatalf("persisted fseqContentHash = %v, want the untouched original %s", params["fseqContentHash"], oldHash)
	}
}

// TestActivateRenderMultiSyncFilenameMismatchRefusesToSwitch proves H4-
// BRIEF.md ruling 1: a MultiSync-reported filename that disagrees with the
// activated Cue's own resolved sequence is a stated mismatch, never a
// reason to switch content and never a silent continue.
func TestActivateRenderMultiSyncFilenameMismatchRefusesToSwitch(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	oldHash := setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	// MultiSync reports FPP is actually playing a THIRD filename — neither
	// the surface's current one nor the Cue's resolved one.
	renderOps.timeline.Observe(multisync.SyncPacket{
		Action: multisync.SyncActionOpen, FileType: multisync.SyncFileTypeSequence, Filename: "some-other-show.fseq",
	}, "fpp-01")

	newPath := writeSynthFSEQ(t, dir, "new.fseq", cueActivationRenderChannelCount, 10, 25)
	newHash, err := hashFile(newPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	act := testActivation("act-mismatch", "cue-4", 1, "halloween-2026", 3, "rev-a", 0)
	out := cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{newHash}}

	err = renderOps.activateRender(act, out, clock.now)
	if err == nil {
		t.Fatalf("activateRender switched content despite a MultiSync filename disagreement")
	}
	t.Logf("stated mismatch (expected): %v", err)

	assertSurfaceStillOnOldFSEQ(t, dir, "old.fseq", oldHash)
}

// TestActivateRenderRedeliveredActivationIsIdempotent proves H4's own
// "re-applying the same ActivationID must not disturb anything already
// correct" requirement: activating the identical authorized Activation a
// second time does not re-open, re-validate, or re-persist anything —
// the persisted assignment's AppliedAt timestamp (driven by the injected
// clock, not wall time) is untouched by the second call.
func TestActivateRenderRedeliveredActivationIsIdempotent(t *testing.T) {
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
		CueID: "cue-5", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-new", Filename: "new.fseq", AssetHashes: []string{newHash}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-idempotent", "cue-5", 1, "halloween-2026", 3, "rev-a", 0)

	// First delivery: at clock t0, actually swaps.
	if result, err := op.activate(context.Background(), activationParams(t, act), clock.now); err != nil || !result.Confirmed {
		t.Fatalf("first activate = (%+v, %v), want confirmed", result, err)
	}
	firstReload, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	firstAppliedAt := firstReload[0].AppliedAt

	// Redelivery: clock has advanced (a real redelivery would not arrive
	// at literally the same instant), but nothing about the already-
	// correct surface may be disturbed.
	clock.advance(5 * time.Second)
	if result, err := op.activate(context.Background(), activationParams(t, act), clock.now); err != nil || !result.Confirmed {
		t.Fatalf("redelivered activate = (%+v, %v), want confirmed", result, err)
	}

	secondReload, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	if len(secondReload) != 1 {
		t.Fatalf("persisted assignments after redelivery = %+v, want exactly one", secondReload)
	}
	if !secondReload[0].AppliedAt.Equal(firstAppliedAt) {
		t.Fatalf("AppliedAt changed on a redelivered, already-correct activation: first=%v second=%v (the assignment was re-persisted, meaning the surface was disturbed)",
			firstAppliedAt, secondReload[0].AppliedAt)
	}
}

// currentFrameWriter reaches into renderOps' own writer table for surfaceID
// under its lock, package-internal test-only access exactly like
// pipeline_test.go's own procFor — not something a real caller ever needs,
// since a real caller only ever drives the writer through renderOperations'
// own methods. Returning the pointer itself, not a value, is the point: it
// is what lets a test tell "the same writer kept running" apart from "a new
// writer happened to end up drawing the same content" — a restart replaces
// this pointer even when nothing about what reaches the wall changes.
func currentFrameWriter(o *renderOperations, surfaceID string) *pipeline.FrameWriter {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, ok := o.writers[surfaceID]
	if !ok {
		return nil
	}
	return h.fw
}

// establishRenderUnderAuth activates surfaceID under act's own authorization
// tuple with out's resolved content, for a test that needs a REAL, non-nil
// [pipeline.Assignment.Auth] to already be in place before exercising a
// second activation under a DIFFERENT tuple — setupActivatedSurface's own
// initial render.surface.apply carries no authorization tuple at all
// (fseqApplyParams sends no show/generation/catalogRevision), so it cannot
// stand in for "the surface was already correctly activated under some
// prior CatalogRevision," which is the state every test below needs to
// start from.
func establishRenderUnderAuth(t *testing.T, renderOps *renderOperations, act cueactivation.Activation, out cuecatalog.RenderOutput, clock *fakeClock) {
	t.Helper()
	if err := renderOps.activateRender(act, out, clock.now); err != nil {
		t.Fatalf("establishing activation under %+v: %v", act, err)
	}
	if !renderOps.hasRunningFrameWriter("surface-1") {
		t.Fatalf("test setup error: surface-1 is not running after the establishing activation")
	}
}

// TestActivateRenderCatalogDeployWithUnchangedContentSkipsRestart is
// this seam's own regression test: an operator deploying a new catalog to
// a node mid-show mints a new CatalogRevision even when the deploy left
// the running Cue's resolved sequence and content hash untouched. Before
// the fix, surfaceAlreadyActivated's CatalogRevision comparison alone
// forced a stop-then-start swap here — the wall-visible gap
// cueactivationrender.go's own doc comment names — for a deploy that
// changed nothing this surface is drawing. The persisted assignment's
// CatalogRevision must still catch up to the new one, with no restart, so
// a later reboot's boot-clearing rule does not wrongly discard a
// currently-correct assignment (see refreshAssignmentAuth's own comment).
func TestActivateRenderCatalogDeployWithUnchangedContentSkipsRestart(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	sharedPath := writeSynthFSEQ(t, dir, "shared.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	out := cuecatalog.RenderOutput{Sequence: "seq-shared", Filename: "shared.fseq", AssetHashes: []string{sharedHash}}

	firstAct := testActivation("act-deploy-first", "cue-7", 1, "halloween-2026", 3, "rev-a", 0)
	establishRenderUnderAuth(t, renderOps, firstAct, out, clock)
	writerBefore := currentFrameWriter(renderOps, "surface-1")
	if writerBefore == nil {
		t.Fatalf("test setup error: no frame writer running after establishing activation")
	}

	// Same Show, same Generation, a NEW CatalogRevision (the mid-show
	// deploy), and the SAME resolved filename/hash: a redeploy that left
	// this Cue's content untouched.
	clock.advance(5 * time.Second)
	deployAct := testActivation("act-deploy-second", "cue-7", 1, "halloween-2026", 3, "rev-b", 0)
	if err := renderOps.activateRender(deployAct, out, clock.now); err != nil {
		t.Fatalf("activateRender after unchanged-content deploy: %v", err)
	}

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter != writerBefore {
		t.Fatalf("frame writer was restarted for a catalog deploy that resolved to unchanged content (surfaceAlreadyActivated must ignore CatalogRevision)")
	}

	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, want exactly one", reloaded)
	}
	if reloaded[0].Auth == nil {
		t.Fatalf("persisted assignment carries no authorization tuple after the deploy")
	}
	wantAuth := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-b"}
	if *reloaded[0].Auth != wantAuth {
		t.Fatalf("persisted authorization tuple = %+v, want %+v (a stale persisted CatalogRevision would wrongly discard this assignment at the next boot)",
			*reloaded[0].Auth, wantAuth)
	}
}

// TestActivateRenderCatalogDeployWithChangedContentStillSwaps is the
// acceptance line's other half: a mid-show deploy that DOES change the
// running Cue's resolved content must still restart the frame writer.
// Proves the fix did not loosen the comparison further than
// CatalogRevision alone.
func TestActivateRenderCatalogDeployWithChangedContentStillSwaps(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	firstPath := writeSynthFSEQ(t, dir, "first.fseq", cueActivationRenderChannelCount, 10, 25)
	firstHash, err := hashFile(firstPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	firstOut := cuecatalog.RenderOutput{Sequence: "seq-first", Filename: "first.fseq", AssetHashes: []string{firstHash}}
	firstAct := testActivation("act-deploy-changed-first", "cue-8", 1, "halloween-2026", 3, "rev-a", 0)
	establishRenderUnderAuth(t, renderOps, firstAct, firstOut, clock)
	writerBefore := currentFrameWriter(renderOps, "surface-1")

	secondPath := writeSynthFSEQ(t, dir, "second.fseq", cueActivationRenderChannelCount, 10, 25)
	secondHash, err := hashFile(secondPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	secondOut := cuecatalog.RenderOutput{Sequence: "seq-second", Filename: "second.fseq", AssetHashes: []string{secondHash}}
	clock.advance(5 * time.Second)
	secondAct := testActivation("act-deploy-changed-second", "cue-8", 1, "halloween-2026", 3, "rev-b", 0)
	if err := renderOps.activateRender(secondAct, secondOut, clock.now); err != nil {
		t.Fatalf("activateRender after changed-content deploy: %v", err)
	}

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter == writerBefore {
		t.Fatalf("frame writer was NOT restarted for a catalog deploy that changed the resolved content")
	}

	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if params["fseqFilename"] != "second.fseq" {
		t.Fatalf("persisted fseqFilename = %v, want second.fseq", params["fseqFilename"])
	}
}

// TestActivateRenderDifferentShowStillSwapsDespiteIdenticalContent proves
// Show stays strict even though CatalogRevision no longer gates: a
// genuinely different Show authorizing the identical resolved content is
// not "the same authorization, just redeployed" and must still restart the
// writer, never be folded into the mid-show-deploy skip.
func TestActivateRenderDifferentShowStillSwapsDespiteIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	sharedPath := writeSynthFSEQ(t, dir, "shared-show.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	out := cuecatalog.RenderOutput{Sequence: "seq-shared-show", Filename: "shared-show.fseq", AssetHashes: []string{sharedHash}}

	firstAct := testActivation("act-show-first", "cue-9", 1, "halloween-2026", 3, "rev-a", 0)
	establishRenderUnderAuth(t, renderOps, firstAct, out, clock)
	writerBefore := currentFrameWriter(renderOps, "surface-1")

	// A DIFFERENT Show, same Generation and CatalogRevision, identical
	// resolved filename/hash: must still swap.
	clock.advance(5 * time.Second)
	otherShowAct := testActivation("act-show-second", "cue-9", 1, "winter-2026", 3, "rev-a", 0)
	if err := renderOps.activateRender(otherShowAct, out, clock.now); err != nil {
		t.Fatalf("activateRender under a different Show: %v", err)
	}

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter == writerBefore {
		t.Fatalf("frame writer was NOT restarted for a genuinely different Show, despite identical resolved content (Show must stay strict)")
	}
}

// TestActivateRenderDifferentGenerationStillSwapsDespiteIdenticalContent
// is TestActivateRenderDifferentShowStillSwapsDespiteIdenticalContent's
// Generation counterpart: Generation stays strict for the identical reason
// Show does.
func TestActivateRenderDifferentGenerationStillSwapsDespiteIdenticalContent(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	sharedPath := writeSynthFSEQ(t, dir, "shared-gen.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	out := cuecatalog.RenderOutput{Sequence: "seq-shared-gen", Filename: "shared-gen.fseq", AssetHashes: []string{sharedHash}}

	firstAct := testActivation("act-gen-first", "cue-10", 1, "halloween-2026", 3, "rev-a", 0)
	establishRenderUnderAuth(t, renderOps, firstAct, out, clock)
	writerBefore := currentFrameWriter(renderOps, "surface-1")

	// Same Show and CatalogRevision, a DIFFERENT Generation, identical
	// resolved filename/hash: must still swap.
	clock.advance(5 * time.Second)
	otherGenAct := testActivation("act-gen-second", "cue-10", 1, "halloween-2026", 4, "rev-a", 0)
	if err := renderOps.activateRender(otherGenAct, out, clock.now); err != nil {
		t.Fatalf("activateRender under a different Generation: %v", err)
	}

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter == writerBefore {
		t.Fatalf("frame writer was NOT restarted for a genuinely different Generation, despite identical resolved content (Generation must stay strict)")
	}
}

// TestActivateRenderBothHashesEmptyRefusesRatherThanVacuouslyMatches is
// the empty-hash hazard's first case: BOTH sides carry no hash — the
// persisted assignment (a legacy record with no fseqContentHash) and the
// CATALOG (out.AssetHashes resolves none, so firstAssetHash returns "").
// Comparing "" == "" would read as "identical content" with nothing
// behind it — exactly the trap that made dropping CatalogRevision unsafe
// on its own.
//
// This deliberately establishes a REAL authorization tuple first (Show
// and Generation both matching what the second activation carries), not
// merely a nil Auth: surfaceAlreadyActivated already returns false for a
// nil Auth or a Show/Generation mismatch, on an EARLIER line than the
// hash comparison, so a test that never gets past those checks would
// "pass" whether or not the hash guard does anything at all — the same
// vacuous-coverage trap the hash hazard itself is an instance of. Only a
// same-Show, same-Generation, both-hashes-empty pair actually exercises
// the guard this test is named for.
//
// The fix must refuse this activation rather than silently skip the
// restart: buildAssignedSpec's own content-hash requirement
// (requireString) already rejects an empty fseqContentHash, so a
// surfaceAlreadyActivated that correctly returns false here surfaces as a
// STATED refusal, never a silent "nothing to do."
func TestActivateRenderBothHashesEmptyRefusesRatherThanVacuouslyMatches(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	sharedPath := writeSynthFSEQ(t, dir, "no-hash-shared.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	firstAct := testActivation("act-hash-both-first", "cue-11", 1, "halloween-2026", 3, "rev-a", 0)
	firstOut := cuecatalog.RenderOutput{Sequence: "seq-no-hash-shared", Filename: "no-hash-shared.fseq", AssetHashes: []string{sharedHash}}
	establishRenderUnderAuth(t, renderOps, firstAct, firstOut, clock)
	writerBefore := currentFrameWriter(renderOps, "surface-1")

	// Strip the persisted fseqContentHash, simulating a legacy record —
	// same technique as TestActivateRenderPersistedHashMissingStillSwaps.
	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	delete(params, "fseqContentHash")
	rawParams, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encoding legacy params: %v", err)
	}
	if err := assignmentStore.Upsert(pipeline.Assignment{
		SurfaceID: "surface-1", RawParams: rawParams, AppliedAt: clock.now(), Auth: reloaded[0].Auth, CueID: reloaded[0].CueID,
	}); err != nil {
		t.Fatalf("upsert legacy-hash assignment: %v", err)
	}

	// The SAME Show and Generation, a NEW CatalogRevision, the SAME
	// filename, but the catalog resolves NO asset hash either: both sides
	// now carry no hash.
	clock.advance(5 * time.Second)
	secondAct := testActivation("act-hash-both-second", "cue-11", 1, "halloween-2026", 3, "rev-b", 0)
	secondOut := cuecatalog.RenderOutput{Sequence: "seq-no-hash-shared", Filename: "no-hash-shared.fseq", AssetHashes: nil}

	err = renderOps.activateRender(secondAct, secondOut, clock.now)
	if err == nil {
		t.Fatalf("activateRender succeeded (or silently skipped) with no resolved content hash on either side; want a stated refusal")
	}
	t.Logf("stated refusal (expected): %v", err)

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter != writerBefore {
		t.Fatalf("frame writer identity changed despite the refused activation; the original surface must be left exactly as it was")
	}
}

// TestActivateRenderPersistedHashMissingStillSwaps is the empty-hash
// hazard's second case: the PERSISTED assignment carries no
// fseqContentHash (a legacy assignment applied before this field was
// required, or one hand-edited), while the newly activated Cue resolves a
// real, non-empty hash. filename equality alone must not be read as
// agreement: the swap must proceed.
func TestActivateRenderPersistedHashMissingStillSwaps(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)

	sharedPath := writeSynthFSEQ(t, dir, "legacy-shared.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}

	// Establish a real, running surface the normal way (real hash, so
	// buildFSEQAssignment's own requireString accepts it and a real
	// frame writer actually starts), then overwrite the PERSISTED
	// params to strip fseqContentHash and carry act's authorization
	// tuple — simulating the exact legacy shape (fseqFilename present,
	// fseqContentHash absent) that requireString would refuse to create
	// today but that already-persisted state on a real node could still
	// carry.
	act := testActivation("act-hash-persisted-first", "cue-12", 1, "halloween-2026", 3, "rev-a", 0)
	initialParams := fseqApplyParams("surface-1", "legacy-shared.fseq", sharedHash, 1, cueActivationRenderChannelCount, 2, 2, "rgb", 40)
	if _, err := renderOps.applySurface(context.Background(), initialParams, clock.now); err != nil {
		t.Fatalf("initial applySurface: %v", err)
	}
	legacyParams := fseqApplyParams("surface-1", "legacy-shared.fseq", sharedHash, 1, cueActivationRenderChannelCount, 2, 2, "rgb", 40)
	delete(legacyParams, "fseqContentHash")
	rawParams, err := json.Marshal(legacyParams)
	if err != nil {
		t.Fatalf("encoding legacy params: %v", err)
	}
	auth := &pipeline.AssignmentAuth{Show: act.Show, Generation: act.Generation, CatalogRevision: act.CatalogRevision}
	if err := assignmentStore.Upsert(pipeline.Assignment{
		SurfaceID: "surface-1", RawParams: rawParams, AppliedAt: clock.now(), Auth: auth,
	}); err != nil {
		t.Fatalf("upsert legacy assignment: %v", err)
	}
	writerBefore := currentFrameWriter(renderOps, "surface-1")

	// The activated Cue resolves the SAME filename with a REAL hash, under
	// a new CatalogRevision.
	clock.advance(5 * time.Second)
	secondAct := testActivation("act-hash-persisted-second", "cue-12", 1, "halloween-2026", 3, "rev-b", 0)
	out := cuecatalog.RenderOutput{Sequence: "seq-legacy-shared", Filename: "legacy-shared.fseq", AssetHashes: []string{sharedHash}}
	if err := renderOps.activateRender(secondAct, out, clock.now); err != nil {
		t.Fatalf("activateRender against a persisted assignment with no recorded hash: %v", err)
	}

	writerAfter := currentFrameWriter(renderOps, "surface-1")
	if writerAfter == writerBefore {
		t.Fatalf("frame writer was NOT restarted when the persisted assignment carried no content hash to verify against (filename equality alone must not count as agreement)")
	}

	reloaded, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	var gotParams map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &gotParams); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if gotParams["fseqContentHash"] != sharedHash {
		t.Fatalf("persisted fseqContentHash = %v, want %s (the swap should have recorded the newly verified hash)", gotParams["fseqContentHash"], sharedHash)
	}
}

// TestActivateRenderRefreshedAssignmentSurvivesBootResumeWhereStaleWouldNot
// is the reboot-path regression test refreshAssignmentAuth exists for.
// Asserting the assignment was merely re-persisted with the new tuple
// proves the mechanism RAN, not that it WORKS — the actual failure it
// prevents only shows up by feeding the persisted state into
// decideBootResume (TRACK-H-H3-SPEC.md section 7's boot-clearing rule)
// against the node's now-current held catalog and comparing the two
// possible persisted states side by side: what refreshAssignmentAuth
// actually wrote for this surface, and what the store would still hold
// had refreshAssignmentAuth NOT run (the stale CatalogRevision from
// before the deploy). Same held catalog, same surface, two different
// on-disk states, two different boot-resume outcomes: that contrast is
// what proves the fix, not either half alone.
func TestActivateRenderRefreshedAssignmentSurvivesBootResumeWhereStaleWouldNot(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	assignmentStore := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, assignmentStore, dir, clock)
	setupActivatedSurface(t, renderOps, dir, "old.fseq", clock)

	sharedPath := writeSynthFSEQ(t, dir, "shared-boot.fseq", cueActivationRenderChannelCount, 10, 25)
	sharedHash, err := hashFile(sharedPath)
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	out := cuecatalog.RenderOutput{Sequence: "seq-shared-boot", Filename: "shared-boot.fseq", AssetHashes: []string{sharedHash}}

	// The surface's own state BEFORE the mid-show deploy: authorized under
	// the OLD CatalogRevision. This is exactly what the store would still
	// hold if refreshAssignmentAuth had never run.
	firstAct := testActivation("act-boot-first", "cue-13", 1, "halloween-2026", 3, "rev-old", 0)
	establishRenderUnderAuth(t, renderOps, firstAct, out, clock)
	reloadedBeforeDeploy, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	staleAssignment := reloadedBeforeDeploy[0]

	// The mid-show deploy: same Show/Generation, a NEW CatalogRevision,
	// identical resolved content. surfaceAlreadyActivated skips the
	// restart and refreshAssignmentAuth catches the persisted
	// CatalogRevision up to "rev-new".
	clock.advance(5 * time.Second)
	deployAct := testActivation("act-boot-second", "cue-13", 1, "halloween-2026", 3, "rev-new", 0)
	if err := renderOps.activateRender(deployAct, out, clock.now); err != nil {
		t.Fatalf("activateRender after unchanged-content deploy: %v", err)
	}
	reloadedAfterDeploy, err := assignmentStore.Load()
	if err != nil {
		t.Fatalf("reloading assignments: %v", err)
	}
	refreshedAssignment := reloadedAfterDeploy[0]
	if refreshedAssignment.Auth == nil || refreshedAssignment.Auth.CatalogRevision != "rev-new" {
		t.Fatalf("test setup error: refreshed assignment Auth = %+v, want CatalogRevision rev-new", refreshedAssignment.Auth)
	}

	// The node's own held catalog, as it stands the moment it reboots
	// after the deploy: it now holds "rev-new", the same revision the
	// deploy authorized, because a real node's held catalog is what the
	// coordinator most recently deployed, independent of what any single
	// surface's assignment happens to say.
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-new"}

	refreshedDecision := decideBootResume(refreshedAssignment, held, true)
	if !refreshedDecision.Authorized {
		t.Fatalf("decideBootResume discarded the refreshed assignment at boot: %s (refreshAssignmentAuth exists to prevent exactly this)", refreshedDecision.Reason)
	}

	staleDecision := decideBootResume(staleAssignment, held, true)
	if staleDecision.Authorized {
		t.Fatalf("test setup error: decideBootResume authorized the pre-deploy assignment against the post-deploy held catalog; the two states must differ for this test to prove anything")
	}
}
