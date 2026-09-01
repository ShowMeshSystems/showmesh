package api

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F5: resting.backgroundAudio's own continuous session
// lifecycle (nightbackgroundaudio.go), distinct from the cue dispatch path
// nightcue_audio_test.go already proves.

// TestNightBackgroundAudioSessionID_SatisfiesAudioSessionIDPattern guards
// against this class of defect recurring: nightBackgroundAudioSessionID
// once minted "night-bg:" + rec.ID, and the colon does not match
// audioSessionIDPattern (audiodispatch.go), the same pattern every
// operator surface (showmeshctl, the API, the Operator UI) enforces
// against sessionId - so a night session's own bed became a session no
// operator could ever address again. Asserted against
// audioSessionIDPattern ITSELF, never a copied regex, so the two can
// never drift apart.
func TestNightBackgroundAudioSessionID_SatisfiesAudioSessionIDPattern(t *testing.T) {
	rec := store.NightSessionRecord{ID: "8047b0c8-9c1e-4b1a-8a3f-example-uuid"}
	sessionID := nightBackgroundAudioSessionID(rec)
	if !audioSessionIDPattern.MatchString(sessionID) {
		t.Fatalf("nightBackgroundAudioSessionID(%q) = %q, which does not match audioSessionIDPattern %s; no operator surface could ever address this session", rec.ID, sessionID, audioSessionIDPattern.String())
	}
}

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
		ResolumeActions: &fakeResolumeActionDispatcher{}, Nodes: &fakeNodeLister{}, Audio: &fakeNodeAudioLister{},
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

// twoNodeBackgroundAudioConfig builds a resting.backgroundAudio config
// whose two items name DIFFERENT target nodes - the list-of-targets
// shape, one item per node.
func twoNodeBackgroundAudioConfig(nodeA, nodeB string) *config.NightSessionBackgroundAudio {
	return &config.NightSessionBackgroundAudio{
		Items: []config.NightSessionBackgroundAudioItem{
			{ItemID: "track-a", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-a", Target: nodeA}},
			{ItemID: "track-b", Asset: config.NightSessionAssetRef{Show: "halloween", Sequence: "bg-b", Target: nodeB}},
		},
		Repeat: config.NightSessionBackgroundRepeatPlaylist, Resume: config.NightSessionBackgroundResumeRestart,
		ItemTransition: config.NightSessionItemTransitionSequential, MaxGainDb: -10,
	}
}

// TestNightAdvanceBackgroundAudio_TwoNodesIndependentProgress is the
// acceptance proof at unit level: two nodes both play the bed
// (OutputNodeIDs derives both from the items' own distinct targets), and
// a refused step on one node is reported against that node while the
// other's own progress is completely unaffected - never blocked,
// corrupted, or silently retried on the other's behalf.
func TestNightAdvanceBackgroundAudio_TwoNodesIndependentProgress(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-a", "node-a", "asset-a")
	putBackgroundAudioAsset(t, st, "halloween", "bg-b", "node-b", "asset-b")
	ba := twoNodeBackgroundAudioConfig("node-a", "node-b")
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.resultsByNode = map[string]mqttproto.ResultPayload{
		"node-a": confirmedResultForAction("apply", sessionID, "position"),
		"node-b": {
			Outcome: mqttproto.OutcomeRefused, Reason: "node-b refuses this apply",
			Evidence: &mqttproto.ResultEvidence{Value: map[string]any{"outcome": "refused", "reason": "node-b refuses this apply"}},
		},
	}

	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	nodeASteps := nightBackgroundAudioStepsForNode(history, "node-a")
	nodeBSteps := nightBackgroundAudioStepsForNode(history, "node-b")
	if len(nodeASteps) != 1 || nodeASteps[0].Row.State != nightCueStateResolved || nodeASteps[0].Row.Outcome != nightCueOutcomeConfirmed {
		t.Fatalf("node-a steps = %+v, want exactly one resolved/confirmed apply", nodeASteps)
	}
	if len(nodeBSteps) != 1 || nodeBSteps[0].Row.State != nightCueStateResolved || nodeBSteps[0].Row.Outcome != nightCueOutcomeRefused {
		t.Fatalf("node-b steps = %+v, want exactly one resolved/refused apply", nodeBSteps)
	}

	// A second tick: node-a advances to gain (its own apply confirmed).
	// node-b's refused apply is left for an operator, never auto-retried
	// (nightAdvanceBackgroundAudioForNode's "apply did not confirm; not
	// auto-retrying" rule) - and, crucially, dealing with node-b never
	// touches node-a's own already-advancing state.
	before := pub.count()
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	if pub.lastAction != "audio.gain.set" {
		t.Fatalf("second tick's last dispatched action = %q, want audio.gain.set (node-a's own next step)", pub.lastAction)
	}
	if got := pub.count() - before; got != 1 {
		t.Fatalf("second tick dispatched %d command(s), want exactly 1 (node-a's own gain; node-b's refused apply must not be retried)", got)
	}

	history, err = h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	nodeASteps = nightBackgroundAudioStepsForNode(history, "node-a")
	nodeBSteps = nightBackgroundAudioStepsForNode(history, "node-b")
	if len(nodeASteps) != 2 || nodeASteps[1].Step.Kind != nightBGStepGain {
		t.Fatalf("node-a steps after its second tick = %+v, want apply then gain", nodeASteps)
	}
	if len(nodeBSteps) != 1 {
		t.Fatalf("node-b steps after node-a's own second tick = %+v, want still exactly 1 (unaffected by node-a's own progress)", nodeBSteps)
	}

	// GET /night/session's own wire mapping: every node reports through
	// backgroundAudio.steps[] with its own nodeId (owner ruling: uniform
	// reporting, no first-node exception).
	wire := mapNightBackgroundAudio(context.Background(), h.deps, rec, true)
	nodeIDs := map[string]int{}
	for _, step := range wire.Steps {
		if step.Sequence != v1.NightAudioSequenceBackground {
			continue
		}
		nodeIDs[step.NodeID]++
	}
	if nodeIDs["node-a"] != 2 {
		t.Fatalf("wire steps tagged node-a = %d, want 2 (apply, gain)", nodeIDs["node-a"])
	}
	if nodeIDs["node-b"] != 1 {
		t.Fatalf("wire steps tagged node-b = %d, want 1 (its own refused apply, isolated from node-a's)", nodeIDs["node-b"])
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

	pub.result = confirmedResultForAction("apply", sessionID, "position")
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

	pub.result = confirmedResultForAction("apply", sessionID, "position")
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
	pub.result = confirmedResultForAction("apply", sessionID, "position")
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

	pub.result = confirmedResultForAction("apply", nightBackgroundAudioSessionID(rec), "position")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action after re-entering resting following a stop = %q, want audio.session.apply", pub.lastAction)
	}
	if _, hasPlaylist := pub.lastParams["playlist"]; !hasPlaylist {
		t.Fatalf("re-apply params carried no playlist: %v", pub.lastParams)
	}
}

// twoItemBackgroundAudioConfigWithFade is [twoItemBackgroundAudioConfig]
// plus a configured fadeOutMs/fadeInMs pair, for the show-boundary fade
// tests below.
func twoItemBackgroundAudioConfigWithFade(node, repeat, resume, itemTransition string, fadeOutMs, fadeInMs int) *config.NightSessionBackgroundAudio {
	ba := twoItemBackgroundAudioConfig(node, repeat, resume, itemTransition)
	ba.FadeOutMs = &fadeOutMs
	ba.FadeInMs = &fadeInMs
	return ba
}

// fadeStateObservation builds an audio_session.fade.state observation for
// sessionID, exactly as internal/coordinator/collector/nodeaudio's own
// collector reports it (this package must never import that collector
// package - see nightBackgroundAudioFadeSettled's own doc comment - so
// this is hand-built here, mirroring nightaudioreadiness_test.go's own
// nodeAudioEngineStateObservation one file over).
func fadeStateObservation(sessionID, value string, observedAt time.Time) observation.Observation {
	return observation.Observation{
		Resource:   observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sessionID},
		Signal:     "audio_session.fade.state",
		Value:      value,
		ObservedAt: &observedAt,
	}
}

