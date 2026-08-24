package pipeline

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// fakeShowMode is a [ShowModeSource] test double whose answer a test flips
// between ticks, which is the whole point: the writer must read it on the
// frame it fails on, not once at construction.
type fakeShowMode struct {
	show atomic.Bool
}

func (f *fakeShowMode) BehavesAsShow() bool { return f.show.Load() }

func newFakeShowMode(behavesAsShow bool) *fakeShowMode {
	m := &fakeShowMode{}
	m.show.Store(behavesAsShow)
	return m
}

// recordingLogger captures Warn calls so a test can prove a coercion is
// reported rather than applied silently.
type recordingLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Info(string, ...any) {}
func (l *recordingLogger) Warn(msg string, _ ...any) {
	l.mu.Lock()
	l.warns = append(l.warns, msg)
	l.mu.Unlock()
}
func (l *recordingLogger) Error(string, ...any) {}

func (l *recordingLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

// coverageGapWriter starts a writer whose every extraction fails (frame 0
// still covered, so construction succeeds) with the timeline Playing well
// past the covered frames: the exact runtime coverage gap the owner ruling
// is about. surfaceWidth*rgbBytesPerPixel is the surface's channel count,
// so the geometry resolves exactly and the alert fill is expressible as
// red pixels.
const (
	surfaceWidth      = 4
	rgbBytesPerPixel  = 3
	gapChannelCount   = surfaceWidth * rgbBytesPerPixel
	gapUncoveredFrom  = 1
	gapTimelinePosMS  = 5000
	gapSurfaceStepsMS = 5
)

func startCoverageGapWriter(t *testing.T, showMode ShowModeSource) (*Supervisor, *fakeProcess, *FrameWriter) {
	t.Helper()
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: gapSurfaceStepsMS, uncoveredFrom: gapUncoveredFrom}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, gapTimelinePosMS)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, gapChannelCount, surfaceWidth, 1, IdleOutputBlack, showMode, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})
	return sup, fp, fw
}

// awaitFrameAfter waits until fp has recorded at least one whole frame
// beyond offset and returns that frame's bytes: the actual bytes the frame
// writer put on the pipeline's stdin, never a claim about a colour.
func awaitFrameAfter(t *testing.T, fp *fakeProcess, offset int) []byte {
	t.Helper()
	var frame []byte
	waitFor(t, func() bool {
		b := fp.stdinSnapshot()
		if len(b) < offset+gapChannelCount {
			return false
		}
		frame = b[len(b)-gapChannelCount:]
		return true
	})
	return frame
}

// alertFrame is what [FailureOutputAlert] must put on the wire for this
// test's rgb geometry: every pixel full red.
func alertFrame() []byte {
	out := make([]byte, gapChannelCount)
	for i := range out {
		out[i] = alertPixelFill[i%rgbBytesPerPixel]
	}
	return out
}

// TestCoverageGapDrawsAlertInProgramMode is the owner ruling's first half:
// a broken assignment must not look like a healthy idle to the person
// standing in front of the surface while an operator is programming.
func TestCoverageGapDrawsAlertInProgramMode(t *testing.T) {
	sup, fp, _ := startCoverageGapWriter(t, newFakeShowMode(false))

	frame := awaitFrameAfter(t, fp, 0)
	if want := alertFrame(); !bytes.Equal(frame, want) {
		t.Fatalf("frame written in Program Mode = %v, want the alert fill %v", frame, want)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot for surface-1")
	}
	if snap.Drawing != DrawingFailure {
		t.Fatalf("Drawing = %q, want %q", snap.Drawing, DrawingFailure)
	}
	if snap.FailureOutput != FailureOutputAlert {
		t.Fatalf("FailureOutput = %q, want %q", snap.FailureOutput, FailureOutputAlert)
	}
	if snap.IdleMode != "" {
		t.Fatalf("IdleMode = %q during an extraction failure, want empty: a failure is not an idle mode", snap.IdleMode)
	}
}

