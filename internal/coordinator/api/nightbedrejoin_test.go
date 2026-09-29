package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file proves the bed rejoin path (nightbedrejoin.go): a node whose
// bed session is gone from its own report is given the bed again, at the
// item and position a node still playing it reports, and a command the
// node itself refused is not re-sent every tick.
//
// TWO FAKE AGENTS, NOT TWO REAL ONES: every node here is the
// fakeAudioPublisher plus hand-built observations. The real two-agent run,
// with one agent restarted mid-bed, is the owner's to do on real hardware
// and is NOT covered by anything in this file.

// bedNodeAudioReport builds the node-level audio evidence
// [nightBedNodeReportsAudioSince] reads: proof this node is publishing
// audio reports this coordinator recorded at collectedAt. Unlike
// nodeAudioEngineStateObservation one file over, this sets CollectedAt,
// which is exactly what the rejoin fence compares against a dispatch
// instant.
func bedNodeAudioReport(nodeID string, observedAt, collectedAt time.Time) observation.Observation {
	return observation.Observation{
		Resource:    observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Signal:      audioNodeEngineStateSignalID,
		Value:       "usable",
		ObservedAt:  &observedAt,
		CollectedAt: collectedAt,
	}
}

// nodeRefusedResult is the result a node's own session layer returns when
// it refuses a command: the refusal rides the result EVIDENCE, not the
// envelope outcome, which is what the coordinator's own mapResultOutcome
// reads. A bare envelope-level failure with no evidence is unconfirmable,
// a different case entirely (see the retry test below).
func nodeRefusedResult(action, sessionID, reason string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session." + action,
			Value:  map[string]any{"sessionId": sessionID, "outcome": string(pkgaudio.OutcomeRefused), "reason": reason},
		},
	}
}

// bedSessionPlayingReport is one node's own report of the bed session
// playing at itemID/index/positionMs, the evidence a rejoining node's
// position is read from.
func bedSessionPlayingReport(sessionID, itemID string, index, positionMs int64, at time.Time) []observation.Observation {
	return []observation.Observation{
		sessionStateObservation(sessionID, string(pkgaudio.StatePlaying), at, at),
		{
			Resource: observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
			Signal:   audioSessionItemIDSignalID, Value: itemID, ObservedAt: &at, CollectedAt: at,
		},
		{
			Resource: observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
			Signal:   audioSessionItemIndexSignalID, Value: index, ObservedAt: &at, CollectedAt: at,
		},
		{
			Resource: observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
			Signal:   audioSessionPositionMsSignalID, Value: positionMs, ObservedAt: &at, CollectedAt: at,
		},
	}
}

// bedOutboxRowsForTest reads rec's own background-audio outbox rows, the
// list the night read path already holds when it asks which speakers are
// not playing.
func bedOutboxRowsForTest(t *testing.T, h *handlers, rec store.NightSessionRecord) []store.NightCueOutboxRecord {
	t.Helper()
	rows, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(context.Background(), rec.ID, nightPhaseRestingBackground)
	if err != nil {
		t.Fatalf("list background-audio outbox rows: %v", err)
	}
	return rows
}

// startMultiNodeBedForTest drives a two-node bed to a confirmed start on
// both nodes and returns the session record and its bed session id.
func startMultiNodeBedForTest(t *testing.T, h *handlers, st *store.Store, pub *fakeAudioPublisher) (store.NightSessionRecord, string) {
	t.Helper()
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	// Both nodes report holding every bed file, which is the ordinary
	// state of a rig whose assets have synced. It matters here because it
	// arms the late-file recovery path as well as the rejoin, so these
	// tests exercise the two together rather than one at a time.
	for _, nodeID := range []string{"node-a", "node-b"} {
		putNodeInventoryForTest(t, st, nodeID, testNow, bedItemInventoryForTest(nodeID, "asset-1", "asset-2")...)
	}
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)
	for _, nodeID := range []string{"node-a", "node-b"} {
		if _, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.start"); !ok {
			t.Fatalf("setup: node %q never started the bed", nodeID)
		}
	}
	return rec, sessionID
}

