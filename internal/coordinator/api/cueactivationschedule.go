package api

import (
	"context"
	"errors"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/cueactivate"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
)

// This file is ADR-049 decision 3's shared scheduling step: the one place
// between resolve ([cueactivate.Decide]/[cueactivate.ResolveDirectCueActivations])
// and dispatch ([handlers.dispatchCueActivations], cuefire.go's own
// direct-fire loop) that chooses a Cue activation's shared multi-node
// start instant, so the Playlist path and the direct-fire path reach the
// identical code rather than two independently-written copies (ADR-049's
// own "not two implementations" rule).
//
// A Cue reaching one (or zero) audio-bearing node is untouched: no
// reading round, no instant, dispatch proceeds exactly as it always has
// (ADR-049's own "a Cue reaching one node behaves exactly as today").

// scheduleProbeApplyStep and scheduleProbePrepareStep are two small,
// strictly-increasing revisions for [cueactivation.ScheduleProbeSessionID]
// — that session is never touched by anything else, so unlike
// [cueactivation.AudioSessionRevision] this derivation owes no other
// caller's own step numbering; it only has to increase between this
// function's own two calls on the SAME (fresh, per-attempt) timestamp.
const (
	scheduleProbeApplyStep   = 0
	scheduleProbePrepareStep = 1
)

// scheduleCueActivations is ADR-049 decision 3's own entry point. For
// every audio-bearing Activation in activations (outputs.Audio != nil,
// resolved via [cueactivate.Authorize] exactly as [handlers.
// dispatchOneCueActivation] independently re-resolves it per node — this
// never trusts a cached resolution any more than that file's own doc
// comment already insists on): when more than one node is audio-bearing,
// it obtains a fresh per-node media-clock reading (a throwaway apply-and-
// prepare round on [cueactivation.ScheduleProbeSessionID], cleared
// afterward on every path via [handlers.clearScheduleProbe]) and chooses
// ONE instant with [SelectAudioStartInstant], then writes that instant
// (or, on refusal, the concrete reason) back onto every audio-bearing
// Activation in activations, in place.
//
// activations is mutated in place; there is no separate return value for
// the chosen instant because every caller (the Playlist loop and the
// direct-fire route) already reads each node's own outcome off its own
// Activation, and ADR-049's own per-node-attributable-outcome rule means
// the instant belongs on the thing every other per-node fact already
// lives on.
func (h *handlers) scheduleCueActivations(ctx context.Context, now time.Time, activations map[string]cueactivation.Activation, issuer cueActivationIssuer, pin *cueactivate.ShowPin) {
	if len(activations) < 2 || h.deps.AssetManifests == nil {
		return
	}
	inventoryInterval := h.deps.AssetSettings.InventoryInterval()
	reconnectedAt := h.deps.BrokerConnection.ConnectedSince()

	type audioBearing struct {
		nodeID string
		out    pkgaudio.MediaRef
	}
	var bearing []audioBearing
	for nodeID, act := range activations {
		_, _, outputs, _, err := cueactivate.Authorize(ctx, h.deps.AssetManifests, now, inventoryInterval, reconnectedAt, nodeID, act, pin)
		if err != nil || outputs.Audio == nil {
			continue
		}
		contentHash := ""
		if len(outputs.Audio.AssetHashes) > 0 {
			contentHash = outputs.Audio.AssetHashes[0]
		}
		bearing = append(bearing, audioBearing{nodeID: nodeID, out: pkgaudio.MediaRef{
			AssetID: outputs.Audio.Asset, ContentHash: contentHash, RuntimeFilename: outputs.Audio.Filename,
		}})
	}
	if len(bearing) < 2 {
		return
	}

	settings, err := h.alignedStartSettings(ctx)
	if err != nil {
		h.logWarn("cue activation schedule: read audio.settings failed; every node starts on arrival", "error", err)
		return
	}

	mediaByNode := make(map[string]pkgaudio.MediaRef, len(bearing))
	nodeIDs := make([]string, 0, len(bearing))
	for _, b := range bearing {
		mediaByNode[b.nodeID] = b.out
		nodeIDs = append(nodeIDs, b.nodeID)
	}

	read := func(ctx context.Context, nodeID string) (AudioStartInstantReading, error) {
		holdsClock, err := h.nodeHoldsMediaClock(ctx, nodeID)
		if err != nil {
			return AudioStartInstantReading{}, err
		}
		evidence := h.readScheduleProbe(ctx, now, nodeID, mediaByNode[nodeID], issuer)
		return AudioStartInstantReading{HoldsMediaClock: holdsClock, Evidence: evidence}, nil
	}

	sel, selErr := SelectAudioStartInstant(ctx, nodeIDs, settings, read)
	for _, nodeID := range nodeIDs {
		act := activations[nodeID]
		if selErr != nil {
			var noClock *audiosched.ErrNoUsableClock
			if !errors.As(selErr, &noClock) {
				h.logWarn("cue activation schedule: select start instant failed; every node starts on arrival", "error", selErr)
			}
			act.ScheduledAtNs = nil
			act.UnalignedReason = audiosched.DescribeUnscheduled(selErr)
		} else {
			at := sel.ScheduledAtNs
			act.ScheduledAtNs = &at
			act.UnalignedReason = ""
		}
		activations[nodeID] = act
	}
}

