package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The fading-out tick: run the configured presentation fade/blackout
// cues, issue a real FPP stop, and reach stopped on fresh idle evidence
// that postdates that stop. A power down reaches stopped without that
// evidence once the confirm deadline passes (RESTING-MODE.md §4.6/§4.7,
// ADR-069).

// nightAnchorPurposeShutdownStop marks the anchor that records the
// shutdown stop's own dispatch and its confirming evidence.
const nightAnchorPurposeShutdownStop = "shutdown-stop"

// nightShutdownStopConfirmDeadline bounds how long fading-out waits for
// idle evidence after the stop reached the wire. Past it a fade-out-only
// night degrades with a stated reason; a power down reaches stopped with
// the stop recorded as unconfirmed, and the stop is resent on this cadence.
const nightShutdownStopConfirmDeadline = 90 * time.Second

const (
	nightStopUnconfirmedFact = "FPP %s did not confirm the stop, so power down started anyway. The stop is sent again until FPP reports idle."
	nightStopForcedFact      = "Power down was forced without waiting for FPP %s to stop. The stop is sent again until FPP reports idle."
)

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
	// waits on none of them, since there is no launch to gate. Announcement
	// cues are excluded: RESTING-MODE.md section 7.1 stages an announcement
	// after the blackout barrier as a show-start element, never as part of
	// reaching black, so replaying one here greets an emptying site.
	h.nightAdvanceCueList(ctx, now, rec, rec.StateEnteredAt, nightPhaseFadeOut, nightFadeOutCues(payload.EnterShow.Cues), payload)

	hold := time.Duration(payload.EnterShow.BlackoutHoldMs) * time.Millisecond
	fadingOutNight := rec.ShutdownIntent == "fade-out"
	if ms := nightRenderFadeOutNightMs(payload); ms != nil && fadingOutNight {
		fade := time.Duration(*ms) * time.Millisecond
		h.nightRenderFade(ctx, now, rec, "out-night", 0, time.Time{}, rec.StateEnteredAt.Add(fade), 0)
		hold = max(hold, fade)
	}
	if fadeMs := nightFadeOutNightMs(payload); fadeMs != nil && fadingOutNight {
		// The stop waits for the lights to reach black, so ending the night
		// fades. A power-down, emergency or not, never waits for a fade.
		fade := time.Duration(*fadeMs) * time.Millisecond
		if endsAt := rec.StateEnteredAt.Add(fade); now.Before(endsAt) {
			h.nightLightsStart(ctx, now, rec, payload, nightLightsStep{step: nightLightsStepNightOut, target: 0, endsAt: endsAt})
		}
		hold = max(hold, fade)
	}
	if now.Sub(rec.StateEnteredAt) < hold {
		return
	}

	instanceID := nightShutdownStopInstance(payload)
	if instanceID == "" {
		h.nightDegradeSession(ctx, now, rec, nightDegradedReason(nightDegradedNoFPPToStopFact))
		return
	}

	anchor, has := decodeNightContentAnchor(rec.ContentAnchorJSON)
	if !has || anchor.Purpose != nightAnchorPurposeShutdownStop {
		anchor = nightContentAnchor{Purpose: nightAnchorPurposeShutdownStop, FPPInstanceID: instanceID}
	}

	powerDown := rec.ShutdownIntent == "power-down"
	if anchor.DispatchedAt.IsZero() {
		if powerDown && !now.Before(rec.StateEnteredAt.Add(hold+nightShutdownStopConfirmDeadline)) {
			h.nightReachStoppedStopUnconfirmed(ctx, now, rec, instanceID, nil)
			return
		}
		if !h.nightShutdownRetryDue(anchor, now) {
			return
		}
		h.nightDispatchShutdownStop(ctx, now, rec, instanceID, anchor)
		return
	}

	obs := nightObservePlayback(ctx, h.deps.Observations, anchor.FPPInstanceID, anchor.DispatchedAt, now)
	if nightPlaybackIdle(obs) {
		h.nightReachStopped(ctx, now, rec)
		return
	}
	if now.Sub(anchor.DispatchedAt) < nightShutdownStopConfirmDeadline {
		return
	}
	if powerDown {
		sentAt := anchor.DispatchedAt
		h.nightReachStoppedStopUnconfirmed(ctx, now, rec, anchor.FPPInstanceID, &sentAt)
		return
	}
	if !rec.Degraded {
		h.nightDegradeSession(ctx, now, rec, nightDegradedReason(nightStopNotConfirmedFact, anchor.FPPInstanceID))
	}
	// Degrading records what happened; it does not end the attempt. The
	// display is still running, so the stop is re-armed for another try
	// under a fresh attempt number rather than left to sit forever.
	next := anchor
	next.DispatchedAt = time.Time{}
	next.AttemptedAt = now
	next.Attempts = anchor.Attempts + 1
	next.Source = fmt.Sprintf(nightStopNotConfirmedFact, anchor.FPPInstanceID) + " " + nightTryingAgain
	h.nightCommitShutdownAnchorFor(ctx, now, rec, next)
}

