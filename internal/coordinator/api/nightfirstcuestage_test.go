package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// nightFirstCueStageTestFixture builds an active show with a two-node
// audio-only Playlist: entry-1 is video-only (no show.cue audio output,
// proving [nightFirstAudioBearingCue]'s own scan skips it) and entry-2 is
// the first entry that actually carries audio, authorized on both nodes.
func nightFirstCueStageTestFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time) (nodeA, nodeB, playlistID, cueID string) {
	t.Helper()
	const showID, videoCueID, instanceUUID = "halloween-2026", "cue-video", "inst-1"
	cueID = "cue-audio"
	playlistID = "playlist-1"
	nodeA, nodeB = "audio-01", "audio-02"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	for _, nodeID := range []string{nodeA, nodeB} {
		putAudioNodeForTest(t, setup.st, nodeID)
		declareNodeForTest(t, setup.st, nodeID)
		putFreshReportForTest(t, setup.st, nodeID, now)
	}
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, videoCueID, mustEncodeShowCuePayloadForTest(t, config.ShowCuePayload{
		Show: showID, Name: videoCueID,
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "video-" + videoCueID}},
	}))
	// Targets names both nodes explicitly: an empty Targets resolves to
	// the installation's single default audio node, which would leave
	// only one of the two nodes actually participating.
	putConfigForTest(t, setup.st, config.ShowCueConfigKind, cueID, mustEncodeShowCuePayloadForTest(t, config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cueID, Targets: []string{nodeA, nodeB}}},
	}))
	putPlaylistForTest(t, setup.st, playlistID, config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: videoCueID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-2", Cue: cueID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	})
	putActiveShowForTest(t, setup.st, showID)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cueID, nodeA, now)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cueID, nodeB, now)
	return nodeA, nodeB, playlistID, cueID
}

func mustEncodeShowCuePayloadForTest(t *testing.T, p config.ShowCuePayload) string {
	t.Helper()
	raw, err := config.EncodeShowCuePayload(p)
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	return raw
}

// TestNightStageFirstShowCueAudioStagesEveryAudioNode proves the owner
// ruling's staging half: the first PLAYLIST entry with no audio output is
// skipped, the next entry's Cue is staged under
// [cueactivation.PrepareStagingSessionID] via apply then prepare on EVERY
// audio node it resolves to, and nothing is reported unstaged when every
// node confirms.
func TestNightStageFirstShowCueAudioStagesEveryAudioNode(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeA, nodeB, playlistID, cueID := nightFirstCueStageTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", StateEnteredAt: now}
	payload := config.NightSessionPayload{ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "inst-1", Playlist: playlistID}}

	unstaged := h.nightStageFirstShowCueAudio(context.Background(), now, rec, payload)
	if len(unstaged) != 0 {
		t.Fatalf("unstagedNodes = %v, want none: both nodes' fake dispatches confirm", unstaged)
	}

	for _, nodeID := range []string{nodeA, nodeB} {
		staged := dispatchedActionsToSessionForNode(setup, cueactivation.PrepareStagingSessionID, nodeID)
		if len(staged) != 2 {
			t.Fatalf("node %q: dispatched %d commands against the staging session, want 2 (apply, prepare); got %+v", nodeID, len(staged), staged)
		}
		if staged[0].Action != "audio.session.apply" {
			t.Fatalf("node %q: first staging dispatch action = %q, want audio.session.apply", nodeID, staged[0].Action)
		}
		media, ok := staged[0].Params["media"].(map[string]any)
		if !ok {
			t.Fatalf("node %q: audio.session.apply params carried no media object: %+v", nodeID, staged[0].Params)
		}
		if got := media["assetId"]; got != "asset-"+cueID {
			t.Fatalf("node %q: staged media assetId = %v, want %q", nodeID, got, "asset-"+cueID)
		}
		if staged[1].Action != "audio.session.prepare" {
			t.Fatalf("node %q: second staging dispatch action = %q, want audio.session.prepare", nodeID, staged[1].Action)
		}
	}

	// Never dispatched against the playing show session: staging there
	// would tear down whatever is already playing once the show starts.
	for _, d := range setup.pub.dispatched {
		if s, _ := d.Params["sessionId"].(string); s == cueactivation.AudioSessionID {
			t.Fatalf("dispatched against the playing show session id %q: %+v", cueactivation.AudioSessionID, d)
		}
	}
}

