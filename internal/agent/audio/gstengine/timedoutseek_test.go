//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestTimedOutSeekLeavesSegmentStartStale reproduces the defect against a
// real pipeline before any fix: seekTo issues a real, blocking seek on a
// goroutine of its own, then hands boundedCall's ctx a deadline so tight
// it always fires first. The seek keeps running in that abandoned
// goroutine and, given time, lands for real — this test waits for that to
// happen and then shows segmentStart was never moved to match it, so
// localRunningTime is now off by the whole missed seek delta.
func TestTimedOutSeekLeavesSegmentStartStale(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "seekstale1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	const target = 4 * time.Second
	tctx, tcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer tcancel()
	if _, seekErr := e.Seek(tctx, handle, target); seekErr == nil {
		t.Fatalf("Seek with an exhausted deadline: err = nil, want a timeout")
	} else if !errors.Is(seekErr, context.DeadlineExceeded) {
		t.Fatalf("Seek with an exhausted deadline: err = %v, want context.DeadlineExceeded in its chain", seekErr)
	}

	// Sample across a settle window: the abandoned goroutine's seek is
	// real GStreamer work and needs time to land, and once it lands the
	// position must keep tracking real time from there rather than
	// bouncing back — a single instantaneous read would not tell the two
	// apart.
	const settleWait = 5 * time.Second
	deadline := time.Now().Add(settleWait)
	var landed bool
	var lastPos time.Duration
	for time.Now().Before(deadline) {
		lastPos = b.queryPosition()
		if lastPos >= target-200*time.Millisecond {
			landed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !landed {
		t.Skipf("abandoned seek never landed within %s (last observed position %s); cannot reproduce the mis-anchoring on this environment", settleWait, lastPos)
	}
	// Confirm the landed seek is sustained, not a transient overshoot.
	time.Sleep(300 * time.Millisecond)
	if pos := b.queryPosition(); pos < target-200*time.Millisecond {
		t.Fatalf("position fell back below the seek target after landing: %s", pos)
	}

	b.mu.Lock()
	stale := b.segmentStart
	b.mu.Unlock()
	if stale >= target-200*time.Millisecond {
		t.Fatalf("segmentStart = %s already tracks the landed seek to %s; this reproduction no longer demonstrates staleness", stale, target)
	}

	// The consequence: localRunningTime is inflated by the whole missed
	// seek delta, which is exactly what a later mixer resync or fade
	// anchor would use to place this branch's buffers in the aggregator's
	// past.
	local := b.localRunningTime(b.queryPosition())
	minExpected := target - stale - 500*time.Millisecond
	if local < minExpected {
		t.Fatalf("localRunningTime = %s, want at least %s (inflated by the missed seek delta from stale segmentStart=%s)", local, minExpected, stale)
	}

	_ = e.Release(context.Background(), handle)
}
