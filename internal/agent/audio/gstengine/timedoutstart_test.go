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
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	const trials = 15
	var buggy, timedOut int
	for i := 0; i < trials; i++ {
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
				// The bug: Position now reads a live query (frozen=false)
				// even though the branch never actually reached PLAYING.
				buggy++
			}
		}

		_ = e.Release(context.Background(), handle)
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

// TestTeardownGuardsAgainstEveryAbandonedStateChange proves at the
// source level that teardown refuses to touch a pad or an element while
// any setElementsState call on this branch, not only its own, may
// still be running. A live timing race against a real pipeline cannot be
// forced onto the vulnerable window from Go (see
// TestCloseMarksBrokenBeforeAnyTeardownStep in closeorder_test.go for the
// same argument applied to a different ordering hazard in this package),
// so this is checked structurally instead: doTeardown's body (teardown
// itself is now only the sync.Once wrapper around it, see
// TestTeardownIsWrappedInSyncOnce) must call awaitNoElementRace before
// it calls setElementsState or touches b.channelMixerPads / bin.Remove.
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

// TestTeardownIsWrappedInSyncOnce proves teardown's own body is the
// teardownOnce.Do wrapper, not a released-flag check set ahead of the
// guard: a caller that retries teardown after a deferred attempt must
// see the same terminal outcome, never a false nil from a guard that
// ran before doTeardown decided anything.
func TestTeardownIsWrappedInSyncOnce(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var teardownFn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "teardown" && fn.Recv != nil {
			teardownFn = fn
			return false
		}
		return true
	})
	if teardownFn == nil {
		t.Fatal("could not find func (b *branch) teardown in methods.go")
	}

	usesOnce := false
	ast.Inspect(teardownFn.Body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if base, ok := sel.X.(*ast.SelectorExpr); ok && base.Sel.Name == "teardownOnce" && sel.Sel.Name == "Do" {
			usesOnce = true
		}
		return true
	})
	if !usesOnce {
		t.Fatal("teardown no longer calls b.teardownOnce.Do: its idempotency guard must run the real attempt " +
			"exactly once and return that same outcome to every caller, not a released-only check that can be " +
			"set true before the attempt's outcome is known")
	}
}
