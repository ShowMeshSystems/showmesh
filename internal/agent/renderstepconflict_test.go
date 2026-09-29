package agent

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

type stepFixture struct {
	t     *testing.T
	dir   string
	clock *fakeClock
	ops   *renderOperations
	store *pipeline.AssignmentStore
}

func newStepFixture(t *testing.T) *stepFixture {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	return &stepFixture{t: t, dir: dir, clock: clock, store: store, ops: newTestRenderOperations(sup, store, dir, clock)}
}

func (f *stepFixture) params(surfaceID, file string, stepMS byte) map[string]any {
	f.t.Helper()
	path := writeSynthFSEQ(f.t, f.dir, file, 12, 10, stepMS)
	hash, err := hashFile(path)
	if err != nil {
		f.t.Fatalf("hashFile: %v", err)
	}
	return fseqApplyParams(surfaceID, file, hash, 1, 12, 2, 2, "rgb", 40)
}

func (f *stepFixture) apply(surfaceID, file string, stepMS byte) error {
	f.t.Helper()
	_, err := f.ops.applySurface(context.Background(), f.params(surfaceID, file, stepMS), f.clock.now)
	return err
}

func (f *stepFixture) clear(surfaceID string) {
	f.t.Helper()
	if _, err := f.ops.clearSurface(context.Background(), map[string]any{"surfaceId": surfaceID}, f.clock.now); err != nil {
		f.t.Fatalf("clearSurface(%s): %v", surfaceID, err)
	}
}

func (f *stepFixture) savedFilename(surfaceID string) string {
	f.t.Helper()
	all, err := f.store.Load()
	if err != nil {
		f.t.Fatalf("store.Load: %v", err)
	}
	for _, a := range all {
		if a.SurfaceID == surfaceID {
			return string(a.RawParams)
		}
	}
	return ""
}

func (f *stepFixture) writerFile(surfaceID string) string {
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	if h, ok := f.ops.writers[surfaceID]; ok {
		return h.filename
	}
	return ""
}

func TestSecondSurfaceWithSameStepTimeApplies(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("second apply with the same step time: %v", err)
	}
}

func TestSecondSurfaceWithDifferentStepTimeIsRefusedAndNothingChanges(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		// right applied with a different step time: must fail
		want := "This sequence runs at 25 ms per frame but the surface left on this node is playing a 50 ms sequence. Render both sequences at the same frame timing in xLights."
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	} else {
		t.Fatalf("apply with a different step time succeeded, want refusal")
	}
	if got := f.writerFile("left"); got != "a.fseq" {
		t.Fatalf("left writer file = %q, want a.fseq", got)
	}
	if got := f.writerFile("right"); got != "" {
		t.Fatalf("refused surface has a writer for %q, want none", got)
	}
	if got := f.savedFilename("right"); got != "" {
		t.Fatalf("refused surface saved an assignment: %s", got)
	}
	if got := f.savedFilename("left"); !strings.Contains(got, "a.fseq") {
		t.Fatalf("left saved assignment = %s, want a.fseq", got)
	}
}

func TestRefusedReapplyKeepsSurfacePreviousState(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 50); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	if err := f.apply("right", "c.fseq", 25); err == nil {
		t.Fatalf("re-apply changing step time while another surface holds content succeeded, want refusal")
	}
	if got := f.writerFile("right"); got != "b.fseq" {
		t.Fatalf("right writer file = %q, want b.fseq", got)
	}
	if got := f.savedFilename("right"); !strings.Contains(got, "b.fseq") {
		t.Fatalf("right saved assignment = %s, want b.fseq", got)
	}
}

func TestSameSurfaceReapplyMayChangeStepTimeWhenAlone(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := f.apply("left", "b.fseq", 25); err != nil {
		t.Fatalf("re-apply with a new step time and no other surface: %v", err)
	}
	if got := f.writerFile("left"); got != "b.fseq" {
		t.Fatalf("left writer file = %q, want b.fseq", got)
	}
}

func TestClearingOtherSurfaceLetsDifferentStepTimeApply(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err == nil {
		t.Fatalf("mismatched apply succeeded, want refusal")
	}
	f.clear("left")
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("apply after clearing the other surface: %v", err)
	}
}

