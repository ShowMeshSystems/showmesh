package assetsync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type publishCall struct {
	topic   string
	qos     byte
	retain  bool
	payload []byte
}

type fakePublisher struct {
	mu    sync.Mutex
	calls []publishCall
	err   error
}

func (f *fakePublisher) Publish(_ context.Context, topic string, qos byte, retain bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{topic: topic, qos: qos, retain: retain, payload: append([]byte(nil), payload...)})
	return f.err
}

func (f *fakePublisher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakePublisher) callsFor(nodeID string) []publishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	wantTopic, _ := mqttproto.CmdTopic(nodeID)
	var out []publishCall
	for _, c := range f.calls {
		if c.topic == wantTopic {
			out = append(out, c)
		}
	}
	return out
}

// --- FetchConfirmed: the post-dispatch evidence fence ---

func TestFetchConfirmedRequiresPostDispatchEvidence(t *testing.T) {
	dispatchedAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	t.Run("report before dispatch does not confirm even if held", func(t *testing.T) {
		report := &store.NodeAssetReportRecord{ReportedAt: dispatchedAt.Add(-time.Second)}
		if FetchConfirmed(dispatchedAt, report, true) {
			t.Fatal("FetchConfirmed() = true, want false: evidence from BEFORE dispatch is not evidence of THIS dispatch")
		}
	})
	t.Run("report at dispatch time confirms", func(t *testing.T) {
		report := &store.NodeAssetReportRecord{ReportedAt: dispatchedAt}
		if !FetchConfirmed(dispatchedAt, report, true) {
			t.Fatal("FetchConfirmed() = false, want true: a report AT dispatch time counts")
		}
	})
	t.Run("report after dispatch confirms", func(t *testing.T) {
		report := &store.NodeAssetReportRecord{ReportedAt: dispatchedAt.Add(time.Microsecond)}
		if !FetchConfirmed(dispatchedAt, report, true) {
			t.Fatal("FetchConfirmed() = false, want true")
		}
	})
	t.Run("not held never confirms regardless of report time", func(t *testing.T) {
		report := &store.NodeAssetReportRecord{ReportedAt: dispatchedAt.Add(time.Hour)}
		if FetchConfirmed(dispatchedAt, report, false) {
			t.Fatal("FetchConfirmed() = true, want false: held=false means the node does not have it")
		}
	})
	t.Run("nil report never confirms", func(t *testing.T) {
		if FetchConfirmed(dispatchedAt, nil, true) {
			t.Fatal("FetchConfirmed() = true, want false: nil report means no evidence at all")
		}
	})
}

// --- Service.Enabled / Run's no-op-when-disabled contract ---

func TestServiceDisabledWhenNoContentBaseURL(t *testing.T) {
	st := openTestStore(t)
	svc := NewService(st, &fakePublisher{}, discardLogger(), Settings{SyncInterval: time.Millisecond, InventoryInterval: time.Minute})
	if svc.Enabled() {
		t.Fatal("Enabled() = true, want false with an empty content base URL")
	}

	// Track G seam G-4 (ADR-039 decision 6): Run must NOT return early
	// just because it started disabled — contentBaseUrl can be set later,
	// through the assets.settings configuration kind, with no restart, and
	// a Run that already returned could never notice. It must keep
	// looping (responsive to ctx) until told to stop.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()

	select {
	case <-done:
		t.Fatal("Run() returned while ctx was still live; a disabled service must keep watching for its settings to change rather than exiting")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return promptly after ctx was cancelled")
	}
}

