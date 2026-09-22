package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
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

// TestCueActivationTickOneAudioOnlyAssetMissingNeverBlacksOutRenderSurface
// proves problem 1's fix through the composed path: node "audio-01" holds
// BOTH a declared audio.node object AND a show.surface (so it renders for
// OTHER cues in this Show) and this Show's one Cue declares audio only.
// When that Cue's own audio asset is genuinely missing, THIS
// coordinator's own pre-dispatch Authorize refuses asset-missing, and the
// resulting fail-to-black must stop only [cueactivation.AudioSessionID]
// (the session THIS Cue's own audio output actually runs in) and must
// dispatch NO render.surface.blackout at all and NO
// [cueactivation.BackgroundSessionID]/[cueactivation.AnnouncementSessionID]
// stop — the exact node-wide blast radius the reviewer demonstrated
// before this fix (an audio-only refused cue blacking both of the node's
// render surfaces).
func TestCueActivationTickOneAudioOnlyAssetMissingNeverBlacksOutRenderSurface(t *testing.T) {
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

	h.cueActivationTickOne(context.Background(), now, obs, nil, &cueHeldTracker{})

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
		t.Fatalf("render.surface.blackout was dispatched %d time(s), want 0: an audio-only Cue's refusal must never touch a render surface (problem 1)", got)
	}
}

// TestCueActivationTickOneAssetMissingNeverBlacksInProgramMode is the
// composed-path regression proof for the owner's own ruling this branch
// must not disturb: in setup/program mode an asset-missing refusal stays
// loud (the existing log line and audit entry) and dispatches NOTHING —
// zero render.surface.blackout, zero audio.session.stop — the reviewer's own
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

	h.cueActivationTickOne(context.Background(), now, obs, nil, &cueHeldTracker{})

	// The fail-to-black decision is made in its own goroutine (problem
	// 3's own fix); give it a generous window to have run and NOT
	// dispatched anything before asserting the negative.
	time.Sleep(200 * time.Millisecond)

	if got := setup.audioPub.count(); got != 0 {
		t.Fatalf("audio publish count = %d, want 0: setup/program mode must never black (the owner's own ruling)", got)
	}
	if got := setup.renderPub.count(); got != 0 {
		t.Fatalf("render.surface.blackout dispatched %d time(s), want 0: setup/program mode must never black (the owner's own ruling)", got)
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

	h.cueActivationTickOne(context.Background(), now, obs, nil, &cueHeldTracker{})

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
// arrives before the deadline. Before this fix, the render.surface.blackout
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
	h.cueActivationTickOne(context.Background(), now, obs, nil, &cueHeldTracker{})
	elapsed := time.Since(start)
	if elapsed >= renderCommandConfirmDeadline {
		t.Fatalf("cueActivationTickOne took %s, want well under renderCommandConfirmDeadline (%s): the fail-to-black dispatch must not block the caller", elapsed, renderCommandConfirmDeadline)
	}

	// The dispatch must still actually happen — just off the tick's own
	// critical path. Poll (bounded well past renderCommandConfirmDeadline,
	// since the real clear-and-timeout round trip must complete) for the
	// render.surface.blackout this refusal must still produce.
	deadline := time.After(2 * time.Second)
	for setup.renderPub.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("render.surface.blackout was never dispatched within 2s: the async fail-to-black dispatch must still run, just off the tick's own critical path")
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

// --- StateEvidenceBroken: owner ruling 2026-09-02, cue-deactivate-on-jump ---

// TestCueActivationTickOneEvidenceBrokenStopsOnlyThisCuesOwnAudio proves
// the composed path for the new marker: a real accepted observation
// resolves normally, [store.Store.MarkFPPPlaylistEntryObservationEvidenceBroken]
// then records the sequence-regression discontinuity §1.5 describes,
// and re-running the SAME tick over the now-marked row stops exactly the
// audio session this Cue's own audio output runs in — never a render
// clear (this Cue declares none), never the background bed or
// announcement session, mirroring
// TestCueActivationTickOneAudioOnlyAssetMissingNeverBlacksOutRenderSurface's
// own per-cue-scoping proof one mechanism over.
func TestCueActivationTickOneEvidenceBrokenStopsOnlyThisCuesOwnAudio(t *testing.T) {
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

	// Asset presence is deliberately not staged here: the evidence-broken
	// dispatch never calls Authorize (stopping requires no reauthorization,
	// matching dispatchBlackAndSilence's own existing posture) — it only
	// resolves the previously-activated Cue's own declared Outputs from a
	// live catalog lookup, so whether its asset is present or missing has
	// no bearing on this path at all.
	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)

	// The sequence-regression discontinuity: recorded exactly the way
	// fppobservations.go's own ingestion handler records it on a 409
	// refusal, against the SAME row the tick below reads.
	brokenAt := now.Add(time.Second)
	if err := setup.st.MarkFPPPlaylistEntryObservationEvidenceBroken(context.Background(), instanceUUID, brokenAt); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}
	if obs.EvidenceBrokenAt == nil {
		t.Fatal("precondition failed: EvidenceBrokenAt not set on the row the tick will read")
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil, &cueHeldTracker{})
	h.cueActivationFailToBlackWG.Wait()

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
		t.Fatalf("render.surface.blackout was dispatched %d time(s), want 0: an audio-only Cue's evidence-broken stop must never touch a render surface", got)
	}
}

