package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// RESTING-MODE.md §7.1.1's commit/dispatch/recover mechanics, built on
// nightcue.go's classification and dispatch.

// The night_cue_outbox.phase vocabulary — part of a row's own identity
// alongside session/cycle/cue name, since the two lists are validated
// separately and may share a cue name.
const (
	nightPhaseEnterShow    = "enterShow"
	nightPhaseEnterResting = "enterResting"

	// nightPhaseFadeOut replays the enterShow cue definitions when the
	// session is fading out. It is its own phase so those rows never
	// collide with a real enter-show commit in the same cycle.
	nightPhaseFadeOut = "fadeOut"
)

// nightCueDispatchHooks lets a test deterministically interrupt the two
// crash windows §7.1.1 names. Both fields are nil in production; a hook
// that returns true aborts processing at exactly that point, leaving the
// row exactly as the prior write left it, for a later call to resume.
type nightCueDispatchHooks struct {
	// afterCommit runs once the commit transaction has returned, before
	// any dispatch call is made.
	afterCommit func(cueName string) (abort bool)

	// afterDispatch runs once a dispatch call has returned, before its
	// outcome is persisted into night_cue_outbox.
	afterDispatch func(cueName string) (abort bool)
}

func (h *handlers) hookAfterCommit(cueName string) bool {
	if h.nightCueHooks.afterCommit == nil {
		return false
	}
	return h.nightCueHooks.afterCommit(cueName)
}

func (h *handlers) hookAfterDispatch(cueName string) bool {
	if h.nightCueHooks.afterDispatch == nil {
		return false
	}
	return h.nightCueHooks.afterDispatch(cueName)
}

// errNightCueNotConfirmableForFirst is returned when a cue configured as
// the first outward-facing cue resolves to an action [nightCueConfirmable]
// rejects. Readiness (nightcue_readiness.go) is the primary gate; this is
// defense in depth.
var errNightCueNotConfirmableForFirst = errors.New("api: this cue's action is not confirmable and cannot be the first outward-facing cue")

// errNightCueSessionMoved is returned when the atomic first-cue commit
// found the session no longer matches the tick's own snapshot — §7.1.1's
// "if fade-out-night wins before commit, cancel the armed boundary" case.
// Nothing was written by this call.
var errNightCueSessionMoved = errors.New("api: the night session moved out from under this cue commit")

// nightCommitFirstCue is §7.1.1's own atomic commit: it writes rec's
// show_committed flag and the first outward-facing cue's outbox row in
// ONE transaction, before either is acted on. committed is false with a
// nil error when a concurrent write already moved the session.
func (h *handlers) nightCommitFirstCue(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string, actionRevision int64) (bool, error) {
	committed := false
	err := h.deps.NightSessions.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		cur, ok, gerr := tx.GetCurrentNightSession(ctx)
		if gerr != nil {
			return gerr
		}
		if !ok || cur.ID != rec.ID || cur.State != rec.State || cur.Cycle != rec.Cycle || cur.ShowCommitted {
			return nil
		}
		cur.ShowCommitted = true
		if uerr := tx.UpdateNightSession(ctx, cur, now); uerr != nil {
			return uerr
		}
		if ierr := tx.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
			ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: phase, CueName: cueName,
			ActionRevision: actionRevision, State: nightCueStatePending,
		}, now); ierr != nil {
			return ierr
		}
		committed = true
		return nil
	})
	return committed, err
}

// nightCommitCueRow durably records a non-first cue's own outbox row —
// still atomic (one INSERT), with no show_committed linkage.
func (h *handlers) nightCommitCueRow(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string, actionRevision int64) error {
	return h.deps.NightSessions.InsertNightCueOutboxRow(ctx, store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: phase, CueName: cueName,
		ActionRevision: actionRevision, State: nightCueStatePending,
	}, now)
}