// nightCommitShutdownAnchorFor persists anchor against whatever state rec
// is in, which for a re-armed stop may already be degraded.
func (h *handlers) nightCommitShutdownAnchorFor(ctx context.Context, now time.Time, rec store.NightSessionRecord, anchor nightContentAnchor) {
	h.nightCommit(ctx, now, rec.ID, nightStateFadingOut, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.ContentAnchorJSON = encodeNightContentAnchor(anchor)
		cur.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateUnknown, Reason: anchor.Source})
		return cur
	})
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
// the refusal's own detail, and AttemptedAt is when that attempt was made.
// The stop is retried even on a terminal-looking refusal: leaving the
// display running is worse than a repeated refusal in the log.
func (h *handlers) nightShutdownRetryDue(anchor nightContentAnchor, now time.Time) bool {
	if anchor.AttemptedAt.IsZero() {
		return true
	}
	return !now.Before(anchor.AttemptedAt.Add(nightShutdownStopRetryBackoff))
}

// nightShutdownStopIdempotencyKey is stable for one attempt, so a retry
// of that same attempt (a restart mid-dispatch, say) replays instead of
// sending twice. A new attempt number takes a new key deliberately: a
// command row exists under the old one, and replaying it would answer
// with the old outcome and put nothing on the wire, which is the wrong
// answer when the display is still running.
func nightShutdownStopIdempotencyKey(rec store.NightSessionRecord, attempt int64) string {
	if attempt == 0 {
		return fmt.Sprintf("night-stop:%s:%d", rec.ID, rec.Cycle)
	}
	return fmt.Sprintf("night-stop:%s:%d:%d", rec.ID, rec.Cycle, attempt)
}

// nightDispatchShutdownStop issues the stop and persists an anchor whose
// DispatchedAt is set only when the command reached the wire. A pre-wire
// refusal records its reason and leaves DispatchedAt zero so a later tick
// retries under the same key.
func (h *handlers) nightDispatchShutdownStop(ctx context.Context, now time.Time, rec store.NightSessionRecord, instanceID string, anchor nightContentAnchor) {
	notSent := func(reason string) {
		// Nothing reached FPP. DispatchedAt stays zero, so the next tick
		// retries under a fresh attempt rather than waiting out a
		// confirmation deadline for a command that was never sent.
		next := anchor
		next.Purpose, next.FPPInstanceID = nightAnchorPurposeShutdownStop, instanceID
		next.DispatchedAt = time.Time{}
		next.AttemptedAt = now
		next.Attempts = anchor.Attempts + 1
		next.Source = reason
		h.nightCommitShutdownAnchorFor(ctx, now, rec, next)
	}

	outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:                  instanceID,
		Action:                      fppActionStopPlaylist,
		IdempotencyKey:              nightShutdownStopIdempotencyKey(rec, anchor.Attempts),
		Issuer:                      nightControllerIssuer(rec),
		NeverWithholdOnAuditFailure: true,
	})
	switch {
	case err != nil:
		h.logWarn("night loop: shutdown stop dispatch failed", "sessionId", rec.ID, "instanceId", instanceID, "error", err)
		notSent("The stop was not sent to FPP. Trying again.")
		return
	case problem != nil:
		h.logWarn("night loop: shutdown stop was held back", "sessionId", rec.ID, "instanceId", instanceID, "reason", problem.Detail)
		notSent(fmt.Sprintf("FPP %s could not be told to stop yet. Trying again.", instanceID))
		return
	case outcome.DispatchFailed:
		// FPP did not accept the request: the host is unreachable or
		// answered non-2xx. Outcome alone cannot say that, which is what
		// DispatchFailed exists for.
		h.logWarn("night loop: shutdown stop did not reach FPP", "sessionId", rec.ID, "instanceId", instanceID, "reason", outcome.OutcomeReason)
		notSent(fmt.Sprintf(nightStopNotReachedFact, instanceID) + " " + nightTryingAgain)
		return
	}

	dispatchedAt := now
	if outcome.DispatchedAt != nil {
		dispatchedAt = *outcome.DispatchedAt
	}
	next := anchor
	next.Purpose, next.FPPInstanceID = nightAnchorPurposeShutdownStop, instanceID
	next.DispatchedAt = dispatchedAt
	next.AttemptedAt = now
	next.Source = outcome.OutcomeReason
	if outcome.Outcome == "confirmed" {
		obs := nightObservePlayback(ctx, h.deps.Observations, instanceID, dispatchedAt, now)
		if nightPlaybackIdle(obs) {
			h.nightReachStopped(ctx, now, rec)
			return
		}
	}
	h.nightCommitShutdownAnchorFor(ctx, now, rec, next)
}

