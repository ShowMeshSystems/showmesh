package fpp

import (
	"os/exec"
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
