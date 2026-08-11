//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// assertRawHeartbeatIsUnknownAge decodes body (a raw GET
// /api/v1/nodes/{id} response) into a generic map[string]any — deliberately
// NOT into [v1.NodeResponse]/[v1.Node]/[v1.Evidence], per contract section
// 1's standing rule: "a test that marshals a handler response and
// unmarshals it back into the same struct proves nothing about the
// contract: rename a JSON tag and it still passes." Walking the raw keys
// here is what makes this assertion mean something a struct round-trip
// would not.
func assertRawHeartbeatIsUnknownAge(t *testing.T, body []byte) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw node response: %v; body: %s", err, body)
	}
	node, ok := raw["node"].(map[string]any)
	if !ok {
		t.Fatalf(`raw response has no "node" object; body: %s`, body)
	}
	evidence, ok := node["evidence"].(map[string]any)
	if !ok {
		t.Fatalf(`node has no "evidence" object; body: %s`, body)
	}
	heartbeat, ok := evidence["heartbeat"].(map[string]any)
	if !ok {
		t.Fatalf(`evidence has no "heartbeat" object; body: %s`, body)
	}

	observedAt, present := heartbeat["observedAt"]
	if !present {
		t.Errorf(`heartbeat has no "observedAt" key at all; want the key present with a literal JSON null. body: %s`, body)
	} else if observedAt != nil {
		t.Errorf(`heartbeat "observedAt" = %v (raw JSON), want literal null`, observedAt)
	}

	if state, _ := heartbeat["state"].(string); state != "unknown_age" {
		t.Errorf(`heartbeat "state" = %v, want "unknown_age"`, heartbeat["state"])
	}
}

// assertRawControlPlaneState is assertRawHeartbeatIsUnknownAge's sibling
// for node.controlPlane.state: Step 3 review finding 4.4 named this field
// specifically as a load-bearing case that, everywhere else in this
// package, was only ever asserted through [v1.Node]'s own decoded struct
// (view.ControlPlane.State) — exactly the struct-round-trip pattern
// contract section 1 warns proves nothing about a JSON tag rename. body is
// a raw GET /api/v1/nodes/{id} response.
func assertRawControlPlaneState(t *testing.T, body []byte, want string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode raw node response: %v; body: %s", err, body)
	}
	node, ok := raw["node"].(map[string]any)
	if !ok {
		t.Fatalf(`raw response has no "node" object; body: %s`, body)
	}
	cp, ok := node["controlPlane"].(map[string]any)
	if !ok {
		t.Fatalf(`node has no "controlPlane" object; body: %s`, body)
	}
	if got, _ := cp["state"].(string); got != want {
		t.Errorf(`controlPlane "state" = %v (raw JSON), want %q`, cp["state"], want)
	}
}

// TestCoordinatorRestartRestoresInventoryFromRetainedTopics is BUILD-PLAN's
// Step 2 acceptance criterion 3: "the coordinator restores state from
// retained topics after its own restart", and — since the Step 3 wiring
// pass — a real process restart: coord.shutdown() SIGTERMs the real
// showmesh-coordinator subprocess and coord2 execs a brand new one against
// the same SQLite file and the same broker (see startCoordinator's doc
// comment).
//
// The agent in this test is never killed or restarted: it stays connected
// throughout. That is deliberate — it means hello and the online Last Will
// can ONLY reach the rebuilt coordinator via a retained-store replay on the
// fresh subscription (the agent has no reason to republish either; it only
// does that on its own reconnect), so seeing them present immediately after
// rebuild is unambiguous proof of retained-topic restoration, not a race
// against the agent happening to republish something itself.
func TestCoordinatorRestartRestoresInventoryFromRetainedTopics(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	clientID := "coord-restart-" + uniqueSuffix()
	coord := startCoordinator(t, dataDir, clientID)

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID, capabilities: "matrix.render"})
	defer agent.stopIfRunning()

	waitOnline(t, coord, nodeID)

	coord.shutdown() // teardown: SIGTERM the real subprocess and wait for exit

	coord2 := startCoordinator(t, dataDir, clientID) // rebuild: same DB file, same broker, same client ID, a brand new process

	// The rebuilt coordinator's subscription is asynchronous (see
	// broker.NewBrokerManager's doc comment: it "begins connecting ... and
	// returns immediately"), so finding the node at all requires bounded
	// polling, not a single immediate check.
	var view v1.Node
	waitFor(t, 15*time.Second, 50*time.Millisecond, func() bool {
		v, ok := coord2.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.Evidence.Hello.State != "not_collected" && v.Evidence.LastWill.State != "not_collected"
	}, "node "+nodeID+" hello and last-will to be restored from retained topics after coordinator rebuild")

	if view.Evidence.Hello.State == "not_collected" {
		t.Fatalf("hello evidence state = not_collected, want the retained hello restored")
	}
	if len(view.Capabilities) != 1 || view.Capabilities[0].ID != "matrix.render" {
		t.Errorf("Capabilities = %+v, want [matrix.render] restored from the retained hello", view.Capabilities)
	}
	if view.Evidence.LastWill.State == "not_collected" {
		t.Fatalf("lastWill evidence state = not_collected, want the retained online=true record")
	}
	if online, ok := view.Evidence.LastWill.Value.(bool); !ok || !online {
		t.Errorf("lastWill evidence value = %v (ok=%v), want true", view.Evidence.LastWill.Value, ok)
	}

	// And it resolves back to a confident online verdict once the
	// still-connected agent's next live heartbeat lands on the fresh
	// subscription — "unknown after restart is correct, not a defect" per
	// the shared contract, and it must resolve on its own within roughly one
	// heartbeat interval without any further action.
	waitOnline(t, coord2, nodeID)
}

