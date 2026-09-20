package weathertrigger

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPackageNeverImportsAPI mirrors internal/coordinator/config's
// TestPackageNeverImportsAPI (same "go list -deps" mechanism): this
// package holds no reference, direct or transitive, to
// internal/coordinator/api, so it stays pure trigger logic that builds
// [TriggerEvent] and [PendingDecision] values for that package to act on.
// This does NOT prove ADR-053 decision 12's "a trigger may never end a
// delay": the trigger path itself lives in internal/coordinator/api,
// which can reach the resume handler. Only
// TestWeatherDelayTriggerNeverResumes, over there, proves that rule.
func TestPackageNeverImportsAPI(t *testing.T) {
	const forbidden = "github.com/showmeshsystems/showmesh/internal/coordinator/api"

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbidden {
			t.Fatalf("internal/coordinator/weathertrigger transitively imports %q: this package must stay pure trigger "+
				"logic with no reference to the coordinator API", forbidden)
		}
	}
}

// TestPackageNeverImportsStore proves the same package-isolation property
// against internal/coordinator/store: this package is pure logic plus the
// NWS HTTP poller, with no persistence of its own. Persisting a
// [PendingDecision] is internal/coordinator/api's job.
func TestPackageNeverImportsStore(t *testing.T) {
	const forbidden = "github.com/showmeshsystems/showmesh/internal/coordinator/store"

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbidden {
			t.Fatalf("internal/coordinator/weathertrigger transitively imports %q: this package holds no store of its own", forbidden)
		}
	}
}
