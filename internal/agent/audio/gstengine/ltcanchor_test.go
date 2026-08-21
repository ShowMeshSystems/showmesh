//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestResolveFeedAnchorStaysUnresolvedWithNoPipeline proves
// resolveFeedAnchor leaves anchorKnown false, rather than reading a
// running time it does not have, when a channel is not bound to a
// pipeline yet. A real gst.Pipeline reaching PLAYING asynchronously and
// reporting gst.ClockTimeNone cannot be reproduced against fakesink,
// whose state transition is synchronous — this exercises the same "no
// real running time available yet" branch resolveFeedAnchor takes for
// that case, through the one condition this environment can actually
// drive: ch.pipeline == nil.
func TestResolveFeedAnchorStaysUnresolvedWithNoPipeline(t *testing.T) {
	ch := &ltcChannel{}
	ch.resolveFeedAnchor()
	if ch.anchorKnown {
		t.Fatal("resolveFeedAnchor set anchorKnown with no pipeline bound")
	}
	if ch.feedAnchor != 0 {
		t.Fatalf("resolveFeedAnchor set feedAnchor = %v with no pipeline bound, want the zero value untouched", ch.feedAnchor)
	}
}

// TestLTCConfirmationRequiresAnchorKnown is a source-level guard for the
// zero-anchor defect: a confirmed LTC emission may only claim
// [agentaudio.LTCRunning] once resolveFeedAnchor has actually read a real
// running time. This cannot be driven live on fakesink (its PLAYING
// transition is synchronous, so ch.pipeline.GetCurrentRunningTime never
// observes gst.ClockTimeNone here), so the confirmation gate is checked
// at the source level: runLTCFeeder's confirmation condition must test
// ch.anchorKnown alongside the checks it already had.
func TestLTCConfirmationRequiresAnchorKnown(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "ltc.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing ltc.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "runLTCFeeder" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) runLTCFeeder in ltc.go")
	}

	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// The confirmation gate is the one `if` whose condition ANDs
		// together at least "pushed" and "ch.emittedGeneration...==gen";
		// require ch.anchorKnown to appear somewhere in that same
		// condition, not merely somewhere in the function.
		text := condText(ifStmt.Cond)
		if containsAll(text, "pushed", "emittedGeneration") {
			found = containsAll(text, "anchorKnown")
		}
		return true
	})
	if !found {
		t.Fatal("runLTCFeeder's confirmation condition (the one guarding the LTCRunning observation) does not " +
			"test ch.anchorKnown — a confirmed push could report LTCRunning before the PTS anchor is known, " +
			"which is the zero-anchor defect this test guards")
	}
}

func condText(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.BinaryExpr:
		return condText(x.X) + " " + condText(x.Y)
	case *ast.SelectorExpr:
		return condText(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	case *ast.CallExpr:
		s := condText(x.Fun)
		for _, a := range x.Args {
			s += " " + condText(a)
		}
		return s
	case *ast.ParenExpr:
		return condText(x.X)
	default:
		return ""
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
