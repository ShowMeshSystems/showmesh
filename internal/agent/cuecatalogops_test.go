package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// paramsFromWire round-trips wire through JSON into a map[string]any, the
// same shape a decoded MQTT command's params arrive in
// (mqttproto.CmdPayload.Params), so this test exercises the operation
// exactly the way HandleMessage would call it.
func paramsFromWire(t *testing.T, wire catalogDeployWireParams) map[string]any {
	t.Helper()
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire params: %v", err)
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal wire params: %v", err)
	}
	return params
}

func sampleEntries() []cuecatalog.Entry {
	return []cuecatalog.Entry{
		{CueID: "cue-b", CueRevision: 1, Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-b", AssetHashes: []string{"h2", "h1"}},
		}},
		{CueID: "cue-a", CueRevision: 2, Outputs: cuecatalog.Outputs{
			Audio: &cuecatalog.AudioOutput{Asset: "audio-a", AssetHashes: []string{"h3"}},
		}},
	}
}

func computeExpectedRevision(t *testing.T, show, node string, generation int64, entries []cuecatalog.Entry) string {
	t.Helper()
	sorted := sortCatalogEntries(entries)
	rev, err := cuecatalog.ComputeRevision(cuecatalog.RevisionInput{Show: show, Generation: generation, Node: node, Entries: sorted})
	if err != nil {
		t.Fatalf("ComputeRevision: %v", err)
	}
	return rev
}

func TestCatalogDeployPersistsAgreeingRevision(t *testing.T) {
	const nodeID = "node-01"
	store := heldcatalog.NewFileStore(t.TempDir())
	op := &catalogDeployOperation{nodeID: nodeID, store: store}

	entries := sampleEntries()
	revision := computeExpectedRevision(t, "halloween-2026", nodeID, 3, entries)
	params := paramsFromWire(t, catalogDeployWireParams{Show: "halloween-2026", Generation: 3, Revision: revision, Entries: entries})

	now := func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
	result, err := op.deploy(context.Background(), params, now)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("deploy reported Confirmed=false for an agreeing revision")
	}
	if result.Value != revision {
		t.Fatalf("deploy reported Value=%v, want computed revision %q", result.Value, revision)
	}

	held, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after deploy: ok=%v err=%v", ok, err)
	}
	if held.Show != "halloween-2026" || held.Generation != 3 || held.Revision != revision || held.Node != nodeID {
		t.Fatalf("persisted held catalog mismatch: %+v", held)
	}
	if len(held.Entries) != 2 || held.Entries[0].CueID != "cue-a" || held.Entries[1].CueID != "cue-b" {
		t.Fatalf("persisted held catalog entries not canonically sorted: %+v", held.Entries)
	}
}

// TestCatalogDeployRefusesDisagreeingRevision proves build item 1's own
// requirement: the node computes its OWN revision and refuses to store a
// catalog it cannot independently corroborate, rather than trusting the
// coordinator's claimed revision at face value.
func TestCatalogDeployRefusesDisagreeingRevision(t *testing.T) {
	const nodeID = "node-01"
	store := heldcatalog.NewFileStore(t.TempDir())
	op := &catalogDeployOperation{nodeID: nodeID, store: store}

	params := paramsFromWire(t, catalogDeployWireParams{
		Show: "halloween-2026", Generation: 3, Revision: "not-the-real-revision", Entries: sampleEntries(),
	})

	_, err := op.deploy(context.Background(), params, time.Now)
	if err == nil {
		t.Fatalf("deploy accepted a catalog whose claimed revision does not match the node's own computation")
	}

	if _, ok, loadErr := store.Load(); loadErr != nil || ok {
		t.Fatalf("deploy persisted a catalog it disagreed with: ok=%v err=%v", ok, loadErr)
	}
}

