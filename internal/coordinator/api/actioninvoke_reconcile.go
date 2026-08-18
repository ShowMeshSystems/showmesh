package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file closes the same gap ReconcileStrandedResolumeActions
// (resolumeaction_reconcile.go) closes for a second command family: an
// action invocation a prior process left dispatched but never resolved
// replays outcome="" forever, since nothing else in this package resolves
// a row whose TargetKind is actionInvokeTargetKind. Same shape, same
// non-fatal, synchronous, before-ListenAndServe call site, and the same
// "resolved unconfirmed, never re-derive confirmation retroactively"
// posture.
func ReconcileStrandedActionInvocations(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) (resolved int, err error) {
	deps = deps.withDefaults()

	unresolved, err := deps.Commands.ListUnresolvedCommands(ctx)
	if err != nil {
		return 0, fmt.Errorf("api: reconcile stranded action invocations: list unresolved commands: %w", err)
	}

	for _, rec := range unresolved {
		if rec.TargetKind != actionInvokeTargetKind {
			continue
		}

		nowT := now()
		outcomeState := string(observation.StateNotCollected)
		reason := "resolved by startup reconciliation, not by this invocation's own original request (a restart or " +
			"an abandoned connection left it dispatched but never resolved): this coordinator does not re-run an " +
			"action's own confirmation rules retroactively, so it reports unconfirmed rather than guessing whether " +
			"the action's effect actually landed"

		resultJSON, _ := json.Marshal(actionInvokeResultPayload{Outcome: outcomeWordUnconfirmed})
		resultStr := string(resultJSON)
		resolvedState := "resolved"
		if err := deps.Commands.UpdateCommandOutcome(ctx, rec.ID, store.CommandOutcomeUpdate{
			ResolvedAt: &nowT, State: &resolvedState, ResultJSON: &resultStr,
			OutcomeState: &outcomeState, OutcomeReason: &reason,
		}); err != nil {
			if logger != nil {
				logger.Warn("api: failed to resolve a stranded action invocation", "commandId", rec.ID, "error", err)
			}
			continue
		}
		resolved++

		// Best-effort outcome audit entry, attributed to the ORIGINAL
		// issuer — there is no live request at coordinator startup to
		// attribute this to instead.
		if err := deps.Identity.WriteAudit(ctx, identity.AuditEntry{
			Timestamp: nowT, PrincipalID: rec.IssuerPrincipalID, PrincipalName: rec.IssuerPrincipalName,
			Action: rec.Action, Target: rec.TargetID, IdempotencyKey: rec.IdempotencyKey,
			Kind: identity.AuditOutcome, CommandID: rec.ID,
			Outcome: outcomeWordUnconfirmed, OutcomeState: outcomeState, OutcomeReason: reason,
		}); err != nil && logger != nil {
			logger.Warn("api: audit write failed while resolving a stranded action invocation; outcome was still recorded",
				"commandId", rec.ID, "error", err)
		}

		if logger != nil {
			logger.Warn("api: resolved an action invocation left stranded by a prior process",
				"commandId", rec.ID, "action", rec.Action, "target", rec.TargetID, "outcome", outcomeWordUnconfirmed)
		}
	}

	return resolved, nil
}