// nightPlaybackIdle is the stop's confirming evidence: FPP idle with no playlist.
func nightPlaybackIdle(obs nightPlaybackObservation) bool {
	return obs.Current && obs.Status == fppStatusValueIdle && obs.PlaylistCurrent && obs.Playlist == ""
}

// nightReachStopped is the confirmed path to stopped: FPP has been
// observed idle with no playlist after the stop.
func (h *handlers) nightReachStopped(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	h.nightReachStoppedWith(ctx, now, rec, "")
}

// nightReachStoppedStopUnconfirmed reaches stopped for a power down whose
// stop FPP never confirmed. sentAt is when the stop last reached FPP, or nil.
func (h *handlers) nightReachStoppedStopUnconfirmed(ctx context.Context, now time.Time, rec store.NightSessionRecord, instanceID string, sentAt *time.Time) {
	h.logWarn("night loop: power down reached stopped without FPP confirming the stop", "sessionId", rec.ID, "instanceId", instanceID)
	h.nightReachStoppedWith(ctx, now, rec, encodeNightBoundary(nightStopUnconfirmedBoundary(nightStopUnconfirmedFact, instanceID, sentAt)))
}

func nightStopUnconfirmedBoundary(fact, instanceID string, sentAt *time.Time) nightBoundary {
	return nightBoundary{
		State: nightBoundaryStateUnknown, Kind: nightBoundaryKindStopUnconfirmed, Reason: fmt.Sprintf(fact, instanceID),
		FPPInstanceID: instanceID, StopSentAt: sentAt, StopAttemptedAt: sentAt,
	}
}

func (h *handlers) nightReachStoppedWith(ctx context.Context, now time.Time, rec store.NightSessionRecord, boundaryJSON string) {
	// Read OUTSIDE nightCommit's own short transaction, matching every
	// other pinned-payload read in this file: this is a plain read, never
	// held across the store's single connection alongside a write.
	powerPhase := nightShutdownPowerPhaseNotConfigured
	if rec.ShutdownIntent == "power-down" {
		if payload, err := h.getPinnedNightSessionPayload(ctx, rec); err == nil {
			if seqs, serr := h.nightPowerDownSequences(ctx, rec, payload); serr == nil && len(seqs) > 0 {
				powerPhase = nightPowerPhaseConfiguredNotDispatched
			}
		}
	}
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.State = nightStateStopped
		cur.StateEnteredAt = now
		cur.ArmedShowID = ""
		cur.ShowCommitted = false
		cur.ContentAnchorJSON = ""
		cur.BoundaryJSON = boundaryJSON
		if cur.ShutdownIntent == "power-down" && cur.PowerPhase == "" {
			cur.PowerPhase = powerPhase
		}
		return cur
	})
}

