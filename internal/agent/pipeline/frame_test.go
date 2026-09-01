package pipeline

import (
	"bytes"
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

// setWithFilename is [fakeTimelineSource.set] plus a reported filename, for
// tests that drive the timeline-versus-held-sequence mismatch check rather
// than only state/position.
func (f *fakeTimelineSource) setWithFilename(state multisync.State, positionMS int64, filename string) {
	f.mu.Lock()
	f.snap = multisync.Snapshot{State: state, PositionMS: positionMS, Filename: filename}
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
	if _, ok := sup.AwaitState(ctx, surfaceID, []State{StateRunning}, time.Time{}, -1, 5*time.Millisecond); !ok {
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

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
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

// TestFrameWriterReportsDrawStateOnSupervisorSnapshot is finding 7's
// regression test: the render report must carry what a surface is actually
// drawing (content vs idle, which idle mode, and the timeline state/
// position behind it), not just PipelineState=="running". Drives content,
// then idle (Stopped), and asserts the supervisor's own Snapshot — the
// exact source renderreport.go's toRenderSurfaceReport reads from —
// reflects each transition. Remove FrameWriter's SetDrawState call (or
// revert writeOneFrame/recordTick to not compute drawing/idleMode/position)
// to see this fail.
func TestFrameWriterReportsDrawStateOnSupervisorSnapshot(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 250) // 250ms / 25ms = frame 10

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputDiagnostic, nil, testLogger{})
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
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingContent
	})
	snap, ok := sup.Snapshot(surfaceID)
	if !ok {
		t.Fatalf("no snapshot for %q", surfaceID)
	}
	if snap.Drawing != DrawingContent {
		t.Fatalf("Drawing = %q while Playing with a covered range, want %q", snap.Drawing, DrawingContent)
	}
	if snap.TimelineState != string(multisync.StatePlaying) {
		t.Fatalf("TimelineState = %q, want %q", snap.TimelineState, multisync.StatePlaying)
	}
	if snap.TimelinePositionMS == nil || *snap.TimelinePositionMS != 250 {
		t.Fatalf("TimelinePositionMS = %v, want a pointer to 250", snap.TimelinePositionMS)
	}
	if snap.IdleMode != "" {
		t.Fatalf("IdleMode = %q while drawing content, want empty", snap.IdleMode)
	}

	// Now go idle (Stopped) — this is the exact scenario the finding names:
	// a MultiSync bind failure (or any cause) leaves the timeline at a
	// non-content state, and the writer must report idle drawing, never a
	// silent "still running" with no distinction from content.
	tl.set(multisync.StateStopped, 999999)
	waitFor(t, func() bool {
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingIdle
	})
	snap, ok = sup.Snapshot(surfaceID)
	if !ok {
		t.Fatalf("no snapshot for %q", surfaceID)
	}
	if snap.Drawing != DrawingIdle {
		t.Fatalf("Drawing = %q while Stopped, want %q", snap.Drawing, DrawingIdle)
	}
	if snap.TimelineState != string(multisync.StateStopped) {
		t.Fatalf("TimelineState = %q, want %q", snap.TimelineState, multisync.StateStopped)
	}
	if snap.TimelinePositionMS != nil {
		t.Fatalf("TimelinePositionMS = %v while idle, want nil (a position is not meaningful for idle output)", *snap.TimelinePositionMS)
	}
	if snap.IdleMode != IdleOutputDiagnostic {
		t.Fatalf("IdleMode = %q while Stopped with idleOutput=diagnostic, want %q", snap.IdleMode, IdleOutputDiagnostic)
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

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
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

// TestFrameWriterStopsDrawingOnSequenceMismatch is the regression test for
// a render surface drawing the wrong sequence forever as long as sync
// keeps arriving: the timeline reports Playing, sync keeps arriving, but
// the filename it reports is not the sequence this writer opened. Before this
// fix, Playing was drawn as content unconditionally, whatever FSEQ the
// surface held — this is the exact rehearsal-stack incident (a node kept
// rendering the previous show's sequence once FPP moved to one the node had
// no content for). The FSEQ source must never even be asked for a frame:
// its buffer holds content for the WRONG sequence, so there is nothing safe
// to extract from it for this tick.
func TestFrameWriterStopsDrawingOnSequenceMismatch(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.setWithFilename(multisync.StatePlaying, 250, "kpop.fseq")

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "resting.fseq", 0, 8, 8, 1, IdleOutputHold, nil, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	requestsAtConstruction := len(source.requests)

	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	waitFor(t, func() bool {
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingStale
	})
	snap, ok := sup.Snapshot(surfaceID)
	if !ok {
		t.Fatalf("no snapshot for %q", surfaceID)
	}
	if snap.Drawing != DrawingStale {
		t.Fatalf("Drawing = %q while timeline filename disagrees with the held sequence, want %q", snap.Drawing, DrawingStale)
	}
	// The reason must be visible on the reported evidence itself (the
	// acceptance line this test proves), not only inside the frame writer:
	// TimelineState still says Playing, so a reader of this snapshot alone
	// can see sync is arriving AND that this surface stopped drawing
	// content for it.
	if snap.TimelineState != string(multisync.StatePlaying) {
		t.Fatalf("TimelineState = %q, want %q (sync is still arriving)", snap.TimelineState, multisync.StatePlaying)
	}
	if snap.TimelinePositionMS != nil {
		t.Fatalf("TimelinePositionMS = %v while stale, want nil", *snap.TimelinePositionMS)
	}
	// idleOutput is configured as Hold: if the stale check fell through to
	// idleOutputFor as an ordinary idle tick, Hold would draw fw.buf, the
	// last successfully extracted frame — which, for a writer that has
	// never had a healthy tick, is still its zero-valued buffer, so this
	// alone would not catch the regression. IdleMode being reported empty
	// is the real proof: a genuine Hold idle tick always reports
	// IdleMode == IdleOutputHold (see TestFrameWriterHoldDrawsLastContentFrame),
	// so an empty IdleMode here proves the stale branch bypassed
	// idleOutputFor entirely rather than merely happening to draw black.
	if snap.IdleMode != "" {
		t.Fatalf("IdleMode = %q while stale, want empty (a stale mismatch is not an idle mode)", snap.IdleMode)
	}
	if snap.FailureOutput != "" {
		t.Fatalf("FailureOutput = %q while stale, want empty (a stale mismatch is not an extraction failure)", snap.FailureOutput)
	}

	source.mu.Lock()
	gotRequests := len(source.requests)
	source.mu.Unlock()
	if gotRequests != requestsAtConstruction {
		t.Fatalf("ChannelRange was called %d time(s) after Run started while stale; the writer must never extract from a source holding the wrong sequence", gotRequests-requestsAtConstruction)
	}
	for _, b := range fp.stdinSnapshot() {
		if b != 0 {
			t.Fatalf("stdin byte = %d while stale, want all-zero output — never the previous sequence's content", b)
		}
	}
}

// TestFrameWriterKeepsRenderingWhenFilenameMatches proves the mismatch
// check does not blank a healthy surface: the timeline's reported filename
// matching the writer's own sequence is content, exactly as before this
// fix, with no dropped frames introduced by the new comparison.
func TestFrameWriterKeepsRenderingWhenFilenameMatches(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.setWithFilename(multisync.StatePlaying, 250, "kpop.fseq") // 250ms / 25ms = frame 10

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "kpop.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
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
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingContent
	})

	frame, ok := source.lastRequest()
	if !ok || frame != 10 {
		t.Fatalf("last ChannelRange request frame = %d (ok=%v), want 10", frame, ok)
	}
	written, _, dropped := fw.Counts()
	if written == 0 || dropped != 0 {
		t.Fatalf("written=%d dropped=%d, want written>0 dropped=0 when the held sequence matches the timeline's reported filename", written, dropped)
	}
	snap, ok := sup.Snapshot(surfaceID)
	if !ok || snap.Drawing != DrawingContent {
		t.Fatalf("Drawing = %q (ok=%v), want %q when the filenames match", snap.Drawing, ok, DrawingContent)
	}
	for _, b := range fp.stdinSnapshot() {
		if b == 0 {
			t.Fatalf("stdin contains a zero byte with matching filenames; want content, not the stale/idle output")
		}
	}
}

