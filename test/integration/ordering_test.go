//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"
)

// This file proves Step 3 review finding 4.10: the Group 2 builder
// documented array ordering guarantees in api/openapi.yaml (nodes by node
// ID, fpp.instances/collectors in configuration order, observations by
// resource then signal, events ascending by seq) after reading the store
// and wiring code — internal/coordinator/store/queries.go's `ORDER BY
// n.node_id`, internal/coordinator/store/observations.go's `ORDER BY
// resource_kind, resource_id, signal`, internal/coordinator/store/events.go's
// `ORDER BY seq ASC`, and internal/coordinator/apiwiring.go's iteration of
// cfg.FPPEndpoints in the order config.LoadConfig parsed them — but could
// not test any of it from the files that builder owned. Those orderings
// are now published guarantees the API reference makes to every client,
// and the SSE hub's own byte-diff change detection (contract section 6.5)
// silently depends on them being stable: a reordering with no content
// change would otherwise register as a change on every render tick.
//
// Every test below deliberately arranges its own inputs (node IDs, FPP
// instance IDs) so that insertion order, alphabetical order, and
// configuration order all disagree — an assertion that happened to match
// by coincidence (e.g. because insertion order and the real order are the
// same for inputs given in sorted order already) would not actually prove
// anything.

// TestAPINodesOrderedByNodeID starts three real agents in an order that is
// neither alphabetical nor its own reverse, then asserts GET
// /api/v1/nodes — read as raw JSON into a minimal ad hoc struct, not the
// server's own v1 types, so this is a genuine wire-order assertion rather
// than a struct round trip — returns them sorted by nodeId.
func TestAPINodesOrderedByNodeID(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	suffix := uniqueSuffix()
	nodeC := "zzz-node-" + suffix
	nodeA := "aaa-node-" + suffix
	nodeB := "mmm-node-" + suffix

	// Started deliberately out of alphabetical order (C, A, B), so a list
	// that merely preserved insertion/arrival order would read C, A, B —
	// not the alphabetical A, B, C this test requires.
	startAgent(t, agentConfig{nodeID: nodeC})
	startAgent(t, agentConfig{nodeID: nodeA})
	startAgent(t, agentConfig{nodeID: nodeB})
	waitOnline(t, coord, nodeC)
	waitOnline(t, coord, nodeA)
	waitOnline(t, coord, nodeB)

	status, body := coord.getRaw(t, "/api/v1/nodes")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/nodes: status %d, body: %s", status, body)
	}
	var resp struct {
		Nodes []struct {
			NodeID string `json:"nodeId"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/nodes: %v; body: %s", err, body)
	}

	var got []string
	for _, n := range resp.Nodes {
		if n.NodeID == nodeA || n.NodeID == nodeB || n.NodeID == nodeC {
			got = append(got, n.NodeID)
		}
	}
	want := []string{nodeA, nodeB, nodeC}
	sort.Strings(want) // honest sort rather than hand-ordering the literal; happens to already be aaa/mmm/zzz order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("this test's 3 nodes appeared in order %v, want alphabetical by nodeId: %v (full response: %s)", got, want, body)
	}
}

// TestAPIFPPInstancesOrderedByConfiguration configures three FPP
// instances whose IDs are neither alphabetical nor reverse-alphabetical in
// SHOWMESH_FPP_ENDPOINTS, and asserts GET /api/v1/fpp returns them in
// exactly that configured order — proving the list is NOT re-sorted
// alphabetically by instanceId, which an alphabetically-sorted set of test
// IDs could not distinguish from configuration order.
func TestAPIFPPInstancesOrderedByConfiguration(t *testing.T) {
	requireBroker(t)
	suffix := uniqueSuffix()
	idZ := "zzz-fpp-" + suffix
	idA := "aaa-fpp-" + suffix
	idM := "mmm-fpp-" + suffix

	// 127.0.0.1:1 is the same deliberately-closed-port trick cli_test.go
	// uses for "coordinator unreachable": syntactically valid, guaranteed
	// never to answer, so this test only exercises ordering, never
	// depending on any FPP actually being reachable.
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		fppEndpoints: idZ + "=http://127.0.0.1:1," + idA + "=http://127.0.0.1:1," + idM + "=http://127.0.0.1:1",
	})

	status, body := coord.getRaw(t, "/api/v1/fpp")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/fpp: status %d, body: %s", status, body)
	}
	var resp struct {
		Instances []struct {
			InstanceID string `json:"instanceId"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/fpp: %v; body: %s", err, body)
	}

	var got []string
	for _, inst := range resp.Instances {
		got = append(got, inst.InstanceID)
	}
	want := []string{idZ, idA, idM} // configuration order, deliberately not alphabetical
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fpp instances order = %v, want configuration order %v (full response: %s)", got, want, body)
	}
}

