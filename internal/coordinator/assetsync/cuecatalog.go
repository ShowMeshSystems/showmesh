package assetsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file is TRACK-H-H3-SPEC.md section 3's own resolver, beside the
// manifest builder above: [ResolveCueCatalog] is the single authority on
// what a node is allowed to execute for the active Show at one generation,
// the same posture [ComputeNodeManifest] holds for asset readiness. Every
// consumer (the HTTP read route, a future H4 activation check) calls this
// one function; nothing else derives a catalog a second way.

// ResolveCueCatalog resolves the Cue catalog nodeID may execute for
// active's Show, per H3 spec section 3:
//
//  1. Collect every show.cue this Show's show.playlist objects reference
//     (an entry's cue, or a "safeCue" mismatchPolicy's safeCueRef). A Cue
//     no Playlist references and no policy names is a draft and is never
//     included (step 2).
//  2. For each referenced Cue, resolve only the outputs that concern
//     nodeID: render.sequence when nodeID holds at least one show.surface
//     in this Show, and audio/ltc/announcement when nodeID has an
//     audio.node object (see this function's own doc comment further
//     down).
//  3. Attach asset content hashes via [ExpectedAssetsForNode] — reused,
//     never re-derived.
//  4. Compute the catalog revision via [cuecatalog.ComputeRevision], the
//     one function a node computes the identical value with.
//
// active MUST already be Configured — this function does not itself
// consult show.active, matching [ExpectedAssetsForNode]'s identical
// precondition. Calling it against an unconfigured active is a caller
// bug, refused with an error rather than silently returning an empty
// catalog that could be mistaken for "this Show simply has no Cues yet".
//
// Render is scoped by show.surface assignment (a surface names its own
// node); audio, ltc, and announcement are scoped by the presence of an
// audio.node object whose id is this node (ADR-018, ADR-039): a node with
// no audio.node object has no program-audio or LTC route at all.
func ResolveCueCatalog(ctx context.Context, st *store.Store, active ActiveShow, nodeID string) (Catalog, error) {
	if !active.Configured {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: active show is not configured; an unconfigured show.active authorizes nothing")
	}
	showID := active.ShowID

	referencedCues, err := referencedCueIDs(ctx, st, showID)
	if err != nil {
		return Catalog{}, err
	}

	expected, err := ExpectedAssetsForNode(ctx, st, showID, nodeID)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: %w", err)
	}
	assetsBySequence := make(map[string][]ExpectedAsset, len(expected.Assets))
	for _, a := range expected.Assets {
		assetsBySequence[a.SequenceID] = append(assetsBySequence[a.SequenceID], a)
	}

	surfaceIDs, err := surfaceIDsForNode(ctx, st, showID, nodeID)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: %w", err)
	}
	nodeHasSurface := len(surfaceIDs) > 0

	nodeHasAudioNode, err := hasAudioNode(ctx, st, nodeID)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: %w", err)
	}

	cueObjs, err := st.ListConfigObjects(ctx, config.ShowCueConfigKind)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: list show.cue objects: %w", err)
	}

	entries := make([]cuecatalog.Entry, 0, len(referencedCues))
	for _, obj := range cueObjs {
		if !referencedCues[obj.ID] {
			// Not referenced by any Playlist entry or safeCueRef in this
			// Show: a draft. Deploying drafts is how a half-authored Cue
			// would reach a node (H3 spec section 3, point 2).
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: read show.cue %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeShowCuePayload(rev.PayloadJSON, alwaysTrue)
		if verr != nil {
			return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: decode stored show.cue %q: %s", obj.ID, verr.Detail)
		}
		if payload.Show != showID {
			// A Cue id referenced by a same-show Playlist can only ever
			// belong to that Playlist's own Show (config.go's own
			// cross-show write-time check), so this branch is defensive,
			// not a real case — never silently included regardless.
			continue
		}

		entries = append(entries, cuecatalog.Entry{
			CueID:       obj.ID,
			CueRevision: obj.CurrentRevision,
			Outputs:     resolveCueOutputs(payload, nodeHasSurface, nodeHasAudioNode, assetsBySequence),
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].CueID < entries[j].CueID })

	revision, err := cuecatalog.ComputeRevision(cuecatalog.RevisionInput{
		Show: showID, Generation: active.Generation, Node: nodeID, Entries: entries,
	})
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: compute revision: %w", err)
	}

	return Catalog{
		Show: showID, Generation: active.Generation, Node: nodeID,
		Entries: entries, Revision: revision,
	}, nil
}

