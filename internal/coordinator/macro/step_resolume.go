package macro

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// resolumeAuditTarget mirrors resolumeActionTargetID
// (internal/coordinator/api/resolumeaction.go: "resolume") by value — this
// package must not import that package (macro imports api, so api must
// never import macro), matching this codebase's established convention for
// the identical tradeoff (resolumeCompositionConfigKind,
// resolumeActionCoordinatorRequiredLabel, ...).
const resolumeAuditTarget = "resolume"

// dispatchResolumeStep dispatches one Resolume-integration step
// (TRACK-D-SEAM-C-MACRO-SPEC.md section 3) through the SAME
// [api.ResolumeActionDispatcher] the HTTP handler uses
// (resolumeaction.go's handleDispatchResolumeAction) — this package builds
// no second dispatch path, so D-3's confirmation, deadlines, exempt class,
// deck term and identity gate all apply here unchanged. Every reference in
// action.Payload.Target.Ref is a NAME (ADR-037): Dispatch resolves it
// against the composition it reads at the instant it runs, never against a
// value this package cached at write time, which is what makes rule 2
// ("a reference that no longer resolves is refused at run time") true for
// free.
//
// This package writes its own audit entries directly against
// identity.Service, the identical shape dispatchMQTTStep (step_mqtt.go)
// uses one file over, because — like MQTT — there is no pre-built
// in-process dispatch seam that already does it:
// [api.ResolumeActionDispatcher.Dispatch] is the raw D-3/A engine, not the
// audited HTTP-handler wrapper around it (that wrapper's own bookkeeping,
// the commands-table insert and its idempotency-replay handling, is an
// HTTP-request concern this package does not reproduce — a macro step's
// own idempotency is already the run's own [stepIdempotencyKey]).
//
// ADR-035 decision 1 applies unconditionally here exactly as it does for
// MQTT: every step dispatches whatever the audit store's health, and a
// failed audit write only degrades attribution, never withholds the
// command. This is what makes blackout and clearLayer — and every other
// Resolume action — undeniable by an audit outage inside a run.
func (e *Executor) dispatchResolumeStep(ctx context.Context, run store.MacroRunRecord, step store.MacroRunStepRecord, action resolvedAction, issuer api.FPPCommandIssuer) stepResult {
	target := action.Payload.Target
	if e.resolumeActions == nil {
		return stepResult{
			outcome:       outcomeFailed,
			outcomeState:  resolumeStateNotConfigured,
			outcomeReason: "no Resolume action dispatcher is configured on this coordinator",
			resolvedAt:    ptrTime(e.now()),
		}
	}

	now := e.now()
	stepKey := stepIdempotencyKey(run.ID, step.StepIndex)
	auditAction := "resolume." + target.Action
	dispatchEntry := identity.AuditEntry{
		Timestamp:      now,
		PrincipalID:    issuer.PrincipalID,
		PrincipalName:  issuer.PrincipalName,
		Form:           issuer.Form,
		CredentialID:   issuer.CredentialID,
		ClientAddr:     issuer.ClientAddr,
		Action:         auditAction,
		Target:         resolumeAuditTarget,
		IdempotencyKey: stepKey,
		Kind:           identity.AuditDispatch,
		Params:         map[string]any{"runId": run.ID, "stepId": step.StepID, "stepIndex": step.StepIndex},
	}

	// OWNER DECISION, 2026-08-14: a macro run never withholds a command
	// because the audit store is down, whatever this step's safety class —
	// see step_mqtt.go's identical dispatchMQTTStep for the MQTT half of
	// this same rule, and [api.FPPCommandInput.NeverWithholdOnAuditFailure]
	// for the FPP half.
	attrDegraded := false
	if auditErr := e.identity.WriteAudit(ctx, dispatchEntry); auditErr != nil {
		attrDegraded = true
		e.logWarn("resolume step dispatched with degraded attribution: audit store unwritable, and a macro run never withholds a command for that",
			"runId", run.ID, "stepId", step.StepID, "action", target.Action, "cause", errString(auditErr))
	}

	dispatchedAt := e.now()
	result, err := e.resolumeActions.Dispatch(ctx, target.Action, target.Ref, dispatchedAt)

	var res stepResult
	if err != nil {
		res = stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "internal_error",
			outcomeReason: "this step could not be dispatched because of an internal coordinator error",
		}
	} else {
		res = mapResolumeActionResult(result)
	}
	res.attrDegraded = res.attrDegraded || attrDegraded
	if res.resolvedAt == nil {
		t := e.now()
		res.resolvedAt = &t
	}

	outcomeEntry := identity.AuditEntry{
		Timestamp:      *res.resolvedAt,
		PrincipalID:    issuer.PrincipalID,
		PrincipalName:  issuer.PrincipalName,
		Form:           issuer.Form,
		CredentialID:   issuer.CredentialID,
		ClientAddr:     issuer.ClientAddr,
		Action:         auditAction,
		Target:         resolumeAuditTarget,
		IdempotencyKey: stepKey,
		Kind:           identity.AuditOutcome,
		Outcome:        res.outcome,
		OutcomeState:   res.outcomeState,
		OutcomeReason:  res.outcomeReason,
		Params:         map[string]any{"runId": run.ID, "stepId": step.StepID, "stepIndex": step.StepIndex},
	}
	// Best-effort, always — mirroring dispatchMQTTStep's identical outcome
	// audit entry, "for every step regardless of safety class": the
	// exemption governs whether the STEP dispatches, never whether its
	// outcome is worth trying to record.
	if err := e.identity.WriteAudit(ctx, outcomeEntry); err != nil {
		res.attrDegraded = true
		e.logWarn("failed to write resolume step outcome audit entry", "runId", run.ID, "stepId", step.StepID, "error", err)
	}

	return res
}

