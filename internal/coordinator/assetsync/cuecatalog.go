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
//     (an entry's cue, or a "safeCue" mismatchPolicy's safeCueRef), PLUS
//     every show.cue that declares the `announcement` output (H0.4: "An
//     announcement Cue... is directly activatable and is not required to
//     be a Playlist entry" — such a Cue is not a draft merely because no
//     Playlist references it). A Cue neither referenced nor declaring
//     announcement is a draft and is never included (step 2).
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

	audioNode, nodeHasAudioNode, err := loadAudioNodePayload(ctx, st, nodeID)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: %w", err)
	}

	cueObjs, err := st.ListConfigObjects(ctx, config.ShowCueConfigKind)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: list show.cue objects: %w", err)
	}
	// Sorted for a deterministic pass: [detectClaimConflicts] registers
	// claims into a shared map in loop order, and the FIRST cue to claim a
	// resource becomes [claimant] "holder" the SECOND one is reported
	// against — st.ListConfigObjects makes no ordering guarantee, so
	// without this sort the reported conflict PAIR (which one is named
	// "holder" versus the new claimant) would depend on store iteration
	// order rather than being a stable fact about the catalog.
	sort.Slice(cueObjs, func(i, j int) bool { return cueObjs[i].ID < cueObjs[j].ID })

	referencingPlaylists, err := cueReferencingPlaylistIDs(ctx, st, showID)
	if err != nil {
		return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: %w", err)
	}

	// A program-only audio.node declares no LTC route at all, so
	// having an audio.node no longer implies being able to emit LTC. LTC
	// scoping, LTC output projection, and the LTC claim context all key
	// off this narrower fact; Audio and Announcement still key off the
	// audio.node's mere existence.
	nodeHasLTC := nodeHasAudioNode && audioNode.LTCRoute != ""

	claimCtx := config.ShowCueClaimContext{
		RenderSurfaceIDs: surfaceIDs,
	}
	if nodeHasAudioNode {
		claimCtx.ProgramAudioNode, claimCtx.ProgramAudioRoute = nodeID, audioNode.ProgramRoute
		claimCtx.AnnouncementNode = nodeID
	}
	if nodeHasLTC {
		claimCtx.LTCNode, claimCtx.LTCRoute = nodeID, audioNode.LTCRoute
	}
	claimants := make(map[config.ShowCueClaim]claimant)
	var conflicts []CatalogConflict

	entries := make([]cuecatalog.Entry, 0, len(referencedCues))
	for _, obj := range cueObjs {
		rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return Catalog{}, fmt.Errorf("assetsync: resolve cue catalog: read show.cue %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeShowCuePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
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

		// Included when a Playlist entry or a "safeCue" safeCueRef
		// references this Cue, OR when it declares the `announcement`
		// output — H0.4: "An announcement Cue... is directly activatable
		// and is not required to be a Playlist entry." A Cue that
		// declares announcement is not a draft even with zero Playlist
		// references; anything else with zero references is a draft, and
		// deploying drafts is how a half-authored Cue would reach a node
		// (H3 spec section 3, point 2).
		directlyActivatableAnnouncement := payload.Outputs.Announcement != nil
		if !referencedCues[obj.ID] && !directlyActivatableAnnouncement {
			continue
		}

		entries = append(entries, cuecatalog.Entry{
			CueID:       obj.ID,
			CueRevision: obj.CurrentRevision,
			Outputs:     resolveCueOutputs(payload, nodeHasSurface, nodeHasAudioNode, nodeHasLTC, assetsBySequence),
		})

		scoped := scopeShowCueOutputsForNode(payload, nodeHasSurface, nodeHasAudioNode, nodeHasLTC)
		conflict, err := detectClaimConflicts(claimants, claimant{cueID: obj.ID, playlists: referencingPlaylists[obj.ID]}, scoped, claimCtx)
		if err != nil {
			return Catalog{}, err
		}
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
		}
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
		Entries: entries, Revision: revision, Conflicts: conflicts,
	}, nil
}

