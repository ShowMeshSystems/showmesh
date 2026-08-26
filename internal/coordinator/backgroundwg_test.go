package coordinator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRunBackgroundGoroutinesGoThroughSpawnBackground statically enforces
// the invariant that keeps Run's background-goroutine count from drifting
// off the number of goroutines actually started: every goroutine joined via
// backgroundWG must be started through the spawnBackground helper, which
// pairs backgroundWG.Add(1) with the goroutine it counts at a single call
// site. A leading backgroundWG.Add(N) followed by N+1 (or N-1) unconditional
// goroutines below it, each with its own bare "defer backgroundWG.Done()",
// used to require a human to keep two numbers in sync by hand and they
// drifted. This test fails the moment a future seam adds a raw
// "backgroundWG.Add" or "backgroundWG.Done" call anywhere in Run outside
// spawnBackground's own definition, which is exactly the shape that let the
// count and the goroutines disagree.
func TestRunBackgroundGoroutinesGoThroughSpawnBackground(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "coordinator.go", nil, 0)
	if err != nil {
		t.Fatalf("parse coordinator.go: %v", err)
	}

	var runFn *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "Run" {
			runFn = fn
			break
		}
	}
	if runFn == nil {
		t.Fatal("could not find func Run in coordinator.go")
	}

	// spawnBackgroundLit is the single FuncLit assigned to the
	// spawnBackground variable: backgroundWG.Add and backgroundWG.Done may
	// only appear inside it.
	var spawnBackgroundLit *ast.FuncLit
	ast.Inspect(runFn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		ident, ok := assign.Lhs[0].(*ast.Ident)
		if !ok || ident.Name != "spawnBackground" {
			return true
		}
		lit, ok := assign.Rhs[0].(*ast.FuncLit)
		if ok {
			spawnBackgroundLit = lit
		}
		return true
	})
	if spawnBackgroundLit == nil {
		t.Fatal("could not find the spawnBackground helper in Run; " +
			"if it was renamed or removed, this test needs updating alongside it")
	}

	insideSpawnBackground := func(pos token.Pos) bool {
		return pos >= spawnBackgroundLit.Pos() && pos <= spawnBackgroundLit.End()
	}

	var addOutside, doneOutside, addInside, doneInside, spawnCalls int
	ast.Inspect(runFn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "spawnBackground" {
			spawnCalls++
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "backgroundWG" {
			return true
		}
		switch sel.Sel.Name {
		case "Add":
			if insideSpawnBackground(call.Pos()) {
				addInside++
			} else {
				addOutside++
			}
		case "Done":
			if insideSpawnBackground(call.Pos()) {
				doneInside++
			} else {
				doneOutside++
			}
		}
		return true
	})

	if addOutside != 0 {
		t.Errorf("found %d backgroundWG.Add call(s) outside spawnBackground; "+
			"every background goroutine must be started via spawnBackground so "+
			"the Add count cannot drift from the goroutines it counts", addOutside)
	}
	if doneOutside != 0 {
		t.Errorf("found %d backgroundWG.Done call(s) outside spawnBackground; "+
			"every background goroutine must be started via spawnBackground so "+
			"the Done count cannot drift from the goroutines it counts", doneOutside)
	}
	if addInside != 1 {
		t.Errorf("spawnBackground should call backgroundWG.Add exactly once, found %d", addInside)
	}
	if doneInside != 1 {
		t.Errorf("spawnBackground should call backgroundWG.Done exactly once, found %d", doneInside)
	}
	// A sanity floor, not a ceiling: Run currently starts 11 unconditional
	// background loops through spawnBackground (hub, fppRunner, assetSync,
	// the asset-settings reconciler, the Resolume composition wiring
	// refresh, the unclaimed-bootstrap watcher, the night loop, the cue
	// activation loop, the FPP MQTT manager, the Resolume manager, and the
	// show-mode loop). This only guards against the whole block silently
	// disappearing; it does not need bumping when a new loop is added,
	// because spawnBackground keeps the count correct by construction.
	if spawnCalls < 11 {
		t.Errorf("expected at least 11 spawnBackground calls in Run, found %d", spawnCalls)
	}
}
