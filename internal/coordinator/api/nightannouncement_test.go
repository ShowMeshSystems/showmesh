package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Track F seam F5's announcement policy declaration (nightannouncement.go).
// The node enforces duck/mix/interrupt from what is declared on the
// announcement's own session; this controller declares it and never
// drives background audio's gain or playback around a cue. The proof that
// the node actually honors the declaration lives against a real
// audio.Manager in internal/agent/audio/nightduck_test.go.

func announcementAudioAction() config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationAudio,
		AudioNodeID: "node-a", AudioSessionID: "announcement-1", AudioAction: "audio.session.apply",
	}
}

// mustPutAnnouncementCueAction registers a hyphenated cue action name
// deliberately: an earlier version of this seam broke on exactly this
// shape, so every announcement test below exercises it rather than a name
// with no hyphen that would never have caught the bug.
func mustPutAnnouncementCueAction(t *testing.T, st *store.Store) {
	t.Helper()
	putNightAction(t, st, "thank-you", config.ShowActionPayload{
		Show: "halloween", Label: "Thank you announcement", SafetyClass: config.ShowSafetyClassNone,
		Target: announcementAudioAction(),
	})
}

// mustPutAnnouncementFPPAction binds the same cue name to a NON-audio
// action, standing in for an announcement played through FPP: nothing a
// mix policy can be declared on.
func mustPutAnnouncementFPPAction(t *testing.T, st *store.Store, id string) {
	t.Helper()
	putNightAction(t, st, id, config.ShowActionPayload{
		Show: "halloween", Label: "Thank you announcement", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationFPP, InstanceID: "fpp-main",
			Primitive: "startPlaylist", Params: map[string]any{"playlist": id},
		},
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

// announcementFixture is every announcement test's identical setup:
// background audio configured, applied, gained and started on node-a.
func announcementFixture(t *testing.T, resume string) (*handlers, *store.Store, *fakeAudioPublisher, store.NightSessionRecord, *config.NightSessionBackgroundAudio) {
	t.Helper()
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, resume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)
	return h, st, pub, rec, ba
}

// announcementApplyParams returns the params of the single dispatched
// audio.session.apply addressed at the announcement's own session.
func announcementApplyParams(t *testing.T, pub *fakeAudioPublisher) map[string]any {
	t.Helper()
	var found map[string]any
	for _, d := range pub.dispatched {
		if d.Action != "audio.session.apply" {
			continue
		}
		// The background session's own apply always carries a playlist;
		// the announcement's never does in these fixtures.
		if _, isBackground := d.Params["playlist"]; isBackground {
			continue
		}
		found = d.Params
	}
	if found == nil {
		t.Fatal("no audio.session.apply was dispatched for the announcement session")
	}
	return found
}

// assertNoGainCommandsAfter fails when any audio.gain.* or background
// pause/stop/resume command was dispatched at or after index from. This is
// the regression guard for the removed coordinator-side duck: reinstating
// any of it makes this fail.
func assertNoBackgroundCommandsAfter(t *testing.T, pub *fakeAudioPublisher, from int) {
	t.Helper()
	for _, d := range pub.dispatched[from:] {
		if strings.HasPrefix(d.Action, "audio.gain.") ||
			d.Action == "audio.session.pause" || d.Action == "audio.session.stop" || d.Action == "audio.session.resume" {
			t.Fatalf("announcement handling dispatched %q at background audio; the node owns making room for an announcement and this controller must send nothing", d.Action)
		}
	}
}

// mutation target: nightAnnouncementDeclaredTarget's two params writes.
// Drop either and the assertion fails; the node would then see an
// announcement with no source role and no mix policy, and would never
// duck the bed at all.
func TestNightAnnouncement_DeclaresRoleAndPolicyOnTheAnnouncementSession(t *testing.T) {
	for _, policy := range []string{
		config.NightSessionAnnouncementPolicyDuck,
		config.NightSessionAnnouncementPolicyMix,
		config.NightSessionAnnouncementPolicyInterrupt,
	} {
		t.Run(policy, func(t *testing.T) {
			h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
			mustPutAnnouncementCueAction(t, st)
			cue := announcementCue(&policy)
			payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyMix)

			before := len(pub.dispatched)
			pub.result = confirmedResultForAction("apply", "announcement-1", "started")
			h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

			params := announcementApplyParams(t, pub)
			if params["sourceRole"] != "announcement" {
				t.Fatalf("announcement apply sourceRole = %v, want \"announcement\"", params["sourceRole"])
			}
			if params["mixPolicy"] != policy {
				t.Fatalf("announcement apply mixPolicy = %v, want %q", params["mixPolicy"], policy)
			}
			assertNoBackgroundCommandsAfter(t, pub, before)
		})
	}
}

// mutation target: nightAnnouncementCueWithResolvedPolicy's fallback to
// payload.AnnouncementDefaultPolicy. Return the cue unchanged and the
// declared policy becomes empty, failing here.
func TestNightAnnouncement_UnsetCuePolicyFallsBackToTheSessionDefault(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	cue := announcementCue(nil)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyInterrupt)

	pub.result = confirmedResultForAction("apply", "announcement-1", "started")
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	params := announcementApplyParams(t, pub)
	if params["mixPolicy"] != config.NightSessionAnnouncementPolicyInterrupt {
		t.Fatalf("mixPolicy = %v, want the session default %q", params["mixPolicy"], config.NightSessionAnnouncementPolicyInterrupt)
	}
}

