package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// nightStageFirstShowCueTimeout bounds, per node, how long start-night's
// own first-cue staging waits for an apply or a prepare to confirm,
// mirroring [scheduleProbeStepTimeout]'s own established bound for a
// night-controller-driven node dispatch (see nightannouncement.go's
// identical use of it).
const nightStageFirstShowCueTimeout = scheduleProbeStepTimeout

// nightStageFirstShowCueAudio stages the bound show playlist's own first
// audio-bearing entry on every audio node that entry's Cue resolves to,
// under [cueactivation.PrepareStagingSessionID], with the exact apply and
// prepare shape and revision scheme [handlers.dispatchPrepareAheadAudio]
// already uses for the ordinary cue-to-cue prepare-ahead case
// (cueactivationloop.go). Called once, before start-night ever tells the
// player to start: prepare-ahead only ever stages cue N+1 while cue N is
// already activating, so the first cue of the night has nothing to stage
// it ahead of time until this runs. This is the rig's own recorded
// defect: both nodes cold-prepared on FPP's OPEN and started 1.35s and
// 1.63s late.
//
// Bounded to nightStageFirstShowCueTimeout per node: a node that has not
// confirmed within it is named in the returned unstagedNodes and left to
// resolve in the background, exactly as dispatchPrepareAheadAudio's own
// dispatches are — this must never delay or refuse start-night. Returns
// nil when there is nothing to stage at all: no bound playlist, no entry
// resolves to a cue with audio anywhere, or a dependency is unavailable.
//
// Idempotent across repeat ticks: each node's apply carries an
// invocation key derived from rec.ArmedShowID, so a tick that finds one
// already dispatched (successfully or not) skips it rather than
// re-dispatching or waiting on it again.
func (h *handlers) nightStageFirstShowCueAudio(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) (unstagedNodes []string) {
	if h.deps.Config == nil || h.deps.AssetManifests == nil || h.deps.Commands == nil {
		return nil
	}
	playlistID := payload.ShowPlaylist.Playlist
	if playlistID == "" {
		return nil
	}
	entries, err := nightShowPlaylistEntries(ctx, h.deps.Config, playlistID)
	if err != nil || len(entries) == 0 {
		return nil
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil || !active.Configured {
		return nil
	}
	nodeIDs, err := nightAudioNodeIDs(ctx, h.deps.Config)
	if err != nil || len(nodeIDs) == 0 {
		return nil
	}

	catalogs := make(map[string]assetsync.Catalog, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		catalog, err := assetsync.ResolveCueCatalog(ctx, h.deps.AssetManifests, active, nodeID)
		if err != nil {
			h.logWarn("night loop: stage first cue: resolve cue catalog failed", "nodeId", nodeID, "error", err)
			continue
		}
		catalogs[nodeID] = catalog
	}

	cueID, ok := nightFirstAudioBearingCue(entries, catalogs)
	if !ok {
		return nil
	}

	issuer := nightControllerIssuer(rec)
	t := rec.StateEnteredAt
	applyRevision := cueactivation.PrepareStagingSessionRevision(t, cueactivation.PrepareStagingSessionStepApply)
	prepareRevision := cueactivation.PrepareStagingSessionRevision(t, cueactivation.PrepareStagingSessionStepPrepare)
	staging := cueactivation.PrepareStagingSessionID

	type inflightApply struct {
		nodeID string
		ch     <-chan audioDispatchOutcome
	}
	var applies []inflightApply
	for nodeID, catalog := range catalogs {
		entry, participates := prepareAheadCatalogEntry(catalog, cueID)
		if !participates || entry.Outputs.Audio == nil || entry.Outputs.Audio.Filename == "" {
			continue
		}
		applyInvocation := rec.ArmedShowID + ":stage-first-cue-apply:" + nodeID
		if _, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, applyInvocation); err == nil {
			continue
		} else if !errors.Is(err, store.ErrCommandNotFound) {
			h.logWarn("night loop: stage first cue: look up staging apply by idempotency key failed", "nodeId", nodeID, "cueId", cueID, "error", err)
			unstagedNodes = append(unstagedNodes, nodeID)
			continue
		}
		contentHash := ""
		if len(entry.Outputs.Audio.AssetHashes) > 0 {
			contentHash = entry.Outputs.Audio.AssetHashes[0]
		}
		ch := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
			Action: "audio.session.apply", NodeID: nodeID, SessionID: staging,
			Params: map[string]any{
				"sessionId": staging, "invocationId": applyInvocation, "revision": applyRevision,
				"sourceRole": string(pkgaudio.SourceRoleShow),
				"media": map[string]any{
					"assetId": entry.Outputs.Audio.Asset, "contentHash": contentHash, "filename": entry.Outputs.Audio.Filename,
				},
			},
			Revision: applyRevision, IdempotencyKey: applyInvocation,
			IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
			IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		})
		applies = append(applies, inflightApply{nodeID: nodeID, ch: ch})
	}

	for _, ia := range applies {
		out, timedOut := awaitProbeStep(ia.ch)
		switch {
		case timedOut:
			h.logWarn("night loop: stage first cue: apply dispatch timed out", "nodeId", ia.nodeID, "cueId", cueID, "timeout", nightStageFirstShowCueTimeout)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
			continue
		case out.err != nil:
			h.logWarn("night loop: stage first cue: apply dispatch failed", "nodeId", ia.nodeID, "cueId", cueID, "error", out.err)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
			continue
		case out.problem != nil:
			h.logWarn("night loop: stage first cue: apply dispatch refused", "nodeId", ia.nodeID, "cueId", cueID, "detail", out.problem.Detail)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
			continue
		case out.result.Outcome == "refused" || out.result.Outcome == "failed":
			h.logWarn("night loop: stage first cue: apply outcome", "nodeId", ia.nodeID, "cueId", cueID, "outcome", out.result.Outcome, "reason", out.result.Reason)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
			continue
		}

		prepareInvocation := rec.ArmedShowID + ":stage-first-cue-prepare:" + ia.nodeID
		prepCh := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
			Action: "audio.session.prepare", NodeID: ia.nodeID, SessionID: staging,
			Params:   map[string]any{"sessionId": staging, "invocationId": prepareInvocation, "revision": prepareRevision},
			Revision: prepareRevision, IdempotencyKey: prepareInvocation,
			IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
			IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		})
		prepOut, prepTimedOut := awaitProbeStep(prepCh)
		switch {
		case prepTimedOut:
			h.logWarn("night loop: stage first cue: prepare dispatch timed out", "nodeId", ia.nodeID, "cueId", cueID, "timeout", nightStageFirstShowCueTimeout)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
		case prepOut.err != nil:
			h.logWarn("night loop: stage first cue: prepare dispatch failed", "nodeId", ia.nodeID, "cueId", cueID, "error", prepOut.err)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
		case prepOut.problem != nil:
			h.logWarn("night loop: stage first cue: prepare dispatch refused", "nodeId", ia.nodeID, "cueId", cueID, "detail", prepOut.problem.Detail)
			unstagedNodes = append(unstagedNodes, ia.nodeID)
		}
	}
	return unstagedNodes
}

