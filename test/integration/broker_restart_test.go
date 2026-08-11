//go:build integration

package integration

import (
	"testing"
	"time"
)

// TestBrokerRestartResubscribesAndObservesSubsequentChanges is not one of
// BUILD-PLAN's three acceptance criteria, but the Task E spec calls it out
// by name: the coordinator builder flagged that a subscription silently
// failing to re-establish after a broker restart looks fine right up until
// a node changes state and nobody notices. This restarts the actual
// Mosquitto container underneath a running coordinator and checks two
// independent things that only a genuinely re-established subscription
// (not merely a reconnected TCP session) can produce: a live heartbeat
// delivered strictly after the restart, and a subsequent Last Will still
// being observed.
//
// Requires SHOWMESH_TEST_MOSQUITTO_CONTAINER (set by `make
// test-integration`); skips with an explicit message otherwise — see
// restartBroker's doc comment and the Task E spec's "say so explicitly"
// requirement.
func TestBrokerRestartResubscribesAndObservesSubsequentChanges(t *testing.T) {
	requireBroker(t)

	dataDir := t.TempDir()
	coord := startCoordinator(t, dataDir, "coord-"+uniqueSuffix())

	nodeID := "agent-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})

	waitOnline(t, coord, nodeID)

	restartAt := time.Now()
	restartBroker(t) // skips t itself if the container name is unknown

	// Reconnecting the TCP session is necessary but not sufficient: it is
	// exactly the thing that could look fine while the subscription itself
	// silently failed to come back (subscribeAll is only invoked from
	// OnConnectionUp; if that callback's Subscribe call errors, it is
	// logged and NOT retried until the next reconnect — see
	// internal/coordinator/broker/broker.go's subscribeAll doc comment).
	waitFor(t, 30*time.Second, 200*time.Millisecond, func() bool {
		return coord.bm.State().Connected
	}, "coordinator's BrokerManager to report Connected again after the broker restart")

	// Prove the resubscription actually took effect by waiting for a LIVE
	// heartbeat whose ObservedAt is strictly after the restart. If the
	// subscription silently failed to re-establish, no further evidence of
	// any kind would ever reach inventory again, and this loop times out
	// instead of a staleness timeout papering over the gap.
	waitFor(t, 30*time.Second, 50*time.Millisecond, func() bool {
		v, ok := coord.findNode(t, nodeID)
		return ok && v.Health != nil && v.Health.ObservedAt != nil && v.Health.ObservedAt.After(restartAt)
	}, "coordinator to keep receiving live heartbeats after the broker restart (proves the observed/health subscription was re-established, not merely that the TCP connection came back)")

	// Extend the same proof to the LWT subscription specifically: kill the
	// agent and confirm the coordinator still observes the resulting Will
	// (see waitOffline's doc comment for why this waits for the specific
	// LivenessOffline verdict, not merely "not online").
	agent.sigkill(t)
	waitOffline(t, coord, nodeID)
}