// cueActivationAlignment reports the ONE aligned/unaligned verdict
// [scheduleCueActivations] chose for activations, read back off the
// Activations it mutated: every audio-bearing Activation in one
// scheduling attempt carries the identical ScheduledAtNs (or the
// identical UnalignedReason), so any one of them answers for the whole
// batch. A Cue that never attempted scheduling at all (zero or one
// audio-bearing node) reports aligned=true with no instant and no
// reason — ADR-049's own "a Cue reaching one node behaves exactly as
// today", never reported as an unaligned failure it never attempted.
func cueActivationAlignment(activations map[string]cueactivation.Activation) (aligned bool, unalignedReason string, scheduledAtNs *int64) {
	for _, act := range activations {
		if act.ScheduledAtNs != nil {
			return true, "", act.ScheduledAtNs
		}
		if act.UnalignedReason != "" {
			return false, act.UnalignedReason, nil
		}
	}
	return true, "", nil
}

// readScheduleProbe applies act's own media onto
// [cueactivation.ScheduleProbeSessionID] and prepares it, to read a fresh
// media-clock evidence map without touching nodeID's real show session
// (which may already be Playing the PRECEDING Cue) or the prepare-ahead
// staging session (which a concurrent tick may already be using for a
// DIFFERENT, later Cue) — see [cueactivation.ScheduleProbeSessionID]'s own
// doc comment. The probe apply carries only Media: no LTC start offset,
// no mix policy, no announcement role, so it can never start LTC or
// participate in a duck/interrupt relationship even if a caller later
// mistakenly started it (which this function itself never does — Prepare
// only, never Start).
//
// The probe session is cleared on every path — evidence obtained,
// refused, or never returned at all — via a deferred best-effort
// audio.session.clear, so a probe never accumulates a loaded engine
// handle nodeID's own asset directory has to carry for longer than this
// one selection.
func (h *handlers) readScheduleProbe(ctx context.Context, now time.Time, nodeID string, media pkgaudio.MediaRef, issuer cueActivationIssuer) map[string]any {
	defer h.clearScheduleProbe(ctx, now, nodeID, issuer)

	session := cueactivation.ScheduleProbeSessionID
	applyInvocation := "schedule-probe-apply:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	prepareInvocation := "schedule-probe-prepare:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	applyRevision := uint64(now.UnixNano())*10 + scheduleProbeApplyStep
	prepareRevision := uint64(now.UnixNano())*10 + scheduleProbePrepareStep

	applyResult, applyProblem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.apply", NodeID: nodeID, SessionID: session,
		Params: map[string]any{
			"sessionId": session, "invocationId": applyInvocation, "revision": applyRevision,
			"sourceRole": string(pkgaudio.SourceRoleShow),
			"media": map[string]any{
				"assetId": media.AssetID, "contentHash": media.ContentHash, "filename": media.RuntimeFilename,
			},
		},
		Revision: applyRevision, IdempotencyKey: applyInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	switch {
	case err != nil:
		h.logWarn("cue activation schedule: probe apply dispatch failed", "nodeId", nodeID, "error", err)
		return nil
	case applyProblem != nil:
		h.logWarn("cue activation schedule: probe apply dispatch refused", "nodeId", nodeID, "detail", applyProblem.Detail)
		return nil
	case applyResult.Outcome == "refused" || applyResult.Outcome == "failed":
		h.logWarn("cue activation schedule: probe apply outcome", "nodeId", nodeID, "outcome", applyResult.Outcome, "reason", applyResult.Reason)
		return nil
	}

	var evidence map[string]any
	_, prepareProblem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: session,
		Params:   map[string]any{"sessionId": session, "invocationId": prepareInvocation, "revision": prepareRevision},
		Revision: prepareRevision, IdempotencyKey: prepareInvocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		OnEvidence: func(v map[string]any) { evidence = v },
	})
	switch {
	case err != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch failed", "nodeId", nodeID, "error", err)
		return nil
	case prepareProblem != nil:
		h.logWarn("cue activation schedule: probe prepare dispatch refused", "nodeId", nodeID, "detail", prepareProblem.Detail)
		return nil
	}
	return evidence
}

// clearScheduleProbe best-effort clears [cueactivation.ScheduleProbeSessionID]
// on nodeID, unconditionally — see [handlers.readScheduleProbe]'s own doc
// comment for why this runs on every path. audio.session.clear is exempt
// from stale-revision refusal ([audio.Manager.Clear]'s own doc comment),
// so a fixed, always-fresh revision is sufficient; failure is logged and
// swallowed; nothing about this Cue's own activation depends on it.
func (h *handlers) clearScheduleProbe(ctx context.Context, now time.Time, nodeID string, issuer cueActivationIssuer) {
	session := cueactivation.ScheduleProbeSessionID
	invocation := "schedule-probe-clear:" + nodeID + ":" + now.Format(time.RFC3339Nano)
	_, problem, err := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.clear", NodeID: nodeID, SessionID: session,
		Params:   map[string]any{"sessionId": session, "invocationId": invocation, "revision": uint64(now.UnixNano())},
		Revision: uint64(now.UnixNano()), IdempotencyKey: invocation,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
	})
	if err != nil {
		h.logWarn("cue activation schedule: probe clear dispatch failed", "nodeId", nodeID, "error", err)
	} else if problem != nil {
		h.logWarn("cue activation schedule: probe clear dispatch refused", "nodeId", nodeID, "detail", problem.Detail)
	}
}
