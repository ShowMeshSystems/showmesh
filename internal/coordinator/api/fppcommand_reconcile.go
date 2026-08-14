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

// This file is Step 7 seam C review defect 5's fix: a command left
// state='dispatched' (or even 'pending') with resolved_at NULL, because
// the process handling it stopped existing before it could finish —
// coordinator restart, a kill -9, or (before this review pass fixed it)
// defect 4's own context-cancellation bug — carried outcome_state and
// outcome_reason as permanent empty strings. The replay path (Step 9:
// [handlers.resolveFPPCommandReplay] in fppcommand_dispatch.go, formerly
// handleFPPCommandReplay in this file's own sibling) emits them as "",
// openapi.yaml declared outcomeState a bare, unconstrained
// string so no conformance check could catch a blank one, the UI rendered
// "Pending: this command has not yet resolved" forever, and the CLI
// printed "pending: ... (state )" forever, for a command that would never
// resolve on its own. ADR-020 requires a stated state and reason instead
// of a null that renders as blank; ADR-024 decision 11 requires an outcome
// audit entry.
//
// ReconcileStrandedFPPCommands is that fix: called exactly once, by
// internal/coordinator/coordinator.go, right after the API's Dependencies
// are wired and — Finding 3, Step 8 review — SYNCHRONOUSLY, blocking
// Run() before it launches the goroutine that calls srv.ListenAndServe().
// Before this fix the call site was `go func() { ... }()`, which is not a
// happens-before edge at all: coordinator.go's own comment claimed this
// ordering while the code raced it, and a live request confirming a
// command concurrently with this sweep was proved to resolve twice (a
// `resolved` state and an outcome audit entry written by each of the two
// paths racing the same row). Blocking is deliberately not gated behind
// an error check: if the scan itself fails (see the ListUnresolvedCommands
// error return below), Run() still proceeds to start listening — ADR-024
// constraint 23 draws the line at "you cannot act", never "you cannot
// see", and this is local infrastructure with no principal to hold
// accountable for refusing to boot. The scan itself is a bounded local
// SQLite query, which is what makes blocking acceptable rather than a
// startup-time dependency on anything that can itself hang indefinitely.
//
// That timing is what makes "any command still unresolved right now is
// STRANDED, not merely in flight" true: a command genuinely still being
// handled by a LIVE request can only exist once this process has served
// at least one request, and this function now provably runs before that
// has happened. Calling it at any OTHER time — from a request handler, on
// a timer, more than once — would race a real in-flight confirmation loop
// and either resolve it prematurely or contend with it; nothing in this
// package calls it anywhere else, and nothing should.
//
// This is also what keeps the ONE deliberately accepted blank —
// [handlers.resolveFPPCommandReplay]'s narrow concurrent-insert race, where
// Outcome is legitimately "" for the instant before the winning request
// finishes —
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
		if rec.TargetKind != "fpp" {
			continue
		}

		nowT := now()
		var confirmed bool
		var outcomeState, reason string

		primitive, known := fppPrimitivesByAuditAction[rec.Action]
		switch {
		case !known:
			// Finding 11 (Step 8 review): an action this coordinator does
			// not recognize AT ALL — a future action a newer coordinator
			// shipped, reachable here after a rollback, or a row from a
			// different command family entirely — used to `continue` here
			// with NO log line and NO state change, leaving the row
			// "pending" with outcome_state and outcome_reason permanently
			// blank. That is exactly the "stranded, blank forever" shape
			// this whole file exists to close, reintroduced for the one
			// case this switch's own comment used to name instead of fix.
			// This coordinator cannot re-evaluate evidence for an action
			// it does not know the confirmation rules for, so it states
			// that plainly (ADR-020: absent evidence is stated, never
			// omitted) and resolves unconfirmed rather than leaving the
			// row silently stuck forever.
			outcomeState = string(observation.StateUnsupported)
			reason = fmt.Sprintf(
				"this coordinator does not recognize action %q and cannot re-evaluate its evidence (a newer "+
					"version's action, reached after a rollback, or a row from a different command family)", rec.Action)
			if logger != nil {
				logger.Warn("api: stranded command carries an action this coordinator does not recognize; resolving unconfirmed rather than leaving it pending forever",
					"commandId", rec.ID, "action", rec.Action, "target", rec.TargetID)
			}

		case rec.DispatchedAt == nil:
			// Finding 2 (Step 8 review): DispatchedAt nil with
			// state=="pending" is exactly the row shape a process crash
			// BETWEEN the AuditedWrite commit and primitive.Dispatch
			// (fppcommand_handler.go's own numbered sequence) leaves
			// behind — the row exists and NOTHING was ever sent to FPP.
			// Falling back to CreatedAt as a notBefore fence and then
			// evaluating the primitive's ordinary Confirm predicate
			// against whatever evidence exists since then can spuriously
			// "confirm" a command that never dispatched, off FPP's own
			// unrelated activity in that window: proved live — a
			// `pending` fpp.start_playlist row with DispatchedAt nil,
			// plus evidence collected 2s after CreatedAt, reconciled to
			// {"outcome":"confirmed"}, attributing FPP's own scheduled
			// start to the operator's principal. Violates ADR-001 and
			// ADR-003. A row that was never dispatched must resolve
			// unconfirmed UNCONDITIONALLY: there is no notBefore fence
			// that makes any observation attributable to a command that
			// was never sent, so this branch never calls primitive.Confirm
			// at all.
			outcomeState = string(observation.StateNotCollected)
			reason = "this command's dispatch to FPP was never attempted before the prior process stopped existing " +
				"(dispatched_at is unset): no observation, however it reads, can be attributed to a command that was " +
				"never sent"

		default:
			notBefore := *rec.DispatchedAt

			// params: decoded from the row's own canonical ParamsJSON —
			// the SAME normalized params the original (now-dead) request
			// dispatched with. baseline is deliberately the zero
			// [fppBaseline] for every primitive: nextPlaylistItem/
			// prevPlaylistItem's own pre-dispatch index/status snapshot
			// was captured (or not) by the process that crashed, and is
			// not reconstructable now — [evaluateNextItemEvidence]/
			// [evaluatePrevItemEvidence] both treat an unknown baseline as
			// "report unconfirmed, state why" rather than inventing one,
			// so this sweep inherits that honesty rather than working
			// around it.
			var params map[string]any
			_ = json.Unmarshal([]byte(rec.ParamsJSON), &params)
			if params == nil {
				params = map[string]any{}
			}
			confirmed, outcomeState, reason = primitive.Confirm(ctx, deps.Observations, rec.TargetID, params, fppBaseline{}, notBefore, nowT)
		}

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
