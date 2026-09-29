package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
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