// TestServiceEnabledFollowsLiveSetSettings is Track G seam G-4's own
// acceptance shape at the unit level: SetSettings flips Enabled with no
// reconstruction, and Run (already looping while disabled) starts
// dispatching on its very next iteration rather than needing to be
// restarted — the "zero to one" transition ADR-039 decision 6 names as the
// one that must actually work.
func TestServiceEnabledFollowsLiveSetSettings(t *testing.T) {
	st := openTestStore(t)
	pub := &fakePublisher{}
	svc := NewService(st, pub, discardLogger(), Settings{SyncInterval: 2 * time.Millisecond, InventoryInterval: time.Minute})
	if svc.Enabled() {
		t.Fatal("Enabled() = true, want false before any settings are applied")
	}

	nodeID := "shed-01"
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, nodeID)
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, nodeID, "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, nodeID, time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Give Run a couple of disabled iterations to prove it does not dispatch.
	time.Sleep(20 * time.Millisecond)
	if pub.callCount() != 0 {
		t.Fatalf("callCount() = %d, want 0: a disabled service must not dispatch", pub.callCount())
	}

	svc.SetSettings(Settings{ContentBaseURL: "https://coordinator.example", MaxUploadBytes: 1, SyncInterval: 2 * time.Millisecond, InventoryInterval: time.Minute})
	if !svc.Enabled() {
		t.Fatal("Enabled() = false after SetSettings with a non-empty ContentBaseURL, want true")
	}

	deadline := time.After(2 * time.Second)
	for pub.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("no asset.fetch was dispatched within the deadline after SetSettings enabled the service with no restart")
		case <-time.After(time.Millisecond):
		}
	}
}

// --- Service.tick: dispatch, budget, in-flight, reconciliation ---

func newTestService(t *testing.T, st *store.Store, pub Publisher) *Service {
	t.Helper()
	svc := NewService(st, pub, discardLogger(), Settings{
		ContentBaseURL: "https://coordinator.example", MaxUploadBytes: 1, InventoryInterval: time.Minute,
	})
	return svc
}

// seedEmptyCompleteReport seeds nodeID's FIRST-EVER inventory report: empty
// and complete, simulating an agent that has scanned its own (empty) asset
// directory at least once. P4b: syncNode now routes through the same
// ComputeNodeManifest a manifest read uses, and a node with NO report at
// all reads Unknown/NeverReported — nothing is dispatched to a node syncNode
// cannot currently read (§1 rule 4's "complete: true has to be earned" cuts
// both ways: no report is also not permission to assume ready-to-receive).
// Every dispatch-behavior test below needs this seeded first, or syncNode
// dispatches nothing and the test is measuring the wrong thing.
func seedEmptyCompleteReport(t *testing.T, st *store.Store, nodeID string, at time.Time) {
	t.Helper()
	if err := st.ReplaceNodeAssetInventory(context.Background(), nodeID, nil,
		store.NodeAssetReportRecord{ReportedAt: at, Complete: true}); err != nil {
		t.Fatalf("seed empty complete report for %q: %v", nodeID, err)
	}
}

