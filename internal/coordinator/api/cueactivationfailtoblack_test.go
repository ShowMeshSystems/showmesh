package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueauth"
)

// This file is the composed acceptance proof PR #158 admitted it lacked:
// a real [fppreconcile.Reconcile] (via [StoreFPPReconciliation]) into a
// real [cueactivate.Decide] into a real [cueactivate.Authorize] into a
// real dispatch into the fail-to-black effect, exercised through
// [handlers.cueActivationTickOne] exactly as [CueActivationLoop.Run]'s own
// tick calls it — never a hand-built Decision or Activation asserted
// against a unit under test in isolation. The node-wide blast radius and
// the never-blacks-at-all gap were both composition failures: every one
// of decide.go's and
// cueactivationdispatch.go's own unit tests passed while the composed path
// blacked the whole node for an audio-only refusal (problem 1) and never
// blacked anything at all for a node-side refusal (problem 2).

// putShowModeForTest activates showMode ("show" or "program") as
// show.mode's current revision.
func putShowModeForTest(t *testing.T, st *store.Store, mode string) {
	t.Helper()
	payload, err := config.EncodeShowModePayload(config.ShowModePayload{Mode: mode})
	if err != nil {
		t.Fatalf("encode show.mode payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowModeConfigKind, config.ShowModeConfigObjectID, payload)
}

// failToBlackComposedSetup wires one *store.Store as every dependency the
// composed Reconcile->Decide->Authorize->dispatch->fail-to-black path
// touches: FPPReconciliation and FPPObservations for real reconciliation,
// AssetManifests/Config for real catalog and asset resolution, Commands
// for real idempotency, RenderPublisher/AudioPublisher (the fake) for the
// one genuinely external dependency, and Identity for the refusal audit
// trail this composed path also writes.
type failToBlackComposedSetup struct {
	st        *store.Store
	svc       identity.Service
	audioPub  *fakeAudioPublisher
	renderPub *fakeRenderPublisher
	obs       *dynamicObservationLister
}

func newFailToBlackComposedSetup(t *testing.T, now func() time.Time) *failToBlackComposedSetup {
	t.Helper()
	audio := newAudioDispatchTestSetup(t, now)
	return &failToBlackComposedSetup{
		st: audio.st, svc: audio.svc, audioPub: audio.pub,
		renderPub: &fakeRenderPublisher{},
		obs:       &dynamicObservationLister{},
	}
}

func (s *failToBlackComposedSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: s.obs,
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Commands: s.st, Config: s.st,
		AudioPublisher: s.audioPub, AudioSessions: s.st,
		RenderPublisher: s.renderPub, AssetManifests: s.st,
		FPPReconciliation: StoreFPPReconciliation{Store: s.st},
		FPPObservations:   s.st,
	}
}

// failToBlackFixture is [cueActivationDispatchTestFixture]'s own show/
// playlist/node scaffolding, driven one layer earlier: a REAL
// [store.FPPPlaylistEntryObservationRecord] this fixture writes directly
// (mirroring what fppobservations.go's POST handler would have written),
// rather than a hand-built [cueactivation.Activation] a test skips
// Reconcile/Decide to construct.
type failToBlackFixture struct {
	showID, cueID, playlistID, instanceUUID, entryID, nodeID string
}

func putFailToBlackObservation(t *testing.T, st *store.Store, f failToBlackFixture, playlist config.ShowPlaylistPayload, sequence, entryOccurrenceSequence int64, now time.Time) {
	t.Helper()
	entryKey, err := config.DerivePlaylistEntryKey(playlist, f.entryID)
	if err != nil {
		t.Fatalf("derive playlist entry key: %v", err)
	}
	if err := st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: f.instanceUUID, SchemaVersion: 1, Sequence: sequence, Action: "playing",
		PlaylistName: playlist.FPP.PlaylistName, PlaylistHash: playlist.FPP.PlaylistHash,
		Section: "mainPlaylist", Position: 0, EntryKey: entryKey,
		EntryOccurrenceSequence: entryOccurrenceSequence,
		ObservedAt:              now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}
}

