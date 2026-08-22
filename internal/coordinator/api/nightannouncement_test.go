package api

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Track F seam F5's announcement duck/mix/interrupt orchestration
// (nightannouncement.go).

func announcementAudioAction() config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationAudio,
		AudioNodeID: "node-a", AudioSessionID: "announcement-1", AudioAction: "audio.session.apply",
	}
}

func mustPutAnnouncementCueAction(t *testing.T, st *store.Store) {
	t.Helper()
	putNightAction(t, st, "thank-you", config.ShowActionPayload{
		Show: "halloween", Label: "Thank you announcement", SafetyClass: config.ShowSafetyClassNone,
		Target: announcementAudioAction(),
	})
}

// TestNightAnnouncement_DuckThenRestoreOnSuccess proves the ordinary
// path: duck fires before the announcement, and restore fires once both
// the duck and the announcement cue resolve.
func TestNightAnnouncement_DuckThenRestoreOnSuccess(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	mustPutAnnouncementCueAction(t, st)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	cue := config.NightSessionCue{Name: "thank-you", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue}
	payload := config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDuck,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     confirmedResultForAction("fade", sessionID, "gain"),
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}

	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	duckRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncement, nightAnnouncementDuckCueName(nightPhaseEnterResting, "thank-you"))
	if err != nil || duckRow.State != nightCueStateResolved || duckRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("duck row = %+v, err = %v, want resolved/confirmed", duckRow, err)
	}
	restoreRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncement, nightAnnouncementRestoreCueName(nightPhaseEnterResting, "thank-you"))
	if err != nil || restoreRow.State != nightCueStateResolved || restoreRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore row = %+v, err = %v, want resolved/confirmed", restoreRow, err)
	}
}

// TestNightAnnouncement_RestoreHappensEvenWhenAnnouncementFails is the
// defect-class proof the coordinator's own report names: a failed
// announcement dispatch must never strand background audio at duck gain.
// Restore must still fire once the announcement's own row resolves,
// regardless of its outcome.
func TestNightAnnouncement_RestoreHappensEvenWhenAnnouncementFails(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	mustPutAnnouncementCueAction(t, st)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	cue := config.NightSessionCue{Name: "thank-you", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue}
	payload := config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDuck,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}

	// The duck itself succeeds, but the announcement's own action reports
	// a genuine content-level failure - reached the node, but the node
	// itself refused/failed it.
	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     confirmedResultForAction("fade", sessionID, "gain"),
		"audio.session.apply": {Outcome: mqttproto.OutcomeFailed, Reason: "node refused to apply"},
	}

	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	announcementRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterResting, "thank-you")
	if err != nil || announcementRow.State != nightCueStateResolved || announcementRow.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("announcement row = %+v, err = %v, want resolved and NOT confirmed (this test proves the failure path)", announcementRow, err)
	}
	restoreRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncement, nightAnnouncementRestoreCueName(nightPhaseEnterResting, "thank-you"))
	if err != nil {
		t.Fatalf("restore row was never committed after the announcement failed: %v (this is the exact stranded-duck defect class)", err)
	}
	if restoreRow.State != nightCueStateResolved || restoreRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore row = %+v, want resolved/confirmed even though the announcement itself failed", restoreRow)
	}
}

// TestNightAnnouncement_MixNeverDucks proves policy "mix" touches
// background audio at all: no duck row is ever committed.
func TestNightAnnouncement_MixNeverDucks(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)

	mixPolicy := config.NightSessionAnnouncementPolicyMix
	cue := config.NightSessionCue{Name: "thank-you", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue, AnnouncementPolicy: &mixPolicy}
	payload := config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDuck,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}

	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncement, nightAnnouncementDuckCueName(nightPhaseEnterResting, "thank-you")); !errIsNightCueOutboxNotFound(err) {
		t.Fatalf("expected no duck row for policy=mix, got err=%v", err)
	}
}

func errIsNightCueOutboxNotFound(err error) bool {
	return err == store.ErrNightCueOutboxNotFound
}

// TestNightAnnouncement_InterruptGateBlocksWhileAnnouncementUnresolved
// proves the interrupt-stop gate actually WAITS: while the announcement
// cue's own row is still pending (frozen here via nightCueDispatchHooks,
// the same crash-injection seam nightcuerun_test.go already uses), a
// background-audio tick must not restart playback out from under the
// announcement still in flight.
func TestNightAnnouncement_InterruptGateBlocksWhileAnnouncementUnresolved(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	mustPutAnnouncementCueAction(t, st)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	interrupt := config.NightSessionAnnouncementPolicyInterrupt
	cue := config.NightSessionCue{Name: "urgent", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue, AnnouncementPolicy: &interrupt}
	payload := config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDuck,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.session.stop": confirmedResultForAction("stop", sessionID, "stopped"),
	}
	// Freeze the announcement cue's own row right after it commits, so it
	// stays pending rather than resolving in this same call.
	h.nightCueHooks.afterCommit = func(cueName string) bool { return cueName == "urgent" }
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	h.nightCueHooks.afterCommit = nil

	announcementRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterResting, "urgent")
	if err != nil || announcementRow.State != nightCueStatePending {
		t.Fatalf("announcement row = %+v, err = %v, want pending (frozen before dispatch)", announcementRow, err)
	}

	pub.resultsByAction = nil
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	countBeforeGatedTick := pub.count()
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != countBeforeGatedTick {
		t.Fatalf("publish count = %d after a tick while the announcement is still pending, want unchanged at %d (must not restart yet)", pub.count(), countBeforeGatedTick)
	}
}

// TestNightAnnouncement_InterruptStopsThenBackgroundRestartsAfterResolve
// proves the interrupt path: background audio is stopped to make room,
// and once the announcement's own cue row resolves, the NEXT background-
// audio tick restarts it (nightAdvanceBackgroundAudio's own interrupt-
// stop gate, nightbackgroundaudio.go) rather than waiting forever or
// restarting before the announcement is actually done.
func TestNightAnnouncement_InterruptStopsThenBackgroundRestartsAfterResolve(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	mustPutAnnouncementCueAction(t, st)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	interrupt := config.NightSessionAnnouncementPolicyInterrupt
	cue := config.NightSessionCue{Name: "urgent", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue, AnnouncementPolicy: &interrupt}
	payload := config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDuck,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.session.stop":  confirmedResultForAction("stop", sessionID, "stopped"),
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	// Background audio must now be stopped.
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	latest := history[len(history)-1]
	if latest.Step.Kind != "stop" || latest.Row.State != nightCueStateResolved || latest.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("latest background-audio step = %+v, want a resolved/confirmed stop", latest)
	}

	// The announcement itself already resolved (this fake answers
	// synchronously), so the next background-audio tick restarts it.
	pub.resultsByAction = nil
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action after interrupt resolved = %q, want audio.session.apply (restart)", pub.lastAction)
	}
	media, _ := pub.lastParams["media"].(map[string]any)
	if media["assetId"] != "asset-1" {
		t.Fatalf("restarted item asset = %v, want asset-1 (resume policy replays the last-started item)", media["assetId"])
	}
}
