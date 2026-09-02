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
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Step 7 seam C's original write endpoint, generalized by
// Step 8 from one hardcoded primitive ("stopPlaylist") to
// docs/bench/fpp-command-vocabulary.md section 4's full eight-primitive
// registry (fppcommand_primitives.go). Bound above all by ADR-001 (this
// dispatches FPP's own native commands; it never schedules) and ADR-003 (a
// 200 from FPP is not success; confirmation is by evidence, against an
// explicit per-primitive deadline). See this task's report for the full
// accounting against every acceptance criterion.
//
// Step 7's own review-fix defects (2, 3, 4, 6, 8, 9 in this file; 1 partly
// in api.go/pkg/command; 5 in fppcommand_reconcile.go; 7 in
// internal/coordinator/fppcommand and collector/fpp's import-graph test)
// are preserved unconditionally by this generalization — see this file's
// own report for how each was verified to still hold for every primitive,
// not just stopPlaylist.

// scopeFPPCommand exists only so api.go's route registration can take its
// address: [handlers.writeGuard] takes *identity.Scope (nil means "any
// authenticated principal, no specific scope" — DELETE /api/v1/session),
// and a Go constant's address cannot be taken directly.
var scopeFPPCommand = identity.ScopeFPPCommand

// fppActionStopPlaylist is Step 7's own wire action name, UNCHANGED by
// this generalization — see primitiveStopPlaylist in
// fppcommand_primitives.go, which is the only primitive this file's
// registry marks as already deployed.
const fppActionStopPlaylist = "stopPlaylist"

// auditActionFPPStopPlaylist is Step 7's own internal audit action
// identifier, UNCHANGED — see [fppActionStopPlaylist]'s doc comment.
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
// confirmed live against a bench fppd (see Step 7's own report) to be
// exactly what FPP's own status_name reads once "Stop Now" has taken
// effect.
const (
	fppStatusSignal    = "fpp.status"
	fppStatusValueIdle = "idle"
)

// maxFPPCommandRequestBodyBytes bounds this endpoint's request body,
// mirroring session.go's maxSessionRequestBodyBytes convention (a request
// this small has no legitimate reason to be large; a caller sending more
// gets a decode error rather than this handler reading an unbounded body).
// Left unchanged by Step 8's params object: even startPlaylist's largest
// realistic body (a playlist name, two booleans) fits comfortably within
// this bound.
const maxFPPCommandRequestBodyBytes = 4 << 10 // 4 KiB

