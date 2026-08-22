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

// mustPutAnnouncementCueAction registers a hyphenated cue action name
// deliberately: an earlier version of this seam's interrupt-stop
// encoding broke on exactly this shape (CRITICAL 3, found by review), so
// every announcement test below exercises it rather than a name with no
// hyphen that would never have caught the bug.
func mustPutAnnouncementCueAction(t *testing.T, st *store.Store) {
	t.Helper()
	putNightAction(t, st, "thank-you", config.ShowActionPayload{
		Show: "halloween", Label: "Thank you announcement", SafetyClass: config.ShowSafetyClassNone,
		Target: announcementAudioAction(),
	})
}

func announcementCue(policy *string) config.NightSessionCue {
	return config.NightSessionCue{
		Name: "thank-you", Role: config.NightSessionCueRoleAnnouncement, Action: "thank-you",
		OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue, AnnouncementPolicy: policy,
	}
}

func announcementPayload(ba *config.NightSessionBackgroundAudio, defaultPolicy string) config.NightSessionPayload {
	return config.NightSessionPayload{
		Show: "halloween", AnnouncementDefaultPolicy: defaultPolicy,
		Resting: config.NightSessionResting{BackgroundAudio: ba},
	}
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
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     confirmedResultForAction("fade", nightBackgroundAudioSessionID(rec), "gain"),
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	duckRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncementDuck+":"+nightPhaseEnterResting, "thank-you")
	if err != nil || duckRow.State != nightCueStateResolved || duckRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("duck row = %+v, err = %v, want resolved/confirmed", duckRow, err)
	}
	restorePhase := nightAnnouncementRestorePhase(nightPhaseEnterResting, 1)
	restoreRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, restorePhase, "thank-you")
	if err != nil || restoreRow.State != nightCueStateResolved || restoreRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore row = %+v, err = %v, want resolved/confirmed", restoreRow, err)
	}
}

// TestNightAnnouncement_RestoreHappensEvenWhenAnnouncementFails is the
// defect-class proof the coordinator's own report names: a failed
// announcement dispatch must never strand background audio at duck gain.
func TestNightAnnouncement_RestoreHappensEvenWhenAnnouncementFails(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     confirmedResultForAction("fade", nightBackgroundAudioSessionID(rec), "gain"),
		"audio.session.apply": {Outcome: mqttproto.OutcomeFailed, Reason: "node refused to apply"},
	}
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	announcementRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterResting, "thank-you")
	if err != nil || announcementRow.State != nightCueStateResolved || announcementRow.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("announcement row = %+v, err = %v, want resolved and NOT confirmed", announcementRow, err)
	}
	restorePhase := nightAnnouncementRestorePhase(nightPhaseEnterResting, 1)
	restoreRow, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, restorePhase, "thank-you")
	if err != nil {
		t.Fatalf("restore row was never committed after the announcement failed: %v (this is the exact stranded-duck defect class)", err)
	}
	if restoreRow.State != nightCueStateResolved || restoreRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore row = %+v, want resolved/confirmed even though the announcement itself failed", restoreRow)
	}
}

