package assetsync

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// putConfigRev writes payload as the given revision of (kind, id) and
// activates it — [putConfig]'s own sibling for a test that needs to
// simulate a SECOND write to an object already at revision 1 (putConfig
// itself always writes revision 1), which is what proving generation
// propagation and catalog revision change-detection both require.
func putConfigRev(t *testing.T, st *store.Store, kind, id string, revision int64, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: revision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision %s/%s/%d: %v", kind, id, revision, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, revision); err != nil {
		t.Fatalf("activate config revision %s/%s/%d: %v", kind, id, revision, err)
	}
}

func putCue(t *testing.T, st *store.Store, cueID, showID string, payload config.ShowCuePayload) {
	t.Helper()
	payload.Show = showID
	raw, err := config.EncodeShowCuePayload(payload)
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, cueID, raw)
}

func putPlaylist(t *testing.T, st *store.Store, playlistID string, payload config.ShowPlaylistPayload) {
	t.Helper()
	raw, err := config.EncodeShowPlaylistPayload(payload)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, playlistID, raw)
}

// putAudioNode writes a minimal, valid audio.node object whose id is
// nodeID — the fact that gates a node's audio/ltc/announcement catalog
// outputs.
func putAudioNode(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "usb-interface", LTCRoute: "usb-interface",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "single interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	putConfig(t, st, config.AudioNodeConfigKind, nodeID, raw)
}

func simplePlaylist(showID string, cueIDs ...string) config.ShowPlaylistPayload {
	entries := make([]config.ShowPlaylistEntry, 0, len(cueIDs))
	for i, cueID := range cueIDs {
		entries = append(entries, config.ShowPlaylistEntry{
			ID: cueID + "-entry", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Position: i},
		})
	}
	return config.ShowPlaylistPayload{
		Show: showID, Name: "main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: "11111111-1111-1111-1111-111111111111",
			PlaylistName: "Main", PlaylistHash: strings.Repeat("a", 64),
		},
		Entries: entries,
	}
}

// --- generation propagation ---

func TestActiveShowGenerationPropagatesFromRevision(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")

	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: "halloween-2026"})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	putConfigRev(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, 1, payload)

	active, err := ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	if !active.Configured || active.Generation != 1 {
		t.Fatalf("ResolveActiveShow after revision 1 = %+v, want Configured=true Generation=1", active)
	}

	// A deliberate reissue of the identical show is still a new revision
	// (writeShowConfigRevision computes nextRevisionNo unconditionally,
	// per H3 spec section 2) — this test writes revision 2 directly to
	// simulate that without going through the HTTP write path.
	putConfigRev(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, 2, payload)

	active2, err := ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow after revision 2: %v", err)
	}
	if active2.Generation != 2 {
		t.Fatalf("ResolveActiveShow after a reissue: Generation = %d, want 2", active2.Generation)
	}
}

func TestUnconfiguredActiveShowAuthorizesNothing(t *testing.T) {
	st := openTestStore(t)

	active, err := ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("ResolveActiveShow with no show.active written: %v", err)
	}
	if active.Configured {
		t.Fatalf("ResolveActiveShow with no show.active written: Configured = true, want false")
	}
	if active.Generation != 0 {
		t.Fatalf("ResolveActiveShow with no show.active written: Generation = %d, want 0 (zero value, not a real grant)", active.Generation)
	}

	// The catalog resolver refuses outright rather than returning an
	// empty-but-plausible catalog for an unconfigured show — an empty
	// catalog silently authorizing nothing looks identical to a Show with
	// zero Cues, and this seam's own rule is that absence must be honest,
	// never inferred from an empty result.
	if _, err := ResolveCueCatalog(context.Background(), st, active, "render-01"); err == nil {
		t.Fatalf("ResolveCueCatalog against an unconfigured active show: err = nil, want a refusal")
	}
}

// --- draft-cue exclusion ---

func TestCueCatalogExcludesDraftCues(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "garage", "halloween-2026", "render-01")

	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putCue(t, st, "unused-draft", "halloween-2026", config.ShowCuePayload{
		Name:    "Unused Draft",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "unused-draft"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if len(catalog.Entries) != 1 {
		t.Fatalf("ResolveCueCatalog entries = %+v, want exactly one (the referenced Cue)", catalog.Entries)
	}
	if catalog.Entries[0].CueID != "thriller" {
		t.Fatalf("ResolveCueCatalog entries[0].CueID = %q, want %q (the draft must not appear)", catalog.Entries[0].CueID, "thriller")
	}
}

func TestCueCatalogIncludesSafeCueRefEvenIfNoEntryReferencesIt(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")

	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}},
	})
	putCue(t, st, "safe", "halloween-2026", config.ShowCuePayload{
		Name:    "Safe",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "safe-audio"}},
	})

	playlist := simplePlaylist("halloween-2026", "thriller")
	playlist.MismatchPolicy = config.ShowPlaylistMismatchPolicySafeCue
	playlist.SafeCueRef = "safe"
	putPlaylist(t, st, "main", playlist)
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	found := false
	for _, e := range catalog.Entries {
		if e.CueID == "safe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ResolveCueCatalog entries = %+v, want the safeCueRef Cue included even with no Playlist entry naming it", catalog.Entries)
	}
}

// --- catalog revision stability and change-detection ---

func TestCueCatalogRevisionIsStableAcrossRepeatedResolution(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "garage", "halloween-2026", "render-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	first, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (first): %v", err)
	}
	second, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (second): %v", err)
	}
	if first.Revision == "" {
		t.Fatalf("ResolveCueCatalog: Revision is empty")
	}
	if first.Revision != second.Revision {
		t.Fatalf("ResolveCueCatalog resolved twice with nothing changed: revision %q != %q", first.Revision, second.Revision)
	}
}

