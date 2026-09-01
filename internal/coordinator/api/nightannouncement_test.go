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

// mustRefreshNightSession re-reads rec's own row, the way the real night
// loop begins every tick: nightAdvanceCueList never mutates its rec
// parameter, so a test that re-walks the cue list across ticks (as the
// loop does) must fetch ShowCommitted and Cycle back out of the store
// itself rather than assuming the previous call's local value.
func mustRefreshNightSession(t *testing.T, st *store.Store, id string) store.NightSessionRecord {
	t.Helper()
	rec, err := st.GetNightSession(context.Background(), id)
	if err != nil {
		t.Fatalf("refresh night session: %v", err)
	}
	return rec
}

// mustPutPlainFirstCueAction binds a confirmable, non-announcement action
// suitable for a cue that is meant to BE the first outward-facing cue,
// standing in for something like a lighting or FPP cue that normally
// precedes an announcement in a real show.
func mustPutPlainFirstCueAction(t *testing.T, st *store.Store, id string) {
	t.Helper()
	putNightAction(t, st, id, config.ShowActionPayload{
		Show: "halloween", Label: "Enter show lighting", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationFPP, InstanceID: "fpp-main",
			Primitive: "startPlaylist", Params: map[string]any{"playlist": id},
		},
	})
}

// mutation target: nightAdvanceCueList's clear gate. Gating the clear on
// !isFirst alone let the skip on the commit tick evaporate the very next
// tick, since the cue list is re-walked and isFirst is then false —
// landing the clear one tick after the announcement's own start and
// cutting it off. An announcement that is the first outward-facing cue
// must get exactly one apply, one start, and no clear at all this cycle,
// no matter how many more ticks re-walk the same cue list.
func TestNightAnnouncement_FirstOutwardCue_NoClearAfterStartAcrossTicks(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	before := len(pub.dispatched)
	// Tick 1: the show is not yet committed, so this announcement is the
	// first outward-facing cue.
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterShow, []config.NightSessionCue{cue}, payload)
	rec = mustRefreshNightSession(t, st, rec.ID)
	if !rec.ShowCommitted {
		t.Fatal("show not committed after the first outward-facing cue ran")
	}

	// Ticks 2 and 3: re-walk the exact same cue list, as the real night
	// loop does every tick. isFirst is now false on both.
	for i := 0; i < 2; i++ {
		h.nightAdvanceCueList(ctx, testNow.Add(time.Duration(i+1)*time.Second), rec, testNow, nightPhaseEnterShow, []config.NightSessionCue{cue}, payload)
		rec = mustRefreshNightSession(t, st, rec.ID)
	}

	counts := map[string]int{}
	for _, d := range pub.dispatched[before:] {
		counts[d.Action]++
	}
	if counts["audio.session.clear"] != 0 {
		t.Fatalf("dispatched %d clears for the first outward-facing announcement this cycle, want 0", counts["audio.session.clear"])
	}
	if counts["audio.session.apply"] != 1 {
		t.Fatalf("dispatched %d applies, want exactly 1", counts["audio.session.apply"])
	}
	if counts["audio.session.start"] != 1 {
		t.Fatalf("dispatched %d starts, want exactly 1", counts["audio.session.start"])
	}
	if _, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterShow, "thank-you"); err == nil {
		t.Fatal("a clear step was committed for the first outward-facing cue, even after later ticks re-walked the cue list")
	}
}

// mutation target: the same clear gate, from the other direction. A
// non-first announcement must not lose its clear-before-apply ordering
// to the new per-cycle-applied check: the clear has to run on the very
// first tick, before any apply row exists for this cue this cycle.
func TestNightAnnouncement_NotFirstOutwardCue_ClearStillRunsBeforeApply(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	before := len(pub.dispatched)
	// enterResting carries no first-outward-cue boundary at all, so this
	// announcement is never "first" here.
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	got := announcementActions(pub, before)
	if len(got) < 2 || got[0] != "audio.session.clear" {
		t.Fatalf("dispatched %v, want the clear to run first, before the apply", got)
	}
}

