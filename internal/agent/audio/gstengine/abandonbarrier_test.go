//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// barrierTotals counts what the between-test barrier met over the whole
// run; TestMain prints them so a gate log shows whether anything carried over.
var barrierTotals struct {
	engines         atomic.Int64 // engines the barrier settled
	stillRunning    atomic.Int64 // branches with a state change still running at test end
	closeIncomplete atomic.Int64 // engines Close could not finish, here or in the test
	leftBehind      atomic.Int64 // engines the barrier could not bring to NULL at all

	mu                  sync.Mutex
	closeIncompleteTest []string
	leftBehindTest      []string
}

func recordBarrierName(list *[]string, name string) {
	barrierTotals.mu.Lock()
	defer barrierTotals.mu.Unlock()
	*list = append(*list, name)
}

func barrierNames(list *[]string) string {
	barrierTotals.mu.Lock()
	defer barrierTotals.mu.Unlock()
	return strings.Join(*list, ", ")
}

func TestMain(m *testing.M) {
	code := m.Run()
	fmt.Fprintf(os.Stderr,
		"gstengine test barrier: settled %d engines; %d branch state changes were still running at test end, %d engines needed more than Close, %d engines were left live, %d goroutines are still inside gst_element_set_state; close incomplete in: [%s]; left live in: [%s]\n",
		barrierTotals.engines.Load(), barrierTotals.stillRunning.Load(),
		barrierTotals.closeIncomplete.Load(), barrierTotals.leftBehind.Load(), goroutinesInSetState(),
		barrierNames(&barrierTotals.closeIncompleteTest), barrierNames(&barrierTotals.leftBehindTest))
	os.Exit(code)
}

// holdStateChangePending stands in for a state change abandoned to a
// timed-out call, and withdraws it when the test ends so the barrier
// can tell it from one GStreamer is really still running.
func holdStateChangePending(t *testing.T, b *branch) {
	t.Helper()
	b.pendingStateChanges.Add(1)
	t.Cleanup(func() { b.pendingStateChanges.Add(-1) })
}

