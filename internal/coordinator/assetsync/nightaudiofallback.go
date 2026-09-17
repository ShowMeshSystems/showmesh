package assetsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is ADR-049 decision 7's own registered-copy rule, mirroring
// audiofallback.go's identical rule for a Cue (decision 5): a night.session
// bed's declared Targets list nodes that play every item regardless of
// which node the item's own Asset.Target names, so a listed node without
// its own registered row for a file must still end up expecting it, by
// borrowing another listed target's own registered row. Wired into
// [ExpectedAssetsForNode] (manifest.go) alongside audioFallbackAssets.

// nightBedItemSequences resolves ba's own configured items to their
// sequence ids, in order: ba.Items directly for the inline form, or the
// referenced media.playlist's own current revision's items for the
// reference form. ok is false only when a referenced media.playlist is
// missing or tombstoned, mirroring nightResolveMediaPlaylist's own
// (api package) "unavailable, never a distinguished error" rule - a
// dangling reference contributes nothing here, exactly as it is warned
// and left for an operator everywhere else this reference is resolved.
func nightBedItemSequences(ctx context.Context, st *store.Store, ba config.NightSessionBackgroundAudio) ([]string, bool, error) {
	if ba.MediaPlaylist == "" {
		seqs := make([]string, 0, len(ba.Items))
		for _, item := range ba.Items {
			seqs = append(seqs, item.Asset.Sequence)
		}
		return seqs, true, nil
	}
	obj, err := st.GetConfigObject(ctx, config.MediaPlaylistConfigKind, ba.MediaPlaylist)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("assetsync: night bed item sequences: read media.playlist %q object: %w", ba.MediaPlaylist, err)
	}
	rev, err := st.GetConfigRevision(ctx, config.MediaPlaylistConfigKind, ba.MediaPlaylist, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("assetsync: night bed item sequences: read media.playlist %q revision %d: %w", ba.MediaPlaylist, obj.CurrentRevision, err)
	}
	var payload config.MediaPlaylistPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		// An already-stored, already-validated revision that no longer
		// decodes is unexpected in practice; treated the same as a missing
		// reference (unavailable) rather than failing every node's own
		// resolution over one corrupted, unrelated object.
		return nil, false, nil
	}
	seqs := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		seqs = append(seqs, item.Asset.Sequence)
	}
	return seqs, true, nil
}

// nightBedAudioFallbackAssets implements ADR-049 decision 7's registered-
// copy rule for a night.session bed with declared Targets: for nodeID,
// borrow the current node-scoped "audio" row of another node THIS SAME BED
// already lists as a target, for a sequence covered names as still
// uncovered (neither nodeID's own row nor a show-scoped row per
// [preferNodeOverShowAudioAssets]'s output).
//
// nodeID must itself be a declared target of the bed for its sequences to
// be borrowed at all. Among the OTHER declared targets, in the bed's own
// declared order, the first one holding a node-scoped row wins,
// deterministically, regardless of store iteration order - identical to
// [audioFallbackAssets]'s own tie-break for a Cue.
func nightBedAudioFallbackAssets(ctx context.Context, st *store.Store, showID, nodeID string, covered map[string]bool) ([]store.AssetRecord, error) {
	objs, err := st.ListConfigObjects(ctx, config.NightSessionConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: night bed fallback assets for node %q: list night.session objects: %w", nodeID, err)
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].ID < objs[j].ID })

	seenSequences := make(map[string]bool)
	rowsByTarget := make(map[string][]store.AssetRecord)
	var out []store.AssetRecord

	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.NightSessionConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, fmt.Errorf("assetsync: night bed fallback assets for node %q: read night.session %q revision %d: %w", nodeID, obj.ID, obj.CurrentRevision, err)
		}
		var payload config.NightSessionPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			// A corrupted or otherwise-unrelated session must not fail
			// every node's own asset resolution; skipped exactly as an
			// undecodable show.cue is skipped one file over.
			continue
		}
		if payload.Show != showID {
			continue
		}
		ba := payload.Resting.BackgroundAudio
		if ba == nil || !ba.HasDeclaredTargets() {
			continue
		}

		isTarget := false
		for _, t := range ba.Targets {
			if t == nodeID {
				isTarget = true
				break
			}
		}
		if !isTarget {
			continue
		}

		seqs, ok, err := nightBedItemSequences(ctx, st, *ba)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		for _, seq := range seqs {
			if seenSequences[seq] || covered[seq] {
				continue
			}
			seenSequences[seq] = true

			for _, t := range ba.Targets {
				if t == nodeID {
					continue
				}
				rows, ok := rowsByTarget[t]
				if !ok {
					rows, err = st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, t)
					if err != nil {
						return nil, fmt.Errorf("assetsync: night bed fallback assets for node %q: list node-targeted assets for %q: %w", nodeID, t, err)
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
	}

	return out, nil
}
