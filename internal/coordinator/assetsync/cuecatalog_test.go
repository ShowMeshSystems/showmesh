package assetsync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
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

// cueOutputsByID returns cueID's own Outputs out of entries, if present.
func cueOutputsByID(entries []cuecatalog.Entry, cueID string) (cuecatalog.Outputs, bool) {
	for _, e := range entries {
		if e.CueID == cueID {
			return e.Outputs, true
		}
	}
	return cuecatalog.Outputs{}, false
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
	// "thriller" (audio + ltc) and "cue-ann" (audio + announcement) are
	// deliberately two SEPARATE Cues, never one Cue declaring both: TRACK-
	// H-cues-and-playlists.md section H5 build item 5's own authoring-time
	// refusal (config/showcue.go's decodeShowCueOutputs) now rejects a
	// Cue that declares both ltc and announcement — a node has one LTC
	// generator, tied to the program-audio clock domain, and the
	// announcement session is not that domain.
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name: "Thriller",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"},
			LTC:   &config.ShowCueLTCOutput{},
		},
	})
	putCue(t, st, "cue-ann", "halloween-2026", config.ShowCuePayload{
		Name: "Announcement",
		Outputs: config.ShowCueOutputs{
			Audio:        &config.ShowCueAudioOutput{Asset: "ann-audio"},
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
	// cue-ann is included even though no Playlist references it (build
	// item 7: a Cue declaring `announcement` is directly activatable).
	if len(withAudioNode.Entries) != 2 {
		t.Fatalf("ResolveCueCatalog (audio-01) entries = %+v, want exactly two (thriller + cue-ann)", withAudioNode.Entries)
	}
	thrillerOut, ok := cueOutputsByID(withAudioNode.Entries, "thriller")
	if !ok || thrillerOut.Audio == nil || thrillerOut.LTC == nil {
		t.Fatalf("ResolveCueCatalog (audio-01) thriller outputs = %+v, want audio and ltc present (audio-01 has an audio.node object)", thrillerOut)
	}
	annOut, ok := cueOutputsByID(withAudioNode.Entries, "cue-ann")
	if !ok || annOut.Audio == nil || annOut.Announcement == nil {
		t.Fatalf("ResolveCueCatalog (audio-01) cue-ann outputs = %+v, want audio and announcement present (audio-01 has an audio.node object)", annOut)
	}

	withoutAudioNode, err := ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (render-01): %v", err)
	}
	if len(withoutAudioNode.Entries) != 2 {
		t.Fatalf("ResolveCueCatalog (render-01) entries = %+v, want exactly two (thriller + cue-ann, both with no audio/ltc/announcement outputs)", withoutAudioNode.Entries)
	}
	out2, ok := cueOutputsByID(withoutAudioNode.Entries, "thriller")
	if !ok || out2.Audio != nil || out2.LTC != nil || out2.Announcement != nil {
		t.Fatalf("ResolveCueCatalog (render-01) thriller outputs = %+v, want audio/ltc/announcement all absent (render-01 has no audio.node object)", out2)
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

// TestCueCatalogAudioOutputResolvesHashesBySequenceIDNotAssetRowID proves
// build item 4's own question: outputs.audio.asset names the SAME identity
// space AssetRecord.SequenceID does (every asset, audio or render, is
// uploaded under a "sequence" parameter regardless of MediaType — see
// resolveCueOutputs' own comment on the Audio branch), not
// AssetRecord.ID. A real stored audio asset is created with a SequenceID
// equal to the Cue's outputs.audio.asset value and a content hash distinct
// from its own store-generated row id, so a lookup that used AssetID
// instead of SequenceID would find nothing and this test would see an
// empty AssetHashes.
func TestCueCatalogAudioOutputResolvesHashesBySequenceIDNotAssetRowID(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")

	const contentHash = "deadbeef00000000000000000000000000000000000000000000000000ab"
	rec := createAsset(t, st, "halloween-2026", "thriller-audio", store.AssetTargetKindShow, "", contentHash, "thriller-audio.wav")
	if rec.ID == contentHash {
		t.Fatalf("test fixture's asset row id %q must differ from its content hash %q, or this test cannot distinguish a SequenceID lookup from an AssetID lookup", rec.ID, contentHash)
	}

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	catalog, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if len(catalog.Entries) != 1 {
		t.Fatalf("ResolveCueCatalog entries = %+v, want exactly one", catalog.Entries)
	}
	out := catalog.Entries[0].Outputs
	if out.Audio == nil {
		t.Fatalf("ResolveCueCatalog entries[0].Outputs.Audio is nil, want set")
	}
	if len(out.Audio.AssetHashes) != 1 || out.Audio.AssetHashes[0] != contentHash {
		t.Fatalf("ResolveCueCatalog entries[0].Outputs.Audio.AssetHashes = %+v, want [%q] (looked up by SequenceID %q, not AssetID %q)",
			out.Audio.AssetHashes, contentHash, "thriller-audio", rec.ID)
	}
	if out.Audio.Filename != "thriller-audio.wav" {
		t.Fatalf("ResolveCueCatalog entries[0].Outputs.Audio.Filename = %q, want %q (the runtime filename a node must actually open, not the logical asset id)",
			out.Audio.Filename, "thriller-audio.wav")
	}
}

// cueCatalogDeployWireParamsForTest mirrors internal/agent/cuecatalogops.go's
// own catalogDeployWireParams field for field and tag for tag AND
// internal/coordinator/api/cuecatalogdeploy.go's cueCatalogDeployWireParams
// — this package cannot import either (internal/agent must never import
// internal/coordinator, and this file already lives in
// internal/coordinator/assetsync), so it is independently reproduced a
// third time, matching this project's standing each-side-of-a-wire-
// boundary-decodes-independently convention.
type cueCatalogDeployWireParamsForTest struct {
	Show       string             `json:"show"`
	Generation int64              `json:"generation"`
	Revision   string             `json:"revision"`
	Entries    []cuecatalog.Entry `json:"entries"`
}

// TestCueCatalogRevisionAgreesAfterWireRoundTrip is this build item's own
// cross-boundary regression coverage. The reviewer's finding: every prior
// revision-agreement test (both here and internal/agent/cuecatalogops_
// test.go's computeExpectedRevision) computes its "expected" value from
// entries the SAME test hand-authored, which proves nothing about whether
// a REAL resolved catalog survives the actual cuecatalog.deploy wire
// shape unchanged. internal/agent must never import internal/coordinator
// (pkg/cuecatalog's own doc comment), so this file cannot call the
// agent's catalogDeployOperation.deploy directly — a real coordinator
// resolution IS crossed here as far as this project's own layering rule
// allows: [ResolveCueCatalog]'s real output is marshaled through the
// EXACT wire shape cuecatalog.deploy carries (cueCatalogDeployWireParamsForTest,
// built from the real cuecatalog.Entry values ResolveCueCatalog produced,
// never re-authored by hand) and decoded back with encoding/json — the
// same decode internal/agent/cuecatalogops.go's deploy operation performs
// — before recomputing the revision via [cuecatalog.ComputeRevision], the
// ONE function both sides call. Agreement here rules out every wire/
// marshalling-shaped disagreement (JSON number precision, field
// omission, nil-vs-empty-slice, sort-order-sensitive canonicalization);
// what it cannot rule out is a defect in internal/agent's own Go code
// path, which is why cuecatalogops_test.go's own suite (against fixed,
// hand-authored entries) remains that package's job to cover.
//
// Covers all three of this build item's required shapes in one
// resolution each: media-01 (holds both a surface and an audio.node)
// gets a render Cue whose sequence has TWO current assets (a node-
// targeted upload and a show-targeted upload with different bytes, so
// AssetHashes has two DISTINCT non-empty entries) and an announcement Cue
// with a non-nil duckGainDb; quiet-01 (holds neither) gets an audio-only
// Cue that resolves to a catalog Entry with every output field nil.
func TestCueCatalogRevisionAgreesAfterWireRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "media-01")
	declareNode(t, st, "quiet-01")
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putSurface(t, st, "wall", "halloween-2026", "media-01")
	putAudioNode(t, st, "media-01")

	putCue(t, st, "opener", "halloween-2026", config.ShowCuePayload{
		Name:    "Opener",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "opener"}},
	})
	duckGain := -6.0
	putCue(t, st, "announce", "halloween-2026", config.ShowCuePayload{
		Name: "Announce",
		Outputs: config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "announce-track"},
			Announcement: &config.ShowCueAnnouncementOutput{
				Policy: config.ShowCueAnnouncementPolicyDuck, DuckGainDb: &duckGain, FadeMillis: 250,
			},
		},
	})
	putCue(t, st, "silent-elsewhere", "halloween-2026", config.ShowCuePayload{
		Name:    "Silent Elsewhere",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "background-track"}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "opener", "announce", "silent-elsewhere"))
	putActiveShow(t, st, "halloween-2026")

	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}

	// Two DISTINCT current assets for sequence "opener": one node-
	// targeted, one show-targeted, deliberately different content so
	// AssetHashes ends up with two entries, not one deduplicated one.
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	createAsset(t, st, "halloween-2026", "opener", store.AssetTargetKindNode, "media-01", hashA, "opener-node.fseq")
	createAsset(t, st, "halloween-2026", "opener", store.AssetTargetKindShow, "", hashB, "opener-show.fseq")

	mediaCatalog, err := ResolveCueCatalog(ctx, st, active, "media-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (media-01): %v", err)
	}
	if len(mediaCatalog.Entries) != 3 {
		t.Fatalf("media-01 catalog entries = %+v, want 3 (opener, announce, silent-elsewhere)", mediaCatalog.Entries)
	}
	assertRoundTripRevisionAgrees(t, mediaCatalog)

	var openerEntry, announceEntry cuecatalog.Entry
	for _, e := range mediaCatalog.Entries {
		switch e.CueID {
		case "opener":
			openerEntry = e
		case "announce":
			announceEntry = e
		}
	}
	if openerEntry.Outputs.Render == nil || len(openerEntry.Outputs.Render.AssetHashes) != 2 {
		t.Fatalf("opener entry render output = %+v, want exactly 2 asset hashes", openerEntry.Outputs.Render)
	}
	gotHashes := map[string]bool{openerEntry.Outputs.Render.AssetHashes[0]: true, openerEntry.Outputs.Render.AssetHashes[1]: true}
	if !gotHashes[hashA] || !gotHashes[hashB] {
		t.Fatalf("opener entry render asset hashes = %+v, want %q and %q in some order", openerEntry.Outputs.Render.AssetHashes, hashA, hashB)
	}
	// hashA sorts first ("aaaa..." < "bbbb..."), so the paired runtime
	// filename must be the node-targeted upload's own filename, never the
	// empty string or a filename paired with the OTHER hash.
	if openerEntry.Outputs.Render.Filename != "opener-node.fseq" {
		t.Fatalf("opener entry render filename = %q, want %q (paired with the alphabetically-first hash %q)",
			openerEntry.Outputs.Render.Filename, "opener-node.fseq", hashA)
	}
	if announceEntry.Outputs.Announcement == nil || announceEntry.Outputs.Announcement.DuckGainDb == nil || *announceEntry.Outputs.Announcement.DuckGainDb != duckGain {
		t.Fatalf("announce entry announcement output = %+v, want DuckGainDb = %v", announceEntry.Outputs.Announcement, duckGain)
	}

	quietCatalog, err := ResolveCueCatalog(ctx, st, active, "quiet-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (quiet-01): %v", err)
	}
	if len(quietCatalog.Entries) != 3 {
		t.Fatalf("quiet-01 catalog entries = %+v, want 3", quietCatalog.Entries)
	}
	assertRoundTripRevisionAgrees(t, quietCatalog)
	for _, e := range quietCatalog.Entries {
		if e.CueID != "silent-elsewhere" {
			continue
		}
		if e.Outputs.Render != nil || e.Outputs.Audio != nil || e.Outputs.LTC != nil || e.Outputs.Announcement != nil {
			t.Fatalf("quiet-01's silent-elsewhere entry outputs = %+v, want every field nil (quiet-01 holds neither a surface nor an audio.node object)", e.Outputs)
		}
	}
}

