//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// findFuncDecl parses filename and returns the top-level function or
// method named fn, or nil if it is not there.
func findFuncDecl(t *testing.T, filename, fn string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}
	var found *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == fn {
			found = d
			return false
		}
		return true
	})
	return found
}

// callsSelector reports whether body calls a method or function named
// name anywhere in its statements.
func callsSelector(body ast.Node, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				found = true
			}
		case *ast.Ident:
			if fn.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}

// TestResumeReanchorsViaFlushingSeek guards Resume against going back to
// an offset-only re-anchor: GstAudioAggregator keeps advancing its own
// output clock for the whole hold, so buffers still carrying pre-hold
// timestamps land in its past and are discarded outright rather than
// played back late. Resume never flush-seeks the branch it was called
// on in place (see swapToPosition's own doc comment); instead it builds
// a replacement and calls prepare on it, which is the one place a real
// flushing seek is issued and confirmed against the hold. This checks
// both hops: Resume calls swapToPosition, and swapToPosition calls
// prepare. See engine_real_integration_test.go's resume-continuity
// tests for the flow-level proof.
func TestResumeReanchorsViaFlushingSeek(t *testing.T) {
	resumeFn := findFuncDecl(t, "methods.go", "Resume")
	if resumeFn == nil {
		t.Fatal("could not find func (e *Engine) Resume in methods.go")
	}
	if !callsSelector(resumeFn.Body, "swapToPosition") {
		t.Fatal("Resume no longer calls swapToPosition: a joined branch must never be flush-seeked in place, " +
			"since GstAudioAggregator's own output clock keeps advancing for the whole hold and discards buffers " +
			"still carrying pre-hold timestamps")
	}

	swapFn := findFuncDecl(t, "methods.go", "swapToPosition")
	if swapFn == nil {
		t.Fatal("could not find func (e *Engine) swapToPosition in methods.go")
	}
	if !callsSelector(swapFn.Body, "prepare") {
		t.Fatal("swapToPosition no longer calls prepare on its replacement branch: without a real flushing seek " +
			"confirmed against the hold, the replacement's segment cannot be trusted to start at the position " +
			"this swap committed to")
	}
}
