package api

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// ADR-054: a level 1 emergency stop holds the night session. While held the
// night loop and the cue activation loop start nothing; resume-show clears
// the hold and starts the show playlist from its first entry.

const (
	nightStopHoldReason          = "The show was stopped with Stop."
	nightStopHoldCycleReason     = "The operator stopped the show before it finished."
	nightResumeShowNoHoldDetail  = "The show is not stopped, so there is nothing to resume."
	nightResumeShowPreshowDetail = "The night has not started yet. Press Start Night to start the show."
)

// nightStopHoldStands reports whether rec carries a hold that still
// governs the loop. A session that has ended carries none.
func nightStopHoldStands(rec store.NightSessionRecord) bool {
	return rec.StopHold != nil && rec.State != nightStateInactive && rec.State != nightStateStopped
}

// nightStopHoldActive is the cue activation loop's per-tick check, read
// fresh from the store like weatherDelayActive.
func (h *handlers) nightStopHoldActive(ctx context.Context) (bool, error) {
	rec, ok, err := h.deps.NightSessions.GetCurrentNightSession(ctx)
	if err != nil || !ok {
		return false, err
	}
	return nightStopHoldStands(rec), nil
}

// nightCommandAuditAction names the audit action a night command records.
func nightCommandAuditAction(cmd string) string {
	if cmd == nightCommandResumeShow {
		return identity.AuditActionShowNightResumeShow
	}
	return "night." + cmd
}

// nightStopHoldCycleClose names the live cycle a new hold interrupted, so
// level 1 can close its outcome record after the targets are stopped.
type nightStopHoldCycleClose struct {
	sessionID string
	cycle     int64
}

// nightEmergencyStopHold is level 1's night-session component. It never
// aborts the stop: a failure is logged and returned in the outcome's Error.
func (h *handlers) nightEmergencyStopHold(ctx context.Context, now time.Time, issuer identity.AuditEntry) (v1.EmergencyStopNightSessionOutcome, *nightStopHoldCycleClose) {
	var (
		out       v1.EmergencyStopNightSessionOutcome
		closeNext *nightStopHoldCycleClose
	)
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		closeNext = nil
		cur, ok, err := tx.GetCurrentNightSession(ctx)
		if err != nil {
			return err
		}
		if !ok || cur.State == nightStateInactive || cur.State == nightStateStopped {
			out = v1.EmergencyStopNightSessionOutcome{Present: false}
			return nil
		}
		out = v1.EmergencyStopNightSessionOutcome{Present: true, SessionID: cur.ID, Outcome: nightOutcomeIdempotentNoOp}
		if cur.StopHold != nil {
			return nil
		}
		principal := issuer.PrincipalName
		if principal == "" {
			principal = issuer.PrincipalID
		}
		next := cur
		next.StopHold = &store.NightSessionStopHold{Reason: nightStopHoldReason, At: now, Principal: principal}
		if cur.State == nightStateLive {
			closeNext = &nightStopHoldCycleClose{sessionID: cur.ID, cycle: cur.Cycle}
		}
		out.Outcome = nightOutcomeApplied
		return tx.UpdateNightSession(ctx, next, now)
	})
	if err != nil {
		h.logWarn("emergency stop: could not hold the night session; the stop still proceeded", "error", err)
		return v1.EmergencyStopNightSessionOutcome{Present: out.Present, SessionID: out.SessionID, Error: "hold step failed: " + err.Error()}, nil
	}
	return out, closeNext
}

// nightCloseStopHoldCycle records the interrupted live cycle as stopped.
func (h *handlers) nightCloseStopHoldCycle(ctx context.Context, now time.Time, c *nightStopHoldCycleClose) {
	if c == nil {
		return
	}
	if err := h.deps.NightSessions.CloseNightCycleOutcome(ctx, c.sessionID, c.cycle, now, store.NightCycleOutcomeStopped, nightStopHoldCycleReason); err != nil && err != store.ErrNightCycleOutcomeNotFound {
		h.logWarn("emergency stop: failed to close night cycle outcome record", "sessionId", c.sessionID, "cycle", c.cycle, "error", err)
	}
}

// nightTickDuringStopHold starts no playlist, bed, cue or announcement. A
// shutdown still proceeds, because it only removes output.
func (h *handlers) nightTickDuringStopHold(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if rec.ShutdownIntent != "" && rec.State != nightStateFadingOut {
		h.nightForceShutdownDuringWeatherDelay(ctx, now, rec)
		return
	}
	if rec.State == nightStateFadingOut {
		h.nightAdvanceFadingOut(ctx, now, rec)
	}
	h.nightStopBackgroundAudioIfRunning(ctx, now, rec)
}

// nightResumeShowTx clears the hold and puts the session at the start of
// its show transition with the launch due now, so the next loop tick
// starts the show playlist from its first entry through the start-night
// launch path.
func (h *handlers) nightResumeShowTx(ctx context.Context, tx *store.Tx, now time.Time, current *store.NightSessionRecord) (nightCommandOutcome, *v1.Problem, error) {
	if current == nil || !nightStopHoldStands(*current) {
		p := nightStateRejectedProblem(nightResumeShowNoHoldDetail)
		return nightCommandOutcome{}, &p, nil
	}
	if current.Degraded {
		p := nightAmbiguousProblem(fmt.Sprintf(nightDegradedGuidance, current.DegradedReason))
		return nightCommandOutcome{}, &p, nil
	}
	switch current.State {
	case nightStatePreshow:
		p := nightStateRejectedProblem(nightResumeShowPreshowDetail)
		return nightCommandOutcome{}, &p, nil
	case nightStateRestingIntershow, nightStateTransitionToShow, nightStateLive, nightStateTransitionToResting:
	case nightStateEndOfNightResting:
		p := nightStateRejectedProblem("The last show of the night has already played, so there is no show to resume. Fade out the night or end the session.")
		return nightCommandOutcome{}, &p, nil
	case nightStateFadingOut:
		p := nightStateRejectedProblem("The night is shutting down, so there is no show to resume. End the session to finish.")
		return nightCommandOutcome{}, &p, nil
	default:
		p := nightNotReadyProblem("The night has not started yet, so there is no show to resume. Start the pre-show first.")
		return nightCommandOutcome{}, &p, nil
	}
	if _, err := h.getPinnedNightSessionPayloadTx(ctx, tx, *current); err != nil {
		return nightCommandOutcome{}, nil, err
	}
	next := *current
	next.StopHold = nil
	next.State = nightStateTransitionToShow
	next.StateEnteredAt = now
	next.ArmedShowID = uuid.NewString()
	next.ShowCommitted = false
	next.Cycle = current.Cycle + 1
	next.ContentAnchorJSON = ""
	lastTick := now
	next.BoundaryJSON = encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &lastTick, LastTickAt: &lastTick,
		Reason: fmt.Sprintf("The show starts again from the top after Stop, expected at %s.", now.Format(time.RFC3339))})
	return nightCommandOutcome{result: next, outcome: nightOutcomeApplied, persist: "update"}, nil, nil
}