// TestFrameWriterDrawsContentWhenTimelineFilenameNotYetObserved proves the
// other half of the mismatch check's match semantics: an empty timeline filename means
// MultiSync has not reported one yet, not a mismatch — a writer must never
// blank a surface for lack of evidence, only for evidence that actually
// disagrees (the same reading internal/agent/cueactivationrender.go's
// activateSurfaceRender already gives an empty Snapshot.Filename, ADR-043
// decision 6).
func TestFrameWriterDrawsContentWhenTimelineFilenameNotYetObserved(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 250) // no filename observed yet

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "kpop.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
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
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingContent
	})
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

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputHold, nil, testLogger{})
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
// the drawn buffer is never the all-zero black sentinel, and the buffer
// idleOutputFor returns actually MUTATES between two ticks sampled far
// enough apart in wall-clock time — not merely "differs from the black
// idleBuf," but the same backing array visibly changing content, which is
// the owner's explicit ruling that this mode must be generated, never a
// frozen frame. Strengthened from the two-flat-fill original (see this
// file's blame): idleOutputFor now returns the SAME backing array
// (diagBuf, mutated in place — see frame.go's own doc comment on why that
// is deliberate, not an oversight) every call, so this test copies before
// comparing rather than relying on two distinct slices, and it directly
// re-inspects the original slice header after the second call as its
// mutation evidence.
func TestFrameWriterDiagnosticNeverBlackAndNeverFrozen(t *testing.T) {
	const diagWidth = 64
	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}

	fw, err := NewFrameWriter(nil, "surface-1", source, tl, "seq.fseq", 0, diagWidth, diagWidth, 1, IdleOutputDiagnostic, nil, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	buf1 := fw.idleOutputFor(base)
	got1 := append([]byte(nil), buf1...)
	for _, b := range got1 {
		if b == 0 {
			t.Fatalf("diagnostic output byte = 0 (black) at t0, want a generated non-zero fill")
		}
	}

	// Advance many pixel periods (never a frame count) so the bar visibly
	// moves regardless of the chosen diagWidth — see diagnosticBarColumn,
	// whose whole point is that position is a function of wall-clock time,
	// not of how many times this loop has been called.
	advance := 10 * diagnosticPixelPeriod
	got2 := append([]byte(nil), fw.idleOutputFor(base.Add(advance))...)
	if bytes.Equal(got1, got2) {
		t.Fatalf("diagnostic output at t0+%s = %v, want it to differ from t0's %v — a diagnostic output must be REGENERATED, never frozen", advance, got2, got1)
	}
	for _, b := range got2 {
		if b == 0 {
			t.Fatalf("diagnostic output byte = 0 (black) at t0+%s, want a generated non-zero fill", advance)
		}
	}

	// The mutation-in-place proof: buf1 (never re-read since the first
	// call) is the exact same backing array idleOutputFor just wrote got2's
	// bytes into — if this equals the original t0 snapshot, idleOutputFor
	// stopped mutating the buffer it returns and is now allocating fresh
	// ones instead (also a hot-path allocation, which the build contract
	// forbids).
	if bytes.Equal(got1, buf1) {
		t.Fatalf("diagBuf's backing array is unchanged after a second idleOutputFor call at t0+%s; want it mutated in place", advance)
	}
}

