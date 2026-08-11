//go:build integration

package integration

import (
	"strings"
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

	if view.LWT == nil {
		t.Fatalf("LWT = nil, want the broker-fired last-will record")
	}
	if view.LWT.Online {
		t.Errorf("LWT.Online = true, want false")
	}
	if !strings.Contains(view.LWT.Reason, "unexpected disconnect") {
		t.Errorf("LWT.Reason = %q, want it to name an unexpected disconnect (the registered Will's own reason text, see internal/agent/advertise.go's willDisconnectReason)", view.LWT.Reason)
	}
}