// mapResolumeActionResult translates one [api.ResolumeActionResult] into
// this package's own five-value step vocabulary — a straight rename, since
// D-3/A's own outcome vocabulary already IS this package's evidence
// contract (ADR-003, ADR-029):
// [api.ResolumeOutcomeRefused] becomes outcomeFailed exactly as an FPP
// refusal does in dispatchFPPStep (step_fpp.go), never a sixth "refused"
// outcome this package's stored vocabulary does not have.
func mapResolumeActionResult(result api.ResolumeActionResult) stepResult {
	res := stepResult{
		outcomeReason: result.Reason,
		dispatchedAt:  result.DispatchedAt,
		resolvedAt:    result.ResolvedAt,
	}
	switch result.Outcome {
	case api.ResolumeOutcomeConfirmed:
		res.outcome = outcomeConfirmed
		res.outcomeState = resolumeStateConfirmed
	case api.ResolumeOutcomeUnconfirmed:
		res.outcome = outcomeUnconfirmed
		res.outcomeState = resolumeStateUnconfirmed
	case api.ResolumeOutcomeUnconfirmable:
		res.outcome = outcomeUnconfirmable
		res.outcomeState = resolumeStateUnconfirmable
	case api.ResolumeOutcomeRefused:
		res.outcome = outcomeFailed
		res.outcomeState = resolumeStateRefused
	case api.ResolumeOutcomeFailed:
		res.outcome = outcomeFailed
		res.outcomeState = resolumeStateFailed
	default:
		// Unreachable given api.ResolumeActionResult's own closed
		// five-member outcome vocabulary, answered rather than silently
		// treated as any one of the five if that vocabulary and this
		// switch ever disagree.
		res.outcome = outcomeFailed
		res.outcomeState = "unrecognized_outcome"
		res.outcomeReason = fmt.Sprintf("this coordinator's Resolume dispatcher returned an unrecognized outcome %q", result.Outcome)
	}
	return res
}