// TestNightBedRejoin_NodeThatLostTheBedIsGivenItAgainAtThePeerPosition is
// the issue's own case: node-b's agent restarts mid-night, so node-b
// reports audio but no longer holds the bed session at all, while node-a
// carries on playing it. The coordinator must give node-b the bed again
// and start it where node-a is, not at the bed's first item.
func TestNightBedRejoin_NodeThatLostTheBedIsGivenItAgainAtThePeerPosition(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)

	// node-a is minutes into the bed's SECOND item; node-b reports audio
	// with no bed session at all, which is what an agent restart leaves.
	// Both reports are recorded after the starts were dispatched, so they
	// genuinely describe what those starts produced.
	lossAt := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, lossAt)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)})

	appliesBefore := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	startsBefore := countDispatchedActionForSession(pub, "audio.session.start", sessionID)
	driveNightAdvanceBackgroundAudioUntilStableAt(t, h, pub, rec, lossAt, 12)

	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != appliesBefore+1 {
		t.Fatalf("bed applies = %d, want exactly one more than the %d before the loss (one rejoin for node-b)", got, appliesBefore)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.start", sessionID); got != startsBefore+1 {
		t.Fatalf("bed starts = %d, want exactly one more than the %d before the loss", got, startsBefore)
	}
	start, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.start")
	if !ok {
		t.Fatalf("node-b: no audio.session.start dispatched for the rejoin")
	}
	if start[pkgaudio.ParamStartItemID] != "track-2" {
		t.Fatalf("node-b rejoin start %s = %v, want %q (the item node-a is playing)", pkgaudio.ParamStartItemID, start[pkgaudio.ParamStartItemID], "track-2")
	}
	if index, _ := evidenceInt64(start[pkgaudio.ParamStartIndex]); index != 1 {
		t.Fatalf("node-b rejoin start %s = %v, want 1", pkgaudio.ParamStartIndex, start[pkgaudio.ParamStartIndex])
	}
	if positionMs, _ := evidenceInt64(start[pkgaudio.ParamStartPositionMs]); positionMs != 65_000 {
		t.Fatalf("node-b rejoin start %s = %v, want 65000 (node-a's own reported position)", pkgaudio.ParamStartPositionMs, start[pkgaudio.ParamStartPositionMs])
	}
	if _, present := start[pkgaudio.ParamScheduledAtNs]; present {
		t.Fatalf("node-b rejoin start params = %v, want no scheduledAtNs: a rejoin starts on arrival and is recorded unaligned", start)
	}

	// node-a was never touched: one node's recovery must not restart the
	// bed on a node that is still playing it.
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != appliesBefore+1 {
		t.Fatalf("bed applies = %d after the rejoin, want %d", got, appliesBefore+1)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	var rejoinApply, rejoinStart store.NightCueOutboxRecord
	for _, row := range nightBackgroundAudioStepsForNode(history, "node-b") {
		switch row.Step.Kind {
		case nightBGStepApply:
			rejoinApply = row.Row
		case nightBGStepStart:
			rejoinStart = row.Row
		}
	}
	if !strings.Contains(rejoinApply.OutcomeReason, "no longer holding the background music") {
		t.Fatalf("node-b rejoin apply reason = %q, want it to state that the speaker lost the background music", rejoinApply.OutcomeReason)
	}
	if !strings.Contains(rejoinStart.OutcomeReason, "node-a") {
		t.Fatalf("node-b rejoin start reason = %q, want it to name the node the position came from", rejoinStart.OutcomeReason)
	}
}

// TestNightBedRejoin_NodeThatRestartedWhileTheBedWasPausedIsGivenItAgain
// covers the other half of the night: the agent restarts DURING a show,
// while the bed is paused, so the node comes back holding nothing and the
// resume after that show would be refused with nothing to self-heal it.
func TestNightBedRejoin_NodeThatRestartedWhileTheBedWasPausedIsGivenItAgain(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)

	// Into the show: the bed pauses on both nodes.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 65_000),
		"node-b:audio.session.pause": pauseResultWithBookmark(true, "track-2", 1, 65_000),
	}
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	for _, nodeID := range []string{"node-a", "node-b"} {
		if _, ok := dispatchedByNodeAction(pub, nodeID, "audio.session.pause"); !ok {
			t.Fatalf("setup: node %q never paused the bed", nodeID)
		}
	}

	// node-b's agent restarted while the bed was paused: it reports audio
	// and no bed session. node-a still holds its paused session.
	pub.resultsByNode = nil
	lossAt := testNow.Add(90 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", []observation.Observation{
		bedNodeAudioReport("node-a", lossAt, lossAt),
		sessionStateObservation(sessionID, string(pkgaudio.StatePaused), lossAt, lossAt),
	})
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)})

	appliesBefore := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != appliesBefore+1 {
		t.Fatalf("bed applies = %d, want exactly one more than the %d before the loss (one rejoin for node-b)", got, appliesBefore)
	}
	if _, ok := dispatchedByNodeAction(pub, "node-b", "audio.session.apply"); !ok {
		t.Fatalf("node-b: no audio.session.apply dispatched for the rejoin")
	}
}