// dbWriteTimeout bounds each individual piece of post-dispatch
// bookkeeping (a commands-row update, an outcome audit entry) once it is
// no longer tied to the client's own request context — see bgCtx's own
// comment in [handlers.dispatchFPPCommand] (fppcommand_dispatch.go; Step 7
// seam C review defect 4).
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
// happened. Params is NOT carried here — it lives in
// store.CommandRecord.ParamsJSON, its own column, and is echoed onto the
// wire from there (or from the freshly normalized params on a fresh
// dispatch) rather than duplicated into this payload too.
type commandResultPayload struct {
	Outcome    string `json:"outcome,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
	Body       string `json:"body,omitempty"`

	// PipelineFailed is Finding 15's own field, set only by
	// renderdispatch.go's evaluateRenderSurfaceState: true when the
	// surface.pipeline.state evidence confirming/refusing a render command
	// is itself the pipeline's own reported "failed" value, distinct from
	// merely absent, stale, or wrong-but-otherwise-healthy evidence.
	// OutcomeState (both here and on the wire) only ever carries
	// pkg/observation's six-value State vocabulary, never a pipeline
	// state — this field is what lets a caller (showmeshctl's
	// exitRenderPipelineDown) distinguish the two without parsing
	// OutcomeReason's free text. Irrelevant, and always false/omitted, for
	// every non-render command this payload also serves.
	PipelineFailed bool `json:"pipelineFailed,omitempty"`
}

// fppCommandRecordFor builds the commands-table row for env, with
// paramsJSON (already canonical — see [canonicalParamsJSON]) as its own
// ParamsJSON column. Factored out so both the transactional path and the
// safety-class degraded fallback (Step 7 seam C review defect 8; see
// [handlers.dispatchFPPCommand] in fppcommand_dispatch.go) build the IDENTICAL row rather than two
// copies of this struct literal that could silently drift apart from each
// other.
func fppCommandRecordFor(env command.Envelope, paramsJSON string) store.CommandRecord {
	return store.CommandRecord{
		ID:                  env.ID,
		IdempotencyKey:      env.IdempotencyKey,
		Action:              env.Action,
		TargetKind:          env.Target.Kind,
		TargetID:            env.Target.ID,
		ParamsJSON:          paramsJSON,
		IssuerPrincipalID:   env.Issuer.PrincipalID,
		IssuerPrincipalName: env.Issuer.PrincipalName,
		CallerIntent:        env.RequestedRevision,
		ConfirmationMethod:  string(env.ConfirmationMethod),
		DeadlineAt:          env.Deadline,
		State:               "pending",
	}
}

// utcTimePtr returns a copy of t converted to UTC, or nil for a nil t —
// see this file's own comment at its one call site (the fresh-dispatch
// response write in [handlers.dispatchFPPCommand], fppcommand_dispatch.go)
// for why a FRESH dispatch's
// h.now()-derived DispatchedAt must be normalized identically to how
// store/queries.go already normalizes a REPLAY's stored equivalent before
// either reaches the wire (Step 8 review finding 14).
func utcTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// fppQuotedWireActions formats [fppCommandWireActions] for an
// "unsupported action" problem detail — every supported action, quoted
// and comma-separated, rather than Step 7's single hardcoded name.
func fppQuotedWireActions() string {
	actions := fppCommandWireActions()
	quoted := make([]string, len(actions))
	for i, a := range actions {
		quoted[i] = strconv.Quote(a)
	}
	return strings.Join(quoted, ", ")
}

// handleFPPCommand serves POST /api/v1/fpp/{instanceId}/commands, behind
// writeGuard(&identity.ScopeFPPCommand, ...) — so by the time this method
// runs, ADR-024 decision 4's scope check and decision 6's CSRF check have
// both already passed, and [authFromContext] is guaranteed to report
// ac.ok == true with a principal holding fpp:command.
//
// As of Step 9's fppcommand_dispatch.go, this method is a thin WIRE
// ADAPTER: it does only what is intrinsically HTTP-shaped (bound the
// response write deadline, validate the path's instanceId syntax, decode
// the JSON body, resolve the wire action far enough to run
// [decodeFPPCommandParams] against it, resolve the calling principal via
// [authFromContext]/[handlers.clientAddr]) and then hands off to
// [handlers.dispatchFPPCommand] — the SAME dispatch/confirm/audit core
// [FPPCommandDispatcher.Dispatch] (fppcommand_dispatch.go) calls
// in-process for Step 9's macro executor. Every review finding that core
// preserves (idempotency-first ordering, the three-way replay rule, the
// CollectedAt confirmation fence, the safety-class audit rule,
// context.WithoutCancel detachment) is documented there, once, rather than
// here and there in two copies that could drift apart — see
// fppcommand_dispatch.go's own top comment and
// [handlers.dispatchFPPCommand]'s doc comment for the full numbered
// accounting; this method's own job ends at "decode the wire request and
// render the wire response."
func (h *handlers) handleFPPCommand(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	// internal/coordinator/httpapi.NewServer configures a WriteTimeout on
	// the *http.Server this handler is mounted on — a reasonable default
	// for this package's ordinary REST-style routes, and net/http.Server.
	// WriteTimeout bounds the ENTIRE response-writing phase of one
	// request, not merely one write. This handler can legitimately hold
	// the connection open past that default while the confirmation wait
	// runs out the eventually-resolved primitive's own deadline, so left
	// unguarded the coordinator's own HTTP server would silently sever the
	// connection out from under a still-working confirmation wait —
	// discovered only by running the real binary against the bench fppd
	// and watching curl report "Empty reply from server" seconds before
	// the handler had finished, the exact shape of defect stream.go's own
	// resetWriteDeadline exists to prevent for the SSE stream (see that
	// function's doc comment, "finding 1.1"). Set once, unlike stream.go's
	// per-write reset, because this handler performs exactly one write
	// (jsonWrite, at the very end) rather than a long-lived series of
	// them.
	//
	// This runs before the request body has even been read, so which
	// primitive will end up dispatched is not known yet — bounded instead
	// by [fppMaxConfirmDeadline]'s max over EVERY registered primitive's
	// own ConfirmDeadline function, evaluated against h.fppCommandConfirmDeadline
	// (the coordinator's own CONFIGURED base — Options.FPPCommandConfirmDeadline
	// in production, a test's own shrunk value in a test), never against
	// [command.MaxFPPCommandConfirmDeadline]'s fixed constant alone: that
	// constant is sized for the DEFAULT base and documents what a CLIENT
	// budgets against (a client has no way to learn a deployment's
	// configured override), but this write deadline guards this SERVER's
	// own connection against its own actually-configured wait — using the
	// fixed constant here would let an operator who configures a LARGER
	// FPPCommandConfirmDeadline reintroduce finding 1.1's exact defect,
	// invisibly, the day they did that.
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(fppMaxConfirmDeadline(h.fppCommandConfirmDeadline) + 30*time.Second))

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	// --- Decode the body far enough to resolve the primitive and its
	// wire-shaped params (JSON-specific; [handlers.dispatchFPPCommand]
	// itself never sees a json.RawMessage — see [FPPCommandInput]'s own
	// doc comment for why that split is where it is). ---

	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxFPPCommandRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be a JSON object matching {\"action\":string,\"idempotencyKey\":string,\"params\":object?}"))
		return
	}

	actionRaw, hasAction := top["action"]
	if !hasAction {
		writeProblem(w, h.logger, now, invalidParameterProblem("action is required"))
		return
	}
	var action string
	if err := json.Unmarshal(actionRaw, &action); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("action must be a string"))
		return
	}
	primitive, ok := fppPrimitivesByWireAction[action]
	if !ok {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("unsupported action %q; this coordinator supports: %s", action, fppQuotedWireActions())))
		return
	}

	normalizedParams, paramProblem := decodeFPPCommandParams(primitive, top)
	if paramProblem != nil {
		writeProblem(w, h.logger, now, *paramProblem)
		return
	}

	var idempotencyKey string
	if idemRaw, hasIdem := top["idempotencyKey"]; hasIdem {
		// A non-string or explicit null both leave idempotencyKey at its
		// zero value (""), which [command.ValidateIdempotencyKey] rejects
		// (inside [handlers.dispatchFPPCommand]) exactly as an absent key
		// would — this endpoint does not need a THIRD distinct message for
		// idempotencyKey the way section 2 requires for params, since
		// ValidateIdempotencyKey's own "empty" rejection already covers
		// every one of these shapes correctly (there is no default to
		// silently apply for a required, caller-minted key).
		_ = json.Unmarshal(idemRaw, &idempotencyKey)
	}

	// The calling principal — [FPPCommandIssuer]'s wire-path construction.
	// [handlers.dispatchFPPCommand] never reads [authFromContext] or
	// [handlers.clientAddr] itself; an in-process caller has neither an
	// *http.Request nor this request's own auth context to read them from.
	ac := authFromContext(ctx)
	issuer := FPPCommandIssuer{
		PrincipalID:   ac.result.Principal.ID,
		PrincipalName: ac.result.Principal.Name,
		Form:          ac.result.Form,
		CredentialID:  ac.result.CredentialID,
		ClientAddr:    h.clientAddr(r),
	}

	outcome, problem, dispatchErr := h.dispatchFPPCommand(ctx, now, FPPCommandInput{
		InstanceID:     instanceID,
		Action:         action,
		Params:         normalizedParams,
		IdempotencyKey: idempotencyKey,
		Issuer:         issuer,
	})
	if dispatchErr != nil {
		var ierr *fppCommandInternalError
		if errors.As(dispatchErr, &ierr) {
			h.writeInternalError(w, now, ierr.label, ierr.err)
		} else {
			h.writeInternalError(w, now, "dispatch fpp command", dispatchErr)
		}
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: outcome.CommandID, IdempotencyKey: outcome.IdempotencyKey, Action: outcome.Action, InstanceID: outcome.InstanceID,
			Params:  outcome.Params,
			Replay:  outcome.Replay,
			Outcome: outcome.Outcome, OutcomeState: outcome.OutcomeState, OutcomeReason: outcome.OutcomeReason,
			AttributionDegraded: outcome.AttributionDegraded,
			DispatchedAt:        formatTimePtr(outcome.DispatchedAt),
			ResolvedAt:          formatTimePtr(outcome.ResolvedAt),
		},
	})
}

// setFPPCommandDesiredState writes every desired_state row primitive.DesiredState
// asks for (nil-safe: nextPlaylistItem/prevPlaylistItem carry no
// DesiredState function at all, per their own doc comments), inside tx.
// desired_state is advisory bookkeeping with no reconciler (store's own
// standing rule): a failure on any one row must never cost the command
// its own row or its dispatch audit entry, so this logs and continues
// rather than returning an error (which would roll back the whole
// transaction).
func (h *handlers) setFPPCommandDesiredState(ctx context.Context, tx *store.Tx, primitive fppPrimitive, env command.Envelope, now time.Time, params map[string]any) {
	if primitive.DesiredState == nil {
		return
	}
	for _, rec := range primitive.DesiredState(env, now, params) {
		if _, err := tx.SetDesiredState(ctx, rec); err != nil {
			h.logWarn("failed to record desired state for fpp command", "commandId", env.ID, "signal", rec.Signal, "error", err)
		}
	}
}

// setFPPCommandDesiredStateNonTx is [handlers.setFPPCommandDesiredState]'s
// non-transactional sibling, used only on the ADR-024 decision 11
// safety-class degraded-attribution path (defect 8's fallback), where the
// atomic path could not be taken.
func (h *handlers) setFPPCommandDesiredStateNonTx(ctx context.Context, primitive fppPrimitive, env command.Envelope, now time.Time, params map[string]any) {
	if primitive.DesiredState == nil {
		return
	}
	for _, rec := range primitive.DesiredState(env, now, params) {
		if _, err := h.deps.Commands.SetDesiredState(ctx, rec); err != nil {
			h.logWarn("failed to record desired state for fpp command", "commandId", env.ID, "signal", rec.Signal, "error", err)
		}
	}
}

// confirmFPPCommand polls primitive.Confirm — every primitive's own
// predicate, see fppcommand_primitives.go and fppcommand_evidence.go —
// every h.fppCommandPollInterval until it confirms or
// primitive.ConfirmDeadline(h.fppCommandConfirmDeadline) elapses.
// Generalizes Step 7's own confirmFPPStatus (which polled
// [evaluateFPPStatusEvidence] specifically) to any primitive's own
// Confirm function, carrying the identical two guarantees forward
// unconditionally: ADR-003's evidence-against-a-deadline rule (never a
// synchronous assumption that a 200 from FPP means the state actually
// moved), and Step 7 seam C review defect 2 (never a pre-existing
// reading that merely happens to already agree — notBefore is threaded
// through to every Confirm call unchanged).
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
func (h *handlers) confirmFPPCommand(ctx context.Context, primitive fppPrimitive, instanceID string, params map[string]any, baseline fppBaseline, notBefore time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	absDeadline := time.Now().Add(primitive.ConfirmDeadline(h.fppCommandConfirmDeadline))
	ticker := time.NewTicker(h.fppCommandPollInterval)
	defer ticker.Stop()

	for {
		confirmed, outcomeState, outcomeReason = primitive.Confirm(ctx, h.deps.Observations, instanceID, params, baseline, notBefore, h.now())
		if confirmed {
			return true, outcomeState, outcomeReason
		}
		if !time.Now().Before(absDeadline) {
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

// updateCommandOutcomeBounded wraps h.deps.Commands.UpdateCommandOutcome
// with its own dbWriteTimeout, derived from parent (bgCtx in production —
// see [handlers.dispatchFPPCommand]'s own comment (fppcommand_dispatch.go)
// on why post-dispatch bookkeeping is no longer bound by the client's
// request context).
func (h *handlers) updateCommandOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}

// degradedAttributionReasonSafetyClassExemption,
// degradedAttributionReasonPostDispatch, degradedAttributionReasonMacroRunNeverWithheld,
// and degradedAttributionReasonAuditNeverBlocks are the four, and ONLY the
// four, justifications [handlers.reportDegradedAttribution] may report for
// proceeding without a durable audit entry. They are deliberately
// different strings: no two call sites that use them are the same
// decision wearing two names, and conflating them was this task's own
// finding (the doc comment on the pre-fix writeSafetyClassAudit claimed
// EVERY use of it was decision 11's safety class, which stopped being true
// the moment it started being called for a fppSafetyClassNotExempt
// primitive's own post-dispatch outcome).
const (
	// degradedAttributionReasonSafetyClassExemption is
	// [handlers.dispatchFPPCommand]'s dispatch-side (PRE-dispatch) fallback
	// (fppcommand_dispatch.go): primitive.SafetyClass == fppSafetyClassExempt, so ADR-024 decision
	// 11's own named safety class (stopPlaylist, stopPlaylistGracefully)
	// proceeds regardless of the audit-write failure. This is the ONLY
	// call site where "degraded because of the safety-class exemption" is
	// literally true.
	degradedAttributionReasonSafetyClassExemption = "ADR-024 decision 11's blackout/stop/power-off safety class exemption (pre-dispatch write)"

	// degradedAttributionReasonPostDispatch covers every OTHER
	// [handlers.writeBestEffortAudit]/[handlers.writeBestEffortAuditBounded]
	// call site: the post-dispatch outcome entry (every primitive,
	// including a fppSafetyClassNotExempt one — by this point the command
	// has already been dispatched, or the attempt already made) and the
	// replay entry (every primitive — a replay dispatches nothing by
	// construction). Neither is decision 11's safety-class exemption; both
	// are decision 11's OWN reasoning applied a layer later: refusing to
	// answer here cannot undo an effect that has already happened (or, for
	// a replay, was already resolved), it can only deny the operator the
	// record of it — ADR-024's own "you cannot act" vs. "you cannot see"
	// distinction, landing on the "you cannot see" side this time, which
	// is never acceptable per that record's own reasoning.
	degradedAttributionReasonPostDispatch = "the event this entry records already happened and cannot be un-recorded; refusing to answer would only deny the operator the record of it (ADR-024: \"you cannot see\", never acceptable), not protect them from anything"

	// degradedAttributionReasonMacroRunNeverWithheld is
	// [handlers.dispatchFPPCommand]'s pre-dispatch fallback when the caller
	// set [FPPCommandInput.NeverWithholdOnAuditFailure] — Step 9's macro
	// executor, and nothing else. Deliberately its own string rather than
	// reusing the safety-class one: the safety class of the step that
	// reaches this branch is usually NOT exempt (that is the entire point),
	// so reporting decision 11's exemption here would be the same
	// conflation the two constants above already exist to prevent, and
	// would make an audit record claim a justification that does not
	// apply to it.
	degradedAttributionReasonMacroRunNeverWithheld = "this dispatch belongs to a macro run, which never withholds a command for an audit failure (owner decision 2026-08-14, superseding ADR-024 decision 11's fail-closed default inside a run)"

	// degradedAttributionReasonAuditNeverBlocks is the pre-dispatch
	// fallback for a dispatch that reaches [handlers.dispatchFPPCommand],
	// [handlers.executeAudioSessionDispatch], or [handlers.handleInvokeAction]'s
	// own audit-write failure branch OUTSIDE both of the other two
	// pre-dispatch cases above: not a member of decision 11's own named
	// safety class, and not a macro run. Before 2026-08-26 this branch did
	// not exist: the dispatch was refused with a 503 instead. OWNER
	// RULING, 2026-08-26: "Audit log database becoming unavailable SHOULD
	// NOT STOP ANY ACTIONS, rather than stopping it should be LOUD in the
	// UI and non-audit logs about it. Audit logging is NOT a show stopping
	// issue... it should NOT stop the show or any actions from running. If
	// the audit log being down currently blocks actions, that must be
	// corrected." Recorded as ADR-024 decision 11's own amendment, not as
	// a fourth safety class: every command dispatch now proceeds
	// regardless of SafetyClass, so this reason exists to keep an
	// investigator able to tell "this ran because it is blackout/stop/
	// power-off" and "this ran because a macro run never withholds" apart
	// from "this ran because audit unavailability no longer blocks
	// anything" (three different facts about the same command, not one
	// fact reported three ways).
	degradedAttributionReasonAuditNeverBlocks = "ADR-024 decision 11's audit-unavailability-never-blocks rule (owner ruling 2026-08-26): this action is not a member of the blackout/stop/power-off safety class and does not belong to a macro run, and still proceeds without a durable pre-dispatch audit entry"
)

// writeBestEffortAuditBounded is [handlers.writeBestEffortAudit] with its
// own dbWriteTimeout applied to parent, for the identical reason
// [updateCommandOutcomeBounded] exists.
func (h *handlers) writeBestEffortAuditBounded(parent context.Context, now time.Time, reason string, entry identity.AuditEntry) (degraded bool) {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	return h.writeBestEffortAudit(ctx, now, reason, entry)
}

// writeBestEffortAudit writes entry via h.deps.Identity.WriteAudit and
// reports whether it had to degrade. Renamed from writeSafetyClassAudit:
// that name was accurate for this endpoint's ORIGINAL, single caller (Step
// 7's dispatch-side stopPlaylist exemption) and became misleading once
// Step 8 pointed it at every primitive's post-dispatch outcome entry and
// every primitive's replay entry, safety-class or not — see
// [degradedAttributionReasonPostDispatch]'s own doc comment for why those
// two call sites are never refused regardless of
// [fppPrimitive.SafetyClass].
//
// Unlike [handlers.writeAuditOrFail] (auth.go), which REFUSES the write it
// protects on an audit-write failure, this method never blocks its caller
// — that asymmetry is deliberate and is NOT the same fail-closed rule
// [handlers.dispatchFPPCommand]'s pre-dispatch write (fppcommand_dispatch.go)
// applies for a
// fppSafetyClassNotExempt primitive: fail-closed is meaningful only
// BEFORE an effect has happened, where refusing genuinely prevents an
// unattributed action. Every call site that reaches this method runs
// AFTER that point — either the command has already been dispatched (the
// outcome entry) or nothing was ever going to be dispatched (the replay
// entry) — so refusing here would not prevent anything; it would only
// hide a true record from the operator, which is the "you cannot see"
// failure ADR-024 decision 7 and decision 11 both exist to keep out of
// this architecture. On failure this calls
// [handlers.reportDegradedAttribution] and returns true so the caller can
// flag the command's own wire response as attribution-degraded rather than
// silently absorbing the gap.
func (h *handlers) writeBestEffortAudit(ctx context.Context, now time.Time, reason string, entry identity.AuditEntry) (degraded bool) {
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.reportDegradedAttribution(now, entry, err, reason)
		return true
	}
	return false
}

// reportDegradedAttribution is the best-effort, human-readable stderr line
// written whenever a command proceeds (or a post-dispatch/replay audit
// entry goes unrecorded) despite an audit-write failure — never to
// h.logger alone, whose own destination this coordinator controls no more
// durably than it controls audit_log's, and both call sites exist
// precisely for the case where NEITHER may be trusted (disk exhaustion is
// the named trigger). reason is one of the two
// degradedAttributionReason... constants above, named explicitly by the
// caller rather than inferred here, so this function cannot itself
// misattribute a post-dispatch degrade to the safety-class exemption or
// vice versa — the exact conflation this task's own finding named. Factored
// out of [handlers.writeBestEffortAudit] so the dispatch-side degraded
// fallback in [handlers.dispatchFPPCommand] (fppcommand_dispatch.go; Step 7
// seam C review defect 8's AuditedWrite fallback path, which fails before entry is ever handed to
// WriteAudit at all) can report the identical shape rather than a second,
// drifted copy of it.
func (h *handlers) reportDegradedAttribution(now time.Time, entry identity.AuditEntry, err error, reason string) {
	fmt.Fprintf(os.Stderr,
		"showmesh: DEGRADED ATTRIBUTION (audit write failed; proceeding without it: %s) time=%s principal=%s(%s) "+
			"action=%s target=%s commandId=%s kind=%s idempotencyKey=%s error=%v\n",
		reason, now.Format(time.RFC3339), entry.PrincipalName, entry.PrincipalID, entry.Action, entry.Target,
		entry.CommandID, entry.Kind, entry.IdempotencyKey, err)
	h.logWarn("audit write failed; proceeding with degraded attribution",
		"reason", reason, "error", err, "commandId", entry.CommandID, "kind", entry.Kind)
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

// logDebug is [handlers.logWarn]'s identical nil-safe wrapper at Debug
// level, for a condition that is evidence a caller should be able to find,
// never a silent no-op, but is not itself a failure or a refusal (a
// suppressed replay of an unchanged blackAndSilence episode,
// cueactivationloop.go).
func (h *handlers) logDebug(msg string, args ...any) {
	if h.logger != nil {
		h.logger.Debug("api: "+msg, args...)
	}
}

// logError is [handlers.logWarn]'s identical nil-safe wrapper at Error
// level, for a failure that is deliberately louder than an ordinary
// best-effort warn: fppobservations.go's sequence-regression marker write
// is the one call site today (see that file's own doc comment on the
// regression branch) — a control input's own write failing must not read,
// in the logs, the same as a forensic audit write's ordinary best-effort
// swallow.
func (h *handlers) logError(msg string, args ...any) {
	if h.logger != nil {
		h.logger.Error("api: "+msg, args...)
	}
}