// TestCoverageGapDrawsBlackInShowMode is the ruling's second half: red in
// front of an audience is worse than black, and the evidence must still
// say this is a failure even though the wall now looks exactly like a
// healthy idle.
func TestCoverageGapDrawsBlackInShowMode(t *testing.T) {
	sup, fp, _ := startCoverageGapWriter(t, newFakeShowMode(true))

	frame := awaitFrameAfter(t, fp, 0)
	if want := make([]byte, gapChannelCount); !bytes.Equal(frame, want) {
		t.Fatalf("frame written in Show Mode = %v, want black %v", frame, want)
	}

	snap, ok := sup.Snapshot("surface-1")
	if !ok {
		t.Fatalf("no snapshot for surface-1")
	}
	if snap.Drawing != DrawingFailure {
		t.Fatalf("Drawing = %q, want %q: the wall drawing black must not make the report say idle", snap.Drawing, DrawingFailure)
	}
	if snap.FailureOutput != FailureOutputBlack {
		t.Fatalf("FailureOutput = %q, want %q", snap.FailureOutput, FailureOutputBlack)
	}
}

// TestCoverageGapDrawsBlackWhenModeNeverReceived proves ADR-033 decision
// 5's default on the path where getting it wrong means red in front of an
// audience: a node nobody has told the mode reads unknown, and unknown
// behaves as show.
func TestCoverageGapDrawsBlackWhenModeNeverReceived(t *testing.T) {
	sup, fp, _ := startCoverageGapWriter(t, nil)

	frame := awaitFrameAfter(t, fp, 0)
	if want := make([]byte, gapChannelCount); !bytes.Equal(frame, want) {
		t.Fatalf("frame written with no mode ever received = %v, want black %v", frame, want)
	}

	snap, _ := sup.Snapshot("surface-1")
	if snap.FailureOutput != FailureOutputBlack {
		t.Fatalf("FailureOutput = %q with no mode ever received, want %q", snap.FailureOutput, FailureOutputBlack)
	}
}

// TestModeFlipChangesLiveSurfaceOutput is ADR-036 decision 1's own test:
// one writer, never rebuilt, changes what it puts on the wire because the
// mode changed under it. A writer that captured the mode at construction
// passes every other test in this file and fails this one.
func TestModeFlipChangesLiveSurfaceOutput(t *testing.T) {
	mode := newFakeShowMode(true)
	sup, fp, fw := startCoverageGapWriter(t, mode)

	if frame := awaitFrameAfter(t, fp, 0); !bytes.Equal(frame, make([]byte, gapChannelCount)) {
		t.Fatalf("frame before the flip = %v, want black", frame)
	}
	generationBefore := sup.Generation("surface-1")
	writtenBefore := len(fp.stdinSnapshot())

	mode.show.Store(false) // Program Mode, with the writer already running

	if frame := awaitFrameAfter(t, fp, writtenBefore); !bytes.Equal(frame, alertFrame()) {
		t.Fatalf("frame after the flip = %v, want the alert fill %v", frame, alertFrame())
	}
	if got := sup.Generation("surface-1"); got != generationBefore {
		t.Fatalf("pipeline generation moved from %d to %d across a mode flip; the flip must not restart anything", generationBefore, got)
	}

	// Back again, so this proves a live read rather than a one-way latch.
	writtenBefore = len(fp.stdinSnapshot())
	mode.show.Store(true)
	if frame := awaitFrameAfter(t, fp, writtenBefore); !bytes.Equal(frame, make([]byte, gapChannelCount)) {
		t.Fatalf("frame after flipping back = %v, want black", frame)
	}
	if _, _, dropped := fw.Counts(); dropped == 0 {
		t.Fatalf("dropped = 0 across a run of failed extractions, want > 0")
	}
}

// TestFailureEvidenceIsDistinctFromHealthyIdleInBothModes is the evidence
// half of the ruling. The failure and a healthy operator-chosen black idle
// draw identical bytes in Show Mode, so the report is the only place they
// can still be told apart, and it has to do that in both modes.
func TestFailureEvidenceIsDistinctFromHealthyIdleInBothModes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		behavesAsShow bool
		failureOutput string
	}{
		{"show mode", true, FailureOutputBlack},
		{"program mode", false, FailureOutputAlert},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const surfaceID = "surface-1"
			sup, _ := newTestFrameWriterSupervisor(t, surfaceID)

			source := &fakeFrameSource{frameCount: 1000, stepTimeMS: gapSurfaceStepsMS, uncoveredFrom: gapUncoveredFrom}
			tl := &fakeTimelineSource{}
			tl.set(multisync.StateStopped, gapTimelinePosMS) // healthy, operator-chosen idle

			fw, err := NewFrameWriter(sup, surfaceID, source, tl, 0, gapChannelCount, surfaceWidth, 1,
				IdleOutputBlack, newFakeShowMode(tc.behavesAsShow), testLogger{})
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
				return ok && snap.Drawing == DrawingIdle
			})
			idle, _ := sup.Snapshot(surfaceID)
			if idle.IdleMode != IdleOutputBlack || idle.FailureOutput != "" {
				t.Fatalf("healthy idle reported idleMode=%q failureOutput=%q, want %q and empty",
					idle.IdleMode, idle.FailureOutput, IdleOutputBlack)
			}

			tl.set(multisync.StatePlaying, gapTimelinePosMS) // now the assignment is broken
			waitFor(t, func() bool {
				snap, ok := sup.Snapshot(surfaceID)
				return ok && snap.Drawing == DrawingFailure
			})
			failure, _ := sup.Snapshot(surfaceID)
			if failure.Drawing == idle.Drawing {
				t.Fatalf("a coverage gap and a healthy idle both report drawing=%q; nothing in the report tells them apart", failure.Drawing)
			}
			if failure.FailureOutput != tc.failureOutput {
				t.Fatalf("FailureOutput = %q, want %q", failure.FailureOutput, tc.failureOutput)
			}
			if failure.IdleMode != "" {
				t.Fatalf("IdleMode = %q during a failure, want empty", failure.IdleMode)
			}
		})
	}
}

