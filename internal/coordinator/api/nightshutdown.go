package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The fading-out tick: run the configured presentation fade/blackout
// cues, issue a real FPP stop, and reach stopped only on fresh idle
// evidence that postdates that stop (RESTING-MODE.md §4.6/§4.7).

// nightAnchorPurposeShutdownStop marks the anchor that records the
// shutdown stop's own dispatch and its confirming evidence.
const nightAnchorPurposeShutdownStop = "shutdown-stop"

// nightShutdownStopConfirmDeadline bounds how long fading-out waits for
// idle evidence after the stop reached the wire. Past it the session
// degrades with a stated reason; it never reports stopped unconfirmed.
const nightShutdownStopConfirmDeadline = 90 * time.Second

// nightShutdownStopRetryBackoff bounds how often a pre-wire refusal of
// the stop is retried. The stop keeps one stable idempotency key, so a
// retry can never become a second send.
const nightShutdownStopRetryBackoff = 10 * time.Second

// nightShutdownPowerPhaseNotConfigured is invariant 6's recorded value for
// the optional presentation-power phase when no power configuration
// exists.
const nightShutdownPowerPhaseNotConfigured = "not_configured"

func (h *handlers) nightAdvanceFadingOut(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}

	// The enterShow cue definitions are the configured "bring presentation
	// to black" list; fading out replays them under their own phase and
	// waits on none of them, since there is no launch to gate.
	h.nightAdvanceCueList(ctx, now, rec, rec.StateEnteredAt, nightPhaseFadeOut, payload.EnterShow.Cues)

	hold := time.Duration(payload.EnterShow.BlackoutHoldMs) * time.Millisecond
	if now.Sub(rec.StateEnteredAt) < hold {
		return
	}

	instanceID := nightShutdownStopInstance(payload)
	if instanceID == "" {
		h.nightDegradeSession(ctx, now, rec, "fading out: this night.session revision names no FPP instance to stop; end-session and prepare-site again to recover")
		return
	}

	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.Purpose != nightAnchorPurposeShutdownStop {
		anchor = nightContentAnchor{Purpose: nightAnchorPurposeShutdownStop, FPPInstanceID: instanceID}
	}

	if anchor.DispatchedAt.IsZero() {
		if !h.nightShutdownRetryDue(anchor, now) {
			return
		}
		h.nightDispatchShutdownStop(ctx, now, rec, instanceID, anchor)
		return
	}

	obs := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, anchor.DispatchedAt, now)
	if obs.Current && obs.Status == fppStatusValueIdle && obs.PlaylistCurrent && obs.Playlist == "" {
		h.nightReachStopped(ctx, now, rec)
		return
	}
	if now.Sub(anchor.DispatchedAt) >= nightShutdownStopConfirmDeadline {
		h.nightDegradeSession(ctx, now, rec, fmt.Sprintf(
			"fading out: a stop was issued to FPP instance %q but no idle evidence confirming it arrived within %s; the presentation may still be running, so this session is not reported stopped",
			anchor.FPPInstanceID, nightShutdownStopConfirmDeadline))
	}
}

// nightShutdownStopInstance prefers the resting instance, which is what is
// playing whenever fading-out is reached without a live show.
func nightShutdownStopInstance(payload config.NightSessionPayload) string {
	if payload.Resting.FPPInstanceID != "" {
		return payload.Resting.FPPInstanceID
	}
	return payload.ShowPlaylist.FPPInstanceID
}

// nightShutdownRetryDue paces retries of a stop that was refused before it
// reached the wire. On such an anchor DispatchedAt is zero, Source carries
// the refusal's own detail, and ObservedAt is when that attempt was made.
func (h *handlers) nightShutdownRetryDue(anchor nightContentAnchor, now time.Time) bool {
	if anchor.Source == "" {
		return true
	}
	return !now.Before(anchor.ObservedAt.Add(nightShutdownStopRetryBackoff))
}

// nightShutdownStopIdempotencyKey is stable across ticks and restarts, so
// a redispatch of the shutdown stop replays rather than sending twice.
func nightShutdownStopIdempotencyKey(rec store.NightSessionRecord) string {
	return fmt.Sprintf("night-stop:%s:%d", rec.ID, rec.Cycle)
}

// nightDispatchShutdownStop issues the stop and persists an anchor whose
// DispatchedAt is set only when the command reached the wire. A pre-wire
// refusal records its reason and leaves DispatchedAt zero so a later tick
// retries under the same key.
func (h *handlers) nightDispatchShutdownStop(ctx context.Context, now time.Time, rec store.NightSessionRecord, instanceID string, anchor nightContentAnchor) {
	outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:                  instanceID,
		Action:                      fppActionStopPlaylist,
		IdempotencyKey:              nightShutdownStopIdempotencyKey(rec),
		Issuer:                      nightSafetyIssuer(rec.ID),
		NeverWithholdOnAuditFailure: true,
	})
	if err != nil {
		h.logWarn("night loop: shutdown stop dispatch failed", "sessionId", rec.ID, "instanceId", instanceID, "error", err)
		h.nightCommitShutdownAnchor(ctx, now, rec, nightContentAnchor{
			Purpose: nightAnchorPurposeShutdownStop, FPPInstanceID: instanceID,
			ObservedAt: now, Source: "the stop could not be dispatched: " + err.Error(),
		})
		return
	}
	if problem != nil {
		// Nothing reached FPP: DispatchedAt stays zero and the reason is
		// visible while later ticks retry under the same key.
		h.nightCommitShutdownAnchor(ctx, now, rec, nightContentAnchor{
			Purpose: nightAnchorPurposeShutdownStop, FPPInstanceID: instanceID,
			ObservedAt: now, Source: "the stop was refused before it reached FPP: " + problem.Detail,
		})
		return
	}

	dispatchedAt := now
	if outcome.DispatchedAt != nil {
		dispatchedAt = *outcome.DispatchedAt
	}
	next := nightContentAnchor{
		Purpose: nightAnchorPurposeShutdownStop, FPPInstanceID: instanceID,
		DispatchedAt: dispatchedAt, Source: outcome.OutcomeReason,
	}
	if outcome.Outcome == "confirmed" {
		obs := nightObservePlayback(ctx, h.deps.Observations, instanceID, dispatchedAt, now)
		if obs.Current && obs.Status == fppStatusValueIdle && obs.PlaylistCurrent && obs.Playlist == "" {
			h.nightReachStopped(ctx, now, rec)
			return
		}
	}
	h.nightCommitShutdownAnchor(ctx, now, rec, next)
}

func (h *handlers) nightCommitShutdownAnchor(ctx context.Context, now time.Time, rec store.NightSessionRecord, anchor nightContentAnchor) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateUnknown, Reason: anchor.Source})
		return cur
	})
}

// nightReachStopped is the only path to stopped that is not operator
// recovery: FPP has been observed idle with no playlist after the stop.
func (h *handlers) nightReachStopped(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateStopped
		cur.StateEnteredAt = now
		cur.ArmedShowID = ""
		cur.ShowCommitted = false
		cur.ContentAnchorJSON = ""
		cur.BoundaryJSON = ""
		if cur.ShutdownIntent == "power-down" && cur.PowerPhase == "" {
			cur.PowerPhase = nightShutdownPowerPhaseNotConfigured
		}
		return cur
	})
}
