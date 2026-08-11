//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// This file scrutinizes the Step 3 review's finding 3.4 fix, per finding
// 4.12: that fix wraps the node lister (apiwiring.go's
// livenessObservingNodeLister) so every Snapshot call feeds liveness back
// into inventory.Manager's transition-recording path, closing the gap
// where a node whose heartbeats simply stopped — no further message, no
// last will — never got its offline transition into event history. That
// makes a read endpoint cause a write, which is a smell, chosen because it
// guarantees the rendered state and the event history can never disagree.
//
// Its correctness rests on two properties nobody had checked:
//
//  1. A staleness-driven transition is recorded even if no client ever
//     calls the API — the SSE hub's own render tick must be sufficient on
//     its own. This is the load-bearing test: if it is not sufficient,
//     the event history depends on somebody happening to look, which is
//     worse than the gap the fix was closing.
//  2. Concurrent readers do not double-record: several simultaneous GET
//     /api/v1/nodes requests across one transition must produce exactly
//     one event, not one per reader.

// TestStalenessDrivenTransitionRecordedByHubTickAloneNoAPICallEver is
// property 1, the load-bearing one.
//
// It brings a real agent online the ordinary, message-driven way (which
// is itself the one node-related API read this test needs, to establish
// the "was online" baseline in inventory.Manager's own per-process
// lastLiveness map — see that field's doc comment: the FIRST liveness
// observation for a node never itself produces an event, so there must be
// a prior baseline for a later change to be detected against). It then
// freezes the agent process with SIGSTOP rather than killing it: the TCP
// connection to the broker stays fully open (no FIN, no RST), so the
// broker has nothing to fire a Last Will over, and no further message of
// any kind ever arrives for this node again. From that point until the
// final check, this test issues no request to any endpoint that touches
// api.Dependencies.Nodes (no GET /api/v1/nodes, no GET
// /api/v1/nodes/{id}, no GET /api/v1/observations with a node-inclusive
// filter) — only GET /api/v1/events, which internal/coordinator/api's
// handleEvents never routes through Nodes.Snapshot at all. So the ONLY
// thing in the entire window that can have caused the coordinator to
// notice this node go stale is the SSE hub's own background render tick
// (api's default 5s StreamTickInterval, which calls Nodes.Snapshot on
// every tick regardless of subscriber count — see stream.go's render).
func TestStalenessDrivenTransitionRecordedByHubTickAloneNoAPICallEver(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	nodeID := "freeze-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})
	waitOnline(t, coord, nodeID) // establishes the "was online" baseline via real messages

	agent.sigstop(t)
	defer agent.sigcont(t) // resume it so ordinary cleanup can terminate it cleanly

	// testStalenessWindow (compressed under this suite; see
	// harness_test.go) must elapse, and then at least one hub render tick
	// (fixed at 5s in production, not overridable) must land after that —
	// sized with real margin so this is not a race against the tick's own
	// timer phase.
	time.Sleep(testStalenessWindow + 8*time.Second)

	status, body := coord.getRaw(t, "/api/v1/events") // does not touch Nodes.Snapshot
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/events: status %d, body: %s", status, body)
	}

	events := controlPlaneEventsFor(t, body, nodeID)
	// waitOnline's own message-driven online transition already recorded
	// one control_plane event (the "was online" baseline this test's own
	// doc comment names) — that event existing is expected and proves
	// nothing about the staleness path this test actually cares about. A
	// first, decoy version of this test checked only "at least one
	// control_plane event exists for this node", which that baseline event
	// alone always satisfies regardless of whether the hub's tick ever did
	// anything at all — confirmed by mutation: disabling the hub's own
	// tick-triggered render left that version green. The real assertion
	// needs a SECOND event whose recorded liveness differs from "online".
	if len(events) < 2 {
		t.Fatalf("only %d control_plane event(s) recorded for node %s (want 2: the online baseline plus a later staleness-driven change) — "+
			"the SSE hub's own render tick must be sufficient on its own to detect and record the second transition, and this test called no "+
			"endpoint besides GET /api/v1/events (which never touches Nodes.Snapshot) after freezing the agent; body: %s", len(events), nodeID, body)
	}
	last := events[len(events)-1]
	if last.Details["to"] == "online" {
		t.Fatalf("the only events recorded for node %s still end on \"online\" (%+v) — no staleness-driven transition away from online was ever recorded; body: %s", nodeID, events, body)
	}
}

