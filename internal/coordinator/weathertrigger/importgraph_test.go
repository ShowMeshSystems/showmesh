package weathertrigger

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPackageNeverImportsAPI mirrors internal/coordinator/config's
// TestPackageNeverImportsAPI (same "go list -deps" mechanism), proving
// ADR-053 decision 12's "a trigger may never end a delay" at the strongest
// level Go's own module graph can state it: this package holds no
// reference, direct or transitive, to internal/coordinator/api, which is
// the only package that defines handleWeatherDelayResume. A package that
// never imports another cannot call a function in it, so no code path
// through weathertrigger can reach the resume handler; it can only build
// [TriggerEvent] and [PendingDecision] values for internal/coordinator/api
// to act on.
func TestPackageNeverImportsAPI(t *testing.T) {
	const forbidden = "github.com/showmeshsystems/showmesh/internal/coordinator/api"

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}

	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbidden {
			t.Fatalf("internal/coordinator/weathertrigger transitively imports %q — a trigger source must never be "+
				"able to reach the resume handler, and the only way this package can be prevented from calling it "+
				"is to never import the package that defines it", forbidden)
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
			t.Fatalf("internal/coordinator/weathertrigger transitively imports %q — this package holds no store of its own", forbidden)
		}
	}
}