// TestFillAlertPixelLayouts covers the three layouts the alert fill has to
// handle: rgb, rgbw (whose white element must stay off, or the surface
// shows washed-out pink rather than red), and a channel count that is not
// a whole number of pixels, where red has no expressible layout at all.
func TestFillAlertPixelLayouts(t *testing.T) {
	rgb := make([]byte, 7) // deliberately a partial trailing pixel
	fillAlert(rgb, 3)
	if want := []byte{0xFF, 0, 0, 0xFF, 0, 0, 0xFF}; !bytes.Equal(rgb, want) {
		t.Errorf("fillAlert(rgb) = %v, want %v", rgb, want)
	}

	rgbw := make([]byte, 8)
	fillAlert(rgbw, 4)
	if want := []byte{0xFF, 0, 0, 0, 0xFF, 0, 0, 0}; !bytes.Equal(rgbw, want) {
		t.Errorf("fillAlert(rgbw) = %v, want %v (the white element stays off)", rgbw, want)
	}

	unknown := make([]byte, 5)
	fillAlert(unknown, 1)
	for i, b := range unknown {
		if b != alertUnknownStrideFill {
			t.Errorf("fillAlert(unknown stride)[%d] = %#x, want %#x", i, b, alertUnknownStrideFill)
		}
	}
	if bytes.Equal(unknown, make([]byte, len(unknown))) {
		t.Errorf("fillAlert(unknown stride) produced black, which is exactly what a healthy idle draws")
	}
}

// TestNewFrameWriterRefusesEmptySource proves the constructor no longer
// waves through a source with no frames. That skipped probe is how a
// broken assignment used to start a writer that could only ever fail.
func TestNewFrameWriterRefusesEmptySource(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 0, stepTimeMS: 25, uncoveredFrom: -1}
	if _, err := NewFrameWriter(sup, surfaceID, source, &fakeTimelineSource{}, 0, 8, 8, 1, IdleOutputBlack, nil, testLogger{}); err == nil {
		t.Fatalf("NewFrameWriter over a source with no frames: want an error, got nil")
	}
}

// TestNewFrameWriterReportsCoercedIdleOutput proves the constructor's
// defense-in-depth coercion of an unrecognized idle output is logged. It
// decides what this surface draws for the writer's whole life, and it used
// to happen with no log and no evidence at all.
func TestNewFrameWriterReportsCoercedIdleOutput(t *testing.T) {
	const surfaceID = "surface-1"
	sup, _ := newTestFrameWriterSupervisor(t, surfaceID)
	source := &fakeFrameSource{frameCount: 10, stepTimeMS: 25, uncoveredFrom: -1}

	logger := &recordingLogger{}
	if _, err := NewFrameWriter(sup, surfaceID, source, &fakeTimelineSource{}, 0, 8, 8, 1, "strobe", nil, logger); err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	if logger.warnCount() != 1 {
		t.Fatalf("warnings logged for an unrecognized idle output = %d, want 1", logger.warnCount())
	}

	quiet := &recordingLogger{}
	if _, err := NewFrameWriter(sup, surfaceID, source, &fakeTimelineSource{}, 0, 8, 8, 1, IdleOutputBlack, nil, quiet); err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	if quiet.warnCount() != 0 {
		t.Fatalf("warnings logged for an explicit black idle output = %d, want 0", quiet.warnCount())
	}
}
