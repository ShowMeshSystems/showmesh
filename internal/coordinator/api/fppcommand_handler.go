package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Step 7 seam C: the first write endpoint this project has
// ever shipped, and the only one that touches FPP. Bound above all by
// ADR-001 (this dispatches FPP's own native command; it never schedules)
// and ADR-003 (a 200 from FPP is not success; confirmation is by
// evidence, against an explicit deadline). See this task's report for the
// full accounting against every acceptance criterion.
//
// This version is the review-fix pass: defects 2, 3, 4, 6, 8, and 9 all
// live in this file (defect 1 lives partly in api.go/pkg/command, defect 5
// lives in fppcommand_reconcile.go, defect 7 lives in
// internal/coordinator/fppcommand and collector/fpp's import-graph test).
// See this task's report for the design choice behind each.

// scopeFPPCommand exists only so api.go's route registration can take its
// address: [handlers.writeGuard] takes *identity.Scope (nil means "any
// authenticated principal, no specific scope" — DELETE /api/v1/session),
// and a Go constant's address cannot be taken directly.
var scopeFPPCommand = identity.ScopeFPPCommand

// fppActionStopPlaylist is the wire-level v1.FPPCommandRequest.Action
// value this endpoint accepts — the ONLY one, today. Deliberately a body
// field rather than folded into the URL: a second primitive command,
// later, is an additive change to this same endpoint's accepted action
// set, not a second route.
const fppActionStopPlaylist = "stopPlaylist"

// auditActionFPPStopPlaylist is this command's own internal action
// identifier — commands.action and every audit_log entry's action column
// — namespaced and dotted like this codebase's other admin action names
// (identity/service.go's "session.create", "bootstrap.claim"), not the
// wire's camelCase Action value, which is a request-body convenience.
const auditActionFPPStopPlaylist = "fpp.stop_playlist"

// fppStatusSignal is [internal/coordinator/collector/fpp.SignalStatus]'s
// exact wire value ("fpp.status", carrying FPP's own status_name string:
// "idle", "playing", ...), inlined as a literal rather than importing
// that package — the identical choice mapping.go's healthCriticalSignals
// map already makes for "fpp.reachable", and for the identical reason:
// this package confirms against evidence from whichever collector source
// produced it (fpp-rest today; fpp-mqtt reports overlapping signals too),
// and importing one concrete collector package to borrow a string
// constant would tie a write endpoint to one specific collector
// implementation.
//
// fppStatusValueIdle is the value Stop Playlist's desired state names:
// confirmed live against a bench fppd (see this task's report) to be
// exactly what FPP's own status_name reads once "Stop Now" — the real FPP
// command this endpoint dispatches; see internal/coordinator/fppcommand's
// own doc comment on why "Stop Playlist" names no actual FPP command —
// has taken effect.
const (
	fppStatusSignal    = "fpp.status"
	fppStatusValueIdle = "idle"
)

// maxFPPCommandRequestBodyBytes bounds this endpoint's request body,
// mirroring session.go's maxSessionRequestBodyBytes convention (a request
// this small has no legitimate reason to be large; a caller sending more
// gets a decode error rather than this handler reading an unbounded body).
const maxFPPCommandRequestBodyBytes = 4 << 10 // 4 KiB

// dbWriteTimeout bounds each individual piece of post-dispatch
// bookkeeping (a commands-row update, an outcome audit entry) once it is
// no longer tied to the client's own request context — see bgCtx's own
// comment in [handlers.handleFPPCommand] (Step 7 seam C review defect 4).
// "Not cancellable by an abandoned client" must not silently become
// "capable of hanging forever" if the store is ever wedged; each write
// gets its own short, independent deadline instead of inheriting nothing.
// SHOWMESH HYPOTHESIS, NOT MEASURED: chosen only to be comfortably larger
// than one local SQLite write.
const dbWriteTimeout = 10 * time.Second

