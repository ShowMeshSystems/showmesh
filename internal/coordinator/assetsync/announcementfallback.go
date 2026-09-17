package assetsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the announcement half of ADR-049 decisions 5, 7 and 9's registered-copy rule: a listed node without
// its own registered row for an announcement's named file borrows another listed node's row for it, the direct analog of audiofallback.go's [audioFallbackAssets] for a Cue.

// AnnouncementMedia is the file identity an announcement cue's own bound audio.session.apply target.params
// carries, per ADR-049 decision 9: {assetId, contentHash, filename, sizeBytes}, the same shape a bed item's own media reference uses.
type AnnouncementMedia struct {
	AssetID     string
	ContentHash string
	Filename    string
	SizeBytes   int64
}

// Complete reports whether every field a caller needs to trust this
// reference was actually present in the action's own params.
func (m AnnouncementMedia) Complete() bool {
	return m.AssetID != "" && m.ContentHash != "" && m.Filename != ""
}

// decodeAnnouncementMedia is this package's own copy of
// nightannouncement.go's identical decode (ADR-049 decision 9): this
// package must never import internal/coordinator/api, so the shape is read back twice rather than shared.
func decodeAnnouncementMedia(params map[string]any) AnnouncementMedia {
	media, _ := params["media"].(map[string]any)
	assetID, _ := media["assetId"].(string)
	contentHash, _ := media["contentHash"].(string)
	filename, _ := media["filename"].(string)
	var sizeBytes int64
	switch v := media["sizeBytes"].(type) {
	case float64:
		sizeBytes = int64(v)
	case json.Number:
		sizeBytes, _ = v.Int64()
	case int64:
		sizeBytes = v
	}
	return AnnouncementMedia{AssetID: assetID, ContentHash: contentHash, Filename: filename, SizeBytes: sizeBytes}
}

// AnnouncementBinding is one announcement-role night.session cue,
// resolved far enough to answer asset-delivery questions: which nodes
// need the file, and what file (the ADR-049 decision 9 media reference) they need.
type AnnouncementBinding struct {
	CueName  string
	ActionID string
	NodeIDs  []string
	Media    AnnouncementMedia
}

// AnnouncementBindings resolves showID's own configured announcement-role
// night.session cues into their bound show.action targets. A night.session,
// show.action, or cue this package cannot cleanly read back is skipped, never a hard error.
func AnnouncementBindings(ctx context.Context, st *store.Store, showID string) ([]AnnouncementBinding, error) {
	sessionObjs, err := st.ListConfigObjects(ctx, config.NightSessionConfigKind)
	if err != nil {
		return nil, fmt.Errorf("assetsync: announcement bindings for show %q: list night.session objects: %w", showID, err)
	}
	var out []AnnouncementBinding
	for _, obj := range sessionObjs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.NightSessionConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			continue
		}
		var payload config.NightSessionPayload
		if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
			continue
		}
		if payload.Show != showID {
			continue
		}
		cues := append(append([]config.NightSessionCue{}, payload.EnterShow.Cues...), payload.EnterResting.Cues...)
		for _, cue := range cues {
			if cue.Role != config.NightSessionCueRoleAnnouncement || cue.Action == "" {
				continue
			}
			actionObj, err := st.GetConfigObject(ctx, config.ShowActionConfigKind, cue.Action)
			if err != nil || actionObj.CurrentRevision == 0 {
				continue
			}
			actionRev, err := st.GetConfigRevision(ctx, config.ShowActionConfigKind, cue.Action, actionObj.CurrentRevision)
			if err != nil {
				continue
			}
			var action config.ShowActionPayload
			if err := json.Unmarshal([]byte(actionRev.PayloadJSON), &action); err != nil {
				continue
			}
			if action.Target.Integration != config.ShowActionIntegrationAudio || action.Target.AudioAction != "audio.session.apply" {
				continue
			}
			out = append(out, AnnouncementBinding{
				CueName: cue.Name, ActionID: cue.Action,
				NodeIDs: []string(action.Target.AudioNodeIDs),
				Media:   decodeAnnouncementMedia(action.Target.Params),
			})
		}
	}
	return out, nil
}

