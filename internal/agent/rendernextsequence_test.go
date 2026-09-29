package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// nextSequenceRig is a node rendering wake.fseq for cue-1 with kpop.fseq
// (cue-2) in the held catalog, the shape of a planned sequence change.
type nextSequenceRig struct {
	t         *testing.T
	dir       string
	clock     *fakeClock
	sup       *pipeline.Supervisor
	renderOps *renderOperations
	catalog   *heldcatalog.FileStore
	op        *cueActivationOperation
	wake      cuecatalog.RenderOutput
	kpop      cuecatalog.RenderOutput
}

func newNextSequenceRig(t *testing.T) *nextSequenceRig {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 23, 21, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	renderOps := newTestRenderOperations(sup, pipeline.NewAssignmentStore(dir), dir, clock)
	t.Cleanup(renderOps.Shutdown)
	wakeHash := setupActivatedSurface(t, renderOps, dir, "wake.fseq", clock)
	kpopHash, err := hashFile(writeSynthFSEQ(t, dir, "kpop.fseq", cueActivationRenderChannelCount, 10, 50))
	if err != nil {
		t.Fatalf("hashFile: %v", err)
	}
	r := &nextSequenceRig{
		t: t, dir: dir, clock: clock, sup: sup, renderOps: renderOps,
		catalog: heldcatalog.NewFileStore(dir),
		wake:    cuecatalog.RenderOutput{Sequence: "seq-wake", Filename: "wake.fseq", AssetHashes: []string{wakeHash}},
		kpop:    cuecatalog.RenderOutput{Sequence: "seq-kpop", Filename: "kpop.fseq", AssetHashes: []string{kpopHash}},
	}
	r.holdCatalog("rev-a")
	r.op = &cueActivationOperation{assetDir: dir, catalogStore: r.catalog, render: renderOps}
	return r
}

func (r *nextSequenceRig) holdCatalog(revision string) {
	wake, kpop := r.wake, r.kpop
	saveHeld(r.t, r.catalog, "show-1", 1, revision, []cuecatalog.Entry{
		{CueID: "cue-1", CueRevision: 1, Outputs: cuecatalog.Outputs{Render: &wake}},
		{CueID: "cue-2", CueRevision: 1, Outputs: cuecatalog.Outputs{Render: &kpop}},
	})
}

// activate runs cue.activate for cueID naming nextCueID, and waits for the
// next sequence to be verified and queued.
func (r *nextSequenceRig) activate(activationID, cueID, catalogRevision, nextCueID string) {
	r.t.Helper()
	act := testActivation(activationID, cueID, 1, "show-1", 1, catalogRevision, 0)
	act.NextCueID = nextCueID
	result, err := r.op.activate(context.Background(), activationParams(r.t, act), r.clock.now)
	if err != nil {
		r.t.Fatalf("activate %s: %v", cueID, err)
	}
	if !result.Confirmed {
		r.t.Fatalf("activate %s did not confirm: %+v", cueID, result.Value)
	}
	r.renderOps.queueing.Wait()
}

func (r *nextSequenceRig) handle() frameWriterHandle {
	r.renderOps.mu.Lock()
	defer r.renderOps.mu.Unlock()
	h, ok := r.renderOps.writers["surface-1"]
	if !ok {
		r.t.Fatalf("surface-1 has no frame writer")
	}
	return *h
}

func (r *nextSequenceRig) play(filename string) {
	r.renderOps.timeline.Observe(multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence, Filename: filename,
	}, "fpp-01")
}

func (r *nextSequenceRig) waitFor(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *nextSequenceRig) drawing() string {
	snap, _ := r.sup.Snapshot("surface-1")
	return snap.Drawing
}

func (r *nextSequenceRig) persistedFilename() string {
	r.t.Helper()
	assignments, err := pipeline.NewAssignmentStore(r.dir).Load()
	if err != nil || len(assignments) != 1 {
		r.t.Fatalf("persisted assignments = %+v, %v", assignments, err)
	}
	var params map[string]any
	if err := json.Unmarshal(assignments[0].RawParams, &params); err != nil {
		r.t.Fatalf("decoding persisted params: %v", err)
	}
	filename, _ := params["fseqFilename"].(string)
	return filename
}

