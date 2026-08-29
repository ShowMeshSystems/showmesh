//go:build cgo

package gstengine

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// TestBranchComesUpDownstreamFirstAndGoesDownSourceFirst proves
// stateChangeOrder walks a real branch's own elements downstream first
// on the way up and source first on the way down. Expectations are
// derived from b.elements(), not restated, so reordering that slice
// fails here too: the reversal only means "downstream first" while the
// slice stays in link order from filesrc to deinterleave.
func TestBranchComesUpDownstreamFirstAndGoesDownSourceFirst(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	const handle = "order1"
	if _, err := e.Load(ctx, handle, mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, err := e.branchFor(handle)
	if err != nil {
		t.Fatalf("branchFor: %v", err)
	}

	els := b.elements()
	if els[0] != b.filesrc || els[len(els)-1] != b.deinterleave {
		t.Fatalf("b.elements() is no longer in link order from filesrc to deinterleave: %v; "+
			"stateChangeOrder reverses that slice, so reversing a differently ordered one no "+
			"longer brings the branch up downstream first", elementNames(els))
	}

	for _, state := range []gst.State{gst.StatePaused, gst.StatePlaying} {
		got := stateChangeOrder(els, state)
		for i, el := range got {
			if el != els[len(els)-1-i] {
				t.Fatalf("upward transition to %v: order = %v, want b.elements() reversed, %v",
					state, elementNames(got), elementNames(reversedElements(els)))
			}
		}
	}

	got := stateChangeOrder(els, gst.StateNull)
	for i, el := range got {
		if el != els[i] {
			t.Fatalf("downward transition to NULL: order = %v, want b.elements() unchanged, %v",
				elementNames(got), elementNames(els))
		}
	}
}

func elementNames(els []gst.Element) []string {
	out := make([]string, len(els))
	for i, el := range els {
		out[i] = el.GetName()
	}
	return out
}

func reversedElements(els []gst.Element) []gst.Element {
	out := make([]gst.Element, len(els))
	for i, el := range els {
		out[len(els)-1-i] = el
	}
	return out
}

// TestSetElementsStateNowWalksTheStateChangeOrder proves at the source
// level that the state change actually uses stateChangeOrder. The order
// itself cannot be observed from Go once SetState has been called, and
// ranging over b.elements() directly compiles, passes every behavioural
// test in this package, and silently restores the source-first upward
// transition this ordering exists to prevent, so the call site is
// checked structurally instead (the same argument
// TestTeardownGuardsAgainstEveryAbandonedStateChange makes for
// teardown's own ordering hazard).
func TestSetElementsStateNowWalksTheStateChangeOrder(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "branch.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing branch.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "setElementsStateNow" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func setElementsStateNow in branch.go")
	}

	var ranged ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if r, ok := n.(*ast.RangeStmt); ok && ranged == nil {
			ranged = r.X
		}
		return true
	})
	if ranged == nil {
		t.Fatal("setElementsStateNow no longer ranges over anything; it must walk stateChangeOrder's result")
	}

	call, ok := ranged.(*ast.CallExpr)
	if !ok {
		t.Fatalf("setElementsStateNow ranges over %T, not a call to stateChangeOrder: the elements are "+
			"walked in b.elements()'s own order, which brings the branch up source first and lets "+
			"filesrc's push-mode task race decodebin's switch to pull mode", ranged)
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "stateChangeOrder" {
		t.Fatal("setElementsStateNow ranges over a call that is not stateChangeOrder: the elements are " +
			"walked in some other order, which is what this ordering exists to prevent")
	}
}
