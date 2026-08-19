package nodeaudio

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// samplePayloadWithSession attaches one AudioSessionReport to
// samplePayload's discovery evidence — a node can report both at once.
func samplePayloadWithSession(sess mqttproto.AudioSessionReport) mqttproto.AudioPayload {
	p := samplePayload()
	p.Sessions = []mqttproto.AudioSessionReport{sess}
	return p
}

// TestSessionObservationsUseAudioSessionKindAndSessionID proves the
// resource attribution rule the identifier register states: a session's
// observations attach to [observation.ResourceAudioSession] with the
// SESSION id as the resource id, never the node id.
func TestSessionObservationsUseAudioSessionKindAndSessionID(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	}), time.Now())

	c := New(st)
	obs, _ := c.Poll(context.Background())
	state := findSessionObs(t, obs, SignalSessionState)
	if state.Resource.Kind != observation.ResourceAudioSession || state.Resource.ID != "sess-1" {
		t.Errorf("resource = %+v, want kind=audio_session id=sess-1", state.Resource)
	}
	if state.Value != "playing" {
		t.Errorf("state value = %v, want %q", state.Value, "playing")
	}
}

func findSessionObs(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig && o.Resource.Kind == observation.ResourceAudioSession {
			return o
		}
	}
	t.Fatalf("no audio_session observation found for signal %q among %d observations", sig, len(obs))
	return observation.Observation{}
}

// TestSessionFaultSignalsDistinguishAllSix proves the distinct-faults rule at the
// observation surface: each of the six named fault classes reports
// distinctly on SignalSessionFaultKind, and FaultReason is only ever
// not_collected when the fault kind is "none".
func TestSessionFaultSignalsDistinguishAllSix(t *testing.T) {
	faults := []string{
		"pipeline_crash", "freeze", "decode_failure",
		"media_disappeared", "route_changed", "timing_authority_lost",
	}
	seen := map[string]bool{}
	for _, f := range faults {
		st := NewStore()
		st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
			SessionID: "sess-1", State: "failed", Fault: f, FaultReason: "injected: " + f,
		}), time.Now())
		c := New(st)
		obs, _ := c.Poll(context.Background())

		kind := findSessionObs(t, obs, SignalSessionFaultKind)
		if kind.Value != f {
			t.Errorf("fault kind = %v, want %q", kind.Value, f)
		}
		reason := findSessionObs(t, obs, SignalSessionFaultReason)
		if reason.Absence != "" {
			t.Errorf("fault %q: reason absence = %q, want a real value since a fault is in effect", f, reason.Absence)
		}
		if reason.Value != "injected: "+f {
			t.Errorf("fault %q: reason value = %v, want %q", f, reason.Value, "injected: "+f)
		}

		stateObs := findSessionObs(t, obs, SignalSessionState)
		if stateObs.Value == "stopped" {
			t.Errorf("fault %q: state collapsed into \"stopped\"", f)
		}
		if seen[f] {
			t.Fatalf("fault %q checked twice", f)
		}
		seen[f] = true
	}
}

// TestSessionFaultNoneReportsReasonNotCollected proves the converse: no
// standing fault means FaultKind is the literal "none" and FaultReason is
// not_collected with a reason — never omitted, never an empty string
// masquerading as "nothing to report".
func TestSessionFaultNoneReportsReasonNotCollected(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	kind := findSessionObs(t, obs, SignalSessionFaultKind)
	if kind.Value != "none" {
		t.Errorf("fault kind = %v, want %q", kind.Value, "none")
	}
	reason := findSessionObs(t, obs, SignalSessionFaultReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("fault reason absence = %q, want %q", reason.Absence, observation.StateNotCollected)
	}
	if reason.Reason == "" {
		t.Error("fault reason's own Reason is empty, want a stated explanation")
	}
}

// TestSessionPositionUnknownIsNotCollectedNeverStale proves a
// mid-discontinuity session (PositionKnown=false) reports position as
// not_collected — never a stale prior reading presented as current, and
// never silently omitted.
func TestSessionPositionUnknownIsNotCollectedNeverStale(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "paused", Fault: "none", PositionKnown: false,
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	pos := findSessionObs(t, obs, SignalSessionPositionMs)
	if pos.Absence != observation.StateNotCollected {
		t.Errorf("position absence = %q, want %q", pos.Absence, observation.StateNotCollected)
	}
}

// TestSessionStateReasonDrawsPlayingClaimDistinction proves AUDIO-ENGINE section 15's rule:
// audio_session.state.reason states the claim-not-proof distinction
// specifically for Playing (and Paused), never for a state that carries
// no such ambiguity.
func TestSessionStateReasonDrawsPlayingClaimDistinction(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	reason := findSessionObs(t, obs, SignalSessionStateReason)
	s, ok := reason.Value.(string)
	if !ok || s == "" {
		t.Fatalf("state reason value = %v, want a non-empty string", reason.Value)
	}
}

// TestClockAlignmentAlwaysNotCollected proves nothing in this
// seam ever reports program-to-LTC alignment as measured, regardless of
// program/LTC bus state.
func TestClockAlignmentAlwaysNotCollected(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayload(), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	align := findObs(t, obs, SignalClockAlignment)
	if align.Absence != observation.StateNotCollected {
		t.Errorf("clock alignment absence = %q, want %q, never a value", align.Absence, observation.StateNotCollected)
	}
	if align.Value != nil {
		t.Errorf("clock alignment value = %v, want nil (never inferred)", align.Value)
	}
	if align.Reason == "" {
		t.Error("clock alignment reason is empty, want a stated explanation")
	}
}
