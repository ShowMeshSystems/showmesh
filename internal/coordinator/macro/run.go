package macro

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// stepResult is one step's dispatch outcome, as step_fpp.go/step_mqtt.go
// report it back to executeRun — the common shape both integrations
// resolve to, regardless of which produced it.
type stepResult struct {
	outcome       string // one of the outcome* constants (vocab.go)
	outcomeState  string
	outcomeReason string
	attrDegraded  bool
	commandID     *string // FPP only; nil for MQTT (store.MacroRunStepRecord's own documented ambiguity)

	// publishAttempted is MQTT only, and is what decides whether the
	// step gets a dispatchedAt at all. A step whose broker identifier
	// never resolved, or whose broker connection was already down, put
	// nothing on any wire, and stamping it "dispatched at 17:00:03"
	// manufactures a fact on an evidence surface. Recording nil there
	// and stating the failure in the reason is the honest shape, the
	// same one fppCommandRecordFor's own "defect 9" settled for a
	// command that never dispatched.
	publishAttempted bool

	dispatchedAt *time.Time
	resolvedAt   *time.Time
}

// executeRun is the background step loop STEP-9-SPEC.md section 6.4
// describes: steps run in order, one at a time; for each step, dispatch
// through the section 4 seam (FPP) or the section 7 waiter (MQTT), record
// the outcome, then apply section 2.2's two independent policy axes.
//
// ctx is already detached from the submitting request (SubmitRun wraps it
// in context.WithoutCancel before starting this goroutine) — this method
// does not do that itself, so a test can drive it with an ordinary,
// cancellable context and assert cancellation has no effect on an
// in-flight dispatch, matching [api.FPPCommandDispatcher]'s own bgCtx
// contract one level up.
func (e *Executor) executeRun(ctx context.Context, rm resolvedMacro, run store.MacroRunRecord, steps []store.MacroRunStepRecord, issuer api.FPPCommandIssuer) {
	defer e.wg.Done()

	completed := true
	confirmed := true
	reason := ""
	attributionDegraded := run.AttributionDegraded

	for i := range steps {
		step := steps[i]
		stepDef := rm.Payload.Steps[i]
		action := rm.Actions[i]

		stepIssuer := issuer
		stepIssuer.RunID = run.ID

		res := e.dispatchStep(ctx, run, step, action, stepIssuer)

		e.persistStepOutcome(ctx, run.ID, step.StepIndex, res)
		if res.attrDegraded {
			attributionDegraded = true
			if err := e.store.SetMacroRunStepAttributionDegraded(ctx, run.ID, step.StepIndex); err != nil {
				e.logWarn("failed to record per-step attribution degradation", "runId", run.ID, "stepIndex", step.StepIndex, "error", err)
			}
			if err := e.store.SetMacroRunAttributionDegraded(ctx, run.ID); err != nil {
				e.logWarn("failed to record run-level attribution degradation", "runId", run.ID, "error", err)
			}
		}

		abort := false
		switch res.outcome {
		case outcomeConfirmed:
			// Run completed/confirmed unaffected.
		case outcomeUnconfirmable:
			confirmed = false
			if reason == "" {
				reason = fmt.Sprintf("step %q (index %d) is structurally unconfirmable: %s", step.StepID, i, res.outcomeReason)
			}
		case outcomeUnconfirmed:
			confirmed = false
			if reason == "" {
				reason = fmt.Sprintf("step %q (index %d) did not confirm: %s", step.StepID, i, res.outcomeReason)
			}
			if stepDef.OnUnconfirmed == config.ShowMacroOnUnconfirmedAbort {
				abort = true
				completed = false
			}
		case outcomeFailed:
			confirmed = false
			// completed is false because a STEP FAILED, not because the run
			// stopped. Those were the same condition while a failure
			// aborted by default; they stopped being the same on
			// 2026-08-14, when the owner made continue the default (see
			// config.ShowMacroOnFailureDefault). Leaving completed keyed on
			// abort would have made a run whose first step failed and whose
			// remaining steps succeeded report completed: true, which is
			// the "reports success regardless" failure ADR-029 names by
			// hand as worse than no report at all: the operator stops
			// reading it.
			completed = false
			if reason == "" {
				reason = fmt.Sprintf("step %q (index %d) failed: %s", step.StepID, i, res.outcomeReason)
			}
			if stepDef.OnFailure == config.ShowMacroOnFailureAbort {
				abort = true
			}
		}

		if abort {
			e.skipRemaining(ctx, run.ID, steps[i+1:])
			break
		}
	}

	finishedAt := e.now()
	if err := e.store.FinishMacroRun(ctx, run.ID, store.MacroRunFinishUpdate{
		FinishedAt:          finishedAt,
		Completed:           completed,
		Confirmed:           confirmed,
		Reason:              reason,
		AttributionDegraded: attributionDegraded,
	}); err != nil {
		e.logError("failed to finish macro run", "runId", run.ID, "error", err)
	}
	e.notifyChange()
}