func TestServiceDispatchesMissingAsset(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	calls := pub.callsFor("render-01")
	if len(calls) != 1 {
		t.Fatalf("callsFor(render-01) = %d calls, want exactly 1", len(calls))
	}
	if calls[0].qos != mqttproto.CmdDeliveryPolicy.QoS || calls[0].retain != mqttproto.CmdDeliveryPolicy.Retain {
		t.Errorf("dispatch qos/retain = %d/%v, want CmdDeliveryPolicy's %d/%v", calls[0].qos, calls[0].retain, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain)
	}

	env, err := mqttproto.DecodeEnvelope(calls[0].payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	cmd, err := mqttproto.DecodeCmdPayload(env)
	if err != nil {
		t.Fatalf("DecodeCmdPayload() error = %v", err)
	}
	if cmd.Action != "asset.fetch" {
		t.Errorf("Action = %q, want %q", cmd.Action, "asset.fetch")
	}
	if cmd.Target.Kind != "node" || cmd.Target.ID != "render-01" {
		t.Errorf("Target = %+v, want node/render-01", cmd.Target)
	}
	if cmd.Params["contentHash"] != "sha256:aaa" || cmd.Params["filename"] != "Opening.fseq" {
		t.Errorf("Params = %+v, want contentHash=sha256:aaa filename=Opening.fseq", cmd.Params)
	}
	// D5: sizeBytes decodes through JSON as float64, and the agent's own
	// parseAssetFetchParams refuses anything below 1 — an unasserted or
	// zero sizeBytes here would make every asset fetch on every node fail
	// permanently without this test noticing.
	if sb, ok := cmd.Params["sizeBytes"].(float64); !ok || sb != 1024 {
		t.Errorf("Params[sizeBytes] = %v (%T), want float64(1024)", cmd.Params["sizeBytes"], cmd.Params["sizeBytes"])
	}
	wantURL := "https://coordinator.example/api/v1/assets/" + cmd.Params["assetId"].(string) + "/content"
	if cmd.Params["url"] != wantURL {
		t.Errorf("Params[url] = %v, want %q", cmd.Params["url"], wantURL)
	}
	if cmd.Issuer.PrincipalID == "" {
		t.Error("Issuer.PrincipalID is empty; CmdPayload.Validate requires it non-empty")
	}
}

func TestServiceReadyNodeIsNeverDispatchedTo(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	rec := createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	// render-01 already reports holding it, freshly.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{ContentHash: rec.ContentHash, RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes, VerifiedAt: time.Now()}},
		store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true},
	); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != 0 {
		t.Fatalf("callCount() = %d, want 0: a node already holding everything must never be dispatched to", n)
	}
}

func TestServicePerNodeBudgetLimitsConcurrentDispatch(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "seq-a", store.AssetTargetKindNode, "render-01", "sha256:aaa", "A.fseq")
	createAsset(t, st, "halloween-2026", "seq-b", store.AssetTargetKindNode, "render-01", "sha256:bbb", "B.fseq")
	createAsset(t, st, "halloween-2026", "seq-c", store.AssetTargetKindNode, "render-01", "sha256:ccc", "C.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != maxInFlightPerNode {
		t.Fatalf("callCount() = %d, want exactly maxInFlightPerNode (%d): a third asset for one node must wait for budget", n, maxInFlightPerNode)
	}
}

func TestServiceDoesNotRedispatchAlreadyInFlight(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())
	svc.tick(context.Background())

	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() = %d after two ticks with no confirmation, want 1: an outstanding fetch must not be redispatched", n)
	}
}

func TestServiceReconcilesInFlightOnPostDispatchConfirmation(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	rec := createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() after first tick = %d, want 1", n)
	}

	svc.mu.Lock()
	inFlightBefore := len(svc.inFlight)
	svc.mu.Unlock()
	if inFlightBefore != 1 {
		t.Fatalf("in-flight count after dispatch = %d, want 1", inFlightBefore)
	}

	// The agent confirms, with a report timestamped AFTER the dispatch.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{ContentHash: rec.ContentHash, RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes, VerifiedAt: time.Now()}},
		store.NodeAssetReportRecord{ReportedAt: time.Now().Add(time.Second), Complete: true},
	); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	svc.tick(context.Background())

	svc.mu.Lock()
	inFlightAfter := len(svc.inFlight)
	svc.mu.Unlock()
	if inFlightAfter != 0 {
		t.Fatalf("in-flight count after confirmation = %d, want 0: FetchConfirmed should have cleared it", inFlightAfter)
	}
	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() after confirmation tick = %d, want still 1 (no redispatch of a now-held asset)", n)
	}
}

