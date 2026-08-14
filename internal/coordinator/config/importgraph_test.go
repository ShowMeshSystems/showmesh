package config

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPackageNeverImportsAPI mirrors
// internal/coordinator/collector/fpp's TestPackageNeverImportsFPPCommand
// (same "go list -deps" mechanism) for the wave 2 shared contract section
// 1's forced import direction: internal/coordinator/macro imports
// internal/coordinator/api, so internal/coordinator/api must never import
// internal/coordinator/macro — and this package (config), which api's
// showaction_registry.go itself imports to implement
// [FPPPrimitiveRegistry], must never import api back, or that single edge
// becomes a cycle the moment anything imports both. Verified against a
// deliberately broken working tree — see this task's own report for the
// output.
func TestPackageNeverImportsAPI(t *testing.T) {
	const forbidden = "github.com/showmeshsystems/showmesh/internal/coordinator/api"

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbidden {
			t.Fatalf("internal/coordinator/config transitively imports %q — the import direction is forced the "+
				"other way (api imports config to implement config.FPPPrimitiveRegistry), so config importing api "+
				"back would be an import cycle the moment anything imports both", forbidden)
		}
	}
}