// TestNightStopBackgroundAudioIfRunning_FadesDownBeforePausing proves the
// trap this lane exists to close: dispatching audio.gain.fade and
// immediately dispatching audio.session.pause still cuts the audio dead,
// because a fade's own dispatch outcome confirms the instant the ramp is
// accepted, never when it finishes. This asserts the ORDER (fadedown
// dispatched first, pause withheld while the node still reports the fade
// in_progress) and the AWAITED completion (pause dispatched only once
// fade state reads something other than in_progress) - never only "a
// fade command was sent", which a broken implementation could still
// pass.
func TestNightStopBackgroundAudioIfRunning_FadesDownBeforePausing(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.lastAction != "audio.gain.fade" {
		t.Fatalf("dispatched action = %q, want audio.gain.fade (fadedown before any pause/stop)", pub.lastAction)
	}
	if got := pub.lastParams["targetGain"]; got != 0.0 {
		t.Fatalf("fadedown targetGain = %v, want 0.0 (silence)", got)
	}
	if got := pub.lastParams["durationMs"]; got != 200.0 {
		t.Fatalf("fadedown durationMs = %v, want 200 (the configured fadeOutMs)", got)
	}
	countAfterFadeDispatch := pub.count()

	audio := h.deps.Audio.(*fakeNodeAudioLister)
	audio.setObservations("node-a", []observation.Observation{fadeStateObservation(sessionID, "in_progress", testNow)})
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.count() != countAfterFadeDispatch {
		t.Fatalf("publish count while fade state is still in_progress = %d, want unchanged at %d (pause must never race the ramp)", pub.count(), countAfterFadeDispatch)
	}

	audio.setObservations("node-a", []observation.Observation{fadeStateObservation(sessionID, "none", testNow)})
	pub.result = confirmedResultForAction("pause", sessionID, "started")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.count() != countAfterFadeDispatch+1 {
		t.Fatalf("publish count once fade state reads settled = %d, want %d (exactly one more dispatch)", pub.count(), countAfterFadeDispatch+1)
	}
	if pub.lastAction != "audio.session.pause" {
		t.Fatalf("dispatched action once the fade settled = %q, want audio.session.pause", pub.lastAction)
	}
}

