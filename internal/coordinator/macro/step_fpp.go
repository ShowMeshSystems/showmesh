package macro

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// dispatchFPPStep dispatches one FPP-integration step through the section
// 4 in-process seam, [api.FPPCommandDispatcher.Dispatch] (via the
// fppDispatcher interface, macro.go), and maps its three-way return
// (outcome, *v1.Problem, error) onto this package's five-value step
// vocabulary.
//
// Dispatch immediately, always. This package never consults
// [fppDispatcher.NextNudgeAt] at all, before or after dispatch, and the
// reason is a seam gap rather than a preference. It is recorded here
// because the next person to read section 6.3 will come looking for the
// reservation it describes and will not find one.
//
// [api.FPPCommandDispatcher.Dispatch] performs its OWN complete
// dispatch-then-confirm cycle inside one blocking call: it nudges the
// collector once, then polls the observation store on its own interval
// until either fresh evidence resolves the command or its own confirmation
// deadline elapses, and it returns only once that whole cycle is over.
// There is therefore no second, executor-owned confirmation read for a
// reservation to schedule. Re-invoking Dispatch with the same idempotency
// key would replay the row the first call already wrote and re-poll
// nothing.
//
// STEP-9-SPEC.md section 6.3 as written assumes an executor that owns its
// own confirmation read, and through this seam that executor does not
// exist. The consequence is bounded and is latency, not failure: a step
// dispatched inside the limiter's window has its nudge refused and waits
// for the collector's ordinary tick, which section 6.3's own arithmetic
// puts at roughly 15 s against a 20 s deadline. Section 2.2 as corrected
// means a blown margin marks the run unconfirmed and never stops the show.
//
// What this package does guarantee, and what acceptance criterion 18
// measures, is that dispatch itself is never delayed by that window. See
// nudge_test.go.
func (e *Executor) dispatchFPPStep(ctx context.Context, run store.MacroRunRecord, step store.MacroRunStepRecord, action resolvedAction, issuer api.FPPCommandIssuer) stepResult {
	target := action.Payload.Target

	in := api.FPPCommandInput{
		InstanceID:        target.InstanceID,
		Action:            target.Primitive,
		Params:            target.Params,
		IdempotencyKey:    stepIdempotencyKey(run.ID, step.StepIndex),
		Issuer:            issuer,
		RequestedRevision: store.FormatMacroRunRequestedRevision(run.MacroObjectID, run.MacroRevision),
	}

	// Dispatch first, unconditionally — see this function's own doc
	// comment for why NextNudgeAt is deliberately NOT consulted before
	// this call, and why this package does not call it at all: there is
	// no second, separate confirmation read in this seam for it to
	// schedule (Dispatch already owns the complete nudge-then-poll cycle
	// internally), so calling it here would read as using it while
	// actually discarding the result, which is worse than not calling it.
	outcome, problem, err := e.dispatch.Dispatch(ctx, in)

	if err != nil {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "internal_error",
			outcomeReason: "this step could not be dispatched because of an internal coordinator error",
			resolvedAt:    ptrTime(e.now()),
		}
	}
	if problem != nil {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "refused",
			outcomeReason: problem.Detail,
			resolvedAt:    ptrTime(e.now()),
		}
	}

	var cmdID *string
	if outcome.CommandID != "" {
		id := outcome.CommandID
		cmdID = &id
	}

	// A request that never reached FPP is a failure of the show, not a
	// gap in our own evidence, and the two go onto different policy
	// axes (ADR-031 decision 2). [api.FPPCommandOutcome.Outcome] cannot
	// tell them apart, because it is only ever "confirmed" or
	// "unconfirmed" and a powered-off host arrives as the latter. Before
	// this branch existed, a four-step macro against a dead host
	// dispatched nothing and the run reported completed: true, which is
	// the opposite of what "every step dispatched" means.
	//
	// The reason is written here rather than passed through:
	// outcome.OutcomeReason on this path interpolates the raw Go error,
	// so it carries a package path, an HTTP verb and the instance's
	// internal URL into a string an operator reads. What went wrong in
	// detail is in the command's own record and in the log.
	if outcome.DispatchFailed {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  outcome.OutcomeState,
			outcomeReason: fmt.Sprintf("the request to %s did not succeed, so this step never reached it", in.InstanceID),
			attrDegraded:  outcome.AttributionDegraded,
			commandID:     cmdID,
			dispatchedAt:  outcome.DispatchedAt,
			resolvedAt:    outcome.ResolvedAt,
		}
	}

	stepOutcome := outcomeUnconfirmed
	outcomeReason := outcome.OutcomeReason
	if outcome.Outcome == "confirmed" {
		stepOutcome = outcomeConfirmed
	}
	if outcomeReason == "" {
		outcomeReason = fmt.Sprintf("fpp command %s produced no outcome reason (outcome=%q)", outcome.CommandID, outcome.Outcome)
	}

	return stepResult{
		outcome:       stepOutcome,
		outcomeState:  outcome.OutcomeState,
		outcomeReason: outcomeReason,
		attrDegraded:  outcome.AttributionDegraded,
		commandID:     cmdID,
		dispatchedAt:  outcome.DispatchedAt,
		resolvedAt:    outcome.ResolvedAt,
	}
}

// stepIdempotencyKey derives one step's own idempotency key deterministically
// from the run id and the step index (STEP-9-SPEC.md section 6.2: "A
// step's idempotency key is derived deterministically from the run id and
// the step index. This makes reconciliation and any future retry safe by
// construction: a step that already dispatched cannot dispatch twice,
// because the key is already in commands.").
func stepIdempotencyKey(runID string, stepIndex int) string {
	return fmt.Sprintf("macro-step:%s:%d", runID, stepIndex)
}