func TestServiceExpiredInFlightIsEventuallyRedispatched(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub) // 1-minute inventoryInterval -> 3-minute staleness window

	now := time.Now()
	svc.now = func() time.Time { return now }
	seedEmptyCompleteReport(t, st, "render-01", now)
	svc.tick(context.Background())
	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() after first tick = %d, want 1", n)
	}

	// Advance past inFlightExpiry for a 1024-byte asset (assetstore.
	// UploadBudget's grace alone is 30s) without any confirming report, but
	// stay WELL WITHIN the 3-minute staleness window (StalenessWindow of
	// this service's 1-minute inventoryInterval): this proves the in-flight
	// EXPIRY mechanism specifically. Advancing past the staleness window too
	// would make the node's own report go stale, which (correctly, per
	// P4b) also suppresses redispatch — a different mechanism this test is
	// not the one to exercise.
	now = now.Add(45 * time.Second)
	svc.tick(context.Background())

	if n := pub.callCount(); n != 2 {
		t.Fatalf("callCount() after expiry = %d, want 2: an unconfirmed dispatch must eventually be retried", n)
	}
}

func TestServiceDispatchFailureIsNotCountedInFlight(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{err: context.DeadlineExceeded}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	svc.mu.Lock()
	inFlight := len(svc.inFlight)
	svc.mu.Unlock()
	if inFlight != 0 {
		t.Fatalf("in-flight count after a failed publish = %d, want 0: a failed dispatch must not consume budget", inFlight)
	}

	// A second tick must try again rather than treating the failed attempt
	// as consuming its one shot.
	svc.tick(context.Background())
	if n := pub.callCount(); n != 2 {
		t.Fatalf("callCount() = %d, want 2: a failed dispatch must be retried on the next tick", n)
	}
}

// --- P4b: syncNode routes through the same readiness function a manifest
// read uses, and dispatches nothing when that verdict is unknown for any
// cause ---

// TestServiceNeverReportedNodeDispatchesNothing is P4b's core claim: before
// this fix, syncNode dispatched to a node regardless of whether it had ever
// reported at all. A node with zero evidence is a node syncNode cannot
// currently read, and "you cannot know what is missing from a node you
// cannot read" applies to silence exactly as much as to a stale or
// incomplete report.
func TestServiceNeverReportedNodeDispatchesNothing(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	// Deliberately no seeded report at all.

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != 0 {
		t.Fatalf("callCount() = %d, want 0: a node that has never reported must not be dispatched to", n)
	}
}

// TestServiceTransientIncompleteReportDoesNotCauseRedispatchStorm is P4's
// own end-to-end scenario, both halves together: a node already holding
// everything goes transiently unreadable (complete:false, no items) for
// several ticks. Before the fix: (a) store.replaceNodeAssetInventory
// (P4a) deleted the node's held rows on the very first incomplete report,
// and (b) syncNode (P4b) then saw nothing held and re-dispatched every
// expected asset on every tick until the outage ended — "two at a time,
// every tick" per the review's own description. Neither may happen now.
func TestServiceTransientIncompleteReportDoesNotCauseRedispatchStorm(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	rec := createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{ContentHash: rec.ContentHash, RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes, VerifiedAt: time.Now()}},
		store.NodeAssetReportRecord{ReportedAt: time.Now(), Complete: true},
	); err != nil {
		t.Fatalf("seed initial complete report: %v", err)
	}

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())
	if n := pub.callCount(); n != 0 {
		t.Fatalf("callCount() before any incompleteness = %d, want 0: the node already holds everything", n)
	}

	// The node's asset directory goes transiently unreadable.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: time.Now().Add(time.Second), Complete: false, Reason: "asset directory temporarily unreadable"},
	); err != nil {
		t.Fatalf("seed incomplete report: %v", err)
	}

	for i := 0; i < 3; i++ {
		svc.tick(context.Background())
	}
	if n := pub.callCount(); n != 0 {
		t.Fatalf("callCount() across 3 ticks during the outage = %d, want 0", n)
	}

	inv, err := st.GetNodeAssetInventory(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if len(inv) != 1 || inv[0].ContentHash != rec.ContentHash {
		t.Fatalf("inventory during the outage = %+v, want the PRIOR complete report's inventory left untouched", inv)
	}
}

