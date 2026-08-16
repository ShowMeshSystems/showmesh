package resolume

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestNoNonTestFileSupervisesAProcess is Track D seam D-3a's own AST guard
// (criterion 7, build contract §2.5), in the same shape as
// guardosc_test.go and guardpositional_test.go: nothing in this
// repository may start, restart, or signal the Arena process. "Just
// relaunch it" is the obvious thing a future builder adds to a crash-
// recovery seam; this guard is a boundary, not a verdict on process
// supervision everywhere — see TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §1's
// 2026-08-16 amendment: an external watchdog may exist one day, as its own
// small repository, never part of core. This guard only says core is not
// that repository.
//
// Scoped to this package's own directory. See
// guardnoprocesssupervision_coordinator_test.go (internal/coordinator) for
// the companion guard over resolume*.go files under that package — split
// across two files because Go tests are per-package and the two
// directories are two different packages.
func TestNoNonTestFileSupervisesAProcess(t *testing.T) {
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
		checkFileHasNoProcessSupervision(t, fset, name)
	}
}

// checkFileHasNoProcessSupervision parses path and fails t if it imports
// os/exec, or calls exec.Command/exec.CommandContext,
// os.StartProcess/os.FindProcess, or syscall.Kill.
func checkFileHasNoProcessSupervision(t *testing.T, fset *token.FileSet, path string) {
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
		// (build contract §2.5's (*os.Process).Signal): the receiver is a
		// value, not a package-qualified identifier, so the identifier-keyed
		// table above cannot name it the way os.FindProcess/os.StartProcess
		// are named — but no legitimate code under this guard ever calls a
		// method named Signal at all.
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
