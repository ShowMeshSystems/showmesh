package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Track F seam F6's own removal-policy runtime: once nightReachStopped
// (nightshutdown.go) records stopped from an OBSERVED idle FPP stop, this
// drives siteControl.presentationPowerOff's configured removal policy to
// completion, persisting each step as a night_cue_outbox row (nightcuerun.go)
// so a restart resumes without repeating or skipping one.

const (
	// nightPhasePowerOffPrereq holds one after-actions prerequisite's own
	// outbox row, named nightPowerOffPrereqName(index).
	nightPhasePowerOffPrereq = "power-off-prereq"
	// nightPhasePowerOffAction holds the one dispatch every removal
	// policy ends with.
	nightPhasePowerOffAction   = "power-off-action"
	nightPowerOffActionCueName = "power-off"
)

func nightPowerOffPrereqName(index int) string {
	return fmt.Sprintf("prereq-%d", index)
}

// nightAdvancePowerOff is nightTick's own stopped-state entry point for a
// power-down whose removal policy has not yet reached a terminal outcome.
// A no-op once ShutdownIntent is not "power-down" or PowerPhase has
// already moved past nightPowerPhaseConfiguredNotDispatched.
func (h *handlers) nightAdvancePowerOff(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if rec.ShutdownIntent != "power-down" || rec.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		return
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: power-off: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	if payload.SiteControl == nil || payload.SiteControl.PresentationPowerOff == nil {
		// Cannot happen for a session already prepared against this
		// configuration; must not spin forever if it somehow does.
		h.nightCompletePowerPhase(ctx, now, rec, "power-down-presentation was requested, but this session's pinned night.session revision no longer configures siteControl.presentationPowerOff; presentation power was not removed by this session")
		return
	}
	off := *payload.SiteControl.PresentationPowerOff

	// Defense in depth alongside decodeNightPresentationPowerOff's own
	// write-time refusal (nightsitecontrol.go): this is the one path that
	// can actually remove power, and environmental equipment must never
	// come down with it (RESTING-MODE.md section 10.2).
	if off.PowerDomain != config.NightPowerDomainPresentation {
		h.nightCompletePowerPhase(ctx, now, rec, fmt.Sprintf(
			"power-off sequence refused: the pinned siteControl.presentationPowerOff.powerDomain is %q, not %q; presentation power was NOT removed, and no environmental binding is ever dispatched by this path",
			off.PowerDomain, config.NightPowerDomainPresentation))
		return
	}

	if off.RemovalPolicy == config.NightRemovalPolicyAfterActions {
		for i, prereq := range off.Prerequisites {
			satisfied, terminal, reason, err := h.nightPowerOffAdvancePrerequisite(ctx, now, rec, i, prereq)
			if err != nil {
				h.logWarn("night loop: power-off: failed to advance a prerequisite", "sessionId", rec.ID, "index", i, "error", err)
				return
			}
			if terminal {
				h.nightCompletePowerPhase(ctx, now, rec, fmt.Sprintf(
					"power-off sequence stopped at prerequisite %d of %d (%s): %s; presentation power was NOT removed; remove it by invoking the configured action directly (POST /api/v1/actions/{id}/invocations) or through a configured force-power-off action",
					i+1, len(off.Prerequisites), nightPowerOffPrerequisiteLabel(prereq), reason))
				return
			}
			if !satisfied {
				return
			}
		}
	}

	h.nightPowerOffDispatchAction(ctx, now, rec, off)
}

// nightPowerOffPrerequisiteLabel is the operator-facing name for one
// prerequisite, used only once a sequence has stopped and must say which
// step stopped it.
func nightPowerOffPrerequisiteLabel(prereq config.NightPrerequisite) string {
	switch prereq.Kind {
	case config.NightPrerequisiteKindDelay:
		return fmt.Sprintf("a %s delay", time.Duration(prereq.DelayMs)*time.Millisecond)
	case config.NightPrerequisiteKindEvidence:
		return fmt.Sprintf("evidence from action %q", prereq.Action)
	default:
		return fmt.Sprintf("action %q", prereq.Action)
	}
}

// nightPowerOffAdvancePrerequisite drives one after-actions prerequisite
// towards completion. satisfied reports whether it is done; terminal
// reports whether it failed or never confirmed, stopping the sequence.
func (h *handlers) nightPowerOffAdvancePrerequisite(ctx context.Context, now time.Time, rec store.NightSessionRecord, index int, prereq config.NightPrerequisite) (satisfied, terminal bool, reason string, err error) {
	if prereq.Kind == config.NightPrerequisiteKindDelay {
		return h.nightPowerOffAdvanceDelay(ctx, now, rec, index, prereq)
	}
	return h.nightPowerOffAdvanceActionPrerequisite(ctx, now, rec, index, prereq)
}

// nightPowerOffAdvanceDelay times prereq's own delay from a night_cue_outbox
// row's own DispatchedAt, stamped once on first reaching this step so a
// restart resumes the same wait rather than restarting it.
func (h *handlers) nightPowerOffAdvanceDelay(ctx context.Context, now time.Time, rec store.NightSessionRecord, index int, prereq config.NightPrerequisite) (satisfied, terminal bool, reason string, err error) {
	cueName := nightPowerOffPrereqName(index)
	row, ferr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhasePowerOffPrereq, cueName)
	if errors.Is(ferr, store.ErrNightCueOutboxNotFound) {
		startedAt := now
		newRow := store.NightCueOutboxRecord{
			ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhasePowerOffPrereq, CueName: cueName,
			State: nightCueStateDispatched, DispatchedAt: &startedAt,
		}
		if ierr := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, newRow, now); ierr != nil {
			if !errors.Is(ierr, store.ErrNightCueOutboxDuplicate) {
				return false, false, "", ierr
			}
			row, ferr = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhasePowerOffPrereq, cueName)
			if ferr != nil {
				return false, false, "", ferr
			}
		} else {
			row = newRow
		}
	} else if ferr != nil {
		return false, false, "", ferr
	}

	if row.State == nightCueStateResolved {
		return true, false, "", nil
	}
	delay := time.Duration(prereq.DelayMs) * time.Millisecond
	if row.DispatchedAt == nil || now.Sub(*row.DispatchedAt) < delay {
		return false, false, "", nil
	}
	row.State = nightCueStateResolved
	row.Outcome = nightCueOutcomeConfirmed
	row.OutcomeReason = fmt.Sprintf("%s delay elapsed", delay)
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if uerr := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); uerr != nil {
		return false, false, "", uerr
	}
	return true, false, "", nil
}

