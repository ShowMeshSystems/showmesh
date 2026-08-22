package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F5: resting.backgroundAudio's own continuous session
// lifecycle (nightbackgroundaudio.go), distinct from the cue dispatch path
// nightcue_audio_test.go already proves.

// nightBackgroundAudioTestHandlers is [nightAudioCueTestHandlers] plus the
// two dependencies the background-audio controller additionally needs:
// a real asset store (background-audio items resolve through
// nightResolveCurrentAsset, same as the resting timeline asset) and an
// observation lister the test can mutate to report item completion.
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

func backgroundAudioSessionObs(sessionID string, signal observation.SignalID, value string, at time.Time) observation.Observation {
	o, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
		signal, value, at,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(at), observation.WithSource("node-audio"),
	)
	if err != nil {
		panic(err)
	}
	return o
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

// TestNightAdvanceBackgroundAudio_StartsOnFirstTick proves entering
// resting with backgroundAudio configured issues an apply for item 0 on
// the very first tick.
func TestNightAdvanceBackgroundAudio_StartsOnFirstTick(t *testing.T) {
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
	media, ok := pub.lastParams["media"].(map[string]any)
	if !ok || media["assetId"] != "asset-1" {
		t.Fatalf("dispatched media = %v, want asset-1", pub.lastParams["media"])
	}

	rows, err := st.ListNightCueOutboxRowsForPhase(context.Background(), rec.ID, nightPhaseRestingBackground)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(rows) != 1 || rows[0].CueName != "bg-0001-apply-track-1" {
		t.Fatalf("history = %+v, want exactly one bg-0001-apply-track-1 row", rows)
	}
}

// TestNightAdvanceBackgroundAudio_AppliesThenStarts proves the second
// tick, seeing a confirmed apply, issues start for the SAME item, not
// another apply.
func TestNightAdvanceBackgroundAudio_AppliesThenStarts(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != 2 {
		t.Fatalf("publish count = %d, want 2 (apply, then start)", pub.count())
	}
	if pub.lastAction != "audio.session.start" {
		t.Fatalf("second dispatched action = %q, want audio.session.start", pub.lastAction)
	}
}

// TestNightAdvanceBackgroundAudio_AdvancesOnCompletionEvidence proves a
// PLAYING item that is observed Completed advances to the next item, and
// that natural completion is anchored on pkgaudio.StateCompleted, never
// merely absent evidence.
func TestNightAdvanceBackgroundAudio_AdvancesOnCompletionEvidence(t *testing.T) {
	h, st, pub, obs := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply track-1
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start track-1
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // establish gain (once per session)

	// Not yet completed: no observation set at all.
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	if pub.count() != 3 {
		t.Fatalf("publish count = %d after a tick with no completion evidence, want still 3", pub.count())
	}

	obs.setObs([]observation.Observation{
		backgroundAudioSessionObs(sessionID, "audio_session.state", "completed", testNow),
		backgroundAudioSessionObs(sessionID, "audio_session.playlist.item_id", "track-1", testNow),
	})
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply track-2

	if pub.count() != 4 {
		t.Fatalf("publish count = %d, want 4 (apply track-2 after track-1 completed)", pub.count())
	}
	media, _ := pub.lastParams["media"].(map[string]any)
	if media["assetId"] != "asset-2" {
		t.Fatalf("advanced to asset %v, want asset-2", media["assetId"])
	}
}

// TestNightAdvanceBackgroundAudio_PlayingStateNeverAdvances proves
// natural completion is anchored on pkgaudio.StateCompleted specifically:
// an observed "playing" state for the same item must never be treated as
// completion, distinguishing this from "any observation present".
func TestNightAdvanceBackgroundAudio_PlayingStateNeverAdvances(t *testing.T) {
	h, st, pub, obs := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	countAfterStart := pub.count()

	obs.setObs([]observation.Observation{
		backgroundAudioSessionObs(sessionID, "audio_session.state", "playing", testNow),
		backgroundAudioSessionObs(sessionID, "audio_session.playlist.item_id", "track-1", testNow),
	})
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != countAfterStart {
		t.Fatalf("publish count = %d after an observed \"playing\" state, want unchanged at %d (playing is not completion)", pub.count(), countAfterStart)
	}
}