// TestRetainedHeartbeatReplayNeverReadsHealthy is not one of BUILD-PLAN's
// three acceptance criteria, but per the Step 2 round 2 shared contract and
// the Step 3 contract's section 3.3 it matters more than any of them: a
// dead node's last heartbeat replaying into a fresh coordinator subscription
// must never read as healthy — and, per Step 3's own addition, must render
// on the wire as observedAt: null and state: "unknown_age", not as a
// fabricated freshness. This is the one test in this package that a broken
// implementation could pass by accident if it only checked liveness once at
// the wrong moment, so the assertion loop below polls continuously across a
// window sized to outlast the staleness window rather than sampling a
// single point in time — see the loop's own comment for why.
func TestRetainedHeartbeatReplayNeverReadsHealthy(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	clientID := "coord-freshness-" + uniqueSuffix()
	coord := startCoordinator(t, dataDir, clientID)

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})

	waitOnline(t, coord, nodeID)

	// Kill it uncleanly: the broker fires its Will (online:false) AND its
	// last heartbeat stays retained on the broker exactly as last
	// published — nothing republishes either topic again after this.
	agent.sigkill(t)
	waitOffline(t, coord, nodeID)

	coord.shutdown()

	// Rebuild against a FRESH database — deliberately NOT the same dataDir
	// as coord above, unlike the criterion 3 test. This is the one place in
	// this package that diverges from "same database and broker" on
	// purpose: coord already recorded this node's single live heartbeat
	// (boot ID + sequence 0) before the kill, and internal/coordinator/store's
	// RecordHealth treats a same-boot-ID, same-or-lower-sequence delivery as
	// a duplicate and silently ignores it (correct anti-replay behavior for
	// QoS 1 redelivery — see the shared contract's "Boot ID and sequence"
	// section). Reusing that database here would make the broker's retained
	// replay of that exact same heartbeat a no-op against already-stored
	// evidence, which still happens to end up safe (the pre-existing
	// ObservedAt is real and ages out normally) but would prove nothing
	// about classify()'s handling of a retained delivery on the write path
	// itself — the actual mechanism this test exists to catch a regression
	// in. A fresh database has no prior evidence to collide with, so the
	// retained heartbeat's arrival is a genuine first INSERT, and
	// ObservedAt is asserted nil below with nothing to obscure it.
	coord2 := startCoordinator(t, t.TempDir(), clientID)

	// Poll continuously across a window that outlasts testStalenessWindow:
	// if the retained-freshness bug were present, liveness would read
	// online starting the instant the retained heartbeat is delivered and
	// would only stop reading online once that stamped-as-fresh evidence
	// itself aged past the staleness window — so a single snapshot taken
	// either too early (before delivery) or too late (after the bug's own
	// window expired) could miss it entirely. A single point-in-time check
	// would not be doing this test's job; see the package doc comment and
	// the Task E spec's framing of exactly this failure mode.
	deadline := time.Now().Add(3 * testStalenessWindow)
	sawHealthEvidence := false
	var rawBodyAtFirstSighting []byte
	for time.Now().Before(deadline) {
		status, body := coord2.getRaw(t, "/api/v1/nodes/"+nodeID)
		if status == http.StatusOK {
			v, ok := coord2.findNode(t, nodeID)
			if ok && v.Evidence.Heartbeat.State != "not_collected" {
				if !sawHealthEvidence {
					rawBodyAtFirstSighting = body
				}
				sawHealthEvidence = true
				if v.ControlPlane.State == "online" {
					t.Fatalf(
						"controlPlane.state = online for node %s, which was killed before this coordinator instance ever started: "+
							"a retained heartbeat replay was read as proof of life (heartbeat evidence = %+v)",
						nodeID, v.Evidence.Heartbeat)
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !sawHealthEvidence {
		t.Fatalf("never observed any heartbeat evidence for %s after the coordinator rebuild; the retained heartbeat was not delivered at all, so this test proved nothing about the freshness rule", nodeID)
	}

	// Contract section 1's standing rule, applied directly: assert the raw
	// JSON bytes captured at first sighting, not a re-decoded struct field —
	// a test that only checked view.Evidence.Heartbeat.ObservedAt == nil via
	// the same v1.Evidence struct the server uses to marshal it would pass
	// whether or not a future "helpful" refactor renamed observedAt's JSON
	// tag or started defaulting it to collectedAt with the wrong tag; a raw
	// substring/key check on the literal bytes would not.
	assertRawHeartbeatIsUnknownAge(t, rawBodyAtFirstSighting)

	view, ok := coord2.findNode(t, nodeID)
	if !ok {
		t.Fatalf("node %s missing from the rebuilt coordinator's inventory entirely", nodeID)
	}
	if view.Evidence.Heartbeat.State == "not_collected" {
		t.Fatalf("heartbeat evidence state = not_collected at the end of the window, want the retained health record still present")
	}
	if view.Evidence.Heartbeat.ObservedAt != nil {
		t.Errorf("heartbeat observedAt = %v, want nil: a retained delivery's age is unknown and must never be stamped with a receipt time", *view.Evidence.Heartbeat.ObservedAt)
	}
	if view.Evidence.Heartbeat.State != "unknown_age" {
		t.Errorf("heartbeat state = %q, want \"unknown_age\"", view.Evidence.Heartbeat.State)
	}
	if view.ControlPlane.State != "offline" {
		t.Errorf("final controlPlane.state = %q, want \"offline\" (from the retained last-will, which IS trustworthy regardless of the RETAIN flag)", view.ControlPlane.State)
	}
}
