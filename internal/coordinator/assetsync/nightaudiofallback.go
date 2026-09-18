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

// This file is ADR-049 decision 7's registered-copy rule, mirroring
// audiofallback.go's Cue rule: a bed's declared Targets list nodes without
// their own registered row still expect it, borrowed from another target.

// nightBedItemSequences resolves ba's own configured items to their
// sequence ids, in order. ok is false only when a referenced
// media.playlist is missing or tombstoned.
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
		// A corrupt stored revision reads as unavailable, never a failure.
		return nil, false, nil
	}
	seqs := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		seqs = append(seqs, item.Asset.Sequence)
	}
	return seqs, true, nil
}

// nightBedAudioFallbackAssets borrows, for nodeID, a node-scoped row from
// another declared target of the SAME bed, for any sequence still
// uncovered. The first other target (in declared order) holding one wins.
func nightBedAudioFallbackAssets(ctx context.Context, st *store.Store, showID, nodeID string, covered map[string]bool) ([]borrowedAsset, error) {
	showAudioNodes, err := ShowAudioNodes(ctx, st, showID)
	if err != nil {
		return nil, err
	}

	objs, err := st.ListConfigObjects(ctx, config.NightSessionConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: night bed fallback assets for node %q: list night.session objects: %w", nodeID, err)
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].ID < objs[j].ID })

	seenSequences := make(map[string]bool)
	rowsByTarget := make(map[string][]store.AssetRecord)
	var out []borrowedAsset

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
		if ba == nil {
			continue
		}
		resolvedTargets, resolvedFrom := ba.ResolvedPlaybackNodeIDs(showAudioNodes)
		if resolvedFrom == config.AudioNodeResolutionDefault {
			// The per-item legacy default (each node plays only the
			// items registered for it, [NightSessionBackgroundAudio.
			// OutputNodeIDs]) is not decision 7's list-valued shape;
			// this borrow-from-another-target rule applies only once a
			// bed resolves to an explicit or show-wide node list.
			continue
		}

		isTarget := false
		for _, t := range resolvedTargets {
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

			for _, t := range resolvedTargets {
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
						out = append(out, borrowedAsset{AssetRecord: rec, ReferencedBy: obj.ID})
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
