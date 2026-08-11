//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// TestAgentUncleanKillGoesOffline is BUILD-PLAN's Step 2 acceptance
// criterion 2: "the agent disappears into unknown after an unclean kill,
// via Last Will". This can only be honestly proven with a real subprocess
// and a real broker — see the package doc comment. SIGKILL gives the
// process no chance to run any of its own shutdown code (no DISCONNECT, no
// offline publish), which is exactly what makes it the one signal that
// forces the broker itself to fire the client's registered Will: the will
// is registered on the MQTT CONNECT packet, and only the broker knows to
// fire it once the TCP connection simply vanishes.
func TestAgentUncleanKillGoesOffline(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})

	waitOnline(t, coord, nodeID)

	agent.sigkill(t)

	view := waitOffline(t, coord, nodeID)

	// Contract section 3.2: this test's own name and BUILD-PLAN's criterion
	// both say "offline", and the field the wire contract pins for exactly
	// this fact is node.controlPlane.state — never node.state/node.online —
	// asserted here on the literal field path, not merely inferred from
	// waitOffline having returned.
	if view.ControlPlane.State != "offline" {
		t.Fatalf("controlPlane.state = %q, want \"offline\"", view.ControlPlane.State)
	}
	// Step 3 review finding 4.4: also assert this directly against the raw
	// wire bytes, not only through v1.Node's decoded struct above — see
	// assertRawControlPlaneState's doc comment in restart_test.go.
	if status, body := coord.getRaw(t, "/api/v1/nodes/"+nodeID); status != http.StatusOK {
		t.Fatalf("GET /api/v1/nodes/%s: status %d, body: %s", nodeID, status, body)
	} else {
		assertRawControlPlaneState(t, body, "offline")
	}
	online, ok := view.Evidence.LastWill.Value.(bool)
	if !ok || online {
		t.Errorf("lastWill evidence value = %v (ok=%v), want false (the broker-fired last-will record)", view.Evidence.LastWill.Value, ok)
	}
	// See lifecycle_test.go's TestAgentCleanShutdownGoesOfflinePromptly for
	// why the raw disconnect-reason text ("unexpected disconnect...") is
	// not asserted here: it has no path to the wire API today. This test
	// checks the boolean and the state field the contract actually pins.
}
