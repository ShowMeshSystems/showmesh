package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The night's site actions (ADR-065): prepare-site actions, the power
// down that runs shutdown sequences side by side, and end session actions.
// Every step is a night_cue_outbox row, so a restart resumes a wait or a
// dispatch instead of repeating or skipping it.

// The night_cue_outbox.phase values this file writes.
const (
	nightPhasePrepareSite  = "prepareSite"
	nightPhaseShutdownStep = "shutdownStep"
	nightPhaseEndSession   = "endSession"
)

func nightShutdownStepName(scope, seqID string, index int) string {
	return fmt.Sprintf("%s:%s:%d", scope, seqID, index)
}

func nightSiteActionName(index int) string { return fmt.Sprintf("action-%d", index) }

// nightEndSessionSkippedName marks a night whose end session actions must
// not run, because a hard stop ended it.
const nightEndSessionSkippedName = "skipped-by-hard-stop"

// nightMarkEmergencySelection records that level 2 forced rec's power down.
func (h *handlers) nightMarkEmergencySelection(ctx context.Context, now time.Time, rec store.NightSessionRecord) error {
	resolvedAt := now
	err := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseShutdownStep, CueName: nightEmergencySelectionName,
		State: nightCueStateResolved, Outcome: nightCueOutcomeConfirmed, OutcomeReason: "Emergency stop picked the shutdown sequences.", ResolvedAt: &resolvedAt,
	}, now)
	if errors.Is(err, store.ErrNightCueOutboxDuplicate) {
		return nil
	}
	return err
}

// nightSkipEndSessionActions records that rec's end session actions are
// skipped. A hard stop calls it right after ending the session.
func (h *handlers) nightSkipEndSessionActions(ctx context.Context, now time.Time, rec store.NightSessionRecord) error {
	resolvedAt := now
	err := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseEndSession, CueName: nightEndSessionSkippedName,
		State: nightCueStateResolved, Outcome: nightCueOutcomeRefused, OutcomeReason: "Hard stop skips end session actions.", ResolvedAt: &resolvedAt,
	}, now)
	if errors.Is(err, store.ErrNightCueOutboxDuplicate) {
		return nil
	}
	return err
}

// nightScopedSequence is a sequence with where it came from, so a global
// and a night-only sequence with the same id never share outbox rows.
type nightScopedSequence struct {
	scope string
	seq   config.ShutdownSequence
}

// nightEmergencySelectionName marks a night whose power down was forced by
// emergency stop level 2, so that level's skip list picks the sequences.
const nightEmergencySelectionName = "selected-by-emergency-stop"

// nightPowerDownSequences returns what this night runs at power down: the
// global sequences saved now, less the ones skipped, then its own. The
// skip list is the night's, or level 2's when level 2 forced the power down.
func (h *handlers) nightPowerDownSequences(ctx context.Context, rec store.NightSessionRecord, payload config.NightSessionPayload) ([]nightScopedSequence, error) {
	global, _, _, _, err := h.resolveSitePowerConfig(ctx)
	if err != nil {
		return nil, err
	}
	var out []nightScopedSequence
	skip := []string(nil)
	var own []config.ShutdownSequence
	if payload.SiteActions != nil && payload.SiteActions.PowerDown != nil {
		skip = payload.SiteActions.PowerDown.SkipSequences
		own = payload.SiteActions.PowerDown.AdditionalSequences
	}
	if _, merr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseShutdownStep, nightEmergencySelectionName); merr == nil {
		if es, eerr := h.resolveEmergencyStopPayload(ctx); eerr == nil {
			skip = es.StopPowerDown.SkipSequences
		}
	}
	for _, s := range config.SequencesExcept(global.ShutdownSequences, skip) {
		out = append(out, nightScopedSequence{scope: "global", seq: s})
	}
	for _, s := range own {
		out = append(out, nightScopedSequence{scope: "night", seq: s})
	}
	return out, nil
}

