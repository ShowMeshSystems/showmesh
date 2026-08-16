package macro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Reconcile's (run.go) own per-step decision: what a single
// unresolved step of a stranded run should be recorded as, now that
// "unresolved" no longer means "never attempted."
//
// The defect this closes: Reconcile used to resolve every unresolved step
// of a stranded run identically, to "skipped" with a generic restart
// reason, on the theory that a step this coordinator's own process never
// finished handling could not have reached anything outside itself. That
// is false for a step whose dispatch call was blocking (waiting on
// confirmation, or on an MQTT response) when the process was killed: the
// request already left this coordinator, and for an FPP step it already
// changed the show. Recording that as "skipped" — this package's own word
// for "never attempted" (vocab.go) — actively misinforms an operator
// reading the run afterward: measured live, a coordinator killed mid-macro
// left a step's own row reading skipped while its command journal showed
// the command dispatched, resolved unconfirmed by the command-side
// reconciler, and the volume it set was still in effect on the hardware.
//
// stepIdempotencyKey (step_fpp.go) is what makes this recoverable: because
// a step's idempotency key is derived deterministically from the run id and
// the step index, this package can always ask "does anything durable exist
// under this exact step's own key?" rather than trusting whatever
// macro_run_steps itself still says.

// resolveStrandedStep records one unresolved step of a stranded run —
// called only by Reconcile (run.go), only for a step whose ResolvedAt is
// nil, and only once, at startup, before this coordinator begins serving
// (mirroring [api.ReconcileStrandedFPPCommands]'s own timing argument one
// level down). It never dispatches, retries, or otherwise attempts to make
// further progress on a step — it only decides, from what is already
// durably recorded, whether the step reached the dispatch seam before the
// prior process stopped existing, and if so, what that seam's own evidence
// says happened.
//
// neverDispatchedReason is the reason recorded for a step this function
// determines genuinely never dispatched — plumbed in from Reconcile rather
// than declared here so there is exactly one copy of that string, matching
// this package's own precedent (skipRemaining's identical reason one file
// over) for "the run aborted/restarted before this step ran" wording.
func (e *Executor) resolveStrandedStep(ctx context.Context, runID string, step store.MacroRunStepRecord, now time.Time, neverDispatchedReason string) error {
	switch step.Integration {
	case config.ShowActionIntegrationFPP:
		return e.resolveStrandedFPPStep(ctx, runID, step, now, neverDispatchedReason)
	case config.ShowActionIntegrationMQTT:
		return e.resolveStrandedMQTTStep(ctx, runID, step, now, neverDispatchedReason)
	case config.ShowActionIntegrationResolume:
		return e.resolveStrandedResolumeStep(ctx, runID, step, now, neverDispatchedReason)
	default:
		// Unreachable given write-time validation (config.decodeShowAction
		// rejects any integration other than "fpp"/"mqtt") — answered
		// rather than left to silently fall through to one of the two
		// branches above, mirroring dispatchStep's own identical default
		// case in run.go.
		state := stepStateResolved
		outcome := outcomeFailed
		outcomeState := "unknown_integration"
		reason := fmt.Sprintf("this step names an integration %q this coordinator does not recognize, so startup reconciliation could not determine whether it ever dispatched", step.Integration)
		return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State: &state, ResolvedAt: &now, Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &reason,
		})
	}
}

