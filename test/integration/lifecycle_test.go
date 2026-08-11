//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// waitOnline polls coord for nodeID to reach controlPlane.state "online",
// bounded generously relative to the (possibly heavily compressed)
// testHeartbeatInterval: per a review finding surfaced while this suite was
// being built, a freshly connected agent publishes hello and its online
// Last Will immediately but no heartbeat until its ticker first fires a
// full HeartbeatInterval later, so a node legitimately reads `unknown` for
// up to one interval after connecting even though the coordinator already
// has live presence evidence. 10 intervals (or 10s, whichever is larger)
// leaves ample room for that plus MQTT round-trip, broker startup latency,
// and (post-Step-3) an HTTP round trip to the coordinator subprocess,
// without hiding an actual liveness bug behind a timeout that is simply
// too generous.
func waitOnline(t *testing.T, coord *testCoordinator, nodeID string) v1.Node {
	t.Helper()
	bound := 10 * testHeartbeatInterval
	if bound < 10*time.Second {
		bound = 10 * time.Second
	}
	var view v1.Node
	waitFor(t, bound, 50*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.ControlPlane.State == "online"
	}, "node "+nodeID+" to reach controlPlane.state \"online\"")
	return view
}

// TestAgentAppearsInInventoryWithCapabilities is BUILD-PLAN's Step 2
// acceptance criterion 1: "an agent appears in coordinator inventory after
// start", exercised against a real showmesh-agent subprocess, a real
// showmesh-coordinator subprocess, and a real broker so the assertion
// actually depends on the wire behavior (MQTT CONNECT, retained hello
// publish, subscription delivery, and — post-Step-3 — the coordinator's own
// /api/v1 rendering of all of it) rather than on anything a fake stood in
// for.
//
// CAPABILITY ATTRIBUTES: the Task E spec asks that "advertised capabilities
// arrive intact including attributes". This test verifies capability ID and
// Version arrive intact through the real agent, a real broker, and the
// real coordinator's API — the part no unit test can prove. It
// deliberately does NOT exercise capability.Capability.Attributes
// end-to-end through the real agent subprocess, because there is currently
// no way to do so: the only capability-advertisement mechanism this agent
// has is SHOWMESH_NODE_CAPABILITIES (internal/agent/config.parseCapabilities),
// and that env var's format is comma-separated "id" or "id:version" pairs
// with no syntax for attributes at all — its own doc comment records this
// as deliberate ("acceptable because this variable exists for
// testing/override, not as a real capability-declaration mechanism").
// Attributes round-tripping through JSON storage and retrieval IS already
// covered — at L1, by unit tests that do not need a real broker to prove it
// — by pkg/capability/capability_test.go and
// internal/coordinator/store/store_test.go's
// TestCapabilitySetRoundTripsWithAttributes.
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

	if view.Label == nil {
		t.Fatalf("Label = nil, want a stored capability advertisement's label")
	}
	if *view.Label != "integration test node" {
		t.Errorf("Label = %q, want %q", *view.Label, "integration test node")
	}

	wantVersions := map[string]int{"matrix.render": 2, "display.hdmi": 1}
	if len(view.Capabilities) != len(wantVersions) {
		t.Fatalf("Capabilities = %+v, want %d entries", view.Capabilities, len(wantVersions))
	}
	for _, c := range view.Capabilities {
		want, ok := wantVersions[c.ID]
		if !ok {
			t.Errorf("unexpected capability %q in advertisement", c.ID)
			continue
		}
		if c.Version != want {
			t.Errorf("capability %q Version = %d, want %d", c.ID, c.Version, want)
		}
	}

	if view.Evidence.LastWill.State != "current" {
		t.Errorf("lastWill evidence state = %q, want \"current\"", view.Evidence.LastWill.State)
	}
	online, ok := view.Evidence.LastWill.Value.(bool)
	if !ok || !online {
		t.Errorf("lastWill evidence value = %v, want true", view.Evidence.LastWill.Value)
	}
}

// waitOffline polls coord for nodeID to reach controlPlane.state "offline"
// (not merely "stop being online") and returns its view.
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
//  2. A transient "unknown" disagreement window even once the Will lands.
//     Per internal/coordinator/inventory/liveness.go's deriveLiveness, an
//     offline last-will observed while a LIVE heartbeat is still within
//     StalenessWindow reads as unknown, not offline, until that heartbeat
//     evidence itself ages past the window — by design, so a momentary
//     out-of-order arrival is never reported as a confident offline. A node
//     killed shortly after publishing a heartbeat visibly passes through
//     this state before settling on offline. Waiting for merely "not
//     online" (which this package's first draft did) stops inside that
//     window and is not the same assertion as "offline" — that was the
//     actual cause of this package's first observed failures against a
//     real broker, not a liveness bug.
func waitOffline(t *testing.T, coord *testCoordinator, nodeID string) v1.Node {
	t.Helper()
	bound := 45*time.Second + testStalenessWindow
	var view v1.Node
	waitFor(t, bound, 50*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.ControlPlane.State == "offline"
	}, "node "+nodeID+" to settle on controlPlane.state \"offline\" (see waitOffline's doc comment for why this is bounded past both Will-delivery latency and the disagreement window, not just message latency)")
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
	var view v1.Node
	waitFor(t, bound, 25*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.ControlPlane.State == "offline"
	}, "node to go offline within "+bound.String()+" of a clean SIGTERM shutdown (well inside the "+testStalenessWindow.String()+" staleness window, per the shared contract's clean-shutdown ordering requirement)")

	online, ok := view.Evidence.LastWill.Value.(bool)
	if !ok || online {
		t.Fatalf("lastWill evidence value = %v (ok=%v), want false", view.Evidence.LastWill.Value, ok)
	}
	// The wire API has no path today for the raw LWT disconnect-reason text
	// ("clean shutdown" vs. "unexpected disconnect ...", stored in
	// store.LWTRecord.Reason) to reach a client: mapping.go's
	// lastWillObservation renders only the Online bool as the evidence
	// Value, and [v1.ControlPlane.Reason] carries inventory's own derived
	// LivenessReason, not the agent's disconnect-reason string. See this
	// task's report — flagged as a genuine gap, not fixed here, because
	// closing it touches contract-pinned shapes and existing, separately
	// tested inventory behavior. This test therefore checks what the API
	// actually exposes: the boolean flipped promptly, and *some*
	// human-readable reason accompanies the non-online state.
	if view.ControlPlane.Reason == nil || strings.TrimSpace(*view.ControlPlane.Reason) == "" {
		t.Errorf("controlPlane.reason = %v, want a non-empty explanation once state is not online", view.ControlPlane.Reason)
	}
}