// TestAPIObservationsOrderedByResourceThenSignal proves the flat
// GET /api/v1/observations list — which unions the FPP collector's
// persisted rows with node evidence synthesized at read time (Step 3
// review finding 3.1) — comes back sorted by (resource kind, resource ID,
// signal) as one stable order across both sources, not "store rows in
// their own order, then whichever synthesized entries got appended after."
//
// The FPP instance ID is chosen to start with "a" and the node ID with
// "b" — irrelevant to the ordering this test actually needs, which turns
// on resource KIND ("fpp" < "node" lexicographically) regardless of
// either ID, but chosen anyway so a reader does not have to check the
// alphabet to see why "fpp" sorts first.
func TestAPIObservationsOrderedByResourceThenSignal(t *testing.T) {
	requireBroker(t)
	suffix := uniqueSuffix()
	fppID := "a-fpp-" + suffix
	nodeID := "b-node-" + suffix

	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: t.TempDir(), clientID: "coord-" + uniqueSuffix(),
		fppEndpoints: fppID + "=http://127.0.0.1:1",
	})
	startAgent(t, agentConfig{nodeID: nodeID})
	waitOnline(t, coord, nodeID)

	type obsEntry struct {
		Resource struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"resource"`
		Signal string `json:"signal"`
	}
	decode := func(body []byte) []obsEntry {
		var resp struct {
			Observations []obsEntry `json:"observations"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode /api/v1/observations: %v; body: %s", err, body)
		}
		return resp.Observations
	}

	// The FPP collector's first poll happens promptly on Runner.Run start
	// (see internal/coordinator/collector's own TestRunnerPollsOnCadence),
	// but is not instantaneous; wait for at least one fpp observation
	// rather than racing it.
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		_, body := coord.getRaw(t, "/api/v1/observations?resourceKind=fpp&resourceId="+fppID)
		return len(decode(body)) > 0
	}, "the FPP collector's first poll to land at least one observation")

	status, body := coord.getRaw(t, "/api/v1/observations")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/observations: status %d, body: %s", status, body)
	}
	entries := decode(body)
	if len(entries) == 0 {
		t.Fatalf("GET /api/v1/observations returned no entries at all; body: %s", body)
	}

	// The whole list, from both sources, must be non-decreasing by
	// (resource.kind, resource.id, signal).
	for i := 1; i < len(entries); i++ {
		prev, cur := entries[i-1], entries[i]
		prevKey := [3]string{prev.Resource.Kind, prev.Resource.ID, prev.Signal}
		curKey := [3]string{cur.Resource.Kind, cur.Resource.ID, cur.Signal}
		if curKey[0] < prevKey[0] ||
			(curKey[0] == prevKey[0] && curKey[1] < prevKey[1]) ||
			(curKey[0] == prevKey[0] && curKey[1] == prevKey[1] && curKey[2] < prevKey[2]) {
			t.Fatalf("observations out of order at index %d: %+v came after %+v (want non-decreasing (kind, id, signal)); full body: %s",
				i, cur, prev, body)
		}
	}

	// And specifically: every "fpp" entry for this test's own instance
	// must precede every "node" entry for this test's own node — the one
	// fact that actually distinguishes "sorted across the union" from
	// "each source individually sorted, concatenated in whatever order the
	// union happened to append them."
	lastFPPIndex, firstNodeIndex := -1, -1
	for i, e := range entries {
		if e.Resource.Kind == "fpp" && e.Resource.ID == fppID {
			lastFPPIndex = i
		}
		if e.Resource.Kind == "node" && e.Resource.ID == nodeID && firstNodeIndex == -1 {
			firstNodeIndex = i
		}
	}
	if lastFPPIndex == -1 {
		t.Fatalf("no fpp observations for %s found in the response; body: %s", fppID, body)
	}
	if firstNodeIndex == -1 {
		t.Fatalf("no node observations for %s found in the response; body: %s", nodeID, body)
	}
	if lastFPPIndex > firstNodeIndex {
		t.Fatalf("an fpp observation for %s (index %d) appeared after a node observation for %s (index %d); want every fpp entry before every node entry",
			fppID, lastFPPIndex, nodeID, firstNodeIndex)
	}
}

// TestAPIEventsOrderedAscendingBySeq generates several distinct
// control-plane events (one online transition per started agent) and
// asserts GET /api/v1/events returns them in ascending seq order — the
// store's own `ORDER BY seq ASC` is exercised by many other tests
// incidentally, but nothing in this package asserted the ordering
// directly by name until now.
func TestAPIEventsOrderedAscendingBySeq(t *testing.T) {
	requireBroker(t)
	coord := startCoordinator(t, t.TempDir(), "coord-"+uniqueSuffix())

	suffix := uniqueSuffix()
	for i := 0; i < 3; i++ {
		nodeID := "evt-node-" + string(rune('a'+i)) + "-" + suffix
		startAgent(t, agentConfig{nodeID: nodeID})
		waitOnline(t, coord, nodeID)
	}

	var body []byte
	waitFor(t, 15*time.Second, 200*time.Millisecond, func() bool {
		var status int
		status, body = coord.getRaw(t, "/api/v1/events")
		if status != http.StatusOK {
			return false
		}
		var resp struct {
			Events []struct{} `json:"events"`
		}
		_ = json.Unmarshal(body, &resp)
		return len(resp.Events) >= 3
	}, "at least 3 recorded events (one online transition per started agent)")

	var resp struct {
		Events []struct {
			Seq uint64 `json:"seq"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode /api/v1/events: %v; body: %s", err, body)
	}
	for i := 1; i < len(resp.Events); i++ {
		if resp.Events[i].Seq <= resp.Events[i-1].Seq {
			t.Fatalf("events out of order at index %d: seq %d did not increase from %d; full body: %s",
				i, resp.Events[i].Seq, resp.Events[i-1].Seq, body)
		}
	}
}