// TestCueActivationTickOneEvidenceBrokenReplaysIdempotentlyOnAnUnchangedBreak
// proves evidenceBrokenEpisode's own idempotency contract: two ticks over
// the SAME obs.EvidenceBrokenAt value dispatch the stop exactly once, the
// second tick answering from the replay path with no second publish —
// mirroring TestDispatchBlackAndSilenceRedispatchesOnANewEpisode's own
// proof one policy over, for the opposite (unchanged-episode) case.
func TestCueActivationTickOneEvidenceBrokenReplaysIdempotentlyOnAnUnchangedBreak(t *testing.T) {
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
	putFailToBlackObservation(t, setup.st, failToBlackFixture{
		showID: showID, cueID: cueID, playlistID: playlistID, instanceUUID: instanceUUID, entryID: entryID, nodeID: nodeID,
	}, playlist, 1, 1, now)
	if err := setup.st.MarkFPPPlaylistEntryObservationEvidenceBroken(context.Background(), instanceUUID, now.Add(time.Second)); err != nil {
		t.Fatalf("mark evidence broken: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	held := &cueHeldTracker{}
	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()
	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()

	setup.audioPub.mu.Lock()
	stops := 0
	for _, d := range setup.audioPub.dispatched {
		if d.Action == "audio.session.stop" {
			stops++
		}
	}
	setup.audioPub.mu.Unlock()
	if stops != 1 {
		t.Fatalf("audio.session.stop dispatched %d time(s) across two ticks over an unchanged break, want exactly 1 (the second must replay, not re-dispatch)", stops)
	}
}

// --- FollowStop: owner ruling 2026-09-18, follow-the-player ---

// TestCueActivationTickOneFollowStopDispatchesToBothNodesNeverTheBed proves
// this seam's own fix: a Cue held live on two nodes, followed by an
// observation that the H0.2 hold policy leaves entirely undispatched (the
// rehearsal-rig gap this seam closes), must stop exactly the held Cue's
// own audio session on BOTH nodes it was dispatched to, and must never
// address the background bed or the staging session — [cueactivation.
// BackgroundSessionID] and [cueactivation.PrepareStagingSessionID] must
// never appear among the dispatched sessions.
func TestCueActivationTickOneFollowStopDispatchesToBothNodesNeverTheBed(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID = "halloween-2026", "wake-up", "playlist-1", "inst-1", "entry-1"
	const node1, node2 = "audio-01", "audio-02"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	putAudioNodeForTest(t, setup.st, node1)
	putAudioNodeForTest(t, setup.st, node2)
	declareNodeForTest(t, setup.st, node1)
	declareNodeForTest(t, setup.st, node2)
	// Explicit Targets naming both nodes: an untargeted audio output
	// resolves to only the installation's sole program+ltc node, and
	// this fixture declares two, so both must be named to participate.
	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: []string{node1, node2}}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, cueID, cuePayload)

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

	// The Cue's audio is held live on both nodes -- what a prior tick's own
	// StateActivated Decision would have left in the loop's own
	// cueHeldTracker, seeded directly rather than replaying a full prior
	// dispatch this test does not otherwise need.
	held := &cueHeldTracker{}
	held.observe(instanceUUID, cueactivate.Decision{
		State: cueactivate.StateActivated,
		Activations: map[string]cueactivation.Activation{
			node1: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-1", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
			node2: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-2", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
		},
	})

	// fppd restarted and came back on a different playlist: the observed
	// playlistHash no longer matches the bound one, so this resolves
	// StateMismatched under the hold policy -- the exact rig gap this
	// seam closes.
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 2, Action: "playing",
		PlaylistName: "resting-halloween", PlaylistHash: hash64ForTest("b2"),
		Section: "mainPlaylist", Position: 0, EntryKey: "unrelated-entry-key",
		EntryOccurrenceSequence: 2, ObservedAt: now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()

	setup.audioPub.mu.Lock()
	defer setup.audioPub.mu.Unlock()
	gotNodes := map[string]bool{}
	for _, d := range setup.audioPub.dispatched {
		if d.Action != "audio.session.stop" {
			continue
		}
		sessionID, _ := d.Params["sessionId"].(string)
		if sessionID != blackAndSilenceAudioSessionID {
			t.Fatalf("audio.session.stop dispatched sessionId %q, want only %q: never the bed or the staging session", sessionID, blackAndSilenceAudioSessionID)
		}
		gotNodes[d.NodeID] = true
	}
	if !gotNodes[node1] || !gotNodes[node2] || len(gotNodes) != 2 {
		t.Fatalf("audio.session.stop dispatched to %v, want both %q and %q", gotNodes, node1, node2)
	}
}

