//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// startTimeoutProbe is the ctx deadline TestTimedOutStartUnfreezesOnlyAfterReachingPlaying
// hands the very first Start on a freshly loaded branch. It is tuned
// against this environment to land after seekTo's own real (but fast,
// position-0) seek completes and before setElementsState's transition to
// PLAYING does, so the run demonstrates the ordering bug rather than
// aborting before it or waiting it out entirely; see that test for the
// calibration data behind the choice.
const startTimeoutProbe = 200 * time.Microsecond

// TestTimedOutStartUnfreezesOnlyAfterReachingPlaying reproduces the
// unfreeze-ordering half of item 3 against a real pipeline: a Start whose
// own ctx deadline fires before its transition to PLAYING returns must
// not have already switched Position reporting to a live query while the
// session's own state stays non-playing. Only the first Start after Load
// exercises the vulnerable window: once a branch has reached PLAYING
// once, a later Start's own timeout no longer moves frozen or state away
// from what the branch already settled into, so each trial uses its own
// fresh branch rather than repeating Start on one that already played.
func TestTimedOutStartUnfreezesOnlyAfterReachingPlaying(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	// Shrunk so one trial's Close (which now always attempts the
	// pipeline's own bounded SetState(NULL), even past a deferred
	// branch) cannot itself consume a large share of the trial loop's
	// wall-clock budget under CPU contention.
	withShrunkTeardownTimeout(t, time.Second)

	const trials = 3
	var buggy, timedOut int
	for i := 0; i < trials; i++ {
		func() {
			// Each trial gets its own engine and its own ctx: a
			// timed-out Start abandons a goroutine that keeps driving
			// its branch toward PLAYING, and Release on that branch
			// defers rather than removing it, so running many trials on
			// one shared engine accumulates leaked branches still
			// pushing into the shared channel mixers with their request
			// pads never released. Deliberately not calling Close per
			// trial: Close now always attempts the pipeline's own
			// bin-level SetState(NULL) even past a deferred branch (see
			// TestCloseStillAttemptsPipelineNullDespiteADeferredBranch),
			// which recurses into that very branch's still-abandoned
			// elements and can itself collide with the abandoned
			// goroutine this trial just created; newTestEngine's own
			// t.Cleanup reaps each trial's engine once, batched at the
			// end of the test, rather than racing that collision once
			// per trial. A per-trial ctx still matters on its own: a
			// single ctx shared across every trial would let an earlier
			// trial's slow teardown eat into a later trial's own Load
			// budget instead of its own.
			e := newTestEngine(t)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			handle := agentaudio.EngineHandle(fmt.Sprintf("startfreeze-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
				t.Fatalf("Load (trial %d): %v", i, err)
			}
			b, err := e.branchFor(handle)
			if err != nil {
				t.Fatalf("branchFor (trial %d): %v", i, err)
			}

			tctx, tcancel := context.WithTimeout(context.Background(), startTimeoutProbe)
			_, startErr := e.Start(tctx, handle, 0)
			tcancel()

			b.mu.Lock()
			frozen, state := b.frozen, b.state
			b.mu.Unlock()

			if errors.Is(startErr, context.DeadlineExceeded) {
				timedOut++
				t.Logf("trial %d: startErr=%v frozen=%v state=%v", i, startErr, frozen, state)
				if state != pkgaudio.StatePlaying && !frozen {
					// The bug: Position now reads a live query
					// (frozen=false) even though the branch never
					// actually reached PLAYING.
					buggy++
				}
			}
		}()
	}

	if timedOut == 0 {
		t.Skipf("no trial's Start actually timed out with a %s deadline on this environment; cannot exercise the ordering bug's window here", startTimeoutProbe)
	}
	if buggy > 0 {
		t.Fatalf("%d/%d timed-out trials unfroze before reaching PLAYING (%d/%d trials timed out at all): "+
			"Start's unfreeze must not run until setElementsState(PLAYING) actually succeeds", buggy, timedOut, timedOut, trials)
	}
	t.Logf("%d/%d trials timed out and none unfroze early", timedOut, trials)
}

// TestTimedOutStartMarksAnchorUnknown is Start's counterpart to
// TestTimedOutResumeMarksAnchorUnknown: seekTo's own resync already ran
// (the seek itself succeeded) before Start's setElementsState(PLAYING)
// call times out, so that transition may still land arbitrarily late
// with no way to know when. The branch must be marked errAnchorUnknown
// rather than left silently reanchored.
func TestTimedOutStartMarksAnchorUnknown(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	withShrunkTeardownTimeout(t, time.Second)

	const trials = 3
	var caught int
	for i := 0; i < trials; i++ {
		func() {
			// One engine and one ctx per trial, deliberately not closed
			// per trial; see the identical note in
			// TestTimedOutStartUnfreezesOnlyAfterReachingPlaying.
			e := newTestEngine(t)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			handle := agentaudio.EngineHandle(fmt.Sprintf("startanchor-%d", i))
			if _, err := e.Load(ctx, handle, mediaRef(wav), 4*time.Second); err != nil {
				t.Fatalf("Load (trial %d): %v", i, err)
			}

			tctx, tcancel := context.WithTimeout(context.Background(), startTimeoutProbe)
			_, startErr := e.Start(tctx, handle, 0)
			tcancel()

			if errors.Is(startErr, context.DeadlineExceeded) {
				caught++
				if _, err := e.Seek(ctx, handle, 1*time.Second); !errors.Is(err, errAnchorUnknown) {
					t.Fatalf("trial %d: Seek on a branch left by a timed-out Start: err = %v, want errAnchorUnknown in its chain", i, err)
				}
			}
		}()
	}

	if caught == 0 {
		t.Skipf("no trial's Start actually timed out with a %s deadline on this environment; cannot exercise this window here", startTimeoutProbe)
	}
	t.Logf("exercised the anchorUnknown path on %d/%d trials", caught, trials)
}

// TestTeardownGuardsAgainstEveryAbandonedStateChange proves at the
// source level that teardown refuses to touch a pad or an element while
// any setElementsState call on this branch, not only its own, may
// still be running. A live timing race against a real pipeline cannot be
// forced onto the vulnerable window from Go (see
// TestCloseMarksBrokenBeforeAnyTeardownStep in closeorder_test.go for the
// same argument applied to a different ordering hazard in this package),
// so this is checked structurally instead: doTeardown's body (reached
// only while teardown holds teardownGate, after teardown's own
// b.released check; see TestRetriedTeardownEventuallySucceedsOnceThePendingChangeDrains
// in closeincomplete_test.go for the dynamic retry behavior that guard
// exists for) must call awaitNoElementRace before it calls
// setElementsState or touches b.channelMixerPads / bin.Remove.
func TestTeardownGuardsAgainstEveryAbandonedStateChange(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var teardownFn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "doTeardown" && fn.Recv != nil {
			teardownFn = fn
			return false
		}
		return true
	})
	if teardownFn == nil {
		t.Fatal("could not find func (b *branch) doTeardown in methods.go")
	}

	var awaitIdx, setNullIdx = -1, -1
	i := 0
	ast.Inspect(teardownFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		i++
		switch sel.Sel.Name {
		case "awaitNoElementRace":
			if awaitIdx == -1 {
				awaitIdx = i
			}
		case "setElementsState":
			if setNullIdx == -1 {
				setNullIdx = i
			}
		}
		return true
	})

	if awaitIdx == -1 {
		t.Fatal("teardown does not call awaitNoElementRace: an abandoned state change from an earlier operation " +
			"(a timed-out Start left running toward PLAYING) is not guarded against before teardown touches " +
			"this branch's elements, which is the same hazard its own comment already documents for its own " +
			"abandoned NULL transition")
	}
	if setNullIdx == -1 {
		t.Fatal("could not find teardown's call to setElementsState")
	}
	if awaitIdx > setNullIdx {
		t.Fatal("teardown calls setElementsState before awaitNoElementRace: the guard must run first, or it does " +
			"not prevent the race it exists to prevent")
	}
}

