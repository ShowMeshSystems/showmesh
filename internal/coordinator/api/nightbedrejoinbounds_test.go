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
		t.Fatalf("node-b reason = %q, want it to say the music was changed from outside this night's controls", got[0].Reason)
	}
	if strings.Contains(got[0].Reason, "was stopped") {
		t.Fatalf("node-b reason = %q, want it not to claim the outside command was a stop: any outside command counts", got[0].Reason)
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

	for i := 0; i < 20; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	if got := bedRejoinApplyCountForTest(t, h, rec, "node-b"); got != 1 {
		t.Fatalf("node-b rejoin applies over twenty ticks = %d, want exactly 1: one rejoin whose start failed is the end of it", got)
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

	for i := 0; i < 20; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Duration(i)*time.Second), rec)
	}
	if got := bedRejoinApplyCountForTest(t, h, rec, "node-b"); got != 1 {
		t.Fatalf("node-b rejoin applies over twenty ticks = %d, want exactly 1: a start the node never confirmed is not another loss to recover", got)
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

// TestNightBedRejoin_ASuccessfulRejoinDoesNotDisableLaterRecovery is the
// third review's own case. A multi-node rejoin starts with a point, the
// node can legitimately refuse that point, and the designed retry without
// it confirms: that rejoin ENDED WITH THE BED PLAYING, so a later loss on
// the same node must be recovered like any other.
func TestNightBedRejoin_ASuccessfulRejoinDoesNotDisableLaterRecovery(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	first := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", first, first)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, first)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", first, first)})

	// node-b refuses the point it is given, which is the case the
	// point-less retry exists for; every other command confirms.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.start": nodeRefusedResult("start", sessionID,
			`start point names item "track-2" at index 1, but this session's playlist has "track-9" there`),
	}
	h.nightAdvanceBackgroundAudio(context.Background(), first, rec)
	for i := 1; i < 6; i++ {
		at := first.Add(time.Duration(i) * time.Second)
		if bedPointCarryingStartDispatched(pub, "node-b") {
			// The point-carrying start has now been refused, which is the
			// case the retry without a point exists for: let that retry
			// succeed.
			pub.resultsByNode = nil
		}
		h.nightAdvanceBackgroundAudio(context.Background(), at, rec)
	}
	if !bedPointCarryingStartDispatched(pub, "node-b") {
		t.Fatal("no start carrying a playback point was ever dispatched to node-b; this test would prove nothing")
	}
	assertBedStepOutcome(t, h, rec, "node-b", func(row nightBackgroundAudioHistoryRow) bool {
		return row.Step.Kind == nightBGStepStart && row.Step.WithPoint && row.Row.Outcome == nightCueOutcomeRefused
	}, "a refused start carrying a playback point")

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	steps := nightBackgroundAudioStepsForNode(history, "node-b")
	latest := steps[len(steps)-1]
	if latest.Step.Kind != nightBGStepStart || latest.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-b latest step = %+v (%s), want the point-less retry confirmed", latest.Step, latest.Row.Outcome)
	}
	if nightBedRejoinStartFailed(steps) {
		t.Fatal("a rejoin whose point-less retry confirmed read as failed, want the bed playing to clear it")
	}

	// The bed is genuinely playing on node-b again, so nothing further is
	// dispatched at it: the rejoin is over and it worked.
	healthy := first.Add(time.Minute)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", healthy, healthy)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 95_000, healthy)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", healthy, healthy)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 95_000, healthy)...))
	settled := len(pub.dispatchedSnapshot())
	h.nightAdvanceBackgroundAudio(context.Background(), healthy, rec)
	if got := len(pub.dispatchedSnapshot()); got != settled {
		t.Fatalf("tick dispatched %d command(s) at a bed playing on both nodes, want 0", got-settled)
	}

	// A second, genuine loss on the same node must still be recovered.
	second := healthy.Add(5 * time.Minute)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", second, second)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 400_000, second)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", second, second)})

	rejoins := bedRejoinApplyCountForTest(t, h, rec, "node-b")
	h.nightAdvanceBackgroundAudio(context.Background(), second, rec)
	if got := bedRejoinApplyCountForTest(t, h, rec, "node-b"); got != rejoins+1 {
		t.Fatalf("node-b rejoin applies = %d, want one more than %d: a second loss after a successful rejoin is still recovered", got, rejoins)
	}
}

// bedPointCarryingStartDispatched reports whether a start naming a
// playback point has reached nodeID, which is the only start the
// point-less retry can follow.
func bedPointCarryingStartDispatched(pub *fakeAudioPublisher, nodeID string) bool {
	for _, d := range pub.dispatchedSnapshot() {
		if d.NodeID != nodeID || d.Action != "audio.session.start" {
			continue
		}
		if _, has := d.Params[pkgaudio.ParamStartItemID]; has {
			return true
		}
	}
	return false
}

// assertBedStepOutcome fails unless nodeID's own step log contains a row
// match accepts, so a test cannot pass on a sequence that never reached
// the state it claims to be about.
func assertBedStepOutcome(t *testing.T, h *handlers, rec store.NightSessionRecord, nodeID string, match func(nightBackgroundAudioHistoryRow) bool, want string) {
	t.Helper()
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	for _, row := range nightBackgroundAudioStepsForNode(history, nodeID) {
		if match(row) {
			return
		}
	}
	t.Fatalf("node %q's step log contains no %s", nodeID, want)
}

// bedRejoinApplyCountForTest counts nodeID's own rejoin applies, read from
// the step log rather than from dispatch counts: an expiry refresh is also
// dispatched as audio.session.apply, so counting the wire action would
// conflate the two.
func bedRejoinApplyCountForTest(t *testing.T, h *handlers, rec store.NightSessionRecord, nodeID string) int {
	t.Helper()
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	n := 0
	for _, row := range nightBackgroundAudioStepsForNode(history, nodeID) {
		if row.Step.Kind == nightBGStepApply && row.Step.Rejoin {
			n++
		}
	}
	return n
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
