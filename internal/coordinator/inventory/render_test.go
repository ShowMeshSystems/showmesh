package inventory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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

// payloadWithRestartCount builds a one-surface render payload for
// surfaceID with the given RestartCount and PipelineState — the shape the
// restart-event tests below drive through successive HandleMessage calls.
func payloadWithRestartCount(surfaceID string, restartCount int64, state string) mqttproto.RenderPayload {
	return mqttproto.RenderPayload{
		GstLaunchPath:      "/usr/bin/gst-launch-1.0",
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:     surfaceID,
				PipelineState: state,
				Reason:        "crashed: exit status 1",
				Since:         time.Unix(1000, 0).UTC(),
				RestartCount:  restartCount,
				ObservedAt:    time.Unix(2000, 0).UTC(),
			},
		},
	}
}

func deliverRender(t *testing.T, m *Manager, nodeID string, payload mqttproto.RenderPayload, retained bool) {
	t.Helper()
	env, err := mqttproto.NewRenderEnvelope(nil, nodeID, payload)
	if err != nil {
		t.Fatalf("build render envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: renderTopic(t, nodeID), Payload: mustEnvelopeBytes(t, env), Retained: retained})
}

func countRenderEvents(t *testing.T, m *Manager) []store.EventRecord {
	t.Helper()
	events, _, err := m.store.ListEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	var out []store.EventRecord
	for _, e := range events {
		if e.Category == "render_pipeline" {
			out = append(out, e)
		}
	}
	return out
}

// TestRenderRestartCountFirstObservationAppendsNoEvent proves a surface's
// very first render report — including a retained replay at coordinator
// startup — never manufactures a restart event out of a restart count that
// predates this coordinator process. This is the regression guard for
// decision 4 in the task: a retained report replayed at startup must not
// be treated as evidence of a restart just happened.
func TestRenderRestartCountFirstObservationAppendsNoEvent(t *testing.T) {
	m := newTestManagerWithRenderSink(t, nil, &fakeRenderSink{})

	// A retained report already showing restartCount=7 — i.e. this surface
	// restarted seven times before this coordinator process ever existed.
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 7, mqttproto.RenderPipelineStateRunning), true)

	if got := countRenderEvents(t, m); len(got) != 0 {
		t.Fatalf("render events after first-ever (retained) observation = %d, want 0; got %+v", len(got), got)
	}
}

// TestRenderRestartCountIncreaseAppendsWarningEvent proves a genuine
// forward increase in RestartCount, after a baseline has been established,
// appends exactly one warning-severity render_pipeline event naming the
// surface and node.
func TestRenderRestartCountIncreaseAppendsWarningEvent(t *testing.T) {
	m := newTestManagerWithRenderSink(t, nil, &fakeRenderSink{})

	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 1, mqttproto.RenderPipelineStateRunning), false)
	if got := countRenderEvents(t, m); len(got) != 0 {
		t.Fatalf("render events after baseline observation = %d, want 0", len(got))
	}

	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 2, mqttproto.RenderPipelineStateRunning), false)

	got := countRenderEvents(t, m)
	if len(got) != 1 {
		t.Fatalf("render events after restart count increase = %d, want 1; got %+v", len(got), got)
	}
	ev := got[0]
	if ev.Severity != "warning" {
		t.Errorf("Severity = %q, want warning", ev.Severity)
	}
	if ev.Resource.Kind != observation.ResourceSurface || ev.Resource.ID != "garage" {
		t.Errorf("Resource = %+v, want surface/garage", ev.Resource)
	}
	if ev.Summary != `render pipeline for surface "garage" on node "render-01" restarted` {
		t.Errorf("Summary = %q", ev.Summary)
	}
	var details map[string]any
	if err := json.Unmarshal(ev.Details, &details); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	if details["restartCount"] != float64(2) {
		t.Errorf("details[restartCount] = %v, want 2", details["restartCount"])
	}
	if details["nodeId"] != "render-01" {
		t.Errorf("details[nodeId] = %v, want render-01", details["nodeId"])
	}

	// A repeated report at the SAME count is a no-op: still exactly one
	// event.
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 2, mqttproto.RenderPipelineStateRunning), false)
	if got := countRenderEvents(t, m); len(got) != 1 {
		t.Fatalf("render events after an unchanged repeat = %d, want still 1", len(got))
	}
}

