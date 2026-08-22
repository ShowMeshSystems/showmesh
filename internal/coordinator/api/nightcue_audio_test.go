package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Track F seam F5: the audio show.action target dispatches through the
// SAME executeAudioSessionDispatch machinery a direct audio.session.* API
// call uses (audiodispatch.go), never a parallel path — this file proves
// that end to end, plus the pkg/audio.Outcome -> night_cue_outbox outcome
// mapping.

// nightAudioCueTestHandlers wires a real store, a real identity.Service,
// and a fakeAudioPublisher — mirroring newAudioDispatchTestSetup's own
// shape (audiodispatch_test.go) rather than nightCueTestHandlers' lighter
// no-op defaults, because executeAudioSessionDispatch genuinely needs a
// working Commands store and Identity.AuditedWrite.
func nightAudioCueTestHandlers(t *testing.T) (*handlers, *store.Store, *fakeAudioPublisher) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	pub := &fakeAudioPublisher{}
	deps := Dependencies{
		NightSessions: st, Config: st, Commands: st, Identity: svc,
		AudioPublisher: pub, AudioSessions: st,
		ResolumeActions: &fakeResolumeActionDispatcher{},
	}.withDefaults()
	return &handlers{deps: deps, clock: fixedClock(testNow), logger: testLogger()}, st, pub
}

func audioCueTarget() config.ShowActionTarget {
	return config.ShowActionTarget{
		Integration: config.ShowActionIntegrationAudio,
		AudioNodeID: "node-a", AudioSessionID: "resting-bg", AudioAction: "audio.session.stop",
	}
}

// TestNightDispatchCueAudio_ConfirmedFromNodeEvidence proves the cue
// dispatch path actually reaches executeAudioSessionDispatch: the fake
// publisher records the SAME action/node the target names, params carry
// sessionId/invocationId/revision exactly as a direct API call's own
// dispatchAudioSessionCommand would inject them, and a genuine node
// evidence outcome maps onto nightCueOutcomeConfirmed. Mutation-checked:
// replacing h.executeAudioSessionDispatch's call with a stub that always
// returns Outcome:"started" with no publish makes setup.pub.count() fail
// at 0; renaming nightAudioCueOutcome's confirmed case to something else
// makes the outcome assertion fail.
func TestNightDispatchCueAudio_ConfirmedFromNodeEvidence(t *testing.T) {
	h, _, pub := nightAudioCueTestHandlers(t)
	pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeUnconfirmed,
		Reason:  "operation applied, but the post-write read-back evidence did not match the requested value",
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.stop",
			Value:  map[string]any{"sessionId": "resting-bg", "outcome": "stopped", "reason": ""},
		},
	}
	target := audioCueTarget()
	idemKey := nightCueIdempotencyKey("sess-1", 1, nightPhaseEnterResting, "background-stop")

	result := h.nightDispatchCueAudio(context.Background(), testNow, testIssuer, target, idemKey, 7)

	if !result.resolved || result.outcome != nightCueOutcomeConfirmed {
		t.Fatalf("result = %+v, want resolved with outcome %q", result, nightCueOutcomeConfirmed)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", pub.count())
	}
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("dispatched action = %q, want audio.session.stop", pub.lastAction)
	}
	if pub.lastParams["sessionId"] != "resting-bg" {
		t.Fatalf("dispatched params.sessionId = %v, want resting-bg", pub.lastParams["sessionId"])
	}
	if pub.lastParams["invocationId"] != idemKey {
		t.Fatalf("dispatched params.invocationId = %v, want %q (the cue's own stable identity)", pub.lastParams["invocationId"], idemKey)
	}
	if rev, ok := pub.lastParams["revision"].(float64); !ok || rev != 7 {
		t.Fatalf("dispatched params.revision = %v, want 7 (the pinned action revision)", pub.lastParams["revision"])
	}
}

// TestNightDispatchCueAudio_NoEvidenceIsUnconfirmable proves a receipt
// with no evidence never reports confirmed — mapResultOutcome's own rule,
// carried through nightAudioCueOutcome. Mutation-checked: changing
// mapResultOutcome's no-evidence branch to return OutcomeStarted makes
// this fail.
func TestNightDispatchCueAudio_NoEvidenceIsUnconfirmable(t *testing.T) {
	h, _, pub := nightAudioCueTestHandlers(t)
	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeConfirmed, Reason: ""}
	target := audioCueTarget()
	idemKey := nightCueIdempotencyKey("sess-1", 1, nightPhaseEnterResting, "background-stop")

	result := h.nightDispatchCueAudio(context.Background(), testNow, testIssuer, target, idemKey, 1)

	if result.outcome != nightCueOutcomeUnconfirmable {
		t.Fatalf("outcome = %q, want %q (receipt alone is never confirmation)", result.outcome, nightCueOutcomeUnconfirmable)
	}
}

