package assetsync

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

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
	svc := NewService(st, &fakePublisher{}, discardLogger(), "", time.Minute)
	if svc.Enabled() {
		t.Fatal("Enabled() = true, want false with an empty content base URL")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { svc.Run(ctx, time.Millisecond); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return promptly when disabled; it must log and return rather than looping")
	}
}

// --- Service.tick: dispatch, budget, in-flight, reconciliation ---

func newTestService(t *testing.T, st *store.Store, pub Publisher) *Service {
	t.Helper()
	svc := NewService(st, pub, discardLogger(), "https://coordinator.example", time.Minute)
	return svc
}

func TestServiceDispatchesMissingAsset(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")
	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindNode, "render-01", "sha256:aaa", "Opening.fseq")

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
	svc := newTestService(t, st, pub)

	now := time.Now()
	svc.now = func() time.Time { return now }
	svc.tick(context.Background())
	if n := pub.callCount(); n != 1 {
		t.Fatalf("callCount() after first tick = %d, want 1", n)
	}

	// Advance well past inFlightExpiry for a 1024-byte asset (assetstore.
	// UploadBudget's grace alone is 30s) without any confirming report.
	now = now.Add(2 * time.Hour)
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
	svc := NewService(st, &fakePublisher{}, discardLogger(), "https://coordinator.example", time.Minute)

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
