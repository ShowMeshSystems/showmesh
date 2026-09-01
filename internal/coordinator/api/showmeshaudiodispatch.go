package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// This file is TRACK-H-cues-and-playlists.md section H5's own coordinator
// half of the `showmesh-audio` runner: resolve the active Show's
// `showmesh-audio` show.playlist into ONE [pkgaudio.PlaylistRef]
// (assetsync.ResolveShowmeshAudioPlaylistRef) and dispatch Apply, Prepare,
// and Start against [cueactivation.BackgroundSessionID] — reusing
// audiodispatch.go's ALREADY-SHARED [handlers.executeAudioSessionDispatch]
// core (the SAME dispatch every audio.session.* HTTP route goes through),
// never a second dispatch mechanism, and never the night-session outbox's
// durable-retry machinery (internal/coordinator/api/nightbackgroundaudio.go
// — TRACK-H-cues-and-playlists.md section H5 ruling 2 forbids touching or
// copying it). There is no per-item apply here and no polling for item
// completion: TRACK-H-cues-and-playlists.md section H5's own ruling 1 is
// that the node's own Manager.RunWatcher
// (internal/agent/audio/restore.go) already advances a pinned PlaylistRef
// on its own, so a coordinator-side progression loop would be a fourth
// copy of logic pkg/audio already owns.
//
// Apply alone used to be the whole of this file — a defect a fresh review
// caught: [audio.Manager.Apply] never touches the engine and leaves the
// session StateReady, so a Playlist that was only ever Applied held a
// perfectly pinned playlist and played nothing, forever (StatePlaying is
// what [audio.Manager.watchTick] advances). Prepare then Start is the
// SAME sequence internal/agent/cueactivationaudio.go's activateAudio
// already uses to bring the show session to Playing — this file now
// dispatches the identical command set for the background session,
// rather than a second, independently-decided one.
//
// Triggered from a confirmed cue-catalog deploy (cuecatalogdeploy.go) —
// TRACK-H-H3-SPEC.md's own "this node now holds the active Show's
// authorized catalog and may run its Cues" gate — AND from an ordinary
// show.playlist write that changes THIS playlist's own revision without
// changing the Cue catalog revision (showplaylist.go's
// reapplyShowmeshAudioPlaylistIfActive): TRACK-H-cues-and-playlists.md
// section H5 build item 8's trigger-hole fix. Each of Apply/Prepare/Start
// has its own idempotency key, deterministic in (node, playlist, step,
// and the resolved playlist ref's own content) — see
// showmeshAudioIdempotencyKey's own doc comment for why content, not the
// bare playlist revision number, is what a repeated dispatch is keyed on.

// showmeshAudioIssuerPrincipalID attributes this autonomous apply to a
// stable, clearly-labeled identity — mirrors
// cueActivationSystemPrincipalID's identical posture one file over
// (cueactivationloop.go): an autonomous dispatch's attribution gap is
// made visible, never hidden behind a normal-looking operator principal
// that never made this request.
const showmeshAudioIssuerPrincipalID = "system:showmesh-audio-runner"

