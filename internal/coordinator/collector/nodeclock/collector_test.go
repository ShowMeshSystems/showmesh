package nodeclock

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func findObs(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("no observation found for signal %q", sig)
	return observation.Observation{}
}

func lockedPayload(observedAt time.Time) mqttproto.ClockPayload {
	lastStep := observedAt.Add(-time.Minute)
	return mqttproto.ClockPayload{
		State: "locked", Provider: "external", Role: "follower", RoleKnown: true,
		Owner: "external (unidentified)", Interface: "eth0",
		Domain: 24, DomainKnown: true,
		GrandmasterIdentity: "3cecef.fffe.a1b2c3", GMKnown: true,
		Timescale: "ptp", OffsetNs: -42, OffsetKnown: true,
		FrequencyPPM: 15.286, FrequencyPPMKnown: true,
		ClockClass: 248, ClockClassKnown: true,
		Timestamping: "hardware", TimestampingKnown: true,
		LockedSeconds: 120, LockedSecondsKnown: true,
		LastStepAt: &lastStep, LastStepNs: 1500, LastStepKnown: true,
		Mismatch:   false,
		ObservedAt: &observedAt,
	}
}

func TestCollectorPollRendersLockedPayload(t *testing.T) {
	st := NewStore()
	observedAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	receivedAt := observedAt.Add(time.Second)
	st.Put("node-1", lockedPayload(observedAt), receivedAt)

	c := New(st)
	if c.ID() != SourceName {
		t.Fatalf("ID() = %q, want %q", c.ID(), SourceName)
	}

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Fatalf("Poll() complete = false, want true (this collector never touches the network)")
	}
	if len(obs) != len(AllSignalIDs) {
		t.Fatalf("got %d observations, want %d (one per AllSignalIDs)", len(obs), len(AllSignalIDs))
	}

	state := findObs(t, obs, SignalState)
	if state.Value != "locked" {
		t.Errorf("state value = %v, want locked", state.Value)
	}
	if state.Source != SourceFor("node-1") {
		t.Errorf("source = %q, want %q", state.Source, SourceFor("node-1"))
	}

	reason := findObs(t, obs, SignalReason)
	if reason.Absence != "" {
		t.Errorf("reason: expected a current empty value while locked, got not_collected: %v", reason.Absence)
	}
	if reason.Value != "" {
		t.Errorf("reason value = %v, want empty string while locked", reason.Value)
	}

	role := findObs(t, obs, SignalRole)
	if role.Value != "follower" {
		t.Errorf("role value = %v, want follower", role.Value)
	}

	offset := findObs(t, obs, SignalOffsetNs)
	if offset.Value != int64(-42) {
		t.Errorf("offsetNs value = %v, want -42", offset.Value)
	}

	frequency := findObs(t, obs, SignalFrequencyPPM)
	if frequency.Value != 15.286 {
		t.Errorf("frequencyPpm value = %v, want 15.286", frequency.Value)
	}

	lockedSeconds := findObs(t, obs, SignalLockedSeconds)
	if lockedSeconds.Value != int64(120) {
		t.Errorf("lockedSeconds value = %v, want 120", lockedSeconds.Value)
	}

	mismatch := findObs(t, obs, SignalMismatch)
	if mismatch.Value != false {
		t.Errorf("mismatch value = %v, want false", mismatch.Value)
	}

	lastStepAt := findObs(t, obs, SignalLastStepAt)
	if lastStepAt.Absence != "" {
		t.Errorf("lastStepAt: expected a value, got not_collected: %v", lastStepAt.Absence)
	}
}

func TestCollectorPollNotLockedReportsReasonAndNoOffset(t *testing.T) {
	st := NewStore()
	observedAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	payload := mqttproto.ClockPayload{
		State: "acquiring", Reason: "not yet locked", Provider: "external",
		Timescale: "unknown", ObservedAt: &observedAt,
	}
	st.Put("node-1", payload, observedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())

	reason := findObs(t, obs, SignalReason)
	if reason.Value != "not yet locked" {
		t.Errorf("reason value = %v, want \"not yet locked\"", reason.Value)
	}

	offset := findObs(t, obs, SignalOffsetNs)
	if offset.Absence == "" {
		t.Errorf("offsetNs: expected not_collected while not locked, got a value")
	}

	frequency := findObs(t, obs, SignalFrequencyPPM)
	if frequency.Absence == "" {
		t.Errorf("frequencyPpm: expected not_collected when the payload never reported it, got a value")
	}
	if frequency.Reason == "" {
		t.Errorf("frequencyPpm: expected a fallback reason when the payload carried none, got empty")
	}

	if offset.Absence != observation.StateNotCollected {
		t.Errorf("offsetNs absence = %q, want %q: an unlocked clock still owes an offset", offset.Absence, observation.StateNotCollected)
	}
}