func TestIdleSurfaceIsNeverRefused(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	idle := f.params("right", "unused.fseq", 25)
	delete(idle, "fseqFilename")
	delete(idle, "fseqContentHash")
	if _, err := f.ops.applySurface(context.Background(), idle, f.clock.now); err != nil {
		t.Fatalf("idle apply: %v", err)
	}
	if err := f.apply("left", "c.fseq", 25); err != nil {
		t.Fatalf("left re-apply while the other surface is idle: %v", err)
	}
}

func TestResumeRefusesSurfaceWithDifferentStepTime(t *testing.T) {
	f := newStepFixture(t)
	if err := f.ops.ResumeAssignment("left", f.params("left", "a.fseq", 50)); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	err := f.ops.ResumeAssignment("right", f.params("right", "b.fseq", 25))
	if err == nil || !strings.Contains(err.Error(), "playing a 50 ms sequence") {
		t.Fatalf("second resume error = %v, want a step time refusal", err)
	}
	if got := f.writerFile("left"); got != "a.fseq" {
		t.Fatalf("left writer file = %q, want a.fseq", got)
	}
	if got := f.writerFile("right"); got != "" {
		t.Fatalf("refused resume started a writer for %q", got)
	}
}

// timelineStepMS reads back the shared timeline's step time by starting a
// sequence at frame 10 with no usable elapsed seconds.
func (f *stepFixture) timelineStepMS() int64 {
	f.t.Helper()
	tl := f.ops.timeline
	tl.Observe(multisync.SyncPacket{Action: multisync.SyncActionOpen, Filename: "probe.fseq", FileType: multisync.SyncFileTypeSequence}, "master-1")
	tl.Observe(multisync.SyncPacket{Action: multisync.SyncActionStart, Filename: "probe.fseq", FileType: multisync.SyncFileTypeSequence, FrameNumber: 10}, "master-1")
	return tl.Snapshot().PositionMS / 10
}

func (f *stepFixture) requireTimelineStep(want int64) {
	f.t.Helper()
	if got := f.timelineStepMS(); got != want {
		f.t.Fatalf("shared timeline step time = %d ms, want %d ms", got, want)
	}
}

func (f *stepFixture) renderOutput(file string, stepMS byte) cuecatalog.RenderOutput {
	f.t.Helper()
	path := writeSynthFSEQ(f.t, f.dir, file, 12, 10, stepMS)
	hash, err := hashFile(path)
	if err != nil {
		f.t.Fatalf("hashFile: %v", err)
	}
	return cuecatalog.RenderOutput{Sequence: "seq-" + file, Filename: file, AssetHashes: []string{hash}}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("cannot list open files: %v", err)
	}
	return len(entries)
}

func TestConcurrentAppliesWithDifferentStepTimesLetExactlyOneWin(t *testing.T) {
	for round := 0; round < 25; round++ {
		f := newStepFixture(t)
		pa := f.params("left", "a.fseq", 50)
		pb := f.params("right", "b.fseq", 25)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, p := range []map[string]any{pa, pb} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = f.ops.applySurface(context.Background(), p, f.clock.now)
			}()
		}
		close(start)
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("round %d: errors = %v, %v, want exactly one refusal", round, errs[0], errs[1])
		}
		f.ops.mu.Lock()
		writers, reserved := len(f.ops.writers), len(f.ops.stepReserved)
		f.ops.mu.Unlock()
		if writers != 1 || reserved != 0 {
			t.Fatalf("round %d: %d writers and %d reservations, want 1 and 0", round, writers, reserved)
		}
	}
}

func TestSuccessfulAppliesSetTheSharedTimelineStepTime(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 50); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	f.requireTimelineStep(50)
	f.clear("right")
	if err := f.apply("left", "c.fseq", 25); err != nil {
		t.Fatalf("lone re-apply: %v", err)
	}
	f.requireTimelineStep(25)
}

func TestRefusedApplyLeavesTheSharedTimelineStepTime(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("apply: %v", err)
	}
	_ = f.apply("right", "b.fseq", 25)
	f.requireTimelineStep(50)
}

