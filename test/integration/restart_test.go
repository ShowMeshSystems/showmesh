//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// TestCoordinatorRestartRestoresInventoryFromRetainedTopics is BUILD-PLAN's
// Step 2 acceptance criterion 3: "the coordinator restores state from
// retained topics after its own restart". See the package doc comment for
// why "restart" here means tearing down and rebuilding
// internal/coordinator/{store,inventory,broker} against the same SQLite
// file and the same broker, rather than restarting the shipped binary.
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

	coord.shutdown() // teardown

	coord2 := startCoordinator(t, dataDir, clientID) // rebuild: same DB file, same broker, same client ID

	// The rebuilt coordinator's subscription is asynchronous (see
	// broker.NewBrokerManager's doc comment: it "begins connecting ... and
	// returns immediately"), so finding the node at all requires bounded
	// polling, not a single immediate check.
	var view inventory.NodeView
	waitFor(t, 15*time.Second, 50*time.Millisecond, func() bool {
		v, ok := coord2.findNode(t, nodeID)
		if !ok {
			return false
		}
		view = v
		return v.Hello != nil && v.LWT != nil
	}, "node "+nodeID+" hello and last-will to be restored from retained topics after coordinator rebuild")

	if view.Hello == nil {
		t.Fatalf("Hello = nil, want the retained hello restored")
	}
	if len(view.Hello.Capabilities) != 1 || string(view.Hello.Capabilities[0].ID) != "matrix.render" {
		t.Errorf("Capabilities = %+v, want [matrix.render] restored from the retained hello", view.Hello.Capabilities)
	}
	if view.LWT == nil || !view.LWT.Online {
		t.Errorf("LWT = %+v, want a retained online=true record", view.LWT)
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
// the Task E spec it matters more than any of them: a dead node's last
// heartbeat replaying into a fresh coordinator subscription must never read
// as healthy. This is the one test in this package that a broken
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
	for time.Now().Before(deadline) {
		v, ok := coord2.findNode(t, nodeID)
		if ok && v.Health != nil {
			sawHealthEvidence = true
			if v.Liveness == inventory.LivenessOnline {
				t.Fatalf(
					"Liveness = online for node %s, which was killed before this coordinator instance ever started: "+
						"a retained heartbeat replay was read as proof of life (Health = %+v)",
					nodeID, v.Health)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !sawHealthEvidence {
		t.Fatalf("never observed any health evidence for %s after the coordinator rebuild; the retained heartbeat was not delivered at all, so this test proved nothing about the freshness rule", nodeID)
	}

	view, ok := coord2.findNode(t, nodeID)
	if !ok {
		t.Fatalf("node %s missing from the rebuilt coordinator's inventory entirely", nodeID)
	}
	if view.Health == nil {
		t.Fatalf("Health = nil at the end of the window, want the retained health record still present")
	}
	if view.Health.ObservedAt != nil {
		t.Errorf("Health.ObservedAt = %v, want nil: a retained delivery's age is unknown and must never be stamped with a receipt time", *view.Health.ObservedAt)
	}
	if view.Liveness != inventory.LivenessOffline {
		t.Errorf("final Liveness = %q, want offline (from the retained last-will, which IS trustworthy regardless of the RETAIN flag)", view.Liveness)
	}
}