// TestNightDispatchCueAudio_ReplayNeverRedispatches proves crash recovery
// under the SAME idemKey/target never sends a second command: the second
// call must observe the SAME publish count as the first, exactly like
// dispatchFPPCommand's own idempotency-first replay — this is the direct
// evidence [nightCueRetryableByIdentity] cites for audio.
func TestNightDispatchCueAudio_ReplayNeverRedispatches(t *testing.T) {
	h, _, pub := nightAudioCueTestHandlers(t)
	pub.result = mqttproto.ResultPayload{
		Outcome:  mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{Signal: "node.audio_session.stop", Value: map[string]any{"sessionId": "resting-bg", "outcome": "stopped"}},
	}
	target := audioCueTarget()
	idemKey := nightCueIdempotencyKey("sess-1", 1, nightPhaseEnterResting, "background-stop")

	first := h.nightDispatchCueAudio(context.Background(), testNow, testIssuer, target, idemKey, 1)
	if !first.resolved || first.outcome != nightCueOutcomeConfirmed {
		t.Fatalf("first dispatch = %+v", first)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count after first dispatch = %d, want 1", pub.count())
	}

	second := h.nightDispatchCueAudio(context.Background(), testNow, testIssuer, target, idemKey, 1)
	if !second.resolved || second.outcome != nightCueOutcomeConfirmed {
		t.Fatalf("second (replay) dispatch = %+v, want the same resolved outcome", second)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count after replay = %d, want STILL 1 (no re-send under the same identity)", pub.count())
	}
}

// TestNightRunCue_AudioTargetEndToEnd proves the audio integration is
// reachable through the ordinary cue-advance path (nightRunCue), not only
// by calling nightDispatchCueAudio directly: an enterResting cue bound to
// an audio show.action commits an outbox row and resolves it from real
// node evidence.
func TestNightRunCue_AudioTargetEndToEnd(t *testing.T) {
	h, st, pub := nightAudioCueTestHandlers(t)
	pub.result = mqttproto.ResultPayload{
		Outcome:  mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{Signal: "node.audio_session.stop", Value: map[string]any{"sessionId": "resting-bg", "outcome": "stopped"}},
	}
	putNightAction(t, st, "hush-background", config.ShowActionPayload{
		Show: "halloween", Label: "Hush background audio", SafetyClass: config.ShowSafetyClassStop,
		Target: audioCueTarget(),
	})
	rec := mustCreateTransitionToShowSession(t, st, "sess-audio", 1, testNow)
	cue := config.NightSessionCue{Name: "hush", Role: config.NightSessionCueRoleAudio, Action: "hush-background", OffsetMs: 0, OnFailure: config.NightSessionCueOnFailureContinue}

	row, err := h.nightRunCue(context.Background(), testNow, rec, nightPhaseEnterResting, cue, testIssuer, false)
	if err != nil {
		t.Fatalf("nightRunCue: %v", err)
	}
	if row.State != nightCueStateResolved || row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("row = %+v, want resolved/confirmed", row)
	}
	if pub.lastAction != "audio.session.stop" || pub.lastParams["sessionId"] != "resting-bg" {
		t.Fatalf("dispatched action/params = %q/%v, want audio.session.stop/resting-bg", pub.lastAction, pub.lastParams)
	}
}

// TestNightAudioCueOutcome defends the pkg/audio.Outcome ->
// night_cue_outbox mapping this seam adds. Mutation-checked: deleting the
// OutcomeCompleted case (falling to default) fails "completed"; changing
// the Unconfirmable case to return nightCueOutcomeFailed fails
// "unconfirmable"; changing Refused's case to Confirmed fails "refused".
func TestNightAudioCueOutcome(t *testing.T) {
	cases := []struct {
		outcome string
		want    string
	}{
		{"started", nightCueOutcomeConfirmed},
		{"position", nightCueOutcomeConfirmed},
		{"gain", nightCueOutcomeConfirmed},
		{"fade_complete", nightCueOutcomeConfirmed},
		{"stopped", nightCueOutcomeConfirmed},
		{"completed", nightCueOutcomeConfirmed},
		{"unconfirmable", nightCueOutcomeUnconfirmable},
		{"refused", nightCueOutcomeRefused},
		{"failed", nightCueOutcomeFailed},
		{"something-unrecognized", nightCueOutcomeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			if got := nightAudioCueOutcome(tc.outcome); got != tc.want {
				t.Errorf("nightAudioCueOutcome(%q) = %q, want %q", tc.outcome, got, tc.want)
			}
		})
	}
}