// --- Problem 1: an audio-only refusal must not black a render surface ---

// TestCueActivationTickOneAudioOnlyAssetMissingNeverClearsRenderSurface
// proves problem 1's fix through the composed path: node "audio-01" holds
// BOTH a declared audio.node object AND a show.surface (so it renders for
// OTHER cues in this Show) and this Show's one Cue declares audio only.
// When that Cue's own audio asset is genuinely missing, THIS
// coordinator's own pre-dispatch Authorize refuses asset-missing, and the
// resulting fail-to-black must stop only [cueactivation.AudioSessionID]
// (the session THIS Cue's own audio output actually runs in) and must
// dispatch NO render.surface.clear at all and NO
// [cueactivation.BackgroundSessionID]/[cueactivation.AnnouncementSessionID]
// stop — the exact node-wide blast radius the reviewer demonstrated
// before this fix (an audio-only refused cue blacking both of the node's
// render surfaces).
func TestCueActivationTickOneAudioOnlyAssetMissingNeverClearsRenderSurface(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "cue-1", "playlist-1", "inst-1", "entry-1", "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	putAudioNodeForTest(t, setup.st, nodeID)
	renderPutSurface(t, setup.st, "surface-1", showID, nodeID) // same node also renders for other cues.
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putAudioOnlyCueForTest(t, setup.st, cueID, showID)

	playlist := config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: entryID, Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putPlaylistForTest(t, setup.st, playlistID, playlist)
	putActiveShowForTest(t, setup.st, showID)

	// The Cue's own asset is genuinely missing: an AssetRecord exists,
	// targeted at nodeID, but node-1's own reported inventory never holds
	// it (mirrors TestDispatchOneCueActivationAssetMissingNamesTheSequenceAndAsset).
	if _, _, err := setup.st.CreateAsset(context.Background(), store.AssetRecord{
		ID: "sha256:missing-cue-1-node", ShowID: showID, SequenceID: "asset-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: "sha256:missing-cue-1-node", RuntimeFilename: "Missing.wav",
		SizeBytes: 1024, Backend: "volume", StorageKey: "sha256:missing-cue-1-node",
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}

	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil)

	// The fail-to-black dispatch is async (problem 3's own fix) — poll
	// for the audio stop to appear rather than asserting immediately.
	deadline := time.After(2 * time.Second)
	for {
		setup.audioPub.mu.Lock()
		stopped := 0
		for _, d := range setup.audioPub.dispatched {
			if d.Action == "audio.session.stop" {
				stopped++
			}
		}
		setup.audioPub.mu.Unlock()
		if stopped > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no audio.session.stop was ever dispatched within 2s")
		case <-time.After(5 * time.Millisecond):
		}
	}

	setup.audioPub.mu.Lock()
	gotSessions := map[string]bool{}
	for _, d := range setup.audioPub.dispatched {
		if d.Action != "audio.session.stop" {
			continue
		}
		sessionID, _ := d.Params["sessionId"].(string)
		gotSessions[sessionID] = true
	}
	setup.audioPub.mu.Unlock()
	if len(gotSessions) != 1 || !gotSessions[blackAndSilenceAudioSessionID] {
		t.Fatalf("audio.session.stop dispatched to %v, want exactly [%q] (never background/announcement — this Cue declares neither)", gotSessions, blackAndSilenceAudioSessionID)
	}

	if got := setup.renderPub.count(); got != 0 {
		t.Fatalf("render.surface.clear was dispatched %d time(s), want 0: an audio-only Cue's refusal must never touch a render surface (problem 1)", got)
	}
}

