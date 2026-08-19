package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeAudioSink is a minimal [AudioSink] a test can inspect, mirroring
// fakeRenderSink one report type over.
type fakeAudioSink struct {
	calls []audioPut
}

type audioPut struct {
	nodeID     string
	payload    mqttproto.AudioPayload
	receivedAt time.Time
}

func (s *fakeAudioSink) Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time) {
	s.calls = append(s.calls, audioPut{nodeID: nodeID, payload: payload, receivedAt: receivedAt})
}

func newTestManagerWithAudioSink(t *testing.T, clock *fakeClock, sink AudioSink) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, testLogger(), WithAudioSink(sink))
	if clock != nil {
		m.now = clock.now
	}
	return m
}

func audioTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "audio")
	if err != nil {
		t.Fatalf("audio topic: %v", err)
	}
	return topic
}

func sampleAudioPayload() mqttproto.AudioPayload {
	observedAt := time.Unix(2000, 0).UTC()
	return mqttproto.AudioPayload{
		EngineAvailable:    true,
		HardwareEnumerated: true,
		DeviceAvailable:    true,
		OutputsCount:       1,
		ProgramAvailable:   true,
		LTCAvailable:       false,
		LTCReason:          "no route achieved 3 or more channels",
		Routes: []mqttproto.AudioRouteReport{
			{Device: "hw:CARD=PCH,DEV=0", Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
		},
		ObservedAt:         &observedAt,
		Sessions:           []mqttproto.AudioSessionReport{},
		LTCGeneratorState:  "stopped",
		LTCGeneratorReason: "no generator has ever been started on this node",
	}
}

// TestHandleMessageLiveAudioIsPushedWithReceiptTime proves a live delivery
// reaches the sink with the coordinator's OWN receipt time — never the
// envelope's SentAt — matching TestHandleMessageLiveRenderIsPushedWithReceiptTime
// one report type over.
func TestHandleMessageLiveAudioIsPushedWithReceiptTime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	sink := &fakeAudioSink{}
	m := newTestManagerWithAudioSink(t, clock, sink)

	agentSentAt := clock.now().Add(-time.Hour) // deliberately stale, to prove it is ignored
	env, err := mqttproto.NewAudioEnvelope(func() time.Time { return agentSentAt }, "audio-01", sampleAudioPayload())
	if err != nil {
		t.Fatalf("build audio envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: audioTopic(t, "audio-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	call := sink.calls[0]
	if call.nodeID != "audio-01" {
		t.Errorf("nodeID = %q, want audio-01", call.nodeID)
	}
	if !call.receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v (not the agent's SentAt %v)", call.receivedAt, clock.now(), agentSentAt)
	}
	if !call.payload.EngineAvailable {
		t.Errorf("payload.EngineAvailable = false, want true")
	}
}

// TestHandleMessageRetainedAudioIsStillPushed proves a retained delivery
// reaches the sink too — handleAudio's own doc comment explains why this
// differs from asset inventory's skip.
func TestHandleMessageRetainedAudioIsStillPushed(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	sink := &fakeAudioSink{}
	m := newTestManagerWithAudioSink(t, clock, sink)

	env, err := mqttproto.NewAudioEnvelope(nil, "audio-01", sampleAudioPayload())
	if err != nil {
		t.Fatalf("build audio envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: audioTopic(t, "audio-01"), Payload: mustEnvelopeBytes(t, env), Retained: true,
	})

	if len(sink.calls) != 1 {
		t.Fatalf("sink got %d calls, want 1", len(sink.calls))
	}
	if !sink.calls[0].receivedAt.Equal(clock.now()) {
		t.Errorf("receivedAt = %v, want the coordinator's own receipt time %v even for a retained delivery", sink.calls[0].receivedAt, clock.now())
	}
}

// TestHandleMessageMalformedAudioIsDropped proves a malformed audio
// payload is skipped rather than pushed to the sink.
func TestHandleMessageMalformedAudioIsDropped(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	sink := &fakeAudioSink{}
	m := newTestManagerWithAudioSink(t, clock, sink)

	badEnv, err := mqttproto.NewAudioEnvelope(nil, "audio-01", sampleAudioPayload())
	if err != nil {
		t.Fatalf("build audio envelope: %v", err)
	}
	// engineAvailable:false with no engineReason fails AudioPayload.Validate.
	badEnv.Payload = []byte(`{"engineAvailable":false,"routes":[]}`)

	m.HandleMessage(broker.Message{
		Topic: audioTopic(t, "audio-01"), Payload: mustEnvelopeBytes(t, badEnv), Retained: false,
	})

	if len(sink.calls) != 0 {
		t.Errorf("sink got %d calls, want 0 for a malformed payload", len(sink.calls))
	}
}

// TestHandleMessageAudioWithNoSinkRegisteredDoesNotPanic proves the default
// (WithAudioSink never called) is a silent no-op, matching every other
// optional dependency in this package.
func TestHandleMessageAudioWithNoSinkRegisteredDoesNotPanic(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock) // no WithAudioSink option

	env, err := mqttproto.NewAudioEnvelope(nil, "audio-01", sampleAudioPayload())
	if err != nil {
		t.Fatalf("build audio envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: audioTopic(t, "audio-01"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})
	// Reaching this line without panicking is the assertion.
}
