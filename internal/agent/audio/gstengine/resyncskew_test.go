//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestResumeSamplesPositionAndRunningTimeBackToBack guards the two-clock
// skew finding in resyncMixerPads: Resume used to query the branch's live
// decode position, return to Resume's own stack frame, and only then call
// resyncMixerPads (which reads the pipeline's running time) — two
// independent cgo reads separated by a function boundary and everything
// Resume did between them. resyncMixerPadsToLivePosition reads both
// inside one function body with nothing else between the two cgo calls,
// which is the smallest gap this API allows; some residual skew between
// two independent clock reads is unavoidable across a cgo boundary and is
// not eliminated by this test, only minimized and documented (see
// resyncMixerPadsToLivePosition's doc comment). Checked at the source
// level because the skew itself is far below what any timer on this
// machine can observe.
func TestResumeSamplesPositionAndRunningTimeBackToBack(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing methods.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "Resume" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) Resume in methods.go")
	}

	callsLivePosition := false
	callsQueryPositionDirectly := false
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
		case "resyncMixerPadsToLivePosition":
			callsLivePosition = true
		case "queryPosition":
			callsQueryPositionDirectly = true
		}
		return true
	})

	if !callsLivePosition {
		t.Fatal("Resume no longer calls resyncMixerPadsToLivePosition — if it went back to a separately captured " +
			"position, the two-clock reads are a function boundary apart again")
	}
	if callsQueryPositionDirectly {
		t.Fatal("Resume calls queryPosition directly, in addition to resyncMixerPadsToLivePosition — the position " +
			"read must happen only inside resyncMixerPadsToLivePosition, immediately adjacent to the running-time " +
			"read, not captured separately first")
	}
}
