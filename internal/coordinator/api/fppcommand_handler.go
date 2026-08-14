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

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
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

// ProblemTypeFPPCommandRefusedAuditUnavailable and its constructor
// (fppCommandAuditUnavailableProblem) now live in problem.go alongside
// this package's other problem constructors — moved there (and added to
// api/openapi.yaml's Problem.type enum in the same change) once this
// file's own seam boundary no longer forbade touching problem.go. Both
// gaps were flagged by this comment's own earlier version, not discovered
// later.

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
// happened. Params is NOT carried here — it lives in
// store.CommandRecord.ParamsJSON, its own column, and is echoed onto the
// wire from there (or from the freshly normalized params on a fresh
// dispatch) rather than duplicated into this payload too.
type commandResultPayload struct {
	Outcome    string `json:"outcome,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
	Body       string `json:"body,omitempty"`
}

// fppCommandRecordFor builds the commands-table row for env, with
// paramsJSON (already canonical — see [canonicalParamsJSON]) as its own
// ParamsJSON column. Factored out so both the transactional path and the
// safety-class degraded fallback (Step 7 seam C review defect 8; see
// [handlers.handleFPPCommand]) build the IDENTICAL row rather than two
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
		ConfirmationMethod:  string(env.ConfirmationMethod),
		DeadlineAt:          env.Deadline,
		State:               "pending",
	}
}

// utcTimePtr returns a copy of t converted to UTC, or nil for a nil t —
// see this file's own comment at its one call site (the fresh-dispatch
// response write in [handlers.handleFPPCommand]) for why a FRESH dispatch's
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
// The order below is ADR-024 decision 11's, and it is NOT the same order
// seam A's coordinator-local configuration write follows. It is also NOT
// Step 8's original order: Step 8 review finding 4 proved, end to end
// against a real dispatch, that running the pre-dispatch guard (the old
// step 3) BEFORE recognizing a replayed idempotency key (the old step 4)
// answers a legitimate replay with a 409 the guard invented for what it
// wrongly evaluated as a brand-new request — dispatched
// `startPlaylist holiday-show` idle with key `key-replay` (one FPP hit,
// 200), the client never saw the response, FPP's own scheduler moved on to
// a different show, and resending the byte-identical body and key answered
// 409 instead of the documented replay. That is this project's own
// recurring "a client that gives up before the server answers deletes an
// outcome from existence" lesson, except here the SERVER deleted it. The
// fix is idempotency-first: recognize an existing key before any guard
// gets a chance to rule on the request at all.
//
//  1. Decode the body, resolve the requested primitive from this file's
//     registry, decode and validate its params (section 2's
//     absent/null/empty rule; fpp values validated through
//     internal/coordinator/fppcommand's own exported validators),
//     validate the idempotency key, and canonicalize params
//     ([canonicalParamsJSON]) — needed below to tell a true replay apart
//     from a params conflict before anything else runs. Any failure here
//     dispatches nothing and stores nothing.
//  2. Look up the target FPP instance (404 if unconfigured).
//  3. Look up the idempotency key BEFORE anything decides whether this
//     request is even allowed to proceed
//     ([handlers.deps.Commands.GetCommandByIdempotencyKey]). A HIT means
//     this key has already been used: answer it via
//     [handlers.handleFPPCommandReplay] exactly as a race-detected
//     duplicate at step 5 would (replay, or a 409 conflict if the action,
//     target, or params differ) — WITHOUT ever running step 4's guard,
//     because a replay's whole point is to report what already happened,
//     not to re-litigate whether it should have. A MISS (this coordinator
//     has never recorded this key at all) proceeds to step 4 — the guard
//     still runs, and still refuses, for a genuinely NEW command; nothing
//     about this fix weakens that. This lookup is a best-effort read, not
//     the mechanism that keeps replay detection race-free for two
//     concurrent NEW requests sharing one key — that guarantee remains
//     step 5's own atomic INSERT and its UNIQUE-constraint violation (see
//     [CommandStore.GetCommandByIdempotencyKey]'s own doc comment, in
//     interfaces.go): if this read races a concurrent first insert and misses,
//     the request falls through to steps 4 and 5 exactly as it always
//     did, and step 5's own DuplicateCommandError handling still catches
//     it. A command that was ITSELF refused by the guard (or by any step
//     1/2 validation) is never inserted in the first place — "any failure
//     here dispatches nothing and stores nothing" — so resending that
//     same key after a refusal finds nothing here and is correctly
//     re-evaluated fresh, never answered as a stale replay of a refusal.
//  4. Run the primitive's own PreDispatchCheck, if it has one —
//     startPlaylist's ifBusy guard is the only user today (capture
//     section 5). A refusal here is a 409, and — like every validation
//     failure above — dispatches nothing and stores nothing.
//  5. Mint the command id, insert the commands row, record every
//     desired_state row the primitive asks for (ADR-003's split,
//     expressed in storage), and write the DISPATCH audit entry BEFORE
//     dispatching — decision 11's write-before-dispatch rule for a
//     command sent outward — ALL IN ONE TRANSACTION when the audit store
//     is healthy (Step 7 seam C review defect 8: a crash between the
//     insert and the dispatch audit entry must never leave a commands row
//     with no audit record). A duplicate idempotency key stops here too —
//     the race step 3's own best-effort read did not catch — see
//     [handlers.handleFPPCommandReplay], which also checks for a PARAMS
//     conflict (Step 8's own extension: same action and target, different
//     params, is a 409, never a replay). On an AUDIT-WRITE failure
//     specifically (never any other failure), the primitive's own
//     [fppPrimitive.SafetyClass] decides what happens next, per ADR-024
//     decision 11 — and this is decided PER PRIMITIVE, not once for the
//     whole endpoint, because Step 8 found that Step 7's single-primitive
//     exemption had been inherited onto all eight primitives with no
//     review. [fppSafetyClassExempt] (stopPlaylist and
//     stopPlaylistGracefully — decision 11's own named "stop", and
//     nothing else) proceeds regardless, with degraded attribution — the
//     identical posture this handler always used before Step 7's own fix,
//     now reached only when the atomic path could not be taken. Every
//     other primitive is [fppSafetyClassNotExempt] and fails closed: the
//     transaction above already rolled back in full, so nothing is
//     re-inserted and nothing is dispatched — the request is refused with
//     a 503 naming the audit store as the cause (see
//     [fppCommandAuditUnavailableProblem]).
//  6. Capture any pre-dispatch baseline the primitive needs
//     (nextPlaylistItem/prevPlaylistItem only), then dispatch to FPP via
//     internal/coordinator/fppcommand — deliberately OUTSIDE any
//     transaction (network I/O must never run inside one; store/tx.go's
//     own rule) and, as of Step 8 review finding 14, deliberately no
//     longer on r.Context() either — see bgCtx's own comment below for
//     why the dispatch attempt itself must survive a client disconnect,
//     not merely the bookkeeping that records it.
//  7. On a successful dispatch, best-effort nudge the FPP collector to
//     poll this instance now (h.deps.Nudger.NudgePoll — 2026-08-13's
//     post-dispatch poll nudge; see that call site's own comment), then
//     confirm by evidence against the primitive's own deadline exactly as
//     before the nudge existed, and write the OUTCOME as a separate,
//     correlated audit entry — never by mutating the dispatch row
//     (audit_log has no update path, by design).
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

	// --- 1. Decode, resolve the primitive, decode+validate params,
	// validate the idempotency key. ---

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
	if primitive.ValidateParams != nil {
		if err := primitive.ValidateParams(normalizedParams); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
			return
		}
	}

	var idempotencyKey string
	if idemRaw, hasIdem := top["idempotencyKey"]; hasIdem {
		// A non-string or explicit null both leave idempotencyKey at its
		// zero value (""), which [command.ValidateIdempotencyKey] rejects
		// below exactly as an absent key would — this endpoint does not
		// need a THIRD distinct message for idempotencyKey the way
		// section 2 requires for params, since ValidateIdempotencyKey's
		// own "empty" rejection already covers every one of these shapes
		// correctly (there is no default to silently apply for a
		// required, caller-minted key).
		_ = json.Unmarshal(idemRaw, &idempotencyKey)
	}
	if err := command.ValidateIdempotencyKey(idempotencyKey); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("idempotencyKey: "+err.Error()))
		return
	}

	// paramsJSON is computed here, in step 1, rather than after the guard
	// the way Step 8 originally had it — step 3 below needs it to tell a
	// true replay apart from a params conflict BEFORE the guard runs at
	// all (finding 4).
	paramsJSON, err := canonicalParamsJSON(normalizedParams)
	if err != nil {
		h.writeInternalError(w, now, "canonicalize fpp command params", err)
		return
	}

	// --- 2. Look up the target FPP instance. ---

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

	// --- 3. Idempotency-first (finding 4): recognize an already-used key
	// BEFORE the guard below gets a chance to rule on this request at all.
	// A hit is answered exactly as a race-detected duplicate at step 5
	// would be — replay, or a conflict if action/target/params differ —
	// and the guard never runs for it. A miss (this exact error,
	// store.ErrCommandNotFound) means this key is genuinely new — the
	// guard below still applies in full; nothing here weakens it. See this
	// function's own doc comment for the full reasoning, including why a
	// resend of a key that was previously REFUSED (never inserted) finds
	// nothing here and is correctly re-evaluated fresh rather than being
	// mistaken for a stale replay.
	existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
	switch {
	case lookupErr == nil:
		h.handleFPPCommandReplay(w, r, now, existing, instanceID, primitive.AuditAction, paramsJSON)
		return
	case errors.Is(lookupErr, store.ErrCommandNotFound):
		// Genuinely new key — fall through to the guard and the insert.
	default:
		h.writeInternalError(w, now, "look up fpp command by idempotency key", lookupErr)
		return
	}

	// --- 4. The primitive's own pre-dispatch guard, if it has one
	// (startPlaylist's ifBusy check — capture section 5). Only reached for
	// a key this coordinator has never recorded (step 3 above). ---

	var ifNotRunning bool
	if primitive.PreDispatchCheck != nil {
		var refusal *v1.Problem
		ifNotRunning, refusal = primitive.PreDispatchCheck(ctx, h.deps.Observations, instanceID, normalizedParams, now)
		if refusal != nil {
			writeProblem(w, h.logger, now, *refusal)
			return
		}
	}

	ac := authFromContext(ctx)
	issuerID, issuerName := ac.result.Principal.ID, ac.result.Principal.Name

	env := command.Envelope{
		ID:                 uuid.NewString(),
		IdempotencyKey:     idempotencyKey,
		Action:             primitive.AuditAction,
		Target:             command.Target{Kind: string(observation.ResourceFPP), ID: instanceID},
		Params:             normalizedParams,
		Issuer:             command.Issuer{PrincipalID: issuerID, PrincipalName: issuerName},
		ConfirmationMethod: command.ConfirmationEvidence,
	}
	deadline := now.Add(primitive.ConfirmDeadline(h.fppCommandConfirmDeadline))
	env.Deadline = &deadline

	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditDispatch, CommandID: env.ID,
	}

	// --- 5. Insert, record every desired_state row, and write the
	// dispatch audit entry — atomically when the audit store is healthy
	// (defect 8). ---
	var dispatchDegraded bool
	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, err := tx.InsertCommand(ctx, fppCommandRecordFor(env, paramsJSON)); err != nil {
			return identity.AuditEntry{}, err
		}
		h.setFPPCommandDesiredState(ctx, tx, primitive, env, now, normalizedParams)
		return dispatchEntry, nil
	})

	var dup *store.DuplicateCommandError
	switch {
	case errors.As(auditErr, &dup):
		h.handleFPPCommandReplay(w, r, now, dup.Existing, instanceID, env.Action, paramsJSON)
		return
	case errors.Is(auditErr, identity.ErrAuditWrite):
		// store.Store.InTx has already rolled back the WHOLE transaction on
		// this error — including tx.InsertCommand's own effect — regardless
		// of which branch below runs. What happens next is decided PER
		// PRIMITIVE by [fppPrimitive.SafetyClass] (ADR-024 decision 11):
		// see fppcommand_primitives.go's [fppSafetyClass] doc comment for
		// the membership decision.
		if primitive.SafetyClass != fppSafetyClassExempt {
			// Fail closed (the default, and every primitive except
			// stopPlaylist/stopPlaylistGracefully): the rolled-back
			// transaction already means there is no commands row and no
			// desired_state row, so nothing is re-inserted here, and
			// nothing below this branch runs — dispatch is never reached.
			// This is ADR-024 decision 11's own default rule ("a write
			// that cannot be attributed does not proceed"), scoped to "you
			// cannot act", never "you cannot see": no read path is touched,
			// and the refusal is entirely local to this one write.
			writeProblem(w, h.logger, now, fppCommandAuditUnavailableProblem(primitive.WireAction, auditErr))
			return
		}
		// Safety-class exemption (ADR-024 decision 11): redo the insert and
		// desired-state writes through the plain, non-transactional store
		// methods, and proceed with degraded attribution — the exact
		// posture this handler always used before Step 7's own fix, now
		// reached only when the atomic path could not be taken.
		rec, err := h.deps.Commands.InsertCommand(ctx, fppCommandRecordFor(env, paramsJSON))
		if errors.As(err, &dup) {
			h.handleFPPCommandReplay(w, r, now, dup.Existing, instanceID, env.Action, paramsJSON)
			return
		}
		if err != nil {
			h.writeInternalError(w, now, "insert fpp command", err)
			return
		}
		_ = rec
		h.setFPPCommandDesiredStateNonTx(ctx, primitive, env, now, normalizedParams)
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedAttributionReasonSafetyClassExemption)
		dispatchDegraded = true
	case auditErr != nil:
		h.writeInternalError(w, now, "insert fpp command", auditErr)
		return
	}

	// --- 6. Capture any pre-dispatch baseline, then dispatch — outside
	// any transaction. ---

	// bgCtx carries none of r.Context()'s cancellation from THIS POINT
	// FORWARD — Step 7 seam C review defect 4 severed it from every
	// post-dispatch bookkeeping write; Step 8 review finding 14 moves the
	// cutover one step earlier, to before the dispatch ATTEMPT itself.
	// Defect 4's original fix still left primitive.Dispatch below running
	// on r.Context(), which means a client that simply closes its tab
	// while FPP is slow could abort the OUTBOUND command mid-flight — not
	// merely the record of it — while the preliminary write just below
	// still stamps dispatchedAt as though the attempt had gone out
	// normally. In the worst case the aborted command is a stop. A
	// dispatch attempt, once started, must run to its own conclusion
	// exactly like the bookkeeping that follows it; a client walking away
	// must not be able to sever either one. This is deliberately NOT
	// context.Background(): every write below still gets its own short,
	// bounded deadline via dbWriteTimeout, and the dispatch itself remains
	// bounded by internal/coordinator/fppcommand's own per-request timeout
	// (fppcommand.Client.Invoke's context.WithTimeout, applied inside
	// primitive.Dispatch regardless of what this context does or does not
	// carry) — "not cancellable by an abandoned client" must not become
	// "capable of hanging forever" either way.
	bgCtx := context.WithoutCancel(ctx)

	dispatchAttemptedAt := h.now()
	var baseline fppBaseline
	if primitive.CaptureBaseline != nil {
		baseline = primitive.CaptureBaseline(bgCtx, h.deps.Observations, instanceID, dispatchAttemptedAt)
	}

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
		outcome, dispatchErr = primitive.Dispatch(bgCtx, client, normalizedParams, ifNotRunning)
	}

	dispatchState := "dispatched"
	prelimResult, _ := json.Marshal(commandResultPayload{StatusCode: outcome.StatusCode, Body: outcome.Body})
	prelimResultStr := string(prelimResult)
	if err := h.updateCommandOutcomeBounded(bgCtx, env.ID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, State: &dispatchState, ResultJSON: &prelimResultStr,
	}); err != nil {
		h.logWarn("failed to record fpp command dispatch", "commandId", env.ID, "error", err)
	}

	// --- 7. Confirm by evidence, or report the dispatch failure
	// directly. ---
	var (
		confirmed     bool
		outcomeState  string
		outcomeReason string
	)
	if dispatchErr != nil {
		outcomeState = string(observation.StateCollectionFailed)
		outcomeReason = "dispatching to FPP failed: " + dispatchErr.Error()
	} else {
		// Post-dispatch poll nudge (owner decision, 2026-08-13): the FPP
		// REST collector's own cadence (fpp.DefaultPollInterval, 15s) made
		// every confirmation below wait for whatever was left of that
		// cycle — measured live against the bench at 3.5s-15.1s,
		// averaging ~7.5s of pure waiting for a command that reaches FPP
		// in milliseconds. h.deps.Nudger asks that SAME collector to poll
		// THIS instance now instead of waiting out its cadence — see
		// [FPPPollNudger]'s own doc comment for the full contract this
		// leans on: best-effort, rate-limited per instance, and never a
		// substitute for evidence. Its bool return is deliberately not
		// consulted here: whether the request was accepted, suppressed by
		// the rate limit, or answered by [noFPPPollNudger] (nothing
		// wired in), confirmFPPCommand below reads the exact same
		// [ObservationLister] through the exact same notBefore fence
		// either way — a nudge changes WHEN the collector's next poll
		// happens, never what confirms. NudgePoll cannot block this
		// path (see that method's doc comment), so this call is
		// synchronous and unbounded-wait-free by construction, not by a
		// timeout wrapped around it here.
		h.deps.Nudger.NudgePoll(instanceID)

		// notBefore = dispatchAttemptedAt (Step 7 seam C review defect 2):
		// confirmation must rest on evidence collected no earlier than the
		// moment this handler attempted dispatch, never on a stale reading
		// that merely happens to agree — applied identically to every
		// primitive's own Confirm function (fppcommand_evidence.go). The
		// nudge above does not change this fence in any way: a nudged
		// poll is an ordinary observation, produced by the exact same
		// collector code path a scheduled poll uses, indistinguishable in
		// kind, and resolved through the exact same [fppPrimitive.Confirm]
		// predicate below.
		confirmed, outcomeState, outcomeReason = h.confirmFPPCommand(bgCtx, primitive, instanceID, normalizedParams, baseline, dispatchAttemptedAt)
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
	// mutation of the dispatch row above. Best-effort for EVERY primitive,
	// including a fppSafetyClassNotExempt one: by this point the command
	// has already been dispatched (or the attempt already made) and that
	// effect cannot be un-dispatched, so refusing to answer here would only
	// hide an outcome the operator needs — see
	// [handlers.writeBestEffortAudit]'s own doc comment for why this is
	// deliberately NOT the same fail-closed rule the pre-dispatch write
	// above applies. ---
	outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: issuerID, PrincipalName: issuerName,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: env.Action, Target: instanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: env.ID,
		Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
	})

	// .UTC() before formatting (Step 8 review finding 14): store/queries.go
	// deliberately normalizes every persisted timestamp to UTC on write and
	// read back ("this package owns the format itself"), so a REPLAY
	// response's DispatchedAt/ResolvedAt — decoded from existing.DispatchedAt/
	// existing.ResolvedAt via [handlers.handleFPPCommandReplay] — always
	// render with a "Z" offset. dispatchAttemptedAt/resolvedAt here are the
	// h.now()-derived local variables for a FRESH dispatch, never
	// round-tripped through storage, so without this call they would
	// render in whatever offset the coordinator's configured clock happens
	// to use — both are valid RFC 3339, but the SAME underlying instant
	// then renders two different ways depending on whether a client is
	// looking at the original response or a later replay of it. Converting
	// here, rather than in mapping.go's shared formatTime (used by every
	// other handler in this package for values that never round-trip
	// through storage in the first place), keeps the fix scoped to the one
	// place this specific inconsistency can arise.
	dispatchedAtStr := formatTimePtr(utcTimePtr(dispatchedAt))
	resolvedAtStr := formatTime(resolvedAt.UTC())
	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(h.now()),
		Command: v1.FPPCommandResult{
			ID: env.ID, IdempotencyKey: env.IdempotencyKey, Action: env.Action, InstanceID: instanceID,
			Params:  normalizedParams,
			Replay:  false,
			Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
			AttributionDegraded: dispatchDegraded || outcomeDegraded,
			DispatchedAt:        dispatchedAtStr,
			ResolvedAt:          &resolvedAtStr,
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

// handleFPPCommandReplay answers a replayed idempotency key: NOTHING is
// dispatched, and existing's own already-recorded result is returned
// verbatim — ADR-024 decision 11: "a replay is precisely the case an
// investigator wants to see, because it means the operator did not get
// their response." A replay audit entry is written best-effort, for EVERY
// primitive regardless of [fppPrimitive.SafetyClass] — see
// [handlers.writeBestEffortAudit]'s own doc comment for why a replay
// specifically must never fail closed: it dispatches nothing by
// construction, so refusing to answer it would deny the operator the
// record of what already happened, which is "you cannot see", not "you
// cannot act". Correlated by existing.ID, never a mutation of the original
// dispatch or outcome entries.
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
// requestedParamsJSON is Step 8's own extension (canonical — see
// [canonicalParamsJSON]): a key reused against the SAME action and target
// but DIFFERENT normalized params is ALSO a conflict, never a replay —
// see [fppCommandReplayParamsConflictProblem]'s own doc comment for why
// answering it as a replay would break idempotency rather than honor it.
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
func (h *handlers) handleFPPCommandReplay(w http.ResponseWriter, r *http.Request, now time.Time, existing store.CommandRecord, instanceID, requestedAction, requestedParamsJSON string) {
	if existing.Action != requestedAction || existing.TargetID != instanceID {
		writeProblem(w, h.logger, now, fppCommandReplayConflictProblem(
			existing.ID, existing.Action, existing.TargetID, requestedAction, instanceID))
		return
	}
	existingParamsJSON := existing.ParamsJSON
	if existingParamsJSON == "" {
		existingParamsJSON = "{}"
	}
	if existingParamsJSON != requestedParamsJSON {
		writeProblem(w, h.logger, now, fppCommandReplayParamsConflictProblem(
			existing.ID, existing.Action, existing.TargetID, existingParamsJSON, requestedParamsJSON))
		return
	}

	ac := authFromContext(r.Context())
	degraded := h.writeBestEffortAudit(r.Context(), now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: existing.Action, Target: instanceID, IdempotencyKey: existing.IdempotencyKey,
		Kind: identity.AuditReplay, CommandID: existing.ID,
	})

	var payload commandResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &payload) // best-effort; "{}" or malformed decodes to the zero value

	var replayParams map[string]any
	_ = json.Unmarshal([]byte(existingParamsJSON), &replayParams) // canonical JSON always decodes; best-effort regardless
	if replayParams == nil {
		replayParams = map[string]any{}
	}

	jsonWrite(w, v1.FPPCommandResponse{
		ServerTime: formatTime(now),
		Command: v1.FPPCommandResult{
			ID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Action: existing.Action, InstanceID: instanceID,
			Params:  replayParams,
			Replay:  true,
			Outcome: payload.Outcome, OutcomeState: existing.OutcomeState, OutcomeReason: existing.OutcomeReason,
			AttributionDegraded: degraded,
			DispatchedAt:        formatTimePtr(existing.DispatchedAt),
			ResolvedAt:          formatTimePtr(existing.ResolvedAt),
		},
	})
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
// see [handlers.handleFPPCommand]'s own comment on why post-dispatch
// bookkeeping is no longer bound by the client's request context).
func (h *handlers) updateCommandOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, dbWriteTimeout)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}

// degradedAttributionReasonSafetyClassExemption and
// degradedAttributionReasonPostDispatch are the two, and ONLY the two,
// justifications [handlers.reportDegradedAttribution] may report for
// proceeding without a durable audit entry. They are deliberately
// different strings: the two call sites that use them are not the same
// decision wearing two names, and conflating them was this task's own
// finding (the doc comment on the pre-fix writeSafetyClassAudit claimed
// EVERY use of it was decision 11's safety class, which stopped being true
// the moment it started being called for a fppSafetyClassNotExempt
// primitive's own post-dispatch outcome).
const (
	// degradedAttributionReasonSafetyClassExemption is
	// [handlers.handleFPPCommand]'s dispatch-side (PRE-dispatch) fallback:
	// primitive.SafetyClass == fppSafetyClassExempt, so ADR-024 decision
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
// [handlers.handleFPPCommand]'s pre-dispatch write applies for a
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
// fallback in [handlers.handleFPPCommand] (Step 7 seam C review defect 8's
// AuditedWrite fallback path, which fails before entry is ever handed to
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
