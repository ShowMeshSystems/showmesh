package nodeaudio

import (
	"context"
	"math"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
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

// TestSessionFaultSignalsDistinguishAllSeven proves the distinct-faults rule at the
// observation surface: each of the seven named fault classes reports
// distinctly on SignalSessionFaultKind, and FaultReason is only ever
// not_collected when the fault kind is "none".
func TestSessionFaultSignalsDistinguishAllSeven(t *testing.T) {
	faults := []string{
		"pipeline_crash", "freeze", "decode_failure",
		"media_disappeared", "media_mismatch", "route_changed", "timing_authority_lost",
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

// TestSessionLTCClaimRefusedCarriesItsReason proves the surface this
// package exists to add: a session whose claim on its node's LTC run was
// refused reports SignalSessionLTCClaimState "refused" with
// SignalSessionLTCClaimReason present and non-empty — legible from the
// refused session's own evidence, never only from the node's log.
func TestSessionLTCClaimRefusedCarriesItsReason(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "show-b", State: "playing", Fault: "none",
		LTCClaimState: "refused", LTCClaimReason: "this node's one LTC run is held by session show-a",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findSessionObs(t, obs, SignalSessionLTCClaimState)
	if state.Value != "refused" {
		t.Errorf("ltc claim state = %v, want %q", state.Value, "refused")
	}
	reason := findSessionObs(t, obs, SignalSessionLTCClaimReason)
	if reason.Absence != "" {
		t.Errorf("ltc claim reason absence = %q, want a real value since the claim was refused", reason.Absence)
	}
	if reason.Value != "this node's one LTC run is held by session show-a" {
		t.Errorf("ltc claim reason = %v, want the stated refusal reason naming the holder", reason.Value)
	}
}

// TestSessionLTCClaimHeldReasonNotCollected proves the converse: a
// session that holds the run reports state "held" and its reason as
// not_collected, never an empty string masquerading as "no reason to
// give."
func TestSessionLTCClaimHeldReasonNotCollected(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "show-a", State: "playing", Fault: "none",
		LTCClaimState: "held",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findSessionObs(t, obs, SignalSessionLTCClaimState)
	if state.Value != "held" {
		t.Errorf("ltc claim state = %v, want %q", state.Value, "held")
	}
	reason := findSessionObs(t, obs, SignalSessionLTCClaimReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("ltc claim reason absence = %q, want %q", reason.Absence, observation.StateNotCollected)
	}
}

// TestSessionLTCClaimStateDefaultsToNone proves a session that never sent
// LTCClaimState (a node predating this signal, or a non-show session that
// never attempted a claim) reports the literal "none" -- never an empty
// string, matching SignalSessionFaultKind's identical zero-value rule.
func TestSessionLTCClaimStateDefaultsToNone(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "bg-1", State: "playing", Fault: "none",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	state := findSessionObs(t, obs, SignalSessionLTCClaimState)
	if state.Value != "none" {
		t.Errorf("ltc claim state = %v, want %q", state.Value, "none")
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

// TestSessionGainSignalsReportDecibelsNotLinearMultiplier proves the read
// side agrees with the write side (PR #134 made every operator-facing
// gain input decibels): an operator who typed -6 dB and a ceiling of
// 3 dB must see -6 and 3 on audio_session.gain.effective/.ceiling, never
// the agent's own linear multiplier (pkg/audio.Gain/Ceiling stay linear
// all the way to the engine -- only the coordinator's read boundary here
// converts, matching pkg/audio/gain.go's write-side boundary).
func TestSessionGainSignalsReportDecibelsNotLinearMultiplier(t *testing.T) {
	linearGain := float64(pkgaudio.GainFromDb(-6))
	linearCeiling := float64(pkgaudio.CeilingFromDb(3))

	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
		HasGain: true, Gain: linearGain,
		HasCeiling: true, Ceiling: linearCeiling,
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	gain := findSessionObs(t, obs, SignalSessionGain)
	gainDb, ok := gain.Value.(float64)
	if !ok {
		t.Fatalf("gain.effective value = %v (%T), want a float64 decibel value", gain.Value, gain.Value)
	}
	if math.Abs(gainDb-(-6)) > 0.01 {
		t.Errorf("gain.effective value = %v, want -6 (dB, what the operator entered) -- not the linear multiplier %v", gainDb, linearGain)
	}

	ceiling := findSessionObs(t, obs, SignalSessionGainCeiling)
	ceilingDb, ok := ceiling.Value.(float64)
	if !ok {
		t.Fatalf("gain.ceiling value = %v (%T), want a float64 decibel value", ceiling.Value, ceiling.Value)
	}
	if math.Abs(ceilingDb-3) > 0.01 {
		t.Errorf("gain.ceiling value = %v, want 3 (dB, what the operator entered) -- not the linear multiplier %v", ceilingDb, linearCeiling)
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

// TestSessionItemGapReportedWhenKnown proves the measured-gap pair reports
// a genuine value on both signal ids when the node reports one, with the
// successor's own engine-clock evidence time, never this call's own time.
func TestSessionItemGapReportedWhenKnown(t *testing.T) {
	gapAt := time.Date(2026, 8, 20, 3, 0, 0, 0, time.UTC)
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
		GapKnown: true, ItemGapMs: 137, ItemGapReason: "", ItemGapObservedAt: &gapAt,
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	ms := findSessionObs(t, obs, SignalSessionItemGapMs)
	if ms.Absence != "" {
		t.Errorf("item_gap_ms absence = %q, want a real value", ms.Absence)
	}
	if ms.Value != int64(137) {
		t.Errorf("item_gap_ms value = %v, want 137", ms.Value)
	}
	if ms.ObservedAt == nil || !ms.ObservedAt.Equal(gapAt) {
		t.Errorf("item_gap_ms observedAt = %v, want %v", ms.ObservedAt, gapAt)
	}

	reason := findSessionObs(t, obs, SignalSessionItemGapReason)
	if reason.Absence != "" {
		t.Errorf("item_gap.reason absence = %q, want a real value since the gap is known", reason.Absence)
	}
}

// TestSessionItemGapNotCollectedWhenUnknown proves the not-collected case:
// both signal ids report [observation.StateNotCollected] with the node's
// stated reason, never zero and never omitted.
func TestSessionItemGapNotCollectedWhenUnknown(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
		GapKnown: false, ItemGapReason: "session is stopped",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	ms := findSessionObs(t, obs, SignalSessionItemGapMs)
	if ms.Absence != observation.StateNotCollected {
		t.Errorf("item_gap_ms absence = %q, want %q", ms.Absence, observation.StateNotCollected)
	}
	if ms.Reason != "session is stopped" {
		t.Errorf("item_gap_ms reason = %q, want %q", ms.Reason, "session is stopped")
	}
	if ms.Value != nil {
		t.Errorf("item_gap_ms value = %v, want nil, never a fabricated zero", ms.Value)
	}

	reason := findSessionObs(t, obs, SignalSessionItemGapReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("item_gap.reason absence = %q, want %q", reason.Absence, observation.StateNotCollected)
	}
	if reason.Reason != "session is stopped" {
		t.Errorf("item_gap.reason reason = %q, want %q", reason.Reason, "session is stopped")
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

// TestRestoreSignalsReportQueuedBeforeTheFirstAutomaticAttempt reproduces
// a review-flagged honesty defect: a session with a restore genuinely
// queued, but not yet retried by the node's own automatic driver
// (RestoreAttempts still 0), must not be reported identically to a
// session with nothing queued at all. Gating on RestoreAttempts > 0
// alone cannot tell those two apart -- RestorePending is the
// authoritative signal, and this is exactly the window an operator most
// needs visibility into.
func TestRestoreSignalsReportQueuedBeforeTheFirstAutomaticAttempt(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "restore_pending", Fault: "other", FaultReason: "no audio engine bound yet",
		RestorePending: true, RestoreAttempts: 0, RestoreNextAttemptMs: 0, RestoreLastReason: "",
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	attempts := findSessionObs(t, obs, SignalSessionRestoreAttempts)
	if attempts.Absence != "" {
		t.Errorf("restore.attempts absence = %q, want collected (a restore IS queued, even with zero attempts so far)", attempts.Absence)
	}
	if attempts.Value != int64(0) {
		t.Errorf("restore.attempts value = %v, want 0", attempts.Value)
	}

	next := findSessionObs(t, obs, SignalSessionRestoreNextAttemptMs)
	if next.Absence != "" {
		t.Errorf("restore.next_attempt_ms absence = %q, want collected", next.Absence)
	}

	reason := findSessionObs(t, obs, SignalSessionRestoreLastReason)
	if reason.Absence != observation.StateNotCollected {
		t.Errorf("restore.last_reason absence = %q, want %q: no attempt has run yet, so there is no reason to report", reason.Absence, observation.StateNotCollected)
	}
}

// TestRestoreSignalsAllNotCollectedWhenNothingIsQueued is the negative
// case TestRestoreSignalsReportQueuedBeforeTheFirstAutomaticAttempt
// exists to distinguish: an ordinary session with no restore queued at
// all reports all three restore.* signals as not collected.
func TestRestoreSignalsAllNotCollectedWhenNothingIsQueued(t *testing.T) {
	st := NewStore()
	st.Put("audio-01", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none", RestorePending: false,
	}), time.Now())
	c := New(st)
	obs, _ := c.Poll(context.Background())

	for _, sig := range []observation.SignalID{SignalSessionRestoreAttempts, SignalSessionRestoreNextAttemptMs, SignalSessionRestoreLastReason} {
		o := findSessionObs(t, obs, sig)
		if o.Absence != observation.StateNotCollected {
			t.Errorf("%s absence = %q, want %q when no restore is queued", sig, o.Absence, observation.StateNotCollected)
		}
	}
}