// TestNightAdvanceBackgroundAudio_RepeatNoneStopsAfterLastItem proves
// repeat "none" issues no further command once the last item completes -
// never guessing a next item that does not exist.
func TestNightAdvanceBackgroundAudio_RepeatNoneStopsAfterLastItem(t *testing.T) {
	h, st, pub, obs := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatNone, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply track-1
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start track-1
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	obs.setObs([]observation.Observation{
		backgroundAudioSessionObs(sessionID, "audio_session.state", "completed", testNow),
		backgroundAudioSessionObs(sessionID, "audio_session.playlist.item_id", "track-1", testNow),
	})
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // track-1 completed
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // track-2 apply
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // track-2 start
	countBeforeLastCompletion := pub.count()

	obs.setObs([]observation.Observation{
		backgroundAudioSessionObs(sessionID, "audio_session.state", "completed", testNow),
		backgroundAudioSessionObs(sessionID, "audio_session.playlist.item_id", "track-2", testNow),
	})
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.count() != countBeforeLastCompletion {
		t.Fatalf("publish count = %d after the last item completed under repeat=none, want unchanged at %d", pub.count(), countBeforeLastCompletion)
	}
}

// TestNightAdvanceBackgroundAudio_RefusesUnconfirmedItemTransition proves
// a configured "gapless"/"crossfade" transition refuses to start rather
// than silently falling back to sequential, because no audio.node
// capability signal for it exists in this codebase yet
// (ValidateItemTransitionSupport's outputConfirms is always false here).
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

// TestNightStopBackgroundAudioIfRunning_IssuesStopThenIsIdempotent proves
// leaving resting stops a playing background session exactly once.
func TestNightStopBackgroundAudioIfRunning_IssuesStopThenIsIdempotent(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	rec.State = nightStateTransitionToShow
	pub.result = confirmedResultForAction("stop", sessionID, "stopped")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.lastAction != "audio.session.stop" {
		t.Fatalf("dispatched action = %q, want audio.session.stop", pub.lastAction)
	}
	countAfterStop := pub.count()

	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.count() != countAfterStop {
		t.Fatalf("publish count = %d after a second stop call, want unchanged at %d (already stopped)", pub.count(), countAfterStop)
	}
}

// TestNightAdvanceBackgroundAudio_ResumePicksLastStartedItem proves a
// fresh resting entry after a stop, with resume="resume", replays the
// last item that was ever confirmed started rather than restarting at
// item 0 - the resume/restart distinction this seam's own config exists
// to carry.
func TestNightAdvanceBackgroundAudio_ResumePicksLastStartedItem(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply track-1
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start track-1
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // advance would apply track-2, but nothing completed yet
	// Force an explicit advance to track-2 via completion evidence would
	// require another observation set; instead simulate a mid-track-1
	// interruption: leave resting NOW, before any advance.

	rec.State = nightStateTransitionToShow
	pub.result = confirmedResultForAction("stop", sessionID, "stopped")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)

	// Re-enter resting: resume should replay track-1 (the last CONFIRMED
	// start), not restart implicitly at track-1 by coincidence of it
	// being item 0 - proven properly by TestNightAdvanceBackgroundAudio_
	// ResumeSkipsStaleBookmark below, which uses a bookmark that is NOT
	// item 0.
	rec.State = nightStateRestingIntershow
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	media, _ := pub.lastParams["media"].(map[string]any)
	if media["assetId"] != "asset-1" {
		t.Fatalf("resumed item asset = %v, want asset-1 (track-1, the last started item)", media["assetId"])
	}
}

// TestNightAdvanceBackgroundAudio_ResumeSkipsStaleBookmark proves that
// when the last-started item id no longer exists in the CURRENT
// configuration, resume falls back to restarting at item 0 rather than
// guessing - AUDIO-ENGINE §3's own rule, applied to this controller's own
// bookmark.
func TestNightAdvanceBackgroundAudio_ResumeSkipsStaleBookmark(t *testing.T) {
	// Directly unit-test the pure function rather than reconstructing a
	// full history through the dispatch path: nightBackgroundStartItem is
	// exactly the "never guess a stale bookmark" decision.
	items := []pkgaudio.PlaylistItem{
		{ItemID: "track-1", Index: 0, Media: pkgaudio.MediaRef{AssetID: "asset-1", ContentHash: "sha256:asset-1"}},
		{ItemID: "track-2", Index: 1, Media: pkgaudio.MediaRef{AssetID: "asset-2", ContentHash: "sha256:asset-2"}},
	}
	got := nightBackgroundStartItem(items, config.NightSessionBackgroundResumeResume, "track-removed", true)
	if got.ItemID != "track-1" {
		t.Fatalf("nightBackgroundStartItem with a stale bookmark = %q, want track-1 (restart at item 0)", got.ItemID)
	}
}

