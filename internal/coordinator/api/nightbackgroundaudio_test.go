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

// Track F seam F5: resting.backgroundAudio's own continuous session
// lifecycle (nightbackgroundaudio.go), distinct from the cue dispatch path
// nightcue_audio_test.go already proves.

// nightBackgroundAudioTestHandlers wires a real store, a real
// identity.Service, and a fakeAudioPublisher, plus a real asset store
// (background-audio items resolve through nightResolveCurrentAsset, same
// as the resting timeline asset).
func nightBackgroundAudioTestHandlers(t *testing.T) (*handlers, *store.Store, *fakeAudioPublisher, *dynamicObservationLister) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	pub := &fakeAudioPublisher{}
	obsLister := &dynamicObservationLister{}
	deps := Dependencies{
		NightSessions: st, Config: st, Commands: st, Identity: svc,
		AudioPublisher: pub, AudioSessions: st, Assets: st, Observations: obsLister,
		ResolumeActions: &fakeResolumeActionDispatcher{},
	}.withDefaults()
	return &handlers{deps: deps, clock: fixedClock(testNow), logger: testLogger()}, st, pub, obsLister
}

// putBackgroundAudioAsset registers a current audio asset for
// (show, sequence, node) - the exact tuple a backgroundAudio item names.
func putBackgroundAudioAsset(t *testing.T, st *store.Store, show, sequence, node, assetID string) {
	t.Helper()
	_, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: assetID, ShowID: show, SequenceID: sequence, TargetKind: store.AssetTargetKindNode, TargetID: node,
		MediaType: "audio", ContentHash: "sha256:" + assetID, RuntimeFilename: assetID + ".mp3", SizeBytes: 12345,
		Backend: "volume", StorageKey: assetID, CreatedByPrincipalID: "test", CreatedByPrincipalName: "test",
	})
	if err != nil {
		t.Fatalf("create asset %q: %v", assetID, err)
	}
}

// twoItemBackgroundAudioConfig builds a minimal, valid
// resting.backgroundAudio config naming two items on the same node,
// repeat/resume/itemTransition as given.
func twoItemBackgroundAudioConfig(node, repeat, resume, itemTransition string) *config.NightSessionBackgroundAudio {
	return &config.NightSessionBackgroundAudio{
		Items: []config.NightSessionBackgroundAudioItem{
			{ItemID: "track-1", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-1", Target: node}},
			{ItemID: "track-2", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-2", Target: node}},
		},
		Repeat: repeat, Resume: resume, ItemTransition: itemTransition, MaxGainDb: -10,
	}
}

// mustCreateRestingSessionWithBackgroundAudio creates a night_sessions row
// pinned to a night.session config revision carrying ba, in the given
// state, and returns it.
func mustCreateRestingSessionWithBackgroundAudio(t *testing.T, st *store.Store, id, node string, ba *config.NightSessionBackgroundAudio, state string) store.NightSessionRecord {
	t.Helper()
	ctx := context.Background()
	payload := config.NightSessionPayload{
		Show: "halloween", Label: "Halloween main loop",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "halloween-show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "halloween-resting",
			TimelineAsset:   config.NightSessionAssetRef{Show: "halloween", Sequence: "resting-loop", Target: "fpp-main"},
			BackgroundAudio: ba,
		},
		EnterShow:                 config.NightSessionEnterShow{Cues: nil, BlackoutHoldMs: 0},
		EnterResting:              config.NightSessionEnterResting{Cues: nil, BlackoutAfterShowMs: 0},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	}
	if _, err := st.CreateConfigObject(ctx, config.NightSessionConfigKind, "halloween-main"); err != nil {
		t.Fatalf("create night.session object: %v", err)
	}
	raw, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode night.session payload: %v", err)
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 1, PayloadJSON: raw,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create night.session revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.NightSessionConfigKind, "halloween-main", 1); err != nil {
		t.Fatalf("activate night.session revision: %v", err)
	}
	rec := store.NightSessionRecord{
		ID: id, ConfigObjectID: "halloween-main", ConfigRevision: 1,
		State: state, StateEnteredAt: testNow, Cycle: 1,
	}
	if err := st.CreateNightSession(ctx, rec, testNow); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	return rec
}

func confirmedResultForAction(action, sessionID string, outcome string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session." + action,
			Value:  map[string]any{"sessionId": sessionID, "outcome": outcome},
		},
	}
}

func refusedResultForAction(action, reason string) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{Outcome: mqttproto.OutcomeFailed, Reason: reason}
}