func TestPlannedSequenceChangeSwitchesAheadAndActivationKeepsTheWriter(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")
	if h := r.handle(); h.queued == nil || h.queued.filename != "kpop.fseq" {
		t.Fatalf("after cue-1's activation the queue holds %+v, want kpop.fseq", h.queued)
	}

	r.play("kpop.fseq")
	r.waitFor("the writer to switch to kpop.fseq", func() bool { return r.handle().ahead != nil })
	r.waitFor("content drawn from kpop.fseq", func() bool { return r.drawing() == pipeline.DrawingContent })
	writer := r.handle().fw
	if got := r.persistedFilename(); got != "wake.fseq" {
		t.Fatalf("persisted filename before cue-2's activation = %q, want wake.fseq until the activation confirms", got)
	}

	r.activate("act-2", "cue-2", "rev-a", "")
	h := r.handle()
	if h.fw != writer {
		t.Fatalf("cue-2's activation restarted the frame writer, want it kept")
	}
	if h.ahead != nil || h.queued != nil {
		t.Fatalf("after cue-2's activation ahead=%+v queued=%+v, want both settled", h.ahead, h.queued)
	}
	if got := r.persistedFilename(); got != "kpop.fseq" {
		t.Fatalf("persisted filename after cue-2's activation = %q, want kpop.fseq", got)
	}
	if got := r.drawing(); got != pipeline.DrawingContent {
		t.Fatalf("drawing after cue-2's activation = %q, want content", got)
	}
}

func TestSequenceNobodyQueuedStillDrawsBlack(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")

	r.play("halloween.fseq")
	r.waitFor("the stale black output", func() bool { return r.drawing() == pipeline.DrawingStale })
	if h := r.handle(); h.ahead != nil || h.queued == nil {
		t.Fatalf("an unqueued sequence changed the queue: ahead=%+v queued=%+v", h.ahead, h.queued)
	}
}

