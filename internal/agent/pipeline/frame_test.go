package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// fakeFrameSource is a [FrameSource] test double: ChannelRange writes a
// deterministic byte pattern (frame index repeated) so a test can assert
// exactly which frame was extracted without a real FSEQ file on disk.
type fakeFrameSource struct {
	mu            sync.Mutex
	frameCount    int
	stepTimeMS    byte
	uncoveredFrom int // -1 disables; frame >= this value returns an error
	requests      []int
}

func (f *fakeFrameSource) FrameCount() int  { return f.frameCount }
func (f *fakeFrameSource) StepTimeMS() byte { return f.stepTimeMS }
func (f *fakeFrameSource) ChannelRange(frame, start, count int, dst []byte) error {
	f.mu.Lock()
	f.requests = append(f.requests, frame)
	f.mu.Unlock()

	if f.uncoveredFrom >= 0 && frame >= f.uncoveredFrom {
		return fmt.Errorf("fakeFrameSource: frame %d not covered", frame)
	}
	for i := range dst {
		dst[i] = byte(frame%250) + 1 // never 0, so distinguishable from idle (all-zero)
	}
	return nil
}

func (f *fakeFrameSource) lastRequest() (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return 0, false
	}
	return f.requests[len(f.requests)-1], true
}

// fakeTimelineSource is a [TimelineSource] test double whose Snapshot is
// entirely test-controlled, so a test can drive every [multisync.State]
// without real MultiSync packets or real elapsed time.
type fakeTimelineSource struct {
	mu   sync.Mutex
	snap multisync.Snapshot
}

func (f *fakeTimelineSource) Snapshot() multisync.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeTimelineSource) set(state multisync.State, positionMS int64) {
	f.mu.Lock()
	f.snap = multisync.Snapshot{State: state, PositionMS: positionMS}
	f.mu.Unlock()
}

