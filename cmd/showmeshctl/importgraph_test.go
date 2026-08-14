package main

import (
	"os/exec"
	"strings"
	"testing"
)

// coordinatorImportBase is the import path this program must never import,
// exactly OR as a subpackage — every package rooted here is, by
// construction, a coordinator package (CLAUDE.md: "showmeshctl ... must
// never import a coordinator package").
//
// This used to be a hand-enumerated deny-list of four specific
// subpackages (api/store/inventory/collector) instead of a prefix rule,
// and that was a defect this test itself never caught: wave 2 added
// internal/coordinator/macro and nobody added it to the list, so this
// test passed — not because the rule held, but because macro happened to
// transitively pull two OTHER packages that were already enumerated
// (internal/coordinator/api and .../store). internal/coordinator/config,
// internal/coordinator/broker, internal/coordinator/httpapi and
// internal/coordinator/readiness would all still pass today under the old
// list, despite every one of them being exactly the thing the standing
// rule forbids. A prefix rule forbids a new coordinator package by
// default the moment it exists, rather than the moment someone remembers
// to enumerate it.
const coordinatorImportBase = "github.com/showmeshsystems/showmesh/internal/coordinator"

// allowedCoordinatorImports is the explicit escape hatch for the
// coordinatorImportBase prefix rule: empty today, on purpose. No
// coordinator package is a legitimate showmeshctl dependency — this
// program decodes the wire contract independently precisely so a
// coordinator-side rename cannot silently rename both sides of its own
// decode at once (doc.go). A future, deliberate exception must be listed
// here BY NAME, so adding one is a visible, reviewable decision rather
// than the silent gap the old enumerated list was.
var allowedCoordinatorImports = map[string]bool{}

// isCoordinatorImport reports whether dep is coordinatorImportBase itself
// or any subpackage of it (e.g. ".../internal/coordinator/macro", or a
// future ".../internal/coordinator/foo/bar").
func isCoordinatorImport(dep string) bool {
	return dep == coordinatorImportBase || strings.HasPrefix(dep, coordinatorImportBase+"/")
}

// forbiddenImports are additional, individually named forbidden packages
// that sit OUTSIDE the internal/coordinator/ prefix above and so need
// their own entries: pkg/observation and pkg/command are shared models a
// coordinator package also happens to use, not coordinator packages
// themselves, so coordinatorImportBase's prefix rule does not reach them.
// Importing either would let a server-side rename silently rename both
// sides of this CLI's own independent decode at once — the exact failure
// mode task spec §1 exists to prevent ("a test that passes whether or not
// the bug is present is worse than no test").
var forbiddenImports = []string{
	"github.com/showmeshsystems/showmesh/pkg/observation",

	// pkg/command is not itself a coordinator package (it is the shared
	// envelope model), but this CLI mints its own idempotency key
	// independently (cmd_fpp_stop_playlist.go's newIdempotencyKey) rather
	// than calling pkg/command.NewIdempotencyKey (see
	// cmd_fpp_command.go's own newIdempotencyKey, shared by every "fpp
	// <verb>" write subcommand), for the identical reason it decodes every
	// wire type independently rather than importing pkg/observation for
	// one: a future change to that package's minting logic must not be
	// able to silently change what this CLI sends without a reviewer
	// noticing the coupling.
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

	// The prefix rule: ANY internal/coordinator/ dependency is forbidden
	// by default unless explicitly allow-listed. This is deliberately not
	// an enumerated list of known-bad subpackages — see
	// coordinatorImportBase's own doc comment for why that shape already
	// failed once.
	for _, d := range deps {
		if !isCoordinatorImport(d) {
			continue
		}
		if allowedCoordinatorImports[d] {
			continue
		}
		t.Errorf("cmd/showmeshctl transitively imports coordinator package %q, which is not in allowedCoordinatorImports (CLAUDE.md: showmeshctl must never import a coordinator package)", d)
	}

	for _, forbidden := range forbiddenImports {
		if depSet[forbidden] {
			t.Errorf("cmd/showmeshctl transitively imports forbidden package %q (task spec §1: showmeshctl must decode the wire contract independently, not share types with the server)", forbidden)
			continue
		}
		// Also catch a subpackage of a forbidden package (e.g. a future
		// pkg/observation/foo), not just an exact match.
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
	// assertion silently stopped meaning anything. It is NOT under
	// internal/coordinator/, so the prefix rule above does not touch it.
	if !depSet["github.com/showmeshsystems/showmesh/internal/version"] {
		t.Error("expected cmd/showmeshctl to import internal/version (build stamping); it appears to be missing")
	}
}
