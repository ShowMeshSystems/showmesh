//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// loadPlaying loads and starts one branch per handle from its own
// fixture and returns them in the order named.
func loadPlaying(t *testing.T, e *Engine, ctx context.Context, handles ...agentaudio.EngineHandle) []*branch {
	t.Helper()
	dir := t.TempDir()
	out := make([]*branch, 0, len(handles))
	for _, h := range handles {
		wav := filepath.Join(dir, string(h)+".wav")
		generateWAV(t, wav, 6)
		if _, err := e.Load(ctx, h, mediaRef(wav), 6*time.Second); err != nil {
			t.Fatalf("Load %s: %v", h, err)
		}
		if _, err := e.Start(ctx, h, 0); err != nil {
			t.Fatalf("Start %s: %v", h, err)
		}
		waitForPosition(t, e, string(h), 50*time.Millisecond, 5*time.Second)
		b, err := e.branchFor(h)
		if err != nil {
			t.Fatalf("branchFor %s: %v", h, err)
		}
		out = append(out, b)
	}
	return out
}

// branchMuted reports whether every one of b's mixer pads is muted.
func branchMuted(b *branch) bool {
	pads := b.mixerPads()
	if len(pads) == 0 {
		return false
	}
	for _, pad := range pads {
		if pad == nil {
			continue
		}
		if muted, _ := pad.ObjectProperty("mute").(bool); !muted {
			return false
		}
	}
	return true
}

func isParked(e *Engine, b *branch) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.retiringBranches[b]
	return ok
}