// mutation target: nightAnnouncementAppliedThisCycle's per-cycle row
// lookup. A stale announcement session left over from an earlier cycle
// must still be cleared: otherwise it can hold the background bed ducked
// indefinitely (this file's own clear-ordering doc comment).
func TestNightAnnouncement_StaleAnnouncementFromEarlierCycleIsStillCleared(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	// Cycle 1: run the announcement to completion via enterResting, so it
	// is left applied and started, exactly as a session left playing by
	// an earlier cycle would be.
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	// Cycle 2: a fresh cycle has no outbox row yet for this cue's own
	// phase, so the clear must run again before the apply, even though a
	// clear already ran once for this same cue in the prior cycle.
	rec.Cycle++
	before := len(pub.dispatched)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)

	got := announcementActions(pub, before)
	if len(got) < 2 || got[0] != "audio.session.clear" {
		t.Fatalf("cycle 2 dispatched %v, want a clear first, stopping any announcement left over from cycle 1", got)
	}
	if _, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterResting, "thank-you"); err != nil {
		t.Fatalf("no clear row committed for cycle 2: %v", err)
	}
}

// mutation target: nightAnnouncementAppliedThisCycle scoped to the
// current cycle. Following the cut-off scenario through to its next
// cycle: once a cycle where the announcement was the first outward cue
// ends, the very next cycle must still clear before its own apply, even
// though no clear ever ran the cycle before.
func TestNightAnnouncement_NextCycleAfterFirstOutwardCueStillClearsBeforeApply(t *testing.T) {
	h, st, pub, rec, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	mustPutPlainFirstCueAction(t, st, "lights")
	duck := config.NightSessionAnnouncementPolicyDuck
	announcement := announcementCue(&duck)
	lights := config.NightSessionCue{
		Name: "lights", Role: "", Action: "lights", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue,
	}
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	// Cycle 1: the announcement is the only enterShow cue, so it is
	// itself the first outward-facing cue and gets no clear this cycle.
	before := len(pub.dispatched)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterShow, []config.NightSessionCue{announcement}, payload)
	if got := announcementActions(pub, before); len(got) > 0 && got[0] == "audio.session.clear" {
		t.Fatalf("cycle 1 dispatched %v: a clear was sent ahead of the show-commit boundary", got)
	}
	rec = mustRefreshNightSession(t, st, rec.ID)

	// Cycle 2: both cues are due from the first tick, "lights" sorts
	// first by offset and becomes the first outward-facing cue instead,
	// so the announcement is no longer first and its clear must run
	// before its apply.
	rec.Cycle++
	rec.ShowCommitted = false
	if err := st.UpdateNightSession(ctx, rec, testNow); err != nil {
		t.Fatalf("advance to cycle 2: %v", err)
	}
	before = len(pub.dispatched)
	h.nightAdvanceCueList(ctx, testNow, rec, testNow, nightPhaseEnterShow, []config.NightSessionCue{lights, announcement}, payload)

	clearRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterShow, "thank-you")
	if err != nil {
		t.Fatalf("cycle 2: no clear row for the announcement: %v", err)
	}
	applyRow, err := st.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseEnterShow, "thank-you")
	if err != nil {
		t.Fatalf("cycle 2: no apply row for the announcement: %v", err)
	}
	if clearRow.ActionRevision <= applyRow.ActionRevision {
		t.Fatalf("clear revision %d must exceed the apply's pinned %d", clearRow.ActionRevision, applyRow.ActionRevision)
	}
	var clearIdx, applyIdx = -1, -1
	for i, d := range pub.dispatched[before:] {
		if d.Action == "audio.session.clear" && clearIdx == -1 {
			clearIdx = i
		}
		if d.Action == "audio.session.apply" && applyIdx == -1 {
			if _, isLights := d.Params["playlist"]; !isLights {
				applyIdx = i
			}
		}
	}
	if clearIdx == -1 {
		t.Fatal("cycle 2: the announcement's clear was never dispatched")
	}
	if applyIdx == -1 || clearIdx > applyIdx {
		t.Fatalf("cycle 2: clear dispatched at index %d, apply at %d; the clear must run before the apply", clearIdx, applyIdx)
	}
}