// TestNothingToMeasureIsNotApplicableAndGoesStale pins ADR-056 for the
// clock signals: no lock and no step are readings, and they age.
func TestNothingToMeasureIsNotApplicableAndGoesStale(t *testing.T) {
	st := NewStore()
	observedAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	st.Put("node-1", mqttproto.ClockPayload{
		State: "acquiring", Reason: "not yet locked", Provider: "external",
		Timescale: "unknown", ObservedAt: &observedAt,
	}, observedAt)
	obs, _ := New(st).Poll(context.Background())

	noStep := "No clock step has been seen since this node's clock provider started."
	want := map[observation.SignalID]string{
		SignalLockedSeconds: "This node's clock is not locked, so there is no lock duration.",
		SignalLastStepAt:    noStep,
		SignalLastStepNs:    noStep,
	}
	for sig, reason := range want {
		got := findObs(t, obs, sig)
		if state := got.StateAt(observedAt); state != observation.StateNotApplicable {
			t.Errorf("%s state = %q, want %q", sig, state, observation.StateNotApplicable)
		}
		if got.Reason != reason {
			t.Errorf("%s reason = %q, want %q", sig, got.Reason, reason)
		}
		if got.ObservedAt == nil || !got.ObservedAt.Equal(observedAt) {
			t.Errorf("%s observedAt = %v, want the report's own %s", sig, got.ObservedAt, observedAt)
		}
		if state := got.StateAt(observedAt.Add(DefaultValidFor + time.Second)); state != observation.StateStale {
			t.Errorf("%s state after the node went quiet = %q, want %q", sig, state, observation.StateStale)
		}
	}
}

// TestStepAndLockDurationAreValuesWhenTheyExist is the other half: the
// same signals carry a current value once their subject exists.
func TestStepAndLockDurationAreValuesWhenTheyExist(t *testing.T) {
	st := NewStore()
	observedAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	st.Put("node-1", lockedPayload(observedAt), observedAt)
	obs, _ := New(st).Poll(context.Background())

	for _, sig := range []observation.SignalID{SignalLockedSeconds, SignalLastStepAt, SignalLastStepNs} {
		if state := findObs(t, obs, sig).StateAt(observedAt); state != observation.StateCurrent {
			t.Errorf("%s state = %q, want %q", sig, state, observation.StateCurrent)
		}
	}
}

// TestCollectorPollUsesAgentFrequencyReasonWhenPresent proves a specific
// agent-supplied reason wins over the collector's own generic fallback --
// an operator on a node with a hardware clock but no declared PHC device
// gets told what to set, not just "unavailable".
func TestCollectorPollUsesAgentFrequencyReasonWhenPresent(t *testing.T) {
	st := NewStore()
	observedAt := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	payload := mqttproto.ClockPayload{
		State: "locked", Provider: "external", Timescale: "ptp",
		FrequencyPPMReason: "eth0 has a hardware clock, but no PHC device is declared for it. Set phcDevice in this node's clock settings to report its frequency.",
		ObservedAt:         &observedAt,
	}
	st.Put("node-1", payload, observedAt)

	c := New(st)
	obs, _ := c.Poll(context.Background())

	frequency := findObs(t, obs, SignalFrequencyPPM)
	if frequency.Reason != payload.FrequencyPPMReason {
		t.Errorf("reason = %q, want the agent's own reason %q", frequency.Reason, payload.FrequencyPPMReason)
	}
}

func TestStoreNodeClockObservationsUnknownNodeReturnsNil(t *testing.T) {
	st := NewStore()
	if obs := st.NodeClockObservations("no-such-node"); obs != nil {
		t.Errorf("expected nil for a node that never reported, got %d observations", len(obs))
	}
}

func TestSourceForAndNodeFromSourceRoundTrip(t *testing.T) {
	src := SourceFor("node-1")
	nodeID, ok := NodeFromSource(src)
	if !ok || nodeID != "node-1" {
		t.Errorf("NodeFromSource(%q) = %q/%v, want node-1/true", src, nodeID, ok)
	}
	if _, ok := NodeFromSource("bogus"); ok {
		t.Errorf("NodeFromSource(bogus) should report false")
	}
}