// Catalog is [ResolveCueCatalog]'s result: one node's resolved Cue
// catalog for the active Show at one generation, plus its own revision.
//
// Conflicts is DATA, never an error: TRACK-H-cues-and-playlists.md section
// H5 build item 2's own ruling. A claim conflict is a readiness fact about
// the catalog, not a resolution failure — [cueactivate.Decide], [Authorize],
// and [participatingNodesForShow] must keep working for every OTHER Cue in
// the Show even while one colliding pair exists, and a caller that must
// refuse (deployment) reads this field and refuses explicitly, naming both
// Cues and the exact claim, rather than the whole resolver returning an
// opaque error that broke the live activation loop for the entire fleet
// for the rest of the show.
type Catalog struct {
	Show       string
	Generation int64
	Node       string
	Entries    []cuecatalog.Entry
	Revision   string
	Conflicts  []CatalogConflict
}

// CatalogConflict is one H0.5 exclusive-claim collision [ResolveCueCatalog]
// found between two Cues neither authoring-time validation nor
// [sameSinglePlaylist]'s exemption ruled out. CueA/CueB are sorted
// (CueA < CueB) so the reported pair is deterministic regardless of which
// Cue this resolver happened to visit first.
type CatalogConflict struct {
	CueA  string
	CueB  string
	Claim config.ShowCueClaim
}

// Detail renders c for an operator-visible refusal: TRACK-H-cues-and-playlists.md
// section H5 build item 2's ruling requires naming both Cue ids and the
// exact [config.ShowCueClaim.String] value, never only a log line.
func (c CatalogConflict) Detail() string {
	return fmt.Sprintf("cues %q and %q both hold exclusive claim %q; a concurrent claim is refused, never silently preempted (H0.5)", c.CueA, c.CueB, c.Claim.String())
}