// rawControlPlaneEvent is one element of GET /api/v1/events, decoded only
// as far as this file's tests need: which resource, what category, and
// the from/to pair inventory.go's observeLiveness stamps into Details.
type rawControlPlaneEvent struct {
	Resource struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"resource"`
	Category string            `json:"category"`
	Details  map[string]string `json:"details"`
}

// controlPlaneEventsFor decodes body (a raw GET /api/v1/events response)
// and returns every "control_plane" category event for nodeID, in the
// order the response carries them (ascending by seq — see
// TestAPIEventsOrderedAscendingBySeq in ordering_test.go).
func controlPlaneEventsFor(t *testing.T, body []byte, nodeID string) []rawControlPlaneEvent {
	t.Helper()
	var resp struct {
		Events []rawControlPlaneEvent `json:"events"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/events: %v; body: %s", err, body)
	}
	var out []rawControlPlaneEvent
	for _, e := range resp.Events {
		if e.Resource.Kind == "node" && e.Resource.ID == nodeID && e.Category == "control_plane" {
			out = append(out, e)
		}
	}
	return out
}

// TestConcurrentReadersDoNotDoubleRecordATransition is property 2: several
// simultaneous GET /api/v1/nodes requests, all racing to observe the same
// staleness-driven transition via livenessObservingNodeLister.Snapshot,
// must still produce exactly one recorded event — inventory.Manager's
// lastLiveness bookkeeping (observeLiveness) is the thing that has to be
// safe under concurrent callers for this to hold, since every one of the
// N concurrent readers below computes the same new Liveness independently
// and feeds it back.
//
// This test again freezes the agent with SIGSTOP (see the previous test's
// doc comment for why that — not a kill — is the honest way to produce
// "heartbeats simply stopped, no last will"), but this time deliberately
// fires a burst of concurrent GET /api/v1/nodes requests right as
// staleness should be tipping over, instead of relying on the hub's own
// serialized tick, specifically to create the race the fix's own
// correctness depends on not losing.
func TestConcurrentReadersDoNotDoubleRecordATransition(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	nodeID := "race-" + uniqueSuffix()
	agent := startAgent(t, agentConfig{nodeID: nodeID})
	waitOnline(t, coord, nodeID)

	agent.sigstop(t)
	defer agent.sigcont(t)

	// Wait until staleness has just set in, then hammer /api/v1/nodes with
	// many concurrent readers at once — every one of them is a genuine
	// Nodes.Snapshot call via livenessObservingNodeLister, all racing to
	// be the one that records the transition.
	time.Sleep(testStalenessWindow + 3*time.Second)

	const concurrentReaders = 25
	var wg sync.WaitGroup
	wg.Add(concurrentReaders)
	for i := 0; i < concurrentReaders; i++ {
		go func() {
			defer wg.Done()
			coord.getRaw(t, "/api/v1/nodes")
		}()
	}
	wg.Wait()

	// Give the store a brief moment to settle (AppendEvent calls triggered
	// by the burst above are not necessarily synchronously visible the
	// instant getRaw's own HTTP response returned), then read events once.
	time.Sleep(500 * time.Millisecond)

	status, body := coord.getRaw(t, "/api/v1/events")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/events: status %d, body: %s", status, body)
	}

	// Exactly 2, not 1: waitOnline's own message-driven online transition
	// is one event on its own (the baseline), and the staleness-driven
	// transition the burst above raced to observe is the second. A first,
	// decoy version of this test asserted "exactly 1 total", which is
	// simply wrong given the baseline event always exists — that version
	// could not have told a correctly-deduplicated single record apart
	// from the staleness transition never having been recorded at all,
	// since both cases equally satisfy "exactly 1". If the fix
	// double-recorded, this list would have more than 2 entries (one per
	// racing reader that won); if the tick/notify path never recorded the
	// staleness transition at all, this list would still show only 1.
	events := controlPlaneEventsFor(t, body, nodeID)
	if len(events) != 2 {
		t.Fatalf("control_plane events recorded for node %s = %d, want exactly 2 (the online baseline, then exactly one staleness-driven change, "+
			"even though %d readers raced to observe it concurrently); body: %s", nodeID, len(events), concurrentReaders, body)
	}
	if events[0].Details["to"] != "online" {
		t.Errorf("first recorded event for %s has to=%q, want \"online\" (the baseline established by waitOnline); events: %+v", nodeID, events[0].Details["to"], events)
	}
	if events[1].Details["to"] == "online" {
		t.Errorf("second recorded event for %s still has to=\"online\"; want the staleness-driven change away from it; events: %+v", nodeID, events)
	}
}
