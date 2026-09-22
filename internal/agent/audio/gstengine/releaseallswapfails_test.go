//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestReleaseAllFinishesWhenAClaimedSwapNeverReachesCommit is the
// acceptance test for a stop that claimed an in-flight swap which then
// failed before it could commit. Only commitSwap ever answered the claim,
// so every early return from swapToPosition left the stop waiting on a
// handoff nothing would ever send: the whole stop burned its caller's
// budget and tore nothing down, while the branches it had already taken
// out of e.handles became unreachable by Close, Release, or a retried
// stop.
//
// Overwriting the fixture with undecodable bytes between Pause and
// Resume is what forces that failure through the public API alone: the
// replacement builds, then fails during its own PAUSED transition. The
// delays stagger the stop across that window; the assertions hold on
// every one of them, since a stop landing clearly before or clearly
// after the swap is the ordinary case.
func TestReleaseAllFinishesWhenAClaimedSwapNeverReachesCommit(t *testing.T) {
	gst.Init()

	dir := t.TempDir()
	delays := []time.Duration{
		0, 500 * time.Microsecond, 1 * time.Millisecond, 2 * time.Millisecond,
		3 * time.Millisecond, 5 * time.Millisecond, 8 * time.Millisecond, 12 * time.Millisecond,
	}
	for i, delay := range delays {
		t.Run(fmt.Sprintf("delay=%s", delay), func(t *testing.T) {
			e := newTestEngine(t)
			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			wav := filepath.Join(dir, fmt.Sprintf("swapfails-%d.wav", i))
			generateWAV(t, wav, 6)

			handle := agentaudio.EngineHandle(fmt.Sprintf("swapfails-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 6*time.Second); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := e.Start(ctx, handle, 0); err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitForPosition(t, e, string(handle), 100*time.Millisecond, 5*time.Second)
			if _, err := e.Pause(ctx, handle); err != nil {
				t.Fatalf("Pause: %v", err)
			}

			// The replacement Resume builds reads this path, so it is the
			// one that fails; the branch already playing is untouched.
			writeGarbage(t, wav)

			resumeDone := make(chan struct{})
			go func() {
				defer close(resumeDone)
				_, _ = e.Resume(ctx, handle)
			}()
			if delay > 0 {
				time.Sleep(delay)
			}

			began := time.Now()
			released, err := e.ReleaseAll(ctx)
			elapsed := time.Since(began)
			if err != nil {
				t.Fatalf("ReleaseAll: %v (after %s)", err, elapsed)
			}
			if elapsed > 5*time.Second {
				t.Fatalf("ReleaseAll took %s, want well inside its caller's budget: a claimed swap that failed before commit must not hold the stop", elapsed)
			}
			if len(released) != 1 || released[0] != handle {
				t.Fatalf("ReleaseAll released %v, want exactly [%s]", released, handle)
			}
			<-resumeDone

			live, err := e.LiveHandles(context.Background())
			if err != nil {
				t.Fatalf("LiveHandles: %v", err)
			}
			if len(live) != 0 {
				t.Fatalf("LiveHandles = %v, want empty", live)
			}
			if left := waitForEmptyElementIndex(e, 5*time.Second); left != 0 {
				t.Fatalf("%d element(s) still attached to the pipeline after ReleaseAll, want 0: "+
					"a branch the stop took out of e.handles but could not tear down was left unreachable", left)
			}
		})
	}
}

// waitForEmptyElementIndex polls until the engine's element index is
// empty, and reports how many entries were left when it gave up: a
// branch still indexed still has its elements attached to the pipeline.
func waitForEmptyElementIndex(e *Engine, within time.Duration) int {
	deadline := time.Now().Add(within)
	for {
		e.mu.Lock()
		n := len(e.elementIndex)
		e.mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}