// TestNightAdvanceBackgroundAudio_AppliesFullPinnedPlaylist proves
// CRITICAL 1's fix directly: the FIRST dispatch is audio.session.apply
// carrying a real pkgaudio.PlaylistRef (params.playlist), not a single
// params.media entry, so the node tracks real item identities rather
// than reporting the single-media constant "media" as every item id
// forever.
func TestNightAdvanceBackgroundAudio_AppliesFullPinnedPlaylist(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", pub.count())
	}
	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action = %q, want audio.session.apply", pub.lastAction)
	}
	if _, hasMedia := pub.lastParams["media"]; hasMedia {
		t.Fatalf("params carried \"media\" (single-item shape); want \"playlist\" only, params=%v", pub.lastParams)
	}
	playlist, ok := pub.lastParams["playlist"].(map[string]any)
	if !ok {
		t.Fatalf("params.playlist = %v, want a JSON object", pub.lastParams["playlist"])
	}
	if playlist["repeat"] != config.NightSessionBackgroundRepeatPlaylist {
		t.Fatalf("playlist.repeat = %v, want %q", playlist["repeat"], config.NightSessionBackgroundRepeatPlaylist)
	}
	if playlist["resume"] != config.NightSessionBackgroundResumeRestart {
		t.Fatalf("playlist.resume = %v, want %q", playlist["resume"], config.NightSessionBackgroundResumeRestart)
	}
	if playlist["requestedTransition"] != config.NightSessionItemTransitionSequential {
		t.Fatalf("playlist.requestedTransition = %v, want %q", playlist["requestedTransition"], config.NightSessionItemTransitionSequential)
	}
	itemsAny, ok := playlist["items"].([]any)
	if !ok || len(itemsAny) != 2 {
		t.Fatalf("playlist.items = %v, want 2 items", playlist["items"])
	}
	items := make([]map[string]any, len(itemsAny))
	for i, it := range itemsAny {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("playlist.items[%d] = %v, want a JSON object", i, it)
		}
		items[i] = m
	}
	if items[0]["itemId"] != "track-1" || items[0]["assetId"] != "asset-1" {
		t.Fatalf("playlist.items[0] = %v, want track-1/asset-1", items[0])
	}
	if items[1]["itemId"] != "track-2" || items[1]["assetId"] != "asset-2" {
		t.Fatalf("playlist.items[1] = %v, want track-2/asset-2", items[1])
	}
}

// TestNightAdvanceBackgroundAudio_GainBeforeStart proves MEDIUM 9's
// ordering fix: gain is sent BEFORE start, never after, so the bed is
// never audible at the node's prior gain even for one tick.
func TestNightAdvanceBackgroundAudio_GainBeforeStart(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // gain

	if pub.lastAction != "audio.gain.set" {
		t.Fatalf("second dispatched action = %q, want audio.gain.set (gain before start)", pub.lastAction)
	}
	gain, ok := pub.lastParams["gain"].(float64)
	if !ok {
		t.Fatalf("params.gain = %v, want a float64", pub.lastParams["gain"])
	}
	if gain != 0.31622776601683794 {
		t.Fatalf("gain = %v, want 0.31622776601683794 (the literal linear amplitude for -10 dB)", gain)
	}

	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start

	if pub.lastAction != "audio.session.start" {
		t.Fatalf("third dispatched action = %q, want audio.session.start", pub.lastAction)
	}
	if pub.count() != 3 {
		t.Fatalf("publish count = %d, want 3 (apply, gain, start)", pub.count())
	}
}

// TestNightAdvanceBackgroundAudio_GainRetriesWhenNotConfirmed proves
// HIGH 5's fix: a non-confirmed gain outcome (including the shipped
// agent's own "unconfirmable" when its engine is unavailable) is
// retried under a fresh revision rather than wedging the controller
// permanently at the gain step.
func TestNightAdvanceBackgroundAudio_GainRetriesWhenNotConfirmed(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply

	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeConfirmed, Reason: ""} // no evidence: unconfirmable
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)                     // gain attempt 1, unconfirmable
	firstGainCue := pub.lastAction
	if firstGainCue != "audio.gain.set" {
		t.Fatalf("first gain attempt action = %q, want audio.gain.set", firstGainCue)
	}

	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeConfirmed, Reason: ""}
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // gain attempt 2, still unconfirmable
	if pub.lastAction != "audio.gain.set" {
		t.Fatalf("retry attempt action = %q, want audio.gain.set (never wedges elsewhere)", pub.lastAction)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	gainAttempts := 0
	for _, hr := range history {
		if hr.Step.Kind == nightBGStepGain {
			gainAttempts++
		}
	}
	if gainAttempts < 2 {
		t.Fatalf("gain attempts recorded = %d, want at least 2 (each retry is a NEW row, not a silently-final one)", gainAttempts)
	}

	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // gain attempt 3, confirmed
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start
	if pub.lastAction != "audio.session.start" {
		t.Fatalf("dispatched action after gain finally confirmed = %q, want audio.session.start", pub.lastAction)
	}
}