// goroutinesInSetState counts goroutines currently inside GStreamer's
// own gst_element_set_state, the call an abandoned state change never
// returns from.
func goroutinesInSetState() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "_Cfunc_gst_element_set_state(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// branchesStillInPipeline lists every branch whose elements e's pipeline
// may still hold, whichever of the engine's own sets it is reachable from.
func branchesStillInPipeline(e *Engine) []*branch {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := make(map[*branch]struct{})
	for _, b := range e.handles {
		seen[b] = struct{}{}
	}
	for b := range e.inFlightReplacements {
		seen[b] = struct{}{}
	}
	for b := range e.retiringBranches {
		seen[b] = struct{}{}
	}
	for _, b := range e.elementIndex {
		seen[b] = struct{}{}
	}
	out := make([]*branch, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	return out
}

// flushDownstream starts a flush at b's downstream end: its mixer pads
// and its queue. A streaming thread parked there holds the stream lock a
// NULL transition on the branch's upstream elements is waiting for.
func flushDownstream(b *branch) {
	for _, pad := range b.mixerPads() {
		if pad != nil {
			pad.SendEvent(gst.NewEventFlushStart())
		}
	}
	b.unblockFlow()
	b.queue.GetStaticPad("src").PushEvent(gst.NewEventFlushStart())
}

// awaitPendingDrained waits up to d for every branch in bs to have no
// state change running, and returns the ones that still do.
func awaitPendingDrained(bs []*branch, d time.Duration) []*branch {
	deadline := time.Now().Add(d)
	for {
		var busy []*branch
		for _, b := range bs {
			if b.pendingStateChanges.Load() != 0 {
				busy = append(busy, b)
			}
		}
		if len(busy) == 0 || time.Now().After(deadline) {
			return busy
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settleTestEngine is the between-test barrier: it returns once e holds
// no live pipeline and no running state change, or has logged that it
// could not get there. Close waits for a teardown in flight, never races it.
func settleTestEngine(t *testing.T, e *Engine) {
	t.Helper()
	if testEnginePipeline(e) == nil {
		return // already released by the test's own Close
	}
	barrierTotals.engines.Add(1)

	branches := branchesStillInPipeline(e)
	if busy := awaitPendingDrained(branches, 0); len(busy) != 0 {
		barrierTotals.stillRunning.Add(int64(len(busy)))
		t.Logf("test barrier: %d branch state changes were still running at test end", len(busy))
	}

	closeErr := e.Close()
	pipeline := testEnginePipeline(e)
	if pipeline == nil {
		return
	}

	// Close ran out of time, here or in the test. Whatever is parked
	// behind a branch's own streaming thread is flushed loose first.
	barrierTotals.closeIncomplete.Add(1)
	recordBarrierName(&barrierTotals.closeIncompleteTest, t.Name())
	t.Logf("test barrier: Close did not finish the pipeline (error: %v)", closeErr)
	for _, b := range branches {
		flushDownstream(b)
	}
	done := make(chan struct{})
	go func() {
		pipeline.SetState(gst.StateNull)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(testCleanupTimeout):
		barrierTotals.leftBehind.Add(1)
		recordBarrierName(&barrierTotals.leftBehindTest, t.Name())
		t.Logf("test barrier: test engine cleanup abandoned the pipeline's NULL transition; its pipeline stays live for the rest of this run")
		return
	}
	if busy := awaitPendingDrained(branches, testCleanupTimeout); len(busy) != 0 {
		barrierTotals.leftBehind.Add(1)
		recordBarrierName(&barrierTotals.leftBehindTest, t.Name())
		t.Logf("test barrier: %d branch state changes never returned after the pipeline reached NULL", len(busy))
	}
}

func testEnginePipeline(e *Engine) gst.Pipeline {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pipeline
}

// TestAbandonedStateChangeDoesNotOutliveItsTest proves the barrier on
// the case it exists for: a NULL transition GStreamer is really still
// inside when the test body ends. The branch's flow is blocked and never
// released, so the transition parks behind its own streaming thread.
func TestAbandonedStateChangeDoesNotOutliveItsTest(t *testing.T) {
	gst.Init()
	wav := filepath.Join(t.TempDir(), "fixture.wav")
	generateWAV(t, wav, 6)

	var abandoned *Engine
	var parked bool
	flushedBefore := barrierTotals.stillRunning.Load()
	// Whatever an earlier test left stuck stays stuck; this test answers
	// only for what it adds.
	stuckBefore := goroutinesInSetState()
	t.Run("abandons a NULL transition", func(t *testing.T) {
		e := newTestEngine(t)
		abandoned = e
		ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
		defer cancel()
		const handle = "abandoned1"
		if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, err := e.Start(ctx, handle, 0); err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitForPosition(t, e, handle, 50*time.Millisecond, 5*time.Second)
		b, err := e.branchFor(handle)
		if err != nil {
			t.Fatalf("branchFor: %v", err)
		}

		b.blockFlow()
		time.Sleep(200 * time.Millisecond)
		shortCtx, shortCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer shortCancel()
		if err := b.setElementsState(shortCtx, gst.StateNull); !errors.Is(err, context.DeadlineExceeded) {
			t.Skipf("the NULL transition returned %v instead of parking on this environment; nothing was abandoned to prove the barrier on", err)
		}
		if b.pendingStateChanges.Load() == 0 {
			t.Skip("the NULL transition finished right after its deadline on this environment; nothing was abandoned to prove the barrier on")
		}
		if goroutinesInSetState() == stuckBefore {
			t.Fatalf("stack probe found no goroutine in gst_element_set_state; the symbol match is stale")
		}
		parked = true
	})
	if !parked {
		t.Skip("no state change was abandoned in this environment")
	}

	if got := barrierTotals.stillRunning.Load() - flushedBefore; got != 1 {
		t.Fatalf("the barrier counted %d state changes still running at test end, want the 1 this test abandoned", got)
	}
	if n := goroutinesInSetState(); n != stuckBefore {
		t.Fatalf("%d goroutines are inside gst_element_set_state after the abandoning test ended, want the %d there were before it", n, stuckBefore)
	}
	if testEnginePipeline(abandoned) != nil {
		t.Fatalf("the abandoning test's pipeline is still held after its cleanup, want it released")
	}

	t.Run("the next test builds and tears down cleanly", func(t *testing.T) {
		e := newTestEngine(t)
		ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
		defer cancel()
		const handle = "next1"
		if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, err := e.Start(ctx, handle, 0); err != nil {
			t.Fatalf("Start: %v", err)
		}
		waitForPosition(t, e, handle, 50*time.Millisecond, 5*time.Second)
		if err := e.Release(ctx, handle); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if err := e.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	if n := goroutinesInSetState(); n != stuckBefore {
		t.Fatalf("%d goroutines are inside gst_element_set_state after the next test, want the %d there were before", n, stuckBefore)
	}
}