// Catalog is [ResolveCueCatalog]'s result: one node's resolved Cue
// catalog for the active Show at one generation, plus its own revision.
type Catalog struct {
	Show       string
	Generation int64
	Node       string
	Entries    []cuecatalog.Entry
	Revision   string
}

// resolveCueOutputs projects payload's declared outputs onto
// [cuecatalog.Outputs], restricted per this file's own doc comment.
func resolveCueOutputs(payload config.ShowCuePayload, nodeHasSurface, nodeHasAudioNode bool, assetsBySequence map[string][]ExpectedAsset) cuecatalog.Outputs {
	var out cuecatalog.Outputs

	if payload.Outputs.Render != nil && nodeHasSurface {
		filename, hashes := resolveAssetFor(assetsBySequence, payload.Outputs.Render.Sequence)
		out.Render = &cuecatalog.RenderOutput{
			Sequence:    payload.Outputs.Render.Sequence,
			Filename:    filename,
			AssetHashes: hashes,
		}
	}
	if payload.Outputs.Audio != nil && nodeHasAudioNode {
		// assetsBySequence is keyed by AssetRecord.SequenceID, and
		// payload.Outputs.Audio.Asset IS that same identity, not
		// AssetRecord.ID: every asset, audio or render, is uploaded
		// under a "sequence" parameter (internal/coordinator/api/
		// assets.go's fields.sequence) that becomes SequenceID
		// regardless of MediaType, so an audio Cue output names the
		// same identity space a render Cue output's Sequence does —
		// only ShowCueAudioOutput's own field is called "asset" rather
		// than "sequence".
		filename, hashes := resolveAssetFor(assetsBySequence, payload.Outputs.Audio.Asset)
		out.Audio = &cuecatalog.AudioOutput{
			Asset:             payload.Outputs.Audio.Asset,
			Filename:          filename,
			StartOffsetMillis: payload.Outputs.Audio.StartOffsetMillis,
			AssetHashes:       hashes,
		}
	}
	if payload.Outputs.LTC != nil && nodeHasAudioNode {
		out.LTC = &cuecatalog.LTCOutput{StartOffsetMillis: payload.Outputs.LTC.StartOffsetMillis}
	}
	if payload.Outputs.Announcement != nil && nodeHasAudioNode {
		out.Announcement = &cuecatalog.AnnouncementOutput{
			Policy:     payload.Outputs.Announcement.Policy,
			DuckGainDb: payload.Outputs.Announcement.DuckGainDb,
			FadeMillis: payload.Outputs.Announcement.FadeMillis,
		}
	}
	return out
}

// hasAudioNode reports whether nodeID has a current audio.node object —
// an audio.node object id IS the node id ([config.ValidateAudioNodeObjectID]),
// so existence alone (not its payload) is what gates audio/ltc/announcement
// output inclusion.
func hasAudioNode(ctx context.Context, st *store.Store, nodeID string) (bool, error) {
	_, err := st.GetConfigObject(ctx, config.AudioNodeConfigKind, nodeID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get audio.node %q: %w", nodeID, err)
	}
	return true, nil
}