// TestNightAnnouncement_RestoreHappensWhenAnnouncementIsAmbiguous proves
// the SAME defect class for the ambiguous outcome specifically -
// review's own explicit ask: an ambiguous announcement row is ALSO
// terminal and must not leave the restore ungated forever.
func TestNightAnnouncement_RestoreHappensWhenAnnouncementIsAmbiguous(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	// Fabricate the announcement cue's own row as ambiguous directly:
	// audio is retryable by identity, so the ordinary dispatch path can
	// never actually produce this outcome for it, but a mqtt- or
	// resolume-bound announcement action still can, and the restore path
	// must handle it the same way regardless of which integration
	// produced it.
	ctx := context.Background()
	if err := st.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: "row-ambiguous", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterResting, CueName: "thank-you",
		ActionRevision: 1, State: nightCueStateAmbiguous, Outcome: nightCueOutcomeAmbiguous, OutcomeReason: "fabricated for this test",
	}, testNow); err != nil {
		t.Fatalf("insert ambiguous announcement row: %v", err)
	}

	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	// Duck first (as the ordinary path would have run it before the cue).
	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade": confirmedResultForAction("fade", nightBackgroundAudioSessionID(rec), "gain"),
	}
	h.nightAdvanceAnnouncementDuck(ctx, testNow, rec, nightPhaseEnterResting, cue, payload)
	h.nightAdvanceAnnouncementRestore(ctx, testNow, rec, nightPhaseEnterResting, cue, payload)

	restorePhase := nightAnnouncementRestorePhase(nightPhaseEnterResting, 1)
	restoreRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, restorePhase, "thank-you")
	if err != nil {
		t.Fatalf("restore row was never committed for an ambiguous announcement: %v", err)
	}
	if restoreRow.State != nightCueStateResolved || restoreRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore row = %+v, want resolved/confirmed even though the announcement itself is ambiguous", restoreRow)
	}
}

// TestNightAnnouncement_RestoreRetriesWhenRefused proves CRITICAL 2's
// second half: a restore attempt that resolves refused (or any other
// non-confirmed outcome) is retried under a NEW attempt rather than
// silently treated as done, so the bed does not strand at duck gain.
func TestNightAnnouncement_RestoreRetriesWhenRefused(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     {Outcome: mqttproto.OutcomeFailed, Reason: "stale_revision"},
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}
	h.nightAdvanceAnnouncementDuck(ctx, testNow, rec, nightPhaseEnterResting, cue, payload)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	attempt1Phase := nightAnnouncementRestorePhase(nightPhaseEnterResting, 1)
	attempt1, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, attempt1Phase, "thank-you")
	if err != nil || attempt1.State != nightCueStateResolved || attempt1.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("restore attempt 1 = %+v, err = %v, want resolved and refused (this test's own fabricated failure)", attempt1, err)
	}
	if _, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, attempt1Phase, "thank-you"); err != nil {
		t.Fatalf("attempt 1 row missing: %v", err)
	}

	// The retry: attempt 2 confirms.
	pub.resultsByAction["audio.gain.fade"] = confirmedResultForAction("fade", nightBackgroundAudioSessionID(rec), "gain")
	h.nightAdvanceAnnouncementRestore(ctx, testNow, rec, nightPhaseEnterResting, cue, payload)

	attempt2Phase := nightAnnouncementRestorePhase(nightPhaseEnterResting, 2)
	attempt2, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, attempt2Phase, "thank-you")
	if err != nil {
		t.Fatalf("restore attempt 2 was never committed after attempt 1 was refused: %v", err)
	}
	if attempt2.State != nightCueStateResolved || attempt2.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("restore attempt 2 = %+v, want resolved/confirmed", attempt2)
	}
	if attempt2.ActionRevision <= attempt1.ActionRevision {
		t.Fatalf("attempt 2 revision %d did not exceed attempt 1 revision %d", attempt2.ActionRevision, attempt1.ActionRevision)
	}
}

// TestNightAnnouncement_DuckAndRestoreShareOneRevisionCounter proves
// CRITICAL 2's first half directly: duck and restore draw from the SAME
// shared counter as the background session's own apply/gain/start
// steps, so restore's revision is strictly greater than duck's - never
// equal, which is exactly what the node's own RevisionState would
// refuse as stale_revision.
func TestNightAnnouncement_DuckAndRestoreShareOneRevisionCounter(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.gain.fade":     confirmedResultForAction("fade", nightBackgroundAudioSessionID(rec), "gain"),
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	duckRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementDuck+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("duck row: %v", err)
	}
	restoreRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightAnnouncementRestorePhase(nightPhaseEnterResting, 1), "thank-you")
	if err != nil {
		t.Fatalf("restore row: %v", err)
	}
	if restoreRow.ActionRevision <= duckRow.ActionRevision {
		t.Fatalf("restore revision %d did not strictly exceed duck revision %d (the counter is not genuinely shared)", restoreRow.ActionRevision, duckRow.ActionRevision)
	}
	// And both exceed the revisions already used by apply/gain/start.
	if duckRow.ActionRevision <= 3 {
		t.Fatalf("duck revision %d did not continue the session's own counter past apply/gain/start (expected > 3)", duckRow.ActionRevision)
	}
}