// TestCueActivationTickOneAssetMissingNeverBlacksInProgramMode is the
// composed-path regression proof for the owner's own ruling this branch
// must not disturb: in setup/program mode an asset-missing refusal stays
// loud (the existing log line and audit entry) and dispatches NOTHING —
// zero render.surface.clear, zero audio.session.stop — the reviewer's own
// verified "0 clears" baseline for program mode, now proven through the
// full composed path rather than only the pure [assetMissingFailToBlack]
// helper.
func TestCueActivationTickOneAssetMissingNeverBlacksInProgramMode(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "cue-1", "playlist-1", "inst-1", "entry-1", "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeProgram)
	putAudioNodeForTest(t, setup.st, nodeID)
	renderPutSurface(t, setup.st, "surface-1", showID, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putAudioOnlyCueForTest(t, setup.st, cueID, showID)

	playlist := config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: entryID, Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putPlaylistForTest(t, setup.st, playlistID, playlist)
	putActiveShowForTest(t, setup.st, showID)

	if _, _, err := setup.st.CreateAsset(context.Background(), store.AssetRecord{
		ID: "sha256:missing-cue-1-node", ShowID: showID, SequenceID: "asset-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: "sha256:missing-cue-1-node", RuntimeFilename: "Missing.wav",
		SizeBytes: 1024, Backend: "volume", StorageKey: "sha256:missing-cue-1-node",
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}

	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil)

	// The fail-to-black decision is made in its own goroutine (problem
	// 3's own fix); give it a generous window to have run and NOT
	// dispatched anything before asserting the negative.
	time.Sleep(200 * time.Millisecond)

	if got := setup.audioPub.count(); got != 0 {
		t.Fatalf("audio publish count = %d, want 0: setup/program mode must never black (the owner's own ruling)", got)
	}
	if got := setup.renderPub.count(); got != 0 {
		t.Fatalf("render.surface.clear dispatched %d time(s), want 0: setup/program mode must never black (the owner's own ruling)", got)
	}
}

// --- Problem 2: a NODE-side refusal must reach the same scoped path ---

// TestCueActivationTickOneNodeSideAssetMissingReachesScopedFailToBlack
// proves problem 2's fix: this coordinator's own Authorize PASSES (so the
// activation is actually dispatched), but the node's own post-dispatch
// result reports "asset-missing" — the fake publisher standing in for a
// node that, asked to open a file this coordinator itself believes is
// present, cannot. Before this fix, [assetMissingNodeIDs] keyed on
// AuthorizeOutcome alone, so this exact case (the most flat-out-missing
// one Eric named — a sequence the coordinator resolves as present and the
// node resolves as absent) reached no fail-to-black path at all.
func TestCueActivationTickOneNodeSideAssetMissingReachesScopedFailToBlack(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "cue-1", "playlist-1", "inst-1", "entry-1", "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	putAudioNodeForTest(t, setup.st, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putAudioOnlyCueForTest(t, setup.st, cueID, showID)

	playlist := config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: entryID, Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putPlaylistForTest(t, setup.st, playlistID, playlist)
	putActiveShowForTest(t, setup.st, showID)
	// Deliberately NO CreateAsset call: this coordinator's own manifest
	// resolves the never-uploaded sequence as vacuously present (nothing
	// to be missing — [cueactivate.cueAssetsPresent]'s own doc comment on
	// an unauthored sequence), so Authorize's own asset check passes and
	// this activation is actually dispatched — the coordinator/node
	// disagreement problem 2 names.

	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)

	// The node's own result reports asset-missing post-dispatch.
	setup.audioPub.result = cueActivationNodeResultPayload(false, string(cueauth.OutcomeAssetMissing))

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil)

	deadline := time.After(2 * time.Second)
	for {
		setup.audioPub.mu.Lock()
		stopped := 0
		for _, d := range setup.audioPub.dispatched {
			if d.Action == "audio.session.stop" {
				stopped++
			}
		}
		setup.audioPub.mu.Unlock()
		if stopped > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no audio.session.stop was ever dispatched within 2s: a node-side asset-missing refusal must still reach fail-to-black")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// --- Problem 3: the fail-to-black dispatch must not stall the tick ---

// putRenderOnlyCueForTest writes a show.cue declaring only a render
// output, naming a sequence nothing ever uploads — mirrors
// putAudioOnlyCueForTest one output over.
func putRenderOnlyCueForTest(t *testing.T, st *store.Store, id, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "seq-" + id}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, id, payload)
}