// TestServiceReconcileInFlightIgnoresPreDispatchReport pins D1:
// reconcileInFlight's call site must apply FetchConfirmed's post-dispatch
// fence, not "held[...]" alone. FetchConfirmed itself is well tested as a
// pure function, but every OTHER test in this file only ever seeds a
// confirming report AFTER the dispatch, so mutating the call site to plain
// held[...] would leave them all green. Here a report dated BEFORE the
// dispatch already lists the hash (content-addressed dedup, ADR-028, makes
// a coincidentally pre-existing hash realistic) — that must NOT confirm
// this particular dispatch.
func TestServiceReconcileInFlightIgnoresPreDispatchReport(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	rec := createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)

	dispatchedAt := time.Now()
	svc.now = func() time.Time { return dispatchedAt }
	seedEmptyCompleteReport(t, st, "render-01", dispatchedAt.Add(-2*time.Second))
	svc.tick(context.Background())

	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() after dispatch = %d, want 1", n)
	}
	svc.mu.Lock()
	if len(svc.inFlight) != 1 {
		svc.mu.Unlock()
		t.Fatalf("in-flight count after dispatch = %d, want 1", len(svc.inFlight))
	}
	svc.mu.Unlock()

	// A report dated BEFORE dispatchedAt already lists the hash.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{ContentHash: rec.ContentHash, RuntimeFilename: rec.RuntimeFilename, SizeBytes: rec.SizeBytes, VerifiedAt: dispatchedAt.Add(-time.Second)}},
		store.NodeAssetReportRecord{ReportedAt: dispatchedAt.Add(-time.Second), Complete: true},
	); err != nil {
		t.Fatalf("seed pre-dispatch report: %v", err)
	}

	svc.tick(context.Background())

	svc.mu.Lock()
	inFlightAfter := len(svc.inFlight)
	svc.mu.Unlock()
	if inFlightAfter != 1 {
		t.Fatalf("in-flight count after a PRE-DISPATCH report tick = %d, want still 1: a report from BEFORE the dispatch is not evidence THIS dispatch succeeded", inFlightAfter)
	}
}

// TestServiceFleetWideBudgetLimitsTotalConcurrentDispatch pins D4:
// maxInFlightTotal is structurally unreachable in every other test in this
// file because they all use exactly one node. Five nodes with two missing
// assets each (10 attempts, none of which individually exceeds
// maxInFlightPerNode) must still cap at maxInFlightTotal across the whole
// fleet in one tick.
func TestServiceFleetWideBudgetLimitsTotalConcurrentDispatch(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")

	const nodeCount = 5
	for i := 0; i < nodeCount; i++ {
		nodeID := fmt.Sprintf("render-%02d", i)
		declareNode(t, st, nodeID)
		createAsset(t, st, "halloween-2026", "seq-a", store.AssetTargetKindNode, nodeID, "sha256:"+nodeID+"-a", "A.fseq")
		createAsset(t, st, "halloween-2026", "seq-b", store.AssetTargetKindNode, nodeID, "sha256:"+nodeID+"-b", "B.fseq")
		seedEmptyCompleteReport(t, st, nodeID, time.Now())
	}

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != maxInFlightTotal {
		t.Fatalf("callCount() = %d, want exactly maxInFlightTotal (%d): 5 nodes x 2 missing assets each must still cap at the fleet-wide ceiling", n, maxInFlightTotal)
	}
}

func TestServiceNoActiveShowDispatchesNothing(t *testing.T) {
	st := openTestStore(t)
	declareNode(t, st, "render-01")

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.tick(context.Background())

	if n := pub.callCount(); n != 0 {
		t.Fatalf("callCount() = %d, want 0 with no active show configured", n)
	}
}