// applyShowmeshAudioPlaylistIfAny resolves active's Show's
// `showmesh-audio` show.playlist (if any) and dispatches Apply, Prepare,
// and Start against nodeID's own [cueactivation.BackgroundSessionID], if
// and only if nodeID has declared an audio.node object at all — a node
// with no program-audio route has nothing for a background Playlist to
// play through (ADR-018). Every failure is logged, never propagated: this
// runs as a best-effort follow-on to an already-successful cue-catalog
// deploy or playlist write, matching that deploy's own
// PutNodeCueCatalogAck best-effort posture immediately above its own call
// site.
//
// TRACK-H-cues-and-playlists.md section H5 build item 1's own fix: this
// used to dispatch Apply alone, which leaves [audio.Manager]'s session in
// StateReady forever — nothing but [audio.Manager.Start] (by way of
// StateReady's own required Prepare step) ever reaches StatePlaying, and
// only StatePlaying advances under [audio.Manager.watchTick]. A node
// holding a perfectly pinned playlist played nothing, and because duck
// and interrupt fire from [audio.Manager.Start] too (never Apply), an
// announcement had nothing to duck: this same fix is what makes the
// announcement feature work in production, not only in a test that fakes
// the precondition by calling Apply and Start directly.
func (h *handlers) applyShowmeshAudioPlaylistIfAny(ctx context.Context, now time.Time, nodeID string, active assetsync.ActiveShow) {
	if h.deps.AssetManifests == nil || h.deps.Commands == nil {
		return
	}
	hasAudioNode, err := nodeHasAudioNodeObject(ctx, h.deps.Config, nodeID)
	if err != nil {
		h.logWarn("showmesh-audio: resolve audio.node failed", "nodeId", nodeID, "error", err)
		return
	}
	if !hasAudioNode {
		return
	}

	objs, payloads, err := assetsync.ListShowmeshAudioPlaylists(ctx, h.deps.AssetManifests, active.ShowID)
	if err != nil {
		h.logWarn("showmesh-audio: list playlists failed", "show", active.ShowID, "error", err)
		return
	}
	if len(objs) == 0 {
		return
	}
	if len(objs) > 1 {
		// showplaylist.go's handlePutShowPlaylist refuses authoring a
		// SECOND showmesh-audio playlist for one Show (TRACK-H-cues-and-
		// playlists.md section H5 build item 8) — this branch is now only
		// reachable for a Show that authored more than one before that
		// refusal existed. Still never an operator-invisible alphabetical
		// pick: logged loudly, and only the first (by object id) is
		// applied, exactly as before.
		h.logWarn("showmesh-audio: more than one showmesh-audio playlist is authored for this show; applying only the first",
			"show", active.ShowID, "chosen", objs[0].ID, "count", len(objs))
	}
	obj := objs[0]
	payload := payloads[obj.ID]

	ref, err := assetsync.ResolveShowmeshAudioPlaylistRef(ctx, h.deps.AssetManifests, active.ShowID, nodeID, obj.ID, obj.CurrentRevision, payload)
	if err != nil {
		h.logWarn("showmesh-audio: resolve playlist ref failed", "show", active.ShowID, "playlist", obj.ID, "node", nodeID, "error", err)
		return
	}

	// Prepare and Start are dispatched unconditionally after a
	// non-refused Apply — including when Apply itself resolved as a
	// replay of an unchanged Apply: Prepare/Start carry their OWN
	// idempotency keys (see showmeshAudioIdempotencyKey), so a node that
	// already confirmed them simply replays those in turn, cheaply, and a
	// node that never got past Apply (e.g. a coordinator restart between
	// steps) gets the rest of the sequence it is still missing.
	if !h.dispatchShowmeshAudioStep(ctx, now, nodeID, obj.ID, ref, "apply",
		cueactivation.AudioSessionStepApply, map[string]any{
			"sourceRole": string(pkgaudio.SourceRoleBackground),
			"playlist":   showmeshAudioPlaylistWireParams(ref),
		}) {
		return
	}
	if !h.dispatchShowmeshAudioStep(ctx, now, nodeID, obj.ID, ref, "prepare", cueactivation.AudioSessionStepPrepare, nil) {
		return
	}
	h.dispatchShowmeshAudioStep(ctx, now, nodeID, obj.ID, ref, "start", cueactivation.AudioSessionStepStart, nil)
}

