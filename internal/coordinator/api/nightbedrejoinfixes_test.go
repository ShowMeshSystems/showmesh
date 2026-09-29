package api

import (
	"context"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file covers the cases review found the first cut of the bed rejoin
// got wrong: a speaker named as absent while the bed is deliberately down
// for a show, a transient refusal treated as final, and the restart the
// issue was actually filed for, where the agent restores the session and
// leaves it stopped rather than losing it.

// bedSessionStateReport is one node's own report of the bed session in a
// given state, with no item or position.
func bedSessionStateReport(sessionID, state string, at time.Time) []observation.Observation {
	return []observation.Observation{sessionStateObservation(sessionID, state, at, at)}
}

// bedSessionStaleReport is [bedSessionStateReport] plus the node's own
// "I could not read this session fresh this tick" marker.
func bedSessionStaleReport(sessionID, state string, at time.Time) []observation.Observation {
	return append(bedSessionStateReport(sessionID, state, at), observation.Observation{
		Resource:   observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
		Signal:     audioSessionStaleSignalID,
		Value:      true,
		ObservedAt: &at, CollectedAt: at,
	})
}

// TestNightBedRejoin_RecoversASessionTheAgentRestoredStopped is the case
// the issue was filed for. The agent restores its persisted sessions at
// boot, so after a restart the bed session usually still EXISTS on the
// node and sits stopped, while this coordinator's latest step is a
// confirmed start. Nothing used to bring that back.
func TestNightBedRejoin_RecoversASessionTheAgentRestoredStopped(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	lossAt := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, lossAt)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateStopped), lossAt)...))

	appliesBefore := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, lossAt, 12)

	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != appliesBefore+1 {
		t.Fatalf("bed applies = %d, want one more than the %d before the restart: a restored-but-stopped session is lost too", got, appliesBefore)
	}
	start, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !ok {
		t.Fatalf("node-b: no audio.session.start dispatched for the recovery")
	}
	if start[pkgaudio.ParamStartItemID] != "track-2" {
		t.Fatalf("node-b start %s = %v, want track-2 (where node-a is playing)", pkgaudio.ParamStartItemID, start[pkgaudio.ParamStartItemID])
	}
}

// TestNightBedRejoin_LeavesADeliberatelyStoppedSessionAlone is that
// case's own guard: the SAME reported state, stopped, is not a loss when
// this controller's own latest step is the confirmed stop that caused it.
func TestNightBedRejoin_LeavesADeliberatelyStoppedSessionAlone(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	// Into a show: the bed is stopped on purpose.
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest, _ := nightBackgroundAudioLatestStepForNode(history, "node-a")
	if latest.Step.Kind != nightBGStepStop || latest.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("setup: node-a latest step = %+v, want a confirmed stop", latest)
	}

	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateStopped), at)...))

	if _, lost := nightBedNodeLostSession(audio, at, "node-a", sessionID, nightBackgroundAudioStepsForNode(history, "node-a")); lost {
		t.Fatal("a session reported stopped right after this controller's own confirmed stop read as lost, want left alone")
	}
}

// TestNightBedNodesNotPlaying_SaysNothingWhileTheBedIsDownForAShow covers
// the surface half of the same defect: during a show the bed is meant to
// be silent, so naming every speaker as not playing is noise that would
// bury a real one.
func TestNightBedNodesNotPlaying_SaysNothingWhileTheBedIsDownForAShow(t *testing.T) {
	for _, state := range []string{nightStateLive, nightStateTransitionToShow, nightStateTransitionToResting, nightStateFadingOut} {
		if nightBedIsMeantToBePlaying(state) {
			t.Errorf("nightBedIsMeantToBePlaying(%q) = true, want false: the bed is deliberately down there", state)
		}
	}
	for _, state := range []string{nightStatePreshow, nightStateRestingIntershow, nightStateEndOfNightResting} {
		if !nightBedIsMeantToBePlaying(state) {
			t.Errorf("nightBedIsMeantToBePlaying(%q) = false, want true: the bed is supposed to be audible there", state)
		}
	}
}

// TestNightBedNodesNotPlaying_SkipsASpeakerThisControllerSuspended is the
// per-node half: resting-intershow past the fade lead is still a
// bed-playing state, so a node already faded down and paused there must
// be excluded by its own ledger rather than by the session state.
func TestNightBedNodesNotPlaying_SkipsASpeakerThisControllerSuspended(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.pause": pauseResultWithBookmark(true, "track-1", 0, 1_000),
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	h.nightBackgroundAudioSuspend(context.Background(), testNow, rec, "halloween", "node-b", sessionID,
		multiNodeBedConfig("node-a", "node-a", "node-b"), history)

	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, at)...))
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StatePaused), at)...))

	got := nightBedNodesNotPlaying(context.Background(), h.deps, rec, at, bedOutboxRowsForTest(t, h, rec))
	for _, node := range got {
		if node.NodeID == "node-b" {
			t.Fatalf("nodesNotPlaying named node-b after this controller paused it on purpose: %+v", got)
		}
	}
}

