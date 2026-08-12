//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file is BUILD-PLAN Step 7 seam B's own acceptance criterion 2,
// proven the way its text requires rather than only against a fake node
// lister: "Verified by declaring a node, stopping its agent, and
// re-running discovery against a real coordinator with a real agent
// subprocess." internal/coordinator/api's own
// TestDiscoveryRunNeverDeletesAnAbsentDeclaration proves the same rule at
// the handler level against a fake inventory; this is the one place the
// same rule is proven against a real MQTT-fed inventory.Manager, a real
// Last Will firing on the broker, and the real HTTP surface.

// postRaw issues an authenticated POST against path with body JSON-encoded
// (nil for no body — POST /api/v1/discovery/runs takes none), returning
// the status and raw response bytes. Deliberately NOT added to
// testCoordinator in harness_test.go itself: this file is the only
// caller so far, and harness_test.go is shared ground every seam's own
// integration tests build on.
func postRaw(t *testing.T, coord *testCoordinator, path string, body any) (status int, respBody []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body for %s: %v", path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(http.MethodPost, coord.url(path), reader)
	if err != nil {
		t.Fatalf("build POST request for %s: %v", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if coord.token != "" {
		req.Header.Set("Authorization", "Bearer "+coord.token)
	}
	resp, err := coord.client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("POST %s: read body: %v", path, err)
	}
	return resp.StatusCode, b
}

func TestDiscoveryNeverDeletesADeclaredNodeAcrossARealAgentStop(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	token := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "coord-" + uniqueSuffix(),
		bearerToken: token,
	})

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})
	waitOnline(t, coord, nodeID)

	// Declare it (POST /api/v1/nodes/{nodeId}/declaration, config:write).
	status, body := postRaw(t, coord, "/api/v1/nodes/"+nodeID+"/declaration", map[string]any{"label": "integration test node"})
	if status != http.StatusOK {
		t.Fatalf("declare status = %d, want 200; body: %s", status, body)
	}

	// Discovery run #1: the agent is up, so this must be counted as
	// observed — the declaration's discovery evidence becomes "present".
	status, body = postRaw(t, coord, "/api/v1/discovery/runs", nil)
	if status != http.StatusOK {
		t.Fatalf("discovery run 1 status = %d, want 200; body: %s", status, body)
	}
	var run1 v1.DiscoveryRunResponse
	if err := json.Unmarshal(body, &run1); err != nil {
		t.Fatalf("decode discovery run 1 response: %v; body: %s", err, body)
	}
	if !run1.Run.Complete {
		t.Fatalf("discovery run 1 did not complete: %+v", run1.Run)
	}

	node1, ok := coord.findNode(t, nodeID)
	if !ok {
		t.Fatalf("node %s not found after declaring and running discovery", nodeID)
	}
	if !node1.Declaration.Declared {
		t.Fatalf("declaration.declared = false right after a successful declare")
	}
	if node1.Declaration.DiscoveryState != "present" {
		t.Fatalf("declaration.discoveryState = %q, want \"present\" while the agent is online", node1.Declaration.DiscoveryState)
	}

	// Stop the agent for real (SIGKILL, the same unclean-kill path
	// kill_test.go's TestAgentUncleanKillGoesOffline uses) and wait for the
	// broker's own Last Will to flip this node's control-plane state to
	// offline through the real MQTT path — not a fake's field assignment.
	agent.sigkill(t)
	waitOffline(t, coord, nodeID)

	// Discovery run #2: the agent is down. This must NOT delete the
	// declaration — RES-008 D6's entire reason for existing — and must
	// flag it not_seen rather than silently leaving it looking present.
	status, body = postRaw(t, coord, "/api/v1/discovery/runs", nil)
	if status != http.StatusOK {
		t.Fatalf("discovery run 2 status = %d, want 200; body: %s", status, body)
	}
	var run2 v1.DiscoveryRunResponse
	if err := json.Unmarshal(body, &run2); err != nil {
		t.Fatalf("decode discovery run 2 response: %v; body: %s", err, body)
	}
	if !run2.Run.Complete {
		t.Fatalf("discovery run 2 did not complete: %+v", run2.Run)
	}

	node2, ok := coord.findNode(t, nodeID)
	if !ok {
		t.Fatalf("node %s disappeared from inventory entirely after its agent stopped — a discovery run must never delete a declaration", nodeID)
	}
	if !node2.Declaration.Declared {
		t.Fatalf("declaration.declared = false after the agent stopped and a second discovery run — a discovery run must never delete a declaration (RES-008 D6)")
	}
	if node2.Declaration.Label == nil || *node2.Declaration.Label != "integration test node" {
		t.Errorf("declaration.label = %v after the agent stopped, want it unchanged (\"integration test node\")", node2.Declaration.Label)
	}
	if node2.Declaration.DiscoveryState != "not_seen" {
		t.Errorf("declaration.discoveryState = %q, want \"not_seen\" once the agent is offline and a complete run did not observe it", node2.Declaration.DiscoveryState)
	}
}
