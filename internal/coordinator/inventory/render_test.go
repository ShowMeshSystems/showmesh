package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeRenderSink is a minimal [RenderSink] a test can inspect, so this
// file's tests prove exactly what handleRender hands the sink without
// depending on internal/coordinator/collector/noderender's own real
// implementation.
type fakeRenderSink struct {
	calls []renderPut
}

type renderPut struct {
	nodeID     string
	payload    mqttproto.RenderPayload
	retained   bool
	receivedAt time.Time
}

func (s *fakeRenderSink) Put(nodeID string, payload mqttproto.RenderPayload, retained bool, receivedAt time.Time) {
	s.calls = append(s.calls, renderPut{nodeID: nodeID, payload: payload, retained: retained, receivedAt: receivedAt})
}

func newTestManagerWithRenderSink(t *testing.T, clock *fakeClock, sink RenderSink) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, testLogger(), WithRenderSink(sink))
	if clock != nil {
		m.now = clock.now
	}
	return m
}

func renderTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "render")
	if err != nil {
		t.Fatalf("render topic: %v", err)
	}
	return topic
}

func samplePayload() mqttproto.RenderPayload {
	return mqttproto.RenderPayload{
		GstLaunchPath:      "/usr/bin/gst-launch-1.0",
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:     "garage",
				PipelineState: mqttproto.RenderPipelineStateRunning,
				Since:         time.Unix(1000, 0).UTC(),
				ObservedAt:    time.Unix(2000, 0).UTC(),
			},
		},
	}
}

// TestHandleMessageLiveRenderIsPushedWithReceiptTime proves a live delivery
// reaches the sink with retained=false and the coordinator's OWN receipt
// time — never the envelope's SentAt.
func TestHandleMessageLiveRenderIsPushedWithReceiptTime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sink := &fakeRenderSink{}
	m := newTestManagerWithRenderSink(t, clock, sink)

	agentSentAt := clock.now().Add(-time.Hour) // deliberately stale, to prove it is ignored
	env, err := mqttproto.NewRenderEnvelope(func() time.Time { return agentSentAt }, "render-01", samplePayload())
	if err != nil {
		t.Fatalf("build render envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: renderTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	call := sink.calls[0]
	if call.nodeID != "render-01" {
		t.Errorf("nodeID = %q, want render-01", call.nodeID)
	}
	if call.retained {
		t.Errorf("retained = true, want false")
	}
	if !call.receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v (not the agent's SentAt %v)", call.receivedAt, clock.now(), agentSentAt)
	}
	if len(call.payload.Surfaces) != 1 || call.payload.Surfaces[0].SurfaceID != "garage" {
		t.Errorf("payload = %+v, want one surface for garage", call.payload)
	}
}

// TestHandleMessageRetainedRenderIsPushedAsRetained proves a retained
// delivery reaches the sink too (unlike asset inventory's skip — see
// handleRender's own doc comment for why) but flagged retained=true, so
// the sink (noderender.Store) can carry ADR-011's unknown-age rule through
// to the observation layer.
func TestHandleMessageRetainedRenderIsPushedAsRetained(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sink := &fakeRenderSink{}
	m := newTestManagerWithRenderSink(t, clock, sink)

	env, err := mqttproto.NewRenderEnvelope(nil, "render-01", samplePayload())
	if err != nil {
		t.Fatalf("build render envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: renderTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: true,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	if !sink.calls[0].retained {
		t.Errorf("retained = false, want true")
	}
	if !sink.calls[0].receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v even for a retained delivery (bookkeeping, not evidence)", sink.calls[0].receivedAt, clock.now())
	}
}

// TestHandleMessageMalformedRenderIsDropped proves a malformed render
// payload is skipped rather than pushed to the sink or crashing this
// package — matching every other handle* method's malformed-message
// contract.
func TestHandleMessageMalformedRenderIsDropped(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sink := &fakeRenderSink{}
	m := newTestManagerWithRenderSink(t, clock, sink)

	// The malformed payload this test actually exercises — a surface with
	// an empty SurfaceID, which fails RenderPayload.Validate — is built by
	// hand below and swapped in after construction, matching how a real
	// misbehaving agent would produce one on the wire. NewRenderEnvelope
	// itself validates (RenderPayload.Surfaces must be a non-nil slice, per
	// that field's own doc comment), so the envelope built here carries a
	// valid, explicitly-empty Surfaces to get past that call.
	badEnv, err := mqttproto.NewRenderEnvelope(nil, "render-01", mqttproto.RenderPayload{Surfaces: []mqttproto.RenderSurfaceReport{}})
	if err != nil {
		t.Fatalf("build render envelope: %v", err)
	}
	badEnv.Payload = []byte(`{"surfaces":[{"pipelineState":"running"}]}`) // surfaceId missing

	m.HandleMessage(broker.Message{
		Topic: renderTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, badEnv), Retained: false,
	})

	if len(sink.calls) != 0 {
		t.Errorf("sink got %d calls, want 0 for a malformed payload", len(sink.calls))
	}
}

// TestHandleMessageRenderWithNoSinkRegisteredDoesNotPanic proves the
// default (WithRenderSink never called) is a silent no-op, not a nil
// pointer panic — the same "an unwired capability degrades quietly"
// posture every optional dependency in this codebase follows.
func TestHandleMessageRenderWithNoSinkRegisteredDoesNotPanic(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock) // no WithRenderSink option

	env, err := mqttproto.NewRenderEnvelope(nil, "render-01", samplePayload())
	if err != nil {
		t.Fatalf("build render envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: renderTopic(t, "render-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})
	// Reaching this line without panicking is the assertion.
}