// nightShowPlaylistEntries reads playlistID's current show.playlist
// revision and returns its entries, or (nil, nil) when the object does
// not exist or is tombstoned (CurrentRevision == 0) — never a
// distinguished error for either, matching this package's established
// "absent config reads as unavailable" posture (see nightResolveMediaPlaylist).
func nightShowPlaylistEntries(ctx context.Context, cfg ConfigStore, playlistID string) ([]config.ShowPlaylistEntry, error) {
	obj, err := cfg.GetConfigObject(ctx, config.ShowPlaylistConfigKind, playlistID)
	if err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if obj.CurrentRevision == 0 {
		return nil, nil
	}
	rev, err := cfg.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, playlistID, obj.CurrentRevision)
	if err != nil {
		if errors.Is(err, store.ErrConfigRevisionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var payload config.ShowPlaylistPayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return nil, err
	}
	return payload.Entries, nil
}

// nightAudioNodeIDs lists every configured, non-tombstoned audio.node
// object id, mirroring [handlers.nightCheckAudioAlignment]'s identical
// enumeration (nightaudioreadiness.go).
func nightAudioNodeIDs(ctx context.Context, cfg ConfigStore) ([]string, error) {
	objs, err := cfg.ListConfigObjects(ctx, config.AudioNodeConfigKind)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		ids = append(ids, obj.ID)
	}
	return ids, nil
}

// nightFirstAudioBearingCue scans entries in playlist order for the first
// one whose Cue has an audio output on at least one node's catalog: the
// first entry of a show playlist may well be video-only, and the owner
// ruling's "video leads audio" gap does not exist for a cue with nothing
// to prepare ahead of time.
func nightFirstAudioBearingCue(entries []config.ShowPlaylistEntry, catalogs map[string]assetsync.Catalog) (cueID string, ok bool) {
	for _, entry := range entries {
		if entry.Cue == "" {
			continue
		}
		for _, catalog := range catalogs {
			if e, participates := prepareAheadCatalogEntry(catalog, entry.Cue); participates && e.Outputs.Audio != nil && e.Outputs.Audio.Filename != "" {
				return entry.Cue, true
			}
		}
	}
	return "", false
}