// resolveStrandedFPPStep implements this file's own rule for an FPP step:
// look up a commands-table row under this step's own deterministic
// idempotency key. Its presence or absence is unambiguous, because
// dispatchFPPStep's own dispatch seam (fppcommand_dispatch.go's
// AuditedWrite) inserts that row and its dispatch audit entry together,
// before ever attempting the request FPP actually sees — so a crash before
// the row exists is a crash before anything reached FPP, and a crash after
// is recoverable from the row.
//
// Ordering matters here: [api.ReconcileStrandedFPPCommands] runs BEFORE
// this reconciler (coordinator.go wires them in that order, synchronously,
// both before the server starts listening), so by the time this function
// runs, any commands row this lookup finds has already had its own chance
// to resolve — the command-side reconciler resolves every unresolved "fpp"
// command it finds, unconditionally, not only ones a macro run dispatched.
// That is deliberate, not incidental: this function trusts the command's
// own ResolvedAt/OutcomeState/OutcomeReason as already-settled evidence
// rather than re-deriving confirmation a second time, matching this
// project's own "a shared rule is only shared where it is called" lesson —
// re-evaluating evidence here, in a second place, is exactly the mistake
// that lesson names. If that ordering is ever reversed, the defensive
// cmd.ResolvedAt == nil branch below is what keeps this function from
// calling a merely-dispatched-not-yet-resolved command "skipped" instead.
func (e *Executor) resolveStrandedFPPStep(ctx context.Context, runID string, step store.MacroRunStepRecord, now time.Time, neverDispatchedReason string) error {
	key := stepIdempotencyKey(runID, step.StepIndex)
	cmd, err := e.store.GetCommandByIdempotencyKey(ctx, key)
	if errors.Is(err, store.ErrCommandNotFound) {
		// No command row was ever created under this step's own key: the
		// prior process stopped existing before this step's dispatch call
		// even began, so nothing reached FPP. "skipped" is correct here.
		state := stepStateSkipped
		outcome := outcomeSkipped
		outcomeState := stepStateSkipped
		return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State: &state, ResolvedAt: &now, Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &neverDispatchedReason,
		})
	}
	if err != nil {
		return fmt.Errorf("look up dispatched command for step %d: %w", step.StepIndex, err)
	}

	var cmdID *string
	if cmd.ID != "" {
		id := cmd.ID
		cmdID = &id
	}
	state := stepStateResolved

	if cmd.ResolvedAt == nil {
		// Defensive only — see this function's own doc comment on
		// ordering. A command row exists, so this step DID dispatch, but
		// nothing has yet decided its outcome; stating that plainly is the
		// honest answer, not "skipped" and not a fabricated "confirmed".
		outcome := outcomeUnconfirmed
		outcomeState := "not_collected"
		reason := fmt.Sprintf(
			"this step dispatched command %s before the coordinator restarted, and that command had not yet resolved "+
				"when this run was reconciled: no confirming evidence is available", cmd.ID)
		return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State: &state, DispatchedAt: cmd.DispatchedAt, ResolvedAt: &now,
			Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &reason, CommandID: cmdID,
		})
	}

	// The command has its own resolved outcome — either it resolved
	// normally before the crash, or api.ReconcileStrandedFPPCommands
	// resolved it as stranded. Either way, propagate it rather than
	// re-deriving it: this step's own outcome word comes from the
	// command's ResultJSON (the same field
	// internal/coordinator/api/macroruns.go decodes for a live read of a
	// resolved step's command detail), and its reason is the command's own
	// OutcomeReason, unchanged.
	var result struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal([]byte(cmd.ResultJSON), &result)

	outcome := outcomeUnconfirmed
	reason := fmt.Sprintf("this step dispatched before the coordinator restarted; its command resolved %s: %s", result.Outcome, cmd.OutcomeReason)
	switch result.Outcome {
	case outcomeConfirmed:
		outcome = outcomeConfirmed
	case outcomeUnconfirmed:
		outcome = outcomeUnconfirmed
	default:
		// Never produced by either resolution path as of this writing
		// (both always write "confirmed" or "unconfirmed" — see
		// fppcommand_dispatch.go and fppcommand_reconcile.go), answered
		// rather than silently defaulted to a word this function cannot
		// actually attribute to the command's own recorded result.
		reason = fmt.Sprintf(
			"this step dispatched before the coordinator restarted; its command resolved with no recognized outcome word, "+
				"so this step's own outcome cannot be stated more specifically than unconfirmed: %s", cmd.OutcomeReason)
	}
	outcomeState := cmd.OutcomeState

	return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
		State: &state, DispatchedAt: cmd.DispatchedAt, ResolvedAt: cmd.ResolvedAt,
		Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &reason, CommandID: cmdID,
	})
}

