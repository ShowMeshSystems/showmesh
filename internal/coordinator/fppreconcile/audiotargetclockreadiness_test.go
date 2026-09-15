package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fakeClockLister is a minimal [ClockObservationsLister] a test can seed
// directly with the exact node.clock.ptp.* observations it wants
// audioTargetClockReadiness to see, without a real nodeclock.Store.
type fakeClockLister map[string][]observation.Observation

func (f fakeClockLister) NodeClockObservations(nodeID string) []observation.Observation {
	return f[nodeID]
}

func clockStateObservation(t *testing.T, nodeID, value string, observedAt time.Time, validFor time.Duration) observation.Observation {
	t.Helper()
	o, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		nodeClockStateSignal, value, observedAt,
		observation.WithValidFor(validFor),
	)
	if err != nil {
		t.Fatalf("build clock state observation: %v", err)
	}
	return o
}

// TestAudioTargetClockReadinessLockedIsSilent proves the common case, a
// locked target, produces no warning at all.
func TestAudioTargetClockReadinessLockedIsSilent(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4"}},
	})
	declareNode(t, st, "m4")
	putNodeOnline(t, st, "m4")
	now := time.Now()
	clock := fakeClockLister{"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)}}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty: m4's clock is locked", warning)
	}
}

// TestAudioTargetClockReadinessUnlockedWarnsNamingNode proves an unlocked
// target warns, naming the node, without failing readiness (the caller,
// not this function, decides Ready — this function never returns a
// [ReadinessCondition] at all).
func TestAudioTargetClockReadinessUnlockedWarnsNamingNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4"}},
	})
	declareNode(t, st, "m4")
	putNodeOnline(t, st, "m4")
	now := time.Now()
	clock := fakeClockLister{"m4": {clockStateObservation(t, "m4", "unsynchronized", now, 45*time.Second)}}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "m4") || !strings.Contains(warning, "not locked") {
		t.Errorf("warning = %q, want it to name m4 and say its clock is not locked", warning)
	}
}

// TestAudioTargetClockReadinessNoEvidenceWarnsDistinctly proves a target
// that has never reported any clock status at all warns with wording
// distinct from the stale-evidence case (TestAudioTargetClockReadinessStaleEvidenceWarnsDistinctly),
// per MANAGER DECISION 2's five distinct texts.
func TestAudioTargetClockReadinessNoEvidenceWarnsDistinctly(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4"}},
	})
	declareNode(t, st, "m4")
	putNodeOnline(t, st, "m4")
	now := time.Now()
	clock := fakeClockLister{} // m4 has never published a clock report.

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "m4") || !strings.Contains(warning, "no node.clock.ptp.state evidence") {
		t.Errorf("warning = %q, want it to name m4 and say no evidence has ever been reported", warning)
	}
	if strings.Contains(warning, "stale") {
		t.Errorf("warning = %q, want it distinct from the stale-evidence case", warning)
	}
}

// TestAudioTargetClockReadinessStaleEvidenceWarnsDistinctly proves a
// target whose last report has aged past its ValidFor warns with wording
// distinct from the no-evidence case above.
func TestAudioTargetClockReadinessStaleEvidenceWarnsDistinctly(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4"}},
	})
	declareNode(t, st, "m4")
	putNodeOnline(t, st, "m4")
	now := time.Now()
	observedAt := now.Add(-time.Hour)
	clock := fakeClockLister{"m4": {clockStateObservation(t, "m4", clockLockedValue, observedAt, 45*time.Second)}}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "m4") || !strings.Contains(warning, "stale") {
		t.Errorf("warning = %q, want it to name m4 and say its clock evidence is stale", warning)
	}
	if strings.Contains(warning, "no node.clock.ptp.state evidence") {
		t.Errorf("warning = %q, want it distinct from the no-evidence case", warning)
	}
}

// TestAudioTargetClockReadinessOfflineNodeWarnsDistinctly proves a target
// that is not currently reporting at all warns that its clock cannot be
// confirmed, distinct from both the no-evidence and stale-evidence texts,
// even when it once reported a perfectly good locked reading.
func TestAudioTargetClockReadinessOfflineNodeWarnsDistinctly(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4"}},
	})
	declareNode(t, st, "m4")
	putNodeOffline(t, st, "m4")
	now := time.Now()
	clock := fakeClockLister{"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)}}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "m4") || !strings.Contains(warning, "not currently reporting") {
		t.Errorf("warning = %q, want it to name m4 and say it is not currently reporting", warning)
	}
	if strings.Contains(warning, "stale") || strings.Contains(warning, "no node.clock.ptp.state evidence") {
		t.Errorf("warning = %q, want it distinct from the stale/no-evidence cases", warning)
	}
}

// TestAudioTargetClockReadinessIgnoresLTCOutput proves outputs.ltc's own
// single target is exempt: ADR-045 decision 2 keeps it single-node, so
// there is no cross-node alignment question for it, and its clock is
// never checked here even when it would otherwise warn.
func TestAudioTargetClockReadinessIgnoresLTCOutput(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		LTC: &config.ShowCueLTCOutput{Target: "m4"},
	})
	declareNode(t, st, "m4")
	putNodeOffline(t, st, "m4")
	now := time.Now()
	clock := fakeClockLister{}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty: outputs.ltc is exempt from this check", warning)
	}
}