// resolveAssetFor returns the sorted, de-duplicated content hashes of
// every asset assetsBySequence holds for sequenceID (never nil, so two
// callers resolving an output with no matching asset yet — nothing
// uploaded — agree on an empty array rather than one producing null and
// the other []: [cuecatalog.RevisionInput]'s own determinism requirement),
// plus the ONE runtime filename a node must actually open: the filename
// paired with hashes[0] (the alphabetically-first content hash), the same
// hash [firstAssetHash] in internal/agent/cueactivationrender.go picks
// when it later verifies that file. Pairing filename and hash from the
// SAME underlying [ExpectedAsset] row is what a node's own "open by
// filename, then verify the opened file's hash" convention
// (renderApplyParamsPayload's own established shape) requires — a
// filename resolved independently of which hash it is meant to
// corroborate would let the two silently drift apart. The ordinary case
// is exactly one asset per sequence per node; a sequence with more than
// one current asset (a node-targeted upload alongside a show-wide one)
// still resolves deterministically, picking whichever asset's hash sorts
// first.
func resolveAssetFor(assetsBySequence map[string][]ExpectedAsset, sequenceID string) (filename string, hashes []string) {
	assets := assetsBySequence[sequenceID]
	filenameByHash := make(map[string]string, len(assets))
	hashes = make([]string, 0, len(assets))
	for _, a := range assets {
		if _, seen := filenameByHash[a.ContentHash]; seen {
			continue
		}
		filenameByHash[a.ContentHash] = a.Filename
		hashes = append(hashes, a.ContentHash)
	}
	sort.Strings(hashes)
	if len(hashes) > 0 {
		filename = filenameByHash[hashes[0]]
	}
	return filename, hashes
}

// referencedCueIDs returns every show.cue id that some show.playlist in
// showID references, either as an entry's cue or a "safeCue"
// mismatchPolicy's safeCueRef (H3 spec section 3, point 2).
func referencedCueIDs(ctx context.Context, st *store.Store, showID string) (map[string]bool, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: list show.playlist objects: %w", err)
	}

	referenced := make(map[string]bool)
	for _, obj := range objs {
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("assetsync: read show.playlist %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, err := decodeStoredShowPlaylistPayload(rev.PayloadJSON)
		if err != nil {
			return nil, fmt.Errorf("assetsync: decode stored show.playlist %q: %w", obj.ID, err)
		}
		if payload.Show != showID {
			continue
		}
		for _, entry := range payload.Entries {
			referenced[entry.Cue] = true
		}
		if payload.MismatchPolicy == config.ShowPlaylistMismatchPolicySafeCue && payload.SafeCueRef != "" {
			referenced[payload.SafeCueRef] = true
		}
	}
	return referenced, nil
}

// showFieldOnly is the minimal shape this file needs to read a stored
// show.playlist payload's own "show" field BEFORE decoding it fully, so
// [decodeStoredShowPlaylistPayload]'s resolveCue stub can answer
// [config.DecodeShowPlaylistPayload]'s cross-show check with the
// payload's own show rather than re-deriving or re-validating a
// reference this package never fetches a second way — see
// [alwaysTrue]'s own doc comment for why re-running a persisted,
// already-valid revision's own cross-reference checks here would be
// wrong, not merely redundant.
type showFieldOnly struct {
	Show string `json:"show"`
}

// decodeStoredShowPlaylistPayload decodes a persisted, already-validated
// show.playlist revision. showExists and resolveCue are both stubbed to
// accept whatever the payload itself already claims (alwaysTrue for
// showExists; resolveCue answers every cue id with the payload's own
// show), for the identical reason [alwaysTrue] exists: this package only
// ever re-reads a row that was valid when written, and re-running its
// cross-reference checks against a persisted row would reject a payload
// that was valid at write time.
func decodeStoredShowPlaylistPayload(raw string) (config.ShowPlaylistPayload, error) {
	var sf showFieldOnly
	if err := json.Unmarshal([]byte(raw), &sf); err != nil {
		return config.ShowPlaylistPayload{}, fmt.Errorf("decode show field: %w", err)
	}
	resolveCue := func(string) (string, bool) { return sf.Show, true }
	payload, verr := config.DecodeShowPlaylistPayload(raw, alwaysTrue, resolveCue)
	if verr != nil {
		return config.ShowPlaylistPayload{}, fmt.Errorf("%s", verr.Detail)
	}
	return payload, nil
}
