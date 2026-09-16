package assetsync

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// audioMediaType is the [store.AssetRecord.MediaType] this file's own
// precedence rules apply to. Render (fseq) assets are genuinely per node
// (ADR-043) and are never touched here.
const audioMediaType = "audio"

// preferNodeOverShowAudioAssets drops, from showAssets, any "audio"
// media-type row whose sequence nodeAssets already covers with its own
// "audio" row: ADR-049 decision 5's first precedence tier ("a node-scoped
// row for that exact target, if one exists, keeps winning") over the
// second (a show-scoped row). ADR-028 decision 4 keys currentness by
// (show, sequence, targetKind, target, mediaType), so a node-scoped and a
// show-scoped row for the identical sequence can both be current at once;
// without this filter both would land in one node's ExpectedSet, which
// would let a stale or differently-encoded show-wide upload silently
// compete with an operator's own per-node mix rather than simply losing.
// Render rows, and every other media type, pass through untouched.
func preferNodeOverShowAudioAssets(nodeAssets, showAssets []store.AssetRecord) []store.AssetRecord {
	nodeAudioSequences := make(map[string]bool, len(nodeAssets))
	for _, rec := range nodeAssets {
		if rec.MediaType == audioMediaType {
			nodeAudioSequences[rec.SequenceID] = true
		}
	}
	filtered := make([]store.AssetRecord, 0, len(showAssets))
	for _, rec := range showAssets {
		if rec.MediaType == audioMediaType && nodeAudioSequences[rec.SequenceID] {
			continue
		}
		filtered = append(filtered, rec)
	}
	return filtered
}

// effectiveAudioTargets resolves an audio or announcement output's
// declared Targets to the node ids it actually concerns, applying ADR-049
// decision 1's own "empty resolves to the installation's default node"
// rule ([audioTargets.OwnsAny]'s identical resolution, restated here
// because that method only ever answers membership, never enumerates the
// list membership is checked against).
func effectiveAudioTargets(targets []string, defaultNode string) []string {
	if len(targets) > 0 {
		return targets
	}
	if defaultNode == "" {
		return nil
	}
	return []string{defaultNode}
}

// audioFallbackAssets implements ADR-049 decision 5's third precedence
// tier: for nodeID, borrow the current node-scoped "audio" row of another
// node THIS SAME CUE ALREADY LISTS as an audio or announcement target, for
// a sequence covered names as still uncovered (neither nodeID's own row
// nor a show-scoped row per [preferNodeOverShowAudioAssets]'s output).
//
// nodeID must itself be a declared target of the Cue's audio or
// announcement output for its sequence to be borrowed at all — this is
// never widened to every node in the show, or every audio node would end
// up expecting every audio asset the show has ever declared. Among the
// OTHER declared targets, the first one (in the Cue's own declared order:
// outputs.audio.targets, then outputs.announcement.targets appended for a
// target only the announcement lists) holding a node-scoped row wins,
// deterministically, regardless of store iteration order. The borrowed
// [store.AssetRecord] is returned exactly as its owning target holds it:
// no reshaping, resampling, or channel-count check, because a borrowed
// asset can legitimately be the wrong shape for the borrowing node's own
// hardware (a 4-channel M4 lending its mix to a 2-channel Scarlett-fed Pi)
// and this function's only job is delivering the same bytes, never
// judging them.
func audioFallbackAssets(ctx context.Context, st *store.Store, showID, nodeID string, covered map[string]bool) ([]store.AssetRecord, error) {
	referencedCues, err := referencedCueIDs(ctx, st, showID)
	if err != nil {
		return nil, err
	}
	nodeTargets, err := loadAudioTargets(ctx, st, nodeID)
	if err != nil {
		return nil, err
	}

	cueObjs, err := st.ListConfigObjects(ctx, config.ShowCueConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: audio fallback assets for node %q: list show.cue objects: %w", nodeID, err)
	}
	sort.Slice(cueObjs, func(i, j int) bool { return cueObjs[i].ID < cueObjs[j].ID })

	seenSequences := make(map[string]bool)
	rowsByTarget := make(map[string][]store.AssetRecord)
	var out []store.AssetRecord

	for _, obj := range cueObjs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, fmt.Errorf("assetsync: audio fallback assets for node %q: read show.cue %q revision %d: %w", nodeID, obj.ID, obj.CurrentRevision, err)
		}
		payload, verr := config.DecodeShowCuePayload(rev.PayloadJSON, alwaysTrue, alwaysTrue)
		if verr != nil {
			// Unlike ResolveCueCatalog's own main loop (which must fail
			// hard so [exclusiveClaimReadiness]'s undecodableCueID match
			// still fires), this resolver runs unconditionally for EVERY
			// node ExpectedAssetsForNode is asked about, including a
			// node with no audio.node at all. A cue belonging to some
			// unrelated, corrupted Show must not turn every node's asset
			// resolution into a hard error; skip it exactly as an
			// unresolvable fallback source would be skipped.
			continue
		}
		if payload.Show != showID || payload.Outputs.Audio == nil {
			continue
		}
		directlyActivatableAnnouncement := payload.Outputs.Announcement != nil
		if !referencedCues[obj.ID] && !directlyActivatableAnnouncement {
			continue
		}

		seq := payload.Outputs.Audio.Asset
		if seenSequences[seq] || covered[seq] {
			continue
		}

		orderedTargets := effectiveAudioTargets(payload.Outputs.Audio.Targets, nodeTargets.defaultNode)
		if payload.Outputs.Announcement != nil {
			present := make(map[string]bool, len(orderedTargets))
			for _, t := range orderedTargets {
				present[t] = true
			}
			for _, t := range effectiveAudioTargets(payload.Outputs.Announcement.Targets, nodeTargets.defaultNode) {
				if !present[t] {
					orderedTargets = append(orderedTargets, t)
					present[t] = true
				}
			}
		}

		isTarget := false
		for _, t := range orderedTargets {
			if t == nodeID {
				isTarget = true
				break
			}
		}
		if !isTarget {
			continue
		}
		seenSequences[seq] = true

		for _, t := range orderedTargets {
			if t == nodeID {
				continue
			}
			rows, ok := rowsByTarget[t]
			if !ok {
				rows, err = st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, t)
				if err != nil {
					return nil, fmt.Errorf("assetsync: audio fallback assets for node %q: list node-targeted assets for %q: %w", nodeID, t, err)
				}
				rowsByTarget[t] = rows
			}
			found := false
			for _, rec := range rows {
				if rec.SequenceID == seq && rec.MediaType == audioMediaType {
					out = append(out, rec)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	return out, nil
}