// TestNightStopBackgroundAudioIfRunning_NoObservationYetWithholdsPause
// proves the conservative default: a fade dispatched but never yet
// reported on by the node's own telemetry (no observation at all, not
// merely a stale one) is treated as NOT settled, never as "no news is
// good news".
func TestNightStopBackgroundAudioIfRunning_NoObservationYetWithholdsPause(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec) // dispatches fadedown
	countAfterFadeDispatch := pub.count()

	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec) // no observation reported at all yet
	if pub.count() != countAfterFadeDispatch {
		t.Fatalf("publish count with no fade-state observation reported = %d, want unchanged at %d", pub.count(), countAfterFadeDispatch)
	}
}

// TestNightStopBackgroundAudioIfRunning_FadeOutMsUnconfiguredCutsImmediately
// proves the compatibility requirement directly at this controller's own
// exit path: with no fade configured, pause is dispatched immediately,
// exactly as it was before fadeOutMs/fadeInMs existed - no fadedown step
// appears on the wire at all.
func TestNightStopBackgroundAudioIfRunning_FadeOutMsUnconfiguredCutsImmediately(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	pub.result = confirmedResultForAction("pause", nightBackgroundAudioSessionID(rec), "started")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec)
	if pub.lastAction != "audio.session.pause" {
		t.Fatalf("dispatched action = %q, want audio.session.pause (no fadedown when fadeOutMs is not configured)", pub.lastAction)
	}
}

// TestNightAdvanceBackgroundAudio_FadesUpAfterResume proves the UP half of
// the pair: resume lands first (silent, at whatever gain the prior
// fadedown left it at), and only after resume confirms does this
// controller dispatch the fadeup toward maxGainDb - never the reverse
// order, which would ramp a still-paused session.
func TestNightAdvanceBackgroundAudio_FadesUpAfterResume(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeResume, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)
	playThroughApplyGainStart(t, h, pub, rec)

	audio := h.deps.Audio.(*fakeNodeAudioLister)
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec) // fadedown
	audio.setObservations("node-a", []observation.Observation{fadeStateObservation(sessionID, "none", testNow)})
	pub.result = confirmedResultForAction("pause", sessionID, "started")
	h.nightStopBackgroundAudioIfRunning(context.Background(), testNow, rec) // pause, now that the fade settled

	pub.result = confirmedResultForAction("resume", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	if pub.lastAction != "audio.session.resume" {
		t.Fatalf("dispatched action = %q, want audio.session.resume (resume before any fadeup)", pub.lastAction)
	}
	countAfterResume := pub.count()

	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)
	if pub.count() != countAfterResume+1 {
		t.Fatalf("publish count after resume confirmed = %d, want %d (exactly one more dispatch)", pub.count(), countAfterResume+1)
	}
	if pub.lastAction != "audio.gain.fade" {
		t.Fatalf("dispatched action after resume confirmed = %q, want audio.gain.fade", pub.lastAction)
	}
	if got := pub.lastParams["durationMs"]; got != 800.0 {
		t.Fatalf("fadeup durationMs = %v, want 800 (the configured fadeInMs)", got)
	}
	if got, ok := pub.lastParams["targetGain"].(float64); !ok || got != 0.31622776601683794 {
		t.Fatalf("fadeup targetGain = %v, want 0.31622776601683794 (the linear amplitude for -10 dB)", pub.lastParams["targetGain"])
	}
}