// TestCueActivationTickOneFollowStopNeverBlacksOutSurfaceForAnAudioOnlyCue
// proves a FollowStop-triggered cue-scoped stop never dispatches
// render.surface.blackout for a Cue that declares no render output at all,
// even though the node itself also has a show.surface assigned (for some
// OTHER Cue) — mirroring TestCueActivationTickOneAudioOnlyAssetMissingNeverBlacksOutRenderSurface's
// own proof one mechanism over.
func TestCueActivationTickOneFollowStopNeverBlacksOutSurfaceForAnAudioOnlyCue(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "wake-up", "playlist-1", "inst-1", "entry-1", "audio-01"

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

	held := &cueHeldTracker{}
	held.observe(instanceUUID, cueactivate.Decision{
		State: cueactivate.StateActivated,
		Activations: map[string]cueactivation.Activation{
			nodeID: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-1", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
		},
	})

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 2, Action: "playing",
		PlaylistName: "resting-halloween", PlaylistHash: hash64ForTest("b2"),
		Section: "mainPlaylist", Position: 0, EntryKey: "unrelated-entry-key",
		EntryOccurrenceSequence: 2, ObservedAt: now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()

	if got := setup.renderPub.count(); got != 0 {
		t.Fatalf("render.surface.blackout was dispatched %d time(s), want 0: an audio-only Cue's FollowStop must never touch a render surface", got)
	}
}