// TestRenderRestartCountDecreaseIsSilentlyRebaselined proves the agent
// -restart case from the task's decision 1: RestartCount is scoped to one
// pipeline process lifetime, so it resets to zero when the agent process
// itself restarts. A decrease must never be read as a restart (it is not
// one) and must not corrupt bookkeeping so that the NEXT genuine restart
// after the reset goes undetected.
func TestRenderRestartCountDecreaseIsSilentlyRebaselined(t *testing.T) {
	m := newTestManagerWithRenderSink(t, nil, &fakeRenderSink{})

	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 5, mqttproto.RenderPipelineStateRunning), false)
	// Agent process restarts; the supervisor's counter resets to 0 for the
	// new pipeline lifetime.
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 0, mqttproto.RenderPipelineStateRunning), false)
	if got := countRenderEvents(t, m); len(got) != 0 {
		t.Fatalf("render events after a restart-count decrease = %d, want 0 (no event, silent rebaseline)", len(got))
	}

	// A genuine restart after the reset must still be detected.
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 1, mqttproto.RenderPipelineStateRunning), false)
	if got := countRenderEvents(t, m); len(got) != 1 {
		t.Fatalf("render events after the post-reset restart = %d, want 1", len(got))
	}
}

// TestRenderFailedLockoutFirstObservationAppendsNoEvent mirrors the
// restart-count first-observation guard for the failed-lockout event: a
// retained replay showing a surface already stuck in "failed" must not be
// read as a transition that just happened.
func TestRenderFailedLockoutFirstObservationAppendsNoEvent(t *testing.T) {
	m := newTestManagerWithRenderSink(t, nil, &fakeRenderSink{})

	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 5, mqttproto.RenderPipelineStateFailed), true)

	if got := countRenderEvents(t, m); len(got) != 0 {
		t.Fatalf("render events after first-ever (retained) failed observation = %d, want 0; got %+v", len(got), got)
	}
}

// TestRenderFailedLockoutTransitionAppendsCriticalEvent proves decision 5:
// a surface entering the supervisor's failed lockout — the case the
// restart machinery gave up, which is arguably more important than a
// restart that succeeded — appends its own critical-severity event, and
// staying failed across repeated reports does not repeat it.
func TestRenderFailedLockoutTransitionAppendsCriticalEvent(t *testing.T) {
	m := newTestManagerWithRenderSink(t, nil, &fakeRenderSink{})

	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 4, mqttproto.RenderPipelineStateRunning), false)
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 5, mqttproto.RenderPipelineStateFailed), false)

	got := countRenderEvents(t, m)
	// The RestartCount also moved 4->5 here, so a warning restart event AND
	// a critical lockout event are both expected — they are not mutually
	// exclusive; a lockout is very often reached mid-restart.
	var critical, warning int
	for _, ev := range got {
		switch ev.Severity {
		case "critical":
			critical++
		case "warning":
			warning++
		}
	}
	if critical != 1 {
		t.Fatalf("critical render events = %d, want 1; got %+v", critical, got)
	}
	if warning != 1 {
		t.Fatalf("warning render events = %d, want 1; got %+v", warning, got)
	}

	// Staying failed across another report must not repeat the lockout
	// event (matching observeLiveness's identical no-op-on-unchanged rule).
	deliverRender(t, m, "render-01", payloadWithRestartCount("garage", 5, mqttproto.RenderPipelineStateFailed), false)
	got = countRenderEvents(t, m)
	critical = 0
	for _, ev := range got {
		if ev.Severity == "critical" {
			critical++
		}
	}
	if critical != 1 {
		t.Fatalf("critical render events after unchanged repeat = %d, want still 1", critical)
	}
}
