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
// two-node Cue whose targets are both locked, produces no warning at all.
func TestAudioTargetClockReadinessLockedIsSilent(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", clockLockedValue, now, 45*time.Second)},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if warning != "" {
		t.Errorf("warning = %q, want empty: both targets are locked", warning)
	}
}

// TestAudioTargetClockReadinessSingleNodeCueIsNeverChecked proves ADR-049
// decision 3's own boundary: a Cue reaching exactly one node picks no
// shared start instant at all, so an unlocked clock on that sole target
// changes nothing and must not warn.
func TestAudioTargetClockReadinessSingleNodeCueIsNeverChecked(t *testing.T) {
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
	if warning != "" {
		t.Errorf("warning = %q, want empty: cue-1 reaches only m4, so its clock state is not this condition's question", warning)
	}
}

// TestAudioTargetClockReadinessUnlockedWarnsNamingNode proves an unlocked
// target in a two-node Cue warns, naming the node, without failing
// readiness (the caller, not this function, decides Ready: this function
// never returns a [ReadinessCondition] at all).
func TestAudioTargetClockReadinessUnlockedWarnsNamingNode(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", "unsynchronized", now, 45*time.Second)},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") || !strings.Contains(warning, "not locked") {
		t.Errorf("warning = %q, want it to name pi and say its clock is not locked", warning)
	}
	if strings.Contains(warning, "m4") {
		t.Errorf("warning = %q, want it to NOT name m4: m4 is locked", warning)
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
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		// pi has never published a clock report.
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") || !strings.Contains(warning, "no node.clock.ptp.state evidence") {
		t.Errorf("warning = %q, want it to name pi and say no evidence has ever been reported", warning)
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
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	now := time.Now()
	observedAt := now.Add(-time.Hour)
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", clockLockedValue, observedAt, 45*time.Second)},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") || !strings.Contains(warning, "stale") {
		t.Errorf("warning = %q, want it to name pi and say its clock evidence is stale", warning)
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
	putProgramOnlyAudioNode(t, st, "pi")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	putNodeOnline(t, st, "m4")
	putNodeOffline(t, st, "pi")
	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", clockLockedValue, now, 45*time.Second)},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") || !strings.Contains(warning, "not currently reporting") {
		t.Errorf("warning = %q, want it to name pi and say it is not currently reporting", warning)
	}
	if strings.Contains(warning, "stale") || strings.Contains(warning, "no node.clock.ptp.state evidence") {
		t.Errorf("warning = %q, want it distinct from the stale/no-evidence cases", warning)
	}
}

// TestAudioTargetClockReadinessCollectsEveryOffendingNodeAcrossCues proves
// the scan never stops at the first problem: two different Cues in one
// playlist, each reaching two nodes, each with its own offending target,
// both get named in the combined warning.
func TestAudioTargetClockReadinessCollectsEveryOffendingNodeAcrossCues(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putProgramOnlyAudioNode(t, st, "zone")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
	})
	putCueWithOutputs(t, st, "cue-2", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "b", Targets: []string{"m4", "zone"}},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	declareNode(t, st, "zone")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	putNodeOnline(t, st, "zone")
	now := time.Now()
	clock := fakeClockLister{
		"m4":   {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi":   {clockStateObservation(t, "pi", "unsynchronized", now, 45*time.Second)},
		"zone": {clockStateObservation(t, "zone", "acquiring", now, 45*time.Second)},
	}
	p := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: "showmesh",
		Entries: []config.ShowPlaylistEntry{
			{ID: "e1", Cue: "cue-1"},
			{ID: "e2", Cue: "cue-2"},
		},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, p)
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") {
		t.Errorf("warning = %q, want it to name pi (cue-1's offending target)", warning)
	}
	if !strings.Contains(warning, "zone") {
		t.Errorf("warning = %q, want it to name zone (cue-2's offending target), not suppressed by cue-1's own problem", warning)
	}
	if !strings.Contains(warning, "cue-1") || !strings.Contains(warning, "cue-2") {
		t.Errorf("warning = %q, want both cues named", warning)
	}
}

// TestAudioTargetClockReadinessIgnoresLTCOutput proves outputs.ltc's own
// single target is exempt even when the SAME Cue's audio outputs make it a
// genuinely multi-node Cue this condition does scan: audio targets m4 and
// pi (pi deliberately unlocked, so a real finding is provable), plus an
// LTC target on a third, offline node whose own state would warn if it
// were not exempt. The offline LTC node must never be named.
func TestAudioTargetClockReadinessIgnoresLTCOutput(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putAudioNode(t, st, "m4")
	putProgramOnlyAudioNode(t, st, "pi")
	putProgramOnlyAudioNode(t, st, "ltc-only")
	putCueWithOutputs(t, st, "cue-1", "show-1", config.ShowCueOutputs{
		Audio: &config.ShowCueAudioOutput{Asset: "a", Targets: []string{"m4", "pi"}},
		LTC:   &config.ShowCueLTCOutput{Target: "ltc-only"},
	})
	declareNode(t, st, "m4")
	declareNode(t, st, "pi")
	declareNode(t, st, "ltc-only")
	putNodeOnline(t, st, "m4")
	putNodeOnline(t, st, "pi")
	putNodeOffline(t, st, "ltc-only")
	now := time.Now()
	clock := fakeClockLister{
		"m4": {clockStateObservation(t, "m4", clockLockedValue, now, 45*time.Second)},
		"pi": {clockStateObservation(t, "pi", "unsynchronized", now, 45*time.Second)},
	}

	warning, err := audioTargetClockReadiness(context.Background(), st, nil, clock, now, playlistWithCue("show-1", "cue-1"))
	if err != nil {
		t.Fatalf("audioTargetClockReadiness: %v", err)
	}
	if !strings.Contains(warning, "pi") || !strings.Contains(warning, "not locked") {
		t.Errorf("warning = %q, want it to name pi and say its clock is not locked: the audio targets are evaluated", warning)
	}
	if strings.Contains(warning, "ltc-only") {
		t.Errorf("warning = %q, want it to NOT name ltc-only: outputs.ltc is exempt from this check even though it is offline", warning)
	}
}
