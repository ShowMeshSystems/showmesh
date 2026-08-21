//go:build cgo

package gstengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestPushLTCSamplesChecksNilBuffer guards gst.NewBufferAllocate's
// documented nullable return: under real memory pressure it can return
// nil, and calling Map on a nil *gst.Buffer is a cgo call on a nil
// receiver — a panic, not a Go error. This cannot be forced live (normal
// allocation does not fail on this machine), so the guard is a nil check
// immediately after NewBufferAllocate, checked at the source level.
func TestPushLTCSamplesChecksNilBuffer(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "ltc.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing ltc.go: %v", err)
	}

	var fn *ast.FuncDecl
	ast.Inspect(f, func(n ast.Node) bool {
		d, ok := n.(*ast.FuncDecl)
		if ok && d.Name.Name == "pushLTCSamples" {
			fn = d
			return false
		}
		return true
	})
	if fn == nil {
		t.Fatal("could not find func (e *Engine) pushLTCSamples in ltc.go")
	}

	stmts := fn.Body.List
	for i, stmt := range stmts {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			continue
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewBufferAllocate" {
			continue
		}
		bufName, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			t.Fatal("NewBufferAllocate's result is not assigned to a plain identifier")
		}
		if i+1 >= len(stmts) {
			t.Fatal("nothing follows the NewBufferAllocate call")
		}
		next, ok := stmts[i+1].(*ast.IfStmt)
		if !ok {
			t.Fatalf("%s: the statement after NewBufferAllocate is not a nil check on %s", fset.Position(stmts[i+1].Pos()), bufName.Name)
		}
		bin, ok := next.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.EQL {
			t.Fatalf("%s: expected %s == nil, got a different condition", fset.Position(next.Pos()), bufName.Name)
		}
		lhs, lhsOK := bin.X.(*ast.Ident)
		rhs, rhsOK := bin.Y.(*ast.Ident)
		if !lhsOK || !rhsOK || lhs.Name != bufName.Name || rhs.Name != "nil" {
			t.Fatalf("%s: expected %s == nil immediately after NewBufferAllocate, in REAL CODE — a nil buffer "+
				"from allocator exhaustion must never reach buf.Map, a cgo call on a nil receiver", fset.Position(next.Pos()), bufName.Name)
		}
		return
	}
	t.Fatal("pushLTCSamples does not call gst.NewBufferAllocate the way this guard expects")
}

// TestRunLTCFeederRecoversFromPanic guards the standing "a subsystem
// problem never stops the show" rule for the LTC feeder specifically: a
// panicking cgo call (see TestPushLTCSamplesChecksNilBuffer) on this
// goroutine would otherwise kill the whole agent process, program audio
// included. Checked at the source level for the same reason as the
// nil-buffer guard: forcing a real allocator failure is not reachable on
// this machine.
func TestRunLTCFeederRecoversFromPanic(t *testing.T) {
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
		d, ok := n.(*ast.DeferStmt)
		if !ok {
			return true
		}
		lit, ok := d.Call.Fun.(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "recover" {
				found = true
			}
			return true
		})
		return true
	})
	if !found {
		t.Fatal("runLTCFeeder has no deferred recover() — a panic on this goroutine (see the nil-buffer guard) " +
			"would otherwise kill the whole agent process")
	}
}