func TestResumeRefusalKeepsSavedAssignmentAndFirstStepTime(t *testing.T) {
	f := newStepFixture(t)
	right := f.params("right", "b.fseq", 25)
	raw := []byte(`{"surfaceId":"right"}`)
	if err := f.store.Upsert(pipeline.Assignment{SurfaceID: "right", RawParams: raw, AppliedAt: f.clock.now()}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := f.ops.ResumeAssignment("left", f.params("left", "a.fseq", 50)); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	if err := f.ops.ResumeAssignment("right", right); err == nil {
		t.Fatalf("second resume with a different step time succeeded, want refusal")
	}
	if got := f.savedFilename("right"); !strings.Contains(got, `"right"`) {
		t.Fatalf("saved assignment for the refused surface = %q, want it kept", got)
	}
	f.requireTimelineStep(50)
	f.ops.mu.Lock()
	reserved := len(f.ops.stepReserved)
	f.ops.mu.Unlock()
	if reserved != 0 {
		t.Fatalf("%d step time reservations left after a refused resume", reserved)
	}
}

func (f *stepFixture) reservations() int {
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	return len(f.ops.stepReserved)
}

func TestCueActivationConflictingWithASurfaceOutsideTheSetRefusesEverything(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	// right keeps playing but is no longer assigned, so the Cue's set is left alone.
	if err := f.store.Remove("right"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	out := f.renderOutput("new.fseq", 50)
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	before := f.savedFilename("left")
	fds := openFDCount(t)
	err := f.ops.activateRender(act, out, f.clock.now)
	if err == nil || !strings.Contains(err.Error(), `surface "left": This sequence runs at 50 ms per frame but the surface right`) {
		t.Fatalf("error = %v, want a refusal that names surface left", err)
	}
	if got := f.writerFile("left"); got != "a.fseq" {
		t.Fatalf("left writer file = %q, want a.fseq", got)
	}
	if got := f.savedFilename("left"); got != before {
		t.Fatalf("saved assignment changed to %s", got)
	}
	if got := openFDCount(t); got != fds {
		t.Fatalf("open files went from %d to %d, want the refused file closed", fds, got)
	}
	if n := f.reservations(); n != 0 {
		t.Fatalf("%d step time reservations left after a refused Cue", n)
	}
	f.requireTimelineStep(25)
}

func (f *stepFixture) breakSavedAssignment(surfaceID string) {
	f.t.Helper()
	if err := f.store.Upsert(pipeline.Assignment{SurfaceID: surfaceID, RawParams: []byte(`[]`), AppliedAt: f.clock.now()}); err != nil {
		f.t.Fatalf("Upsert: %v", err)
	}
}

func TestPartialCueHoldsBlackASurfaceLeftAtADifferentStepTime(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 50); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 50); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	f.breakSavedAssignment("right")
	out := f.renderOutput("new.fseq", 25)
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	err := f.ops.activateRender(act, out, f.clock.now)
	if err == nil || !strings.Contains(err.Error(), `surface "right"`) || !strings.Contains(err.Error(), "so the surface is black") {
		t.Fatalf("error = %v, want it to name surface right and say it is black", err)
	}
	if got := f.writerFile("left"); got != "new.fseq" {
		t.Fatalf("left writer file = %q, want new.fseq", got)
	}
	if !f.ops.isHeldBlack("right") {
		t.Fatalf("right is not held black while it plays 50 ms under a 25 ms timeline")
	}
	if f.ops.isHeldBlack("left") {
		t.Fatalf("left is held black, want it playing")
	}
	f.requireTimelineStep(25)
}

func TestPartialCueLeavesASurfaceAtTheSameStepTimePlaying(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	f.breakSavedAssignment("right")
	out := f.renderOutput("new.fseq", 25)
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	err := f.ops.activateRender(act, out, f.clock.now)
	if err == nil || !strings.Contains(err.Error(), `surface "right"`) || strings.Contains(err.Error(), "black") {
		t.Fatalf("error = %v, want it to name surface right without holding it black", err)
	}
	if got := f.writerFile("right"); got != "b.fseq" {
		t.Fatalf("right writer file = %q, want b.fseq", got)
	}
	if f.ops.isHeldBlack("right") {
		t.Fatalf("right is held black although its step time matches")
	}
}

