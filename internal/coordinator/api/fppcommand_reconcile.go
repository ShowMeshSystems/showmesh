package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 7 seam C review defect 5's fix: a command left
// state='dispatched' (or even 'pending') with resolved_at NULL, because
// the process handling it stopped existing before it could finish —
// coordinator restart, a kill -9, or (before this review pass fixed it)
// defect 4's own context-cancellation bug — carried outcome_state and
// outcome_reason as permanent empty strings. handleFPPCommandReplay emits
// them as "", openapi.yaml declared outcomeState a bare, unconstrained
// string so no conformance check could catch a blank one, the UI rendered
// "Pending: this command has not yet resolved" forever, and the CLI
// printed "pending: ... (state )" forever, for a command that would never
// resolve on its own. ADR-020 requires a stated state and reason instead
// of a null that renders as blank; ADR-024 decision 11 requires an outcome
// audit entry.
//
// ReconcileStrandedFPPCommands is that fix: called exactly once, by
// internal/coordinator/coordinator.go, right after the API's Dependencies
// are wired and before the HTTP server starts accepting connections. That
// timing is what makes "any command still unresolved right now is
// STRANDED, not merely in flight" true: a command genuinely still being
// handled by a LIVE request can only exist once this process has served
// at least one request, and this function runs before that has happened.
// Calling it at any OTHER time — from a request handler, on a timer, more
// than once — would race a real in-flight confirmation loop and either
// resolve it prematurely or contend with it; nothing in this package calls
// it anywhere else, and nothing should.
//
// This is also what keeps the ONE deliberately accepted blank —
// handleFPPCommandReplay's narrow concurrent-insert race, where Outcome is
// legitimately "" for the instant before the winning request finishes —
// distinguishable from PERMANENT blankness: after this sweep has run, any
// row still unresolved is, by construction, being handled by a live
// request in THIS process (which will resolve it within
// h.fppCommandConfirmDeadline) rather than orphaned by a dead one. Before
// this fix the two were identical to every client; this function is what
// makes them different again.
func ReconcileStrandedFPPCommands(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) (resolved int, err error) {
	deps = deps.withDefaults()

	unresolved, err := deps.Commands.ListUnresolvedCommands(ctx)
	if err != nil {
		return 0, fmt.Errorf("api: reconcile stranded fpp commands: list unresolved commands: %w", err)
	}

	for _, rec := range unresolved {
		if rec.Action != auditActionFPPStopPlaylist {
			// The only action this coordinator knows how to re-evaluate
			// evidence for today. A future action left stranded by a
			// crash is not silently guessed at here — see this task's
			// report.
			continue
		}
		if rec.TargetKind != "fpp" {
			continue
		}

		// notBefore: the fence defect 2 requires, applied identically
		// here. DispatchedAt is nil only when dispatch was never even
		// attempted (defect 9) — in that narrow case fall back to
		// CreatedAt, since there is no dispatch instant to fence against
		// and CreatedAt is the earliest possible moment ANY effect
		// attributable to this command could have occurred.
		notBefore := rec.CreatedAt
		if rec.DispatchedAt != nil {
			notBefore = *rec.DispatchedAt
		}

		nowT := now()
		confirmed, outcomeState, reason := evaluateFPPStatusEvidence(ctx, deps.Observations, rec.TargetID, fppStatusValueIdle, notBefore, nowT)
		reason = "resolved by startup reconciliation, not by this command's own original request (see this coordinator's " +
			"own log for why: a restart or an abandoned connection left it dispatched but never resolved): " + reason

		outcomeWord := "unconfirmed"
		if confirmed {
			outcomeWord = "confirmed"
		}

		resultJSON, _ := json.Marshal(commandResultPayload{Outcome: outcomeWord})
		resultStr := string(resultJSON)
		resolvedState := "resolved"
		if err := deps.Commands.UpdateCommandOutcome(ctx, rec.ID, store.CommandOutcomeUpdate{
			ResolvedAt: &nowT, State: &resolvedState, ResultJSON: &resultStr,
			OutcomeState: &outcomeState, OutcomeReason: &reason,
		}); err != nil {
			if logger != nil {
				logger.Warn("api: failed to resolve a stranded fpp command", "commandId", rec.ID, "error", err)
			}
			continue
		}
		resolved++

		// Best-effort outcome audit entry, correlated by rec.ID — the
		// SAME "degrade, never refuse, never block" posture every other
		// write in this endpoint's family uses (Stop Playlist's ADR-024
		// decision 11 safety class). Attributed to the ORIGINAL issuer
		// (rec.IssuerPrincipalID/Name): there is no live request or
		// authenticated caller at coordinator startup to attribute this
		// to instead, and the original issuer is who actually asked for
		// this command's effect.
		if err := deps.Identity.WriteAudit(ctx, identity.AuditEntry{
			Timestamp: nowT, PrincipalID: rec.IssuerPrincipalID, PrincipalName: rec.IssuerPrincipalName,
			Action: rec.Action, Target: rec.TargetID, IdempotencyKey: rec.IdempotencyKey,
			Kind: identity.AuditOutcome, CommandID: rec.ID,
			Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: reason,
		}); err != nil && logger != nil {
			logger.Warn("api: audit write failed while resolving a stranded fpp command; outcome was still recorded",
				"commandId", rec.ID, "error", err)
		}

		if logger != nil {
			logger.Warn("api: resolved a command left stranded by a prior process",
				"commandId", rec.ID, "action", rec.Action, "target", rec.TargetID, "outcome", outcomeWord)
		}
	}

	return resolved, nil
}
