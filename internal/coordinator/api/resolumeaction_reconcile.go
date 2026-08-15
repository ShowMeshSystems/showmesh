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

// This file is Review fix 1's other half (2026-08-15):
// ReconcileStrandedFPPCommands (fppcommand_reconcile.go) skips every row
// whose TargetKind is not "fpp", and nothing else in this package ever
// resolved a Resolume action row a prior process left
// dispatched-but-unresolved. That row's idempotency key replays
// outcome="" and outcomeReason="" forever — the SAME narrow, accepted race
// resolveResolumeActionReplay's own doc comment names for a REPLAY
// answered before the original request finishes, except with no
// reconciliation pass to ever close it, that "temporary" race becomes
// permanent for the life of the database. ReconcileStrandedResolumeActions
// closes it, following ReconcileStrandedFPPCommands' own shape: called
// once, synchronously, before this coordinator starts accepting requests
// (see this function's own call site in coordinator.go).
//
// This deliberately does NOT re-run [ResolumeActionDispatcher]'s own
// per-action confirmation rules (the derived deadline, the pre-dispatch
// baseline, the deck refusal) the way ReconcileStrandedFPPCommands re-runs
// an fppPrimitive's Confirm predicate against fresh observations: doing
// that would require importing internal/coordinator/collector/resolume
// into this package, exactly the production coupling
// resolumeaction_interfaces.go's own doc comment (and resolumeaction.go's
// sibling constants) already refuse to take on, and it would mean this
// startup sweep re-implements — and could disagree with — D-3/A's own
// dispatch-time rules for what counts as confirming evidence for a given
// action. So every stranded Resolume row is resolved "unconfirmed" with a
// reason that says exactly that: this coordinator will not guess whether a
// Resolume action's effect actually landed once the process that
// dispatched it is gone. "unconfirmed" is a claim about THIS
// coordinator's own evidence pipeline (TRACK-D-D3-SPEC.md section 3.3's
// closing rule — "unconfirmed... is a claim about ShowMesh's own evidence
// pipeline"), never a claim that the action failed.
func ReconcileStrandedResolumeActions(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) (resolved int, err error) {
	deps = deps.withDefaults()

	unresolved, err := deps.Commands.ListUnresolvedCommands(ctx)
	if err != nil {
		return 0, fmt.Errorf("api: reconcile stranded resolume actions: list unresolved commands: %w", err)
	}

	for _, rec := range unresolved {
		if rec.TargetKind != resolumeActionTargetKind {
			continue
		}

		nowT := now()
		// StateNotCollected, uniformly: no attempt was made to re-evaluate
		// this row's confirming evidence (see this file's own top comment
		// for why), which is exactly what that state means.
		outcomeState := string(observation.StateNotCollected)
		reason := "resolved by startup reconciliation, not by this action's own original request (a restart or an " +
			"abandoned connection left it dispatched but never resolved): this coordinator does not re-run a " +
			"Resolume action's own confirmation rules retroactively, so it reports unconfirmed rather than guessing " +
			"whether the action's effect actually landed"

		resultJSON, _ := json.Marshal(resolumeActionResultPayload{Outcome: string(ResolumeOutcomeUnconfirmed)})
		resultStr := string(resultJSON)
		resolvedState := "resolved"
		if err := deps.Commands.UpdateCommandOutcome(ctx, rec.ID, store.CommandOutcomeUpdate{
			ResolvedAt: &nowT, State: &resolvedState, ResultJSON: &resultStr,
			OutcomeState: &outcomeState, OutcomeReason: &reason,
		}); err != nil {
			if logger != nil {
				logger.Warn("api: failed to resolve a stranded resolume action", "commandId", rec.ID, "error", err)
			}
			continue
		}
		resolved++

		// Best-effort outcome audit entry, correlated by rec.ID — the SAME
		// "degrade, never refuse, never block" posture
		// ReconcileStrandedFPPCommands' own identical write uses.
		// Attributed to the ORIGINAL issuer: there is no live request or
		// authenticated caller at coordinator startup to attribute this to
		// instead, and the original issuer is who actually asked for this
		// action's effect.
		if err := deps.Identity.WriteAudit(ctx, identity.AuditEntry{
			Timestamp: nowT, PrincipalID: rec.IssuerPrincipalID, PrincipalName: rec.IssuerPrincipalName,
			Action: rec.Action, Target: rec.TargetID, IdempotencyKey: rec.IdempotencyKey,
			Kind: identity.AuditOutcome, CommandID: rec.ID,
			Outcome: string(ResolumeOutcomeUnconfirmed), OutcomeState: outcomeState, OutcomeReason: reason,
		}); err != nil && logger != nil {
			logger.Warn("api: audit write failed while resolving a stranded resolume action; outcome was still recorded",
				"commandId", rec.ID, "error", err)
		}

		if logger != nil {
			logger.Warn("api: resolved a resolume action left stranded by a prior process",
				"commandId", rec.ID, "action", rec.Action, "target", rec.TargetID, "outcome", string(ResolumeOutcomeUnconfirmed))
		}
	}

	return resolved, nil
}