// assertRoundTripRevisionAgrees marshals catalog through the real
// cuecatalog.deploy wire shape, decodes it back, recomputes the revision
// via [cuecatalog.ComputeRevision] against the round-tripped entries, and
// fails t unless it equals catalog.Revision exactly.
func assertRoundTripRevisionAgrees(t *testing.T, catalog Catalog) {
	t.Helper()
	raw, err := json.Marshal(cueCatalogDeployWireParamsForTest{
		Show: catalog.Show, Generation: catalog.Generation, Revision: catalog.Revision, Entries: catalog.Entries,
	})
	if err != nil {
		t.Fatalf("marshal cuecatalog.deploy wire params: %v", err)
	}
	var wire cueCatalogDeployWireParamsForTest
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode cuecatalog.deploy wire params: %v", err)
	}
	recomputed, err := cuecatalog.ComputeRevision(cuecatalog.RevisionInput{
		Show: wire.Show, Generation: wire.Generation, Node: catalog.Node, Entries: wire.Entries,
	})
	if err != nil {
		t.Fatalf("recompute revision after wire round trip: %v", err)
	}
	if recomputed != catalog.Revision {
		t.Fatalf("revision recomputed after a wire round trip = %q, want the original resolved revision %q (node %q)", recomputed, catalog.Revision, catalog.Node)
	}
}