// TestCatalogDeployRevisionIsNodeScoped proves the node computes the
// revision using ITS OWN node id, not a caller-supplied one — the same
// catalog content deployed to a different node id must not match this
// node's own claimed revision, because H3 spec section 3.1's hash input
// covers the node id.
func TestCatalogDeployRevisionIsNodeScoped(t *testing.T) {
	const nodeID = "node-01"
	store := heldcatalog.NewFileStore(t.TempDir())
	op := &catalogDeployOperation{nodeID: nodeID, store: store}

	entries := sampleEntries()
	// Computed for a DIFFERENT node id than op.nodeID.
	revisionForOtherNode := computeExpectedRevision(t, "halloween-2026", "node-02", 3, entries)
	params := paramsFromWire(t, catalogDeployWireParams{Show: "halloween-2026", Generation: 3, Revision: revisionForOtherNode, Entries: entries})

	_, err := op.deploy(context.Background(), params, time.Now)
	if err == nil {
		t.Fatalf("deploy accepted a revision computed for a different node id")
	}
}

func TestCatalogDeployRejectsUnknownParamKey(t *testing.T) {
	store := heldcatalog.NewFileStore(t.TempDir())
	op := &catalogDeployOperation{nodeID: "node-01", store: store}

	params := paramsFromWire(t, catalogDeployWireParams{Show: "s", Generation: 1, Revision: "r"})
	params["unexpectedKey"] = "surprise"

	if _, err := op.deploy(context.Background(), params, time.Now); err == nil {
		t.Fatalf("deploy accepted an unrecognized params key")
	}
}

func TestCatalogDeployRequiresShowGenerationRevision(t *testing.T) {
	store := heldcatalog.NewFileStore(t.TempDir())
	op := &catalogDeployOperation{nodeID: "node-01", store: store}

	cases := []map[string]any{
		{"generation": float64(1), "revision": "r"},
		{"show": "s", "revision": "r"},
		{"show": "s", "generation": float64(1)},
	}
	for _, params := range cases {
		if _, err := op.deploy(context.Background(), params, time.Now); err == nil {
			t.Fatalf("deploy accepted incomplete params %v", params)
		}
	}
}

func TestCuecatalogDeployIsRegisteredWhenCatalogStoreProvided(t *testing.T) {
	store := heldcatalog.NewFileStore(t.TempDir())
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, store, nil, discardLogger())
	if _, ok := ops["cuecatalog.deploy"]; !ok {
		t.Fatalf(`newOperationRegistry() does not contain "cuecatalog.deploy" when a catalog store is provided`)
	}
}

func TestCuecatalogDeployIsAbsentWhenNoCatalogStore(t *testing.T) {
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, discardLogger())
	if _, ok := ops["cuecatalog.deploy"]; ok {
		t.Fatalf(`newOperationRegistry() contains "cuecatalog.deploy" with a nil catalog store`)
	}
}

// TestHandleMessageCuecatalogDeployConfirmed proves the end-to-end path
// through the real CommandHandler.HandleMessage: a cuecatalog.deploy
// command dispatched over the same allowlist/idempotency/evidence pipeline
// every other operation uses persists the catalog and reports
// OutcomeConfirmed with the computed revision as evidence.
func TestHandleMessageCuecatalogDeployConfirmed(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	clock := &fakeClock{t: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}

	h := newCommandHandler(testNodeID, dir, "", nil, nil, nil, nil, nil, store, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	entries := sampleEntries()
	revision := computeExpectedRevision(t, "halloween-2026", testNodeID, 5, entries)
	params := paramsFromWire(t, catalogDeployWireParams{Show: "halloween-2026", Generation: 5, Revision: revision, Entries: entries})

	cmd := renderCmd("cuecatalog.deploy", "cmd-1", "idem-1", params)
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != "confirmed" {
		t.Fatalf("Outcome = %q, want confirmed; reason = %q", result.Outcome, result.Reason)
	}
	if result.Evidence == nil || result.Evidence.Value != revision {
		t.Fatalf("Evidence = %+v, want Value = %q", result.Evidence, revision)
	}

	held, ok, err := store.Load()
	if err != nil || !ok {
		t.Fatalf("Load after deploy: ok=%v err=%v", ok, err)
	}
	if held.Revision != revision || held.Show != "halloween-2026" || held.Generation != 5 {
		t.Fatalf("persisted held catalog = %+v", held)
	}
}