// dispatchStep routes to the FPP dispatch seam or the MQTT waiter by the
// step's own pinned Integration.
func (e *Executor) dispatchStep(ctx context.Context, run store.MacroRunRecord, step store.MacroRunStepRecord, action resolvedAction, issuer api.FPPCommandIssuer) stepResult {
	switch action.Payload.Target.Integration {
	case config.ShowActionIntegrationFPP:
		return e.dispatchFPPStep(ctx, run, step, action, issuer)
	case config.ShowActionIntegrationMQTT:
		return e.dispatchMQTTStep(ctx, run, step, action, issuer)
	case config.ShowActionIntegrationResolume:
		return e.dispatchResolumeStep(ctx, run, step, action, issuer)
	case config.ShowActionIntegrationAudio:
		return e.dispatchAudioStep(ctx, run, step, action, issuer)
	default:
		// Reached whenever action.Payload.Target.Integration is a value
		// this switch does not name: NOT unreachable. Write-time
		// validation (config.DecodeShowActionPayload) only closes the
		// enum against the same list this switch itself must be kept in
		// sync with by hand — "fpp", "mqtt", "resolume", and "audio"
		// today — so this branch is what actually answers a stored row
		// hand-edited to carry a fifth value, or a future integration
		// added to the decoder's enum without a matching case added here.
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "unknown_integration",
			outcomeReason: fmt.Sprintf("action %q names an unrecognized integration %q", action.ObjectID, action.Payload.Target.Integration),
			resolvedAt:    ptrTime(e.now()),
		}
	}
}

// persistStepOutcome writes res into step's row. A store failure here is
// logged, never returned or retried: the run's own execution has already
// happened (or been refused) by the time this is called, and this
// package's standing rule (matching fppcommand_dispatch.go's identical
// choice for updateCommandOutcomeBounded) is that a bookkeeping write
// failure must not re-attempt or undo an action that has already taken
// effect in the real world.
func (e *Executor) persistStepOutcome(ctx context.Context, runID string, stepIndex int, res stepResult) {
	// dispatchStep always returns a terminal result (this package has no
	// partial/in-progress step write; the dispatch seam and the MQTT
	// waiter each block until they have one), so this is always the
	// step's own final "resolved" state — never "dispatched" as an
	// intermediate write a later call would overtake.
	state := stepStateResolved
	upd := store.MacroRunStepOutcomeUpdate{
		State:         &state,
		DispatchedAt:  res.dispatchedAt,
		ResolvedAt:    res.resolvedAt,
		Outcome:       strPtr(res.outcome),
		OutcomeState:  strPtr(res.outcomeState),
		OutcomeReason: strPtr(res.outcomeReason),
		CommandID:     res.commandID,
	}
	if err := e.store.UpdateMacroRunStepOutcome(ctx, runID, stepIndex, upd); err != nil {
		e.logError("failed to record macro run step outcome", "runId", runID, "stepIndex", stepIndex, "error", err)
	}
}

// skipRemaining marks every step in rest "skipped" — STEP-9-SPEC.md
// section 6.4: "steps after an abort are skipped, which is not a
// failure."
func (e *Executor) skipRemaining(ctx context.Context, runID string, rest []store.MacroRunStepRecord) {
	now := e.now()
	for _, step := range rest {
		state := stepStateSkipped
		outcome := outcomeSkipped
		outcomeState := stepStateSkipped
		outcomeReason := "the run aborted at an earlier step; this step was never attempted"
		if err := e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State:         &state,
			ResolvedAt:    &now,
			Outcome:       &outcome,
			OutcomeState:  &outcomeState,
			OutcomeReason: &outcomeReason,
		}); err != nil {
			e.logError("failed to mark macro run step skipped", "runId", runID, "stepIndex", step.StepIndex, "error", err)
		}
	}
}