// TestCueActivationTickOneAssetMissingFailToBlackDoesNotBlockTick proves
// problem 3's fix: [handlers.cueActivationTickOne] must return promptly
// even though the scoped fail-to-black dispatch it triggers awaits real
// node confirmation (renderCommandConfirmDeadline) that, in this test (an
// Observations source that never reports the surface as cleared), never
// arrives before the deadline. Before this fix, the render.surface.clear
// this Cue's own refusal triggers was dispatched synchronously, in-line,
// inside this exact method — so a bad node's refusal stalled
// cueActivationTick's own sequential loop over EVERY OTHER FPP instance
// for the full renderCommandConfirmDeadline, paid again at every new
// entry-start on the bad node. renderCommandConfirmDeadline is shrunk
// (this package's own established test-only-override convention — see
// renderdispatch_test.go) to a value still far larger than any budget a
// non-blocking tick should need, so a tick that returns within that
// budget could only be explained by the dispatch running off the tick's
// own critical path, never by a lucky fast confirmation.
func TestCueActivationTickOneAssetMissingFailToBlackDoesNotBlockTick(t *testing.T) {
	oldDeadline, oldPoll := renderCommandConfirmDeadline, renderCommandPollInterval
	renderCommandConfirmDeadline = 300 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { renderCommandConfirmDeadline, renderCommandPollInterval = oldDeadline, oldPoll })

	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "cue-1", "playlist-1", "inst-1", "entry-1", "render-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	renderPutSurface(t, setup.st, "surface-1", showID, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putRenderOnlyCueForTest(t, setup.st, cueID, showID)

	playlist := config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: entryID, Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	}
	putPlaylistForTest(t, setup.st, playlistID, playlist)
	putActiveShowForTest(t, setup.st, showID)

	// The Cue's own render asset is genuinely missing, exactly mirroring
	// the audio-only fixture above one output over.
	if _, _, err := setup.st.CreateAsset(context.Background(), store.AssetRecord{
		ID: "sha256:missing-cue-1-render", ShowID: showID, SequenceID: "seq-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "render",
		ContentHash: "sha256:missing-cue-1-render", RuntimeFilename: "missing.fseq",
		SizeBytes: 1024, Backend: "volume", StorageKey: "sha256:missing-cue-1-render",
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}

	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	// setup.obs (dynamicObservationLister) reports zero surface.pipeline.
	// state rows for the whole test, so confirmRenderCommand can NEVER
	// confirm — it will run out its own full renderCommandConfirmDeadline
	// every time it is reached. If dispatchCueScopedBlackAndSilence still
	// ran on cueActivationTickOne's own goroutine, this call would take
	// at least renderCommandConfirmDeadline to return.
	start := time.Now()
	h.cueActivationTickOne(context.Background(), now, obs, nil)
	elapsed := time.Since(start)
	if elapsed >= renderCommandConfirmDeadline {
		t.Fatalf("cueActivationTickOne took %s, want well under renderCommandConfirmDeadline (%s): the fail-to-black dispatch must not block the caller", elapsed, renderCommandConfirmDeadline)
	}

	// The dispatch must still actually happen — just off the tick's own
	// critical path. Poll (bounded well past renderCommandConfirmDeadline,
	// since the real clear-and-timeout round trip must complete) for the
	// render.surface.clear this refusal must still produce.
	deadline := time.After(2 * time.Second)
	for setup.renderPub.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("render.surface.clear was never dispatched within 2s: the async fail-to-black dispatch must still run, just off the tick's own critical path")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// setup.renderPub.count() > 0 only proves the clear was DISPATCHED —
	// the background goroutine cueActivationTickOne launched is still
	// polling toward its own renderCommandConfirmDeadline (it never
	// confirms, by this test's own fixture) when that count first turns
	// positive. h.cueActivationFailToBlackWG is that goroutine's real
	// owner (see cueactivationloop.go's own doc comment); waiting on it
	// here — an explicit hook, not a sleep — is what lets t.Cleanup above
	// safely restore renderCommandConfirmDeadline/renderCommandPollInterval
	// once this call returns, instead of racing the still-running goroutine's
	// own reads of those same package vars.
	h.cueActivationFailToBlackWG.Wait()
}