// commandResultPayload is what this handler stores in
// store.CommandRecord.ResultJSON and returns to a replay as the record of
// what actually happened — ARCHITECTURE section 8.1's "result", captured
// as JSON because, unlike every other envelope field, it does not exist
// until dispatch (and, for Outcome specifically, confirmation) has
// happened.
type commandResultPayload struct {
	Outcome    string `json:"outcome,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
	Body       string `json:"body,omitempty"`
}

// fppStopPlaylistCommandRecord builds the commands-table row for env.
// Factored out so both the transactional path and the safety-class
// degraded fallback (defect 8; see [handlers.handleFPPCommand]) build the
// IDENTICAL row rather than two copies of this struct literal that could
// silently drift apart from each other.
func fppStopPlaylistCommandRecord(env command.Envelope) store.CommandRecord {
	return store.CommandRecord{
		ID:                  env.ID,
		IdempotencyKey:      env.IdempotencyKey,
		Action:              env.Action,
		TargetKind:          env.Target.Kind,
		TargetID:            env.Target.ID,
		IssuerPrincipalID:   env.Issuer.PrincipalID,
		IssuerPrincipalName: env.Issuer.PrincipalName,
		ConfirmationMethod:  string(env.ConfirmationMethod),
		DeadlineAt:          env.Deadline,
		State:               "pending",
	}
}

// fppStopPlaylistDesiredState builds the desired_state row env's dispatch
// asks for. Same factoring reason as [fppStopPlaylistCommandRecord].
func fppStopPlaylistDesiredState(env command.Envelope, now time.Time) store.DesiredStateRecord {
	return store.DesiredStateRecord{
		ResourceKind:           env.Target.Kind,
		ResourceID:             env.Target.ID,
		Signal:                 fppStatusSignal,
		Value:                  fppStatusValueIdle,
		RequestedAt:            now,
		RequestedByPrincipalID: env.Issuer.PrincipalID,
		CommandID:              env.ID,
		DeadlineAt:             env.Deadline,
	}
}

// handleFPPCommand serves POST /api/v1/fpp/{instanceId}/commands, behind
// writeGuard(&identity.ScopeFPPCommand, ...) — so by the time this method
// runs, ADR-024 decision 4's scope check and decision 6's CSRF check have
// both already passed, and [authFromContext] is guaranteed to report
// ac.ok == true with a principal holding fpp:command.
//
// The order below is ADR-024 decision 11's, and it is NOT the same order
// seam A's coordinator-local configuration write follows:
//
//  1. Mint the command id.
//  2. Insert the commands row, record the desired state (ADR-003's split,
//     expressed in storage), and write the DISPATCH audit entry BEFORE
//     dispatching — decision 11's write-before-dispatch rule for a
//     command sent outward — ALL THREE IN ONE TRANSACTION when the audit
//     store is healthy (Step 7 seam C review defect 8: a crash between
//     the insert and the dispatch audit entry must never leave a commands
//     row with no audit record, and store.Tx.InsertCommand/SetDesiredState
//     exist for exactly this). A duplicate idempotency key stops here —
//     see [handlers.handleFPPCommandReplay]. On an AUDIT-WRITE failure
//     specifically (never any other failure), Stop Playlist's safety-class
//     exemption (decision 11: blackout/stop/power-off proceed regardless)
//     means the transaction's rollback of the state change is NOT
//     acceptable, so this handler falls back to inserting and recording
//     desired state without a transaction, with degraded attribution —
//     the identical posture this handler always used before this fix, now
//     reached only when the atomic path could not be taken.
//  3. Dispatch to FPP via internal/coordinator/fppcommand — deliberately
//     OUTSIDE any transaction (network I/O must never run inside one;
//     store/tx.go's own rule).
//  4. Confirm by evidence against the deadline, then write the OUTCOME as
//     a separate, correlated audit entry — never by mutating the dispatch
//     row (audit_log has no update path, by design).
func (h *handlers) handleFPPCommand(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	// internal/coordinator/httpapi.NewServer configures a WriteTimeout on
	// the *http.Server this handler is mounted on — a reasonable default
	// for this package's ordinary REST-style routes, and net/http.Server.
	// WriteTimeout bounds the ENTIRE response-writing phase of one
	// request, not merely one write. This handler can legitimately hold
	// the connection open past that default while confirmFPPStatus waits
	// out h.fppCommandConfirmDeadline (whose own default already exceeds
	// it), so left unguarded the coordinator's own HTTP server would
	// silently sever the connection out from under a still-working
	// confirmation wait — discovered only by running the real binary
	// against the bench fppd and watching curl report "Empty reply from
	// server" seconds before the handler had finished, the exact shape of
	// defect stream.go's own resetWriteDeadline exists to prevent for the
	// SSE stream (see that function's doc comment, "finding 1.1"). Set
	// once, unlike stream.go's per-write reset, because this handler
	// performs exactly one write (jsonWrite, at the very end) rather than
	// a long-lived series of them.
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(h.fppCommandConfirmDeadline + 30*time.Second))

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	var req v1.FPPCommandRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPCommandRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be JSON matching {\"action\":string,\"idempotencyKey\":string}"))
		return
	}
	if req.Action != fppActionStopPlaylist {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("unsupported action %q; this coordinator supports: %q", req.Action, fppActionStopPlaylist)))
		return
	}
	if err := command.ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("idempotencyKey: "+err.Error()))
		return
	}

	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances for command dispatch", err)
		return
	}
	var target *FPPInstanceView
	for i := range views {
		if views[i].InstanceID == instanceID {
			target = &views[i]
			break
		}
	}
	if target == nil {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no FPP instance with id "+strconv.Quote(instanceID)+" is configured"))
		return
	}

	ac := authFromContext(ctx)
	issuerID, issuerName := ac.result.Principal.ID, ac.result.Principal.Name

	env := command.Envelope{
		ID:                 uuid.NewString(),
		IdempotencyKey:     req.IdempotencyKey,
		Action:             auditActionFPPStopPlaylist,
		Target:             command.Target{Kind: string(observation.ResourceFPP), ID: instanceID},
		Issuer:             command.Issuer{PrincipalID: issuerID, PrincipalName: issuerName},
		ConfirmationMethod: command.ConfirmationEvidence,
	}
	deadline := now.Add(h.fppCommandConfirmDeadline)
	env.Deadline = &deadline

	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditDispatch, CommandID: env.ID,
	}

	// --- 1-2. Insert, record desired state, and write the dispatch audit
	// entry — atomically when the audit store is healthy (defect 8). ---
	var dispatchDegraded bool
	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, err := tx.InsertCommand(ctx, fppStopPlaylistCommandRecord(env)); err != nil {
			return identity.AuditEntry{}, err
		}
		if _, err := tx.SetDesiredState(ctx, fppStopPlaylistDesiredState(env, now)); err != nil {
			// desired_state is advisory bookkeeping with no reconciler
			// (store's own standing rule): a failure here must never cost
			// the command its own row or its dispatch audit entry, so
			// this does NOT return an error (which would roll back the
			// whole transaction) — it only logs.
			h.logWarn("failed to record desired state for fpp command", "commandId", env.ID, "error", err)
		}
		return dispatchEntry, nil
	})

	var dup *store.DuplicateCommandError
	switch {
	case errors.As(auditErr, &dup):
		h.handleFPPCommandReplay(w, r, now, dup.Existing, instanceID, env.Action)
		return
	case errors.Is(auditErr, identity.ErrAuditWrite):
		// Safety-class exemption (ADR-024 decision 11): the transactional
		// attempt above rolled back IN FULL — its own audit append is what
		// failed, and store.Store.InTx rolls back the entire transaction
		// on any error, including tx.InsertCommand's own effect. Redo the
		// insert and desired-state write through the plain,
		// non-transactional store methods, and proceed with degraded
		// attribution: the exact posture this handler always used before
		// this fix, now reached only when the atomic path could not be
		// taken.
		rec, err := h.deps.Commands.InsertCommand(ctx, fppStopPlaylistCommandRecord(env))
		if errors.As(err, &dup) {
			h.handleFPPCommandReplay(w, r, now, dup.Existing, instanceID, env.Action)
			return
		}
		if err != nil {
			h.writeInternalError(w, now, "insert fpp command", err)
			return
		}
		_ = rec
		if _, err := h.deps.Commands.SetDesiredState(ctx, fppStopPlaylistDesiredState(env, now)); err != nil {
			h.logWarn("failed to record desired state for fpp command", "commandId", env.ID, "error", err)
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr)
		dispatchDegraded = true
	case auditErr != nil:
		h.writeInternalError(w, now, "insert fpp command", auditErr)
		return
	}

	// --- 3. Dispatch — deliberately OUTSIDE any transaction (network I/O
	// must never run inside one; store's own standing rule). ---
	dispatchAttemptedAt := h.now()
	var (
		outcome      fppcommand.Outcome
		dispatchErr  error
		dispatchedAt *time.Time // nil unless dispatch was actually ATTEMPTED — defect 9
	)
	client, cerr := fppcommand.New(target.Endpoint, fppcommand.Options{})
	if cerr != nil {
		dispatchErr = fmt.Errorf("building fpp command client: %w", cerr)
	} else {
		dispatchedAt = &dispatchAttemptedAt
		outcome, dispatchErr = client.StopPlaylist(ctx)
	}

	// bgCtx carries none of r.Context()'s cancellation past this point
	// (Step 7 seam C review defect 4): the command has already been
	// dispatched (or the attempt already made), so a client that simply
	// closes its tab must not be able to sever the RECORD of what
	// happened — that is precisely how a resolved command turns into
	// defect 5's "stranded, blank forever" shape, for the routine case of
	// an abandoned browser tab rather than a coordinator crash. This is
	// deliberately NOT context.Background(): every write below still gets
	// its own short, bounded deadline via dbWriteTimeout — "not
	// cancellable by an abandoned client" must not become "capable of
	// hanging forever" if the store is ever wedged.
	bgCtx := context.WithoutCancel(ctx)

	dispatchState := "dispatched"
	prelimResult, _ := json.Marshal(commandResultPayload{StatusCode: outcome.StatusCode, Body: outcome.Body})
	prelimResultStr := string(prelimResult)
	if err := h.updateCommandOutcomeBounded(bgCtx, env.ID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, State: &dispatchState, ResultJSON: &prelimResultStr,
	}); err != nil {
		h.logWarn("failed to record fpp command dispatch", "commandId", env.ID, "error", err)
	}

	// --- 4. Confirm by evidence, or report the dispatch failure directly. ---
	var (
		confirmed     bool
		outcomeState  string
		outcomeReason string
	)
	if dispatchErr != nil {
		outcomeState = string(observation.StateCollectionFailed)
		outcomeReason = "dispatching to FPP failed: " + dispatchErr.Error()
	} else {
		// notBefore = dispatchAttemptedAt (Step 7 seam C review defect 2):
		// confirmation must rest on evidence collected no earlier than the
		// moment this handler attempted dispatch, never on a stale reading
		// that merely happens to agree — see confirmFPPStatus's own doc
		// comment and evaluateFPPStatusEvidence for the full reasoning.
		confirmed, outcomeState, outcomeReason = h.confirmFPPStatus(bgCtx, instanceID, fppStatusValueIdle, dispatchAttemptedAt)
	}

	outcomeWord := "unconfirmed"
	if confirmed {
		outcomeWord = "confirmed"
	}

	resolvedAt := h.now()
	resolvedState := "resolved"
	finalResult, _ := json.Marshal(commandResultPayload{Outcome: outcomeWord, StatusCode: outcome.StatusCode, Body: outcome.Body})
	finalResultStr := string(finalResult)
	if err := h.updateCommandOutcomeBounded(bgCtx, env.ID, store.CommandOutcomeUpdate{
		ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("failed to record fpp command outcome", "commandId", env.ID, "error", err)
	}

	// --- Outcome audit entry: a SEPARATE, correlated entry — never a
	// mutation of the dispatch row above. ---
	outcomeDegraded := h.writeSafetyClassAuditBounded(bgCtx, resolvedAt, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: env.ID,
		Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})

	dispatchedAtStr := formatTimePtr(dispatchedAt)
	resolvedAtStr := formatTime(resolvedAt)
	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: env.ID, IdempotencyKey: env.IdempotencyKey, Action: env.Action, InstanceID: instanceID,
			Replay: false, Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
			AttributionDegraded: dispatchDegraded || outcomeDegraded,
			DispatchedAt:        dispatchedAtStr,
			ResolvedAt:          &resolvedAtStr,
		},
	})
}

// handleFPPCommandReplay answers a replayed idempotency key: NOTHING is
// dispatched, and existing's own already-recorded result is returned
// verbatim — ADR-024 decision 11: "a replay is precisely the case an
// investigator wants to see, because it means the operator did not get
// their response." A replay audit entry is written (best-effort, per the
// same safety-class posture as every other write this endpoint makes),
// correlated by existing.ID, never a mutation of the original dispatch or
// outcome entries.
//
// requestedAction is this request's own action (env.Action, the internal
// "fpp.stop_playlist"-shaped identifier, not the wire's "stopPlaylist").
// Step 7 seam C review defect 6: idempotencyKey alone is not enough — a
// key reused against a DIFFERENT action or a DIFFERENT target
// (instanceID) is a CONFLICT, not a replay, and answering it as a replay
// would report a stored outcome under a target this request never
// actually named. That check happens FIRST, before anything below reads
// existing as if it belonged to this request.
//
// existing.Outcome is decoded from existing.ResultJSON — see
// [commandResultPayload] — and may be empty in one narrow, accepted race:
// two concurrent requests presenting the SAME idempotency key both reach
// [store.Store.InsertCommand] at nearly the same instant; SQLite's own
// single-writer serialization (store's connection pool is capped at 1)
// guarantees exactly one wins the INSERT, but the loser can observe the
// winner's row before the winner's own request has finished dispatching
// and confirming. This handler does not wait for that: it returns
// whatever the row honestly holds right now (Outcome/OutcomeState/
// OutcomeReason empty, DispatchedAt/ResolvedAt nil), per ADR-020's
// "absence of evidence is stated, never omitted" — an empty Outcome is
// not a bug, it is the true, current state of a command still in flight.
// This race is deliberately indistinguishable, by design, from the
// permanent blankness a coordinator restart could otherwise leave behind
// — see fppcommand_reconcile.go's own doc comment for why a startup
// sweep is what keeps the two from staying identical forever.
func (h *handlers) handleFPPCommandReplay(w http.ResponseWriter, r *http.Request, now time.Time, existing store.CommandRecord, instanceID, requestedAction string) {
	if existing.Action != requestedAction || existing.TargetID != instanceID {
		writeProblem(w, h.logger, now, fppCommandReplayConflictProblem(
			existing.ID, existing.Action, existing.TargetID, requestedAction, instanceID))
		return
	}

	ac := authFromContext(r.Context())
	degraded := h.writeSafetyClassAudit(r.Context(), now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: existing.Action, Target: instanceID, IdempotencyKey: existing.IdempotencyKey,
		Kind: identity.AuditReplay, CommandID: existing.ID,
	})

	var payload commandResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &payload) // best-effort; "{}" or malformed decodes to the zero value

	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(now),
		Command: v1.FPPCommandResult{
			ID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Action: existing.Action, InstanceID: instanceID,
			Replay: true, Outcome: payload.Outcome, OutcomeState: existing.OutcomeState, OutcomeReason: existing.OutcomeReason,
			AttributionDegraded: degraded,
			DispatchedAt:        formatTimePtr(existing.DispatchedAt),
			ResolvedAt:          formatTimePtr(existing.ResolvedAt),
		},
	})
}

// confirmFPPStatus polls [evaluateFPPStatusEvidence] for fppStatusSignal on
// (fpp, instanceID) every h.fppCommandPollInterval until it observes
// wantValue as StateCurrent on evidence collected no earlier than
// notBefore, or h.fppCommandConfirmDeadline elapses — ADR-003: confirmation
// is by evidence against an explicit deadline, never a synchronous
// assumption that a 200 from FPP means the state actually moved, and
// (Step 7 seam C review defect 2) never a pre-existing reading that merely
// happens to already agree.
//
// The wait itself is bound by REAL wall-clock time (time.Now/time.Ticker),
// deliberately independent of h.now() (which a test fixes at one instant
// via [Options.Clock] to make every STAMPED timestamp deterministic —
// dispatchedAt, resolvedAt, serverTime). A confirmation loop paced by a
// clock that never advances would never terminate; h.now() is used only
// to evaluate an observation's OWN freshness ([observation.Observation.StateAt]),
// matching every other handler in this package's identical use of h.now()
// for that purpose.
//
// ctx no longer carries the inbound request's own cancellation as of Step
// 7 seam C review defect 4 (the caller passes bgCtx) — the ctx.Done()
// branch below is retained as a defensive fallback for any future caller
// that does pass a context capable of ending on its own, but is not
// reachable from this handler's own production call site any more.
func (h *handlers) confirmFPPStatus(ctx context.Context, instanceID, wantValue string, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	deadline := time.Now().Add(h.fppCommandConfirmDeadline)
	ticker := time.NewTicker(h.fppCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason = h.checkFPPStatusOnce(ctx, instanceID, wantValue, notBefore)
		if confirmed {
			return true, outcomeState, outcomeReason
		}
		if !time.Now().Before(deadline) {
			return false, outcomeState, outcomeReason
		}
		select {
		case <-ctx.Done():
			// The caller's own request context ended (e.g. a disconnected
			// client) before the deadline. The command was already
			// dispatched to FPP regardless — that effect, if any, already
			// happened — only this response's own wait is abandoned.
			return false, string(observation.StateUnknownAge), "confirmation aborted before the deadline: " + ctx.Err().Error()
		case <-ticker.C:
		}
	}
}

// checkFPPStatusOnce is one evidence check: the current fppStatusSignal
// observation for (fpp, instanceID), evaluated against wantValue and
// notBefore — see [evaluateFPPStatusEvidence].
func (h *handlers) checkFPPStatusOnce(ctx context.Context, instanceID, wantValue string, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	return evaluateFPPStatusEvidence(ctx, h.deps.Observations, instanceID, wantValue, notBefore, h.now())
}

// evaluateFPPStatusEvidence is Step 7 seam C review defects 2 and 3's
// shared evidence check, factored to a package-level function (rather than
// a method on [handlers]) specifically so fppcommand_reconcile.go's
// startup sweep can call it too, against the exact same rule the live
// confirmation loop uses.
//
// Defect 2: an observation is only usable to confirm THIS dispatch when it
// was COLLECTED no earlier than notBefore. A value that already read
// wantValue before the command was ever dispatched — FPP's own schedule
// having independently reached the same state, or simply a stale row that
// has not been re-polled — must never confirm a command whose actual
// effect it cannot possibly reflect. This is deliberately checked against
// CollectedAt (when the collector recorded this row — always set), not
// ObservedAt (when the condition was true according to the source, nil for
// a retained MQTT delivery): CollectedAt is this coordinator's own
// bookkeeping of when it last asked, which is exactly "did we re-poll
// since we dispatched," and using ObservedAt instead would make a
// retained-MQTT observation (ObservedAt always nil) impossible to fence at
// all.
//
// Defect 3: fpp-rest and fpp-mqtt both emit fpp.status for the same
// resource; [ObservationLister] carries no ordering guarantee across
// sources sharing a (resource, signal) pair (schemaV4's primary key is
// (resource_kind, resource_id, signal, source)), so evaluating "the first
// matching row" was a coin flip between two collectors' independent
// answers. This function instead resolves every candidate for (fpp,
// instanceID, fppStatusSignal) via [ResolveObservations] — this package's
// own documented precedence rule (precedence.go) for exactly this
// situation — down to the single evidentiary winner, and its
// outcomeReason names which source produced the verdict, per ADR-011:
// evidence carries provenance.
func evaluateFPPStatusEvidence(ctx context.Context, lister ObservationLister, instanceID, wantValue string, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	kind := observation.ResourceFPP
	sig := observation.SignalID(fppStatusSignal)
	obs, err := lister.ListObservations(ctx, ObservationFilter{
		ResourceKind: &kind, ResourceID: &instanceID, Signal: &sig,
	})
	if err != nil {
		return false, string(observation.StateCollectionFailed), "reading fpp.status for confirmation: " + err.Error()
	}

	var candidates []observation.Observation
	for _, o := range obs {
		if o.Resource.Kind != kind || o.Resource.ID != instanceID || o.Signal != sig {
			continue
		}
		candidates = append(candidates, o)
	}
	if len(candidates) == 0 {
		return false, string(observation.StateNotCollected), "no fpp.status observation is recorded for this instance yet"
	}

	// ResolveObservations groups by (Resource, Signal); every candidate
	// here already shares that exact triple (the loop above filtered to
	// it), so this always resolves to exactly one winner.
	resolved := ResolveObservations(candidates)
	o := resolved[0]
	source := o.Source
	if source == "" {
		source = "unknown source"
	}

	if o.CollectedAt.Before(notBefore) {
		return false, string(observation.StateNotCollected), fmt.Sprintf(
			"no fpp.status observation has been collected since this command was dispatched at %s (most recent "+
				"evidence is from %s, source %s, and predates dispatch — a pre-dispatch reading can never confirm "+
				"this command, ADR-003, even one that already happens to agree)",
			notBefore.Format(time.RFC3339), o.CollectedAt.Format(time.RFC3339), source)
	}

	state := o.StateAt(now)
	if state == observation.StateCurrent {
		if o.Value == wantValue {
			return true, string(state), ""
		}
		return false, string(state), fmt.Sprintf("observed fpp.status = %v (source %s), want %q", o.Value, source, wantValue)
	}
	reason := o.Reason
	if reason == "" {
		reason = fmt.Sprintf("fpp.status evidence state is %s", state)
	}
	return false, string(state), fmt.Sprintf("%s (source %s)", reason, source)
}

// updateCommandOutcomeBounded wraps h.deps.Commands.UpdateCommandOutcome
// with its own dbWriteTimeout, derived from parent (bgCtx in production —
// see [handlers.handleFPPCommand]'s own comment on why post-dispatch
// bookkeeping is no longer bound by the client's request context).
func (h *handlers) updateCommandOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}

// writeSafetyClassAuditBounded is [handlers.writeSafetyClassAudit] with
// its own dbWriteTimeout applied to parent, for the identical reason
// [updateCommandOutcomeBounded] exists.
func (h *handlers) writeSafetyClassAuditBounded(parent context.Context, now time.Time, entry identity.AuditEntry) (degraded bool) {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	return h.writeSafetyClassAudit(ctx, now, entry)
}

// writeSafetyClassAudit writes entry via h.deps.Identity.WriteAudit and
// reports whether it had to degrade. Named for ADR-024 decision 11's
// blackout/stop/power-off safety class, which Stop Playlist is a member
// of: unlike [handlers.writeAuditOrFail] (auth.go), which REFUSES the
// write it protects on an audit-write failure, this method never blocks
// its caller. On failure it calls [handlers.reportDegradedAttribution] and
// returns true so the caller can flag the command's own wire response as
// attribution-degraded rather than silently absorbing the gap.
func (h *handlers) writeSafetyClassAudit(ctx context.Context, now time.Time, entry identity.AuditEntry) (degraded bool) {
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.reportDegradedAttribution(now, entry, err)
		return true
	}
	return false
}

// reportDegradedAttribution is the best-effort, human-readable stderr line
// ADR-024 decision 11's safety-class exemption writes when an audit write
// fails and the command proceeds anyway — never to h.logger alone, whose
// own destination this coordinator controls no more durably than it
// controls audit_log's, and decision 11's exemption exists precisely for
// the case where NEITHER may be trusted (disk exhaustion is the named
// trigger). Factored out of [handlers.writeSafetyClassAudit] so the
// dispatch-side degraded fallback in [handlers.handleFPPCommand] (Step 7
// seam C review defect 8's AuditedWrite fallback path, which fails before
// entry is ever handed to WriteAudit at all) can report the identical line
// rather than a second, drifted copy of it.
func (h *handlers) reportDegradedAttribution(now time.Time, entry identity.AuditEntry, err error) {
	fmt.Fprintf(os.Stderr,
		"showmesh: DEGRADED ATTRIBUTION (audit write failed; command proceeded anyway per ADR-024 decision 11's "+
			"blackout/stop/power-off safety class) time=%s principal=%s(%s) action=%s target=%s commandId=%s "+
			"kind=%s idempotencyKey=%s error=%v\n",
		now.Format(time.RFC3339), entry.PrincipalName, entry.PrincipalID, entry.Action, entry.Target,
		entry.CommandID, entry.Kind, entry.IdempotencyKey, err)
	h.logWarn("audit write failed for a safety-class command; proceeding with degraded attribution",
		"error", err, "commandId", entry.CommandID, "kind", entry.Kind)
}

// logWarn is a small nil-safe wrapper: several call sites in this file
// log a best-effort failure and h.logger, like every other field on
// [handlers], is only ever nil in a test that built one by hand rather
// than through [New].
func (h *handlers) logWarn(msg string, args ...any) {
	if h.logger != nil {
		h.logger.Warn("api: "+msg, args...)
	}
}