// TestNightStageFirstShowCueAudioRepeatTickSkipsAlreadyDispatched proves
// staging is idempotent across the repeat ticks nightAdvanceTransitionToShow
// makes while waiting on the player to start: a second call finds every
// node's apply already recorded under its own idempotency key and
// dispatches nothing more.
func TestNightStageFirstShowCueAudioRepeatTickSkipsAlreadyDispatched(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, _, playlistID, _ := nightFirstCueStageTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", StateEnteredAt: now}
	payload := config.NightSessionPayload{ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "inst-1", Playlist: playlistID}}

	h.nightStageFirstShowCueAudio(context.Background(), now, rec, payload)
	afterFirst := len(setup.pub.dispatched)
	if afterFirst == 0 {
		t.Fatal("first call dispatched nothing")
	}

	h.nightStageFirstShowCueAudio(context.Background(), now.Add(time.Second), rec, payload)
	if len(setup.pub.dispatched) != afterFirst {
		t.Fatalf("second call dispatched %d more command(s) (%v), want none: a repeat tick must not re-stage",
			len(setup.pub.dispatched)-afterFirst, setup.pub.dispatched[afterFirst:])
	}
}

// TestNightStageFirstShowCueAudioSecondEntryRestagesWithNewKeysAndHigherRevisions
// proves the fix for the keying defect a rig review caught: an idempotency
// key derived from (rec.ID, rec.Cycle, rec.StateEnteredAt) — never
// rec.ArmedShowID — so a SECOND entry into the same show tonight (a later
// Cycle, a later StateEnteredAt, the SAME session id) stages again rather
// than being skipped as an already-seen replay, dispatches under
// DIFFERENT node-side invocation ids, and carries a HIGHER revision on
// the shared staging session than the first entry did.
func TestNightStageFirstShowCueAudioSecondEntryRestagesWithNewKeysAndHigherRevisions(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeA, _, playlistID, _ := nightFirstCueStageTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	payload := config.NightSessionPayload{ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "inst-1", Playlist: playlistID}}

	firstEntry := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", Cycle: 1, StateEnteredAt: now}
	if unstaged := h.nightStageFirstShowCueAudio(context.Background(), now, firstEntry, payload); len(unstaged) != 0 {
		t.Fatalf("first entry unstagedNodes = %v, want none", unstaged)
	}
	firstStaged := dispatchedActionsToSessionForNode(setup, cueactivation.PrepareStagingSessionID, nodeA)
	if len(firstStaged) != 2 {
		t.Fatalf("node %q after first entry: dispatched %d commands, want 2", nodeA, len(firstStaged))
	}

	// A LATER entry into the same show tonight: same session id, a later
	// Cycle, and a later StateEnteredAt — the SAME ArmedShowID reuse a
	// stale key derivation would have collapsed into one no-op skip.
	secondEntry := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", Cycle: 2, StateEnteredAt: now.Add(time.Hour)}
	if unstaged := h.nightStageFirstShowCueAudio(context.Background(), now.Add(time.Hour), secondEntry, payload); len(unstaged) != 0 {
		t.Fatalf("second entry unstagedNodes = %v, want none", unstaged)
	}

	allStaged := dispatchedActionsToSessionForNode(setup, cueactivation.PrepareStagingSessionID, nodeA)
	if len(allStaged) != 4 {
		t.Fatalf("node %q after both entries: dispatched %d commands total, want 4 (2 per entry): the second entry must not be skipped as a replay", nodeA, len(allStaged))
	}
	secondStaged := allStaged[2:]

	firstApplyKey, _ := firstStaged[0].Params["invocationId"].(string)
	secondApplyKey, _ := secondStaged[0].Params["invocationId"].(string)
	if firstApplyKey == "" || secondApplyKey == "" {
		t.Fatalf("apply invocationId missing: first=%q second=%q", firstApplyKey, secondApplyKey)
	}
	if firstApplyKey == secondApplyKey {
		t.Fatalf("both entries dispatched under the SAME apply invocationId %q, want different keys per entry", firstApplyKey)
	}

	// Round-tripped through JSON by the fake publisher, so a number decodes
	// as float64, never the uint64 [cueactivation.PrepareStagingSessionRevision]
	// actually returned.
	firstRevision, _ := firstStaged[0].Params["revision"].(float64)
	secondRevision, _ := secondStaged[0].Params["revision"].(float64)
	if secondRevision <= firstRevision {
		t.Fatalf("second entry apply revision = %v, want greater than the first entry's %v", secondRevision, firstRevision)
	}
}

