package main

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenImports are exactly the packages task spec §1 names: importing
// any of them (or a subpackage of one) would let a JSON tag rename on the
// server rename the field on both sides of this CLI's decode at once,
// which is the precise failure mode contract §1 exists to prevent — "a
// test that passes whether or not the bug is present is worse than no
// test." This test is the mechanical enforcement the task spec demands;
// see doc.go for the prose version of the same rule.
var forbiddenImports = []string{
	"github.com/showmeshsystems/showmesh/internal/coordinator/api",
	"github.com/showmeshsystems/showmesh/internal/coordinator/store",
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory",
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector",
	"github.com/showmeshsystems/showmesh/pkg/observation",

	// internal/coordinator/fppcommand (Step 7 seam C) is the coordinator's
	// own FPP command dispatch client — obviously a coordinator package
	// (CLAUDE.md: "showmeshctl ... must never import a coordinator
	// package"), named here explicitly rather than left to the general
	// rule alone. pkg/command is not itself a coordinator package (it is
	// the shared envelope model), but this CLI mints its own idempotency
	// key independently (cmd_fpp_stop_playlist.go's newIdempotencyKey)
	// rather than calling pkg/command.NewIdempotencyKey (see
	// cmd_fpp_command.go's own newIdempotencyKey, shared by every "fpp
	// <verb>" write subcommand), for the identical reason it decodes every
	// wire type independently rather than importing pkg/observation for
	// one: a future change to that package's minting logic must not be
	// able to silently change what this CLI sends without a reviewer
	// noticing the coupling.
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand",
	"github.com/showmeshsystems/showmesh/pkg/command",
}

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
			t.Errorf("cmd/showmeshctl transitively imports forbidden package %q (task spec §1: showmeshctl must decode the wire contract independently, not share types with the server)", forbidden)
			continue
		}
		// Also catch a subpackage of a forbidden package (e.g. a future
		// internal/coordinator/collector/fpp), not just an exact match.
		prefix := forbidden + "/"
		for _, d := range deps {
			if strings.HasPrefix(d, prefix) {
				t.Errorf("cmd/showmeshctl transitively imports %q, a subpackage of forbidden package %q", d, forbidden)
			}
		}
	}

	// internal/version is explicitly allowed (task spec §1: "build
	// stamping, not contract"). Assert it's actually there so this test
	// would notice if that import were ever accidentally removed and this
	// assertion silently stopped meaning anything.
	if !depSet["github.com/showmeshsystems/showmesh/internal/version"] {
		t.Error("expected cmd/showmeshctl to import internal/version (build stamping); it appears to be missing")
	}
}
