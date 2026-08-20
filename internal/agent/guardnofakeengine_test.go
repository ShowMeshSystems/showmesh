package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestProductionNeverConstructsFakeEngine guards Track C phase 1b's own
// requirement: nothing in this package's PRODUCTION source (every .go
// file that is not itself a _test.go file) may call
// [audio.NewFakeEngine] — the real gstengine backend, behind
// [audio.SwitchableEngine], is what agent.go wires in. audio.FakeEngine
// remains real and necessary for tests, which is exactly why this guard
// is scoped to non-test files by an AST walk (matching this codebase's
// existing OSC/UDP import guards in internal/coordinator/collector/
// resolume) rather than a broader rule that would also flag legitimate
// test usage.
func TestProductionNeverConstructsFakeEngine(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "audio" && sel.Sel.Name == "NewFakeEngine" {
				t.Errorf("%s: production code calls audio.NewFakeEngine — the real gstengine backend behind "+
					"audio.SwitchableEngine is what production wires in (agent.go); FakeEngine.Available() is "+
					"always false and exists only to prove the session state machine in tests.",
					fset.Position(call.Pos()))
			}
			return true
		})
	}
}
