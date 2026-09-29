package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The second review found two ways the rejoin could act where it must
// not: over an operator who stopped the bed by hand, and forever against
// a node whose start keeps failing. Both are about the coordinator
// knowing when to stop, which matters more on a show night than any
// recovery it can perform.

// operatorAudioCommandForTest records the desired-state row an operator's
// own audio.session.stop or clear leaves behind: the ordinary audio
// dispatch path writes one for every command that succeeds, whoever sent
// it, and writes no night step at all.
func operatorAudioCommandForTest(t *testing.T, st *store.Store, nodeID, sessionID string, revision uint64) {
	t.Helper()
	if err := st.PutAudioSession(context.Background(), store.AudioSessionRecord{
		ID: sessionID, NodeID: nodeID, DesiredJSON: `{}`, Revision: revision,
	}); err != nil {
		t.Fatalf("put audio session desired state: %v", err)
	}
}

// bedLatestStepRevisionForTest is the revision of nodeID's own latest bed
// step, which is what an operator command has to outrank to be seen.
func bedLatestStepRevisionForTest(t *testing.T, h *handlers, rec store.NightSessionRecord, nodeID string) int64 {
	t.Helper()
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, ok := nightBackgroundAudioLatestStepForNode(history, nodeID)
	if !ok {
		t.Fatalf("node %q has no bed steps", nodeID)
	}
	return latest.Row.ActionRevision
}

// TestNightBedRejoin_LeavesABedAnOperatorStoppedAlone is the first
// blocking case: an operator stop reaches the node through the ordinary
// audio dispatch path, which writes no night step, so this controller's
// ledger still reads "started and confirmed" while the node correctly
// reports stopped. Manual control wins.
func TestNightBedRejoin_LeavesABedAnOperatorStoppedAlone(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateStopped), at)...))

	operatorAudioCommandForTest(t, st, "node-b", sessionID, uint64(bedLatestStepRevisionForTest(t, h, rec, "node-b")+1))

	before := len(pub.dispatchedSnapshot())
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	if got := len(pub.dispatchedSnapshot()); got != before {
		t.Fatalf("tick dispatched %d command(s) over an operator's own stop, want 0", got-before)
	}

	got := nightBedNodesNotPlaying(context.Background(), h.deps, rec, at, bedOutboxRowsForTest(t, h, rec))
	if len(got) != 1 || got[0].NodeID != "node-b" {
		t.Fatalf("nodesNotPlaying = %+v, want node-b named", got)
	}
	if !strings.Contains(got[0].Reason, "outside this night's own controls") {
		t.Fatalf("node-b reason = %q, want it to say the music was stopped from outside this night's controls", got[0].Reason)
	}
	if strings.Contains(got[0].Reason, "on its next check") {
		t.Fatalf("node-b reason = %q, want no promise of a recovery that will not happen", got[0].Reason)
	}
}

// TestNightBedRejoin_LeavesABedAnOperatorClearedAlone is the same rule
// through the absent-session path: a clear removes the session from the
// node entirely, which otherwise looks exactly like an agent that lost it.
func TestNightBedRejoin_LeavesABedAnOperatorClearedAlone(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	// Cleared: the node reports audio and no bed session at all.
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", at, at)})

	operatorAudioCommandForTest(t, st, "node-b", sessionID, uint64(bedLatestStepRevisionForTest(t, h, rec, "node-b")+1))

	before := len(pub.dispatchedSnapshot())
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	if got := len(pub.dispatchedSnapshot()); got != before {
		t.Fatalf("tick dispatched %d command(s) over an operator's own clear, want 0", got-before)
	}
}

// TestNightBedRejoin_RejoinsWhenNothingOutsideThisLedgerTouchedTheSession
// is the guard's own counterpart: the SAME reported state with no command
// from outside the ledger is still a lost bed and is still recovered, so
// the operator check cannot be what silently disables the whole feature.
func TestNightBedRejoin_RejoinsWhenNothingOutsideThisLedgerTouchedTheSession(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateStopped), at)...))

	before := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	h.nightAdvanceBackgroundAudio(context.Background(), at, rec)
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != before+1 {
		t.Fatalf("bed applies = %d, want one more than %d: nothing outside this ledger touched the session", got, before)
	}
}