// TestNightBedRejoin_StaleOrTruncatedReadingIsNotALoss proves the two
// readings that must never be mistaken for a missing session: one the
// node itself marked stale, and an absent one from a report that hit its
// own session cap and may have dropped the bed out of it.
func TestNightBedRejoin_StaleOrTruncatedReadingIsNotALoss(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	steps := nightBackgroundAudioStepsForNode(history, "node-b")
	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStaleReport(sessionID, string(pkgaudio.StateStopped), at)...))
	if _, lost := nightBedNodeLostSession(audio, at, "node-b", sessionID, steps); lost {
		t.Fatal("a session the node marked stale read as lost, want left alone until it reports fresh")
	}

	// An absent bed session in a report already carrying its full cap of
	// sessions: the bed may simply not have fitted.
	full := []observation.Observation{bedNodeAudioReport("node-b", at, at)}
	for i := 0; i < nightBedSessionReportLimit; i++ {
		full = append(full, sessionStateObservation("other-session-"+string(rune('a'+i)), string(pkgaudio.StatePlaying), at, at))
	}
	audio.setObservations("node-b", full)
	if _, lost := nightBedNodeLostSession(audio, at, "node-b", sessionID, steps); lost {
		t.Fatal("an absent bed session in a truncated report read as lost, want left alone")
	}
}

// TestNightBedRefusal_OnlyFinalWhenTheSessionIsGone is the rule review
// called out: an agent that has restarted refuses with "No audio engine
// is connected yet" until its binding arrives, and treating that as final
// would leave the speaker silent for the night.
func TestNightBedRefusal_OnlyFinalWhenTheSessionIsGone(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	_, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	dispatchedAt := testNow
	refused := store.NightCueOutboxRecord{
		State: nightCueStateResolved, Outcome: nightCueOutcomeRefused,
		OutcomeReason: "No audio engine is connected yet. Retry once one connects.",
		DispatchedAt:  &dispatchedAt,
	}

	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StateReady), at)...))
	if h.nightBedRefusalIsFinal(at, "node-b", sessionID, refused) {
		t.Fatal("a refusal from a node still holding the session read as final, want retryable")
	}

	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", at, at)})
	if !h.nightBedRefusalIsFinal(at, "node-b", sessionID, refused) {
		t.Fatal("a refusal from a node whose bed session is gone read as retryable, want final")
	}

	ledger := store.NightCueOutboxRecord{
		State: nightCueStateResolved, Outcome: nightCueOutcomeRefused,
		OutcomeReason: nightLedgerRefusalOperatorReason,
	}
	if h.nightBedRefusalIsFinal(at, "node-b", sessionID, ledger) {
		t.Fatal("this coordinator's own ledger refusal read as final, want retryable under a fresh revision")
	}
}

// TestNightAdvanceBackgroundAudio_StillRetriesARefusedSingleNodeResume is
// the regression the no-resend rule introduced and this fix removes: a
// single-node bed whose resume the node refused retried every tick before
// the rejoin work, and must again.
func TestNightAdvanceBackgroundAudio_StillRetriesARefusedSingleNodeResume(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	// The node still holds the session and still refuses the resume: the
	// exact shape an agent shows while its engine binding is missing.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.resume": nodeRefusedResult("resume", sessionID, "No audio engine is connected yet. Retry once one connects."),
	}
	at := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", at, at)},
		bedSessionStateReport(sessionID, string(pkgaudio.StatePaused), at)...))

	h.nightAdvanceBackgroundAudio(context.Background(), at, rec)
	first := countDispatchedActionForSession(pub, "audio.session.resume", sessionID)
	if first == 0 {
		t.Fatalf("no audio.session.resume dispatched at all; the test proves nothing")
	}
	h.nightAdvanceBackgroundAudio(context.Background(), at.Add(time.Second), rec)
	if got := countDispatchedActionForSession(pub, "audio.session.resume", sessionID); got <= first {
		t.Fatalf("audio.session.resume dispatches = %d on the next tick, want more than %d: a transient refusal still retries", got, first)
	}
}

// TestNightBedRejoin_RetriesOnceWithoutAStartPointTheNodeRefused proves a
// refused position costs the speaker nothing: it starts the bed from its
// first item instead of staying silent, and only once.
func TestNightBedRejoin_RetriesOnceWithoutAStartPointTheNodeRefused(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	lossAt := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, lossAt)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)})

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b:audio.session.start": nodeRefusedResult("start", sessionID,
			`start point names item "track-2" at index 1, but this session's playlist has "track-9" there`),
	}
	// Counted from here, so the bed's own first start, which legitimately
	// carries no position, is not mistaken for the retry.
	before := len(pub.dispatchedSnapshot())
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, lossAt, 14)

	var withPoint, withoutPoint int
	for i, d := range pub.dispatchedSnapshot() {
		if i < before || d.NodeID != "node-b" || d.Action != "audio.session.start" || d.Params["sessionId"] != sessionID {
			continue
		}
		if _, has := d.Params[pkgaudio.ParamStartItemID]; has {
			withPoint++
		} else {
			withoutPoint++
		}
	}
	if withPoint != 1 || withoutPoint != 1 {
		t.Fatalf("node-b starts: %d carrying a position, %d without, want exactly 1 and 1", withPoint, withoutPoint)
	}
}