// TestCueActivationTickOneFollowStopBlacksOutSurfaceWithSurfaceIDWhenCueHasRenderOutput
// proves defect 2's own fix: a Cue that declares BOTH audio and render
// output on the held node must, on its FollowStop-triggered cue-scoped
// stop, dispatch render.surface.blackout carrying a real, non-empty
// params.surfaceId — before this fix, dispatchBlackAndSilenceBlackoutSurfaces
// built its renderDispatchInput with no Params at all, so the command
// reached the wire as "params":null and the node refused it
// ("render.surface.blackout: params.surfaceId is required").
func TestCueActivationTickOneFollowStopBlacksOutSurfaceWithSurfaceIDWhenCueHasRenderOutput(t *testing.T) {
	oldDeadline, oldPoll := renderCommandConfirmDeadline, renderCommandPollInterval
	renderCommandConfirmDeadline = 300 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { renderCommandConfirmDeadline, renderCommandPollInterval = oldDeadline, oldPoll })

	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID, surfaceID = "halloween-2026", "wake-up", "playlist-1", "inst-1", "entry-1", "audio-01", "surface-1"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	putAudioNodeForTest(t, setup.st, nodeID)
	renderPutSurface(t, setup.st, surfaceID, showID, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)

	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{
			Audio:  &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: []string{nodeID}},
			Render: &config.ShowCueRenderOutput{Sequence: "seq-" + cueID},
		},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, cueID, cuePayload)

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

	held := &cueHeldTracker{}
	held.observe(instanceUUID, cueactivate.Decision{
		State: cueactivate.StateActivated,
		Activations: map[string]cueactivation.Activation{
			nodeID: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-1", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
		},
	})

	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 2, Action: "playing",
		PlaylistName: "resting-halloween", PlaylistHash: hash64ForTest("b2"),
		Section: "mainPlaylist", Position: 0, EntryKey: "unrelated-entry-key",
		EntryOccurrenceSequence: 2, ObservedAt: now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()

	if got := setup.renderPub.count(); got != 1 {
		t.Fatalf("render.surface.blackout was dispatched %d time(s), want exactly 1", got)
	}
	env := setup.renderPub.payload[0]
	if env.Payload.Action != "render.surface.blackout" {
		t.Fatalf("dispatched action = %q, want render.surface.blackout", env.Payload.Action)
	}
	if got, _ := env.Payload.Params["surfaceId"].(string); got != surfaceID {
		t.Fatalf("render.surface.blackout params.surfaceId = %q, want %q (the node refuses a missing surfaceId)", got, surfaceID)
	}

	setup.audioPub.mu.Lock()
	defer setup.audioPub.mu.Unlock()
	stops := 0
	for _, d := range setup.audioPub.dispatched {
		if d.Action == "audio.session.stop" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("audio.session.stop dispatched %d time(s), want exactly 1", stops)
	}
}

// TestCueActivationTickOneFollowStopDispatchesExactlyOnceAcrossManyTicks
// proves defect 1's own fix: an FPP player that has left a held Cue for a
// playlist [cueactivate.Decide] cannot bind (StateMismatched under the
// hold policy) keeps producing the identical FollowStop tick after tick,
// since nothing else ever supersedes or clears it. Before this fix,
// [cueHeldTracker.observe] never cleared it, so every tick redispatched
// the same stop against that tick's own advancing `now` — a different
// derived revision each time — and the node's own idempotency-key replay
// path answered every dispatch after the first as a params conflict
// (the rehearsal rig's own per-tick "blackAndSilence audio stop dispatch
// refused" log spam). Many ticks, each with its own later `now` exactly as
// [CueActivationLoop.Run]'s own periodic ticker would produce, must
// dispatch the stop exactly once.
// putAuthorizedRenderAndAudioAssetsForTest creates a real, node-inventoried
// asset for both a render sequence ("seq-"+cueID) and an audio sequence
// ("asset-"+cueID), naming both in one ReplaceNodeAssetInventory call —
// mirrors putAuthorizedAudioAssetForTest one output over, combined so a
// Cue declaring both outputs resolves both as present.
func putAuthorizedRenderAndAudioAssetsForTest(t *testing.T, st *store.Store, showID, cueID, nodeID string, now time.Time) {
	t.Helper()
	renderHash := "sha256:authorized-render-" + cueID
	renderFilename := "Authorized-" + cueID + ".fseq"
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: renderHash + "-node-" + nodeID, ShowID: showID, SequenceID: "seq-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "fseq",
		ContentHash: renderHash, RuntimeFilename: renderFilename,
		SizeBytes: 1024, Backend: "volume", StorageKey: renderHash,
	}); err != nil {
		t.Fatalf("create authorized render asset for %q: %v", cueID, err)
	}
	audioHash := "sha256:authorized-audio-" + cueID
	audioFilename := "Authorized-" + cueID + ".wav"
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: audioHash + "-node-" + nodeID, ShowID: showID, SequenceID: "asset-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: audioHash, RuntimeFilename: audioFilename,
		SizeBytes: 1024, Backend: "volume", StorageKey: audioHash,
	}); err != nil {
		t.Fatalf("create authorized audio asset for %q: %v", cueID, err)
	}
	if err := st.ReplaceNodeAssetInventory(context.Background(), nodeID,
		[]store.NodeAssetInventoryRecord{
			{NodeID: nodeID, ContentHash: renderHash, RuntimeFilename: renderFilename, SizeBytes: 1024, VerifiedAt: now},
			{NodeID: nodeID, ContentHash: audioHash, RuntimeFilename: audioFilename, SizeBytes: 1024, VerifiedAt: now},
		},
		store.NodeAssetReportRecord{NodeID: nodeID, ReportedAt: now, Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory for %q: %v", nodeID, err)
	}
}

