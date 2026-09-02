package assetsync

import (
	"context"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file is TRACK-H-cues-and-playlists.md section H5's own resolver for
// the narrow `showmesh-audio` runner: it resolves a `show.playlist` whose
// Runner is [config.ShowPlaylistRunnerShowmeshAudio] into ONE
// [pkgaudio.PlaylistRef] — the playlist's entries in order, each entry's
// Cue's resolved audio asset from the deployed catalog (reusing
// [ExpectedAssetsForNode], never a second asset lookup). A caller Applies
// the returned ref exactly once, against [cueactivation.BackgroundSessionID]
// — this package never dispatches anything itself (assetsync must never
// import internal/coordinator/api or vice versa) — and the node's own
// Manager.RunWatcher (internal/agent/audio/restore.go) advances it,
// item by item, from there. There is no per-item apply and no
// item-completion polling here: TRACK-H-cues-and-playlists.md section H5's own ruling 1 is that doing
// either would be a fourth copy of logic pkg/audio already owns.

// showmeshAudioRepeat maps show.playlist.showmeshAudio.repeat
// ("none"/"all", showplaylist.go's own closed vocabulary) onto
// [pkgaudio.RepeatMode]. "all" means the WHOLE playlist repeats
// ([pkgaudio.RepeatPlaylist]) — show.playlist has no per-item repeat
// concept, so [pkgaudio.RepeatItem] is never produced here.
func showmeshAudioRepeat(repeat string) pkgaudio.RepeatMode {
	if repeat == config.ShowPlaylistShowmeshAudioRepeatAll {
		return pkgaudio.RepeatPlaylist
	}
	return pkgaudio.RepeatNone
}

// resolveAssetForWithSize is [resolveAssetFor] plus the one field it does
// not resolve (SizeBytes); that function's own signature carries the same
// mediaType filter because internal/agent/cueactivationrender.go's
// ExpectedAsset consumer (and ResolveCueCatalog's own resolveCueOutputs)
// never needed size, but [pkgaudio.MediaRef] is a real field a node's
// engine may use, so this seam's own PlaylistItem is not built with it
// silently left zero when a real value is available.
func resolveAssetForWithSize(assetsBySequence map[string][]ExpectedAsset, sequenceID, mediaType string) (filename, contentHash string, sizeBytes int64) {
	filename, hashes := resolveAssetFor(assetsBySequence, sequenceID, mediaType)
	if len(hashes) == 0 {
		return "", "", 0
	}
	contentHash = hashes[0]
	for _, a := range assetsBySequence[sequenceID] {
		if a.ContentHash == contentHash && a.MediaType == mediaType {
			return filename, contentHash, a.SizeBytes
		}
	}
	return filename, contentHash, 0
}

// ResolveShowmeshAudioPlaylistRef resolves p (a show.playlist whose Runner
// is "showmesh-audio", already validated at write time — H1 spec section
// 4) into ONE [pkgaudio.PlaylistRef] for nodeID: ownerID/ownerRevision pin
// p's own config identity (mirroring [pkgaudio.PlaylistRef]'s own
// immutable-owner-identity contract), and every entry's referenced Cue
// MUST declare an `audio` output (H0.4's "outputs.announcement requires
// outputs.audio" sibling rule doesn't apply here — showmesh-audio entries
// are never announcements — but every playlist entry's Cue still has to
// have SOMETHING to play). A Cue with no resolved asset, or no `audio`
// output at all, fails the WHOLE resolution rather than silently dropping
// one item or playing a wrong one — AUDIO-ENGINE's own "fails visibly
// instead of guessing" rule, applied here the same way
// nightBuildBackgroundPlaylistItems applies it one seam over.
func ResolveShowmeshAudioPlaylistRef(ctx context.Context, st *store.Store, showID, nodeID, ownerID string, ownerRevision int64, p config.ShowPlaylistPayload) (pkgaudio.PlaylistRef, error) {
	if p.Runner != config.ShowPlaylistRunnerShowmeshAudio {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: runner is %q, not %q", ownerID, p.Runner, config.ShowPlaylistRunnerShowmeshAudio)
	}
	if len(p.Entries) == 0 {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: has no entries", ownerID)
	}

	expected, err := ExpectedAssetsForNode(ctx, st, showID, nodeID)
	if err != nil {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: %w", ownerID, err)
	}
	assetsBySequence := make(map[string][]ExpectedAsset, len(expected.Assets))
	for _, a := range expected.Assets {
		assetsBySequence[a.SequenceID] = append(assetsBySequence[a.SequenceID], a)
	}

	items := make([]pkgaudio.PlaylistItem, 0, len(p.Entries))
	for i, entry := range p.Entries {
		cue, err := loadShowCuePayload(ctx, st, entry.Cue)
		if err != nil {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: entry %q: %w", ownerID, entry.ID, err)
		}
		if cue.Show != showID {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: entry %q: cue %q belongs to a different show", ownerID, entry.ID, entry.Cue)
		}
		if cue.Outputs.Audio == nil {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: entry %q: cue %q declares no audio output", ownerID, entry.ID, entry.Cue)
		}
		if cue.Outputs.LTC != nil {
			// TRACK-H-cues-and-playlists.md section H5 build item 5's own
			// ruling: refuse this combination visibly rather than silently
			// dropping it. A showmesh-audio background session has no LTC
			// generator to run from — ADR-018 ties the node's one LTC
			// generator to the program-audio clock domain, which is the
			// show session (cueactivation.AudioSessionID), never
			// [cueactivation.BackgroundSessionID]. Detected here, at the
			// point this playlist's own resolution is what would otherwise
			// discard the declared ltc output.
			return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: entry %q: cue %q declares an ltc output, but a showmesh-audio background session has no LTC generator (ADR-018); refusing rather than silently dropping it", ownerID, entry.ID, entry.Cue)
		}
		filename, contentHash, sizeBytes := resolveAssetForWithSize(assetsBySequence, cue.Outputs.Audio.Asset, "audio")
		if filename == "" || contentHash == "" {
			return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: entry %q: cue %q resolves asset %q to no runtime filename (no matching asset uploaded)", ownerID, entry.ID, entry.Cue, cue.Outputs.Audio.Asset)
		}
		items = append(items, pkgaudio.PlaylistItem{
			ItemID: entry.ID, Index: i,
			Media: pkgaudio.MediaRef{
				AssetID: cue.Outputs.Audio.Asset, ContentHash: contentHash,
				SizeBytes: sizeBytes, RuntimeFilename: filename,
			},
		})
	}

	repeat := pkgaudio.RepeatNone
	if p.ShowmeshAudio != nil {
		repeat = showmeshAudioRepeat(p.ShowmeshAudio.Repeat)
	}

	ref := pkgaudio.PlaylistRef{
		OwnerKind: config.ShowPlaylistConfigKind, OwnerID: ownerID, OwnerRevision: pkgaudio.Revision(ownerRevision),
		Items: items, Repeat: repeat,
		// Resume/RequestedTransition: show.playlist has no authored
		// field for either (H1 spec section 3 — only `repeat` exists
		// under showmeshAudio), so this resolver picks the same
		// defaults [pkgaudio.PlaylistRef.Validate] would otherwise
		// refuse a zero-value ref for: Restart (a fresh Apply always
		// starts a background bed over at item 0, matching this seam's
		// own "Apply it once" posture — there is no coordinator-tracked
		// bookmark to resume from) and Sequential (the only transition
		// every output confirms without capability evidence this
		// resolver does not have access to).
		Resume: pkgaudio.ResumePolicyRestart, RequestedTransition: pkgaudio.ItemTransitionSequential,
	}
	if err := ref.Validate(); err != nil {
		return pkgaudio.PlaylistRef{}, fmt.Errorf("assetsync: resolve showmesh-audio playlist %q: %w", ownerID, err)
	}
	return ref, nil
}

// loadShowCuePayload reads and decodes cueID's current show.cue revision.
// Shared by this file and this package's own claim-conflict detection in
// cuecatalog.go, so a Cue is never decoded two independently written ways.
func loadShowCuePayload(ctx context.Context, st *store.Store, cueID string) (config.ShowCuePayload, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if err != nil {
		return config.ShowCuePayload{}, fmt.Errorf("read cue %q: %w", cueID, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
	if err != nil {
		return config.ShowCuePayload{}, fmt.Errorf("read cue %q revision %d: %w", cueID, obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeShowCuePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
	if verr != nil {
		return config.ShowCuePayload{}, fmt.Errorf("decode cue %q: %s", cueID, verr.Detail)
	}
	return payload, nil
}

// ListShowmeshAudioPlaylists returns every current-revision show.playlist
// in showID whose Runner is "showmesh-audio", sorted by object id for
// deterministic iteration. A caller that finds more than one is expected
// to log and pick the first (there is exactly one background audio
// session per node — [cueactivation.BackgroundSessionID] — so more than
// one authored showmesh-audio playlist in one Show is an authoring state
// this seam does not refuse but also does not attempt to run
// concurrently).
func ListShowmeshAudioPlaylists(ctx context.Context, st *store.Store, showID string) ([]store.ConfigObjectRecord, map[string]config.ShowPlaylistPayload, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, nil, fmt.Errorf("assetsync: list showmesh-audio playlists: list show.playlist objects: %w", err)
	}
	var out []store.ConfigObjectRecord
	payloads := make(map[string]config.ShowPlaylistPayload)
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, nil, fmt.Errorf("assetsync: list showmesh-audio playlists: read %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, err := decodeStoredShowPlaylistPayload(rev.PayloadJSON)
		if err != nil {
			return nil, nil, fmt.Errorf("assetsync: list showmesh-audio playlists: decode %q: %w", obj.ID, err)
		}
		if payload.Show != showID || payload.Runner != config.ShowPlaylistRunnerShowmeshAudio {
			continue
		}
		out = append(out, obj)
		payloads[obj.ID] = payload
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, payloads, nil
}
