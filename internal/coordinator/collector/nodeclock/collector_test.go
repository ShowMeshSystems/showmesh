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
	if reason.Absence == "" {
		t.Errorf("reason: expected not_collected while locked, got a value")
	}

	role := findObs(t, obs, SignalRole)
	if role.Value != "follower" {
		t.Errorf("role value = %v, want follower", role.Value)
	}

	offset := findObs(t, obs, SignalOffsetNs)
	if offset.Value != int64(-42) {
		t.Errorf("offsetNs value = %v, want -42", offset.Value)
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

	lockedSeconds := findObs(t, obs, SignalLockedSeconds)
	if lockedSeconds.Absence == "" {
		t.Errorf("lockedSeconds: expected not_collected while not locked, got a value")
	}

	lastStepAt := findObs(t, obs, SignalLastStepAt)
	if lastStepAt.Absence == "" {
		t.Errorf("lastStepAt: expected not_collected (no step observed), got a value")
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