// resolveCueOutputs projects payload's declared outputs onto
// [cuecatalog.Outputs], restricted per this file's own doc comment.
func resolveCueOutputs(payload config.ShowCuePayload, nodeHasSurface, nodeHasAudioNode, nodeHasLTC bool, assetsBySequence map[string][]ExpectedAsset) cuecatalog.Outputs {
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
	if payload.Outputs.LTC != nil && nodeHasLTC {
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

// loadAudioNodePayload reports whether nodeID has a current audio.node
// object — an audio.node object id IS the node id
// ([config.ValidateAudioNodeObjectID]), so existence alone (not its
// payload) is what gates audio/ltc/announcement output inclusion — and,
// when it does, its decoded payload: [ResolveCueCatalog]'s own claim
// arbitration (see [detectClaimConflicts]) needs nodeID's own
// ProgramRoute/LTCRoute, not merely that an audio.node object exists.
func loadAudioNodePayload(ctx context.Context, st *store.Store, nodeID string) (config.AudioNodePayload, bool, error) {
	obj, err := st.GetConfigObject(ctx, config.AudioNodeConfigKind, nodeID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return config.AudioNodePayload{}, false, nil
	}
	if err != nil {
		return config.AudioNodePayload{}, false, fmt.Errorf("get audio.node %q: %w", nodeID, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.AudioNodeConfigKind, obj.ID, obj.CurrentRevision)
	if err != nil {
		return config.AudioNodePayload{}, false, fmt.Errorf("read audio.node %q revision %d: %w", nodeID, obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeAudioNodePayload(rev.PayloadJSON)
	if verr != nil {
		return config.AudioNodePayload{}, false, fmt.Errorf("decode audio.node %q: %s", nodeID, verr.Detail)
	}
	return payload, true, nil
}

// scopeShowCueOutputsForNode narrows payload's declared outputs to the
// ones that concern THIS node, exactly the same restriction
// [resolveCueOutputs] applies when it builds this node's own
// [cuecatalog.Outputs] a few lines above — necessary because
// [config.DeriveShowCueClaims] refuses an output whose
// [config.ShowCueClaimContext] field is empty (its own doc comment: "a
// field ctx must supply for a claim p's outputs actually produce, left
// empty, is refused"), and a node with no show.surface, or no audio.node,
// leaves those context fields empty. Without this narrowing, a Cue
// declaring render (and this node having no surface at all) would refuse
// catalog resolution outright instead of correctly producing no render
// claim for a node the render output was never meant to concern.
//
// nodeHasLTC is deliberately separate from nodeHasAudioNode rather than
// derived from it. A program-only audio.node exists and carries
// a ProgramRoute but no LTCRoute, so "has an audio.node" no longer
// implies "can emit LTC" and the claim context's LTCNode/LTCRoute are
// empty for a node that does have one. Scoping LTC on the wider
// predicate would leave Outputs.LTC standing against an empty LTC
// context, which DeriveShowCueClaims refuses — failing resolution for
// EVERY cue on that node, not just the one declaring LTC.
func scopeShowCueOutputsForNode(payload config.ShowCuePayload, nodeHasSurface, nodeHasAudioNode, nodeHasLTC bool) config.ShowCuePayload {
	scoped := payload
	if !nodeHasSurface {
		scoped.Outputs.Render = nil
	}
	if !nodeHasAudioNode {
		scoped.Outputs.Audio = nil
		scoped.Outputs.Announcement = nil
	}
	if !nodeHasLTC {
		scoped.Outputs.LTC = nil
	}
	return scoped
}

// claimant is one Cue's own identity for [detectClaimConflicts]: its id
// and the set of show.playlist ids that reference it as an entry (empty
// for a Cue no Playlist references at all — a directly-activatable
// announcement Cue, or a safeCueRef).
type claimant struct {
	cueID     string
	playlists map[string]bool
}

// sameSinglePlaylist reports whether a and b could NEVER be concurrently
// active, per H0.6's "cues that could be concurrently active" test: they
// share at least one playlist. Within any one playlist, only one entry
// plays at a time (H1 spec section 4's "two entries of one Playlist are
// never concurrently active"), so two Cues that are both entries — or a
// safeCueRef and an entry, [cueReferencingPlaylistIDs]'s own doc comment —
// of the SAME playlist are always alternatives in time there, never
// simultaneous, regardless of what ELSE either one is also referenced by.
//
// This name predates the fix that made it a genuine intersection test
// (originally "both belong to exactly one playlist, the same one") — see
// TRACK-H-cues-and-playlists.md section H5 build item 2's own ruling. That
// narrower rule broke on a Cue referenced by more than one playlist: it
// stopped exempting against ANYTHING, including an ordinary sibling entry
// of a playlist it also happened to belong to, so authoring an otherwise
// unremarkable multi-entry Playlist refused itself the moment one of its
// Cues was reused elsewhere (as another playlist's entry, or as a
// safeCueRef). A shared-playlist test still exempts every case the
// original rule correctly exempted (a single shared playlist, referenced
// by nothing else, is the len==1-both-sides case) and additionally covers
// the one it wrongly caught. It does not attempt to model whether two
// DIFFERENT playlists neither shares can themselves run concurrently
// (H0.5's only blessed case is FPP-plus-one-showmesh-audio-playlist);
// a Cue authored into two playlists that could genuinely run at the same
// time, colliding with a third Cue only one of them references, is a
// known residual gap this test does not close — see this seam's own
// build report.
func sameSinglePlaylist(a, b claimant) bool {
	if len(a.playlists) == 0 || len(b.playlists) == 0 {
		return false
	}
	for id := range a.playlists {
		if b.playlists[id] {
			return true
		}
	}
	return false
}

// detectClaimConflicts derives payload's H0.5 claims via
// [config.DeriveShowCueClaims] against ctx and reports the first claim
// already held by a DIFFERENT Cue that is not [sameSinglePlaylist] with
// this one — as DATA (a non-nil *[CatalogConflict]), never as an error:
// TRACK-H-cues-and-playlists.md section H5 build item 2's own ruling. A
// claim conflict is a readiness fact about the catalog, not a resolution
// failure: [ResolveCueCatalog]'s own callers (cueactivate.Decide,
// Authorize, participatingNodesForShow — two of which loop over every
// node) must keep resolving every OTHER Cue correctly even while one
// colliding pair exists, which an error return could not do. claimants is
// mutated in place — the running map every prior Cue in this
// [ResolveCueCatalog] pass registered its own claims into — so this
// function is deliberately called once per Cue, in the SAME loop that
// builds catalog entries, rather than as a separate post-pass.
//
// [config.ShowCueClaimKindAnnouncementSession] is deliberately EXEMPTED:
// H0.5 states "Exclusive. One announcement at a time," but that
// exclusivity is already enforced by there being exactly one well-known
// announcement session ([cueactivation.AnnouncementSessionID]) a node
// ever runs — pkg/audio's own duck/mix/interrupt precedence (this seam's
// own TRACK-H-cues-and-playlists.md section H5 ruling 1) is what decides which announcement plays when
// two are triggered close together, not an authoring-time refusal.
// Refusing a Show for authoring more than one announcement Cue would be
// wrong: authoring several announcements meant to play one at a time,
// sequentially, over the life of a show is the ordinary case this output
// exists for.
func detectClaimConflicts(claimants map[config.ShowCueClaim]claimant, this claimant, payload config.ShowCuePayload, ctx config.ShowCueClaimContext) (*CatalogConflict, error) {
	claims, err := config.DeriveShowCueClaims(payload, ctx)
	if err != nil {
		return nil, fmt.Errorf("assetsync: resolve cue catalog: derive claims for cue %q: %w", this.cueID, err)
	}
	for _, claim := range claims {
		if claim.Kind == config.ShowCueClaimKindAnnouncementSession {
			continue
		}
		holder, held := claimants[claim]
		if held && holder.cueID != this.cueID && !sameSinglePlaylist(holder, this) {
			cueA, cueB := holder.cueID, this.cueID
			if cueB < cueA {
				cueA, cueB = cueB, cueA
			}
			return &CatalogConflict{CueA: cueA, CueB: cueB, Claim: claim}, nil
		}
		claimants[claim] = this
	}
	return nil, nil
}

// cueReferencingPlaylistIDs returns, for every Cue any show.playlist in
// showID references as an entry, the set of playlist ids that reference
// it — [claimant.playlists]' own source. A Cue referenced only via a
// "safeCue" mismatchPolicy's safeCueRef, or not referenced by any
// Playlist at all (a directly-activatable announcement Cue), is simply
// absent from the returned map, which [claimant]'s own zero-value
// (empty, non-exempting) already handles correctly.
func cueReferencingPlaylistIDs(ctx context.Context, st *store.Store, showID string) (map[string]map[string]bool, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.playlist objects: %w", err)
	}
	out := make(map[string]map[string]bool)
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("read show.playlist %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, err := decodeStoredShowPlaylistPayload(rev.PayloadJSON)
		if err != nil {
			return nil, fmt.Errorf("decode stored show.playlist %q: %w", obj.ID, err)
		}
		if payload.Show != showID {
			continue
		}
		for _, entry := range payload.Entries {
			if out[entry.Cue] == nil {
				out[entry.Cue] = make(map[string]bool)
			}
			out[entry.Cue][obj.ID] = true
		}
		// A "safeCue" mismatchPolicy's safeCueRef is never concurrently
		// active with this SAME playlist's own entries — it runs INSTEAD
		// of whichever entry would otherwise be active, exactly the
		// either/or relationship [sameSinglePlaylist] already exempts
		// entries of one playlist from each other for. Folding it into
		// the identical playlist-id set (rather than a separate "safe
		// cue" concept) is what lets that one exemption cover both
		// shapes without a second rule.
		if payload.MismatchPolicy == config.ShowPlaylistMismatchPolicySafeCue && payload.SafeCueRef != "" {
			if out[payload.SafeCueRef] == nil {
				out[payload.SafeCueRef] = make(map[string]bool)
			}
			out[payload.SafeCueRef][obj.ID] = true
		}
	}
	return out, nil
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