// TestNightAdvanceBackgroundAudio_GainBeforeStartIsSilentWhenFadeInConfigured
// proves the entry-side half of the compatibility split: with fadeInMs
// configured, the gain step sent before start targets silence (never the
// configured maxGainDb directly), so the bed is never audible before its
// own fadeup ramps it - the same "never audible before the intended
// level" rule TestNightAdvanceBackgroundAudio_GainBeforeStart already
// proves for the no-fade case, at the opposite gain.
func TestNightAdvanceBackgroundAudio_GainBeforeStartIsSilentWhenFadeInConfigured(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfigWithFade("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential, 200, 800)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	sessionID := nightBackgroundAudioSessionID(rec)

	pub.result = confirmedResultForAction("apply", sessionID, "position")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // apply
	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // gain

	if pub.lastAction != "audio.gain.set" {
		t.Fatalf("dispatched action = %q, want audio.gain.set", pub.lastAction)
	}
	if got := pub.lastParams["gain"]; got != 0.0 {
		t.Fatalf("gain before start = %v, want 0.0 (silence; fadeup ramps it up only after start confirms)", got)
	}

	pub.result = confirmedResultForAction("start", sessionID, "started")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // start
	if pub.lastAction != "audio.session.start" {
		t.Fatalf("dispatched action = %q, want audio.session.start", pub.lastAction)
	}

	pub.result = confirmedResultForAction("gain", sessionID, "gain")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec) // fadeup, once start confirms
	if pub.lastAction != "audio.gain.fade" {
		t.Fatalf("dispatched action after start confirmed = %q, want audio.gain.fade", pub.lastAction)
	}
	if got, ok := pub.lastParams["targetGain"].(float64); !ok || got != 0.31622776601683794 {
		t.Fatalf("fadeup targetGain = %v, want 0.31622776601683794 (the linear amplitude for -10 dB)", pub.lastParams["targetGain"])
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
	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackgroundNode("node-a"), "bg-0001-apply")
	if err != nil {
		t.Fatalf("GetNightCueOutboxRow: %v", err)
	}
	if row.State != nightCueStatePending {
		t.Fatalf("row state = %q, want pending", row.State)
	}

	h.nightCueHooks.afterCommit = nil
	pub.result = confirmedResultForAction("apply", sessionID, "position")
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
	pub.result = confirmedResultForAction("apply", sessionID, "position")
	h.nightAdvanceBackgroundAudio(context.Background(), testNow, rec)

	row, err := st.GetNightCueOutboxRow(context.Background(), rec.ID, rec.Cycle, nightPhaseRestingBackgroundNode("node-a"), "bg-0001-apply")
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

// TestNightClearBackgroundAudioAtEndSession_ClearsNotStops proves
// end-session's own bed cleanup issues audio.session.clear, never
// stop/pause: a stop or pause would leave the node's persisted session
// record in place for the agent's own RestoreAll to resurrect the bed at
// its next start, which end-session (a promise of no resume, ADR-038)
// must never allow.
func TestNightClearBackgroundAudioAtEndSession_ClearsNotStops(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("clear", sessionID, "stopped")

	rec.State = nightStateStopped
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	h.nightClearBackgroundAudioAtEndSession(context.Background(), testNow, rec)

	if pub.lastAction != "audio.session.clear" {
		t.Fatalf("dispatched action = %q, want audio.session.clear", pub.lastAction)
	}
	if pub.lastParams["sessionId"] != sessionID {
		t.Fatalf("dispatched sessionId = %v, want %q", pub.lastParams["sessionId"], sessionID)
	}
}

// countActionDispatches counts every command pub actually put on the wire
// (never one that failed before publish) whose action is action.
func countActionDispatches(pub *fakeAudioPublisher, action string) int {
	n := 0
	for _, d := range pub.dispatched {
		if d.Action == action {
			n++
		}
	}
	return n
}

// TestNightClearBackgroundAudioAtEndSession_RepeatedCallDoesNotRedispatch
// documents the exact defect this review finding fixed, and is kept
// deliberately as a regression guard rather than discarded once the fix
// landed: end-session's own clear (nightClearBackgroundAudioAtEndSession)
// keeps ONE constant, per-session idempotency key by design (its own doc
// comment). Calling it a SECOND time after a pre-wire failure does NOT
// retry against the node - executeAudioSessionDispatch's own idempotency-
// first replay (audiodispatch.go's InsertCommand duplicate-key path)
// answers the second call with the first attempt's own cached, failed
// outcome instead of dispatching again. That is exactly why nightTick's
// stopped-state retry never calls this function a second time and mints
// its own fresh, per-attempt key instead
// ([nightEndSessionClearIdempotencyKey], nightRetryEndSessionClear). If a
// future change ever gives THIS function a per-attempt key of its own,
// this test starts failing - on purpose, so that change is deliberate,
// not a silent reintroduction of the bug the retry above exists to fix.
func TestNightClearBackgroundAudioAtEndSession_RepeatedCallDoesNotRedispatch(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)

	rec.State = nightStateStopped
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	pub.beforePublishErr = errors.New("dial tcp: no route to host")
	h.nightClearBackgroundAudioAtEndSession(context.Background(), testNow, rec)
	before := countActionDispatches(pub, "audio.session.clear")

	pub.beforePublishErr = nil
	sessionID := nightBackgroundAudioSessionID(rec)
	pub.result = confirmedResultForAction("clear", sessionID, "stopped")
	h.nightClearBackgroundAudioAtEndSession(context.Background(), testNow, rec)

	if got := countActionDispatches(pub, "audio.session.clear"); got != before {
		t.Fatalf("audio.session.clear dispatch count after a repeated call to nightClearBackgroundAudioAtEndSession = %d, want unchanged at %d - if this function now retries under its own key, nightTick's stopped-state retry may have become redundant; that is a deliberate design change to make, not an accident", got, before)
	}
}