func TestQueuedSequenceNeverDrawsDuringABlackout(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")

	if _, err := r.renderOps.blackoutSurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, r.clock.now); err != nil {
		t.Fatalf("blackoutSurface: %v", err)
	}
	r.play("kpop.fseq")
	r.waitFor("the queue to be dropped", func() bool { return r.handle().queued == nil })
	for i := 0; i < 10; i++ {
		if got := r.drawing(); got != pipeline.DrawingBlackout {
			t.Fatalf("drawing while blacked out with the queued sequence playing = %q, want blackout", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h := r.handle(); h.ahead != nil || h.filename != "wake.fseq" {
		t.Fatalf("the writer switched during a blackout: ahead=%+v filename=%q", h.ahead, h.filename)
	}
}

func TestQueuedSequenceFromAnOlderCatalogIsReplacedAtActivation(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")
	r.play("kpop.fseq")
	r.waitFor("the writer to switch to kpop.fseq", func() bool { return r.handle().ahead != nil })
	writer := r.handle().fw

	r.holdCatalog("rev-b")
	r.activate("act-2", "cue-2", "rev-b", "")
	h := r.handle()
	if h.fw == writer {
		t.Fatalf("an activation under a newer catalog kept the writer switched under the older one, want a fresh swap")
	}
	assignments, err := pipeline.NewAssignmentStore(r.dir).Load()
	if err != nil || len(assignments) != 1 || assignments[0].Auth == nil || assignments[0].Auth.CatalogRevision != "rev-b" {
		t.Fatalf("persisted assignment = %+v, %v, want catalog revision rev-b", assignments, err)
	}
}

func TestActivationWithoutANextCueQueuesNothing(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")
	r.activate("act-1b", "cue-1", "rev-a", "")
	if h := r.handle(); h.queued != nil {
		t.Fatalf("an activation naming no next Cue left %+v queued", h.queued)
	}
	r.activate("act-1c", "cue-1", "rev-a", "cue-missing")
	if h := r.handle(); h.queued != nil {
		t.Fatalf("a next Cue missing from the held catalog left %+v queued", h.queued)
	}
}

func TestRenderReportNamesTheSequenceDrawnAheadOfItsActivation(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")
	r.play("kpop.fseq")
	r.waitFor("the writer to switch to kpop.fseq", func() bool { return r.handle().ahead != nil })

	pub := newFakePublisher()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	publishOneRenderReport(ctx, pub, "showmesh/nodes/media-03/observed/render", "media-03", r.sup, pipeline.NewAssignmentStore(r.dir), r.renderOps,
		newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, discardLogger())
	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	rep := surfaceReport(t, decodeRenderReport(t, calls[0].payload), "surface-1")
	if rep.FSEQFilename != "kpop.fseq" || rep.FSEQContentHash != r.kpop.AssetHashes[0] || rep.CueID != "cue-2" || rep.CatalogRevision != "rev-a" {
		t.Fatalf("report names %q %q cue %q revision %q, want the kpop.fseq sequence the surface draws", rep.FSEQFilename, rep.FSEQContentHash, rep.CueID, rep.CatalogRevision)
	}
}

func TestPreparationStillRunningAtASurfaceApplyQueuesNothing(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "")
	r.renderOps.mu.Lock()
	gen := r.renderOps.queueGen["surface-1"]
	r.renderOps.mu.Unlock()

	setupActivatedSurface(t, r.renderOps, r.dir, "wake.fseq", r.clock)
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	r.renderOps.prepareQueuedRender("surface-1", gen, map[string]uint64{"surface-1": gen}, act, "cue-2", r.kpop)
	if h := r.handle(); h.queued != nil {
		t.Fatalf("a preparation started before the apply queued %+v on the new writer", h.queued)
	}
}

func TestCatalogDeployBetweenQueueingAndTheSwitchDrawsBlackThenActivatesNormally(t *testing.T) {
	r := newNextSequenceRig(t)
	r.play("wake.fseq")
	r.activate("act-1", "cue-1", "rev-a", "cue-2")
	if h := r.handle(); h.queued == nil {
		t.Fatalf("after cue-1's activation nothing is queued, want kpop.fseq")
	}

	wake, kpop := r.wake, r.kpop
	entries := []cuecatalog.Entry{
		{CueID: "cue-1", CueRevision: 1, Outputs: cuecatalog.Outputs{Render: &wake}},
		{CueID: "cue-2", CueRevision: 1, Outputs: cuecatalog.Outputs{Render: &kpop}},
	}
	revision := computeExpectedRevision(t, "show-1", testNodeID, 2, entries)
	deploy := &catalogDeployOperation{nodeID: testNodeID, store: r.catalog, render: r.renderOps}
	if _, err := deploy.deploy(context.Background(), paramsFromWire(t, catalogDeployWireParams{Show: "show-1", Generation: 2, Revision: revision, Entries: entries}), r.clock.now); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if h := r.handle(); h.queued != nil {
		t.Fatalf("after a catalog deploy %+v is still queued", h.queued)
	}

	r.play("kpop.fseq")
	r.waitFor("the stale black output", func() bool { return r.drawing() == pipeline.DrawingStale })
	if h := r.handle(); h.ahead != nil || h.filename != "wake.fseq" {
		t.Fatalf("the writer switched after a catalog deploy: ahead=%+v filename=%q", h.ahead, h.filename)
	}

	act := testActivation("act-2", "cue-2", 1, "show-1", 2, revision, 0)
	result, err := r.op.activate(context.Background(), activationParams(t, act), r.clock.now)
	if err != nil || !result.Confirmed {
		t.Fatalf("cue-2's activation under the new catalog = %+v, %v, want confirmed", result, err)
	}
	r.waitFor("content drawn from kpop.fseq", func() bool { return r.drawing() == pipeline.DrawingContent })
	if got := r.persistedFilename(); got != "kpop.fseq" {
		t.Fatalf("persisted filename after cue-2's activation = %q, want kpop.fseq", got)
	}
}