// nightAdvancePowerOff is the stopped-state tick: it resends an
// unconfirmed stop, advances a pending power down, and once none is
// pending, the end session actions.
func (h *handlers) nightAdvancePowerOff(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	h.nightResendUnconfirmedStop(ctx, now, rec)
	if rec.ShutdownIntent != "power-down" || rec.PowerPhase != nightPowerPhaseConfiguredNotDispatched {
		h.nightAdvanceEndSessionActions(ctx, now, rec)
		return
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		h.logWarn("night loop: power down: failed to read pinned night.session payload", "sessionId", rec.ID, "error", err)
		return
	}
	seqs, err := h.nightPowerDownSequences(ctx, rec, payload)
	if err != nil {
		h.logWarn("night loop: power down: failed to read the shutdown sequences", "sessionId", rec.ID, "error", err)
		return
	}
	if len(seqs) == 0 {
		h.nightCompletePowerPhase(ctx, now, rec, nightShutdownPowerPhaseNotConfigured)
		return
	}
	allDone := true
	var stopped []string
	for _, s := range seqs {
		done, ok, err := h.nightAdvanceShutdownSequence(ctx, now, rec, s)
		if err != nil {
			h.logWarn("night loop: power down: failed to advance a sequence", "sessionId", rec.ID, "sequence", s.seq.ID, "error", err)
			allDone = false
			continue
		}
		if !done {
			allDone = false
		} else if !ok {
			stopped = append(stopped, s.seq.Label)
		}
	}
	if !allDone {
		return
	}
	if len(stopped) == 0 {
		h.nightCompletePowerPhase(ctx, now, rec, "Power down finished.")
		return
	}
	h.nightCompletePowerPhase(ctx, now, rec, fmt.Sprintf("Power down stopped early in %s, so some power may still be on. Check it by hand.", strings.Join(stopped, ", ")))
}

// nightAdvanceShutdownSequence advances one sequence by as many steps as
// are ready. done reports that it finished or stopped; ok that every step
// succeeded.
func (h *handlers) nightAdvanceShutdownSequence(ctx context.Context, now time.Time, rec store.NightSessionRecord, s nightScopedSequence) (done, ok bool, err error) {
	for i, step := range s.seq.Steps {
		cueName := nightShutdownStepName(s.scope, s.seq.ID, i)
		var satisfied, terminal bool
		switch {
		case step.IsDelay():
			satisfied, err = h.nightAdvanceDelayStep(ctx, now, rec, cueName, step.DelayMs)
		case step.PowerDomain != "" && step.PowerDomain != config.NightPowerDomainPresentation:
			// Never producible through validation; refused here as well so
			// environmental protection never comes down with show power.
			h.logWarn("night loop: power down: refused a step that removes non-show power", "sessionId", rec.ID, "sequence", s.seq.ID, "domain", step.PowerDomain)
			terminal = true
		default:
			satisfied, terminal, err = h.nightAdvanceActionStep(ctx, now, rec, nightPhaseShutdownStep, cueName, step.Action, step.RequireConfirmation)
		}
		if err != nil {
			return false, false, err
		}
		if terminal {
			h.logWarn("night loop: power down: a sequence stopped at a step", "sessionId", rec.ID, "sequence", s.seq.ID, "step", i+1)
			return true, false, nil
		}
		if !satisfied {
			return false, false, nil
		}
	}
	return true, true, nil
}

// nightAdvanceDelayStep times a wait from its outbox row's DispatchedAt,
// stamped once, so a restart resumes the same wait.
func (h *handlers) nightAdvanceDelayStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, cueName string, delayMs int) (bool, error) {
	row, ferr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseShutdownStep, cueName)
	if errors.Is(ferr, store.ErrNightCueOutboxNotFound) {
		startedAt := now
		row = store.NightCueOutboxRecord{
			ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: nightPhaseShutdownStep, CueName: cueName,
			State: nightCueStateDispatched, DispatchedAt: &startedAt,
		}
		if ierr := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, row, now); ierr != nil {
			if !errors.Is(ierr, store.ErrNightCueOutboxDuplicate) {
				return false, ierr
			}
			if row, ferr = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseShutdownStep, cueName); ferr != nil {
				return false, ferr
			}
		}
	} else if ferr != nil {
		return false, ferr
	}
	if row.State == nightCueStateResolved {
		return true, nil
	}
	if row.DispatchedAt == nil || now.Sub(*row.DispatchedAt) < time.Duration(delayMs)*time.Millisecond {
		return false, nil
	}
	row.State = nightCueStateResolved
	row.Outcome = nightCueOutcomeConfirmed
	row.OutcomeReason = "The wait finished."
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	return true, h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row)
}