// TestNightTick_StoppedRetriesEndSessionClearUntilNodeReturns is this
// review finding's own end-to-end proof: nightTick's stopped-state case
// must RETRY end-session's own clear, not do nothing, when the node was
// unreachable when it first ran. It drives the unreachable case end to
// end - the clear fails, the bed is still resident, ticks while stopped
// retry it, the node returns, a clear lands - and asserts convergence
// specifically: the session record is released AND no further tick
// dispatches anything more, not merely that a retry was attempted.
func TestNightTick_StoppedRetriesEndSessionClearUntilNodeReturns(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)
	sessionID := nightBackgroundAudioSessionID(rec)

	// 1. end-session runs and its own synchronous clear FAILS: the node
	// is unreachable.
	pub.beforePublishErr = errors.New("dial tcp: no route to host")
	h.nightClearBackgroundAudioAtEndSession(context.Background(), testNow, rec)
	if got := countActionDispatches(pub, "audio.session.clear"); got != 0 {
		t.Fatalf("audio.session.clear reached the wire on the failed first attempt, want 0, got %d", got)
	}

	rec.State = nightStateStopped
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	// 2. The bed is still resident: nothing released it. No clear anchor
	// has confirmed.
	cur, ok, err := st.GetCurrentNightSession(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetCurrentNightSession: ok=%v err=%v", ok, err)
	}
	if anchor, has := decodeNightContentAnchor(cur.ContentAnchorJSON); has && anchor.Purpose == nightAnchorPurposeEndSessionClear && !anchor.ObservedAt.IsZero() {
		t.Fatalf("a clear is already confirmed before any retry ran")
	}

	// 3. Ticks occur while the session is stopped and the node is still
	// unreachable: the clear is retried, but nothing lands.
	h.nightTick(context.Background(), testNow)
	h.nightTick(context.Background(), testNow)
	if got := countActionDispatches(pub, "audio.session.clear"); got != 0 {
		t.Fatalf("audio.session.clear reached the wire while the node was still unreachable, want 0, got %d", got)
	}

	// 4. The node returns: the very next tick's retry dispatches the
	// clear again, under a fresh idempotency key, and this time it lands.
	pub.beforePublishErr = nil
	pub.result = confirmedResultForAction("clear", sessionID, "stopped")
	h.nightTick(context.Background(), testNow)

	if pub.lastAction != "audio.session.clear" {
		t.Fatalf("dispatched action = %q, want audio.session.clear", pub.lastAction)
	}
	if pub.lastParams["sessionId"] != sessionID {
		t.Fatalf("dispatched sessionId = %v, want %q", pub.lastParams["sessionId"], sessionID)
	}
	cur, ok, err = st.GetCurrentNightSession(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetCurrentNightSession: ok=%v err=%v", ok, err)
	}
	anchor, has := decodeNightContentAnchor(cur.ContentAnchorJSON)
	if !has || anchor.Purpose != nightAnchorPurposeEndSessionClear || anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor = %+v (has=%v), want a confirmed end-session-clear anchor - the session record was never released", anchor, has)
	}

	// Convergence, not just an attempt: further ticks dispatch NOTHING.
	landedCount := countActionDispatches(pub, "audio.session.clear")
	h.nightTick(context.Background(), testNow)
	h.nightTick(context.Background(), testNow)
	if got := countActionDispatches(pub, "audio.session.clear"); got != landedCount {
		t.Fatalf("audio.session.clear dispatch count after confirming = %d, want unchanged at %d (no further dispatch once released)", got, landedCount)
	}
}

// TestNightTick_StoppedDoesNotRedispatchClearOnceConfirmed proves the
// concern the original review comment raised for the success case: once
// end-session's own clear confirms (whether on its first synchronous
// attempt or a later retry), further ticks while stopped never mint
// another redundant clear.
func TestNightTick_StoppedDoesNotRedispatchClearOnceConfirmed(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	playThroughApplyGainStart(t, h, pub, rec)
	sessionID := nightBackgroundAudioSessionID(rec)

	rec.State = nightStateStopped
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}
	pub.result = confirmedResultForAction("clear", sessionID, "stopped")

	h.nightTick(context.Background(), testNow) // the stopped-state retry dispatches and confirms.
	if got := countActionDispatches(pub, "audio.session.clear"); got != 1 {
		t.Fatalf("audio.session.clear dispatch count after the first stopped tick = %d, want exactly 1", got)
	}

	for i := 0; i < 5; i++ {
		h.nightTick(context.Background(), testNow)
	}
	if got := countActionDispatches(pub, "audio.session.clear"); got != 1 {
		t.Fatalf("audio.session.clear dispatch count after 5 more stopped ticks = %d, want still 1 (no endless redundant traffic)", got)
	}
}

// TestNightTick_PreshowStartsBackgroundAudio proves preshow runs the
// same apply/gain/start path resting-intershow already uses: the bed is
// not withheld until the first inter-show resting period.
func TestNightTick_PreshowStartsBackgroundAudio(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	_ = mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)

	h.nightTick(context.Background(), testNow)

	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("dispatched action = %q, want audio.session.apply", pub.lastAction)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", pub.count())
	}
}

// TestNightTick_PreshowToRestingIntershowDoesNotRestartBackgroundAudio
// proves requirement 2: the bed already applied, gained, and started
// during preshow is left alone at the preshow -> resting-intershow
// boundary. The step history is keyed by the session's own stable
// nightBackgroundAudioSessionID, not by state, so the state change alone
// commits no new apply/start step.
func TestNightTick_PreshowToRestingIntershowDoesNotRestartBackgroundAudio(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
	playThroughApplyGainStart(t, h, pub, rec)
	countAfterStart := pub.count()

	rec.State = nightStateRestingIntershow
	if err := st.UpdateNightSession(context.Background(), rec, testNow); err != nil {
		t.Fatalf("UpdateNightSession: %v", err)
	}

	h.nightTick(context.Background(), testNow)

	if pub.count() != countAfterStart {
		t.Fatalf("publish count = %d after entering resting-intershow, want unchanged at %d (must not re-apply/restart)", pub.count(), countAfterStart)
	}

	history, err := h.nightBackgroundAudioHistory(context.Background(), rec)
	if err != nil {
		t.Fatalf("nightBackgroundAudioHistory: %v", err)
	}
	steps := nightBackgroundAudioSteps(history)
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3 (apply, gain, start only - no duplicate apply/start across the boundary)", len(steps))
	}
	if steps[len(steps)-1].Step.Kind != nightBGStepStart {
		t.Fatalf("latest step = %q, want %q", steps[len(steps)-1].Step.Kind, nightBGStepStart)
	}
}