// nightPowerOffAdvanceActionPrerequisite drives an "action" or "evidence"
// prerequisite through nightRunCue, the same dispatch/confirm/resume
// machinery cues already use - an action's own adapter-reported outcome
// is the one confirmation evidence this coordinator has. "evidence" has
// no requireConfirmation of its own (refused at write time) because that
// is the kind's whole point, so it is treated as requireConfirmation true.
func (h *handlers) nightPowerOffAdvanceActionPrerequisite(ctx context.Context, now time.Time, rec store.NightSessionRecord, index int, prereq config.NightPrerequisite) (satisfied, terminal bool, reason string, err error) {
	cueName := nightPowerOffPrereqName(index)
	cue := config.NightSessionCue{Name: cueName, Action: prereq.Action}
	issuer := nightControllerIssuer(rec)
	if nightAttributionMissing(rec) {
		h.nightMarkAttributionDegraded(ctx, now, rec)
	}
	row, rerr := h.nightRunCue(ctx, now, rec, nightPhasePowerOffPrereq, cue, issuer, false)
	if rerr != nil {
		return false, false, "", rerr
	}
	switch row.State {
	case nightCueStatePending, nightCueStateDispatched:
		// No bound here: nightRunCue's own resume path is what turns a
		// row stuck here into a resolved outcome or, when the target
		// carries no stable retry identity, ambiguous - never this file's
		// own timeout.
		return false, false, "", nil
	case nightCueStateAmbiguous:
		return false, true, row.OutcomeReason, nil
	case nightCueStateResolved:
		requireConfirm := prereq.Kind == config.NightPrerequisiteKindEvidence || prereq.RequireConfirmation
		switch row.Outcome {
		case nightCueOutcomeConfirmed:
			return true, false, "", nil
		case nightCueOutcomeFailed, nightCueOutcomeRefused:
			return false, true, row.OutcomeReason, nil
		default: // unconfirmed, unconfirmable: acceptable only when confirmation was never required.
			if requireConfirm {
				return false, true, fmt.Sprintf("did not confirm (%s): %s", row.Outcome, row.OutcomeReason), nil
			}
			return true, false, "", nil
		}
	default:
		return false, false, "", fmt.Errorf("api: power-off prerequisite outbox row %s/%d/%s has unrecognized state %q", rec.ID, rec.Cycle, cueName, row.State)
	}
}

// nightPowerOffDispatchAction is the one dispatch every removal policy
// ends with, reached directly for "immediate" and only once every
// prerequisite is satisfied for "after-actions".
func (h *handlers) nightPowerOffDispatchAction(ctx context.Context, now time.Time, rec store.NightSessionRecord, off config.NightPresentationPowerOff) {
	cue := config.NightSessionCue{Name: nightPowerOffActionCueName, Action: off.Action}
	issuer := nightControllerIssuer(rec)
	if nightAttributionMissing(rec) {
		h.nightMarkAttributionDegraded(ctx, now, rec)
	}
	row, err := h.nightRunCue(ctx, now, rec, nightPhasePowerOffAction, cue, issuer, false)
	if err != nil {
		h.logWarn("night loop: power-off: failed to dispatch the power-off action", "sessionId", rec.ID, "error", err)
		return
	}
	switch row.State {
	case nightCueStatePending, nightCueStateDispatched:
		return
	case nightCueStateAmbiguous:
		h.nightCompletePowerPhase(ctx, now, rec, fmt.Sprintf(
			"power-off action dispatch outcome is ambiguous: %s; presentation power state is unknown - verify manually and, if still on, remove it by invoking the configured action directly (POST /api/v1/actions/{id}/invocations)",
			row.OutcomeReason))
	case nightCueStateResolved:
		switch row.Outcome {
		case nightCueOutcomeFailed, nightCueOutcomeRefused:
			h.nightCompletePowerPhase(ctx, now, rec, fmt.Sprintf(
				"power-off action was not dispatched: %s; presentation power was NOT removed; remove it by invoking the configured action directly (POST /api/v1/actions/{id}/invocations) or through a configured force-power-off action",
				row.OutcomeReason))
		default:
			msg := fmt.Sprintf("presentation power-off action dispatched, outcome %s", row.Outcome)
			if row.OutcomeReason != "" {
				msg += ": " + row.OutcomeReason
			}
			h.nightCompletePowerPhase(ctx, now, rec, msg)
		}
	}
}

// nightCompletePowerPhase persists rec's terminal power-off outcome via
// nightCommit's own re-check pattern. It never sets Degraded, so a
// stopped sequence never also blocks this session's other stopped-state
// work (the background-audio end-session clear retry).
func (h *handlers) nightCompletePowerPhase(ctx context.Context, now time.Time, rec store.NightSessionRecord, reason string) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.PowerPhase = reason
		return cur
	})
}
