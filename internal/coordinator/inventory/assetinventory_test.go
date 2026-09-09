package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func assetsTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "assets")
	if err != nil {
		t.Fatalf("assets topic: %v", err)
	}
	return topic
}

// fakeResyncIntentTrigger is a minimal [ResyncIntentTrigger] a test can
// inspect, so this file's tests prove exactly what handleAssetInventory
// hands it without depending on assetsync.Service's own real
// implementation - [fakeRenderSink]'s identical role, one interface over.
type fakeResyncIntentTrigger struct {
	calls []resyncTriggerCall
}

type resyncTriggerCall struct {
	nodeID     string
	reportedAt time.Time
}

func (f *fakeResyncIntentTrigger) TriggerIfResyncIntentPrecedes(nodeID string, reportedAt time.Time) {
	f.calls = append(f.calls, resyncTriggerCall{nodeID: nodeID, reportedAt: reportedAt})
}

func newTestManagerWithResyncIntentTrigger(t *testing.T, clock *fakeClock, trigger ResyncIntentTrigger) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, testLogger(), WithResyncIntentTrigger(trigger))
	if clock != nil {
		m.now = clock.now
	}
	return m
}

// TestHandleAssetInventoryNotifiesResyncIntentTriggerOnFreshReport proves
// handleAssetInventory calls [ResyncIntentTrigger.TriggerIfResyncIntentPrecedes]
// with the just-stored report's own ReportedAt after every live report -
// noderesync.go's deferred repair has nowhere else to run from.
func TestHandleAssetInventoryNotifiesResyncIntentTriggerOnFreshReport(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	trigger := &fakeResyncIntentTrigger{}
	m := newTestManagerWithResyncIntentTrigger(t, clock, trigger)

	env, err := mqttproto.NewAssetInventoryEnvelope(nil, "render-01", mqttproto.AssetInventoryPayload{Complete: true})
	if err != nil {
		t.Fatalf("build asset inventory envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: false})

	if len(trigger.calls) != 1 || trigger.calls[0].nodeID != "render-01" || !trigger.calls[0].reportedAt.Equal(clock.now()) {
		t.Fatalf("trigger calls = %+v, want exactly one for render-01 at %v", trigger.calls, clock.now())
	}
}

// TestHandleAssetInventoryDoesNotNotifyResyncIntentTriggerOnRetainedReplay
// proves the retained early-out (handleAssetInventory's own
// msg.Retained check, above) runs before this notification: a subscribe-
// time replay must never be mistaken for the fresh evidence noderesync.go's
// deferred repair is waiting on.
func TestHandleAssetInventoryDoesNotNotifyResyncIntentTriggerOnRetainedReplay(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	trigger := &fakeResyncIntentTrigger{}
	m := newTestManagerWithResyncIntentTrigger(t, clock, trigger)

	env, err := mqttproto.NewAssetInventoryEnvelope(nil, "render-01", mqttproto.AssetInventoryPayload{Complete: true})
	if err != nil {
		t.Fatalf("build asset inventory envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: true})

	if len(trigger.calls) != 0 {
		t.Fatalf("trigger calls = %+v, want none: a retained replay must not notify the resync intent trigger", trigger.calls)
	}
}

// TestHandleMessageLiveAssetInventoryIsStored proves a live (non-retained)
// asset inventory report reaches store.ReplaceNodeAssetInventory,
// stamped with the coordinator's OWN receipt time — never the envelope's
// SentAt — matching every other observed signal's ADR-011 provenance
// rule.
func TestHandleMessageLiveAssetInventoryIsStored(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	agentSentAt := clock.now().Add(-time.Hour) // deliberately stale, to prove it is ignored
	env, err := mqttproto.NewAssetInventoryEnvelope(func() time.Time { return agentSentAt }, "render-01", mqttproto.AssetInventoryPayload{
		Complete: true,
		Assets: []mqttproto.AssetInventoryEntry{
			{ContentHash: "sha256:aaa", Filename: "Opening.fseq", SizeBytes: 1024, VerifiedAt: agentSentAt},
		},
	})
	if err != nil {
		t.Fatalf("build asset inventory envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})

	report, err := m.store.GetNodeAssetReport(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get node asset report: %v", err)
	}
	if !report.Complete {
		t.Error("report.Complete = false, want true")
	}
	if !report.ReportedAt.Equal(clock.now()) {
		t.Errorf("report.ReportedAt = %v, want the coordinator's own receipt time %v (not the agent's SentAt %v)", report.ReportedAt, clock.now(), agentSentAt)
	}

	inv, err := m.store.GetNodeAssetInventory(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get node asset inventory: %v", err)
	}
	if len(inv) != 1 || inv[0].ContentHash != "sha256:aaa" || inv[0].RuntimeFilename != "Opening.fseq" {
		t.Errorf("inventory = %+v, want one entry for sha256:aaa/Opening.fseq", inv)
	}
}

// TestHandleMessageRetainedAssetInventoryIsNotPersisted is this seam's own
// load-bearing regression test — break it by removing handleAssetInventory's
// `if msg.Retained { return }` early-out and confirm it fails (see this
// task's own report) before trusting it. A retained replay (e.g. what this
// coordinator gets on every reconnect) must never manufacture a fresh
// report: store.NodeAssetReportRecord.ReportedAt has no "unknown" value to
// fall back to the way hello/lwt/health's nil ObservedAt does, so the only
// safe behavior is to leave whatever was already stored untouched.
func TestHandleMessageRetainedAssetInventoryIsNotPersisted(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	env, err := mqttproto.NewAssetInventoryEnvelope(nil, "render-01", mqttproto.AssetInventoryPayload{
		Complete: true,
		Assets:   []mqttproto.AssetInventoryEntry{{ContentHash: "sha256:aaa", Filename: "Opening.fseq", SizeBytes: 1024, VerifiedAt: clock.now()}},
	})
	if err != nil {
		t.Fatalf("build asset inventory envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: true,
	})

	if _, err := m.store.GetNodeAssetReport(context.Background(), "render-01"); err != store.ErrNodeAssetReportNotFound {
		t.Fatalf("GetNodeAssetReport() error = %v, want ErrNodeAssetReportNotFound: a retained replay must not create a report", err)
	}
}

// TestHandleMessageRetainedAssetInventoryDoesNotOverwriteExisting proves
// the same rule against a node that HAS reported before: a stale retained
// replay must not clobber a genuinely live report with older evidence
// wearing a fresh coordinator timestamp.
func TestHandleMessageRetainedAssetInventoryDoesNotOverwriteExisting(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	liveEnv, err := mqttproto.NewAssetInventoryEnvelope(nil, "render-01", mqttproto.AssetInventoryPayload{
		Complete: true,
		Assets:   []mqttproto.AssetInventoryEntry{{ContentHash: "sha256:aaa", Filename: "Opening.fseq", SizeBytes: 1024, VerifiedAt: clock.now()}},
	})
	if err != nil {
		t.Fatalf("build live envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, liveEnv), Retained: false})

	firstReport, err := m.store.GetNodeAssetReport(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get node asset report after live message: %v", err)
	}

	clock.t = clock.t.Add(time.Hour)
	retainedEnv, err := mqttproto.NewAssetInventoryEnvelope(nil, "render-01", mqttproto.AssetInventoryPayload{Complete: false, Reason: "stale replay"})
	if err != nil {
		t.Fatalf("build retained envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: assetsTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, retainedEnv), Retained: true})

	secondReport, err := m.store.GetNodeAssetReport(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get node asset report after retained message: %v", err)
	}
	if !secondReport.ReportedAt.Equal(firstReport.ReportedAt) || secondReport.Complete != firstReport.Complete {
		t.Errorf("report changed from %+v to %+v after a retained replay; it must be left untouched", firstReport, secondReport)
	}
}
