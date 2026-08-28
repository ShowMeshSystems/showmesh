package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeClockSink is a minimal [ClockSink] a test can inspect, mirroring
// fakeAudioSink one report type over.
type fakeClockSink struct {
	calls []clockPut
}

type clockPut struct {
	nodeID     string
	payload    mqttproto.ClockPayload
	receivedAt time.Time
}

func (s *fakeClockSink) Put(nodeID string, payload mqttproto.ClockPayload, receivedAt time.Time) {
	s.calls = append(s.calls, clockPut{nodeID: nodeID, payload: payload, receivedAt: receivedAt})
}

func newTestManagerWithClockSink(t *testing.T, clock *fakeClock, sink ClockSink) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, testLogger(), WithClockSink(sink))
	if clock != nil {
		m.now = clock.now
	}
	return m
}

func clockTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "clock")
	if err != nil {
		t.Fatalf("clock topic: %v", err)
	}
	return topic
}

func sampleClockPayload() mqttproto.ClockPayload {
	observedAt := time.Unix(2000, 0).UTC()
	return mqttproto.ClockPayload{
		State: "locked", Provider: "external", Role: "follower", RoleKnown: true,
		Owner: "external (unidentified)", Interface: "eth0",
		Domain: 24, DomainKnown: true,
		GrandmasterIdentity: "3cecef.fffe.a1b2c3", GMKnown: true,
		Timescale: "ptp", OffsetNs: -42, OffsetKnown: true,
		ObservedAt: &observedAt,
	}
}

// TestHandleMessageLiveClockIsPushedWithReceiptTime proves a live
// delivery reaches the sink with the coordinator's OWN receipt time —
// never the envelope's SentAt — matching
// TestHandleMessageLiveAudioIsPushedWithReceiptTime one report type over.
func TestHandleMessageLiveClockIsPushedWithReceiptTime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	sink := &fakeClockSink{}
	m := newTestManagerWithClockSink(t, clock, sink)

	agentSentAt := clock.now().Add(-time.Hour) // deliberately stale, to prove it is ignored
	env, err := mqttproto.NewClockEnvelope(func() time.Time { return agentSentAt }, "clock-01", sampleClockPayload())
	if err != nil {
		t.Fatalf("build clock envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: clockTopic(t, "clock-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	call := sink.calls[0]
	if call.nodeID != "clock-01" {
		t.Errorf("nodeID = %q, want clock-01", call.nodeID)
	}
	if !call.receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v (not the agent's SentAt %v)", call.receivedAt, clock.now(), agentSentAt)
	}
	if call.payload.State != "locked" {
		t.Errorf("payload.State = %q, want locked", call.payload.State)
	}
}

// TestHandleMessageRetainedClockIsStillPushed proves a retained delivery
// reaches the sink too, matching
// TestHandleMessageRetainedAudioIsStillPushed one report type over.
func TestHandleMessageRetainedClockIsStillPushed(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	sink := &fakeClockSink{}
	m := newTestManagerWithClockSink(t, clock, sink)

	env, err := mqttproto.NewClockEnvelope(nil, "clock-01", sampleClockPayload())
	if err != nil {
		t.Fatalf("build clock envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: clockTopic(t, "clock-01"), Payload: mustEnvelopeBytes(t, env), Retained: true,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	if !sink.calls[0].receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v even for a retained delivery", sink.calls[0].receivedAt, clock.now())
	}
}

// TestHandleMessageMalformedClockIsDropped proves a malformed clock
// payload is skipped rather than pushed to the sink.
func TestHandleMessageMalformedClockIsDropped(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	sink := &fakeClockSink{}
	m := newTestManagerWithClockSink(t, clock, sink)

	badEnv, err := mqttproto.NewClockEnvelope(nil, "clock-01", sampleClockPayload())
	if err != nil {
		t.Fatalf("build clock envelope: %v", err)
	}
	// state:"" fails ClockPayload.Validate.
	badEnv.Payload = []byte(`{"provider":"external"}`)

	m.HandleMessage(broker.Message{
		Topic: clockTopic(t, "clock-01"), Payload: mustEnvelopeBytes(t, badEnv), Retained: false,
	})

	if len(sink.calls) != 0 {
		t.Errorf("sink got %d calls, want 0 for a malformed payload", len(sink.calls))
	}
}

// TestHandleMessageClockWithNoSinkRegisteredDoesNotPanic proves the
// default (WithClockSink never called) is a silent no-op, matching every
// other optional dependency in this package.
func TestHandleMessageClockWithNoSinkRegisteredDoesNotPanic(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock) // no WithClockSink option

	env, err := mqttproto.NewClockEnvelope(nil, "clock-01", sampleClockPayload())
	if err != nil {
		t.Fatalf("build clock envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: clockTopic(t, "clock-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})
	// Reaching this line without panicking is the assertion.
}
