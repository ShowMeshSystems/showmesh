package api

import (
	"os/exec"
	"strings"
	"testing"
)

// This file is the wave 2 shared contract section 1's own mechanical
// enforcement: "internal/coordinator/macro imports internal/coordinator/api.
// internal/coordinator/api must NEVER import internal/coordinator/macro."
// The reverse edge is an import cycle, so the Go compiler already catches
// it today — but a future refactor could break the cycle some OTHER way
// (a shared third package both import, say) without the compiler
// noticing that the underlying rule — the macro executor is the one and
// only caller of this package's FPPCommandDispatcher, never the other
// way around — had been violated. This test states the rule directly
// rather than relying on the compiler's incidental enforcement of it,
// mirroring cmd/showmeshctl's own importgraph_test.go and
// internal/coordinator/config's own importgraph_test.go exactly.
var forbiddenImports = []string{
	"github.com/showmeshsystems/showmesh/internal/coordinator/macro",
}

func TestAPIDoesNotImportMacro(t *testing.T) {
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
			t.Errorf("internal/coordinator/api transitively imports forbidden package %q: "+
				"the import direction is forced the other way (macro_seam.go's own top comment) — "+
				"internal/coordinator/macro imports this package to reach FPPCommandDispatcher, "+
				"so this package importing macro back would be a cycle", forbidden)
			continue
		}
		prefix := forbidden + "/"
		for _, d := range deps {
			if strings.HasPrefix(d, prefix) {
				t.Errorf("internal/coordinator/api transitively imports %q, a subpackage of forbidden package %q", d, forbidden)
			}
		}
	}
}