// TestNightKickOffFirstCueStageRunsInBackgroundAndNeverBlocks proves the
// owner ruling's own timing fix: staging is kicked off without waiting for
// any node to answer (a Raspberry Pi 3B+'s cold prepare takes seconds),
// and a node that never answers at all is read back as still-unconfirmed
// at the launch moment rather than ever being waited on there.
func TestNightKickOffFirstCueStageRunsInBackgroundAndNeverBlocks(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeA, nodeB, playlistID, _ := nightFirstCueStageTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	release := make(chan struct{})
	setup.pub.onAwaitResponseForNode = map[string]func(){
		nodeB + ":audio.session.apply": func() { <-release },
	}
	t.Cleanup(func() { close(release) })

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", Cycle: 1, StateEnteredAt: now}
	payload := config.NightSessionPayload{ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "inst-1", Playlist: playlistID}}

	started := time.Now()
	h.nightKickOffFirstCueStage(context.Background(), now, rec, payload)
	if took := time.Since(started); took > 100*time.Millisecond {
		t.Fatalf("nightKickOffFirstCueStage took %v to return, want near-instant: it must never wait on any node", took)
	}

	// Read back immediately, before nodeB's fake node has ever answered:
	// this is exactly what nightAdvanceTransitionToShow's own launch step
	// does, and it must see "not done yet," never block.
	if _, done := h.nightFirstCueStageStatus(rec); done {
		t.Fatal("staging status reported done immediately after kickoff, before the slow node could possibly have answered")
	}

	// A repeat kickoff for the SAME entry must not launch a second
	// goroutine (and therefore never a second round of node dispatches).
	h.nightKickOffFirstCueStage(context.Background(), now, rec, payload)

	// nodeB's own dispatch is left blocked (released only by t.Cleanup);
	// the goroutine still finishes on its own, bounded by
	// nightStageFirstShowCueTimeout, exactly as
	// TestNightStageFirstShowCueAudioSlowNodeReportedUnstagedAndBounded
	// proves at nightStageFirstShowCueAudio's own level.
	h.nightFirstCueStageWG.Wait()

	unstaged, done := h.nightFirstCueStageStatus(rec)
	if !done {
		t.Fatal("staging status still not done after the goroutine finished")
	}
	if len(unstaged) != 1 || unstaged[0] != nodeB {
		t.Fatalf("unstagedNodes = %v, want [%q]", unstaged, nodeB)
	}
	staged := dispatchedActionsToSessionForNode(setup, cueactivation.PrepareStagingSessionID, nodeA)
	if len(staged) != 2 {
		t.Fatalf("healthy node %q: dispatched %d commands, want 2 (apply, prepare)", nodeA, len(staged))
	}
}

// TestNightStageFirstShowCueAudioSlowNodeReportedUnstagedAndBounded proves
// the owner ruling's own bound: a node whose apply never answers is named
// in unstagedNodes rather than allowed to hold up start-night, and the
// whole call returns within about one nightStageFirstShowCueTimeout, never
// anywhere near audio.session.apply's own 15s dispatch deadline.
func TestNightStageFirstShowCueAudioSlowNodeReportedUnstagedAndBounded(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeA, nodeB, playlistID, _ := nightFirstCueStageTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	release := make(chan struct{})
	setup.pub.onAwaitResponseForNode = map[string]func(){
		nodeB + ":audio.session.apply": func() { <-release },
	}
	t.Cleanup(func() { close(release) })

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}

	rec := store.NightSessionRecord{ID: "night-1", ArmedShowID: "armed-1", StateEnteredAt: now}
	payload := config.NightSessionPayload{ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "inst-1", Playlist: playlistID}}

	started := time.Now()
	unstaged := h.nightStageFirstShowCueAudio(context.Background(), now, rec, payload)
	took := time.Since(started)

	if took > 3*nightStageFirstShowCueTimeout {
		t.Fatalf("nightStageFirstShowCueAudio took %v, want at most about one nightStageFirstShowCueTimeout (%v)", took, nightStageFirstShowCueTimeout)
	}
	if len(unstaged) != 1 || unstaged[0] != nodeB {
		t.Fatalf("unstagedNodes = %v, want [%q]", unstaged, nodeB)
	}

	staged := dispatchedActionsToSessionForNode(setup, cueactivation.PrepareStagingSessionID, nodeA)
	if len(staged) != 2 {
		t.Fatalf("healthy node %q: dispatched %d commands, want 2 (apply, prepare): a slow sibling must not block it", nodeA, len(staged))
	}
}

// dispatchedActionsToSessionForNode narrows [dispatchedActionsToSession]
// to nodeID's own dispatches, in dispatch order.
func dispatchedActionsToSessionForNode(setup *audioDispatchTestSetup, sessionID, nodeID string) []dispatchedAudioCommand {
	var out []dispatchedAudioCommand
	for _, d := range dispatchedActionsToSession(setup, sessionID) {
		if d.NodeID == nodeID {
			out = append(out, d)
		}
	}
	return out
}