// nightResendUnconfirmedStop is the stopped tick's stop resend: while the
// stop is unconfirmed it is sent again every confirm deadline, and the
// record clears once FPP reports idle after the last send.
func (h *handlers) nightResendUnconfirmedStop(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	b, ok := decodeNightBoundary(rec.BoundaryJSON)
	if !ok || b.Kind != nightBoundaryKindStopUnconfirmed || b.FPPInstanceID == "" {
		return
	}
	if b.StopSentAt != nil {
		if nightPlaybackIdle(nightObservePlayback(ctx, h.deps.Observations, b.FPPInstanceID, *b.StopSentAt, now)) {
			h.nightCommitStopResend(ctx, now, rec, nil)
			return
		}
		if now.Sub(*b.StopSentAt) < nightShutdownStopConfirmDeadline {
			return
		}
	}
	if b.StopAttemptedAt != nil && now.Sub(*b.StopAttemptedAt) < nightShutdownStopRetryBackoff {
		return
	}

	next := b
	attemptedAt := now
	next.StopAttemptedAt = &attemptedAt
	outcome, problem, err := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:                  b.FPPInstanceID,
		Action:                      fppActionStopPlaylist,
		IdempotencyKey:              nightStopResendIdempotencyKey(rec, b.StopAttemptedAt),
		Issuer:                      nightControllerIssuer(rec),
		NeverWithholdOnAuditFailure: true,
	})
	switch {
	case err != nil:
		h.logWarn("night loop: stop resend dispatch failed", "sessionId", rec.ID, "instanceId", b.FPPInstanceID, "error", err)
	case problem != nil:
		h.logWarn("night loop: stop resend was held back", "sessionId", rec.ID, "instanceId", b.FPPInstanceID, "reason", problem.Detail)
	case outcome.DispatchFailed:
		h.logWarn("night loop: stop resend did not reach FPP", "sessionId", rec.ID, "instanceId", b.FPPInstanceID, "reason", outcome.OutcomeReason)
	default:
		sentAt := now
		if outcome.DispatchedAt != nil {
			sentAt = *outcome.DispatchedAt
		}
		next.StopSentAt = &sentAt
		if outcome.Outcome == "confirmed" && nightPlaybackIdle(nightObservePlayback(ctx, h.deps.Observations, b.FPPInstanceID, sentAt, now)) {
			h.nightCommitStopResend(ctx, now, rec, nil)
			return
		}
	}
	h.nightCommitStopResend(ctx, now, rec, &next)
}

// nightStopResendIdempotencyKey is stable for one resend attempt and new
// for the next, keyed by when the previous attempt was made.
func nightStopResendIdempotencyKey(rec store.NightSessionRecord, prevAttempt *time.Time) string {
	var prev int64
	if prevAttempt != nil {
		prev = prevAttempt.UnixNano()
	}
	return fmt.Sprintf("night-stop:%s:%d:resend:%d", rec.ID, rec.Cycle, prev)
}

// nightCommitStopResend persists the resend state, or clears it when next
// is nil, only while the session still carries the unconfirmed stop.
func (h *handlers) nightCommitStopResend(ctx context.Context, now time.Time, rec store.NightSessionRecord, next *nightBoundary) {
	h.nightCommit(ctx, now, rec.ID, nightStateStopped, func(cur store.NightSessionRecord) store.NightSessionRecord {
		if b, ok := decodeNightBoundary(cur.BoundaryJSON); !ok || b.Kind != nightBoundaryKindStopUnconfirmed {
			return cur
		}
		cur.BoundaryJSON = ""
		if next != nil {
			cur.BoundaryJSON = encodeNightBoundary(*next)
		}
		return cur
	})
}

// nightFadeOutCues is the subset of an enterShow cue list that fading out
// replays: every role except announcement.
func nightFadeOutCues(cues []config.NightSessionCue) []config.NightSessionCue {
	out := make([]config.NightSessionCue, 0, len(cues))
	for _, cue := range cues {
		if cue.Role == config.NightSessionCueRoleAnnouncement {
			continue
		}
		out = append(out, cue)
	}
	return out
}

// nightFadeOutNightMs is Fade Out Night's own fade length, or the lights
// fade-out when none is set.
func nightFadeOutNightMs(p config.NightSessionPayload) *int {
	if p.FadeOutNightMs != nil {
		return p.FadeOutNightMs
	}
	return p.LightsFadeOutMs
}
