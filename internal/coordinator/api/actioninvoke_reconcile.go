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
// replays outcome="" forever. Same shape, same non-fatal, synchronous,
// before-ListenAndServe call site, and the same "resolved unconfirmed,
// never re-derive confirmation retroactively" posture, except for an
// FPP-targeted invocation's nested child command row (see
// reconcileActionInvokeOutcome).
//
// Invariant: coordinator.go's ReconcileStrandedFPPCommands reconciles
// every stranded FPP command before this sweep runs, so this function
// must read an outer row's FPP child rather than overwrite its outcome.
func ReconcileStrandedActionInvocations(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) (resolved int, err error) {
	return reconcileStrandedActionInvocations(ctx, deps, now, logger, 0)
}

// actionInvokeReconcileMinAge is how old an unresolved row must be before
// [RunActionInvokeReconciliationLoop]'s periodic sweep will touch it.
//
// Invariant: unlike the one-shot startup call (which runs before this
// coordinator accepts its first request, so "unresolved" and "stranded"
// are the same thing there), a periodic sweep runs alongside genuinely
// in-flight requests. Without this minimum age, the sweep could resolve
// a live request's row out from under it.
const actionInvokeReconcileMinAge = actionInvokeHTTPWriteDeadline + 30*time.Second

// actionInvokeReconcileInterval is how often [RunActionInvokeReconciliationLoop]
// re-scans, mirroring fppCollectorReconcileInterval/assetSettingsReconcileInterval's
// own 10s precedent (internal/coordinator).
const actionInvokeReconcileInterval = 10 * time.Second

// RunActionInvokeReconciliationLoop retries the action-invocation sweep
// on actionInvokeReconcileInterval until ctx is done, so a row that
// becomes stranded after the one-shot startup sweep ran (a commands-table
// write failing right after a successful dispatch, actioninvoke.go's own
// persistErr branch) self-heals without waiting for a restart. Errors are
// logged and never fatal, matching the startup call's own posture
// (ADR-024 constraint 23: "you cannot act", never "you cannot see").
func RunActionInvokeReconciliationLoop(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) {
	runActionInvokeReconciliationLoop(ctx, deps, now, logger, actionInvokeReconcileInterval)
}

// runActionInvokeReconciliationLoop is [RunActionInvokeReconciliationLoop]
// with its own poll interval injectable, so a test can observe more than
// one pass without waiting actionInvokeReconcileInterval in real time.
func runActionInvokeReconciliationLoop(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := reconcileStrandedActionInvocations(ctx, deps, now, logger, actionInvokeReconcileMinAge)
			switch {
			case err != nil:
				if logger != nil {
					logger.Warn("api: periodic action-invocation reconciliation failed", "error", err)
				}
			case n > 0:
				if logger != nil {
					logger.Warn("api: periodic reconciliation resolved action invocations left stranded", "count", n)
				}
			}
		}
	}
}

