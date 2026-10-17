package assetsync

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// putRestingNight writes night.session "night" for showID whose resting
// sequence is sequence, and points night.session.active at it when active.
func putRestingNight(t *testing.T, st *store.Store, showID, sequence string, active bool) {
	t.Helper()
	raw, err := config.EncodeNightSessionPayload(config.NightSessionPayload{
		Show: showID, Label: "resting render test",
		ShowPlaylist: config.NightSessionFPPPlaylist{FPPInstanceID: "fpp-main", Playlist: "show"},
		Resting: config.NightSessionResting{
			FPPInstanceID: "fpp-main", Playlist: "resting",
			TimelineAsset: config.NightSessionAssetRef{Show: showID, Sequence: sequence, Target: "fpp-main"},
		},
		AnnouncementDefaultPolicy: config.NightSessionAnnouncementPolicyDefault,
	})
	if err != nil {
		t.Fatalf("encode night.session payload: %v", err)
	}
	putConfig(t, st, config.NightSessionConfigKind, "night", raw)
	if active {
		pointer, err := config.EncodeNightSessionActivePayload(config.NightSessionActivePayload{Session: "night"})
		if err != nil {
			t.Fatalf("encode night.session.active payload: %v", err)
		}
		putConfig(t, st, config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID, pointer)
	}
}

// restingRenderFixture is one show with render node media-01 holding a
// surface and an FSEQ for "resting-loop", and a playlist with one Cue.
func restingRenderFixture(t *testing.T) *store.Store {
	t.Helper()
	st := openTestStore(t)
	declareNode(t, st, "media-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "wall-1", "halloween-2026", "media-01")
	putCue(t, st, "opener-cue", "halloween-2026", config.ShowCuePayload{
		Name:    "Opener",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "opener"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "opener-cue"))
	putActiveShow(t, st, "halloween-2026")
	createAssetWithMediaType(t, st, "halloween-2026", "resting-loop", store.AssetTargetKindNode, "media-01", "fseq",
		"sha256:"+strings.Repeat("b", 64), "RestingLoop.fseq")
	return st
}

func resolveRestingCatalog(t *testing.T, st *store.Store, nodeID string) Catalog {
	t.Helper()
	ctx := context.Background()
	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	return catalog
}

func TestCueCatalogCarriesRestingEntryForNodeHoldingTheSequence(t *testing.T) {
	st := restingRenderFixture(t)
	without := resolveRestingCatalog(t, st, "media-01")
	if without.Resting != nil {
		t.Fatalf("Resting = %+v with no night session, want nil", without.Resting)
	}

	putRestingNight(t, st, "halloween-2026", "resting-loop", true)
	catalog := resolveRestingCatalog(t, st, "media-01")
	if catalog.Resting == nil {
		t.Fatal("Resting = nil, want the resting-loop entry")
	}
	r := catalog.Resting
	if r.CueID != cuecatalog.RestingEntryID("resting-loop") || r.CueRevision != cuecatalog.RestingEntryRevision {
		t.Fatalf("resting entry identity = %q rev %d", r.CueID, r.CueRevision)
	}
	if r.Outputs.Render == nil || r.Outputs.Render.Filename != "RestingLoop.fseq" ||
		len(r.Outputs.Render.AssetHashes) != 1 || r.Outputs.Render.AssetHashes[0] != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("resting render output = %+v, want RestingLoop.fseq with its hash", r.Outputs.Render)
	}
	if r.Outputs.Audio != nil || len(r.Triggers) != 0 {
		t.Fatalf("resting entry = %+v, want render only and no triggers", r)
	}
	if _, ok := cueOutputsByID(catalog.Entries, r.CueID); ok {
		t.Fatal("Entries holds the resting entry, want it only in AllEntries")
	}
	if _, ok := cueOutputsByID(catalog.AllEntries(), r.CueID); !ok {
		t.Fatal("AllEntries is missing the resting entry")
	}
	if catalog.Revision == without.Revision {
		t.Fatal("catalog revision did not change when the resting entry was added")
	}
	if !catalog.HasAnyOutput() {
		t.Fatal("HasAnyOutput = false, want true")
	}

	assertRoundTripRevisionAgrees(t, catalog)
}

func TestCueCatalogOmitsRestingEntry(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
		node  string
	}{
		{"night session not active", func(t *testing.T, st *store.Store) {
			putRestingNight(t, st, "halloween-2026", "resting-loop", false)
		}, "media-01"},
		{"night session for another show", func(t *testing.T, st *store.Store) {
			putShow(t, st, "christmas-2026", "Christmas 2026")
			putRestingNight(t, st, "christmas-2026", "resting-loop", true)
		}, "media-01"},
		{"node does not hold the sequence", func(t *testing.T, st *store.Store) {
			putRestingNight(t, st, "halloween-2026", "other-loop", true)
		}, "media-01"},
		{"node holds the file but no surface", func(t *testing.T, st *store.Store) {
			declareNode(t, st, "audio-01")
			createAssetWithMediaType(t, st, "halloween-2026", "resting-loop", store.AssetTargetKindNode, "audio-01", "fseq",
				"sha256:"+strings.Repeat("c", 64), "RestingLoop.fseq")
			putRestingNight(t, st, "halloween-2026", "resting-loop", true)
		}, "audio-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := restingRenderFixture(t)
			tc.setup(t, st)
			catalog := resolveRestingCatalog(t, st, tc.node)
			if catalog.Resting != nil || catalog.RestingCoveredByCue != "" {
				t.Fatalf("Resting = %+v, covered by %q, want neither", catalog.Resting, catalog.RestingCoveredByCue)
			}
		})
	}
}

func TestCueCatalogLetsACueThatRendersTheRestingSequenceWin(t *testing.T) {
	st := restingRenderFixture(t)
	putCue(t, st, "resting-cue", "halloween-2026", config.ShowCuePayload{
		Name:    "Resting in the show",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "resting-loop"}},
	})
	putConfigRev(t, st, config.ShowPlaylistConfigKind, "main", 2, mustEncodePlaylist(t, simplePlaylist("halloween-2026", "opener-cue", "resting-cue")))
	putRestingNight(t, st, "halloween-2026", "resting-loop", true)

	catalog := resolveRestingCatalog(t, st, "media-01")
	if catalog.Resting != nil {
		t.Fatalf("Resting = %+v, want nil when a Cue already renders the sequence", catalog.Resting)
	}
	if catalog.RestingCoveredByCue != "resting-cue" {
		t.Fatalf("RestingCoveredByCue = %q, want resting-cue", catalog.RestingCoveredByCue)
	}
}

func mustEncodePlaylist(t *testing.T, p config.ShowPlaylistPayload) string {
	t.Helper()
	raw, err := config.EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	return raw
}