// nightDispatchAndPersistCue dispatches target (idemKey as its stable
// identity) and persists the outcome into the row, which must already
// exist. It is the ONE code path both the ordinary advance and crash
// recovery use — see [nightCueDispatchHooks] for its two crash windows.
func (h *handlers) nightDispatchAndPersistCue(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string, target config.ShowActionTarget, idemKey string, issuer FPPCommandIssuer, actionRevision int64) (store.NightCueOutboxRecord, error) {
	if h.hookAfterCommit(cueName) {
		return h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	}

	// Mark dispatched BEFORE calling the adapter, not after: a row found
	// in this state, however it got there, means "an attempt may have
	// been made and its outcome is unknown" — which is why a
	// non-retryable action found here resolves to ambiguous.
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	if row.State == nightCueStatePending {
		t := now
		row.State = nightCueStateDispatched
		row.DispatchedAt = &t
		if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
			return store.NightCueOutboxRecord{}, err
		}
	}

	result := h.nightDispatchCueTarget(ctx, now, issuer, target, idemKey, actionRevision)

	if h.hookAfterDispatch(cueName) {
		return h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	}

	row, err = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	if result.dispatched && result.dispatchedAt != nil {
		row.DispatchedAt = result.dispatchedAt
	}
	if !result.resolved {
		row.State = nightCueStateDispatched
		if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
			return store.NightCueOutboxRecord{}, err
		}
		return row, nil
	}
	row.State = nightCueStateResolved
	row.Outcome = result.outcome
	row.OutcomeReason = result.reason
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	return row, nil
}

// nightResumeCueRow continues an outbox row that already exists: observe
// first (resolved/ambiguous returns unchanged), retry only with the same
// stable identity when the action supports it, otherwise mark ambiguous.
func (h *handlers) nightResumeCueRow(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase string, cue config.NightSessionCue, row store.NightCueOutboxRecord, issuer FPPCommandIssuer) (store.NightCueOutboxRecord, error) {
	switch row.State {
	case nightCueStateResolved, nightCueStateAmbiguous:
		return row, nil

	case nightCueStatePending:
		action, err := nightResolveShowActionRevision(ctx, h.deps.Config, cue.Action, row.ActionRevision)
		if err != nil {
			return store.NightCueOutboxRecord{}, err
		}
		idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cue.Name)
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cue.Name, nightAnnouncementDeclaredTarget(cue, action.Target), idemKey, issuer, row.ActionRevision)

	case nightCueStateDispatched:
		action, err := nightResolveShowActionRevision(ctx, h.deps.Config, cue.Action, row.ActionRevision)
		if err != nil {
			return store.NightCueOutboxRecord{}, err
		}
		if !nightCueRetryableByIdentity(action.Target) {
			// A dispatch attempt was made but its outcome was never
			// recorded, and this integration has no retry identity —
			// terminal, ambiguous, until an operator resolves it.
			row.State = nightCueStateAmbiguous
			row.Outcome = nightCueOutcomeAmbiguous
			row.OutcomeReason = "a dispatch attempt was made but its outcome was never recorded, and this action has no stable retry identity; end-session and prepare-site again to recover"
			resolvedAt := now
			row.ResolvedAt = &resolvedAt
			if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
				return store.NightCueOutboxRecord{}, err
			}
			return row, nil
		}
		idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cue.Name)
		return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cue.Name, nightAnnouncementDeclaredTarget(cue, action.Target), idemKey, issuer, row.ActionRevision)

	default:
		return store.NightCueOutboxRecord{}, fmt.Errorf("api: night cue outbox row %s/%d/%s/%s has unrecognized state %q", rec.ID, rec.Cycle, phase, cue.Name, row.State)
	}
}