// newTestFrameWriterSupervisor builds a real Supervisor over the fake
// process starter, applies a spec for surfaceID, and waits for it to reach
// Running — so [Supervisor.Stdin] returns a real, byte-recording writer
// (the fakeProcess itself), exactly like a real gst-launch-1.0 subprocess's
// stdin pipe would once B4 attaches a real sink.
func newTestFrameWriterSupervisor(t *testing.T, surfaceID string) (*Supervisor, *fakeProcess) {
	t.Helper()
	starter := &fakeStarter{}
	sup := NewSupervisor(time.Now, starter.Start, testLogger{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if err := sup.Apply(Spec{SurfaceID: surfaceID, Stages: []Stage{{Label: "source", Elements: []Element{{Factory: "fakesrc"}}}}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, ok := sup.AwaitState(ctx, surfaceID, []State{StateRunning}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached running")
	}

	fp, ok := procFor(sup, surfaceID)
	if !ok {
		t.Fatalf("setup: no current process for %q", surfaceID)
	}
	return sup, fp
}

// procFor reaches into the supervisor's current process for the test's own
// assertions (byte-level stdin capture) — package-internal test-only
// access, not something a real caller (renderops.go) ever needs, since real
// callers only ever go through Supervisor.Stdin.
func procFor(sup *Supervisor, surfaceID string) (*fakeProcess, bool) {
	w, err := sup.Stdin(surfaceID)
	if err != nil {
		return nil, false
	}
	fp, ok := w.(*fakeProcess)
	return fp, ok
}

// TestFrameWriterWritesContentWhilePlaying proves the core path: Playing
// state, a position mapped through step time to a frame index, extracted
// via ChannelRange, and written byte-for-byte to the pipeline's stdin.
func TestFrameWriterWritesContentWhilePlaying(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 250) // 250ms / 25ms = frame 10

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, 8, IdleOutputBlack, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	waitFor(t, func() bool {
		written, _, _ := fw.Counts()
		return written >= 1
	})

	frame, ok := source.lastRequest()
	if !ok || frame != 10 {
		t.Fatalf("last ChannelRange request frame = %d (ok=%v), want 10", frame, ok)
	}

	written, late, dropped := fw.Counts()
	if written == 0 || dropped != 0 {
		t.Fatalf("written=%d late=%d dropped=%d, want written>0 dropped=0", written, late, dropped)
	}

	if len(fp.stdinSnapshot()) == 0 {
		t.Fatalf("no bytes reached the pipeline's stdin")
	}
	// Every byte written is derived from frame%250+1, so it must never be 0
	// (0 is the idle/black sentinel this test distinguishes from).
	for _, b := range fp.stdinSnapshot() {
		if b == 0 {
			t.Fatalf("stdin contains a zero byte while Playing with a covered range; want content, not idle output")
		}
	}
}

// TestFrameWriterDrawsIdleOutputWhenStopped proves build contract ruling
// 3's table for the Stopped state: idle (all-zero, "black") output, never
// content, and the FSEQ source is never even asked for a frame.
func TestFrameWriterDrawsIdleOutputWhenStopped(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StateStopped, 5000)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, 8, IdleOutputBlack, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	// NewFrameWriter itself makes one construction-time probe request (frame
	// 0) to validate coverage, independent of Stopped/Playing — capture that
	// baseline so the assertion below is "no NEW request while running,"
	// not "zero requests ever."
	requestsAtConstruction := len(source.requests)

	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	waitFor(t, func() bool {
		written, _, _ := fw.Counts()
		return written >= 1
	})

	source.mu.Lock()
	gotRequests := len(source.requests)
	source.mu.Unlock()
	if gotRequests != requestsAtConstruction {
		t.Fatalf("ChannelRange was called %d time(s) after Run started while Stopped; idle output must never touch the FSEQ source", gotRequests-requestsAtConstruction)
	}
	for _, b := range fp.stdinSnapshot() {
		if b != 0 {
			t.Fatalf("stdin byte = %d while Stopped, want all-zero idle output", b)
		}
	}
}

// TestFrameWriterHoldDrawsLastContentFrame proves IdleOutputHold: once the
// timeline goes idle, the writer keeps drawing the LAST successfully
// extracted content frame rather than black — and never asks the FSEQ
// source for a new frame while idle (the same "idle never touches the
// source" invariant TestFrameWriterDrawsIdleOutputWhenStopped proves for
// black).
func TestFrameWriterHoldDrawsLastContentFrame(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 5, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 50) // 50ms / 5ms = frame 10 -> byte value 11

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, 8, IdleOutputHold, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	// Wait for at least one real content frame to land before going idle.
	waitFor(t, func() bool {
		written, _, _ := fw.Counts()
		return written >= 1
	})
	for _, b := range fp.stdinSnapshot() {
		if b != 11 {
			t.Fatalf("stdin byte = %d while Playing frame 10, want 11 (frame%%250+1)", b)
		}
	}

	// Go idle. stdinBytes only ever grows (fake_test.go's Write appends),
	// so the "already all 11s" state from the Playing phase above would
	// trivially satisfy an unqualified "all bytes are 11" check with no
	// bytes from the idle phase at all — mark the length NOW and only
	// examine bytes written from this point on, so this test can actually
	// fail if idle drew something else.
	requestsBeforeIdle := len(source.requests)
	lenBeforeIdle := len(fp.stdinSnapshot())
	tl.set(multisync.StateStopped, 999999)
	waitFor(t, func() bool {
		snap := fp.stdinSnapshot()
		return len(snap) > lenBeforeIdle && allBytesEqual(snap[lenBeforeIdle:], 11)
	})

	source.mu.Lock()
	gotRequests := len(source.requests)
	source.mu.Unlock()
	if gotRequests != requestsBeforeIdle {
		t.Fatalf("ChannelRange was called %d time(s) after going idle in hold mode; hold must never touch the FSEQ source", gotRequests-requestsBeforeIdle)
	}
}

// allBytesEqual reports whether every byte in buf equals want.
func allBytesEqual(buf []byte, want byte) bool {
	for _, b := range buf {
		if b != want {
			return false
		}
	}
	return true
}

// TestFrameWriterDiagnosticNeverBlackAndNeverFrozen proves IdleOutputDiagnostic:
// the drawn buffer is never the all-zero black sentinel, is regenerated
// (not the last content frame held), and changes when sampled far enough
// apart in wall-clock time — proving it is live-generated rather than a
// frozen snapshot, the owner's explicit ruling on this mode.
func TestFrameWriterDiagnosticNeverBlackAndNeverFrozen(t *testing.T) {
	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}

	fw, err := NewFrameWriter(nil, "surface-1", source, tl, 0, 8, IdleOutputDiagnostic, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	got1 := fw.idleOutputFor(base)
	for _, b := range got1 {
		if b == 0 {
			t.Fatalf("diagnostic output byte = 0 (black) at t0, want a generated non-zero fill")
		}
	}

	got2 := fw.idleOutputFor(base.Add(diagnosticBlinkPeriod))
	if allBytesEqual(got2, got1[0]) {
		t.Fatalf("diagnostic output at t0+%s = %v, want it to differ from t0's %v — a diagnostic output must be REGENERATED, never frozen", diagnosticBlinkPeriod, got2, got1)
	}
	for _, b := range got2 {
		if b == 0 {
			t.Fatalf("diagnostic output byte = 0 (black) at t0+%s, want a generated non-zero fill", diagnosticBlinkPeriod)
		}
	}
}

// TestFrameWriterCountsDroppedOnStdinFailure proves a broken pipe is
// counted, not swallowed and not treated as a reason to stop the writer's
// own loop (build contract ruling 3: the frame writer never stops the
// pipeline itself).
func TestFrameWriterCountsDroppedOnStdinFailure(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)
	fp.setStdinFail(true)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 5, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 0)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, 8, IdleOutputBlack, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	waitFor(t, func() bool {
		_, _, dropped := fw.Counts()
		return dropped >= 1
	})

	written, _, dropped := fw.Counts()
	if written != 0 {
		t.Fatalf("written = %d, want 0 (every write failed)", written)
	}
	if dropped == 0 {
		t.Fatalf("dropped = 0, want > 0")
	}
}