func TestServiceNudgeIsNonBlockingAndCoalesces(t *testing.T) {
	st := openTestStore(t)
	svc := NewService(st, &fakePublisher{}, discardLogger(), Settings{ContentBaseURL: "https://coordinator.example", InventoryInterval: time.Minute})

	// Two nudges before anything drains the channel must not block, and
	// must coalesce to one pending request.
	svc.Nudge()
	svc.Nudge()

	select {
	case <-svc.nudge:
	default:
		t.Fatal("Nudge() left nothing queued")
	}
	select {
	case <-svc.nudge:
		t.Fatal("Nudge() queued a second pending request; it should coalesce")
	default:
	}
}

// TestRequestNodeSyncsOnlyTheRequestedNode proves the per-node request path
// (noderesync.go's own delivery, this package's own RequestNode/
// syncRequestedNodes) dispatches to exactly the node it named, never to
// every declared node the way tick's own full-fleet pass does.
func TestRequestNodeSyncsOnlyTheRequestedNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	createAsset(t, st, "halloween-2026", "finale", store.AssetTargetKindNode, "render-02", "sha256:bbb", "Finale.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())
	seedEmptyCompleteReport(t, st, "render-02", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.RequestNode("render-01")
	svc.syncRequestedNodes(context.Background())

	if n := len(pub.callsFor("render-01")); n != 1 {
		t.Fatalf("callsFor(render-01) = %d, want exactly 1", n)
	}
	if n := len(pub.callsFor("render-02")); n != 0 {
		t.Fatalf("callsFor(render-02) = %d, want 0: a request for render-01 must never touch render-02", n)
	}
}

// TestRequestNodeDrainsEvenWithAPendingNudge proves a queued node id
// survives an already-pending nudge from an unrelated caller: RequestNode
// must never lose a node id to Nudge's own coalescing.
func TestRequestNodeDrainsEvenWithAPendingNudge(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	svc.Nudge()
	svc.RequestNode("render-01")
	svc.syncRequestedNodes(context.Background())

	if n := len(pub.callsFor("render-01")); n != 1 {
		t.Fatalf("callsFor(render-01) = %d, want exactly 1", n)
	}
}

// --- HandleMessage: consuming asset.fetch results ---

// resultMessage builds a broker.Message carrying a result envelope for
// nodeID/cmdID, matching what internal/agent/command.go's publishResult
// actually puts on the wire.
func resultMessage(t *testing.T, nodeID, cmdID string, result mqttproto.ResultPayload) broker.Message {
	t.Helper()
	result.CommandID = cmdID
	if result.IdempotencyKey == "" {
		result.IdempotencyKey = "idempotency-" + cmdID
	}
	env, err := mqttproto.NewResultEnvelope(time.Now, nodeID, result)
	if err != nil {
		t.Fatalf("NewResultEnvelope() error = %v", err)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	topic, err := mqttproto.ResultTopic(nodeID, cmdID)
	if err != nil {
		t.Fatalf("ResultTopic() error = %v", err)
	}
	return broker.Message{Topic: topic, Payload: payload, Retained: false}
}

// dispatchOne ticks svc once against a store already seeded with exactly
// one missing asset for nodeID, and returns the command ID the dispatch
// used (decoded off the wire, the same way TestServiceDispatchesMissingAsset
// does) so a test can then feed HandleMessage a matching result.
func dispatchOne(t *testing.T, st *store.Store, pub *fakePublisher, svc *Service, nodeID string) string {
	t.Helper()
	svc.tick(context.Background())
	calls := pub.callsFor(nodeID)
	if len(calls) == 0 {
		t.Fatalf("callsFor(%s) = 0 calls, want at least 1 dispatch to correlate against", nodeID)
	}
	env, err := mqttproto.DecodeEnvelope(calls[len(calls)-1].payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	cmd, err := mqttproto.DecodeCmdPayload(env)
	if err != nil {
		t.Fatalf("DecodeCmdPayload() error = %v", err)
	}
	return cmd.CommandID
}

func TestHandleMessageRecordsFailedFetchReason(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	cmdID := dispatchOne(t, st, pub, svc, "render-01")

	const wantReason = "asset.fetch: download failed: dial tcp 127.0.0.1:1: connect: connection refused"
	svc.HandleMessage(resultMessage(t, "render-01", cmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: wantReason,
	}))

	reason, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa")
	if !ok {
		t.Fatal("LastFetchFailure() ok = false, want true after a recorded failure")
	}
	if reason != wantReason {
		t.Errorf("LastFetchFailure() reason = %q, want %q", reason, wantReason)
	}

	events, _, err := st.ListEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	var found bool
	for _, ev := range events {
		if ev.Resource.ID == "render-01" && ev.Category == "asset_sync" {
			found = true
			if !strings.Contains(string(ev.Details), wantReason) {
				t.Errorf("event Details = %s, want it to contain %q", ev.Details, wantReason)
			}
		}
	}
	if !found {
		t.Fatal("no asset_sync event was appended for the failed fetch; GET /api/v1/events would show nothing")
	}
}

func TestHandleMessageIgnoresUnknownCommandID(t *testing.T) {
	st := openTestStore(t)
	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)

	// A result for a command this Service never dispatched (e.g. some
	// other command sharing the same topic wildcard, or a stale delivery
	// after a restart) must be a silent no-op, never a panic or a
	// fabricated failure record.
	svc.HandleMessage(resultMessage(t, "render-01", "not-a-tracked-command", mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: "irrelevant",
	}))

	if _, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa"); ok {
		t.Fatal("LastFetchFailure() ok = true for an untracked command; want false")
	}
}

