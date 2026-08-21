//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestBranchForSourceNeverHoldsTheLockAcrossGetParent guards
// branchForSource against re-acquiring e.mu for the whole walk: obj.GetParent
// is a cgo call that can walk arbitrary depth into decodebin's internal
// decoder chain, and holding e.mu across it would block every other
// handle-addressed engine call (Start, Seek, Observe, ...) behind that
// walk. A live-timing test of this property is not reachable on this
// environment: building a synthetic deep GStreamer object chain to force
// a slow walk crashed the test binary rather than producing a measurable
// one (see the LTC feeder finding write-up for why cgo-level timing
// experiments were abandoned here), so the invariant is checked at the
// source level: branchForSource must never contain a defer to e.mu.Unlock,
// the pattern that held it for the loop's entire lifetime before the fix.
func TestBranchForSourceNeverHoldsTheLockAcrossGetParent(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "engine_cgo.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing engine_cgo.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "branchForSource" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) branchForSource in engine_cgo.go")
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		sel, ok := d.Call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "Unlock" {
			t.Fatalf("%s: branchForSource defers e.mu.Unlock, which holds the lock for the function's entire "+
				"lifetime — including the obj.GetParent() walk, a cgo call that can run arbitrarily long. Lock "+
				"and unlock e.mu individually around each map read instead.", fset.Position(d.Pos()))
		}
		return true
	})
}