// TestBlackoutThenCueActivateBothDispatchNoAssignmentTornDown is the
// rehearsal-stack regression this build closes: FPP song entry activates a
// Cue with a render output (held here, mirroring this file's own
// FollowStop tests' established pattern for "already active" prior state),
// FPP moves to a resting entry the coordinator cannot bind, and the loop's
// blackAndSilence effect fires. Before this build, that effect dispatched
// render.surface.clear, which deletes the surface's persisted assignment
// on the node (internal/agent/renderops.go's clearSurface removes it from
// the AssignmentStore); the very next cue.activate for that surface then
// found no assignment to swap content onto and reported apply-failed
// (internal/agent/cueactivationrender.go's activateRender: "no surface is
// assigned on this node"). This test fails on main: the render dispatch
// this test asserts is "render.surface.blackout" is literally
// "render.surface.clear" there. render.surface.blackout never touches the
// assignment (internal/agent/renderops.go's blackoutSurface only sets a
// flag, proven directly at the agent layer by renderops_test.go), so the
// following cue.activate for the next song still has an assignment to
// swap content onto and dispatches and confirms normally.
func TestBlackoutThenCueActivateBothDispatchNoAssignmentTornDown(t *testing.T) {
	oldDeadline, oldPoll := renderCommandConfirmDeadline, renderCommandPollInterval
	renderCommandConfirmDeadline = 300 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { renderCommandConfirmDeadline, renderCommandPollInterval = oldDeadline, oldPoll })

	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID, surfaceID = "halloween-2026", "wake-up", "playlist-1", "inst-1", "entry-1", "audio-01", "surface-1"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putShowModeForTest(t, setup.st, config.ShowModeShow)
	putAudioNodeForTest(t, setup.st, nodeID)
	renderPutSurface(t, setup.st, surfaceID, showID, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)

	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{
			Audio:  &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: []string{nodeID}},
			Render: &config.ShowCueRenderOutput{Sequence: "seq-" + cueID},
		},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, cueID, cuePayload)

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

	// Step 1: the song entry already activated this Cue's render output,
	// held from a prior tick this test does not otherwise need to replay
	// (mirrors TestCueActivationTickOneFollowStopBlacksOutSurfaceWithSurfaceIDWhenCueHasRenderOutput's
	// own seeding).
	held := &cueHeldTracker{}
	held.observe(instanceUUID, cueactivate.Decision{
		State: cueactivate.StateActivated,
		Activations: map[string]cueactivation.Activation{
			nodeID: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-1", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
		},
	})

	// Step 2: FPP moves to a resting entry the coordinator cannot bind,
	// resolving StateMismatched under the blackAndSilence policy.
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 2, Action: "playing",
		PlaylistName: "resting-halloween", PlaylistHash: hash64ForTest("b2"),
		Section: "mainPlaylist", Position: 0, EntryKey: "unrelated-entry-key",
		EntryOccurrenceSequence: 2, ObservedAt: now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	h.cueActivationTickOne(context.Background(), now, obs, nil, held)
	h.cueActivationFailToBlackWG.Wait()

	if got := setup.renderPub.count(); got != 1 {
		t.Fatalf("render dispatch count after the resting entry = %d, want exactly 1", got)
	}
	if got := setup.renderPub.payload[0].Payload.Action; got != "render.surface.blackout" {
		t.Fatalf("render dispatch for the resting entry = %q, want render.surface.blackout (a clear would tear down surface %q's assignment before the next song)", got, surfaceID)
	}

	// Step 3: FPP moves to the next song, activating the same Cue again.
	// This dispatch is coordinator-side only (dispatchOneCueActivation,
	// bypassing the tick's own Reconcile/Decide for a direct, minimal
	// Activation); it does not itself prove the node still holds the
	// assignment (that is renderops_test.go's job, at the agent layer) but
	// it does prove the coordinator's own dispatch path is unaffected by
	// which render action just ran.
	// Both of this Cue's outputs must resolve a real, node-inventoried
	// asset for Authorize to pass — a single ReplaceNodeAssetInventory
	// call naming both, since a second call would overwrite the first's
	// item rather than add to it.
	putAuthorizedRenderAndAudioAssetsForTest(t, setup.st, showID, cueID, nodeID, now)

	issuer := cueActivationIssuer{PrincipalID: cueActivationSystemPrincipalID(instanceUUID)}
	act := cueactivation.Activation{
		Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-new-2",
		Show: showID, Generation: 1, CatalogRevision: resolvedCatalogRevisionForTest(t, setup.st, showID, nodeID),
		CueID: cueID, CueRevision: 1, PositionMS: 0,
		EvidenceAt: now,
	}
	setup.audioPub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if outcome.Err != nil {
		t.Fatalf("dispatchOneCueActivation for the next song: %v", outcome.Err)
	}
	if outcome.AuthorizeOutcome != "" {
		t.Fatalf("cue.activate for the next song was refused: outcome=%q reason=%q", outcome.AuthorizeOutcome, outcome.AuthorizeReason)
	}
	if !outcome.Dispatched || !outcome.Confirmed {
		t.Fatalf("cue.activate for the next song: Dispatched=%v Confirmed=%v, want both true", outcome.Dispatched, outcome.Confirmed)
	}

	// The one render dispatch this whole sequence ever made stays a
	// blackout — never a clear, at any point.
	setup.renderPub.mu.Lock()
	defer setup.renderPub.mu.Unlock()
	for i, p := range setup.renderPub.payload {
		if p.Payload.Action != "render.surface.blackout" {
			t.Fatalf("render dispatch %d = %q, want render.surface.blackout", i, p.Payload.Action)
		}
	}
}

