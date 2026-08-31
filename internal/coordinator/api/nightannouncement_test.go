package api

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
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

// announcementNodeResults answers each dispatched action with the
// outcome a real node actually produces for it. An apply reports
// "position" (Manager.Apply merges desired state and never touches the
// engine), a start reports "started", a clear reports "stopped". Spelling
// them truthfully matters: an earlier version of this file answered an
// apply with "started", which is a result no node can produce, and that
// single lie hid the fact that the announcement was applied and never
// started at all.
func announcementNodeResults(announcementSessionID string) map[string]mqttproto.ResultPayload {
	return map[string]mqttproto.ResultPayload{
		"audio.session.apply": confirmedResultForAction("apply", announcementSessionID, "position"),
		"audio.session.start": confirmedResultForAction("start", announcementSessionID, "started"),
		"audio.session.clear": confirmedResultForAction("clear", announcementSessionID, "stopped"),
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
			pub.resultsByAction = announcementNodeResults("announcement-1")
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

	pub.resultsByAction = announcementNodeResults("announcement-1")
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

	pub.resultsByAction = announcementNodeResults("announcement-1")
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

// mutation target: each branch of nightCheckAnnouncementPolicyEnforceable.
// Every case here is one this build genuinely cannot serve, and each was
// reported healthy before.
func TestNightCheckAnnouncementPolicyEnforceable(t *testing.T) {
	h, st, _, _, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	ctx := context.Background()
	duck := config.NightSessionAnnouncementPolicyDuck
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	noBed := announcementPayload(nil, config.NightSessionAnnouncementPolicyDuck)
	cues := []config.NightSessionCue{announcementCue(&duck)}

	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, nil, payload); check.health != nightCheckStateNotConfigured {
		t.Fatalf("no announcement cues: health = %v, want not_configured", check.health)
	}

	mustPutAnnouncementCueAction(t, st)
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, cues, payload); check.health != nightHealthHealthy() {
		t.Fatalf("audio-bound announcement: health = %v (%s), want healthy", check.health, check.reason)
	}

	// Not bound to audio.session.apply: nothing to declare the policy on,
	// and nothing for this controller's own start step to start.
	mustPutAnnouncementFPPAction(t, st, "over-the-tannoy")
	fppCue := announcementCue(&duck)
	fppCue.Name, fppCue.Action = "over-the-tannoy", "over-the-tannoy"
	check := h.nightCheckAnnouncementPolicyEnforceable(ctx, []config.NightSessionCue{fppCue}, payload)
	if check.health != nightHealthFailed() {
		t.Fatalf("FPP-bound announcement: health = %v (%s), want failed", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "over-the-tannoy") {
		t.Fatalf("reason %q does not name the offending cue", check.reason)
	}
	// Failed with no bed too: it does not play at all, which has nothing
	// to do with whether there is background audio to duck.
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, []config.NightSessionCue{fppCue}, noBed); check.health != nightHealthFailed() {
		t.Fatalf("FPP-bound announcement with no background audio: health = %v (%s), want failed", check.health, check.reason)
	}

	// Operator params contradicting the cue's configured policy.
	putNightAction(t, st, "contradicted", config.ShowActionPayload{
		Show: "halloween", Label: "Contradicted", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationAudio, AudioNodeID: "node-a",
			AudioSessionID: "announcement-2", AudioAction: "audio.session.apply",
			Params: map[string]any{"mixPolicy": "mix"},
		},
	})
	contra := announcementCue(&duck)
	contra.Name, contra.Action = "contradicted", "contradicted"
	check = h.nightCheckAnnouncementPolicyEnforceable(ctx, []config.NightSessionCue{contra}, payload)
	if check.health != nightHealthFailed() {
		t.Fatalf("contradicted policy: health = %v (%s), want failed", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "duck") || !strings.Contains(check.reason, "mix") {
		t.Fatalf("reason %q does not name both the cue policy and the action's own", check.reason)
	}

	// A source role that cannot outrank the bed ducks nothing.
	putNightAction(t, st, "too-quiet", config.ShowActionPayload{
		Show: "halloween", Label: "Too quiet", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationAudio, AudioNodeID: "node-a",
			AudioSessionID: "announcement-3", AudioAction: "audio.session.apply",
			Params: map[string]any{"sourceRole": string(pkgaudio.SourceRoleBackground)},
		},
	})
	lowRole := announcementCue(&duck)
	lowRole.Name, lowRole.Action = "too-quiet", "too-quiet"
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, []config.NightSessionCue{lowRole}, payload); check.health != nightHealthFailed() {
		t.Fatalf("source role that cannot outrank the bed: health = %v (%s), want failed", check.health, check.reason)
	}
	// With no bed there is nothing to outrank, so it is not a failure.
	if check := h.nightCheckAnnouncementPolicyEnforceable(ctx, []config.NightSessionCue{lowRole}, noBed); check.health != nightHealthHealthy() {
		t.Fatalf("low source role with no background audio: health = %v (%s), want healthy", check.health, check.reason)
	}
}

