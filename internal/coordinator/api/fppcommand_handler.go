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

// handleFPPCommand serves POST /api/v1/fpp/{instanceId}/commands, behind
// writeGuard(&identity.ScopeFPPCommand, ...) — so by the time this method
// runs, ADR-024 decision 4's scope check and decision 6's CSRF check have
// both already passed, and [authFromContext] is guaranteed to report
// ac.ok == true with a principal holding fpp:command.
//
// The order below is ADR-024 decision 11's, and it is NOT the same order
// seam A's coordinator-local configuration write follows:
//
//  1. Mint the command id, insert the commands row. A duplicate
//     idempotency key stops here — see [handlers.handleFPPCommandReplay].
//  2. Record the desired state (ADR-003's split, expressed in storage).
//  3. Write the DISPATCH audit entry BEFORE dispatching — decision 11's
//     write-before-dispatch rule for a command sent outward. This is
//     NEVER identity.Service.AuditedWrite: dispatch is network I/O and
//     must never run inside a transaction (store/tx.go's own rule).
//  4. Dispatch to FPP via internal/coordinator/fppcommand.
//  5. Confirm by evidence against the deadline, then write the OUTCOME as
//     a separate, correlated audit entry — never by mutating the dispatch
//     row (audit_log has no update path, by design).
//
// Stop Playlist is a member of decision 11's blackout/stop/power-off
// safety class: an audit-write failure at step 3 or step 5 does NOT
// refuse or abort this command — see [handlers.writeSafetyClassAudit].
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

	// --- 1. Insert the command row; a duplicate idempotency key stops here. ---
	_, err = h.deps.Commands.InsertCommand(ctx, store.CommandRecord{
		ID:                  env.ID,
		IdempotencyKey:      env.IdempotencyKey,
		Action:              env.Action,
		TargetKind:          env.Target.Kind,
		TargetID:            env.Target.ID,
		IssuerPrincipalID:   issuerID,
		IssuerPrincipalName: issuerName,
		ConfirmationMethod:  string(env.ConfirmationMethod),
		DeadlineAt:          env.Deadline,
		State:               "pending",
	})
	var dup *store.DuplicateCommandError
	if errors.As(err, &dup) {
		h.handleFPPCommandReplay(w, r, now, dup.Existing, instanceID)
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "insert fpp command", err)
		return
	}

	// --- 2. Record the desired state. Best-effort: desired_state is
	// advisory bookkeeping with no reconciler (store's own standing
	// rule), and Stop Playlist's safety class means nothing past this
	// point may refuse the command over a failure here. ---
	if _, err := h.deps.Commands.SetDesiredState(ctx, store.DesiredStateRecord{
		ResourceKind: string(observation.ResourceFPP), ResourceID: instanceID, Signal: fppStatusSignal,
		Value: fppStatusValueIdle, RequestedAt: now,
		RequestedByPrincipalID: issuerID, CommandID: env.ID, DeadlineAt: &deadline,
	}); err != nil {
		h.logWarn("failed to record desired state for fpp command", "commandId", env.ID, "error", err)
	}

	// --- 3. Write the DISPATCH audit entry BEFORE dispatching. ---
	dispatchDegraded := h.writeSafetyClassAudit(ctx, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditDispatch, CommandID: env.ID,
	})

	// --- 4. Dispatch. ---
	var (
		outcome     fppcommand.Outcome
		dispatchErr error
	)
	client, cerr := fppcommand.New(target.Endpoint, fppcommand.Options{})
	if cerr != nil {
		dispatchErr = fmt.Errorf("building fpp command client: %w", cerr)
	} else {
		outcome, dispatchErr = client.StopPlaylist(ctx)
	}

	dispatchedAt := h.now()
	dispatchState := "dispatched"
	prelimResult, _ := json.Marshal(commandResultPayload{StatusCode: outcome.StatusCode, Body: outcome.Body})
	prelimResultStr := string(prelimResult)
	if err := h.deps.Commands.UpdateCommandOutcome(ctx, env.ID, store.CommandOutcomeUpdate{
		DispatchedAt: &dispatchedAt, State: &dispatchState, ResultJSON: &prelimResultStr,
	}); err != nil {
		h.logWarn("failed to record fpp command dispatch", "commandId", env.ID, "error", err)
	}

	// --- 5. Confirm by evidence, or report the dispatch failure directly. ---
	var (
		confirmed     bool
		outcomeState  string
		outcomeReason string
	)
	if dispatchErr != nil {
		outcomeState = string(observation.StateCollectionFailed)
		outcomeReason = "dispatching to FPP failed: " + dispatchErr.Error()
	} else {
		confirmed, outcomeState, outcomeReason = h.confirmFPPStatus(ctx, instanceID, fppStatusValueIdle)
	}

	outcomeWord := "unconfirmed"
	if confirmed {
		outcomeWord = "confirmed"
	}

	resolvedAt := h.now()
	resolvedState := "resolved"
	finalResult, _ := json.Marshal(commandResultPayload{Outcome: outcomeWord, StatusCode: outcome.StatusCode, Body: outcome.Body})
	finalResultStr := string(finalResult)
	if err := h.deps.Commands.UpdateCommandOutcome(ctx, env.ID, store.CommandOutcomeUpdate{
		ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("failed to record fpp command outcome", "commandId", env.ID, "error", err)
	}

	// --- Outcome audit entry: a SEPARATE, correlated entry — never a
	// mutation of the dispatch row above. ---
	outcomeDegraded := h.writeSafetyClassAudit(ctx, resolvedAt, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: env.ID,
		Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})

	dispatchedAtStr := formatTime(dispatchedAt)
	resolvedAtStr := formatTime(resolvedAt)
	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: env.ID, IdempotencyKey: env.IdempotencyKey, Action: env.Action, InstanceID: instanceID,
			Replay: false, Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
			AttributionDegraded: dispatchDegraded || outcomeDegraded,
			DispatchedAt:        &dispatchedAtStr,
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
func (h *handlers) handleFPPCommandReplay(w http.ResponseWriter, r *http.Request, now time.Time, existing store.CommandRecord, instanceID string) {
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

// confirmFPPStatus polls [ObservationLister] for fppStatusSignal on (fpp,
// instanceID) every h.fppCommandPollInterval until it observes wantValue
// as StateCurrent, or h.fppCommandConfirmDeadline elapses — ADR-003:
// confirmation is by evidence against an explicit deadline, never a
// synchronous assumption that a 200 from FPP means the state actually
// moved.
//
// The wait itself is bound by REAL wall-clock time (time.Now/time.Ticker),
// deliberately independent of h.now() (which a test fixes at one instant
// via [Options.Clock] to make every STAMPED timestamp deterministic —
// dispatchedAt, resolvedAt, serverTime). A confirmation loop paced by a
// clock that never advances would never terminate; h.now() is used only
// to evaluate an observation's OWN freshness ([observation.Observation.StateAt]),
// matching every other handler in this package's identical use of h.now()
// for that purpose.
func (h *handlers) confirmFPPStatus(ctx context.Context, instanceID, wantValue string) (confirmed bool, outcomeState, outcomeReason string) {
	deadline := time.Now().Add(h.fppCommandConfirmDeadline)
	ticker := time.NewTicker(h.fppCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason = h.checkFPPStatusOnce(ctx, instanceID, wantValue)
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
// observation for (fpp, instanceID), evaluated against wantValue.
func (h *handlers) checkFPPStatusOnce(ctx context.Context, instanceID, wantValue string) (confirmed bool, outcomeState, outcomeReason string) {
	kind := observation.ResourceFPP
	sig := observation.SignalID(fppStatusSignal)
	obs, err := h.deps.Observations.ListObservations(ctx, ObservationFilter{
		ResourceKind: &kind, ResourceID: &instanceID, Signal: &sig,
	})
	if err != nil {
		return false, string(observation.StateCollectionFailed), "reading fpp.status for confirmation: " + err.Error()
	}

	now := h.now()
	for _, o := range obs {
		if o.Resource.Kind != kind || o.Resource.ID != instanceID || o.Signal != sig {
			continue
		}
		state := o.StateAt(now)
		if state == observation.StateCurrent {
			if o.Value == wantValue {
				return true, string(state), ""
			}
			return false, string(state), fmt.Sprintf("observed fpp.status = %v, want %q", o.Value, wantValue)
		}
		reason := o.Reason
		if reason == "" {
			reason = fmt.Sprintf("fpp.status evidence state is %s", state)
		}
		return false, string(state), reason
	}
	return false, string(observation.StateNotCollected), "no fpp.status observation is recorded for this instance yet"
}

// writeSafetyClassAudit writes entry via h.deps.Identity.WriteAudit and
// reports whether it had to degrade. Named for ADR-024 decision 11's
// blackout/stop/power-off safety class, which Stop Playlist is a member
// of: unlike [handlers.writeAuditOrFail] (auth.go), which REFUSES the
// write it protects on an audit-write failure, this method never blocks
// its caller. On failure it writes a best-effort, human-readable line to
// os.Stderr — never to h.logger alone, whose own destination this
// coordinator controls no more durably than it controls audit_log's, and
// decision 11's exemption exists precisely for the case where NEITHER may
// be trusted (disk exhaustion is the named trigger) — carrying everything
// the audit entry would have, and returns true so the caller can flag the
// command's own wire response as attribution-degraded rather than
// silently absorbing the gap.
func (h *handlers) writeSafetyClassAudit(ctx context.Context, now time.Time, entry identity.AuditEntry) (degraded bool) {
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		fmt.Fprintf(os.Stderr,
			"showmesh: DEGRADED ATTRIBUTION (audit write failed; command proceeded anyway per ADR-024 decision 11's "+
				"blackout/stop/power-off safety class) time=%s principal=%s(%s) action=%s target=%s commandId=%s "+
				"kind=%s idempotencyKey=%s error=%v\n",
			now.Format(time.RFC3339), entry.PrincipalName, entry.PrincipalID, entry.Action, entry.Target,
			entry.CommandID, entry.Kind, entry.IdempotencyKey, err)
		h.logWarn("audit write failed for a safety-class command; proceeding with degraded attribution",
			"error", err, "commandId", entry.CommandID, "kind", entry.Kind)
		return true
	}
	return false
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