func TestHandleMessageIgnoresRetainedDelivery(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	cmdID := dispatchOne(t, st, pub, svc, "render-01")

	msg := resultMessage(t, "render-01", cmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: "should never be recorded",
	})
	msg.Retained = true
	svc.HandleMessage(msg)

	if _, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa"); ok {
		t.Fatal("LastFetchFailure() ok = true for a retained delivery; result topics are never retained and this must be discarded defensively")
	}
}

// TestHandleMessageLateResultAfterInventoryReconcileDoesNotClobberFailure
// exercises a late asset.fetch result arriving after its dispatch has
// already been reconciled by an inventory report. Sequence, entirely
// deterministic (no goroutines, no sleeps):
//
//  1. Dispatch attempt #1 fails for a real reason; that reason (with the
//     correct assetID/filename) is recorded in failures[key].
//  2. Dispatch attempt #2 goes out for the same key, giving it a second
//     commandID and a fresh inFlight record.
//  3. An inventory report dated after attempt #2's dispatch reconciles it
//     (FetchConfirmed) directly via reconcileInFlight, deleting
//     inFlight[key] while attempt #2's own result has not arrived yet.
//  4. Attempt #2's OWN result then arrives late as Failed.
//
// The correct behavior is that a late result for an already-reconciled
// dispatch is treated as untracked (the existing not-tracked branch is a
// silent no-op), so the real attempt-#1 failure record must survive
// unchanged.
func TestHandleMessageLateResultAfterInventoryReconcileDoesNotClobberFailure(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)

	// Attempt #1: dispatch, then a genuine failure arrives and is recorded
	// with the real asset metadata.
	firstCmdID := dispatchOne(t, st, pub, svc, "render-01")
	const realReason = "asset.fetch: download failed: dial tcp 127.0.0.1:1: connect: connection refused"
	svc.HandleMessage(resultMessage(t, "render-01", firstCmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: realReason,
	}))
	reason, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa")
	if !ok || reason != realReason {
		t.Fatalf("LastFetchFailure() after attempt #1 = (%q, %v), want (%q, true)", reason, ok, realReason)
	}

	// Attempt #2: force the budget clear (attempt #1's in-flight record
	// does not expire on its own within this test), then redispatch to get
	// a second commandID for the same key.
	svc.mu.Lock()
	svc.inFlight = map[dispatchKey]dispatchRecord{}
	svc.mu.Unlock()
	dispatchedAt2 := time.Now()
	svc.now = func() time.Time { return dispatchedAt2 }
	secondCmdID := dispatchOne(t, st, pub, svc, "render-01")

	svc.mu.Lock()
	_, stillTracked := svc.byCmdID[secondCmdID]
	svc.mu.Unlock()
	if !stillTracked {
		t.Fatal("byCmdID does not track attempt #2's commandID right after dispatch")
	}

	// The inventory-driven reconcile path: a report dated AFTER attempt
	// #2's dispatch says the node now holds the content hash, so
	// reconcileInFlight deletes inFlight[key] out from under attempt #2,
	// with no result for attempt #2 ever having arrived.
	report := &store.NodeAssetReportRecord{ReportedAt: dispatchedAt2.Add(time.Second), Complete: true}
	svc.reconcileInFlight("render-01", report, map[string]bool{"sha256:aaa": true})

	svc.mu.Lock()
	_, stillInFlight := svc.inFlight[dispatchKey{nodeID: "render-01", contentHash: "sha256:aaa"}]
	svc.mu.Unlock()
	if stillInFlight {
		t.Fatal("reconcileInFlight left inFlight[key] populated; test setup is not exercising the reconcile path it needs to")
	}

	// Attempt #2's own result now arrives late: its first and only
	// delivery, but after reconcileInFlight already dropped inFlight[key].
	svc.HandleMessage(resultMessage(t, "render-01", secondCmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: "late spurious failure",
	}))

	reason, _, ok = svc.LastFetchFailure("render-01", "sha256:aaa")
	if !ok {
		t.Fatal("LastFetchFailure() ok = false after the late result; the real attempt #1 failure must still be recorded")
	}
	if reason != realReason {
		t.Errorf("LastFetchFailure() reason = %q after the late result, want unchanged %q: a late result for an already-reconciled dispatch must not overwrite a real failure", reason, realReason)
	}

	events, _, err := st.ListEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	for _, ev := range events {
		if ev.Resource.ID != "render-01" || ev.Category != "asset_sync" {
			continue
		}
		if strings.Contains(string(ev.Details), `"assetId":""`) {
			t.Errorf("event Details = %s: an asset_sync event was appended with an empty assetId, from the late result's zero-value dispatchRecord", ev.Details)
		}
		if strings.Contains(string(ev.Details), "late spurious failure") {
			t.Errorf("event Details = %s: the late result's reason must not be appended as a new event once its dispatch is no longer tracked", ev.Details)
		}
	}
}