// nightRunCue drives cueName through the outbox for rec's current cycle
// and phase. If no row exists yet, it resolves the action, commits it (as
// the atomic §7.1.1 boundary when isFirstOutwardCue, or an ordinary row
// otherwise), and dispatches. If a row already exists — including after a
// restart — it resumes via [nightResumeCueRow]: the ordinary advance and
// crash recovery are the same code, reached with a different row state.
func (h *handlers) nightRunCue(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase string, cue config.NightSessionCue, issuer FPPCommandIssuer, isFirstOutwardCue bool) (store.NightCueOutboxRecord, error) {
	existing, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
	switch {
	case err == nil:
		return h.nightResumeCueRow(ctx, now, rec, phase, cue, existing, issuer)
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		// fall through: nothing committed yet.
	default:
		return store.NightCueOutboxRecord{}, err
	}

	action, revision, err := nightResolveShowAction(ctx, h.deps.Config, cue.Action)
	if err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	if isFirstOutwardCue && !nightCueConfirmable(action.Target) {
		return store.NightCueOutboxRecord{}, errNightCueNotConfirmableForFirst
	}

	if isFirstOutwardCue {
		committed, cerr := h.nightCommitFirstCue(ctx, now, rec, phase, cue.Name, revision)
		if cerr != nil && !errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
			return store.NightCueOutboxRecord{}, cerr
		}
		if !committed {
			row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
			if rerr == nil {
				return h.nightResumeCueRow(ctx, now, rec, phase, cue, row, issuer)
			}
			return store.NightCueOutboxRecord{}, errNightCueSessionMoved
		}
	} else if cerr := h.nightCommitCueRow(ctx, now, rec, phase, cue.Name, revision); cerr != nil {
		if errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
			row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
			if rerr != nil {
				return store.NightCueOutboxRecord{}, rerr
			}
			return h.nightResumeCueRow(ctx, now, rec, phase, cue, row, issuer)
		}
		return store.NightCueOutboxRecord{}, cerr
	}

	idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cue.Name)
	return h.nightDispatchAndPersistCue(ctx, now, rec, phase, cue.Name, nightAnnouncementDeclaredTarget(cue, action.Target), idemKey, issuer, revision)
}

// nightBarrierResolutionDeadline bounds how long a barrier cue may hold
// the launch, measured from referenceE (never a wall-clock time of day).
// Applies to nightCueStatePending (times out failed) and
// nightCueStateDispatched (times out unconfirmed); never to
// nightCueStateAmbiguous, which stays terminal until an operator acts.
const nightBarrierResolutionDeadline = 60 * time.Second

// nightTimeoutBarrierCue resolves a barrier cue once
// nightBarrierResolutionDeadline has passed, past row's own pre-timeout
// state (see [nightBarrierResolutionDeadline] for which states and why).
func (h *handlers) nightTimeoutBarrierCue(ctx context.Context, now time.Time, row store.NightCueOutboxRecord) (store.NightCueOutboxRecord, error) {
	outcome, verb := nightCueOutcomeUnconfirmed, "no confirming or contradicting evidence arrived"
	if row.State == nightCueStatePending {
		outcome, verb = nightCueOutcomeFailed, "it was never dispatched"
	}
	row.State = nightCueStateResolved
	row.Outcome = outcome
	row.OutcomeReason = fmt.Sprintf("cue %q: %s within %s of the content boundary; the show-launch barrier does not wait indefinitely", row.CueName, verb, nightBarrierResolutionDeadline)
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if err := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	return row, nil
}

// nightRecordMissingBarrierCue durably records a barrier cue that never
// got an outbox row, as a resolved failure. Its own onFailure policy then
// decides whether the launch proceeds, exactly as for any other resolved
// row. A row that appeared in the meantime is returned instead.
func (h *handlers) nightRecordMissingBarrierCue(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string) (store.NightCueOutboxRecord, error) {
	resolvedAt := now
	row := store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: phase, CueName: cueName,
		State: nightCueStateResolved, Outcome: nightCueOutcomeFailed, ResolvedAt: &resolvedAt,
		OutcomeReason: fmt.Sprintf("cue %q: no outbox record could be created for it within %s of the content boundary, so it was never dispatched", cueName, nightBarrierResolutionDeadline),
	}
	err := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, row, now)
	if errors.Is(err, store.ErrNightCueOutboxDuplicate) {
		return h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	}
	if err != nil {
		return store.NightCueOutboxRecord{}, err
	}
	return row, nil
}

