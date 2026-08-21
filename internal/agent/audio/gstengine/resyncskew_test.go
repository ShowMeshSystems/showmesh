//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestResumeSamplesPositionAndRunningTimeBackToBack guards Resume against
// reading position via a separate queryPosition call instead of through
// resyncMixerPadsToLivePosition, colocated with the running-time read.
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
