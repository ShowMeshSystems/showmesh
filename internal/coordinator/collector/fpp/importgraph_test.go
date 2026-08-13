package fpp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestPackageNeverImportsFPPCommand is Step 7 seam C's mechanical proof
// that this collector's read-only guarantee is unaffected by the
// existence of internal/coordinator/fppcommand, the package that
// deliberately DOES dispatch a command to FPP. A comment asserting the
// two packages are separate is not the deliverable BUILD-PLAN's Step 7
// spec asks for; this is: mirrors cmd/showmeshctl's own
// TestNoForbiddenImports (importgraph_test.go), same mechanism (`go list
// -deps`), applied to the opposite direction — that test proves the CLI
// never imports the coordinator's internals, this one proves the
// read-only collector never imports the one package built specifically
// to send FPP a command.
//
// Before trusting this test: it was run against a deliberately broken
// working tree with an `import _
// "github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"`
// added to fpp.go, and failed as expected — see this task's report.
//
// Step 7 seam C review, defect 7: this test alone does NOT fail if
// fppcommand is ever deleted and its dispatch logic pasted directly into
// this package — the literal merge its own doc comment claims it catches
// — because deleting the import target removes it from every dependency
// list too. [TestPackageSourceNeverConstructsACommandPath] and
// [TestFPPCommandPackageStillExistsSeparately] below are what actually
// close that hole; this test alone is retained because it is still the
// right check for the ORDINARY defect (a stray import creeping back in
// while both packages still exist).
func TestPackageNeverImportsFPPCommand(t *testing.T) {
	const forbidden = "github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbidden {
			t.Fatalf("internal/coordinator/collector/fpp transitively imports %q — this collector must stay read-only; "+
				"a command dispatch belongs only in that package, called only from internal/coordinator/api", forbidden)
		}
	}
}

// TestFPPCommandPackageStillExistsSeparately is defect 7's other half:
// proves internal/coordinator/fppcommand has not been deleted (folded
// into this package or anywhere else). "go list" on it must succeed —
// this is what a working tree that performed the literal merge this
// file's own doc comment describes (delete fppcommand, paste
// Client/Invoke/StopPlaylist into package fpp) would fail, where
// [TestPackageNeverImportsFPPCommand] alone would not, since a deleted
// package cannot appear in any import list either.
func TestFPPCommandPackageStillExistsSeparately(t *testing.T) {
	const fppcommandPkg = "github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	out, err := exec.Command("go", "list", fppcommandPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s failed (%v) — this package must exist as its own, separate package, never folded into "+
			"internal/coordinator/collector/fpp: output:\n%s", fppcommandPkg, err, out)
	}
}

// TestPackageSourceNeverConstructsACommandPath is defect 7's own fix:
// parses every non-test .go file in this package's directory and fails if
// any REAL CODE (a string literal, not a comment — go/parser's AST never
// contains comment text as a BasicLit) contains FPP's command-dispatch
// path, "/api/command/". This is what actually catches the literal merge
// scenario this file's own doc comment names: deleting
// internal/coordinator/fppcommand and pasting its Invoke method (which
// builds exactly `baseURL + "/api/command/" + url.PathEscape(name)`)
// directly into this package's own fpp.go would leave that string literal
// sitting in this package's compiled code, regardless of what any import
// list says.
//
// Deliberately AST-based, not a plain string search over file bytes: this
// package's own fpp.go doc comment already discusses "/api/command/..."
// in PROSE, explaining why this collector forces CheckRedirect — a
// byte-level grep would flag that legitimate comment as a violation. Only
// a string LITERAL appearing in real, compiled code is what this test
// cares about.
func TestPackageSourceNeverConstructsACommandPath(t *testing.T) {
	const forbiddenPath = "/api/command/"

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
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.Contains(val, forbiddenPath) {
				t.Errorf("%s: string literal %q contains FPP's command-dispatch path %q in REAL CODE, not a comment — "+
					"this collector must stay read-only; a command dispatch belongs only in "+
					"internal/coordinator/fppcommand, called only from internal/coordinator/api",
					fset.Position(lit.Pos()), val, forbiddenPath)
			}
			return true
		})
	}
}
