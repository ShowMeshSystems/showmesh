package assetsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is R7's own announcement half of ADR-049 decision 5's
// registered-copy rule: a listed node without its own registered asset
// row for an announcement's named file uses another listed node's
// registered row for it, exactly as audiofallback.go's
// [audioFallbackAssets] already does for a Cue's own audio/announcement
// output targets. The two files stay separate because the identity each
// resolves against differs: a Cue's own output names a SEQUENCE
// (resolved through (show, sequence, target)); an announcement's own
// bound audio.session.apply names one pinned file directly (R6, no
// sequence identity at all), read back from the action's own params.

// AnnouncementMedia is the file identity an announcement cue's own bound
// audio.session.apply target.params carries (R6, the frozen ruling):
// {assetId, contentHash, filename, sizeBytes}, the same shape a bed
// item's own media reference uses.
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
// nightannouncement.go's identical decode (R6): this package must never
// import internal/coordinator/api (api already imports assetsync, so the
// reverse would cycle), so the same small shape is read back twice
// rather than shared - matching this codebase's established literal-
// mirror convention across this exact package boundary (e.g.
// nightaudioreadiness.go's own audioEngineStateSignalID).
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
// need the file (its bound show.action's own AudioNodeIDs) and what file
// they need (R6's own media reference, decoded from that action's
// params).
type AnnouncementBinding struct {
	CueName  string
	ActionID string
	NodeIDs  []string
	Media    AnnouncementMedia
}

// AnnouncementBindings resolves showID's own configured announcement-role
// night.session cues (enterShow and enterResting, across every
// night.session object bound to showID) into their bound show.action
// targets. A night.session, show.action, or cue this package cannot
// cleanly read back is skipped, never a hard error - mirroring
// audioFallbackAssets' own decode-failure tolerance one file over: this
// runs for the unrelated ExpectedAssetsForNode call of every node the
// coordinator serves, not only ones with a sound announcement
// configuration.
//
// A plain json.Unmarshal, not a validating Decode*Payload call: every
// revision reached here already passed validation on write (this file's
// own top comment for why alwaysTrue exists one file over in
// manifest.go), and re-running that validation's cross-reference checks
// against a row this package only ever reads back would reject a payload
// that was valid when written - matching nightResolveShowAction's own
// identical jsonUnmarshalStrict read-back one layer up.
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

// announcementFallbackAssets is [ExpectedAssetsForNode]'s own wiring
// point for R7's registered-copy rule: every node an announcement-role
// cue's bound action lists in AudioNodeIDs needs that cue's own named
// asset; a node with no CURRENT row of its own matching content receives
// the file borrowed from another listed node's current row, keyed by
// content hash rather than sequence (an announcement has no sequence
// identity, R6) - the direct analog of [audioFallbackAssets]'s identical
// borrow-from-a-declared-sibling-target rule one file over.
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

// AnnouncementNodesWithoutCopy reports, among nodeIDs (an announcement
// cue's own bound action AudioNodeIDs), which ones hold NO current copy
// of media anywhere in showID - neither their own row, a show-wide one,
// nor any sibling's borrowable one - the exact set
// [announcementFallbackAssets] cannot rescue. R7's own fallback rule
// means partial coverage always fully rescues everyone: if ANY listed
// node (or a show-wide row) already holds the content, every other
// listed node receives it via the borrow above, so the return is either
// empty or every one of nodeIDs, never a partial set.
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