// TestNewFrameWriterRefusesUncoveredChannelRange proves construction-time
// validation catches a misconfigured surface (its channel range not
// covered by the file) immediately, with the real error, rather than
// silently failing every subsequent frame forever.
func TestNewFrameWriterRefusesUncoveredChannelRange(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: 0} // every frame uncovered
	tl := &fakeTimelineSource{}

	if _, err := NewFrameWriter(sup, surfaceID, source, tl, 0, 8, IdleOutputBlack, testLogger{}); err == nil {
		t.Fatalf("NewFrameWriter with an uncovered channel range: want error, got nil")
	}
}

// TestFrameIndexForClampsToFrameCount proves a position past the file's own
// duration holds the last frame rather than indexing out of range.
func TestFrameIndexForClampsToFrameCount(t *testing.T) {
	fw := &FrameWriter{source: &fakeFrameSource{frameCount: 10}, stepTime: 25 * time.Millisecond}
	if got := fw.frameIndexFor(1_000_000); got != 9 {
		t.Fatalf("frameIndexFor(huge) = %d, want 9 (last frame)", got)
	}
	if got := fw.frameIndexFor(-5); got != 0 {
		t.Fatalf("frameIndexFor(negative) = %d, want 0", got)
	}
	if got := fw.frameIndexFor(0); got != 0 {
		t.Fatalf("frameIndexFor(0) = %d, want 0", got)
	}
}

// TestSampleRateNilUntilWindowCompletes proves ADR-040's obligation at the
// unit level: the first call only anchors the window (no measurement yet —
// a nil rate must never render as a plausible-looking zero), and a rate
// appears only once frameRateWindow of wall-clock time has actually
// elapsed since the anchor, computed from real elapsed time and frames
// written in it, never from stepTime or any other configured value.
func TestSampleRateNilUntilWindowCompletes(t *testing.T) {
	fw := &FrameWriter{stepTime: 25 * time.Millisecond}
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	fw.sampleRate(base, 1) // anchors the window; written=1 at the anchor
	if fw.currentRate != nil {
		t.Fatalf("currentRate after the anchoring call = %v, want nil", fw.currentRate)
	}

	fw.sampleRate(base.Add(2*time.Second), 81) // 2s elapsed, short of the window
	if fw.currentRate != nil {
		t.Fatalf("currentRate before the window completes = %v, want nil", fw.currentRate)
	}

	fw.sampleRate(base.Add(6*time.Second), 241) // 6s elapsed since the anchor: window closes
	if fw.currentRate == nil {
		t.Fatalf("currentRate after the window completes = nil, want a measurement")
	}
	got := *fw.currentRate
	want := float64(241-1) / (6 * time.Second).Seconds() // frames since the anchor / elapsed since the anchor
	if got < want-0.01 || got > want+0.01 {
		t.Fatalf("currentRate = %v, want ~%v (achieved, not the configured stepTime rate)", got, want)
	}
}

// waitFor polls cond every 2ms for up to 2s, failing the test if it never
// becomes true — the frame writer runs on its own goroutine and ticks at
// real wall-clock intervals, so tests observe it by polling rather than by
// a fake clock (unlike this package's supervisor tests, which inject one).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition never became true within 2s")
}