// TestNightBedRejoin_NoRejoinForANodeThatNeverHeldTheBed keeps the rejoin
// path from becoming an unbounded apply retry: a node whose very first
// apply was refused never held the bed, so there is nothing to give back
// and the ordinary "apply did not confirm" rule owns it.
func TestNightBedRejoin_NoRejoinForANodeThatNeverHeldTheBed(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	putAudioNodeNoLTCForTest(t, st, "node-a")
	putAudioNodeNoLTCForTest(t, st, "node-b")
	ba := multiNodeBedConfig("node-a", "node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b": nodeRefusedResult("apply", sessionID, "node-b has never accepted this bed"),
	}
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	lossAt := testNow.Add(30 * time.Second)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, lossAt)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)})

	before := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != before {
		t.Fatalf("bed applies = %d after five ticks, want unchanged at %d: a node that never held the bed is not re-applied", got, before)
	}
}

// TestNightBedRejoin_NoRejoinWhileTheNodeReportsTheBedPlaying is the
// regression that keeps this path from restarting a healthy bed: a node
// reporting the session playing is left completely alone.
func TestNightBedRejoin_NoRejoinWhileTheNodeReportsTheBedPlaying(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	lossAt := testNow.Add(30 * time.Second)
	for _, nodeID := range []string{"node-a", "node-b"} {
		audio.setObservations(nodeID, append(
			[]observation.Observation{bedNodeAudioReport(nodeID, lossAt, lossAt)},
			bedSessionPlayingReport(sessionID, "track-1", 0, 1_000, lossAt)...))
	}

	before := len(pub.dispatchedSnapshot())
	h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	if got := len(pub.dispatchedSnapshot()); got != before {
		t.Fatalf("tick dispatched %d new command(s) against a bed both nodes report playing, want 0", got-before)
	}
}

// TestNightBedRejoin_NoRejoinWithoutAReportThatPostDatesTheStart proves
// the fence: a node whose only audio report was recorded BEFORE its own
// start was dispatched says nothing about that start's result, and
// evidence that cannot describe the start is not evidence the bed is
// gone.
func TestNightBedRejoin_NoRejoinWithoutAReportThatPostDatesTheStart(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	lossAt := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-2", 1, 65_000, lossAt)...))
	// Recorded a minute before the bed was ever started on node-b.
	stale := testNow.Add(-time.Minute)
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", stale, stale)})

	before := len(pub.dispatchedSnapshot())
	h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	if got := len(pub.dispatchedSnapshot()); got != before {
		t.Fatalf("tick dispatched %d new command(s) on evidence that predates the start, want 0", got-before)
	}
}

// TestNightBedRejoin_OnlyOnceForTheSameLostStep proves the bound that
// keeps a node this coordinator cannot actually recover from being
// re-applied on every tick: the bed is given back once per lost step, and
// a node whose rejoin apply is refused is left with its reason recorded.
func TestNightBedRejoin_OnlyOnceForTheSameLostStep(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	lossAt := testNow.Add(30 * time.Second)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", lossAt, lossAt)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, lossAt)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", lossAt, lossAt)})

	// node-b refuses everything from here: the rejoin apply lands as
	// refused, which must not be retried on the next tick either.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-b": nodeRefusedResult("apply", sessionID, "node-b refuses the rejoin for a test"),
	}

	h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	after := countDispatchedActionForSession(pub, "audio.session.apply", sessionID)
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), lossAt, rec)
	}
	if got := countDispatchedActionForSession(pub, "audio.session.apply", sessionID); got != after {
		t.Fatalf("bed applies = %d after five further ticks, want unchanged at %d: one rejoin per lost step", got, after)
	}
}