// mutation target: nightAnnouncementRevisions' floor computation itself.
// Make the floor ignore persistedRevision (e.g. return applyRevision+1,
// applyRevision+2 unconditionally) and this fails: a persisted revision
// above applyRevision must still push the floor up.
//
// This is the acceptance bullet that matters most: two CONSECUTIVE night
// sessions reusing one audio session id ("announcement-1") must produce a
// clear/start pair that strictly exceeds what the first session left
// persisted, never revisions that collapse back to the bound action's own
// pinned config revision the way a fresh, empty
// nightAnnouncementHistory (keyed by the new session's own id) used to
// produce.
func TestNightAnnouncement_FloorSurvivesConsecutiveNightSessionsSharingOneAudioSession(t *testing.T) {
	h, st, pub, rec1, ba := announcementFixture(t, config.NightSessionBackgroundResumeRestart)
	mustPutAnnouncementCueAction(t, st)
	duck := config.NightSessionAnnouncementPolicyDuck
	cue := announcementCue(&duck)
	payload := announcementPayload(ba, config.NightSessionAnnouncementPolicyDuck)
	ctx := context.Background()
	pub.resultsByAction = announcementNodeResults("announcement-1")

	// Night session 1: run the announcement cue to completion, exactly as
	// a real first night would.
	h.nightAdvanceCueList(ctx, testNow, rec1, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	firstStart, err := st.GetNightCueOutboxRow(ctx, rec1.ID, rec1.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("session 1 start row: %v", err)
	}

	// A brand-new night session record - a fresh uuid, per
	// nightPrepareSiteTx, exactly as prepare-site mints for a new epoch -
	// reusing the SAME node and background-audio config, and therefore the
	// SAME announcement audio session id ("announcement-1"). Its own
	// announcement-session history is empty: nothing has ever run under
	// this session's own id.
	rec2 := store.NightSessionRecord{
		ID: "sess-2", ConfigObjectID: rec1.ConfigObjectID, ConfigRevision: rec1.ConfigRevision,
		State: nightStateRestingIntershow, StateEnteredAt: testNow, Cycle: 1,
	}
	if err := st.CreateNightSession(ctx, rec2, testNow); err != nil {
		t.Fatalf("create night session 2: %v", err)
	}
	if history, err := h.nightAnnouncementHistory(ctx, rec2); err != nil {
		t.Fatalf("session 2 history: %v", err)
	} else if len(history) != 0 {
		t.Fatalf("session 2 announcement history = %v, want empty (this is exactly the case the fix must not depend on)", history)
	}

	h.nightAdvanceCueList(ctx, testNow, rec2, testNow, nightPhaseEnterResting, []config.NightSessionCue{cue}, payload)
	secondClear, err := st.GetNightCueOutboxRow(ctx, rec2.ID, rec2.Cycle, nightPhaseAnnouncementClear+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("session 2 clear row: %v", err)
	}
	secondStart, err := st.GetNightCueOutboxRow(ctx, rec2.ID, rec2.Cycle, nightPhaseAnnouncementStart+":"+nightPhaseEnterResting, "thank-you")
	if err != nil {
		t.Fatalf("session 2 start row: %v", err)
	}

	if secondClear.ActionRevision <= firstStart.ActionRevision {
		t.Fatalf("session 2 clear revision %d did not advance past session 1's start revision %d; the floor collapsed back to the config revision instead of surviving across night sessions", secondClear.ActionRevision, firstStart.ActionRevision)
	}
	if secondStart.ActionRevision <= secondClear.ActionRevision {
		t.Fatalf("session 2 start revision %d did not advance past its own clear's %d", secondStart.ActionRevision, secondClear.ActionRevision)
	}

	persisted, err := st.GetAudioSession(ctx, "announcement-1")
	if err != nil {
		t.Fatalf("get persisted audio session: %v", err)
	}
	if int64(persisted.Revision) != secondStart.ActionRevision {
		t.Fatalf("persisted audio_sessions revision = %d, want it to track session 2's own start revision %d", persisted.Revision, secondStart.ActionRevision)
	}
}

// mutation target: nightAnnouncementRevisions' floor comparison. Ordering
// still holds regardless of which input is larger: the returned pair
// always strictly exceeds BOTH the persisted revision and the apply's own
// pinned revision, so a command built from it can never be refused as
// carrying a revision at or below either one.
func TestNightAnnouncementRevisions_FloorNeverBelowPersistedOrApplyRevision(t *testing.T) {
	cases := []struct{ persisted, apply int64 }{
		{0, 1},
		{5, 1},
		{1, 5},
		{100, 1},
	}
	for _, c := range cases {
		clear, start := nightAnnouncementRevisions(c.persisted, c.apply)
		if clear <= c.persisted || clear <= c.apply {
			t.Fatalf("persisted=%d apply=%d: clear=%d, want it to strictly exceed both", c.persisted, c.apply, clear)
		}
		if start <= clear {
			t.Fatalf("persisted=%d apply=%d: start=%d, want it to strictly exceed clear=%d", c.persisted, c.apply, start, clear)
		}
	}
}