// TestNightAdvanceBackgroundAudio_RefusesUnconfirmedItemTransition proves
// a configured "gapless"/"crossfade" transition refuses to start rather
// than silently falling back to sequential, because no audio.node
// capability signal for it exists in this codebase yet.
func TestNightAdvanceBackgroundAudio_RefusesUnconfirmedItemTransition(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionGapless)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (gapless requested, no output confirmation possible)", pub.count())
	}
}

// playThroughApplyGainStart drives rec's background audio through a
// confirmed apply/gain/start sequence, for tests that need playback
// already established.
func playThroughApplyGainStart(t *testing.T, h *handlers, pub *fakeAudioPublisher, rec store.NightSessionRecord) {
	t.Helper()
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
}

// TestNightAdvanceBackgroundAudio_NoFurtherActionOncePlaying proves the
// engine, not this controller, owns advancement and repeat once playback
// starts: many further ticks with playback confirmed dispatch nothing
// more.
func TestNightAdvanceBackgroundAudio_NoFurtherActionOncePlaying(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	countAfterStart := pub.count()
	for i := 0; i < 5; i++ {
		h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	}
	if pub.count() != countAfterStart {
		t.Fatalf("publish count = %d after 5 further ticks while playing, want unchanged at %d", pub.count(), countAfterStart)
	}
}

// TestNightStopBackgroundAudioIfRunning_ResumePolicyPauses proves resume
// policy "resume" pauses (not stops) on the way out, and a refused pause
// is retried rather than accepted as done - HIGH 4's fix.
func TestNightStopBackgroundAudioIfRunning_ResumePolicyPauses(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = refusedResultForAction("pause", "node refused")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.lastAction != "audio.session.pause" {
		t.Fatalf("dispatched action = %q, want audio.session.pause", pub.lastAction)
	}
	countAfterRefusal := pub.count()

	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.count() != countAfterRefusal+1 {
		t.Fatalf("publish count after a second call following a refused pause = %d, want %d (retried, not accepted as done)", pub.count(), countAfterRefusal+1)
	}
	if pub.lastAction != "audio.session.pause" {
		t.Fatalf("retry action = %q, want audio.session.pause", pub.lastAction)
	}

	pub.result = confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	countAfterConfirmed := pub.count()
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.count() != countAfterConfirmed {
		t.Fatalf("publish count after pause confirmed = %d, want unchanged at %d (idempotent once genuinely done)", pub.count(), countAfterConfirmed)
	}
}

// TestNightStopBackgroundAudioIfRunning_ResumePolicyRestartStops proves
// resume policy "restart" stops (not pauses) on the way out.
func TestNightStopBackgroundAudioIfRunning_ResumePolicyRestartStops(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("stop", nightBackgroundAudioSessionID(rec), "stopped")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("dispatched action = %q, want audio.session.stop", pub.lastAction)
	}
}

// TestNightAdvanceBackgroundAudio_ResumesAfterPause proves a fresh
// resting entry after a confirmed pause issues audio.session.resume, not
// a fresh apply - proven by asserting the dispatched ACTION itself, so
// deleting the resume branch entirely leaves nothing dispatched rather
// than passing by coincidence.
func TestNightAdvanceBackgroundAudio_ResumesAfterPause(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	countAfterPause := pub.count()

	pub.result = confirmedResultForAction("resume", nightBackgroundAudioSessionID(rec), "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != countAfterPause+1 {
		t.Fatalf("publish count after re-entering resting = %d, want %d (exactly one more dispatch)", pub.count(), countAfterPause+1)
	}
	if pub.lastAction != "audio.session.resume" {
		t.Fatalf("dispatched action = %q, want audio.session.resume", pub.lastAction)
	}
}

// TestNightAdvanceBackgroundAudio_ReappliesAfterStop proves a fresh
// resting entry after a confirmed stop re-applies the whole playlist
// from the top, not audio.session.resume.
func TestNightAdvanceBackgroundAudio_ReappliesAfterStop(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("stop", nightBackgroundAudioSessionID(rec), "stopped")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	pub.result = confirmedResultForAction("apply", nightBackgroundAudioSessionID(rec), "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action after re-entering resting following a stop = %q, want audio.session.apply", pub.lastAction)
	}
	if _, hasPlaylist := pub.lastParams["playlist"]; !hasPlaylist {
		t.Fatalf("re-apply params carried no playlist: %v", pub.lastParams)
	}
}

