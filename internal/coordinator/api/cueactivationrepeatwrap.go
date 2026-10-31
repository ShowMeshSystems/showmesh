package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// wrapNextCueOnRepeat names the first entry's Cue as next on the last entry
// of a playlist FPP reports repeating, so the opener is staged for the wrap.
// Absent or stale repeat evidence leaves the last entry with no next Cue.
func (h *handlers) wrapNextCueOnRepeat(ctx context.Context, now time.Time, instanceUUID string, activations map[string]cueactivation.Activation) {
	if len(activations) == 0 || h.deps.Config == nil {
		return
	}
	act := firstActivationByNodeID(activations)
	if act.NextCueID != "" {
		return
	}
	firstCueID, ok, err := wrapPlaylistEntryCueID(ctx, h.deps.Config, act.Playlist, act.PlaylistRevision, act.EntryID)
	if err != nil {
		h.logWarn("cue activation loop: resolve playlist wrap failed", "playlistId", act.Playlist, "error", err)
		return
	}
	if !ok || !h.fppReportsRepeat(ctx, now, instanceUUID) {
		return
	}
	for nodeID, a := range activations {
		a.NextCueID = firstCueID
		activations[nodeID] = a
	}
}

// wrapPlaylistEntryCueID is the first entry's Cue when entryID is the last
// of two or more entries. A single-entry playlist has nothing else to stage.
func wrapPlaylistEntryCueID(ctx context.Context, cfg ConfigStore, playlistID string, playlistRevision int64, entryID string) (cueID string, ok bool, err error) {
	if playlistID == "" || entryID == "" {
		return "", false, nil
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, playlistID, playlistRevision)
	if err != nil {
		if errors.Is(err, store.ErrConfigRevisionNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get show.playlist %q revision %d: %w", playlistID, playlistRevision, err)
	}
	var payload config.ShowPlaylistPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return "", false, fmt.Errorf("decode show.playlist %q revision %d: %w", playlistID, playlistRevision, err)
	}
	n := len(payload.Entries)
	if n < 2 || payload.Entries[n-1].ID != entryID || payload.Entries[0].Cue == payload.Entries[n-1].Cue {
		return "", false, nil
	}
	return payload.Entries[0].Cue, true, nil
}

// fppReportsRepeat reports whether a configured FPP with this uuid currently
// reports repeat on for its running playlist.
func (h *handlers) fppReportsRepeat(ctx context.Context, now time.Time, instanceUUID string) bool {
	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.logWarn("cue activation loop: list FPP instances for playlist repeat failed", "instanceUuid", instanceUUID, "error", err)
		return false
	}
	for _, v := range views {
		if v.InstanceUUID == nil || v.InstanceUUID.UUID != instanceUUID {
			continue
		}
		val, _, current, _, _ := resolveConfirmationEvidence(ctx, h.deps.Observations, v.InstanceID, nightSignalPlaylistRepeatMode, time.Time{}, now)
		if current && nightAsRepeatBool(val) {
			return true
		}
	}
	return false
}
