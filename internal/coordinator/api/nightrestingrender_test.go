package api

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

const restingRenderTestHash = "sha256:resting-loop-bytes"

// restingRenderDispatchFixture is an active show with render node render-01
// holding a surface and an inventoried FSEQ for the active night session's
// resting sequence "resting-loop".
func restingRenderDispatchFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time) *handlers {
	t.Helper()
	const showID, nodeID = "halloween-2026", "render-01"
	ctx := context.Background()
	putShowForTest(t, setup.st, showID, "Halloween 2026")
	declareNodeForTest(t, setup.st, nodeID)
	surface, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: showID, Name: "garage", Node: nodeID,
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode surface: %v", err)
	}
	putConfigForTest(t, setup.st, config.ShowSurfaceConfigKind, "garage", surface)
	if _, _, err := setup.st.CreateAsset(ctx, store.AssetRecord{
		ID: "resting-loop-render-01", ShowID: showID, SequenceID: "resting-loop",
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "fseq",
		ContentHash: restingRenderTestHash, RuntimeFilename: "RestingLoop.fseq",
		SizeBytes: 1024, Backend: "volume", StorageKey: restingRenderTestHash,
	}); err != nil {
		t.Fatalf("create resting asset: %v", err)
	}
	if err := setup.st.ReplaceNodeAssetInventory(ctx, nodeID,
		[]store.NodeAssetInventoryRecord{{NodeID: nodeID, ContentHash: restingRenderTestHash, RuntimeFilename: "RestingLoop.fseq", SizeBytes: 1024, VerifiedAt: now}},
		store.NodeAssetReportRecord{NodeID: nodeID, ReportedAt: now, Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory: %v", err)
	}
	night, err := config.EncodeNightSessionPayload(config.NightSessionPayload{
		Show: showID, Label: "resting render",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "resting",
			TimelineAsset: config.NightSessionAssetRef{Show: showID, Sequence: "resting-loop", Target: "fpp-main"},
		},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	})
	if err != nil {
		t.Fatalf("encode night.session: %v", err)
	}
	putConfigForTest(t, setup.st, config.NightSessionConfigKind, "night", night)
	pointer, err := config.EncodeNightSessionActivePayload(config.NightSessionActivePayload{Session: "night"})
	if err != nil {
		t.Fatalf("encode night.session.active: %v", err)
	}
	putConfigForTest(t, setup.st, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID, pointer)
	putActiveShowForTest(t, setup.st, showID)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	return &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
}

func restingRenderTestEvidence(now time.Time) nightRestingRenderEvidence {
	return nightRestingRenderEvidence{
		key: "s1:1:resting-intershow:x", attempt: 1, show: "halloween-2026", sequence: "resting-loop",
		runnerInstance: "fpp-main", positionMS: 1200, evidenceAt: now,
	}
}

func TestNightDispatchRestingRenderActivatesTheRestingEntry(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	h := restingRenderDispatchFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	confirmed, pending := h.nightDispatchRestingRender(context.Background(), now, restingRenderTestEvidence(now), map[string]bool{}, cueActivationIssuer{PrincipalID: "night:s1"})
	if pending != 0 || len(confirmed) != 1 || confirmed[0] != "render-01" {
		t.Fatalf("confirmed = %v pending = %d, want [render-01] and 0", confirmed, pending)
	}
	if setup.pub.lastAction != "cue.activate" {
		t.Fatalf("published action = %q, want cue.activate", setup.pub.lastAction)
	}
	if got := setup.pub.lastParams["cueId"]; got != cuecatalog.RestingEntryID("resting-loop") {
		t.Fatalf("activation cueId = %v, want the resting entry", got)
	}
}

func TestNightDispatchRestingRenderCountsANodeThatRefused(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	h := restingRenderDispatchFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(false, "apply-failed")

	confirmed, pending := h.nightDispatchRestingRender(context.Background(), now, restingRenderTestEvidence(now), map[string]bool{}, cueActivationIssuer{PrincipalID: "night:s1"})
	if len(confirmed) != 0 || pending != 1 {
		t.Fatalf("confirmed = %v pending = %d, want none and 1", confirmed, pending)
	}
}

func TestNightDispatchRestingRenderSkipsNodesAlreadyDone(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	h := restingRenderDispatchFixture(t, setup, now)
	setup.pub.result = mqttproto.ResultPayload{}

	confirmed, pending := h.nightDispatchRestingRender(context.Background(), now, restingRenderTestEvidence(now), map[string]bool{"render-01": true}, cueActivationIssuer{PrincipalID: "night:s1"})
	if len(confirmed) != 0 || pending != 0 || setup.pub.count() != 0 {
		t.Fatalf("confirmed = %v pending = %d published = %d, want nothing sent", confirmed, pending, setup.pub.count())
	}
}

func TestNightRestingSequencePlayingNeedsTheRestingFileFromTheRestingPlaylist(t *testing.T) {
	anchor := nightContentAnchor{Playlist: "resting"}
	playing := nightPlaybackObservation{
		Current: true, Status: fppStatusValuePlaying,
		Playlist: "resting", PlaylistCurrent: true, Item: "RestingLoop.fseq", ItemCurrent: true,
	}
	if !nightRestingSequencePlaying(playing, anchor, "RestingLoop.fseq") {
		t.Fatal("resting file playing from the resting playlist was not accepted")
	}
	cases := map[string]func(o *nightPlaybackObservation){
		"another sequence":    func(o *nightPlaybackObservation) { o.Item = "Thriller.fseq" },
		"another playlist":    func(o *nightPlaybackObservation) { o.Playlist = "show" },
		"idle":                func(o *nightPlaybackObservation) { o.Status = "idle" },
		"stale status":        func(o *nightPlaybackObservation) { o.Current = false },
		"stale sequence name": func(o *nightPlaybackObservation) { o.ItemCurrent = false },
		"stale playlist name": func(o *nightPlaybackObservation) { o.PlaylistCurrent = false },
	}
	for name, mutate := range cases {
		obs := playing
		mutate(&obs)
		if nightRestingSequencePlaying(obs, anchor, "RestingLoop.fseq") {
			t.Errorf("%s: accepted as resting playing", name)
		}
	}
	if nightRestingSequencePlaying(playing, anchor, "") {
		t.Error("an unknown resting file was accepted")
	}
}
