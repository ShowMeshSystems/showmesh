//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestStartUnblocksFlowOnlyAfterItsOwnSeekLands is SM-143's acceptance
// test. A positioned Start (seek then play) issued on a branch that is
// currently paused -- flow blocked, not released -- must not unblock
// that flow before its own seek has actually landed: unblocking first
// lets whatever sat parked at the flow block, carrying the branch's
// pre-seek position and pre-seek mixer pad offset, reach the shared mix
// before the flushing seek meant to discard it has taken effect. Resume
// already follows the correct order for the identical reason (see
// resyncskew_test.go's TestResumeReanchorsViaFlushingSeek); this proves
// Start does too, by asserting Start's own unblockFlow call comes after
// its seekTo call in source, the same technique this package already
// uses for the sibling ordering invariant. A real GStreamer reproduction
// of the resulting drift is in startpausedsibling_real_integration_test.go's
// doc comment: this environment's flushing seeks reliably discarded the
// stale parked data before it reached the mixer in every run, so the
// wrong-position hazard this guards against is real by construction of
// the source, not reliably forceable at runtime here.
func TestStartUnblocksFlowOnlyAfterItsOwnSeekLands(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "Start" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) Start in methods.go")
	}

	var seekToPos, unblockFlowPos token.Pos
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "seekTo":
			if seekToPos == token.NoPos {
				seekToPos = call.Pos()
			}
		case "unblockFlow":
			if unblockFlowPos == token.NoPos {
				unblockFlowPos = call.Pos()
			}
		}
		return true
	})

	if seekToPos == token.NoPos {
		t.Fatal("Start no longer calls seekTo: a positioned Start must re-anchor the branch before playback resumes")
	}
	if unblockFlowPos == token.NoPos {
		t.Fatal("Start no longer calls unblockFlow: a branch paused or stopped before Start would never flow again")
	}
	if unblockFlowPos < seekToPos {
		t.Fatal("Start calls unblockFlow before seekTo: this releases whatever sat parked at the flow block, carrying the " +
			"branch's pre-seek position and pre-seek mixer pad offset, into the shared mix before the flushing seek meant " +
			"to discard it has taken effect, landing live output at a stale position Start never named")
	}
}