// TestNightBedRejoin_StopsAfterOneRejoinWhoseStartFailed is the second
// blocking case. A start that FAILS on the node leaves the session
// reporting failed, and the rejoin's own start fails the same way, so
// without a bound this repeats for the rest of the night. One attempt is
// the end of it.
func TestNightBedRejoin_StopsAfterOneRejoinWhoseStartFailed(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateFailed), at)...))

	// node-b's start fails on the node, exactly as a missing asset makes
	// it fail: the session reports failed and stays there.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.start": {Outcome: mqttproto.OutcomeConfirmed, Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.start",
			Value:  map[string]any{"sessionId": sessionID, "outcome": string(pkgaudio.OutcomeFailed), "reason": "media not ready: asset bed-1 is not present"},
		}},
	}

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	for i := 0; i < 20; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	extra := countDispatchedActionForSession(pub, "audio.session.apply", sessionID) - applies
	if extra != 1 {
		t.Fatalf("bed applies over twenty ticks = %d, want exactly 1: one rejoin whose start failed is the end of it", extra)
	}

	got := nightBedNodesNotPlaying(context.Background(), h.deps, rec, at, bedOutboxRowsForTest(t, h, rec))
	if len(got) != 1 || !strings.Contains(got[0].Reason, "did not start") {
		t.Fatalf("nodesNotPlaying = %+v, want node-b named as already tried and not starting", got)
	}
}

// TestNightBedRejoin_StopsAfterOneRejoinWhoseStartTimedOut is the same
// bound for a start the node never answered: unconfirmable is not
// confirmed, so it must not feed another attempt either.
func TestNightBedRejoin_StopsAfterOneRejoinWhoseStartTimedOut(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateReady), at)...))

	// No evidence in the result at all: the coordinator's own
	// mapResultOutcome reads that as unconfirmable, which is what a start
	// the node never answered leaves behind.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.start": {Outcome: mqttproto.OutcomeConfirmed, Reason: "the node sent no session evidence"},
	}

	applies := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	for i := 0; i < 20; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	extra := countDispatchedActionForSession(pub, "audio.session.apply", sessionID) - applies
	if extra != 1 {
		t.Fatalf("bed applies over twenty ticks = %d, want exactly 1: a start the node never confirmed is not another loss to recover", extra)
	}
}

// TestNightBedRejoin_AFailedStartIsNotItselfALoss is the narrower half of
// the same fix: a node whose latest step FAILED is not a node that lost a
// playing bed, whatever it reports, so it never starts a rejoin at all.
func TestNightBedRejoin_AFailedStartIsNotItselfALoss(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	steps := nightBackgroundAudioStepsForNode(history, "node-b")
	latest := steps[len(steps)-1]
	latest.Row.Outcome = nightCueOutcomeFailed
	steps[len(steps)-1] = latest

	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateFailed), at)...))

	if _, lost := nightBedNodeLostSession(audio, at, "node-b", sessionID, steps); lost {
		t.Fatal("a node whose own latest step failed read as having lost a playing bed, want left to the ordinary rules")
	}
}

// TestNightBedStepReasons_CarryNoControllerBookkeeping keeps every
// operator-facing step reason free of this controller's own flags, which
// now ride the cue name instead.
func TestNightBedStepReasons_CarryNoControllerBookkeeping(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	at := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, at)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", at, at)})
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, at, 12)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	sawRejoin, sawJoinStart := false, false
	for _, row := range nightBackgroundAudioSteps(history) {
		sawRejoin = sawRejoin || row.Step.Rejoin
		sawJoinStart = sawJoinStart || row.Step.WithPoint
		for _, tag := range []string{"bedRejoin=", "bedStartPoint=", "bedBookmark="} {
			if strings.Contains(row.Row.OutcomeReason, tag) {
				t.Errorf("step %q reason carries %q, which an operator reads verbatim: %q", row.Row.CueName, tag, row.Row.OutcomeReason)
			}
		}
	}
	if !sawRejoin || !sawJoinStart {
		t.Fatalf("expected both a rejoin apply and a start carrying a point in history; rejoin=%v joinStart=%v", sawRejoin, sawJoinStart)
	}
}