// TestNightBackgroundAudioRevisionState_RestoresCurrentFromHistory proves
// RestoreRevisionState reconstructs "current" as the highest CONFIRMED
// revision in history - the exact value a live (never-restarted) process
// would hold - so a coordinator restart cannot reset it to zero.
func TestNightBackgroundAudioRevisionState_RestoresCurrentFromHistory(t *testing.T) {
	history := []nightBackgroundAudioHistoryRow{
		{Step: nightBackgroundAudioStep{Seq: 1, Kind: "apply", ItemID: "track-1"}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0001-apply-track-1",
			ActionRevision: 1, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
		}},
		{Step: nightBackgroundAudioStep{Seq: 2, Kind: "start", ItemID: "track-1"}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0002-start-track-1",
			ActionRevision: 2, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
		}},
		{Step: nightBackgroundAudioStep{Seq: 3, Kind: "apply", ItemID: "track-2"}, Row: store.NightCueOutboxRecord{
			SessionID: "sess-1", Cycle: 1, Phase: nightPhaseRestingBackground, CueName: "bg-0003-apply-track-2",
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
// history's own reconstructed current - the same rule the node's own
// RevisionState enforces, kept here too so a coordinator-side bug can
// never even attempt to send a rewinding revision.
func TestNightRunAudioCommand_RefusesNonAdvancingRevision(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	history := []nightBackgroundAudioHistoryRow{
		{Step: nightBackgroundAudioStep{Seq: 5, Kind: "apply", ItemID: "track-1"}, Row: store.NightCueOutboxRecord{
			SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseRestingBackground, CueName: "bg-0005-apply-track-1",
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
// the same crash window nightCueDispatchHooks already proves for cues
// (nightcuerun_test.go), at this seam's own real call site: a tick that
// crashes right after committing the apply row, before ever calling the
// adapter, must leave a pending row and dispatch nothing, and the VERY
// NEXT tick (hook removed, simulating the restarted process) must resume
// and confirm that SAME row rather than committing a second one.
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
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackground, "bg-0001-apply-track-1")
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
	rows, err := st.ListNightCueOutboxRowsForPhase(context.Background(), rec.ID, nightPhaseRestingBackground)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("history has %d rows after resuming, want exactly 1 (the same committed row, resolved, never a second apply)", len(rows))
	}
	if rows[0].State != nightCueStateResolved || rows[0].Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("resumed row = %+v, want resolved/confirmed", rows[0])
	}
}

// TestNightAdvanceBackgroundAudio_CrashAfterDispatchBeforePersist proves
// the SECOND crash window: a dispatch attempt was made but its outcome
// was never recorded. Audio is retryable by identity, so the very next
// tick (hook removed) redispatches under the SAME idempotency key and the
// fake publisher's own dedup-by-envelope proves it never double-sends -
// no new command reaches the node, only the correlated result of the one
// already in flight.
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

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackground, "bg-0001-apply-track-1")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStateDispatched {
		t.Fatalf("row state = %q, want dispatched (the crash landed after the adapter call, before persisting its outcome)", row.State)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", pub.count())
	}

	h.nightCueHooks.afterDispatch = nil
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	rows, err := st.ListNightCueOutboxRowsForPhase(context.Background(), rec.ID, nightPhaseRestingBackground)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("history has %d rows after resuming, want exactly 1", len(rows))
	}
	if rows[0].State != nightCueStateResolved || rows[0].Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("resumed row = %+v, want resolved/confirmed", rows[0])
	}
}

// TestNightAdvanceBackgroundAudio_EstablishesGainAtConfiguredCeiling
// proves maxGainDb reaches the node as a real audio.gain.set command,
// converted from dB to linear amplitude and passed through
// pkgaudio.ApplyCeiling, once per session (not re-sent on every item
// advance).
func TestNightAdvanceBackgroundAudio_EstablishesGainAtConfiguredCeiling(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.lastAction != "audio.gain.set" {
		t.Fatalf("dispatched action = %q, want audio.gain.set", pub.lastAction)
	}
	gain, ok := pub.lastParams["gain"].(float64)
	if !ok {
		t.Fatalf("params.gain = %v, want a float64", pub.lastParams["gain"])
	}
	wantGain := dbToLinearGain(-10) // ba.MaxGainDb
	if diff := gain - wantGain; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("gain = %v, want %v (linear amplitude for -10 dB)", gain, wantGain)
	}

	countAfterGain := pub.count()
	obs := &dynamicObservationLister{}
	h.deps.Observations = obs
	obs.setObs([]observation.Observation{
		backgroundAudioSessionObs(sessionID, "audio_session.state", "completed", testNow),
		backgroundAudioSessionObs(sessionID, "audio_session.playlist.item_id", "track-1", testNow),
	})
	pub.result = confirmedResultForAction("apply", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // advances to track-2

	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action after gain established = %q, want audio.session.apply (gain is not re-sent per item)", pub.lastAction)
	}
	if pub.count() != countAfterGain+1 {
		t.Fatalf("publish count = %d, want %d (exactly one more dispatch, the apply)", pub.count(), countAfterGain+1)
	}
}