// mutation target: nightAnnouncementDeclaredTarget's two `if _, ok :=
// params[...]; !ok` guards. Overwrite unconditionally and this fails: a
// show.action that spells its own policy out is the operator's decision,
// not this controller's to override.
func TestNightAnnouncement_OperatorDeclaredParamsAreNeverOverwritten(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	putNightAction(t, st, "thank-you", config.ShowActionPayload{
		Show: "halloween", Label: "Thank you announcement", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationAudio,
			AudioNodeID: "node-a", AudioSessionID: "announcement-1", AudioAction: "audio.session.apply",
			Params: map[string]any{"sourceRole": "manual", "mixPolicy": "mix"},
		},
	})
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	pub.result = confirmedResultForAction("apply", "announcement-1", "started")
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	params := announcementApplyParams(t, pub)
	if params["sourceRole"] != "manual" || params["mixPolicy"] != "mix" {
		t.Fatalf("params = %v, want the operator's own sourceRole/mixPolicy left exactly as authored", params)
	}
}

// mutation target: nightAnnouncementTargetDeclarable's integration/action
// check. Remove it and this controller starts injecting audio params into
// an FPP dispatch, which fails here.
func TestNightAnnouncement_NonAudioActionIsDispatchedUntouched(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementFPPAction(t, st, "thank-you")
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	before := len(pub.dispatched)
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	// Nothing at all was sent to the audio node: not a policy declaration
	// it could not carry, and above all not a gain command at background
	// audio that would strand the bed with no way to know when the FPP
	// announcement ended.
	assertNoBackgroundCommandsAfter(t, pub, before)
	for _, d := range pub.dispatched[before:] {
		t.Fatalf("an FPP-bound announcement dispatched %q to the audio node", d.Action)
	}

	// And the FPP target itself is handed to its own integration byte for
	// byte, with no audio vocabulary smuggled into its params.
	fpp := config.ShowActionTarget{
		Integration: config.ShowActionIntegrationFPP, InstanceID: "fpp-main",
		Primitive: "startPlaylist", Params: map[string]any{"playlist": "thank-you"},
	}
	resolved := nightAnnouncementCueWithResolvedPolicy(cue, payload)
	got := nightAnnouncementDeclaredTarget(resolved, fpp)
	if len(got.Params) != 1 || got.Params["playlist"] != "thank-you" {
		t.Fatalf("FPP announcement params = %v, want the authored map unchanged", got.Params)
	}
}

// mutation target: nightAdvanceCueList's removal of the duck/restore
// hooks. This is the stranded-quiet defect class the seam exists to
// close, stated as an invariant rather than as a repair sequence: a
// FAILED announcement leaves background audio exactly as it was, because
// nothing about the bed was changed to need undoing.
func TestNightAnnouncement_FailedAnnouncementLeavesBackgroundAudioUntouched(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	before := len(pub.dispatched)
	historyBefore, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeFailed, Reason: "node refused to apply"}
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseEnterResting, "thank-you")
	if err != nil || row.State != nightCueStateResolved || row.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("announcement row = %+v, err = %v; this test needs a resolved, non-confirmed announcement", row, err)
	}
	assertNoBackgroundCommandsAfter(t, pub, before)
	historyAfter, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(historyAfter) != len(historyBefore) {
		t.Fatalf("background-audio history grew from %d to %d rows around a failed announcement; the bed must be left alone", len(historyBefore), len(historyAfter))
	}
	last := historyAfter[len(historyAfter)-1]
	if last.Step.Kind != nightBGStepStart || last.Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("latest background-audio step = %+v, want the confirmed start it was already at", last)
	}
}

// mutation target: the same removal, for the AMBIGUOUS outcome
// specifically. An announcement that never resolves cleanly must still
// leave the bed at its configured gain, and it does so here by never
// having moved it.
func TestNightAnnouncement_AmbiguousAnnouncementLeavesBackgroundAudioUntouched(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	ctx := context.Background()
	if err := st.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: "row-ambiguous", SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEnterResting, CueName: "thank-you",
		ActionRevision: 1, State: nightCueStateAmbiguous, Outcome: nightCueOutcomeAmbiguous, OutcomeReason: "fabricated for this test",
	}, testNow); err != nil {
		t.Fatalf("insert ambiguous announcement row: %v", err)
	}
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	before := len(pub.dispatched)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	assertNoBackgroundCommandsAfter(t, pub, before)
}

// mutation target: nightCheckAnnouncementPolicyEnforceable's
// nightAnnouncementTargetDeclarable branch. The one case this controller
// genuinely cannot serve is reported rather than silently ignored.
func TestNightCheckAnnouncementPolicyEnforceable(t *testing.T) {
	h, st, _, _, _ := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	ctx := context.Background()
	duck := config.NightSessionAnnouncementPolicyDuck
	cues := []config.NightSessionCue{announcementCue(&duck)}

	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, nil, true); check.health != nightCheckStateNotConfigured {
		t.Fatalf("no announcement cues: health = %v, want not_configured", check.health)
	}

	mustPutAnnouncementCueAction(t, st)
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, cues, true); check.health != nightHealthHealthy() {
		t.Fatalf("audio-bound announcement: health = %v (%s), want healthy", check.health, check.reason)
	}

	mustPutAnnouncementFPPAction(t, st, "over-the-tannoy")
	fppCue := announcementCue(&duck)
	fppCue.Name, fppCue.Action = "over-the-tannoy", "over-the-tannoy"
	fppCues := []config.NightSessionCue{fppCue}
	check := h.nightCheckAnnouncementPolicyEnforceable(ctx, fppCues, true)
	if check.health != nightHealthFailed() {
		t.Fatalf("FPP-bound announcement with background audio configured: health = %v (%s), want failed", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "over-the-tannoy") {
		t.Fatalf("reason %q does not name the offending cue", check.reason)
	}
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, fppCues, false); check.health != nightHealthHealthy() {
		t.Fatalf("FPP-bound announcement with no background audio: health = %v (%s), want healthy", check.health, check.reason)
	}
}
