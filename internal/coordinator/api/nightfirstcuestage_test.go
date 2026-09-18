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