// TestTeardownOnlyCachesGenuineSuccess proves doTeardown does not set
// b.released ahead of a deferral: teardown's only cache is a real
// success, so a deferred attempt (see errTeardownDeferredForRace) stays
// retryable rather than becoming a permanent false success or a
// permanent stale refusal on every later call. See
// TestRetriedTeardownEventuallySucceedsOnceThePendingChangeDrains in
// closeincomplete_test.go for the dynamic proof that a retry actually
// succeeds once the condition clears.
func TestTeardownOnlyCachesGenuineSuccess(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var doTeardownFn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "doTeardown" && fn.Recv != nil {
			doTeardownFn = fn
			return false
		}
		return true
	})
	if doTeardownFn == nil {
		t.Fatal("could not find func (b *branch) doTeardown in methods.go")
	}

	// released must be assigned exactly once in doTeardown's body, and
	// only after the last setElementsState call (the NULL transition),
	// never ahead of either return that reports a deferral.
	var releasedAssignIdx, lastSetElementsStateIdx = -1, -1
	i := 0
	ast.Inspect(doTeardownFn.Body, func(n ast.Node) bool {
		i++
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "released" {
					releasedAssignIdx = i
				}
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "setElementsState" {
				lastSetElementsStateIdx = i
			}
		}
		return true
	})

	if releasedAssignIdx == -1 {
		t.Fatal("doTeardown no longer assigns b.released: teardown would never cache a genuine success, so every " +
			"call would re-run the full teardown attempt forever")
	}
	if lastSetElementsStateIdx == -1 {
		t.Fatal("could not find doTeardown's call to setElementsState")
	}
	if releasedAssignIdx < lastSetElementsStateIdx {
		t.Fatal("doTeardown assigns b.released before its setElementsState(NULL) call: a deferral or failure on " +
			"that call would then be cached as if it had succeeded")
	}
}