// TestNightBedJoinPoint_UsesThePauseBookmarkWhenTheBedIsPaused proves a
// node rejoining while the bed is held down for a show is given the point
// the other nodes will resume from, not the bed's first item.
func TestNightBedJoinPoint_UsesThePauseBookmarkWhenTheBedIsPaused(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 42_000),
		"node-b:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 42_000),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	at := testNow.Add(30 * time.Second)
	point, ok := h.nightBedJoinPoint(context.Background(), at, sessionID, []string{"node-a", "node-b"}, "node-b", history)
	if !ok {
		t.Fatal("no join point found while every other node is paused, want the bookmark they resume from")
	}
	if point.ItemID != "track-2" || point.Index != 1 || point.PositionMs != 42_000 {
		t.Fatalf("join point = %+v, want track-2 index 1 at 42000 ms", point)
	}
}

// TestNightBedPeerPlaybackPoint_DropsAPositionTooOldToTrust proves the
// bound: the coordinator has no item duration to clamp against, so a
// reading old enough that advancing it could name a position past the end
// of the track is not used at all.
func TestNightBedPeerPlaybackPoint_DropsAPositionTooOldToTrust(t *testing.T) {
	audio := &fakeNodeAudioLister{}
	const sessionID = "night-bg-sess-1"
	fresh := testNow
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", fresh, fresh)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, fresh)...))

	within := testNow.Add(nightBedPositionEvidenceMaxAge - time.Second)
	point, ok := nightBedPeerPlaybackPoint(audio, within, sessionID, []string{"node-a", "node-b"}, "node-b")
	if !ok {
		t.Fatal("a reading inside the bound was dropped, want it used")
	}
	if want := int64(5_000) + (nightBedPositionEvidenceMaxAge - time.Second).Milliseconds(); point.PositionMs != want {
		t.Fatalf("position = %d ms, want %d ms (the reading advanced to now)", point.PositionMs, want)
	}

	beyond := testNow.Add(nightBedPositionEvidenceMaxAge + time.Second)
	if _, ok := nightBedPeerPlaybackPoint(audio, beyond, sessionID, []string{"node-a", "node-b"}, "node-b"); ok {
		t.Fatal("a reading older than the bound was used, want it dropped so the node starts from the first item")
	}
}

// TestNightBedNotPlayingReason_PromisesOnlyWhatHappens keeps the operator
// copy honest: every reason that says the coordinator will act is a case
// the coordinator actually acts on.
func TestNightBedNotPlayingReason_PromisesOnlyWhatHappens(t *testing.T) {
	audio := &fakeNodeAudioLister{}
	audio.setObservations("node-a", []observation.Observation{bedNodeAudioReport("node-a", testNow, testNow)})

	cases := []struct {
		name          string
		reading       nightBedSessionReading
		wantRecovered bool
	}{
		{"absent", nightBedSessionReading{}, true},
		{"stopped", nightBedSessionReading{Present: true, State: string(pkgaudio.StateStopped)}, true},
		{"failed", nightBedSessionReading{Present: true, State: string(pkgaudio.StateFailed)}, true},
		{"completed", nightBedSessionReading{Present: true, State: string(pkgaudio.StateCompleted)}, false},
		{"restore pending", nightBedSessionReading{Present: true, State: string(pkgaudio.StateRestorePending)}, false},
		{"stale", nightBedSessionReading{Present: true, State: string(pkgaudio.StateStopped), Stale: true}, false},
	}
	for _, tc := range cases {
		reason := nightBedNotPlayingReason(audio, testNow, "node-a", nightBedNotPlayingFacts{Reading: tc.reading})
		promises := strings.Contains(reason, "on its next check")
		if promises != tc.wantRecovered {
			t.Errorf("%s: reason %q promises a recovery = %v, want %v", tc.name, reason, promises, tc.wantRecovered)
		}
		if reason == "" || reason[:1] != strings.ToUpper(reason[:1]) {
			t.Errorf("%s: reason %q should start with a capital", tc.name, reason)
		}
	}
}

// TestNightBedStartPointNote_ReadsAsOperatorCopy keeps the recorded note
// free of this controller's own item handles.
func TestNightBedStartPointNote_ReadsAsOperatorCopy(t *testing.T) {
	note := nightBedStartPointNote(nightBedPlaybackPoint{FromNodeID: "node-a", ItemID: "bed-item-0003", Index: 1, PositionMs: 65_000})
	if note[:1] != strings.ToUpper(note[:1]) {
		t.Errorf("note %q should start with a capital", note)
	}
	if strings.Contains(note, "bed-item-0003") {
		t.Errorf("note %q exposes an internal item id as if it were a track name", note)
	}
	if !strings.Contains(note, "node-a") {
		t.Errorf("note %q should name the speaker the position came from", note)
	}
}

var _ = v1.NightBedNodeNotPlaying{}
