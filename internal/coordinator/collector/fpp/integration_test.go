//go:build integration

// This file is the FPP collector's integration suite: it proves claims
// that can only be proven against a real fppd, exactly as
// test/integration's package doc comment describes for the coordinator's
// MQTT side ("only a real Mosquitto connection can prove that"). It never
// fails for want of the dependency — every test skips cleanly, with a
// clear message, when no FPP is reachable — following
// scripts/test-integration.sh's shape (which this suite's own harness
// script, scripts/test-integration-fpp.sh, mirrors for the FPP bench
// instead of Mosquitto).
package fpp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const (
	// envTestFPPURL lets a caller point this suite at an FPP other than
	// the repo's own bench default. See requireLiveFPP.
	envTestFPPURL     = "SHOWMESH_TEST_FPP_URL"
	defaultTestFPPURL = "http://localhost:8090"

	// envTestFPPComposeOverride names an extra compose file to layer over
	// the bench base file. See benchComposeFiles.
	envTestFPPComposeOverride = "SHOWMESH_TEST_FPP_COMPOSE_OVERRIDE"
)

func testFPPURL() string {
	if v := os.Getenv(envTestFPPURL); v != "" {
		return v
	}
	return defaultTestFPPURL
}

// requireLiveFPP skips the calling test, with a clear message, unless an
// FPP is actually reachable at testFPPURL(). It never fails for want of the
// dependency, the same discipline test/integration's MQTT suite applies to
// a missing broker.
func requireLiveFPP(t *testing.T) string {
	t.Helper()
	url := testFPPURL()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/fppd/status", nil)
	if err != nil {
		t.Fatalf("building reachability probe request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		skipOrFatalDependency(t, depFPP, "no FPP reachable at %s (%v); start bench/fpp-multisync (`make test-integration-fpp` does this) or set %s — skipping", url, err, envTestFPPURL)
		return ""
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		skipOrFatalDependency(t, depFPP, "FPP at %s returned HTTP %d for /api/fppd/status; skipping", url, resp.StatusCode)
		return ""
	}
	return url
}

// envRequireTestDeps names which dependencies the harness that invoked
// `go test` actually guarantees, as a comma-separated list (for example
// "broker" or "broker,fpp"). A guard for a dependency on that list becomes
// a hard failure instead of a skip; a guard for anything else still skips.
//
// It is a LIST rather than a boolean deliberately. The first revision was a
// truthy flag and CI failed on it at once: `make test-integration` starts a
// broker and no fppd, so a boolean made it demand an FPP it never supplies
// and turned three legitimately-skipping tests into failures. A harness may
// only be held to the dependencies it actually provides. See
// test/integration/harness_test.go for the full note.
const envRequireTestDeps = "SHOWMESH_REQUIRE_TEST_DEPS"

// Dependency names carried in envRequireTestDeps.
const (
	depBroker = "broker"
	depFPP    = "fpp"
)

// requireTestDep reports whether the invoking harness declared that it
// supplies dep. "1"/"true"/"yes"/"all" mean every dependency, so an older
// harness cannot silently weaken the guard by naming nothing.
func requireTestDep(dep string) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envRequireTestDeps)))
	switch raw {
	case "", "0", "false", "no":
		return false
	case "1", "true", "yes", "all":
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == dep {
			return true
		}
	}
	return false
}

// skipOrFatalDependency skips when the invoking harness did not claim to
// supply dep, and fails hard when it did and then did not.
func skipOrFatalDependency(t *testing.T, dep string, format string, args ...any) {
	t.Helper()
	if requireTestDep(dep) {
		t.Fatalf(format, args...)
		return
	}
	t.Skipf(format, args...)
}

// mustFindSignal is findSignal without the "exactly once" requirement
// dialed down for readability in this file's smaller test count; behavior
// is identical to fpp_test.go's findSignal, duplicated here rather than
// shared because this file compiles under a different build tag and
// cannot rely on non-_test.go-adjacent helpers from the other file being
// present in a build that excludes it.
func mustFindSignal(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("signal %q not present in Poll result", sig)
	return observation.Observation{}
}