// mutation target: pkgaudio.OutranksForMixing's strictly-greater
// comparison, which the node and this readiness check now share instead
// of each carrying their own copy of the order.
func TestSourceRolePriorityIsSharedWithTheNode(t *testing.T) {
	if pkgaudio.OutranksForMixing(pkgaudio.SourceRoleBackground, pkgaudio.SourceRoleBackground) {
		t.Fatal("background outranks background; a duck would then suppress its own peers")
	}
	if !pkgaudio.OutranksForMixing(pkgaudio.SourceRoleAnnouncement, pkgaudio.SourceRoleBackground) {
		t.Fatal("announcement does not outrank background")
	}
	if pkgaudio.OutranksForMixing(pkgaudio.SourceRole("not-a-role"), pkgaudio.SourceRoleBackground) {
		t.Fatal("an unrecognized role outranks background")
	}
}

// announcementActions returns the actions dispatched at or after from,
// in order.
func announcementActions(pub *fakeAudioPublisher, from int) []string {
	out := make([]string, 0, len(pub.dispatched)-from)
	for _, d := range pub.dispatched[from:] {
		out = append(out, d.Action)
	}
	return out
}

// mutation target: nightAdvanceCueList's two announcement hooks. Delete
// the start hook and the announcement is applied and never started,
// which internal/agent/nightannouncement_test.go proves plays nothing and
// ducks nothing; delete the clear hook and an announcement left playing
// by an earlier cycle keeps holding the bed.
func TestNightAnnouncement_DispatchesClearThenApplyThenStart(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	before := len(pub.dispatched)
	pub.resultsByAction = announcementNodeResults("announcement-1")
	h.nightAdvanceCueList(context.Background(), testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	got := announcementActions(pub, before)
	want := []string{"audio.session.clear", "audio.session.apply", "audio.session.start"}
	if len(got) != len(want) {
		t.Fatalf("dispatched %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dispatched %v, want %v", got, want)
		}
	}
	assertNoBackgroundCommandsAfter(t, pub, before)

	ctx := context.Background()
	startRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("the start was not committed as a durable outbox row: %v", err)
	}
	if startRow.State != nightCueStateResolved || startRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("start row = %+v, want resolved/confirmed", startRow)
	}
	clearRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("the clear was not committed as a durable outbox row: %v", err)
	}
	applyRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("apply row: %v", err)
	}
	// The revision order the node's own RevisionState requires: clear,
	// then the pinned apply on the session the clear deleted, then start
	// strictly above both.
	if clearRow.ActionRevision <= applyRow.ActionRevision {
		t.Fatalf("clear revision %d must exceed the apply's pinned %d", clearRow.ActionRevision, applyRow.ActionRevision)
	}
	if startRow.ActionRevision <= clearRow.ActionRevision {
		t.Fatalf("start revision %d must exceed the clear's %d", startRow.ActionRevision, clearRow.ActionRevision)
	}
}

// mutation target: nightAnnouncementRevisions' floor computation over
// history. Return a constant and the second cycle's clear or start is
// dispatched at a revision the node has already seen, which its own
// RevisionState refuses as stale.
func TestNightAnnouncement_RevisionsAdvanceAcrossCycles(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	firstStart, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("cycle 1 start row: %v", err)
	}

	rec.Cycle++
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	secondClear, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("cycle 2 clear row: %v", err)
	}
	secondStart, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("cycle 2 start row: %v", err)
	}
	if secondClear.ActionRevision <= firstStart.ActionRevision {
		t.Fatalf("cycle 2 clear revision %d did not advance past cycle 1's start revision %d", secondClear.ActionRevision, firstStart.ActionRevision)
	}
	if secondStart.ActionRevision <= secondClear.ActionRevision {
		t.Fatalf("cycle 2 start revision %d did not advance past its own clear's %d", secondStart.ActionRevision, secondClear.ActionRevision)
	}
}

