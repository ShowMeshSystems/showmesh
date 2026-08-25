//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestStartLeavesFlowBlockedWhenItsOwnSeekTimesOut is this defect's
// acceptance test. It forces Start's own seek to abandon before
// decodebin.Seek returns, using the same already-exhausted-deadline
// technique TestTimedOutSeekRefusesFurtherAnchoring (timedoutseekfix_
// test.go) already relies on for seekTo, then asserts the paused
// branch's flow block is still in place and no buffers reached its
// queue's own src pad afterward. Under the old order -- unblockFlow
// before seekTo -- the block was already released by the time Start
// returned its timeout error, regardless of whether the seek itself
// ever lands; under the fixed order, a seek that never completes means
// unblockFlow never runs at all. This is a genuine behavioral proof of
// the ordering, not a check on source text.
func TestStartLeavesFlowBlockedWhenItsOwnSeekTimesOut(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "startblocked1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	if _, err := e.Pause(ctx, handle); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	count := countQueueSrcBuffers(t, e, handle)
	// Settle past whatever queueMaxSizeTime's worth of already-buffered
	// content still drains immediately after Pause (see pauseflow_real_
	// integration_test.go), so the baseline below is taken once flow has
	// genuinely stopped, not mid-drain.
	time.Sleep(4 * queueMaxSizeTime)
	baseline := count()

	tctx, tcancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer tcancel()
	if _, err := e.Start(tctx, handle, 2*time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start with an exhausted deadline: err = %v, want a deadline timeout", err)
	}

	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	b.mu.Lock()
	stillBlocked := b.blockProbeID != 0
	b.mu.Unlock()
	if !stillBlocked {
		t.Fatalf("Start's own seek timed out before landing, but the branch's flow block was already released: unblockFlow ran ahead of the seek it depends on")
	}

	const settle = 500 * time.Millisecond
	time.Sleep(settle)
	if got := count(); got != baseline {
		t.Fatalf("branch produced %d buffer(s) in the %s after a timed-out Start, want 0: its flow block was released before the abandoned seek could land", got-baseline, settle)
	}

	_ = e.Release(context.Background(), handle)
}

// TestSeekOnPausedBranchNeverTouchesTheFlowBlock establishes why this
// package's own Start, not Seek, is the only call site the ordering
// hazard above reaches: Seek's implementation (methods.go) never calls
// unblockFlow or blockFlow at all, so a paused branch stays paused
// across a Seek regardless of call order. This is a direct runtime
// check of that, not an inference from reading the source.
func TestSeekOnPausedBranchNeverTouchesTheFlowBlock(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = "seeknoblock1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, handle, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, handle, 200*time.Millisecond, 5*time.Second)

	if _, err := e.Pause(ctx, handle); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	count := countQueueSrcBuffers(t, e, handle)
	time.Sleep(4 * queueMaxSizeTime)
	baseline := count()

	if _, err := e.Seek(ctx, handle, 3*time.Second); err != nil {
		t.Fatalf("Seek on a paused branch: %v", err)
	}

	b, err := e.branchFor(agentaudio.EngineHandle(handle))
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}
	b.mu.Lock()
	stillBlocked := b.blockProbeID != 0
	b.mu.Unlock()
	if !stillBlocked {
		t.Fatalf("Seek on a paused branch released its flow block: Seek must never touch blockFlow/unblockFlow")
	}

	const settle = 500 * time.Millisecond
	time.Sleep(settle)
	if got := count(); got != baseline {
		t.Fatalf("branch produced %d buffer(s) in the %s after Seek on a paused branch, want 0: Seek must not resume flow", got-baseline, settle)
	}

	_ = e.Release(context.Background(), handle)
}