// fetchRealMultiSyncState asks the live daemon directly (not through the
// package under test) for its current status.multisync value, so
// TestIntegrationLivePollMatchesRealDaemon compares the collector's answer
// against independently-obtained ground truth rather than against another
// decoding of the same response the collector itself produced.
func fetchRealMultiSyncState(t *testing.T, url string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/fppd/status", nil)
	if err != nil {
		t.Fatalf("building ground-truth request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ground-truth request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var doc struct {
		Multisync bool `json:"multisync"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decoding ground-truth response: %v", err)
	}
	return doc.Multisync
}

// TestIntegrationLivePollMatchesRealDaemon is the L2 core of this suite: a
// live poll against a real, currently-running FPP 9.5.3 produces
// fpp.reachable = true, StateCurrent, and fpp.multisync.enabled matching
// the daemon's own currently-reported state — verified against the real
// daemon at test time, not against a testdata capture.
func TestIntegrationLivePollMatchesRealDaemon(t *testing.T) {
	url := requireLiveFPP(t)
	wantEnabled := fetchRealMultiSyncState(t, url)

	c, err := New("bench-fpp", url, Options{RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	obs, _ := c.Poll(context.Background())

	reachable := mustFindSignal(t, obs, SignalReachable)
	if reachable.Value != true {
		t.Fatalf("fpp.reachable = %v, want true against a live, reachable FPP", reachable.Value)
	}
	if reachable.StateAt(time.Now()) != observation.StateCurrent {
		t.Fatalf("fpp.reachable state = %v, want current", reachable.StateAt(time.Now()))
	}

	enabled := mustFindSignal(t, obs, SignalMultiSyncEnabled)
	if enabled.Absence != "" {
		t.Fatalf("fpp.multisync.enabled Absence = %q, want a value from a reachable daemon", enabled.Absence)
	}
	if enabled.Value != wantEnabled {
		t.Fatalf("fpp.multisync.enabled = %v, want %v (the real daemon's current status.multisync, fetched independently)", enabled.Value, wantEnabled)
	}

	version := mustFindSignal(t, obs, SignalVersion)
	if version.Value == "" || version.Value == nil {
		t.Errorf("fpp.version = %v, want the real daemon's version string", version.Value)
	}

	t.Logf("live daemon at %s: multisync.enabled=%v version=%v", url, enabled.Value, version.Value)
}

// --- The destructive sub-test: actually stopping the container ----------
//
// This is the one test in this package that changes real infrastructure
// state. It is gated to run ONLY against this repo's own bench container
// (testFPPURL() must be the untouched default — an operator-overridden
// SHOWMESH_TEST_FPP_URL is never assumed to be a container this suite is
// allowed to stop) and only when bench/fpp-multisync/docker-compose.yml is
// found relative to this package, which it always is inside this repo. It
// restores the container to running before returning, via t.Cleanup, on
// every exit path including a failing assertion.

// benchComposeFiles returns the compose file paths to layer, relative to
// this test file's own directory (internal/coordinator/collector/fpp), and
// whether the base file exists. Computed relative to this package rather
// than assumed as a fixed absolute path or the process's working
// directory, since `go test` runs with the package directory as its
// working directory regardless of where it was invoked from.
//
// SHOWMESH_TEST_FPP_COMPOSE_OVERRIDE, when set, is appended as a second
// -f layer: CI's prebuilt-fixture mode sets it so this test's own
// --force-recreate recovers the container from the pinned GHCR image
// rather than triggering the source build this file's whole existence is
// meant to avoid. A set-but-missing override fails loudly rather than
// silently falling back to the base file alone.
func benchComposeFiles(t *testing.T) ([]string, bool) {
	t.Helper()
	base := filepath.Join("..", "..", "..", "..", "bench", "fpp-multisync", "docker-compose.yml")
	if _, err := os.Stat(base); err != nil {
		return nil, false
	}
	files := []string{absOrOriginal(t, base)}

	if override := os.Getenv(envTestFPPComposeOverride); override != "" {
		if _, err := os.Stat(override); err != nil {
			t.Fatalf("%s=%q does not exist: %v", envTestFPPComposeOverride, override, err)
		}
		files = append(files, override)
	}
	return files, true
}

func absOrOriginal(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func runCompose(t *testing.T, composeFiles []string, args ...string) error {
	t.Helper()
	full := []string{"compose"}
	for _, f := range composeFiles {
		full = append(full, "-f", f)
	}
	full = append(full, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v: %w\n%s", full, err, out)
	}
	return nil
}

func waitForCondition(t *testing.T, timeout time.Duration, want bool, url string) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		reachable := probeOnce(url)
		if reachable == want {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func probeOnce(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/fppd/status", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// TestIntegrationStoppingContainerProducesCollectionFailedNeverStaleOrFabricated
// is the Task C spec's third required assertion: "stopping the container
// makes the next poll produce collection_failed rather than a stale
// current or a fabricated false." It stops bench/fpp-multisync's fpp-
// master container, polls, asserts, then recreates it (via t.Cleanup, so
// this happens even if an assertion above fails — see the cleanup's own
// comment for why recreation, not a plain restart, is what actually
// recovers it) and waits for it to be reachable again before returning —
// leaving the shared bench in the running state every other test in this
// session expects to find it in.
func TestIntegrationStoppingContainerProducesCollectionFailedNeverStaleOrFabricated(t *testing.T) {
	url := requireLiveFPP(t)

	if os.Getenv(envTestFPPURL) != "" {
		t.Skipf("%s is set to a non-default URL; this test only stops/starts this repo's own bench container, never an operator-supplied endpoint — skipping", envTestFPPURL)
	}
	composeFiles, ok := benchComposeFiles(t)
	if !ok {
		t.Skip("bench/fpp-multisync/docker-compose.yml not found relative to this package; skipping the container-stop test")
	}

	c, err := New("bench-fpp", url, Options{RequestTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Baseline: prove the collector sees this instance as healthy before
	// touching the container, so a failure below is attributable to the
	// stop, not to some pre-existing problem.
	before, _ := c.Poll(context.Background())
	if got := mustFindSignal(t, before, SignalReachable); got.Value != true {
		t.Fatalf("baseline fpp.reachable = %v, want true before stopping the container", got.Value)
	}

	t.Cleanup(func() {
		// NOT `compose start`: verified live, `docker compose stop` then
		// `docker compose start` on this image crash-loops the container
		// (its entrypoint script leaves runtime state — an apache/php pid
		// file — in the container's writable layer that a bare restart of
		// the same container trips over; exits 0 immediately, and the
		// `unless-stopped` restart policy loops it forever). `up -d
		// --force-recreate` recreates the container from the same
		// already-built image (no rebuild) while keeping the named
		// media volume, which is what actually recovers it — confirmed
		// live: the recreated container came back with multisync still
		// at whatever value the volume-persisted setting held.
		if err := runCompose(t, composeFiles, "up", "-d", "--force-recreate", "fpp-master"); err != nil {
			t.Errorf("restoring bench container after test: %v", err)
			return
		}
		if !waitForCondition(t, 60*time.Second, true, url) {
			t.Errorf("bench container did not become reachable again within 60s of recreation")
		}
	})

	if err := runCompose(t, composeFiles, "stop", "fpp-master"); err != nil {
		t.Fatalf("stopping bench container: %v", err)
	}
	if !waitForCondition(t, 20*time.Second, false, url) {
		t.Fatalf("bench container did not become unreachable within 20s of `docker compose stop`")
	}

	after, _ := c.Poll(context.Background())

	reachable := mustFindSignal(t, after, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.reachable Absence = %q after stopping the container, want collection_failed", reachable.Absence)
	}
	if reachable.StateAt(time.Now()) == observation.StateCurrent {
		t.Errorf("fpp.reachable state = current after stopping the container, want anything but current (never a stale current)")
	}

	enabled := mustFindSignal(t, after, SignalMultiSyncEnabled)
	if enabled.Value != nil {
		t.Fatalf("fpp.multisync.enabled = %v after stopping the container, want nil — must never fabricate a value", enabled.Value)
	}
	if enabled.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.multisync.enabled Absence = %q, want collection_failed", enabled.Absence)
	}
}