// TestNightAnnouncement_MixNeverDucks proves policy "mix" never touches
// background audio: no duck row is ever committed.
func TestNightAnnouncement_MixNeverDucks(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	mixPolicy := config.NightSessionAnnouncementPolicyMix
	cue := announcementCue(&mixPolicy)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	if _, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseAnnouncementDuck+":"+nightPhaseEnterResting, "thank-you"); err == nil {
		t.Fatal("expected no duck row for policy=mix, but one exists")
	}
}

// TestNightAnnouncement_InterruptSuspendsThenBackgroundResumesAfterResolve
// proves the interrupt path end to end with a HYPHENATED cue name
// (CRITICAL 3's own regression case): background audio is paused
// (resume policy "resume") to make room, and once the announcement
// resolves, the next background-audio tick resumes it.
func TestNightAnnouncement_InterruptSuspendsThenBackgroundResumesAfterResolve(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	interrupt := config.NightSessionAnnouncementPolicyInterrupt
	cue := announcementCue(&interrupt)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	pub.resultsByAction = map[string]mqttproto.ResultPayload{
		"audio.session.pause": confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started"),
		"audio.session.apply": confirmedResultForAction("apply", "announcement-1", "started"),
	}
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	pbHistory := nightBackgroundAudioPlaybackHistory(history)
	latest := pbHistory[len(pbHistory)-1]
	if latest.Step.Kind != nightBGStepInterruptPause || latest.Row.State != nightCueStateResolved || latest.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("latest background-audio step = %+v, want a resolved/confirmed interrupt pause", latest)
	}

	pub.resultsByAction = nil
	pub.result = confirmedResultForAction("resume", nightBackgroundAudioSessionID(rec), "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.lastAction != "audio.session.resume" {
		t.Fatalf("dispatched action after interrupt resolved = %q, want audio.session.resume", pub.lastAction)
	}
}

// TestNightAnnouncement_InterruptGateBlocksWhileAnnouncementUnresolved
// proves the interrupt gate actually waits: while the announcement cue's
// own row is still pending, a background-audio tick must not resume
// playback out from under the announcement still in flight.
func TestNightAnnouncement_InterruptGateBlocksWhileAnnouncementUnresolved(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	mustPutAnnouncementCueAction(t, st)
	playThroughApplyGainStart(t, h, pub, rec)

	// The interrupt suspend itself confirms normally...
	interrupt := config.NightSessionAnnouncementPolicyInterrupt
	cue := announcementCue(&interrupt)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	pub.result = confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started")
	h.nightAdvanceAnnouncementDuck(context.Background(), testNow, rec, nightPhaseEnterResting, cue, payload)

	// ...but the announcement cue's OWN row is fabricated as still
	// pending, standing in for "the announcement has not resolved yet"
	// without relying on the cueName-only crash hook, which cannot
	// distinguish this row from the interrupt-pause row above once both
	// share the announcement's own cue name (by design, so the gate can
	// look the interrupting cue up directly).
	if err := st.InsertNightCueOutboxRow(context.Background(), store.NightCueOutboxRecord{
		ID: "row-pending", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterResting, CueName: "thank-you",
		ActionRevision: 1, State: nightCueStatePending,
	}, testNow); err != nil {
		t.Fatalf("insert pending announcement row: %v", err)
	}

	pub.result = confirmedResultForAction("resume", nightBackgroundAudioSessionID(rec), "started")
	countBeforeGatedTick := pub.count()
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != countBeforeGatedTick {
		t.Fatalf("publish count = %d after a tick while the announcement is still pending, want unchanged at %d (must not resume yet)", pub.count(), countBeforeGatedTick)
	}
}
