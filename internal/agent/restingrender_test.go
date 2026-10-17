package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// restingEntry is the catalog entry the coordinator adds for a render node
// holding the resting sequence (ADR-061).
func restingEntry(filename, hash string) cuecatalog.Entry {
	return cuecatalog.Entry{
		CueID: cuecatalog.RestingEntryID("resting-loop"), CueRevision: cuecatalog.RestingEntryRevision,
		Outputs:  cuecatalog.Outputs{Render: &cuecatalog.RenderOutput{Sequence: "resting-loop", Filename: filename, AssetHashes: []string{hash}}},
		Triggers: []string{},
	}
}

func TestActivateRestingEntrySwapsSurfaceToTheRestingSequence(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC)}
	renderOps := newTestRenderOperations(newRenderTestSupervisor(t, clock), pipeline.NewAssignmentStore(dir), dir, clock)
	setupActivatedSurface(t, renderOps, dir, "show-last.fseq", clock)

	restingHash, err := hashFile(writeSynthFSEQ(t, dir, "RestingLoop.fseq", cueActivationRenderChannelCount, 10, 25))
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	catalogStore := heldcatalog.NewFileStore(dir)
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{restingEntry("RestingLoop.fseq", restingHash)})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-resting", cuecatalog.RestingEntryID("resting-loop"), cuecatalog.RestingEntryRevision, "halloween-2026", 3, "rev-a", 0)
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("resting activation did not confirm: %+v", result)
	}

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil || len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, err %v, want one", reloaded, err)
	}
	var params map[string]any
	if err := json.Unmarshal(reloaded[0].RawParams, &params); err != nil {
		t.Fatalf("decoding persisted params: %v", err)
	}
	if params["fseqFilename"] != "RestingLoop.fseq" || params["fseqContentHash"] != restingHash {
		t.Fatalf("persisted file = %v %v, want RestingLoop.fseq with its hash", params["fseqFilename"], params["fseqContentHash"])
	}
	if reloaded[0].CueID != cuecatalog.RestingEntryID("resting-loop") {
		t.Fatalf("persisted CueID = %q, want the resting entry", reloaded[0].CueID)
	}
	if !renderOps.hasRunningFrameWriter("surface-1") {
		t.Fatal("no frame writer running after the resting swap")
	}
}

func TestActivateRestingEntryWithWrongHashLeavesCurrentSequence(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 17, 21, 0, 0, 0, time.UTC)}
	renderOps := newTestRenderOperations(newRenderTestSupervisor(t, clock), pipeline.NewAssignmentStore(dir), dir, clock)
	oldHash := setupActivatedSurface(t, renderOps, dir, "show-last.fseq", clock)
	writeSynthFSEQ(t, dir, "RestingLoop.fseq", cueActivationRenderChannelCount, 10, 25)

	catalogStore := heldcatalog.NewFileStore(dir)
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{restingEntry("RestingLoop.fseq", "sha256:not-this-file")})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, render: renderOps}
	act := testActivation("act-resting-bad", cuecatalog.RestingEntryID("resting-loop"), cuecatalog.RestingEntryRevision, "halloween-2026", 3, "rev-a", 0)
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("resting activation confirmed with a wrong hash: %+v", result)
	}
	assertSurfaceStillOnOldFSEQ(t, dir, "show-last.fseq", oldHash)
}

func TestActivateRestingEntryRefusedDuringWeatherDelay(t *testing.T) {
	dir := t.TempDir()
	catalogStore := heldcatalog.NewFileStore(dir)
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{restingEntry("RestingLoop.fseq", "sha256:x")})
	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, weatherDelay: holder}
	act := testActivation("act-resting-delay", cuecatalog.RestingEntryID("resting-loop"), cuecatalog.RestingEntryRevision, "halloween-2026", 3, "rev-a", 0)
	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed || outcomeOf(t, result) != "weather-delay-active" {
		t.Fatalf("result = %+v, want a weather-delay refusal", result)
	}
}
