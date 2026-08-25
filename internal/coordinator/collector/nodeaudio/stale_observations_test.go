package nodeaudio

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestSignalSessionStaleReportsTheWireFlag proves audio_session.stale is
// its own signal, carrying [mqttproto.AudioSessionReport.Stale] straight
// through, always collected (never not_collected) regardless of value.
func TestSignalSessionStaleReportsTheWireFlag(t *testing.T) {
	for _, stale := range []bool{false, true} {
		st := NewStore()
		st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
			SessionID: "sess-1", State: "playing", Fault: "none", Stale: stale,
		}), time.Now())
		c := New(st)
		obs, _ := c.Poll(context.Background())

		got := findSessionObs(t, obs, SignalSessionStale)
		if got.Value != stale {
			t.Errorf("Stale=%v: SignalSessionStale value = %v, want %v", stale, got.Value, stale)
		}
		if got.Absence != "" {
			t.Errorf("Stale=%v: SignalSessionStale reports absence %q, want always collected", stale, got.Absence)
		}
	}
}

// TestStaleSessionSignalsUseTheSessionsOwnCollectedAt proves a session's
// own evidence age is what a reader actually sees: when the node reports
// CollectedAt (its own snapshot-collection time), the session's other
// signals (SignalSessionState here) are stamped with THAT time, not the
// report tick's own blanket ObservedAt -- otherwise a stale fallback's
// carried-forward State would look exactly as fresh as a genuinely
// current one, which is the defect this whole signal exists to close.
func TestStaleSessionSignalsUseTheSessionsOwnCollectedAt(t *testing.T) {
	collectedAt := sampleObservedAt.Add(-90 * time.Second) // well before this tick's own ObservedAt.
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
		Stale: true, CollectedAt: &collectedAt,
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findSessionObs(t, obs, SignalSessionState)
	if state.ObservedAt == nil || !state.ObservedAt.Equal(collectedAt) {
		t.Errorf("SignalSessionState.ObservedAt = %v, want the session's own CollectedAt %s, not the tick's blanket ObservedAt %s", state.ObservedAt, collectedAt, sampleObservedAt)
	}

	// The stale signal itself is always fresh -- this tick's own poll
	// genuinely found the session busy right now, regardless of how old
	// the state it is reporting alongside is.
	stale := findSessionObs(t, obs, SignalSessionStale)
	if stale.ObservedAt == nil || !stale.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("SignalSessionStale.ObservedAt = %v, want this tick's own ObservedAt %s", stale.ObservedAt, sampleObservedAt)
	}
}

// TestFreshSessionSignalsFallBackToTickObservedAtWithNoCollectedAt proves
// backward compatibility: a session report with no CollectedAt (an older
// node, or one that has never produced a snapshot) still stamps its
// signals with the report tick's own ObservedAt, exactly as before this
// signal existed.
func TestFreshSessionSignalsFallBackToTickObservedAtWithNoCollectedAt(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findSessionObs(t, obs, SignalSessionState)
	if state.ObservedAt == nil || !state.ObservedAt.Equal(sampleObservedAt) {
		t.Errorf("SignalSessionState.ObservedAt = %v, want the tick's own ObservedAt %s when CollectedAt is absent", state.ObservedAt, sampleObservedAt)
	}
}
