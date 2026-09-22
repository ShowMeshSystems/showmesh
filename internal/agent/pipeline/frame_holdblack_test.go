package pipeline

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// fakeHoldBlack is a [HoldBlackSource] test double whose answer a test
// flips between ticks, mirroring fakeShowMode's identical shape one file
// over: the writer must read it at the point of decision, on the tick it
// applies to, never once at construction.
type fakeHoldBlack struct {
	held atomic.Bool
}

func (f *fakeHoldBlack) HoldBlack() bool { return f.held.Load() }

func newFakeHoldBlack(held bool) *fakeHoldBlack {
	h := &fakeHoldBlack{}
	h.held.Store(held)
	return h
}

// TestFrameWriterHoldBlackOverridesHoldIdleOutput proves build item 1: a
// held-black surface draws literal black even when its configured idle
// output is Hold and a real content frame is already sitting in fw.buf —
// the one idle mode that would otherwise draw something other than black.
func TestFrameWriterHoldBlackOverridesHoldIdleOutput(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 5, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 50) // 50ms / 5ms = frame 10 -> byte value 11
	holdBlack := newFakeHoldBlack(false)

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputHold, nil, holdBlack, testLogger{})
	if err != nil {
		t.Fatalf("NewFrameWriter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fw.Run(ctx)
	t.Cleanup(func() {
		cancel()
		fw.Stop()
	})

	// Wait for a real content frame to land in fw.buf before blacking out.
	waitFor(t, func() bool {
		written, _, _ := fw.Counts()
		return written >= 1
	})
	for _, b := range fp.stdinSnapshot() {
		if b != 11 {
			t.Fatalf("stdin byte = %d while Playing frame 10, want 11 (frame%%250+1)", b)
		}
	}

	// Hold black while the timeline still reports Playing (content is still
	// nominally available) and idleOutput is still Hold (fw.buf still holds
	// the last real frame): forced black must outrank both.
	lenBeforeBlackout := len(fp.stdinSnapshot())
	requestsBeforeBlackout := len(source.requests)
	holdBlack.held.Store(true)

	waitFor(t, func() bool {
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingBlackout
	})
	snap, ok := sup.Snapshot(surfaceID)
	if !ok {
		t.Fatalf("no snapshot for %q", surfaceID)
	}
	if snap.Drawing != DrawingBlackout {
		t.Fatalf("Drawing = %q while held black, want %q", snap.Drawing, DrawingBlackout)
	}
	if snap.IdleMode != "" {
		t.Fatalf("IdleMode = %q while held black, want empty (forced black is not the surface's configured idle output)", snap.IdleMode)
	}
	if snap.FailureOutput != "" {
		t.Fatalf("FailureOutput = %q while held black, want empty", snap.FailureOutput)
	}

	waitFor(t, func() bool {
		snap := fp.stdinSnapshot()
		return len(snap) > lenBeforeBlackout && allBytesEqual(snap[lenBeforeBlackout:], 0)
	})

	source.mu.Lock()
	gotRequests := len(source.requests)
	source.mu.Unlock()
	if gotRequests != requestsBeforeBlackout {
		t.Fatalf("ChannelRange was called %d time(s) while held black; a forced blackout must never touch the FSEQ source", gotRequests-requestsBeforeBlackout)
	}

	// Clear the flag: the SAME writer (no restart, no Stop/re-construct)
	// must resume drawing content on its very next tick.
	lenBeforeResume := len(fp.stdinSnapshot())
	holdBlack.held.Store(false)
	waitFor(t, func() bool {
		snap, ok := sup.Snapshot(surfaceID)
		return ok && snap.Drawing == DrawingContent
	})
	waitFor(t, func() bool {
		snap := fp.stdinSnapshot()
		return len(snap) > lenBeforeResume && allBytesEqual(snap[lenBeforeResume:], 11)
	})
}

// TestFrameWriterHoldBlackIgnoresNilSource proves a writer built with a nil
// HoldBlackSource (every existing caller that never blacks out) behaves
// exactly as before: content plays normally, matching this package's own
// nil-tolerant convention for [ShowModeSource].
func TestFrameWriterHoldBlackIgnoresNilSource(t *testing.T) {
	const surfaceID = "surface-1"
	sup, fp := newTestFrameWriterSupervisor(t, surfaceID)

	source := &fakeFrameSource{frameCount: 1000, stepTimeMS: 25, uncoveredFrom: -1}
	tl := &fakeTimelineSource{}
	tl.set(multisync.StatePlaying, 250) // 250ms / 25ms = frame 10

	fw, err := NewFrameWriter(sup, surfaceID, source, tl, "seq.fseq", 0, 8, 8, 1, IdleOutputBlack, nil, nil, testLogger{})
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
	for _, b := range fp.stdinSnapshot() {
		if b != 11 {
			t.Fatalf("stdin byte = %d while Playing frame 10 with a nil HoldBlackSource, want 11 (frame%%250+1)", b)
		}
	}
}