func TestHandleMessageConfirmedClearsPriorFailure(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")
	seedEmptyCompleteReport(t, st, "render-01", time.Now())

	pub := &fakePublisher{}
	svc := newTestService(t, st, pub)
	firstCmdID := dispatchOne(t, st, pub, svc, "render-01")
	svc.HandleMessage(resultMessage(t, "render-01", firstCmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeFailed, Reason: "first attempt failed",
	}))
	if _, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa"); !ok {
		t.Fatal("LastFetchFailure() ok = false after a recorded failure, want true")
	}

	// pruneExpiredInFlight already removed the failed dispatch's byCmdID
	// entry (HandleMessage's own one-shot delete); the in-flight record
	// itself still occupies the budget until inFlightExpiry passes, so
	// force that here rather than waiting it out, then redispatch and
	// confirm the SECOND attempt.
	svc.mu.Lock()
	svc.inFlight = map[dispatchKey]dispatchRecord{}
	svc.mu.Unlock()
	secondCmdID := dispatchOne(t, st, pub, svc, "render-01")
	svc.HandleMessage(resultMessage(t, "render-01", secondCmdID, mqttproto.ResultPayload{
		Action: "asset.fetch", Outcome: mqttproto.OutcomeConfirmed,
	}))

	if _, _, ok := svc.LastFetchFailure("render-01", "sha256:aaa"); ok {
		t.Fatal("LastFetchFailure() ok = true after a subsequent confirmed result; the stale failure should have been cleared")
	}
}