// TestNightTick_PreshowNoBackgroundAudioConfiguredDoesNothing proves
// requirement 4: a preshow session with no resting.backgroundAudio
// configured behaves unchanged - nightAdvanceBackgroundAudio's existing
// ba == nil early return covers preshow exactly as it already covers the
// resting states.
func TestNightTick_PreshowNoBackgroundAudioConfiguredDoesNothing(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	_ = mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", nil, nightStatePreshow)

	h.nightTick(context.Background(), testNow)

	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (no background audio configured)", pub.count())
	}
}

// TestNightTick_LeavingPreshowForShowStopsBackgroundAudio proves
// requirement 3: leaving preshow for a show still stops the bed exactly
// as leaving resting-intershow does, and transition-to-show still takes
// no action of its own (the deliberate no-op this switch's own comment
// documents).
func TestNightTick_LeavingPreshowForShowStopsBackgroundAudio(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStatePreshow)
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

// mutation target: nightBackgroundAudioHistory keeping rows it cannot
// parse, and nightNextBackgroundAudioRevision counting them. Drop an
// unrecognized row at the store read and this fails: the next revision
// falls back below one the node has already accepted, and from that
// point every background command is refused as stale for the rest of the
// night with nothing to self-heal it.
//
// Only reachable upgrading over a database an earlier build wrote, when
// that build recorded background-audio steps under phases this one no
// longer uses. That is a development coordinator in practice, but the
// counter must not be able to go backwards for any reason.
func TestNightBackgroundAudioRevisionNeverRewindsPastAnUnrecognizedRow(t *testing.T) {
	h, st, pub, _ := nightBackgroundAudioTestHandlers(t)
	putBackgroundAudioAsset(t, st, "halloween", "bg-1", "node-a", "asset-1")
	putBackgroundAudioAsset(t, st, "halloween", "bg-2", "node-a", "asset-2")
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)
	ctx := context.Background()

	// A row an earlier build wrote under a phase this one no longer
	// recognizes, at a revision the node's own RevisionState has already
	// advanced to.
	if err := st.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: "row-legacy", SessionID: rec.ID, Cycle: rec.Cycle,
		Phase: nightPhaseRestingBackground + ":announcementDuck:enterResting", CueName: "thank-you",
		ActionRevision: 99, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
	}, testNow); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	history, err := h.nightBackgroundAudioHistory(ctx, rec)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if got := nightNextBackgroundAudioRevision(history); got <= 99 {
		t.Fatalf("next revision = %d, want it above the legacy row's own 99", got)
	}
	// And the unrecognized row is still not a step: the state machine must
	// see an empty history and start from apply, not treat it as one.
	if steps := nightBackgroundAudioSteps(history); len(steps) != 0 {
		t.Fatalf("nightBackgroundAudioSteps returned %d step(s) for a history of one unrecognized row", len(steps))
	}

	pub.result = confirmedResultForAction("apply", nightBackgroundAudioSessionID(rec), "position")
	h.nightAdvanceBackgroundAudio(ctx, testNow, rec)
	if pub.lastAction != "audio.session.apply" {
		t.Fatalf("first dispatch after a legacy-only history = %q, want audio.session.apply", pub.lastAction)
	}
	applyRow := pub.dispatched[len(pub.dispatched)-1]
	if applyRow.Params["revision"].(float64) <= 99 {
		t.Fatalf("apply dispatched at revision %v, want above the legacy row's 99", applyRow.Params["revision"])
	}
}

// TestMapNightBackgroundAudio_PinnedMaxGainDb proves the owner ruling
// (2026-08-28) directly: the GET /night/session projection reports the
// maxGainDb the RUNNING session pinned at its own configRevision, and
// keeps reporting that value unchanged after a later revision
// reconfigures the ceiling - never the value night.session.resting's
// config currently holds.
func TestMapNightBackgroundAudio_PinnedMaxGainDb(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	got := mapNightBackgroundAudio(context.Background(), h.deps, rec, true)
	if got.PinnedMaxGainDb == nil || *got.PinnedMaxGainDb != -10 {
		t.Fatalf("PinnedMaxGainDb = %v, want -10", got.PinnedMaxGainDb)
	}

	// A later revision reconfigures the ceiling. The running session stays
	// pinned to revision 1, so the response must not move.
	reconfigured := *ba
	reconfigured.MaxGainDb = -3
	payload := config.NightSessionPayload{
		Show: "halloween", Label: "Halloween main loop",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "halloween-show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "halloween-resting",
			TimelineAsset:   config.NightSessionAssetRef{Show: "halloween", Sequence: "resting-loop", Target: "fpp-main"},
			BackgroundAudio: &reconfigured,
		},
		EnterShow:                 config.NightSessionEnterShow{Cues: nil, BlackoutHoldMs: 0},
		EnterResting:              config.NightSessionEnterResting{Cues: nil, BlackoutAfterShowMs: 0},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	}
	raw, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		t.Fatalf("encode revision 2 payload: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.NightSessionConfigKind, ObjectID: "halloween-main", Revision: 2, PayloadJSON: raw,
		CreatedByPrincipalID: "test", CreatedByPrincipalName: "test", Source: "api",
	}); err != nil {
		t.Fatalf("create night.session revision 2: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.NightSessionConfigKind, "halloween-main", 2); err != nil {
		t.Fatalf("activate night.session revision 2: %v", err)
	}

	got = mapNightBackgroundAudio(ctx, h.deps, rec, true)
	if got.PinnedMaxGainDb == nil || *got.PinnedMaxGainDb != -10 {
		t.Fatalf("PinnedMaxGainDb after reconfiguration = %v, want still -10 (the pinned revision), not the newly configured -3", got.PinnedMaxGainDb)
	}

	// No session running: the field is absent, never a fallback value.
	if none := mapNightBackgroundAudio(ctx, h.deps, store.NightSessionRecord{}, true); none.PinnedMaxGainDb != nil {
		t.Fatalf("PinnedMaxGainDb with no session = %v, want nil", none.PinnedMaxGainDb)
	}
}