func reconcileStrandedActionInvocations(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger, minAge time.Duration) (resolved int, err error) {
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
		if minAge > 0 && nowT.Sub(rec.CreatedAt) < minAge {
			// Too young to be provably stranded rather than genuinely
			// in-flight — see actionInvokeReconcileMinAge's own doc
			// comment. Left untouched; a later pass will reconsider it.
			continue
		}

		outcomeWord, outcomeState, reason := reconcileActionInvokeOutcome(ctx, deps, rec)

		var stored actionInvokeResultPayload
		_ = json.Unmarshal([]byte(rec.ResultJSON), &stored)
		resultJSON, _ := json.Marshal(actionInvokeResultPayload{
			Label: stored.Label, Outcome: outcomeWord,
			DispatchAttribution: stored.DispatchAttribution, DispatchAttributionReason: stored.DispatchAttributionReason,
			OutcomeAttribution: attributionStateComplete, OutcomeAttributionReason: "resolved by startup reconciliation",
		})
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
		auditErr := deps.Identity.WriteAudit(ctx, identity.AuditEntry{
			Timestamp: nowT, PrincipalID: rec.IssuerPrincipalID, PrincipalName: rec.IssuerPrincipalName,
			Action: rec.Action, Target: rec.TargetID, IdempotencyKey: rec.IdempotencyKey,
			Kind: identity.AuditOutcome, CommandID: rec.ID,
			Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: reason,
		})
		if auditErr != nil {
			if logger != nil {
				logger.Warn("api: audit write failed while resolving a stranded action invocation; outcome was still recorded",
					"commandId", rec.ID, "error", auditErr)
			}
			// The reconciliation's own attribution must reflect that the
			// audit half of this resolution did NOT complete durably.
			degradedResult, _ := json.Marshal(actionInvokeResultPayload{
				Label: stored.Label, Outcome: outcomeWord,
				DispatchAttribution: stored.DispatchAttribution, DispatchAttributionReason: stored.DispatchAttributionReason,
				OutcomeAttribution: attributionStateDegraded, OutcomeAttributionReason: degradedAttributionReasonPostDispatch,
			})
			degradedStr := string(degradedResult)
			if err := deps.Commands.UpdateCommandOutcome(ctx, rec.ID, store.CommandOutcomeUpdate{ResultJSON: &degradedStr}); err != nil && logger != nil {
				logger.Warn("api: failed to record degraded attribution for a reconciled action invocation", "commandId", rec.ID, "error", err)
			}
		}

		if logger != nil {
			logger.Warn("api: resolved an action invocation left stranded by a prior process",
				"commandId", rec.ID, "action", rec.Action, "target", rec.TargetID, "outcome", outcomeWord)
		}
	}

	return resolved, nil
}

// reconcileActionInvokeOutcome consults rec's own deterministic FPP child
// (if any) before declaring unconfirmed. A confirmed child reconstructs a
// confirmed outer result; an absent or ambiguous child (no child row, or
// a child that itself resolved something other than confirmed) yields
// explicit unconfirmed with a reason — never a silent guess and never an
// overwrite of stronger evidence.
func reconcileActionInvokeOutcome(ctx context.Context, deps Dependencies, rec store.CommandRecord) (outcomeWord, outcomeState, reason string) {
	child, err := deps.Commands.GetCommandByIdempotencyKey(ctx, actionInvokeFPPChildIdempotencyKeyPrefix+rec.ID)
	switch {
	case err == nil && child.State == "resolved":
		var childPayload commandResultPayload
		_ = json.Unmarshal([]byte(child.ResultJSON), &childPayload)
		if childPayload.Outcome == outcomeWordConfirmed {
			return outcomeWordConfirmed, string(observation.StateNotCollected), fmt.Sprintf(
				"resolved by startup reconciliation, not by this invocation's own original request (a restart or an "+
					"abandoned connection left it dispatched but never resolved): reconstructed from its own dispatched "+
					"FPP command %s, which startup reconciliation independently confirmed (%s)", child.ID, child.OutcomeReason)
		}
		return outcomeWordUnconfirmed, string(observation.StateNotCollected), fmt.Sprintf(
			"resolved by startup reconciliation, not by this invocation's own original request: its own dispatched "+
				"FPP command %s resolved %q rather than confirmed (%s)", child.ID, childPayload.Outcome, child.OutcomeReason)
	case err == nil:
		return outcomeWordUnconfirmed, string(observation.StateNotCollected), fmt.Sprintf(
			"resolved by startup reconciliation, not by this invocation's own original request: its own dispatched "+
				"FPP command %s is itself still unresolved, so this coordinator will not guess whether the action's "+
				"effect actually landed", child.ID)
	default:
		return outcomeWordUnconfirmed, string(observation.StateNotCollected),
			"resolved by startup reconciliation, not by this invocation's own original request (a restart or an " +
				"abandoned connection left it dispatched but never resolved): this coordinator does not re-run an " +
				"action's own confirmation rules retroactively, and no dispatched FPP command is recorded for it, so " +
				"it reports unconfirmed rather than guessing whether the action's effect actually landed"
	}
}
