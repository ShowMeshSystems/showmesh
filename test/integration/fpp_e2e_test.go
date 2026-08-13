//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// This file is Step 3 review finding 4.2: "the FPP success path is never
// proven end to end through the coordinator." Before this file,
// internal/coordinator/collector/fpp's own integration_test.go proved the
// collector package's Poll against a live fppd, and this package's other
// tests proved the FPP wire shapes only against a deliberately closed
// port (collection_failed). Nothing pointed a real showmesh-coordinator
// SUBPROCESS at the real bench fppd and read the result back through
// GET /api/v1/fpp — the one path that actually exercises collector ->
// sink -> store -> apiwiring -> mapping -> JSON together, which is where a
// value or type mapping error between the domain observation and the wire
// envelope would actually be caught.
//
// This is deliberately NOT part of `make test-integration` (the throwaway-
// Mosquitto target that runs in CI): it requires bench/fpp-multisync's
// fpp-master, a multi-gigabyte source build on first run, exactly the
// reason scripts/test-integration-fpp.sh exists as its own opt-in target
// rather than folding into CI. scripts/test-integration-fpp.sh runs this
// file specifically (by test name), alongside its own throwaway broker,
// after making sure fpp-master is up.

// envTestFPPURL and defaultTestFPPURL mirror
// internal/coordinator/collector/fpp/integration_test.go's own constants
// exactly, so a caller who points that package at a non-default FPP (via
// the same environment variable) gets this file pointed at the same one,
// rather than two "live FPP" test suites silently disagreeing about which
// FPP is live.
const (
	envTestFPPURL     = "SHOWMESH_TEST_FPP_URL"
	defaultTestFPPURL = "http://localhost:8090"
)

func testFPPURL() string {
	if v := os.Getenv(envTestFPPURL); v != "" {
		return v
	}
	return defaultTestFPPURL
}

// requireLiveFPP skips t, with a clear message, unless an FPP is actually
// reachable at testFPPURL() — the same "never fail for want of the
// dependency" discipline requireBroker already applies to the MQTT broker,
// applied here to the second real dependency this file needs.
func requireLiveFPP(t *testing.T) string {
	t.Helper()
	url := testFPPURL()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/fppd/status", nil)
	if err != nil {
		t.Fatalf("building FPP reachability probe request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		skipOrFatalDependency(t, depFPP, "no FPP reachable at %s (%v); run `make test-integration-fpp` (starts bench/fpp-multisync's fpp-master) or set %s — skipping", url, err, envTestFPPURL)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		skipOrFatalDependency(t, depFPP, "FPP at %s returned HTTP %d for /api/fppd/status; skipping", url, resp.StatusCode)
		return ""
	}
	return url
}

// fetchRealMultiSyncState asks the live daemon directly (bypassing the
// coordinator and the collector package entirely) for its current
// status.multisync value, exactly mirroring
// internal/coordinator/collector/fpp/integration_test.go's own helper of
// the same name — so this test's ground truth is obtained completely
// independently of the chain it is trying to prove, not by trusting
// another layer of the same code under test.
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

// TestFPPSuccessPathThroughRealCoordinator is Step 3 review finding 4.2.
// It starts a real showmesh-coordinator subprocess configured with a real
// FPP endpoint (the bench fpp-master, or an operator-supplied
// SHOWMESH_TEST_FPP_URL), waits for the collector's first poll to land,
// and asserts GET /api/v1/fpp/{instanceId} — read as raw JSON, per
// contract section 1's standing rule, not decoded into the server's own
// v1 types — reports fpp.reachable=true, a "current" state (never
// not_collected, never collection_failed), and fpp.multisync.enabled
// matching the daemon's own independently-fetched current value. This is
// the chain finding 4.2 named: collector -> sink -> store -> apiwiring ->
// mapping -> JSON, together, against a real daemon, for the first time.
func TestFPPSuccessPathThroughRealCoordinator(t *testing.T) {
	requireBroker(t)
	url := requireLiveFPP(t)
	wantEnabled := fetchRealMultiSyncState(t, url)

	instanceID := "bench-fpp-" + uniqueSuffix()
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		fppEndpoints: instanceID + "=" + url,
	})

	var body []byte
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		var status int
		status, body = coord.getRaw(t, "/api/v1/fpp/"+instanceID)
		return status == http.StatusOK && strings.Contains(string(body), `"signal":"fpp.reachable"`) && !strings.Contains(string(body), `"state":"not_collected"`)
	}, "the FPP collector's first poll against the live bench FPP to land through the coordinator's own store and API")

	// Raw-key assertions against the literal bytes, not a re-decoded
	// v1.FPPInstance — contract section 1's standing rule, applied to the
	// one endpoint in this suite that had never been proven against a
	// reachable FPP at all.
	if strings.Contains(string(body), `"state":"collection_failed"`) {
		t.Fatalf("GET /api/v1/fpp/%s reports collection_failed against a live, reachable FPP; body: %s", instanceID, body)
	}

	var resp struct {
		Instance struct {
			Health        string  `json:"health"`
			LastPollError *string `json:"lastPollError"`
			Observations  []struct {
				Signal string `json:"signal"`
				Value  any    `json:"value"`
				State  string `json:"state"`
			} `json:"observations"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/fpp/%s: %v; body: %s", instanceID, err, body)
	}
	if resp.Instance.LastPollError != nil {
		t.Errorf("lastPollError = %v, want nil against a live, reachable FPP", *resp.Instance.LastPollError)
	}

	var sawReachable, sawMultiSync bool
	for _, o := range resp.Instance.Observations {
		switch o.Signal {
		case "fpp.reachable":
			sawReachable = true
			if o.Value != true {
				t.Errorf("fpp.reachable value = %v, want true", o.Value)
			}
			if o.State != "current" {
				t.Errorf("fpp.reachable state = %q, want current", o.State)
			}
		case "fpp.multisync.enabled":
			sawMultiSync = true
			if o.Value != wantEnabled {
				t.Errorf("fpp.multisync.enabled = %v, want %v (the real daemon's independently-fetched current status.multisync)", o.Value, wantEnabled)
			}
			if o.State != "current" {
				t.Errorf("fpp.multisync.enabled state = %q, want current", o.State)
			}
		}
	}
	if !sawReachable {
		t.Errorf("no fpp.reachable observation in the response at all; body: %s", body)
	}
	if !sawMultiSync {
		t.Errorf("no fpp.multisync.enabled observation in the response at all; body: %s", body)
	}

	t.Logf("live FPP at %s through coordinator %s: multisync.enabled=%v", url, coord.httpAddr, wantEnabled)
}