// TestMapNightBackgroundAudio_NoBackgroundAudioConfigured proves a session
// pinned to a revision that configures no backgroundAudio at all reports
// State "recorded" (a legitimate reading, not a read failure) with
// PinnedMaxGainDb nil rather than a defaulted or fallback value, and a
// non-empty Reason naming why (found by review: an earlier version left
// Reason empty here, breaking the OpenAPI description's own promise that
// state and reason say why the field is absent).
func TestMapNightBackgroundAudio_NoBackgroundAudioConfigured(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", nil, nightStateRestingIntershow)

	got := mapNightBackgroundAudio(context.Background(), h.deps, rec, true)
	if got.State != v1.NightEvidenceRecorded {
		t.Fatalf("state with no backgroundAudio configured = %q, want recorded", got.State)
	}
	if got.PinnedMaxGainDb != nil {
		t.Fatalf("PinnedMaxGainDb with no backgroundAudio configured = %v, want nil", got.PinnedMaxGainDb)
	}
	if got.Reason == "" {
		t.Fatalf("Reason with no backgroundAudio configured = %q, want a non-empty explanation", got.Reason)
	}
}

// TestMapNightBackgroundAudio_NotPopulatedOutsideRunningStates proves the
// owner-ruled gate directly, on the CURRENT-session side only
// (current=true, GET /night/session and its siblings): pinnedMaxGainDb is
// nil for a session record that exists but is not in a running state
// (preparing, or stopped), even though its pinned revision configures a
// real ceiling - so that endpoint never reports the last night's ceiling
// as live before the session has started or after it has ended.
func TestMapNightBackgroundAudio_NotPopulatedOutsideRunningStates(t *testing.T) {
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	for _, state := range []string{nightStatePreparing, nightStateStopped} {
		t.Run(state, func(t *testing.T) {
			h, st, _, _ := nightBackgroundAudioTestHandlers(t)
			rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, state)
			got := mapNightBackgroundAudio(context.Background(), h.deps, rec, true)
			if got.State != v1.NightEvidenceRecorded {
				t.Fatalf("state %q: reported state = %q, want recorded", state, got.State)
			}
			if got.PinnedMaxGainDb != nil {
				t.Fatalf("state %q: PinnedMaxGainDb = %v, want nil (not a running state, current=true)", state, got.PinnedMaxGainDb)
			}
		})
	}
}

// TestMapNightBackgroundAudio_ByIDIgnoresRunningState proves the owner
// ruling's other half (2026-08-30), on the BY-ID side (current=false, GET
// /night/sessions/{id}): a stopped session's own pinned revision still
// reports its pinnedMaxGainDb, because the value there is already scoped
// to the specific historical record the caller asked for, never a live
// claim. Paired with TestMapNightBackgroundAudio_NotPopulatedOutsideRunningStates,
// which proves the opposite for current=true on the same states.
func TestMapNightBackgroundAudio_ByIDIgnoresRunningState(t *testing.T) {
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)

	for _, state := range []string{nightStatePreparing, nightStateStopped} {
		t.Run(state, func(t *testing.T) {
			h, st, _, _ := nightBackgroundAudioTestHandlers(t)
			rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, state)
			got := mapNightBackgroundAudio(context.Background(), h.deps, rec, false)
			if got.State != v1.NightEvidenceRecorded {
				t.Fatalf("state %q: reported state = %q, want recorded", state, got.State)
			}
			if got.PinnedMaxGainDb == nil || *got.PinnedMaxGainDb != -10 {
				t.Fatalf("state %q: PinnedMaxGainDb = %v, want -10 (by-id reports the record's own pinned ceiling regardless of state)", state, got.PinnedMaxGainDb)
			}
		})
	}
}