// resolveStrandedMQTTStep implements this file's own rule for an MQTT
// step, which has no commands-table row to fall back on the way an FPP
// step does (step_mqtt.go's own dispatchMQTTStep writes only audit
// entries and, at the very end, this step's own row — there is no
// separate journal recording an in-flight publish). The one piece of
// durable evidence an MQTT step's dispatch leaves behind BEFORE the
// process could still be killed is its own DISPATCH audit entry
// (identity.AuditDispatch, written by dispatchMQTTStep before it ever
// calls Publish) — see [store.Store.FindAuditDispatchEntry]'s own doc
// comment for exactly what that entry does and does not prove.
//
// Its absence means this step's dispatch call never began: genuinely
// never attempted, so "skipped" is correct, identical to the FPP case's
// "no command row" branch. Its presence means dispatch began but this
// function cannot determine whether the publish reached the broker or a
// response arrived before the crash — there is no second source of
// evidence to consult the way an FPP step's command row provides. That is
// stated as unconfirmed with a reason that says exactly that, rather than
// picking "skipped" because it is the enum member available.
func (e *Executor) resolveStrandedMQTTStep(ctx context.Context, runID string, step store.MacroRunStepRecord, now time.Time, neverDispatchedReason string) error {
	key := stepIdempotencyKey(runID, step.StepIndex)
	entry, found, err := e.store.FindAuditDispatchEntry(ctx, key)
	if err != nil {
		return fmt.Errorf("look up dispatch audit entry for step %d: %w", step.StepIndex, err)
	}
	if !found {
		state := stepStateSkipped
		outcome := outcomeSkipped
		outcomeState := stepStateSkipped
		return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State: &state, ResolvedAt: &now, Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &neverDispatchedReason,
		})
	}

	state := stepStateResolved
	outcome := outcomeUnconfirmed
	outcomeState := mqttStateRestartInterrupted
	reason := "this step's dispatch was recorded as having begun before the coordinator restarted, but whether the " +
		"publish reached the broker, or a response arrived, cannot be established from what was recorded"
	dispatchedAt := entry.RecordedAt

	return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
		State: &state, DispatchedAt: &dispatchedAt, ResolvedAt: &now,
		Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &reason,
	})
}

// resolveStrandedResolumeStep implements this file's own rule for a
// Resolume step, which — like an MQTT step — has no commands-table row to
// fall back on (step_resolume.go's own dispatchResolumeStep writes only
// audit entries and, at the very end, this step's own row; there is no
// separate journal recording an in-flight dispatch). The one piece of
// durable evidence a Resolume step's dispatch leaves behind BEFORE the
// process could still be killed is its own DISPATCH audit entry
// (identity.AuditDispatch, written by dispatchResolumeStep before it ever
// calls Dispatch) — mirroring resolveStrandedMQTTStep's identical
// reasoning one function up, applied to the identical shape.
//
// Its absence means this step's dispatch call never began: genuinely
// never attempted, so "skipped" is correct, identical to the FPP and MQTT
// cases' "no evidence" branch. Its presence means dispatch began but this
// function cannot determine whether the request reached Resolume or
// resolved before the crash — there is no second source of evidence to
// consult. That is stated as unconfirmed with a reason that says exactly
// that, never "skipped" because it is the enum member available.
func (e *Executor) resolveStrandedResolumeStep(ctx context.Context, runID string, step store.MacroRunStepRecord, now time.Time, neverDispatchedReason string) error {
	key := stepIdempotencyKey(runID, step.StepIndex)
	entry, found, err := e.store.FindAuditDispatchEntry(ctx, key)
	if err != nil {
		return fmt.Errorf("look up dispatch audit entry for step %d: %w", step.StepIndex, err)
	}
	if !found {
		state := stepStateSkipped
		outcome := outcomeSkipped
		outcomeState := stepStateSkipped
		return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
			State: &state, ResolvedAt: &now, Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &neverDispatchedReason,
		})
	}

	state := stepStateResolved
	outcome := outcomeUnconfirmed
	outcomeState := resolumeStateRestartInterrupted
	reason := "this step's dispatch was recorded as having begun before the coordinator restarted, but whether the " +
		"request reached Resolume, or resolved, cannot be established from what was recorded"
	dispatchedAt := entry.RecordedAt

	return e.store.UpdateMacroRunStepOutcome(ctx, runID, step.StepIndex, store.MacroRunStepOutcomeUpdate{
		State: &state, DispatchedAt: &dispatchedAt, ResolvedAt: &now,
		Outcome: &outcome, OutcomeState: &outcomeState, OutcomeReason: &reason,
	})
}