// TestReleaseAllMovesOnPastABranchThatOverrunsItsBudget proves a stop's
// bound is per branch, not per call: a branch whose teardown cannot
// finish inside its own share is left muted and parked for the reaper,
// reported in the error, and every branch behind it is still torn down.
// pendingStateChanges is raised by hand, the same way this package's
// other teardown-deferral tests force the condition, because a real
// abandoned state change cannot be scheduled on demand.
func TestReleaseAllMovesOnPastABranchThatOverrunsItsBudget(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	handles := []agentaudio.EngineHandle{"overrun1", "overrun2", "overrun3"}
	branches := loadPlaying(t, e, ctx, handles...)
	stuck := branches[1]
	stuck.pendingStateChanges.Add(1)

	began := time.Now()
	released, err := e.ReleaseAll(ctx)
	elapsed := time.Since(began)
	if err == nil {
		t.Fatal("ReleaseAll returned no error, want the overrunning branch reported")
	}
	if elapsed > 4*branchStopBudget {
		t.Fatalf("ReleaseAll took %s for three branches with one overrunning, want close to one budget of %s", elapsed, branchStopBudget)
	}
	if len(released) != 2 {
		t.Fatalf("ReleaseAll released %v, want the two branches that did not overrun", released)
	}
	for _, h := range released {
		if h == handles[1] {
			t.Fatalf("ReleaseAll reported %s released, but its teardown never finished", h)
		}
	}
	if !branchMuted(stuck) {
		t.Fatal("the overrunning branch is not muted; a stop must silence what it cannot tear down")
	}
	if !isParked(e, stuck) {
		t.Fatal("the overrunning branch was dropped rather than parked for the reaper")
	}

	// Once the condition clears, the reaper started at the end of the
	// sweep finishes the branch without any further call.
	stuck.pendingStateChanges.Add(-1)
	deadline := time.Now().Add(15 * time.Second)
	for isParked(e, stuck) {
		if time.Now().After(deadline) {
			t.Fatal("the parked branch was never reaped after its teardown could succeed again")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if left := waitForEmptyElementIndex(e, 5*time.Second); left != 0 {
		t.Fatalf("%d element(s) still attached after the reaper finished, want 0", left)
	}
}

// TestASecondReleaseAllMutesWhileTheFirstIsStillTearingDown proves the
// silence pass never waits on the sweep already in progress: the first
// call keeps the release gate for its whole teardown pass, and the
// second still mutes the handle the first was told to leave alone.
func TestASecondReleaseAllMutesWhileTheFirstIsStillTearingDown(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	handles := []agentaudio.EngineHandle{"overlap1", "overlap2", "overlap3"}
	branches := loadPlaying(t, e, ctx, handles...)
	kept := branches[2]
	// Two branches that cannot finish inside their budget give the first
	// call a teardown pass long enough to observe the second one inside.
	branches[0].pendingStateChanges.Add(1)
	branches[1].pendingStateChanges.Add(1)
	defer func() {
		branches[0].pendingStateChanges.Add(-1)
		branches[1].pendingStateChanges.Add(-1)
	}()

	firstDone := make(chan time.Duration, 1)
	firstBegan := time.Now()
	go func() {
		_, _ = e.ReleaseAll(ctx, handles[2])
		firstDone <- time.Since(firstBegan)
	}()

	// The first call is past its own silence pass once the handle it was
	// told to keep is the only one left live.
	deadline := time.Now().Add(5 * time.Second)
	for {
		live, err := e.LiveHandles(ctx)
		if err != nil {
			t.Fatalf("LiveHandles: %v", err)
		}
		if len(live) == 1 && live[0] == handles[2] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first ReleaseAll never finished its silence pass; live = %v", live)
		}
		time.Sleep(2 * time.Millisecond)
	}

	secondBegan := time.Now()
	secondDone := make(chan time.Duration, 1)
	go func() {
		_, _ = e.ReleaseAll(ctx)
		secondDone <- time.Since(secondBegan)
	}()

	var mutedAfter time.Duration
	deadline = time.Now().Add(5 * time.Second)
	for {
		if branchMuted(kept) {
			mutedAfter = time.Since(secondBegan)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second ReleaseAll never muted its target")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case d := <-firstDone:
		t.Fatalf("the first ReleaseAll already returned after %s; this run proves nothing about overlap", d)
	default:
	}
	if mutedAfter > 500*time.Millisecond {
		t.Fatalf("the second ReleaseAll took %s to mute, want it immediate: the silence pass must never wait on the sweep in progress", mutedAfter)
	}

	for i, done := range []chan time.Duration{firstDone, secondDone} {
		select {
		case d := <-done:
			if d > engineOpTimeout {
				t.Fatalf("ReleaseAll %d took %s", i+1, d)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("ReleaseAll %d never returned", i+1)
		}
	}
}

// TestOneTeardownAtATimeOnTheSharedPipeline proves every teardown caller
// passes through the engine's own turn: a second Release cannot start
// while the first is still inside its attempt, however it was reached.
func TestOneTeardownAtATimeOnTheSharedPipeline(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	previous := teardownTimeout
	teardownTimeout = 600 * time.Millisecond
	t.Cleanup(func() { teardownTimeout = previous })

	branches := loadPlaying(t, e, ctx, "turn1", "turn2")
	branches[0].pendingStateChanges.Add(1)
	defer branches[0].pendingStateChanges.Add(-1)

	holderDone := make(chan struct{})
	go func() {
		defer close(holderDone)
		_ = e.Release(ctx, "turn1")
	}()
	time.Sleep(100 * time.Millisecond)

	began := time.Now()
	if err := e.Release(ctx, "turn2"); err != nil {
		t.Fatalf("Release turn2: %v", err)
	}
	waited := time.Since(began)
	<-holderDone
	if waited < 300*time.Millisecond {
		t.Fatalf("the second Release returned after %s, want it held behind the first teardown's turn of roughly %s", waited, teardownTimeout)
	}
}

// TestReleaseAllRefusesASwapItHasAlreadyOvertaken proves a swap whose
// handle a stop has already taken is refused before it builds anything,
// rather than building a replacement the stop would only have to chase.
// swapToPosition is called directly because a caller reaching it through
// Resume or Seek is already refused a step earlier, at branchFor.
func TestReleaseAllRefusesASwapItHasAlreadyOvertaken(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const handle = agentaudio.EngineHandle("overtaken1")
	branches := loadPlaying(t, e, ctx, handle)
	b := branches[0]

	if _, err := e.ReleaseAll(ctx); err != nil {
		t.Fatalf("ReleaseAll: %v", err)
	}
	e.mu.Lock()
	indexedAfterStop := len(e.elementIndex)
	e.mu.Unlock()

	if _, err := e.swapToPosition(ctx, handle, b, time.Second, b.currentState()); !errors.Is(err, agentaudio.ErrHandleNotLoaded) {
		t.Fatalf("swapToPosition after the stop took its handle: err = %v, want ErrHandleNotLoaded", err)
	}
	e.mu.Lock()
	indexedAfterSwap := len(e.elementIndex)
	e.mu.Unlock()
	if indexedAfterSwap != indexedAfterStop {
		t.Fatalf("the refused swap built %d element(s); it must refuse before building anything", indexedAfterSwap-indexedAfterStop)
	}
}

// TestReleaseAllLeavesAnExceptedHandleMidSwapPlaying proves the weather
// delay alert survives a stop even while it is itself seeking: the swap
// on an excepted handle is neither refused nor claimed, and commits.
func TestReleaseAllLeavesAnExceptedHandleMidSwapPlaying(t *testing.T) {
	gst.Init()
	dir := t.TempDir()
	delays := []time.Duration{0, 1 * time.Millisecond, 3 * time.Millisecond, 6 * time.Millisecond}
	for i, delay := range delays {
		t.Run(fmt.Sprintf("delay=%s", delay), func(t *testing.T) {
			e := newTestEngine(t)
			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			alert := agentaudio.EngineHandle(fmt.Sprintf("alert-%d", i))
			other := agentaudio.EngineHandle(fmt.Sprintf("other-%d", i))
			for _, h := range []agentaudio.EngineHandle{alert, other} {
				wav := filepath.Join(dir, string(h)+".wav")
				generateWAV(t, wav, 6)
				if _, err := e.Load(ctx, h, mediaRef(wav), 6*time.Second); err != nil {
					t.Fatalf("Load %s: %v", h, err)
				}
				if _, err := e.Start(ctx, h, 0); err != nil {
					t.Fatalf("Start %s: %v", h, err)
				}
			}
			waitForPosition(t, e, string(alert), 100*time.Millisecond, 5*time.Second)

			seekErr := make(chan error, 1)
			go func() {
				_, err := e.Seek(ctx, alert, 2*time.Second)
				seekErr <- err
			}()
			if delay > 0 {
				time.Sleep(delay)
			}
			released, err := e.ReleaseAll(ctx, alert)
			if err != nil {
				t.Fatalf("ReleaseAll(except %s): %v", alert, err)
			}
			if len(released) != 1 || released[0] != other {
				t.Fatalf("ReleaseAll released %v, want exactly [%s]", released, other)
			}
			if err := <-seekErr; err != nil {
				t.Fatalf("the excepted handle's own Seek failed across the stop: %v", err)
			}

			obs, err := e.Observe(ctx, alert)
			if err != nil {
				t.Fatalf("Observe(%s) after the stop: %v", alert, err)
			}
			if obs.State != "playing" {
				t.Fatalf("excepted handle state = %q after the stop, want playing", obs.State)
			}
			b, err := e.branchFor(alert)
			if err != nil {
				t.Fatalf("branchFor(%s): %v", alert, err)
			}
			if branchMuted(b) {
				t.Fatalf("the excepted handle was muted by a stop that was told to leave it alone")
			}
			// Bounded, never context.Background(): a branch whose NULL
			// transition hangs in GStreamer would hang this test forever.
			relCtx, relCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = e.Release(relCtx, alert)
			relCancel()
		})
	}
}

// TestATornDownBranchsBusErrorNeverBreaksTheEngine proves a stop cannot
// leave the engine unusable for the next show. A branch's own elements
// can still post an error that reaches the bus after teardown removed
// them, and an error no longer attributable to anything in this pipeline
// is not this pipeline's fault. The second half is the control: an error
// from an element still attached must still break the engine.
func TestATornDownBranchsBusErrorNeverBreaksTheEngine(t *testing.T) {
	t.Run("detached element", func(t *testing.T) {
		e := newTestEngine(t)
		ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
		defer cancel()

		branches := loadPlaying(t, e, ctx, "torndown1")
		src := branches[0].filesrc
		if err := e.Release(ctx, "torndown1"); err != nil {
			t.Fatalf("Release: %v", err)
		}

		bus := e.pipeline.GetBus()
		defer releaseBus(bus)
		if !bus.Post(gst.NewMessageError(src, "from a branch this stop already removed", errors.New("Internal data stream error."))) {
			t.Fatal("could not post the test error onto the pipeline bus")
		}
		time.Sleep(time.Second)
		if err := e.brokenErr(); err != nil {
			t.Fatalf("a torn-down branch's queued error broke the engine: %v", err)
		}

		dir := t.TempDir()
		wav := filepath.Join(dir, "after.wav")
		generateWAV(t, wav, 2)
		if _, err := e.Load(ctx, "torndown2", mediaRef(wav), 2*time.Second); err != nil {
			t.Fatalf("Load after a torn-down branch's error: %v", err)
		}
		relCtx, relCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.Release(relCtx, "torndown2")
		relCancel()
	})

	t.Run("attached element", func(t *testing.T) {
		e := newTestEngine(t)
		bus := e.pipeline.GetBus()
		defer releaseBus(bus)
		src, ok := e.pipeline.(gst.Object)
		if !ok {
			t.Skip("pipeline is not a gst.Object in this build")
		}
		if !bus.Post(gst.NewMessageError(src, "from the shared pipeline itself", errors.New("Internal data stream error."))) {
			t.Fatal("could not post the test error onto the pipeline bus")
		}
		deadline := time.Now().Add(5 * time.Second)
		for e.brokenErr() == nil {
			if time.Now().After(deadline) {
				t.Fatal("an error from the pipeline itself did not break the engine; the skip is too broad")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
}