// TestNightBackgroundAudioRevisionState_RestoresCurrentFromHistory proves
// RestoreRevisionState reconstructs "current" as the highest CONFIRMED
// revision in history.
func TestNightBackgroundAudioRevisionState_RestoresCurrentFromHistory(t *testing.T) {
	history := []nightBackgroundAudioHistoryRow{
		{Step: nightBackgroundAudioStep{Kind: nightBGStepApply}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0001-apply",
			ActionRevision: 1, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
		}},
		{Step: nightBackgroundAudioStep{Kind: nightBGStepGain}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0002-gain",
			ActionRevision: 2, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
		}},
		{Step: nightBackgroundAudioStep{Kind: nightBGStepStart}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0003-start",
			ActionRevision: 3, State: nightCueStateResolved, Outcome: nightCueOutcomeFailed,
		}},
	}
	rs := nightBackgroundAudioRevisionState("night-bg:sess-1", history)
	if rs.Current() != 2 {
		t.Fatalf("restored current revision = %d, want 2 (the highest CONFIRMED step; the failed rev-3 attempt never advanced it)", rs.Current())
	}
}

// TestNightRunAudioCommand_RefusesNonAdvancingRevision proves the
// RevisionState.Apply gate this seam wires into nightRunAudioCommand
// actually refuses a new step whose revision does not strictly exceed
// history's own reconstructed current.
func TestNightRunAudioCommand_RefusesNonAdvancingRevision(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	history := []nightBackgroundAudioHistoryRow{
		{Step: nightBackgroundAudioStep{Kind: nightBGStepApply}, Row: store.NightCueOutboxRecord{
			SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseRestingBackground, CueName: "bg-0005-apply",
			ActionRevision: 5, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
		}},
	}
	target := nightAudioTarget("node-a", sessionID, "audio.session.stop", map[string]any{})

	_, err := h.nightRunAudioCommand(context.Background(), testNow, rec, nightPhaseRestingBackground, "bg-0003-stop", target, 3, history)
	if err == nil {
		t.Fatal("expected an error refusing a stale (non-advancing) revision, got nil")
	}
	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0: a refused revision must never reach the node", pub.count())
	}
}

// TestNightAdvanceBackgroundAudio_CrashAfterCommitBeforeDispatch exercises
// the crash window nightCueDispatchHooks already proves for cues, at this
// seam's own real call site.
func TestNightAdvanceBackgroundAudio_CrashAfterCommitBeforeDispatch(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	h.nightCueHooks.afterCommit = func(string) bool { return true }
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (crash landed before dispatch)", pub.count())
	}
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackground, "bg-0001-apply")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStatePending {
		t.Fatalf("row state = %q, want pending", row.State)
	}

	h.nightCueHooks.afterCommit = nil
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != 1 {
		t.Fatalf("publish count = %d after resuming past the crash, want 1 (resumed, not duplicated)", pub.count())
	}
	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d rows after resuming, want exactly 1 (the same committed row, resolved, never a second apply)", len(history))
	}
	if history[0].Row.State != nightCueStateResolved || history[0].Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("resumed row = %+v, want resolved/confirmed", history[0].Row)
	}
}

// TestNightAdvanceBackgroundAudio_CrashAfterDispatchBeforePersist proves
// the SECOND crash window: a dispatch attempt was made but its outcome
// was never recorded.
func TestNightAdvanceBackgroundAudio_CrashAfterDispatchBeforePersist(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	h.nightCueHooks.afterDispatch = func(string) bool { return true }
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackground, "bg-0001-apply")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Fatalf("row state = %q, want dispatched", row.State)
	}

	h.nightCueHooks.afterDispatch = nil
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d rows after resuming, want exactly 1", len(history))
	}
	if history[0].Row.State != nightCueStateResolved || history[0].Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("resumed row = %+v, want resolved/confirmed", history[0].Row)
	}
}

// TestNightTick_TransitionToShowDoesNotStopBackgroundAudio proves HIGH 7's
// fix: entering transition-to-show does not hard-stop background audio.
// Music is allowed to keep playing into the transition rather than
// cutting the instant this state is entered.
func TestNightTick_TransitionToShowDoesNotStopBackgroundAudio(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)
	countAfterStart := pub.count()

	rec.State = nightStateTransitionToShow
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	h.nightTick(context.Background(), testNow)

	if pub.count() != countAfterStart {
		t.Fatalf("publish count = %d after entering transition-to-show, want unchanged at %d (must not hard-stop here)", pub.count(), countAfterStart)
	}
}

// TestNightTick_LiveStopsBackgroundAudio proves the other half: once the
// state actually reaches live, background audio IS stopped.
func TestNightTick_LiveStopsBackgroundAudio(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	rec.State = nightStateLive
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}
	pub.result = confirmedResultForAction("stop", nightBackgroundAudioSessionID(rec), "stopped")

	h.nightTick(context.Background(), testNow)

	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("dispatched action = %q, want audio.session.stop", pub.lastAction)
	}
}
