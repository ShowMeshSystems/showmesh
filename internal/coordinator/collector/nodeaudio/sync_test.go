package nodeaudio

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fakeClockStatusSource answers with one node's clock report, or with
// "this node has never reported" when have is false.
type fakeClockStatusSource struct {
	payload mqttproto.ClockPayload
	have    bool
}

func (f fakeClockStatusSource) NodeClockStatus(string) (mqttproto.ClockPayload, bool) {
	return f.payload, f.have
}

func lockedClockPayload() mqttproto.ClockPayload {
	observedAt := sampleObservedAt
	return mqttproto.ClockPayload{
		State: "locked", Provider: "managed", Interface: "eth0",
		Domain: 0, DomainKnown: true,
		GrandmasterIdentity: "000fd4fffe06553a", GMKnown: true,
		Timescale: "ptp", OffsetNs: 214, OffsetKnown: true,
		ObservedAt: &observedAt,
	}
}

// syncObs polls one node whose engine runs on engineClockSource against
// the given clock report and returns its observations.
func syncObs(t *testing.T, engineClockSource string, clock fakeClockStatusSource) []observation.Observation {
	t.Helper()
	p := samplePayload()
	p.EngineClockSource = engineClockSource
	st := NewStore(WithClockStatusSource(clock))
	st.Put("audio-01", p, time.Now())
	obs, _ := New(st).Poll(context.Background())
	return obs
}

// TestSyncReportsLockedAgainstTheGrandmaster proves a node whose pipeline
// runs on its PTP hardware clock, with a locked provider, reports locked,
// what it follows, and the offset its own provider measured.
func TestSyncReportsLockedAgainstTheGrandmaster(t *testing.T) {
	obs := syncObs(t, "phc", fakeClockStatusSource{payload: lockedClockPayload(), have: true})

	if got := findObs(t, obs, SignalSyncState); got.Value != SyncStateLocked {
		t.Errorf("sync state = %v, want %q", got.Value, SyncStateLocked)
	}
	if got := findObs(t, obs, SignalSyncFollows); got.Value != "PTP 000fd4fffe06553a:0" {
		t.Errorf("sync follows = %v, want the grandmaster and domain", got.Value)
	}
	if got := findObs(t, obs, SignalSyncOffsetNs); got.Value != int64(214) {
		t.Errorf("sync offset = %v, want 214", got.Value)
	}
}

// TestSyncReportsFreeRunningOnASystemClockPipeline proves the pipeline's
// own clock decides: a locked provider does not make a pipeline running on
// GStreamer's system clock a locked output.
func TestSyncReportsFreeRunningOnASystemClockPipeline(t *testing.T) {
	obs := syncObs(t, "default", fakeClockStatusSource{payload: lockedClockPayload(), have: true})

	if got := findObs(t, obs, SignalSyncState); got.Value != SyncStateFreeRunning {
		t.Errorf("sync state = %v, want %q", got.Value, SyncStateFreeRunning)
	}
	if got := findObs(t, obs, SignalSyncFollows); got.Value != "" {
		t.Errorf("sync follows = %v, want blank while free-running", got.Value)
	}
	if got := findObs(t, obs, SignalSyncOffsetNs); got.Absence != observation.StateNotCollected {
		t.Errorf("sync offset absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
}

// TestSyncReportsAcquiring proves a provider that has not reached lock is
// reported as acquiring, with no grandmaster claimed for it.
func TestSyncReportsAcquiring(t *testing.T) {
	clock := lockedClockPayload()
	clock.State, clock.Reason = "acquiring", "no offset within tolerance yet"
	clock.OffsetKnown = false
	obs := syncObs(t, "phc", fakeClockStatusSource{payload: clock, have: true})

	if got := findObs(t, obs, SignalSyncState); got.Value != SyncStateAcquiring {
		t.Errorf("sync state = %v, want %q", got.Value, SyncStateAcquiring)
	}
	if got := findObs(t, obs, SignalSyncOffsetNs); got.Absence != observation.StateNotCollected {
		t.Errorf("sync offset absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
}

// TestSyncIsNotCollectedWithoutEvidence proves every input this coordinator
// does not have leaves all three signals not collected, never a
// free-running claim nobody checked.
func TestSyncIsNotCollectedWithoutEvidence(t *testing.T) {
	cases := []struct {
		name              string
		engineClockSource string
		clock             fakeClockStatusSource
	}{
		{"node reported no engine clock", "", fakeClockStatusSource{payload: lockedClockPayload(), have: true}},
		{"node never reported its clock", "phc", fakeClockStatusSource{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := syncObs(t, tc.engineClockSource, tc.clock)
			for _, sig := range []observation.SignalID{SignalSyncState, SignalSyncFollows, SignalSyncOffsetNs} {
				if got := findObs(t, obs, sig); got.Absence != observation.StateNotCollected {
					t.Errorf("%s absence = %q, want %q", sig, got.Absence, observation.StateNotCollected)
				}
			}
		})
	}
}

// TestSyncNoClockStatusSourceWiredIsNotCollected proves the nil default
// (no WithClockStatusSource option) reports not collected.
func TestSyncNoClockStatusSourceWiredIsNotCollected(t *testing.T) {
	p := samplePayload()
	p.EngineClockSource = "phc"
	st := NewStore()
	st.Put("audio-01", p, time.Now())
	obs, _ := New(st).Poll(context.Background())

	if got := findObs(t, obs, SignalSyncState); got.Absence != observation.StateNotCollected {
		t.Errorf("sync state absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
}

// TestSyncCarriesTheClockReportsOwnEvidenceTime proves every sync signal
// is stamped with the clock report's own ObservedAt, not the audio
// report's: a node still publishing audio while its clock report stopped
// hours ago must not read as locked right now.
func TestSyncCarriesTheClockReportsOwnEvidenceTime(t *testing.T) {
	clockObservedAt := sampleObservedAt.Add(-3 * time.Hour)
	clock := lockedClockPayload()
	clock.ObservedAt = &clockObservedAt
	obs := syncObs(t, "phc", fakeClockStatusSource{payload: clock, have: true})

	for _, sig := range []observation.SignalID{SignalSyncState, SignalSyncFollows, SignalSyncOffsetNs} {
		got := findObs(t, obs, sig)
		if got.ObservedAt == nil || !got.ObservedAt.Equal(clockObservedAt) {
			t.Errorf("%s ObservedAt = %v, want the clock report's own %v", sig, got.ObservedAt, clockObservedAt)
		}
	}
}

// TestSyncFollowsIsNotCollectedWithoutAGrandmaster proves a following node
// whose provider reported no grandmaster reports not collected rather than
// a present empty string, which would read as "follows nothing". Blank
// stays correct only while free-running.
func TestSyncFollowsIsNotCollectedWithoutAGrandmaster(t *testing.T) {
	clock := lockedClockPayload()
	clock.GMKnown, clock.GrandmasterIdentity = false, ""
	obs := syncObs(t, "phc", fakeClockStatusSource{payload: clock, have: true})

	got := findObs(t, obs, SignalSyncFollows)
	if got.Absence != observation.StateNotCollected {
		t.Errorf("sync follows absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
}
