package macro

import (
	"context"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// resolumeAuditTarget mirrors resolumeActionTargetID
// (internal/coordinator/api/resolumeaction.go) by value: this package must
// not import that package.
const resolumeAuditTarget = "resolume"

// resolumeAuditParams builds one audit entry's Params: the run/step
// identity every step's entry carries, plus ref's own reference names
// (clip, deck, layer, ...) so an operator can tell which object a run
// addressed, plus resolvedID when known (blackout never has one; the
// dispatch entry, written before Dispatch runs, never has one either).
func resolumeAuditParams(run store.MacroRunRecord, step store.MacroRunStepRecord, ref map[string]any, resolvedID string) map[string]any {
	params := map[string]any{"runId": run.ID, "stepId": step.StepID, "stepIndex": step.StepIndex}
	for k, v := range ref {
		params[k] = v
	}
	if resolvedID != "" {
		params["resolvedId"] = resolvedID
	}
	return params
}

// dispatchResolumeStep dispatches one Resolume-integration step through
// the same [api.ResolumeActionDispatcher] the HTTP handler uses, so D-3's
// confirmation, deadlines, exempt class, deck term and identity gate all
// apply here for free. Every reference in target.Ref is a name (ADR-037),
// resolved fresh by Dispatch against the composition it reads at the
// instant it runs, which is what makes a stale reference refuse at run
// time rather than at write time.
//
// This function writes its own audit entries directly, matching
// dispatchMQTTStep (step_mqtt.go): [api.ResolumeActionDispatcher.Dispatch]
// is the raw dispatch engine, not an audited wrapper around it. A macro
// run never withholds a command for an unwritable audit store (ADR-035);
// a failed write only degrades attribution.
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
		Params:         resolumeAuditParams(run, step, target.Ref, ""),
	}

	attrDegraded := false
	if auditErr := e.identity.WriteAudit(ctx, dispatchEntry); auditErr != nil {
		attrDegraded = true
		e.logWarn("resolume step dispatched with degraded attribution: audit store unwritable, and a macro run never withholds a command for that",
			"runId", run.ID, "stepId", step.StepID, "action", target.Action, "cause", errString(auditErr))
	}

	dispatchedAt := e.now()
	result, err := e.resolumeActions.Dispatch(ctx, target.Action, target.Ref, dispatchedAt)

	var res stepResult
	var resolvedID string
	if err != nil {
		res = stepResult{
			outcome:       outcomeFailed,
			outcomeState:  "internal_error",
			outcomeReason: "this step could not be dispatched because of an internal coordinator error",
		}
	} else {
		res = mapResolumeActionResult(result)
		resolvedID = result.ResolvedID
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
		Params:         resolumeAuditParams(run, step, target.Ref, resolvedID),
	}
	// Best-effort, always: the exemption governs whether the step
	// dispatches, never whether its outcome is worth trying to record.
	if err := e.identity.WriteAudit(ctx, outcomeEntry); err != nil {
		res.attrDegraded = true
		e.logWarn("failed to write resolume step outcome audit entry", "runId", run.ID, "stepId", step.StepID, "error", err)
	}

	return res
}

// mapResolumeActionResult translates one [api.ResolumeActionResult] into
// this package's own five-value step vocabulary.
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
		// Unreachable given the closed five-member outcome vocabulary,
		// answered rather than silently treated as one of the five.
		res.outcome = outcomeFailed
		res.outcomeState = "unrecognized_outcome"
		res.outcomeReason = fmt.Sprintf("this coordinator's Resolume dispatcher returned an unrecognized outcome %q", result.Outcome)
	}
	return res
}