// TestMapNightBackgroundAudio_PinnedGainReadFailureKeepsStepsRecorded
// proves finding 1 directly: a failure reading the pinned night.session
// revision for pinnedMaxGainDb must never hide the step log an operator
// needs to see a stuck or refused announcement clear. State stays
// "recorded", the steps already read are still reported, and only
// PinnedMaxGainDb is nil, with its own Reason - never the whole block
// flipped to Unknown with the steps dropped (the bug this test guards
// against: an earlier version did exactly that for a read failure on
// this strictly additive field).
func TestMapNightBackgroundAudio_PinnedGainReadFailureKeepsStepsRecorded(t *testing.T) {
	h, st, _, _ := nightBackgroundAudioTestHandlers(t)
	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateRestingIntershow)

	// A real step, so there is genuine evidence to lose if this regresses.
	ctx := context.Background()
	if err := st.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: "row-apply", SessionID: rec.ID, Cycle: rec.Cycle,
		Phase: nightPhaseRestingBackgroundNode("node-a"), CueName: nightBackgroundAudioCueNameApply(1),
		ActionRevision: 1, State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed,
	}, testNow); err != nil {
		t.Fatalf("insert apply row: %v", err)
	}

	// Point the session at a config revision that does not exist, so the
	// pinned-config read fails while the outbox reads above are
	// untouched: a real "config read fails, evidence already read is
	// still reported" case, not a simulated error.
	rec.ConfigRevision = 99

	got := mapNightBackgroundAudio(ctx, h.deps, rec, true)
	if got.State != v1.NightEvidenceRecorded {
		t.Fatalf("state = %q, want recorded (a pinnedMaxGainDb read failure must not hide the step log)", got.State)
	}
	if got.PinnedMaxGainDb != nil {
		t.Fatalf("PinnedMaxGainDb = %v, want nil on a pinned-config read failure", got.PinnedMaxGainDb)
	}
	if got.Reason == "" {
		t.Fatalf("Reason = %q, want a non-empty explanation of the pinnedMaxGainDb read failure", got.Reason)
	}
	if len(got.Steps) != 1 || got.Steps[0].CueName != nightBackgroundAudioCueNameApply(1) {
		t.Fatalf("Steps = %+v, want the one apply step still reported despite the pinned-config read failure", got.Steps)
	}
}

// TestNightPinnedBackgroundMaxGainDb_EmptyConfigObjectID proves finding
// 2 directly: an empty rec.ConfigObjectID (mapNightCues already guards
// this; this call site did not) is answered as "nothing pinned yet" -
// nil gain, no reason, no error - never a doomed GetConfigRevision call
// against an empty object id.
func TestNightPinnedBackgroundMaxGainDb_EmptyConfigObjectID(t *testing.T) {
	h, _, _, _ := nightBackgroundAudioTestHandlers(t)
	gain, reason, err := nightPinnedBackgroundMaxGainDb(context.Background(), h.deps, store.NightSessionRecord{ID: "sess-1", State: nightStateRestingIntershow})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if gain != nil {
		t.Fatalf("gain = %v, want nil", gain)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
}

// nightPinnedMaxGainDbFromResponse decodes just the one field this test
// package cares about out of a real NightSessionResponse body.
func nightPinnedMaxGainDbFromResponse(t *testing.T, body []byte) (id string, pinnedMaxGainDb *float64) {
	t.Helper()
	var decoded struct {
		Session struct {
			ID              string `json:"id"`
			BackgroundAudio struct {
				PinnedMaxGainDb *float64 `json:"pinnedMaxGainDb"`
			} `json:"backgroundAudio"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode NightSessionResponse: %v; body=%s", err, body)
	}
	return decoded.Session.ID, decoded.Session.BackgroundAudio.PinnedMaxGainDb
}

// TestNightSessionEndpoints_PinnedMaxGainDbDiffersByEndpoint exercises
// handleGetNightLifecycle and handleGetNightLifecycleByID directly, not
// just the mapper, so a wiring mistake in either handler's own `current`
// argument fails here even if every mapper-level test still passes. A
// stopped session stays the store's "current" row, exactly the case the
// owner ruling (2026-08-30) is about.
func TestNightSessionEndpoints_PinnedMaxGainDbDiffersByEndpoint(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(fixedClock(testNow)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, fixedClock(testNow), filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	deps := Dependencies{
		NightSessions: st, Config: st, Commands: st, Identity: svc,
		AudioPublisher: &fakeAudioPublisher{}, AudioSessions: st, Assets: st, Observations: &dynamicObservationLister{},
		ResolumeActions: &fakeResolumeActionDispatcher{},
	}.withDefaults()
	nightAPI := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	ba := twoItemBackgroundAudioConfig("node-a", config.NightSessionBackgroundRepeatPlaylist, config.NightSessionBackgroundResumeRestart, config.NightSessionItemTransitionSequential)
	rec := mustCreateRestingSessionWithBackgroundAudio(t, st, "sess-1", "node-a", ba, nightStateStopped)

	_, byIDBody := doRequest(t, nightAPI.Handler, "GET", "/api/v1/night/sessions/"+rec.ID, nil)
	byIDID, byIDGain := nightPinnedMaxGainDbFromResponse(t, byIDBody)
	if byIDID != rec.ID {
		t.Fatalf("GET /night/sessions/{id}: id = %q, want %q; body=%s", byIDID, rec.ID, byIDBody)
	}
	if byIDGain == nil || *byIDGain != -10 {
		t.Fatalf("GET /night/sessions/{id} on a stopped session: pinnedMaxGainDb = %v, want -10 (the record's own pinned ceiling, regardless of state)", byIDGain)
	}

	_, curBody := doRequest(t, nightAPI.Handler, "GET", "/api/v1/night/session", nil)
	curID, curGain := nightPinnedMaxGainDbFromResponse(t, curBody)
	if curID != rec.ID {
		t.Fatalf("GET /night/session did not return the stopped session as current: id = %q, want %q; body=%s", curID, rec.ID, curBody)
	}
	if curGain != nil {
		t.Fatalf("GET /night/session on a stopped session: pinnedMaxGainDb = %v, want nil (never a dead session's ceiling reported as live)", curGain)
	}
}
