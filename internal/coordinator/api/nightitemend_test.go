package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// TestNightNudgeAtItemEndPollsOnceTheSequenceShouldHaveEnded proves the live
// show asks for a fresh FPP poll only after the playing sequence's reported
// time left has run out.
func TestNightNudgeAtItemEndPollsOnceTheSequenceShouldHaveEnded(t *testing.T) {
	collected := testNow
	observedAt := collected
	nudger := &recordingNudger{accept: true}
	h := &handlers{deps: Dependencies{
		Observations: &fakeObservationLister{obs: []observation.Observation{{
			Resource: observation.ResourceRef{Kind: observation.ResourceFPP, ID: "fpp-main"},
			Signal:   observation.SignalID(nightSignalPositionRemainingSeconds), Value: int64(5),
			ObservedAt: &observedAt, CollectedAt: collected, Source: "fpp-rest",
			Quality: observation.QualityDirect, ValidFor: time.Minute,
		}}},
		Nudger: nudger,
	}}

	h.nightNudgeAtItemEnd(context.Background(), collected.Add(4*time.Second), "fpp-main")
	if got := nudger.callsFor("fpp-main"); got != 0 {
		t.Fatalf("nudged %d times with time left on the sequence, want 0", got)
	}
	h.nightNudgeAtItemEnd(context.Background(), collected.Add(6*time.Second), "fpp-main")
	if got := nudger.callsFor("fpp-main"); got != 1 {
		t.Fatalf("nudged %d times after the sequence should have ended, want 1", got)
	}
}