// mutation target: nightAdvanceAnnouncementStart's terminal-not-confirmed
// gate. Require a confirmed apply and this fails with no start row: an
// apply the node refused because it already holds this exact desired
// state is not a reason to leave the announcement silent, and a genuinely
// broken one must fail visibly in the start's own row rather than by a
// step that was silently never attempted.
func TestNightAnnouncement_StartsEvenWhenTheApplyDidNotConfirm(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()

	results := announcementNodeResults("announcement-1")
	results["audio.session.apply"] = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeFailed, Reason: "stale_revision"}
	pub.resultsByAction = results

	before := len(pub.dispatched)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	applyRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseEnterResting, "thank-you")
	if err != nil || applyRow.Outcome == nightCueOutcomeConfirmed {
		t.Fatalf("apply row = %+v, err = %v; this test needs an unconfirmed apply", applyRow, err)
	}
	startRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("no start row after an unconfirmed apply: %v (a silent announcement must never be the quiet outcome of a skipped step)", err)
	}
	if startRow.State != nightCueStateResolved || startRow.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("start row = %+v, want resolved/confirmed", startRow)
	}
	assertNoBackgroundCommandsAfter(t, pub, before)
}

// mutation target: nightAnnouncementSessionTarget's declarable check.
// A non-audio announcement gets neither a clear nor a start: there is no
// session to address, and inventing one would dispatch commands at a node
// that has never heard of it.
func TestNightAnnouncement_NonAudioActionGetsNoClearOrStart(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementFPPAction(t, st, "thank-you")
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)

	before := len(pub.dispatched)
	ctx := context.Background()
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	if got := announcementActions(pub, before); len(got) != 0 {
		t.Fatalf("an FPP-bound announcement dispatched %v to the audio node", got)
	}
	// Asserted on the outbox, not only on what reached the publisher: a
	// step is committed BEFORE it is dispatched, so a row exists even for
	// a command that never made it onto the wire.
	for _, phase := range []string{
		nightPhaseAnnouncementClear + ":" + nightPhaseEnterResting,
		nightPhaseAnnouncementStart + ":" + nightPhaseEnterResting,
	} {
		if row, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, "thank-you"); err == nil {
			t.Fatalf("an FPP-bound announcement committed a %q step: %+v", phase, row)
		}
	}
}

// mutation target: nightAdvanceCueList's !isFirst guard on the clear.
// The first outward-facing cue IS the atomic show-commit boundary, so
// nothing outward-facing may be dispatched ahead of it.
func TestNightAnnouncement_NoClearAheadOfTheShowCommitBoundary(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	before := len(pub.dispatched)
	// enterShow with the show not yet committed: this announcement is the
	// first outward-facing cue.
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterShow, []config.NightSessionCue{cue}, payload)

	got := announcementActions(pub, before)
	if len(got) > 0 && got[0] == "audio.session.clear" {
		t.Fatalf("dispatched %v: a clear was sent ahead of the atomic show-commit boundary", got)
	}
	if _, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterShow, "thank-you"); err == nil {
		t.Fatal("a clear step was committed for the first outward-facing cue")
	}
	// The start still happens: it runs after the cue, by which point the
	// boundary has been crossed.
	if _, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterShow, "thank-you"); err != nil {
		t.Fatalf("no start step after the first outward-facing announcement cue: %v", err)
	}
}

// mutation target: mapNightBackgroundAudio's announcement-phase read and
// the sequence tag on each step. Drop either and an announcement's clear
// and start are durable rows reachable from no surface: a refused clear,
// which means a previous announcement may still be playing and still
// holding the bed ducked, would live only in a log line.
func TestNightAnnouncement_StepsAreOnTheOperatorSurface(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	surface := mapNightBackgroundAudio(ctx, h.deps, rec, true)
	if surface.State != v1.NightEvidenceRecorded {
		t.Fatalf("background-audio surface state = %q, want recorded", surface.State)
	}
	byKind := map[string]v1.NightBackgroundAudioStep{}
	for _, step := range surface.Steps {
		byKind[step.Kind] = step
	}
	for _, kind := range []string{nightAnnouncementStepClear, nightAnnouncementStepStart} {
		step, ok := byKind[kind]
		if !ok {
			t.Fatalf("no %q step on the operator surface; the announcement sequence is durable and reachable from nothing", kind)
		}
		if step.Sequence != v1.NightAudioSequenceAnnouncement {
			t.Fatalf("%q step sequence = %q, want %q so an operator can tell which sequence a failure belongs to", kind, step.Sequence, v1.NightAudioSequenceAnnouncement)
		}
		if step.CueName != "thank-you" {
			t.Fatalf("%q step cueName = %q, want the announcement cue's own name", kind, step.CueName)
		}
	}
	// Background audio's own steps are still there, still tagged as
	// background: adding one sequence must not swallow the other.
	if step, ok := byKind[nightBGStepStart]; !ok || step.Sequence != v1.NightAudioSequenceBackground {
		t.Fatalf("background start step = %+v (present=%v), want it present and tagged background", step, ok)
	}
}