func TestPeerAboutToSwitchIsCheckedAgainstItsQueuedStepTime(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	both := []string{"left", "right"}
	f.queueOn("left", both, f.renderOutput("next.fseq", 50))
	if err := f.ops.reserveStepTime("right", 25, nil); err != nil {
		t.Fatalf("reserve before the switch is allowed: %v", err)
	}
	f.ops.releaseStepReservation("right")
	f.ops.mu.Lock()
	h := f.ops.writers["left"]
	q := h.queued.seq
	f.ops.mu.Unlock()
	f.queueOn("right", both, f.renderOutput("next.fseq", 50))
	if !f.ops.allowQueuedSwitch("left", h, q) {
		t.Fatalf("switch refused with both surfaces queued")
	}
	if err := f.ops.reserveStepTime("right", 25, nil); err == nil {
		t.Fatalf("reserve at 25 ms passed while left is about to play 50 ms")
	}
}

func TestCueActivationMovesAllSurfacesToANewStepTimeTogether(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	out := f.renderOutput("new.fseq", 50)
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	if err := f.ops.activateRender(act, out, f.clock.now); err != nil {
		t.Fatalf("activateRender: %v", err)
	}
	for _, id := range []string{"left", "right"} {
		if got := f.writerFile(id); got != "new.fseq" {
			t.Fatalf("%s writer file = %q, want new.fseq", id, got)
		}
	}
	f.requireTimelineStep(50)
}

func (f *stepFixture) queueOn(surfaceID string, receiving []string, out cuecatalog.RenderOutput) {
	f.t.Helper()
	f.ops.mu.Lock()
	gen := f.ops.bumpQueueGenLocked(surfaceID)
	set := make(map[string]uint64, len(receiving))
	for _, id := range receiving {
		set[id] = 0
	}
	f.ops.mu.Unlock()
	act := testActivation("act-1", "cue-1", 1, "show-1", 1, "rev-a", 0)
	f.ops.prepareQueuedRender(surfaceID, gen, set, act, "cue-2", out)
}

func (f *stepFixture) queuedFile(surfaceID string) string {
	f.ops.mu.Lock()
	defer f.ops.mu.Unlock()
	if h, ok := f.ops.writers[surfaceID]; ok && h.queued != nil {
		return h.queued.filename
	}
	return ""
}

func TestNextSequenceIsQueuedOnASingleSurfaceNode(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("apply: %v", err)
	}
	f.queueOn("left", []string{"left"}, f.renderOutput("next.fseq", 50))
	if got := f.queuedFile("left"); got != "next.fseq" {
		t.Fatalf("queued = %q, want next.fseq", got)
	}
}

func TestNextSequenceWithADifferentStepTimeIsNotQueuedWhileAnotherSurfaceKeepsPlaying(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	out := f.renderOutput("next.fseq", 50)
	f.queueOn("left", []string{"left"}, out)
	if got := f.queuedFile("left"); got != "" {
		t.Fatalf("queued = %q, want nothing while the right surface keeps its 25 ms sequence", got)
	}
	f.clear("right")
	f.queueOn("left", []string{"left"}, out)
	if got := f.queuedFile("left"); got != "next.fseq" {
		t.Fatalf("queued after the other surface cleared = %q, want next.fseq", got)
	}
}

func TestNextSequenceQueuedOnEverySurfaceMayChangeStepTimeTogether(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	out := f.renderOutput("next.fseq", 50)
	both := []string{"left", "right"}
	f.queueOn("left", both, out)
	f.queueOn("right", both, out)
	for _, id := range both {
		if got := f.queuedFile(id); got != "next.fseq" {
			t.Fatalf("%s queued = %q, want next.fseq", id, got)
		}
	}
	f.ops.mu.Lock()
	err := f.ops.stepTimeConflictLocked("left", 50, nil, true)
	f.ops.mu.Unlock()
	if err != nil {
		t.Fatalf("switch-time check with both surfaces queued: %v", err)
	}
}

func TestQueuedSwitchIsRefusedWhenAnotherSurfaceWillNotChange(t *testing.T) {
	f := newStepFixture(t)
	if err := f.apply("left", "a.fseq", 25); err != nil {
		t.Fatalf("left apply: %v", err)
	}
	if err := f.apply("right", "b.fseq", 25); err != nil {
		t.Fatalf("right apply: %v", err)
	}
	f.queueOn("left", []string{"left", "right"}, f.renderOutput("next.fseq", 50))
	f.ops.mu.Lock()
	h := f.ops.writers["left"]
	q := h.queued.seq
	f.ops.mu.Unlock()
	if f.ops.allowQueuedSwitch("left", h, q) {
		t.Fatalf("switch allowed while right still plays 25 ms with nothing queued")
	}
	f.requireTimelineStep(25)
}
