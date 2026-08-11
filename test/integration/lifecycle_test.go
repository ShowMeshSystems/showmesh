//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// waitOnline polls coord for nodeID to reach LivenessOnline, bounded
// generously relative to the (possibly heavily compressed)
// testHeartbeatInterval: per a review finding surfaced while this suite was
// being built, a freshly connected agent publishes hello and its online
// Last Will immediately but no heartbeat until its ticker first fires a
// full HeartbeatInterval later, so a node legitimately reads `unknown` for
// up to one interval after connecting even though the coordinator already
// has live presence evidence. 10 intervals (or 10s, whichever is larger)
// leaves ample room for that plus MQTT round-trip and broker startup
// latency without hiding an actual liveness bug behind a timeout that is
// simply too generous.
func waitOnline(t *testing.T, coord *testCoordinator, nodeID string) inventory.NodeView {
	t.Helper()
	bound := 10 * testHeartbeatInterval
	if bound < 10*time.Second {
		bound = 10 * time.Second
	}
	var view inventory.NodeView
	waitFor(t, bound, 50*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.Liveness == inventory.LivenessOnline
	}, "node "+nodeID+" to reach LivenessOnline")
	return view
}

// TestAgentAppearsInInventoryWithCapabilities is BUILD-PLAN's Step 2
// acceptance criterion 1: "an agent appears in coordinator inventory after
// start", exercised against a real showmesh-agent subprocess and a real
// broker so the assertion actually depends on the wire behavior (MQTT
// CONNECT, retained hello publish, subscription delivery) rather than on
// anything a fake stood in for.
//
// CAPABILITY ATTRIBUTES: the Task E spec asks that "advertised capabilities
// arrive intact including attributes". This test verifies capability ID and
// Version arrive intact through the real agent and a real broker — the part
// no unit test can prove. It deliberately does NOT exercise
// capability.Capability.Attributes end-to-end through the real agent
// subprocess, because there is currently no way to do so: the only
// capability-advertisement mechanism this Step 2 agent has is
// SHOWMESH_NODE_CAPABILITIES (internal/agent/config.parseCapabilities),
// and that env var's format is comma-separated "id" or "id:version" pairs
// with no syntax for attributes at all — its own doc comment records this
// as deliberate ("acceptable because this variable exists for
// testing/override, not as a real capability-declaration mechanism").
// There is no real capability detection to advertise attributes from yet
// (Step 2 Task D's agent has no GStreamer, no media, nothing to introspect).
// Attributes round-tripping through JSON storage and retrieval IS already
// covered — at L1, by unit tests that do not need a real broker to prove it
// — by pkg/capability/capability_test.go and
// internal/coordinator/store/store_test.go's
// TestCapabilitySetRoundTripsWithAttributes. See this task's final report
// for why extending parseCapabilities to accept attributes was judged out
// of scope (broader than the "narrowly scoped test-support configuration"
// the spec allows) rather than done here.
func TestAgentAppearsInInventoryWithCapabilities(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{
		nodeID:       nodeID,
		label:        "integration test node",
		capabilities: "matrix.render:2,display.hdmi",
	})

	view := waitOnline(t, coord, nodeID)

	if view.Hello == nil {
		t.Fatalf("Hello = nil, want a stored capability advertisement")
	}
	if view.Hello.Label != "integration test node" {
		t.Errorf("Hello.Label = %q, want %q", view.Hello.Label, "integration test node")
	}

	wantVersions := map[string]int{"matrix.render": 2, "display.hdmi": 1}
	if len(view.Hello.Capabilities) != len(wantVersions) {
		t.Fatalf("Capabilities = %+v, want %d entries", view.Hello.Capabilities, len(wantVersions))
	}
	for _, c := range view.Hello.Capabilities {
		want, ok := wantVersions[string(c.ID)]
		if !ok {
			t.Errorf("unexpected capability %q in advertisement", c.ID)
			continue
		}
		if c.Version != want {
			t.Errorf("capability %q Version = %d, want %d", c.ID, c.Version, want)
		}
	}

	if view.LWT == nil || !view.LWT.Online {
		t.Errorf("LWT = %+v, want an online=true record", view.LWT)
	}
}

