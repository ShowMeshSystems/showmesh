package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
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
// Idempotent across repeat ticks AND across every entry into a show
// tonight: each node's apply and prepare carry an invocation key derived
// from [nightFirstCueStageEntryKey] (rec.ID, rec.Cycle, rec.StateEnteredAt),
// a fresh identity for every entry, so a tick that finds one already
// dispatched (successfully or not) for THIS entry skips it rather than
// re-dispatching or waiting on it again, while a later entry into the
// same show tonight always gets its own keys and a higher revision on
// the shared staging session ([cueactivation.PrepareStagingSessionRevision]
// derives that revision from rec.StateEnteredAt, which only ever advances).
//
// Every participating node's apply-then-prepare pair runs on its own
// goroutine, concurrently with every other node's: a slow node's own
// cold prepare (seconds, on constrained hardware) must never delay
// another node's confirmation, mirroring
// [dispatchCueActivationsConcurrently]'s identical per-node isolation.
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
	entryKey := nightFirstCueStageEntryKey(rec)

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for nodeID, catalog := range catalogs {
		entry, participates := prepareAheadCatalogEntry(catalog, cueID)
		if !participates || entry.Outputs.Audio == nil || entry.Outputs.Audio.Filename == "" {
			continue
		}
		contentHash := ""
		if len(entry.Outputs.Audio.AssetHashes) > 0 {
			contentHash = entry.Outputs.Audio.AssetHashes[0]
		}
		wg.Add(1)
		go func(nodeID string, assetID, filename string) {
			defer wg.Done()
			if unstaged := h.nightStageFirstShowCueAudioOnNode(ctx, now, entryKey, cueID, nodeID, assetID, contentHash, filename, staging, applyRevision, prepareRevision, issuer); unstaged {
				mu.Lock()
				unstagedNodes = append(unstagedNodes, nodeID)
				mu.Unlock()
			}
		}(nodeID, entry.Outputs.Audio.Asset, entry.Outputs.Audio.Filename)
	}
	wg.Wait()
	return unstagedNodes
}

// nightFirstCueStageEntryKey derives a stable identity for one entry into
// a show tonight, from rec.ID, rec.Cycle, and rec.StateEnteredAt: unlike
// rec.ArmedShowID (opaque and, once armed, unchanged across every repeat
// tick of the SAME entry, which is exactly the replay-safety this key
// also needs), this stays legible and, because rec.StateEnteredAt only
// ever advances, guarantees a DIFFERENT key for every later entry into
// the same show tonight or on a later night — so the node-side invocation
// ids this feeds never repeat across entries, only within one.
func nightFirstCueStageEntryKey(rec store.NightSessionRecord) string {
	return rec.ID + ":" + strconv.FormatInt(rec.Cycle, 10) + ":" + rec.StateEnteredAt.UTC().Format(time.RFC3339Nano)
}

// nightKickOffFirstCueStage launches [handlers.nightStageFirstShowCueAudio]
// on its own goroutine, once per entry into a show tonight (owner ruling
// 2026-09-18): a Raspberry Pi 3B+ needs about 8s to cold-prepare a large
// WAV, so staging must start as early as possible - at the same tick
// transition-to-show first runs its enterShow cues (the pre-show
// announcement cue's own release moment), never at the launch moment
// nightAdvanceTransitionToShow's own hold and barrier wait ends. Never
// blocks the calling tick: the goroutine reports back through
// nightFirstCueStageResult, which [handlers.nightFirstCueStageStatus]
// reads without waiting.
//
// Idempotent by construction, twice over: nightFirstCueStageKicked stops
// a second call for the SAME entry from ever launching a second goroutine,
// and nightStageFirstShowCueAudio's own per-node idempotency keys make a
// second goroutine for the same entry harmless even if one somehow did.
func (h *handlers) nightKickOffFirstCueStage(ctx context.Context, now time.Time, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	key := nightFirstCueStageEntryKey(rec)

	h.nightFirstCueStageMu.Lock()
	if h.nightFirstCueStageKicked == nil {
		h.nightFirstCueStageKicked = make(map[string]bool, 1)
	}
	if h.nightFirstCueStageKicked[key] {
		h.nightFirstCueStageMu.Unlock()
		return
	}
	h.nightFirstCueStageKicked[key] = true
	h.nightFirstCueStageMu.Unlock()

	h.nightFirstCueStageWG.Add(1)
	go func() {
		defer h.nightFirstCueStageWG.Done()
		defer func() {
			if r := recover(); r != nil {
				h.logWarn("night loop: stage first cue: dispatch panicked; recovered", "sessionId", rec.ID, "panic", r)
			}
		}()
		unstaged := h.nightStageFirstShowCueAudio(ctx, now, rec, payload)
		h.nightFirstCueStageMu.Lock()
		if h.nightFirstCueStageResult == nil {
			h.nightFirstCueStageResult = make(map[string][]string, 1)
		}
		h.nightFirstCueStageResult[key] = unstaged
		h.nightFirstCueStageMu.Unlock()
	}()
}

