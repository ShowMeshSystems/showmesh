package coordinator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoResolumeWiringFileSupervisesAProcess is the companion to
// internal/coordinator/collector/resolume's
// TestNoNonTestFileSupervisesAProcess (criterion 7, build contract §2.5):
// "the resolume*.go files under internal/coordinator/" half of that
// guard's scope. Two files rather than one because Go tests are
// per-package and these are two different packages; see that test's own
// doc comment for the full reasoning.
func TestNoResolumeWiringFileSupervisesAProcess(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "resolume") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checkResolumeWiringFileHasNoProcessSupervision(t, fset, name)
	}
}

func checkResolumeWiringFileHasNoProcessSupervision(t *testing.T, fset *token.FileSet, path string) {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	for _, imp := range f.Imports {
		if imp.Path.Value == `"os/exec"` {
			t.Errorf("%s: imports \"os/exec\" — no code under this guard's two directories may start, restart, "+
				"or signal the Arena process (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §1). This is a boundary for "+
				"CORE, not a verdict that process supervision is wrong everywhere; a watchdog, if ever built, is "+
				"its own small repository, never part of core.",
				fset.Position(imp.Pos()))
		}
	}

	forbiddenSelectors := map[string]map[string]bool{
		"exec":    {"Command": true, "CommandContext": true},
		"os":      {"StartProcess": true, "FindProcess": true, "Process": true},
		"syscall": {"Kill": true},
	}

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// A method named exactly Signal is flagged regardless of receiver
		// (build contract §2.5's (*os.Process).Signal) — see the sibling
		// guard's own identical comment
		// (internal/coordinator/collector/resolume/guardnoprocesssupervision_test.go).
		if sel.Sel.Name == "Signal" {
			t.Errorf("%s: calls a method named Signal — no code under this guard's two directories may start, "+
				"restart, or signal the Arena process (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §1). This is a "+
				"boundary for CORE, not a verdict that process supervision is wrong everywhere; a watchdog, if "+
				"ever built, is its own small repository, never part of core.",
				fset.Position(sel.Pos()))
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if forbiddenSelectors[ident.Name][sel.Sel.Name] {
			t.Errorf("%s: calls %s.%s — no code under this guard's two directories may start, restart, or "+
				"signal the Arena process (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §1). This is a boundary for CORE, "+
				"not a verdict that process supervision is wrong everywhere; a watchdog, if ever built, is its "+
				"own small repository, never part of core.",
				fset.Position(sel.Pos()), ident.Name, sel.Sel.Name)
		}
		return true
	})
}