// Reconcile implements ADR-031 decision 4 / STEP-9-SPEC.md section 6.5:
// called once at coordinator startup, before the server begins listening,
// alongside [api.ReconcileStrandedFPPCommands]. Every run left "running"
// from a prior process is finished completed:false with a reason naming
// the restart; its remaining unresolved steps are resolved rather than
// left permanently blank — never retried, but also never assumed to have
// been "never dispatched": [resolveStrandedStep] (reconcile_step.go)
// checks each one's own durable evidence (an FPP step's commands row, an
// MQTT step's dispatch audit entry) before deciding, so a step that
// genuinely reached FPP or a broker before the crash is recorded as
// dispatched, not skipped.
//
// This never starts a goroutine and never touches [Executor.wg]: a
// reconciled run is not resumed, so there is nothing to execute in the
// background for it.
func (e *Executor) Reconcile(ctx context.Context) error {
	running, err := e.store.ListRunningMacroRuns(ctx)
	if err != nil {
		return fmt.Errorf("macro: reconcile: list running runs: %w", err)
	}

	now := e.now()
	const restartReason = "the coordinator restarted while this run was in progress; some remaining steps may not have dispatched"
	const neverDispatchedReason = "the coordinator restarted before this step was dispatched or resolved"

	var firstErr error
	for _, run := range running {
		_, steps, err := e.store.GetMacroRun(ctx, run.ID)
		if err != nil {
			e.logError("macro reconcile: failed to read run steps", "runId", run.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		// Resolve every step this run left unresolved — not by blanket
		// marking them all "skipped" (the defect this loop used to have:
		// a step that had actually dispatched before the crash, an FPP
		// command that reached the hardware or an MQTT publish that
		// reached the broker, got recorded as though it had never been
		// attempted). resolveStrandedStep (reconcile_step.go) decides,
		// per step and per integration, whether this coordinator's own
		// durable records show the step reached its dispatch seam before
		// the prior process stopped existing, and if so, records what
		// that seam's own evidence says — never "skipped" for a step that
		// dispatched, regardless of whether it ever resolved.
		stepErr := false
		for _, st := range steps {
			if st.ResolvedAt != nil {
				continue // already resolved before the crash; untouched
			}
			if err := e.resolveStrandedStep(ctx, run.ID, st, now, neverDispatchedReason); err != nil {
				e.logError("macro reconcile: failed to resolve a stranded step", "runId", run.ID, "stepIndex", st.StepIndex, "error", err)
				stepErr = true
				break
			}
		}
		if stepErr {
			// Leave this run "running": a step-level failure here means
			// this coordinator could not determine what actually
			// happened to at least one step, which is exactly the case
			// where finishing the run anyway (with whatever the OTHER
			// steps say) risks the same "recorded skipped for a step
			// that dispatched" defect this loop exists to close. The
			// next restart's Reconcile call will retry this run from
			// scratch.
			if firstErr == nil {
				firstErr = fmt.Errorf("macro: reconcile: resolve stranded steps for run %q: a step could not be resolved", run.ID)
			}
			continue
		}

		// Re-read: confirmedSoFar must reflect what THIS reconciliation
		// just recorded above (a recovered step may itself have resolved
		// confirmed, or unconfirmed with its own evidence-backed reason),
		// not the pre-recovery snapshot, which — before a step's own
		// dispatch is recovered — cannot tell "dispatched" apart from
		// "never touched."
		//
		// A run is confirmed only if every step actually produced
		// post-dispatch evidence, so an interrupted run whose steps
		// never resolved is not confirmed. It is not "confirmed
		// vacuously": ADR-031 decision 3 defines confirmed as "every
		// step produced post-dispatch evidence that its effect
		// occurred", and a step that never ran produced none.
		//
		// The first version of this loop skipped unresolved steps and
		// started from true, so a coordinator that restarted one second
		// after accepting a run finished it with every step reading
		// skipped and the run itself reading confirmed. §2.3 requires
		// the surfaces to render the two booleans distinctly, which
		// makes that reading actively misleading rather than merely
		// wrong.
		_, steps, err = e.store.GetMacroRun(ctx, run.ID)
		if err != nil {
			e.logError("macro reconcile: failed to re-read run steps after resolving them", "runId", run.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		confirmedSoFar := len(steps) > 0
		for _, st := range steps {
			if st.ResolvedAt == nil || st.Outcome != outcomeConfirmed {
				confirmedSoFar = false
				break
			}
		}

		if err := e.store.FinishMacroRun(ctx, run.ID, store.MacroRunFinishUpdate{
			FinishedAt:          now,
			Completed:           false,
			Confirmed:           confirmedSoFar,
			Reason:              restartReason,
			AttributionDegraded: run.AttributionDegraded,
		}); err != nil {
			e.logError("macro reconcile: failed to finish stranded run", "runId", run.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		e.logWarn("macro run finished by startup reconciliation: coordinator restarted mid-run", "runId", run.ID, "macroId", run.MacroObjectID)
	}

	if len(running) > 0 {
		e.notifyChange()
	}
	return firstErr
}