// waitOffline polls coord for nodeID to reach LivenessOffline (not merely
// "stop being online") and returns its view.
//
// This is bounded to cover two independent delays discovered empirically
// while building this harness against a real broker, not just ordinary
// message latency:
//
//  1. The broker's own detection of a dead TCP connection. A SIGKILL'd
//     process usually has its socket torn down by the OS almost
//     immediately, but the worst case is bounded by
//     internal/agent/mqtt.go's keepAliveSeconds (a fixed 30s, NOT affected
//     by the heartbeat-interval override this suite uses to compress
//     everything else): MQTT brokers conventionally allow up to ~1.5x a
//     negotiated keepalive before deciding a session is dead and firing its
//     Will.
//  2. A transient LivenessUnknown "disagreement" window even once the Will
//     lands. Per internal/coordinator/inventory/liveness.go's
//     deriveLiveness, an offline last-will observed while a LIVE heartbeat
//     is still within StalenessWindow reads as unknown, not offline, until
//     that heartbeat evidence itself ages past the window — by design, so a
//     momentary out-of-order arrival is never reported as a confident
//     offline. A node killed shortly after publishing a heartbeat visibly
//     passes through this state before settling on offline. Waiting for
//     merely "not online" (which this package's first draft did) stops
//     inside that window and is not the same assertion as "offline" — that
//     was the actual cause of this package's first observed failures
//     against a real broker, not a liveness bug.
func waitOffline(t *testing.T, coord *testCoordinator, nodeID string) inventory.NodeView {
	t.Helper()
	bound := 45*time.Second + testStalenessWindow
	var view inventory.NodeView
	waitFor(t, bound, 50*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.Liveness == inventory.LivenessOffline
	}, "node "+nodeID+" to settle on LivenessOffline (see waitOffline's doc comment for why this is bounded past both Will-delivery latency and the disagreement window, not just message latency)")
	return view
}

// promptBound returns a duration a healthy clean-shutdown response must
// land comfortably inside, strictly less than testStalenessWindow so a pass
// cannot be explained by "the staleness window merely elapsed" rather than
// "the agent published its own offline state". See
// TestAgentCleanShutdownGoesOfflinePromptly.
func promptBound() time.Duration {
	b := 2 * testHeartbeatInterval
	if b >= testStalenessWindow {
		b = testStalenessWindow / 2
	}
	if b <= 0 {
		b = 500 * time.Millisecond
	}
	return b
}

// TestAgentCleanShutdownGoesOfflinePromptly asserts the shared contract's
// clean-shutdown ordering requirement end to end: an MQTT DISCONNECT with a
// normal reason code makes the broker discard the registered Will, so a
// cleanly stopping agent must publish its own retained "online: false"
// before disconnecting, or the coordinator has no way to learn the node is
// gone short of the full heartbeat-staleness window.
//
// KNOWN FAILING: as of this test being written, this assertion fails
// against internal/agent.Run. A review during this task found that Run
// passes the SIGTERM-derived signal context straight into newMQTTConn, so
// canceling that context on shutdown tears down autopaho's connection
// immediately — sending a normal DISCONNECT and discarding the Will —
// before shutdownCleanly ever gets a chance to publish the offline message
// it builds afterward. The retained LWT topic is left saying "online: true"
// with nothing running: exactly the failure this test (and the shared
// contract's clean-shutdown section) exists to catch. Per instruction, this
// is being left failing and unfixed here; a separate fix pass changes
// Run's connection lifetime. Do not weaken this assertion to make it pass.
func TestAgentCleanShutdownGoesOfflinePromptly(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})

	waitOnline(t, coord, nodeID)

	agent.sigterm(t)
	agent.waitForExit(t, 10*time.Second)

	bound := promptBound()
	var view inventory.NodeView
	waitFor(t, bound, 25*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.Liveness == inventory.LivenessOffline
	}, "node to go offline within "+bound.String()+" of a clean SIGTERM shutdown (well inside the "+testStalenessWindow.String()+" staleness window, per the shared contract's clean-shutdown ordering requirement)")

	if view.LWT == nil || view.LWT.Online {
		t.Fatalf("LWT = %+v, want an offline record", view.LWT)
	}
	if !strings.Contains(view.LWT.Reason, "clean shutdown") {
		t.Errorf("LWT.Reason = %q, want it to name a clean shutdown (published by the agent itself), not the registered Will's reason", view.LWT.Reason)
	}
}