// nightBarrierSatisfied reports whether every barrier cue in cues has
// reached [nightCueStateResolved] with an outcome its own onFailure
// policy accepts. referenceE anchors the deadline. A cue with no row yet,
// pending or dispatched past the deadline, or ambiguous, all fail this
// check until the deadline (where applicable) resolves them; a resolved
// cue satisfies it unless its own onFailure is "abort" and its outcome is
// not confirmed.
func (h *handlers) nightBarrierSatisfied(ctx context.Context, now, referenceE time.Time, rec store.NightSessionRecord, phase string, cues []config.NightSessionCue) (bool, string, error) {
	for _, cue := range cues {
		if !cue.Barrier {
			continue
		}
		// A cue's deadline runs from when it becomes due, not from E: an
		// offset cue is legitimately undispatched until its own offset
		// elapses, and recording that as a failure would both fabricate
		// one and stop the cue ever running.
		dueAt := referenceE.Add(time.Duration(cue.OffsetMs) * time.Millisecond)
		row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
		if errors.Is(err, store.ErrNightCueOutboxNotFound) {
			if now.Sub(dueAt) < nightBarrierResolutionDeadline {
				return false, fmt.Sprintf("barrier cue %q has not been dispatched yet", cue.Name), nil
			}
			// The row could not be created at all, so the deadline has to
			// apply to its absence too, and durably: a barrier that stays
			// unrepresented holds the launch forever with nothing on the
			// operator surface to explain it.
			row, err = h.nightRecordMissingBarrierCue(ctx, now, rec, phase, cue.Name)
			if err != nil {
				return false, "", err
			}
		} else if err != nil {
			return false, "", err
		}
		timedOutState := row.State == nightCueStateDispatched || row.State == nightCueStatePending
		if timedOutState && now.Sub(dueAt) >= nightBarrierResolutionDeadline {
			row, err = h.nightTimeoutBarrierCue(ctx, now, row)
			if err != nil {
				return false, "", err
			}
		}
		if row.State != nightCueStateResolved {
			return false, fmt.Sprintf("barrier cue %q is %s, not resolved", cue.Name, row.State), nil
		}
		if row.Outcome != nightCueOutcomeConfirmed && cue.OnFailure == config.NightSessionCueOnFailureAbort {
			return false, fmt.Sprintf("barrier cue %q resolved %s and its onFailure is abort", cue.Name, row.Outcome), nil
		}
	}
	return true, "", nil
}

// sortedNightCues returns cues sorted by OffsetMs (stable): the first
// outward-facing cue is the earliest offset, not array position.
func sortedNightCues(cues []config.NightSessionCue) []config.NightSessionCue {
	out := make([]config.NightSessionCue, len(cues))
	copy(out, cues)
	sort.SliceStable(out, func(i, j int) bool { return out[i].OffsetMs < out[j].OffsetMs })
	return out
}

// nightAdvanceCueList runs every due cue (offset relative to referenceE).
// Only nightPhaseEnterShow carries the atomic show-commit boundary and a
// launch barrier; enterResting and fadeOut are fire-and-forget.
func (h *handlers) nightAdvanceCueList(ctx context.Context, now time.Time, rec store.NightSessionRecord, referenceE time.Time, phase string, cues []config.NightSessionCue, payload config.NightSessionPayload) (barrierSatisfied bool, blockedReason string) {
	isEnterShow := phase == nightPhaseEnterShow
	issuer := nightControllerIssuer(rec)
	if nightAttributionMissing(rec) && len(cues) > 0 {
		h.nightMarkAttributionDegraded(ctx, now, rec)
	}
	firstCommitted := !isEnterShow || rec.ShowCommitted

	for _, cue := range sortedNightCues(cues) {
		if now.Sub(referenceE) < time.Duration(cue.OffsetMs)*time.Millisecond {
			continue
		}
		isFirst := isEnterShow && !firstCommitted
		// An announcement cue carries its effective duck/mix/interrupt
		// policy into its own dispatch, where the node enforces it
		// (nightannouncement.go). Nothing here touches background audio.
		cue = nightAnnouncementCueWithResolvedPolicy(cue, payload)
		_, err := h.nightRunCue(ctx, now, rec, phase, cue, issuer, isFirst)
		if err != nil {
			if errors.Is(err, errNightCueSessionMoved) {
				return false, "the night session moved out from under this cue sequence"
			}
			if errors.Is(err, errNightCueNotConfirmableForFirst) {
				h.logWarn("night loop: refusing to commit an unconfirmable action as the first outward-facing cue", "sessionId", rec.ID, "cue", cue.Name)
			} else {
				h.logWarn("night loop: failed to run cue", "sessionId", rec.ID, "cue", cue.Name, "error", err)
			}
		} else if isFirst {
			firstCommitted = true
		}
	}

	if !isEnterShow {
		return true, ""
	}
	ok, reason, err := h.nightBarrierSatisfied(ctx, now, referenceE, rec, phase, cues)
	if err != nil {
		return false, "failed to evaluate cue barrier: " + err.Error()
	}
	return ok, reason
}