// dispatchShowmeshAudioStep dispatches one audio.session.<step> command
// (apply/prepare/start — the SAME three internal/agent/cueactivationaudio.
// go's activateAudio drives for the show session) against nodeID's own
// [cueactivation.BackgroundSessionID]. The returned bool is false when the
// step was refused or errored — a caller must stop the sequence there
// (Apply's own failure must not be followed by Prepare/Start) — and true
// otherwise, including a replay of an already-confirmed step.
func (h *handlers) dispatchShowmeshAudioStep(ctx context.Context, now time.Time, nodeID, playlistID string, ref pkgaudio.PlaylistRef, step string, stepIndex int, extraParams map[string]any) bool {
	idempotencyKey := showmeshAudioIdempotencyKey(nodeID, playlistID, step, ref)
	// TRACK-H-cues-and-playlists.md section H5 build item 6's own fix: this
	// used to send uint64(obj.CurrentRevision) — a small integer — as
	// this session's own pkg/audio.Revision. [cueactivation.
	// AudioSessionRevision] derives every OTHER command against the show
	// and announcement sessions (and, after build item 4, this session
	// too, once blackAndSilence extends its stop to it) from a
	// nanosecond-scale wall-clock reading. Mixing scales on ONE session
	// is the exact defect that previously left blackAndSilence unable to
	// silence anything: once any nanosecond-scale command touches this
	// session, [pkgaudio.RevisionState] refuses every future small-
	// integer revision as stale, forever. Using the SAME derivation here
	// keeps this session's revision space unified with every other
	// command that can ever address it.
	revision := cueactivation.AudioSessionRevision(now, stepIndex)
	params := map[string]any{
		"sessionId":    string(cueactivation.BackgroundSessionID),
		"invocationId": idempotencyKey,
		"revision":     revision,
	}
	for k, v := range extraParams {
		params[k] = v
	}

	in := AudioDispatchInput{
		Action: "audio.session." + step, NodeID: nodeID, SessionID: string(cueactivation.BackgroundSessionID),
		Params: params, Revision: revision, IdempotencyKey: idempotencyKey,
		IssuerID: showmeshAudioIssuerPrincipalID, IssuerName: "showmesh-audio runner",
	}
	result, problem, err := h.executeAudioSessionDispatch(ctx, now, in)
	switch {
	case err != nil:
		h.logWarn("showmesh-audio: dispatch failed", "step", step, "node", nodeID, "playlist", playlistID, "error", err)
		return false
	case problem != nil:
		h.logWarn("showmesh-audio: dispatch refused", "step", step, "node", nodeID, "playlist", playlistID, "detail", problem.Detail)
		return false
	case result.Replay:
		// Not a failure — logged at Warn, not Debug (TRACK-H-cues-and-
		// playlists.md section H5 build item 6: "do not leave it a debug
		// log"), because a suppressed step is exactly the evidence an
		// operator needs when a background bed unexpectedly does not
		// (re)start: this line is what tells them the coordinator believed
		// nothing had changed.
		h.logWarn("showmesh-audio: dispatch suppressed as a replay of an unchanged step", "step", step, "node", nodeID, "playlist", playlistID)
	}
	return true
}

// showmeshAudioIdempotencyKey derives one step's idempotency key from
// nodeID, playlistID, step, and a content digest of ref itself —
// deliberately NOT the bare playlist revision number the original
// version of this file keyed on. TRACK-H-cues-and-playlists.md section H5
// build item 6's own fix: a revision number is a config-object counter,
// not evidence of what the NODE'S ENGINE currently holds — once the
// node's background session can be stopped out from under this dispatch
// (build item 4's blackAndSilence extension), a repeated deploy of the
// SAME unchanged playlist revision must still be able to re-establish the
// engine's state rather than silently replay a stale success. Keying on
// content means identical content always resolves identically (still
// idempotent — "Apply it once" holds for a genuinely unchanged Playlist),
// while this key changes wherever the CONTENT a caller means to run
// changes, which a bare revision-number rollback (an authored Playlist
// edited back to a previously published shape, landing on a NEW config
// revision number that nonetheless matches an OLDER revision's content)
// would not.
func showmeshAudioIdempotencyKey(nodeID, playlistID, step string, ref pkgaudio.PlaylistRef) string {
	raw, _ := json.Marshal(showmeshAudioPlaylistWireParams(ref))
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("showmesh-audio-%s-%s-%s-%s", step, nodeID, playlistID, hex.EncodeToString(digest[:8]))
}

// showmeshAudioPlaylistWireParams builds audio.session.apply's own
// "playlist" wire shape from ref — the fields
// internal/agent/audiosessionops.go's parsePlaylistRef accepts, spelled
// exactly as it requires (ownerKind/ownerId/ownerRevision/items/repeat;
// each item itemId/index/assetId/contentHash/filename/sizeBytes),
// mirroring nightBackgroundApplyParams' identical wire shape one seam
// over (nightbackgroundaudio.go) without sharing its code, per
// TRACK-H-cues-and-playlists.md section H5 ruling 2.
func showmeshAudioPlaylistWireParams(ref pkgaudio.PlaylistRef) map[string]any {
	wireItems := make([]map[string]any, 0, len(ref.Items))
	for _, item := range ref.Items {
		wireItems = append(wireItems, map[string]any{
			"itemId": item.ItemID, "index": item.Index,
			"assetId": item.Media.AssetID, "contentHash": item.Media.ContentHash,
			"filename": item.Media.RuntimeFilename, "sizeBytes": item.Media.SizeBytes,
		})
	}
	return map[string]any{
		"ownerKind": ref.OwnerKind, "ownerId": ref.OwnerID, "ownerRevision": uint64(ref.OwnerRevision),
		"items": wireItems, "repeat": string(ref.Repeat),
	}
}