// nightFirstCueStageStatus reads back rec's own entry-keyed staging result
// without blocking: done is false while the goroutine
// [handlers.nightKickOffFirstCueStage] launched for this entry has not
// finished yet, in which case unstagedNodes is meaningless and must not be
// treated as "every node confirmed."
func (h *handlers) nightFirstCueStageStatus(rec store.NightSessionRecord) (unstagedNodes []string, done bool) {
	key := nightFirstCueStageEntryKey(rec)
	h.nightFirstCueStageMu.Lock()
	defer h.nightFirstCueStageMu.Unlock()
	unstagedNodes, done = h.nightFirstCueStageResult[key]
	return unstagedNodes, done
}

// nightStageFirstShowCueAudioOnNode runs one node's own apply-then-prepare
// staging pair, gated on the SAME idempotency check
// [handlers.nightStageFirstShowCueAudio] used inline before this was split
// out for per-node concurrency. Returns true when nodeID should be
// reported unstaged (apply or prepare did not confirm); false covers both
// a genuine confirm and an already-dispatched replay.
func (h *handlers) nightStageFirstShowCueAudioOnNode(ctx context.Context, now time.Time, entryKey, cueID, nodeID, assetID, contentHash, filename, staging string, applyRevision, prepareRevision uint64, issuer FPPCommandIssuer) (unstaged bool) {
	applyInvocation := entryKey + ":stage-first-cue-apply:" + nodeID
	if _, err := h.deps.Commands.GetCommandByIdempotencyKey(ctx, applyInvocation); err == nil {
		return false
	} else if !errors.Is(err, store.ErrCommandNotFound) {
		h.logWarn("night loop: stage first cue: look up staging apply by idempotency key failed", "nodeId", nodeID, "cueId", cueID, "error", err)
		return true
	}

	ch := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.apply", NodeID: nodeID, SessionID: staging,
		Params: map[string]any{
			"sessionId": staging, "invocationId": applyInvocation, "revision": applyRevision,
			"sourceRole": string(pkgaudio.SourceRoleShow),
			"media": map[string]any{
				"assetId": assetID, "contentHash": contentHash, "filename": filename,
			},
		},
		Revision: applyRevision, IdempotencyKey: applyInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	out, timedOut := awaitProbeStep(ch)
	switch {
	case timedOut:
		h.logWarn("night loop: stage first cue: apply dispatch timed out", "nodeId", nodeID, "cueId", cueID, "timeout", nightStageFirstShowCueTimeout)
		return true
	case out.err != nil:
		h.logWarn("night loop: stage first cue: apply dispatch failed", "nodeId", nodeID, "cueId", cueID, "error", out.err)
		return true
	case out.problem != nil:
		h.logWarn("night loop: stage first cue: apply dispatch refused", "nodeId", nodeID, "cueId", cueID, "detail", out.problem.Detail)
		return true
	case out.result.Outcome == "refused" || out.result.Outcome == "failed":
		h.logWarn("night loop: stage first cue: apply outcome", "nodeId", nodeID, "cueId", cueID, "outcome", out.result.Outcome, "reason", out.result.Reason)
		return true
	}

	prepareInvocation := entryKey + ":stage-first-cue-prepare:" + nodeID
	prepCh := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: staging,
		Params:   map[string]any{"sessionId": staging, "invocationId": prepareInvocation, "revision": prepareRevision},
		Revision: prepareRevision, IdempotencyKey: prepareInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	prepOut, prepTimedOut := awaitProbeStep(prepCh)
	switch {
	case prepTimedOut:
		h.logWarn("night loop: stage first cue: prepare dispatch timed out", "nodeId", nodeID, "cueId", cueID, "timeout", nightStageFirstShowCueTimeout)
		return true
	case prepOut.err != nil:
		h.logWarn("night loop: stage first cue: prepare dispatch failed", "nodeId", nodeID, "cueId", cueID, "error", prepOut.err)
		return true
	case prepOut.problem != nil:
		h.logWarn("night loop: stage first cue: prepare dispatch refused", "nodeId", nodeID, "cueId", cueID, "detail", prepOut.problem.Detail)
		return true
	}
	return false
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