// nightAdvanceActionStep dispatches one action through nightRunCue and
// reports whether it is done and whether it ended its list. An action
// deleted since the config was saved ends the list instead of retrying.
func (h *handlers) nightAdvanceActionStep(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName, actionID string, requireConfirm bool) (satisfied, terminal bool, err error) {
	if _, gerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName); errors.Is(gerr, store.ErrNightCueOutboxNotFound) {
		if _, oerr := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, actionID); errors.Is(oerr, store.ErrConfigObjectNotFound) {
			return false, true, nil
		}
	}
	if nightAttributionMissing(rec) {
		h.nightMarkAttributionDegraded(ctx, now, rec)
	}
	row, rerr := h.nightRunCue(ctx, now, rec, phase, config.NightSessionCue{Name: cueName, Action: actionID}, nightControllerIssuer(rec), false)
	if rerr != nil {
		return false, false, rerr
	}
	switch row.State {
	case nightCueStatePending, nightCueStateDispatched:
		return false, false, nil
	case nightCueStateAmbiguous:
		return false, true, nil
	case nightCueStateResolved:
		switch row.Outcome {
		case nightCueOutcomeConfirmed:
			return true, false, nil
		case nightCueOutcomeFailed, nightCueOutcomeRefused:
			return false, true, nil
		default:
			if requireConfirm {
				return false, true, nil
			}
			return true, false, nil
		}
	default:
		return false, false, fmt.Errorf("api: site action outbox row %s/%d/%s has unrecognized state %q", rec.ID, rec.Cycle, cueName, row.State)
	}
}

// nightCompletePowerPhase records rec's power down outcome. It never sets
// Degraded, so a stopped sequence never blocks the session's other
// stopped-state work.
func (h *handlers) nightCompletePowerPhase(ctx context.Context, now time.Time, rec store.NightSessionRecord, reason string) {
	h.nightCommit(ctx, now, rec.ID, rec.State, func(cur store.NightSessionRecord) store.NightSessionRecord {
		cur.PowerPhase = reason
		return cur
	})
}

// nightEndSessionActionsDue reports whether a stopped night should run its
// end session actions now: after its power down finished, or when it was
// ended without one. A night that only faded out waits for its power down.
func nightEndSessionActionsDue(rec store.NightSessionRecord) bool {
	if rec.State != nightStateStopped {
		return false
	}
	switch rec.ShutdownIntent {
	case "power-down":
		return rec.PowerPhase != "" && rec.PowerPhase != nightPowerPhaseConfiguredNotDispatched
	case "":
		return true
	default:
		return false
	}
}

// nightAdvanceEndSessionActions runs the end session actions in order, one
// outbox row each. A failed action is passed over, never retried.
func (h *handlers) nightAdvanceEndSessionActions(ctx context.Context, now time.Time, rec store.NightSessionRecord) {
	if !nightEndSessionActionsDue(rec) {
		return
	}
	if _, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, nightPhaseEndSession, nightEndSessionSkippedName); err == nil {
		return
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil || payload.SiteActions == nil {
		return
	}
	for i, id := range payload.SiteActions.EndSession {
		satisfied, terminal, err := h.nightAdvanceActionStep(ctx, now, rec, nightPhaseEndSession, nightSiteActionName(i), id, false)
		if err != nil {
			h.logWarn("night loop: end session action failed to dispatch", "sessionId", rec.ID, "action", id, "error", err)
			return
		}
		if !satisfied && !terminal {
			return
		}
	}
}

// nightRunPrepareSiteActions sends a new preparation's prepare-site actions
// in order, in the background. The outbox row per action is what keeps a
// repeated or retried prepare-site from sending one twice.
func (h *handlers) nightRunPrepareSiteActions(ctx context.Context, rec store.NightSessionRecord, payload config.NightSessionPayload) {
	if payload.SiteActions == nil || len(payload.SiteActions.PrepareSite) == 0 {
		return
	}
	actions := append([]string(nil), payload.SiteActions.PrepareSite...)
	h.sitePowerRuns.Add(1)
	go func() {
		defer h.sitePowerRuns.Done()
		bg := context.WithoutCancel(ctx)
		for i, id := range actions {
			name := nightSiteActionName(i)
			for attempt := 0; attempt < nightPrepareSiteActionMaxPolls; attempt++ {
				satisfied, terminal, err := h.nightAdvanceActionStep(bg, h.now(), rec, nightPhasePrepareSite, name, id, false)
				if err != nil {
					h.logWarn("prepare-site: action failed to dispatch", "sessionId", rec.ID, "action", id, "error", err)
					break
				}
				if satisfied || terminal {
					break
				}
				time.Sleep(nightPrepareSiteActionPoll)
			}
		}
	}()
}

// A prepare-site action that has not resolved after this long is left
// pending; its outbox row shows where it stopped.
const (
	nightPrepareSiteActionPoll     = 250 * time.Millisecond
	nightPrepareSiteActionMaxPolls = 600
)