// TestDiagnosticBarAdvancesExactPixelsAtReferenceRate proves
// diagnosticBarColumn's contract directly: at the reference 40 fps profile
// (25ms tick cadence, matching diagnosticPixelPeriod), the bar advances by
// EXACTLY one column per tick — not "eventually moves," but a specific,
// checkable number of pixels for a specific, checkable elapsed time. Uses a
// diagWidth wide enough that the run never wraps modulo width, so the
// subtraction below is a direct pixel count rather than a wrapped one.
func TestDiagnosticBarAdvancesExactPixelsAtReferenceRate(t *testing.T) {
	const referenceStepTime = 25 * time.Millisecond // reference 40fps cadence (RES-004 / build contract ruling 5)
	const diagWidth = 1000
	const ticks = 10

	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	col0 := diagnosticBarColumn(base, diagWidth)
	colN := diagnosticBarColumn(base.Add(ticks*referenceStepTime), diagWidth)

	wantAdvance := int(int64(ticks*referenceStepTime) / int64(diagnosticPixelPeriod))
	if got := colN - col0; got != wantAdvance {
		t.Fatalf("bar advanced %d column(s) over %d reference-rate ticks (%s elapsed), want exactly %d (diagnosticPixelPeriod=%s)",
			got, ticks, ticks*referenceStepTime, wantAdvance, diagnosticPixelPeriod)
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

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
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

	if _, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{}); err == nil {
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
	if !fw.framesObservedAt.IsZero() {
		t.Fatalf("framesObservedAt after the anchoring call = %v, want zero (this issue: an unmeasured value must be zero, never defaulted to \"now\")", fw.framesObservedAt)
	}

	fw.sampleRate(base.Add(2*time.Second), 81) // 2s elapsed, short of the window
	if fw.currentRate != nil {
		t.Fatalf("currentRate before the window completes = %v, want nil", fw.currentRate)
	}
	if !fw.framesObservedAt.IsZero() {
		t.Fatalf("framesObservedAt before the window completes = %v, want zero", fw.framesObservedAt)
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

	// This issue's own fix: the window-close instant is stamped onto its
	// own framesObservedAt, independent of any pipeline.Snapshot.ObservedAt.
	// reportCounts is what carries this to Supervisor.SetFrameCounts.
	wantObservedAt := base.Add(6 * time.Second)
	if !fw.framesObservedAt.Equal(wantObservedAt) {
		t.Fatalf("framesObservedAt after the window completes = %v, want %v (the window-close instant)", fw.framesObservedAt, wantObservedAt)
	}
}

// TestRecordTickSamplesRateOnEveryTickIncludingFailures is finding 8's
// regression test at the unit level (no real 5s frameRateWindow wait
// needed, matching TestSampleRateNilUntilWindowCompletes's own synthetic-
// clock approach): sampleRate must be driven from EVERY tick, success or
// failure, using the cumulative written counter — never gated to the
// success path only, which is what let a stalled pipeline's rate freeze at
// its last good value forever. Here: one successful write anchors the
// window at written=1, then the window closes 6s later with zero further
// writes (three delivered=false ticks) — the achieved rate must converge to
// a real, measured 0.0, not stay nil (never sampled) or some stale
// leftover.
func TestRecordTickSamplesRateOnEveryTickIncludingFailures(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)
	fw := &FrameWriter{surfaceID: surfaceID, sup: sup, stepTime: 25 * time.Millisecond}
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	fw.written.Store(1)
	fw.recordTick(base, base, true) // anchors the window at written=1

	// Three failed ticks — no successful write, fw.written stays at 1 —
	// spread across the window, mirroring a stalled pipeline where every
	// stdin write fails. The regression this reproduces: the OLD code only
	// ever called sampleRate from the success path, so recordTick's calls
	// below (delivered=false) would never have reached it, and currentRate
	// would stay frozen at whatever it last was.
	fw.recordTick(base.Add(2*time.Second), base.Add(2*time.Second), false)
	fw.recordTick(base.Add(4*time.Second), base.Add(4*time.Second), false)
	fw.recordTick(base.Add(6*time.Second), base.Add(6*time.Second), false) // window closes

	if fw.currentRate == nil {
		t.Fatalf("currentRate = nil after the window closed with only failed ticks, want a real 0.0 measurement")
	}
	if *fw.currentRate != 0 {
		t.Fatalf("currentRate = %v, want 0.0 (no frames were successfully written in this window)", *fw.currentRate)
	}
}

// TestFrameWriterRateDropsToZeroAfterPipelineStalls is finding 8's
// end-to-end regression test against the real Run loop and a real
// Supervisor/fakeProcess: once every stdin write starts failing, the
// achieved rate must stop being reported as the last good value and
// converge toward 0 rather than freezing — the exact defect ("surface.
// frames.rate keeps reporting 40.0 indefinitely") named in the finding.
// frameRateWindow is a package const (5s), so this drives real ticks for
// slightly over 5 seconds; acceptable as this finding's own end-to-end
// proof, distinct from TestRecordTickSamplesRateOnEveryTickIncludingFailures
// above, which is the fast unit-level version.
func TestFrameWriterRateDropsToZeroAfterPipelineStalls(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 5, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 0)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	// Let it write successfully for a bit so a non-nil rate is established
	// first, proving this test would actually catch a freeze rather than
	// trivially passing on an always-nil rate.
	waitFor(t, func() bool {
		written, _, _ := fw.Counts()
		return written >= 1
	})

	fp.setStdinFail(true)

	// Read the rate through the Supervisor's own snapshot, never fw.
	// currentRate directly: that field is documented single-goroutine
	// (writeOneFrame's own goroutine only), and reading it from this test
	// goroutine while Run's goroutine writes it is a real data race, not
	// merely a style preference — sup.Snapshot is the properly synchronized
	// path every real caller (renderreport.go) already uses.
	deadline := time.Now().Add(7 * time.Second)
	var lastRate *float64
	for time.Now().Before(deadline) {
		snap, ok := sup.Snapshot(surfaceID)
		if ok {
			lastRate = snap.FramesRate
			if lastRate != nil && *lastRate == 0 {
				return // rate correctly converged to 0
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := "nil"
	if lastRate != nil {
		got = fmt.Sprintf("%v", *lastRate)
	}
	t.Fatalf("currentRate never converged to 0 after the pipeline stalled for over 5s; last value = %s", got)
}

// TestCountTickerDropsCountsMissedTicks is finding 13's regression test for
// the ticker-drop half: Go's time.Ticker silently drops ticks when the
// receiver falls behind rather than queuing them, so nothing about a missed
// tick shows up unless the gap between consecutive tickTime values is
// checked directly. Revert countTickerDrops to a no-op (or to only updating
// lastTickTime) to see this fail.
func TestCountTickerDropsCountsMissedTicks(t *testing.T) {
	fw := &FrameWriter{stepTime: 25 * time.Millisecond}
	base := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	fw.countTickerDrops(base) // first call only anchors; nothing to compare against yet
	if got := fw.dropped.Load(); got != 0 {
		t.Fatalf("dropped = %d after the first tick, want 0", got)
	}

	// A 100ms gap on a 25ms step is 4 step periods — 3 ticks the ticker
	// never delivered at all, standing in for a writer that fell behind.
	fw.countTickerDrops(base.Add(100 * time.Millisecond))
	if got := fw.dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d after a 100ms gap on a 25ms step, want 3 missed ticks", got)
	}

	// A normal, on-schedule next tick must not count anything further.
	fw.countTickerDrops(base.Add(125 * time.Millisecond))
	if got := fw.dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d after a normal on-schedule tick, want unchanged at 3", got)
	}
}

// TestRecordTickCountsLateFromScheduledTickTime is finding 13's regression
// test for the lateness half: lateness must be measured against tickTime
// (when this frame was SCHEDULED to happen), not against how long
// writeOneFrame's own call took to run. A tickTime far in the past with an
// instantaneous call (nothing between start and "now") is exactly the case
// the old time.Since(start)-only check was blind to: no work happened, so
// the old check would report 0 lateness even though this frame is
// hopelessly behind schedule. Revert recordTick's lateness check to
// time.Since(start) to see this fail.
func TestRecordTickCountsLateFromScheduledTickTime(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)
	fw := &FrameWriter{surfaceID: surfaceID, sup: sup, stepTime: 25 * time.Millisecond}

	staleTick := time.Now().Add(-500 * time.Millisecond)
	fw.recordTick(time.Now(), staleTick, true)

	_, late, _ := fw.Counts()
	if late != 1 {
		t.Fatalf("late = %d after a tick scheduled 500ms in the past on a 25ms step, want 1", late)
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