func TestCueCatalogRevisionChangesWhenACueChanges(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "garage", "halloween-2026", "render-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	before, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (before): %v", err)
	}

	// A revised Cue (a second, activated revision) — same object id, new
	// content.
	revisedRaw, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "Thriller (revised)",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller-v2"}},
	})
	if err != nil {
		t.Fatalf("encode revised show.cue payload: %v", err)
	}
	putConfigRev(t, st, config.ShowCueConfigKind, "thriller", 2, revisedRaw)

	after, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (after): %v", err)
	}
	if after.Revision == before.Revision {
		t.Fatalf("ResolveCueCatalog revision did not change after the Cue it covers changed (still %q)", before.Revision)
	}
	if after.Entries[0].CueRevision != 2 {
		t.Fatalf("ResolveCueCatalog after revising the Cue: CueRevision = %d, want 2", after.Entries[0].CueRevision)
	}
}

func TestCueCatalogRevisionChangesWithGeneration(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))

	activePayload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: "halloween-2026"})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	putConfigRev(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, 1, activePayload)
	active1, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow (generation 1): %v", err)
	}
	catalog1, err := ResolveCueCatalog(ctx, st, active1, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (generation 1): %v", err)
	}

	// A deliberate reissue (H3 spec section 2): the identical Show,
	// written again, is a new generation.
	putConfigRev(t, st, config.ShowActiveConfigKind, config.ShowActiveObjectID, 2, activePayload)
	active2, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow (generation 2): %v", err)
	}
	catalog2, err := ResolveCueCatalog(ctx, st, active2, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (generation 2): %v", err)
	}

	if catalog1.Revision == catalog2.Revision {
		t.Fatalf("ResolveCueCatalog revision unchanged across a generation reissue (still %q), even though every Cue is identical", catalog1.Revision)
	}
}

func TestCueCatalogRevisionDiffersByNode(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	declareNode(t, st, "render-02")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "garage", "halloween-2026", "render-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	node1Catalog, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (render-01): %v", err)
	}
	node2Catalog, err := ResolveCueCatalog(ctx, st, active, "render-02")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (render-02): %v", err)
	}
	if node1Catalog.Revision == node2Catalog.Revision {
		t.Fatalf("ResolveCueCatalog: render-01 (holds a surface) and render-02 (does not) computed the identical revision %q", node1Catalog.Revision)
	}
	if node1Catalog.Entries[0].Outputs.Render == nil {
		t.Fatalf("ResolveCueCatalog (render-01): render output absent, want present (render-01 holds a surface)")
	}
	if node2Catalog.Entries[0].Outputs.Render != nil {
		t.Fatalf("ResolveCueCatalog (render-02): render output present, want absent (render-02 holds no surface)")
	}
}

// --- audio/ltc/announcement scoping by audio.node presence ---

func TestCueCatalogIncludesAudioOutputsOnlyForNodeWithAudioNode(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name: "Thriller",
		Outputs: config.ShowCueOutputs{
			Audio:        &config.ShowCueAudioOutput{Asset: "thriller-audio"},
			LTC:          &config.ShowCueLTCOutput{},
			Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyMix},
		},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}

	withAudioNode, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (audio-01): %v", err)
	}
	if len(withAudioNode.Entries) != 1 {
		t.Fatalf("ResolveCueCatalog (audio-01) entries = %+v, want exactly one", withAudioNode.Entries)
	}
	out := withAudioNode.Entries[0].Outputs
	if out.Audio == nil || out.LTC == nil || out.Announcement == nil {
		t.Fatalf("ResolveCueCatalog (audio-01) outputs = %+v, want audio/ltc/announcement all present (audio-01 has an audio.node object)", out)
	}

	withoutAudioNode, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (render-01): %v", err)
	}
	if len(withoutAudioNode.Entries) != 1 {
		t.Fatalf("ResolveCueCatalog (render-01) entries = %+v, want exactly one", withoutAudioNode.Entries)
	}
	out2 := withoutAudioNode.Entries[0].Outputs
	if out2.Audio != nil || out2.LTC != nil || out2.Announcement != nil {
		t.Fatalf("ResolveCueCatalog (render-01) outputs = %+v, want audio/ltc/announcement all absent (render-01 has no audio.node object)", out2)
	}

	if withAudioNode.Revision == withoutAudioNode.Revision {
		t.Fatalf("ResolveCueCatalog: audio-01 (holds an audio.node object) and render-01 (does not) computed the identical revision %q", withAudioNode.Revision)
	}
}

// TestCueCatalogAudioOnlyCueYieldsEmptyOutputsForNodeWithNoAudioNode pins
// this package's existing behavior for a Cue whose every declared output
// is scoped away from a node: the Cue still becomes a catalog Entry (with
// an empty Outputs), matching how a render-only Cue already behaves for a
// node holding no show.surface — resolveCueOutputs narrows each output
// field independently and this file's loop never drops an Entry for
// having zero resolved outputs.
func TestCueCatalogAudioOnlyCueYieldsEmptyOutputsForNodeWithNoAudioNode(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "render-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if len(catalog.Entries) != 1 {
		t.Fatalf("ResolveCueCatalog entries = %+v, want exactly one (the Cue is still a catalog entry, with empty outputs)", catalog.Entries)
	}
	out := catalog.Entries[0].Outputs
	if out.Audio != nil || out.LTC != nil || out.Announcement != nil || out.Render != nil {
		t.Fatalf("ResolveCueCatalog entries[0].Outputs = %+v, want every output absent (render-01 has neither a surface nor an audio.node object)", out)
	}
}