func TestCueActivationTickOneFollowStopDispatchesExactlyOnceAcrossManyTicks(t *testing.T) {
	now := testNow
	setup := newFailToBlackComposedSetup(t, fixedClock(now))
	const showID, cueID, playlistID, instanceUUID, entryID, nodeID = "halloween-2026", "wake-up", "playlist-1", "inst-1", "entry-1", "audio-01"

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

	held := &cueHeldTracker{}
	held.observe(instanceUUID, cueactivate.Decision{
		State: cueactivate.StateActivated,
		Activations: map[string]cueactivation.Activation{
			nodeID: {Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-old-1", Show: showID, Generation: 1, CueID: cueID, CueRevision: 1},
		},
	})

	// The player left the held Cue for a playlist Decide cannot bind: the
	// same rehearsal-rig gap TestCueActivationTickOneFollowStopDispatchesToBothNodesNeverTheBed
	// proves for one tick, run here across many.
	if err := setup.st.PutFPPPlaylistEntryObservation(context.Background(), store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 2, Action: "playing",
		PlaylistName: "resting-halloween", PlaylistHash: hash64ForTest("b2"),
		Section: "mainPlaylist", Position: 0, EntryKey: "unrelated-entry-key",
		EntryOccurrenceSequence: 2, ObservedAt: now, ReceivedAt: now,
	}); err != nil {
		t.Fatalf("put fpp playlist entry observation: %v", err)
	}

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(now), logger: testLogger()}
	obs, err := setup.st.GetFPPPlaylistEntryObservation(context.Background(), instanceUUID)
	if err != nil {
		t.Fatalf("get fpp playlist entry observation: %v", err)
	}

	for i := 0; i < 5; i++ {
		tickNow := now.Add(time.Duration(i) * time.Second)
		h.cueActivationTickOne(context.Background(), tickNow, obs, nil, held)
		h.cueActivationFailToBlackWG.Wait()
	}

	if held.get(instanceUUID) != nil {
		t.Fatalf("held record for %q was not cleared once its FollowStop was dispatched", instanceUUID)
	}

	setup.audioPub.mu.Lock()
	defer setup.audioPub.mu.Unlock()
	stops := 0
	for _, d := range setup.audioPub.dispatched {
		if d.Action == "audio.session.stop" {
			stops++
		}
	}
	if stops != 1 {
		t.Fatalf("audio.session.stop dispatched %d time(s) across 5 ticks of an unchanged FollowStop, want exactly 1", stops)
	}
}
