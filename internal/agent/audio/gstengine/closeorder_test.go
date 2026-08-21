//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCloseMarksBrokenBeforeAnyTeardownStep guards the ordering
// [Engine.Close] depends on: watchBus can attribute a bus message to no
// branch and call [Engine.markBroken] on its own, and markBroken's
// first-write-wins guard only makes closedReason durable if Close's own
// call to it happens before any teardown step that could cause such a
// message — an unindexed branch mid-teardown, the shared topology during
// SetState(NULL). This cannot be exercised as a timing race against a
// live pipeline: GStreamer's own state-change locking serializes bus
// delivery with the very SetState calls teardown makes, so a test that
// tries to race a bus error against a real Close never lands during the
// vulnerable window on any environment available here — the failure mode
// this guards is a source-order regression, not a live race, so it is
// checked at the source level instead.
func TestCloseMarksBrokenBeforeAnyTeardownStep(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "engine_cgo.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing engine_cgo.go: %v", err)
	}

	var closeFn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Close" && fn.Recv != nil {
			closeFn = fn
			return false
		}
		return true
	})
	if closeFn == nil {
		t.Fatal("could not find func (e *Engine) Close() in engine_cgo.go")
	}

	// Close's body is expected to be exactly one statement:
	// e.closeOnce.Do(func() { ... }); dig into that literal's body.
	var body []ast.Stmt
	for _, stmt := range closeFn.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expr.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		lit, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
		if !ok {
			continue
		}
		body = lit.Body.List
		break
	}
	if body == nil {
		t.Fatal("could not find the closeOnce.Do(func() { ... }) literal inside Close")
	}
	if len(body) == 0 {
		t.Fatal("closeOnce.Do's literal has an empty body")
	}

	first, ok := body[0].(*ast.ExprStmt)
	if !ok {
		t.Fatalf("closeOnce.Do's first statement is not a call: %s", fset.Position(body[0].Pos()))
	}
	call, ok := first.X.(*ast.CallExpr)
	if !ok {
		t.Fatalf("closeOnce.Do's first statement is not a call: %s", fset.Position(first.Pos()))
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "markBroken" {
		t.Fatalf("%s: Close's first action must be e.markBroken(closedReason), not %s — "+
			"moving it later reopens the race this test guards: a bus message attributed to no branch during "+
			"teardown could win markBroken's first-write-wins guard before Close records closedReason itself",
			fset.Position(first.Pos()), astString(first.X))
	}
	if len(call.Args) != 1 {
		t.Fatalf("%s: e.markBroken called with %d args, want exactly closedReason", fset.Position(call.Pos()), len(call.Args))
	}
	arg, ok := call.Args[0].(*ast.Ident)
	if !ok || arg.Name != "closedReason" {
		t.Fatalf("%s: Close's first markBroken call must use closedReason, not %s", fset.Position(call.Pos()), astString(call.Args[0]))
	}
}

func astString(n ast.Expr) string {
	switch x := n.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.CallExpr:
		return astString(x.Fun) + "(...)"
	case *ast.SelectorExpr:
		return astString(x.X) + "." + x.Sel.Name
	default:
		return "<expr>"
	}
}