// announcementFallbackAssets is [ExpectedAssetsForNode]'s own wiring point
// for ADR-049 decisions 7 and 9's registered-copy rule: a listed node with no current row of its
// own matching content borrows another listed node's current row, keyed by content hash rather than sequence (an announcement has no sequence identity).
func announcementFallbackAssets(ctx context.Context, st *store.Store, showID, nodeID string) ([]store.AssetRecord, error) {
	bindings, err := AnnouncementBindings(ctx, st, showID)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}
	nodeAssets, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, nodeID)
	if err != nil {
		return nil, fmt.Errorf("assetsync: announcement fallback assets for node %q: list node-targeted assets: %w", nodeID, err)
	}
	showAssets, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindShow, "")
	if err != nil {
		return nil, fmt.Errorf("assetsync: announcement fallback assets for node %q: list show-wide assets: %w", nodeID, err)
	}

	rowsByTarget := make(map[string][]store.AssetRecord)
	seenAssetIDs := make(map[string]bool)
	var out []store.AssetRecord
	for _, b := range bindings {
		if !b.Media.Complete() || !containsNodeID(b.NodeIDs, nodeID) {
			continue
		}
		if hasAudioContentHash(nodeAssets, b.Media.ContentHash) || hasAudioContentHash(showAssets, b.Media.ContentHash) {
			continue // this node already holds a current copy of this exact content.
		}
		if seenAssetIDs[b.Media.AssetID] {
			continue
		}
		for _, t := range b.NodeIDs {
			if t == nodeID {
				continue
			}
			rows, ok := rowsByTarget[t]
			if !ok {
				rows, err = st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, t)
				if err != nil {
					return nil, fmt.Errorf("assetsync: announcement fallback assets for node %q: list node-targeted assets for %q: %w", nodeID, t, err)
				}
				rowsByTarget[t] = rows
			}
			if rec, found := findAudioContentHash(rows, b.Media.ContentHash); found {
				out = append(out, rec)
				seenAssetIDs[b.Media.AssetID] = true
				break
			}
		}
	}
	return out, nil
}

// AnnouncementNodesWithoutCopy reports which of nodeIDs hold no current
// copy of media anywhere in showID and cannot be rescued by
// [announcementFallbackAssets]'s own borrow: the result is always empty or every one of nodeIDs, never a partial set.
func AnnouncementNodesWithoutCopy(ctx context.Context, st *store.Store, showID string, nodeIDs []string, media AnnouncementMedia) ([]string, error) {
	if !media.Complete() {
		return nil, fmt.Errorf("assetsync: announcement media reference is incomplete")
	}
	anyHasCopy := false
	for _, nodeID := range nodeIDs {
		rows, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindNode, nodeID)
		if err != nil {
			return nil, fmt.Errorf("assetsync: announcement coverage for node %q: %w", nodeID, err)
		}
		if hasAudioContentHash(rows, media.ContentHash) {
			anyHasCopy = true
			break
		}
	}
	if !anyHasCopy {
		showAssets, err := st.ListCurrentAssetsForTarget(ctx, showID, store.AssetTargetKindShow, "")
		if err != nil {
			return nil, fmt.Errorf("assetsync: announcement coverage: list show-wide assets: %w", err)
		}
		anyHasCopy = hasAudioContentHash(showAssets, media.ContentHash)
	}
	if anyHasCopy {
		return nil, nil
	}
	return append([]string{}, nodeIDs...), nil
}

func containsNodeID(list []string, nodeID string) bool {
	for _, v := range list {
		if v == nodeID {
			return true
		}
	}
	return false
}

func hasAudioContentHash(rows []store.AssetRecord, contentHash string) bool {
	_, ok := findAudioContentHash(rows, contentHash)
	return ok
}

func findAudioContentHash(rows []store.AssetRecord, contentHash string) (store.AssetRecord, bool) {
	for _, rec := range rows {
		if rec.MediaType == audioMediaType && rec.ContentHash == contentHash {
			return rec, true
		}
	}
	return store.AssetRecord{}, false
}