// TestNightStopBackgroundAudio_DoesNotResendAFadeDownTheNodeRefused is
// the issue's second symptom: the coordinator sent a fade-down every tick
// that the node refused. A refusal is the node's own answer, so the same
// command under a fresh revision is never re-sent.
func TestNightStopBackgroundAudio_DoesNotResendAFadeDownTheNodeRefused(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	// The node's bed session is gone, so every fade it is sent is
	// refused, exactly as a restarted agent refuses one.
	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.gain.fade": nodeRefusedResult("fade", sessionID, "session does not exist"),
	}

	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	fades := countDispatchedAction(pub, "audio.gain.fade")
	if fades == 0 {
		t.Fatalf("no audio.gain.fade dispatched at all; the test proves nothing")
	}
	for i := 0; i < 5; i++ {
		h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	}
	if got := countDispatchedAction(pub, "audio.gain.fade"); got != fades {
		t.Fatalf("audio.gain.fade dispatches = %d after five further ticks, want unchanged at %d: a refused step is not re-sent", got, fades)
	}
}

// TestNightStopBackgroundAudio_StillRetriesAFadeDownTheNodeNeverAnswered
// is the other half of the same rule: a step the node never answered has
// not been refused, so it still retries.
func TestNightStopBackgroundAudio_StillRetriesAFadeDownTheNodeNeverAnswered(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("x", sessionID, "started")
	driveNightAdvanceBackgroundAudioUntilStable(t, h, pub, rec, 12)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a:audio.gain.fade": {Outcome: mqttproto.OutcomeFailed, Reason: "the node sent no result"},
	}

	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	first := countDispatchedAction(pub, "audio.gain.fade")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if got := countDispatchedAction(pub, "audio.gain.fade"); got <= first {
		t.Fatalf("audio.gain.fade dispatches = %d on the second tick, want more than %d: a step the node never answered still retries", got, first)
	}
}

// TestNightBedNodesNotPlaying_NamesTheAbsentNodeAndClearsWhenItIsBack is
// the Night screen's own live surface: the node is named while it is
// missing, with its reason, and drops off the list the moment it reports
// the bed playing again.
func TestNightBedNodesNotPlaying_NamesTheAbsentNodeAndClearsWhenItIsBack(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	rec, sessionID := startMultiNodeBedForTest(t, h, st, pub)
	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", append(
		[]observation.Observation{bedNodeAudioReport("node-a", testNow, testNow)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, testNow)...))
	audio.setObservations("node-b", []observation.Observation{bedNodeAudioReport("node-b", testNow, testNow)})

	rows := bedOutboxRowsForTest(t, h, rec)
	got := nightBedNodesNotPlaying(context.Background(), h.deps, rec, testNow, rows)
	if len(got) != 1 || got[0].NodeID != "node-b" {
		t.Fatalf("nodesNotPlaying = %+v, want exactly node-b", got)
	}
	if !strings.Contains(got[0].Reason, "no longer holding the background music") {
		t.Fatalf("node-b reason = %q, want it to state that the speaker is no longer holding the background music", got[0].Reason)
	}

	// A node that has gone quiet altogether reads as unknown, never as a
	// confirmed absence.
	audio.setObservations("node-b", nil)
	got = nightBedNodesNotPlaying(context.Background(), h.deps, rec, testNow, rows)
	if len(got) != 1 || !strings.Contains(got[0].Reason, "not reporting its audio") {
		t.Fatalf("nodesNotPlaying with a silent node = %+v, want one entry stating its audio is not being reported", got)
	}

	// Back and playing: the list clears itself.
	audio.setObservations("node-b", append(
		[]observation.Observation{bedNodeAudioReport("node-b", testNow, testNow)},
		bedSessionPlayingReport(sessionID, "track-1", 0, 5_000, testNow)...))
	if got := nightBedNodesNotPlaying(context.Background(), h.deps, rec, testNow, rows); len(got) != 0 {
		t.Fatalf("nodesNotPlaying once node-b is playing again = %+v, want empty", got)
	}
}
