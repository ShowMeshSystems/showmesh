package main

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenImports mirrors cmd/showmeshctl/importgraph_test.go's own list
// exactly, for the identical reason (CLAUDE.md: this program "may never
// import a coordinator package"): importing any of these would let a JSON
// tag rename on the server silently rename the field on both sides of this
// program's own decode at once, which is the precise failure mode this
// program's independent decoding in types.go exists to prevent.
var forbiddenImports = []string{
	"github.com/showmeshsystems/showmesh/internal/coordinator/api",
	"github.com/showmeshsystems/showmesh/internal/coordinator/store",
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory",
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector",
	"github.com/showmeshsystems/showmesh/internal/coordinator/macro",
	"github.com/showmeshsystems/showmesh/internal/coordinator/config",
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker",
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity",
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand",
	"github.com/showmeshsystems/showmesh/pkg/observation",
	"github.com/showmeshsystems/showmesh/pkg/command",
}

// forbiddenPrefix catches every package under internal/coordinator, not
// only the specific subpackages named above — a subpackage added after
// this test was written (e.g. a future internal/coordinator/macro/store)
// must fail this test by default rather than needing this list edited
// first.
const forbiddenPrefix = "github.com/showmeshsystems/showmesh/internal/coordinator"

func TestNoForbiddenImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	depSet := make(map[string]bool, len(deps))
	for _, d := range deps {
		depSet[d] = true
	}

	for _, forbidden := range forbiddenImports {
		if depSet[forbidden] {
			t.Errorf("cmd/showmesh-fpp-plugin transitively imports forbidden package %q (this program must decode the wire contract independently, not share types with the server)", forbidden)
		}
	}

	for _, d := range deps {
		if d == forbiddenPrefix || strings.HasPrefix(d, forbiddenPrefix+"/") {
			t.Errorf("cmd/showmesh-fpp-plugin transitively imports %q, a coordinator-internal package", d)
		}
	}

	// internal/version is explicitly allowed (build stamping, not
	// contract) — asserted present so this test notices if that import
	// were ever accidentally removed and this assertion silently stopped
	// meaning anything, matching cmd/showmeshctl/importgraph_test.go's
	// own final check.
	if !depSet["github.com/showmeshsystems/showmesh/internal/version"] {
		t.Error("expected cmd/showmesh-fpp-plugin to import internal/version (build stamping); it appears to be missing")
	}
}
