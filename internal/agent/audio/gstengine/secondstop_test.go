//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// stopBudget is the bound the agent puts on a stop's final engine sweep.
const stopBudget = 10 * time.Second

// secondStopAfterStuckCleanups leaves stuckCount branches behind a first
// stop, each handed to a cleanup that can only wait, then loads a healthy
// branch and stops again one second later. It returns what that second
// stop released, how long it took and its error.
func secondStopAfterStuckCleanups(t *testing.T, stuckCount int) ([]agentaudio.EngineHandle, time.Duration, error) {
	t.Helper()
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	handles := make([]agentaudio.EngineHandle, stuckCount)
	for i := range handles {
		handles[i] = agentaudio.EngineHandle(fmt.Sprintf("stuck%d", i+1))
	}
	stuck := loadPlaying(t, e, ctx, handles...)
	for _, b := range stuck {
		b.pendingStateChanges.Add(1)
	}

	firstCtx, firstCancel := context.WithTimeout(ctx, stopBudget)
	released, err := e.ReleaseAll(firstCtx)
	firstCancel()
	if err == nil || len(released) != 0 {
		t.Fatalf("first stop = %v, %v; want every stuck branch left behind and reported", released, err)
	}
	firstReturned := time.Now()

	loadPlaying(t, e, ctx, "healthy")
	time.Sleep(time.Until(firstReturned.Add(time.Second)))
	if late := time.Since(firstReturned); late > teardownTimeout-time.Second {
		t.Fatalf("the second stop could only start %s after the first; the cleanups are no longer waiting, so this run proves nothing", late)
	}

	secondCtx, secondCancel := context.WithTimeout(ctx, stopBudget)
	began := time.Now()
	released, err = e.ReleaseAll(secondCtx)
	elapsed := time.Since(began)
	secondCancel()

	bin := e.pipeline.(gst.Bin)
	for _, b := range stuck {
		if !branchMuted(b) {
			t.Errorf("stuck branch %d is not muted after the second stop", b.id)
		}
		if !isParked(e, b) {
			t.Errorf("stuck branch %d is no longer parked while its state change is still pending", b.id)
		}
		if bin.GetByName(b.filesrcName) == nil {
			t.Errorf("stuck branch %d lost its elements while its state change was still pending", b.id)
		}
	}

	// Once the old state changes settle, the cleanups still in their
	// settle wait finish every branch with no further call.
	for _, b := range stuck {
		b.pendingStateChanges.Add(-1)
	}
	if left := waitForEmptyElementIndex(e, teardownTimeout); left != 0 {
		t.Errorf("%d element(s) still attached after the stuck state changes settled, want 0", left)
	}
	return released, elapsed, err
}

func TestASecondStopIsNotHeldUpByOneWaitingCleanup(t *testing.T) {
	released, elapsed, err := secondStopAfterStuckCleanups(t, 1)
	t.Logf("second stop with one cleanup waiting: released %v in %s, err = %v", released, elapsed, err)
	if err != nil {
		t.Fatalf("second stop reported %v, want it confirmed", err)
	}
	if len(released) != 1 || released[0] != "healthy" {
		t.Fatalf("second stop released %v, want the healthy branch", released)
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("second stop took %s, want well under a second", elapsed)
	}
}

func TestASecondStopIsNotHeldUpByTwoWaitingCleanups(t *testing.T) {
	released, elapsed, err := secondStopAfterStuckCleanups(t, 2)
	t.Logf("second stop with two cleanups waiting: released %v in %s, err = %v", released, elapsed, err)
	if err != nil {
		t.Fatalf("second stop reported %v, want it confirmed", err)
	}
	if len(released) != 1 || released[0] != "healthy" {
		t.Fatalf("second stop released %v, want the healthy branch", released)
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("second stop took %s, want well under a second", elapsed)
	}
}

// TestTeardownLeavesElementsAloneWhenAStateChangeBeginsWhileItQueues
// proves the settle check is repeated once the turn is in hand: a state
// change that began while this teardown queued sends it back to wait.
func TestTeardownLeavesElementsAloneWhenAStateChangeBeginsWhileItQueues(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	b := loadPlaying(t, e, ctx, "queued")[0]

	e.teardownTurn <- struct{}{}
	done := make(chan error, 1)
	go func() { done <- e.Release(ctx, "queued") }()
	time.Sleep(100 * time.Millisecond)
	b.pendingStateChanges.Add(1)
	<-e.teardownTurn

	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("Release returned %v while a state change was still pending on its branch", err)
	default:
	}
	if e.pipeline.(gst.Bin).GetByName(b.filesrcName) == nil {
		t.Fatal("teardown removed the branch's elements while a state change was still pending on it")
	}
	select {
	case e.teardownTurn <- struct{}{}:
		<-e.teardownTurn
	default:
		t.Fatal("a teardown that went back to waiting kept the turn")
	}

	b.pendingStateChanges.Add(-1)
	if err := <-done; err != nil {
		t.Fatalf("Release after the state change settled: %v", err)
	}
}

// countBuffersLeavingQueue counts every buffer b sends on toward the
// mixers from here on.
func countBuffersLeavingQueue(b *branch) *atomic.Int64 {
	var n atomic.Int64
	b.queue.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(gst.Pad, *gst.PadProbeInfo) gst.PadProbeReturn {
		n.Add(1)
		return gst.PadProbeOK
	})
	return &n
}

// TestAPausedBranchStaysSilentWhileItsReleaseWaitsForTheTurn proves a
// paused branch, silent only because of its flow block, sends nothing
// downstream while its teardown queues behind another one.
func TestAPausedBranchStaysSilentWhileItsReleaseWaitsForTheTurn(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	b := loadPlaying(t, e, ctx, "paused")[0]
	if _, err := e.Pause(ctx, "paused"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	passed := countBuffersLeavingQueue(b)

	e.teardownTurn <- struct{}{}
	done := make(chan error, 1)
	go func() {
		relCtx, relCancel := context.WithTimeout(ctx, 3*time.Second)
		defer relCancel()
		done <- e.Release(relCtx, "paused")
	}()
	time.Sleep(time.Second)
	if n := passed.Load(); n != 0 {
		t.Errorf("the paused branch sent %d buffer(s) downstream while its Release waited for the turn, want 0", n)
	}
	<-e.teardownTurn
	if err := <-done; err != nil {
		t.Fatalf("Release once the turn was free: %v", err)
	}
}

// TestAPausedBranchStaysSilentThroughClose proves the same of Close,
// which tears every branch down at once so all but one must queue.
func TestAPausedBranchStaysSilentThroughClose(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	b := loadPlaying(t, e, ctx, "paused", "playing1", "playing2", "playing3", "playing4")[0]
	if _, err := e.Pause(ctx, "paused"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	passed := countBuffersLeavingQueue(b)

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Its own teardown releases the block a moment before the state
	// change, so a buffer or two is expected; a queued wait's worth is not.
	t.Logf("the paused branch sent %d buffer(s) downstream during Close", passed.Load())
	if n := passed.Load(); n > 8 {
		t.Fatalf("the paused branch sent %d buffers downstream during Close, want no more than its own teardown lets through", n)
	}
}
