package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
)

// POST /api/v1/actions/{id}/invocations: invoke one stored show.action by
// id outside a macro run (ADR-037 decision 8). The request carries an
// idempotency key and an optional pinned revision: the stored target
// supplies every parameter (ADR-029) — no protocol address, topic, or
// path is ever accepted here.
// Dispatch reuses dispatchFPPCommand, [Dependencies.ResolumeActions].Dispatch,
// and [DispatchMQTTAction], the same seams macro's own step dispatch
// uses. AuditExempt is read from the stored action's own SafetyClass,
// never re-derived.
//
// A non-exempt audit failure refuses before dispatch; an exempt one
// re-inserts non-transactionally and proceeds with degraded attribution.

// scopeActionInvoke exists only so api.go's route registration can take
// its address — see scopeResolumeAction's identical pattern.
var scopeActionInvoke = identity.ScopeShowActionInvoke

// The outcome vocabulary shared across every integration this endpoint
// dispatches through (ADR-020).
const (
	outcomeWordConfirmed     = "confirmed"
	outcomeWordUnconfirmed   = "unconfirmed"
	outcomeWordUnconfirmable = "unconfirmable"
	outcomeWordRefused       = "refused"
	outcomeWordFailed        = "failed"
)

// Lifecycle states for [v1.ActionInvocationResult.State], mirroring
// store.CommandRecord.State's own two live values for this command
// family.
const (
	actionInvokeStatePending  = "pending"
	actionInvokeStateResolved = "resolved"
)

// Attribution states for [v1.ActionInvocationResult.DispatchAttribution]/
// OutcomeAttribution: named states with their own reasons, replacing a
// single aggregate boolean that could not tell a dispatch-audit loss
// apart from an outcome-audit loss. "pending" applies only to
// OutcomeAttribution, before this invocation has resolved.
const (
	attributionStatePending  = "pending"
	attributionStateComplete = "complete"
	attributionStateDegraded = "degraded"
)

const actionInvokeDispatchCompleteReason = "the pre-dispatch audit entry was written durably before dispatch"
const actionInvokeOutcomeCompleteReason = "the outcome audit entry was written durably"
const actionInvokeOutcomePendingReason = "this invocation has not yet resolved, so no outcome audit entry has been attempted yet"

// actionInvokePendingOutcomeReason is the canned, non-blank OutcomeReason
// a fresh command row carries between insertion and resolution: a pending
// result must state a real reason, never an empty string a racing replay
// could observe.
const actionInvokePendingOutcomeReason = "this invocation has not yet resolved: it is still being dispatched or is awaiting confirmation evidence"

// actionInvokeOutcomeNotPersistedReason covers the case where the outward
// effect ran and its outcome is known to THIS request, but the write
// recording that outcome in the command journal failed. Reporting
// "resolved" here would tell this caller a different story than a
// concurrent replay (which reads the still-pending row) and a future
// restart (which reconciles it independently) would tell — so this
// caller is told the truth the row itself carries: still pending, and
// self-healing via [RunActionInvokeReconciliationLoop] rather than a
// restart.
const actionInvokeOutcomeNotPersistedReason = "this invocation's outward effect has already completed and its outcome is known to this coordinator, but durably recording that outcome failed; it remains pending and this coordinator's own ongoing reconciliation will resolve it without waiting for a restart"

const maxActionInvokeRequestBodyBytes = 1 << 10 // 1 KiB

// actionInvokeTargetKind names this seam's own commands.target_kind.
// target_id is the action's own object id, so two different actions may
// reuse the same idempotency key text without colliding.
const actionInvokeTargetKind = "show.action"

// actionInvokeFPPChildIdempotencyKeyPrefix is the deterministic prefix
// [dispatchActionTarget]'s FPP branch mints its nested command's own
// idempotency key from ("action-invoke:"+cmdID) — see
// [ReconcileStrandedActionInvocations]'s use of it to find that child
// again at startup.
const actionInvokeFPPChildIdempotencyKeyPrefix = "action-invoke:"

// actionInvokeHTTPWriteDeadline covers this endpoint's worst case across
// all three integrations, dominated by mqtt's 120s expect.deadlineSeconds
// cap (config's mqttExpectMaxDeadlineSeconds, duplicated as a literal —
// see TestActionInvokeHTTPWriteDeadlineExceedsMQTTMaxDeadline).
const actionInvokeHTTPWriteDeadline = 150 * time.Second

const actionInvokeBookkeepingBudget = 5 * time.Second

// ProblemTypeActionInvokeRefusedAuditUnavailable mirrors
// [ProblemTypeResolumeActionRefusedAuditUnavailable]'s identical shape:
// a non-exempt action's pre-dispatch audit write failed, the whole
// transaction rolled back, and nothing was recorded or dispatched.
const ProblemTypeActionInvokeRefusedAuditUnavailable = problemBaseURI + "action-invoke-refused-audit-unavailable"

func actionInvokeAuditUnavailableProblem(actionID string, cause error) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeActionInvokeRefusedAuditUnavailable,
		Title:  "Action refused: it could not be durably recorded",
		Status: http.StatusServiceUnavailable,
		Detail: fmt.Sprintf(
			"action %q was refused before anything was dispatched: it must be durably recorded before dispatch, and "+
				"this coordinator's audit store is currently unavailable (%v). Nothing was recorded and nothing was "+
				"dispatched; retry once the audit store is writable again.",
			actionID, cause),
	}
}

// actionInvokeReplayConflictProblem mirrors resolumeActionReplayConflictProblem's
// identical reasoning: an idempotency key reused against a DIFFERENT
// action id, or against a command from a different family entirely, than
// it was first used against. TargetID's grammar is operator-chosen and
// not namespaced per family (fppCommandReplayConflictProblem's own
// precedent), so an FPP instance id and a show.action id can share text
// and must still be told apart.
func actionInvokeReplayConflictProblem(existingID, existingTargetKind, existingTargetID, requestedActionID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different command",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (%s target %q); this request invokes action %q. "+
				"Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingTargetKind, existingTargetID, requestedActionID),
	}
}

// actionInvokeResultPayload is what this handler stores in
// store.CommandRecord.ResultJSON. It carries the attribution axes
// alongside Outcome/Label so a replay or a startup reconciliation reads
// back the SAME attribution this invocation's own original request
// determined, rather than recomputing (and potentially disagreeing with)
// it later.
type actionInvokeResultPayload struct {
	Label   string `json:"label,omitempty"`
	Outcome string `json:"outcome,omitempty"`

	DispatchAttribution       string `json:"dispatchAttribution,omitempty"`
	DispatchAttributionReason string `json:"dispatchAttributionReason,omitempty"`
	OutcomeAttribution        string `json:"outcomeAttribution,omitempty"`
	OutcomeAttributionReason  string `json:"outcomeAttributionReason,omitempty"`
}

// handleInvokeAction serves POST /api/v1/actions/{id}/invocations, behind
// writeGuard(&scopeActionInvoke, ...): by the time this method runs,
// ADR-024 decision 4's scope check and decision 6's CSRF check have both
// already passed. No state change here is reachable by GET.
func (h *handlers) handleInvokeAction(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	id := r.PathValue("id")

	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(actionInvokeHTTPWriteDeadline))

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxActionInvokeRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read action invocation request body", err)
		return
	}
	if len(bodyBytes) > maxActionInvokeRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"the request body exceeds this endpoint's %d byte limit; an idempotency key and an optional revision "+
				"number never legitimately need more",
			maxActionInvokeRequestBodyBytes)))
		return
	}

	var top map[string]json.RawMessage
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &top); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be a JSON object matching {"idempotencyKey":string,"requestedRevision":integer?}`))
			return
		}
	}
	// Unknown-key sweep, matching decodeFPPCommandParams'/decodeResolumeActionParams'
	// own precedent: this is the one endpoint ADR-029 decision 3's raw
	// hatch must never leak through, so a caller trying to smuggle a
	// protocol parameter (e.g. "params":{"topic":"..."}) is told no,
	// never silently ignored.
	var unknownKeys []string
	for k := range top {
		if k != "idempotencyKey" && k != "requestedRevision" {
			unknownKeys = append(unknownKeys, k)
		}
	}
	if len(unknownKeys) > 0 {
		sort.Strings(unknownKeys)
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			`request body contains unrecognized key(s): %s (this endpoint accepts only "idempotencyKey" and `+
				`"requestedRevision" — the action's own stored target supplies every parameter)`, strings.Join(unknownKeys, ", "))))
		return
	}
	var idempotencyKey string
	if idemRaw, hasIdem := top["idempotencyKey"]; hasIdem {
		_ = json.Unmarshal(idemRaw, &idempotencyKey)
	}
	if err := command.ValidateIdempotencyKey(idempotencyKey); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("idempotencyKey: "+err.Error()))
		return
	}
	var requestedRevision *int64
	if revRaw, hasRev := top["requestedRevision"]; hasRev {
		var rr int64
		if err := json.Unmarshal(revRaw, &rr); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem("requestedRevision must be a JSON integer"))
			return
		}
		requestedRevision = &rr
	}

	rev, problem, err := h.resolveActionInvokeRevision(ctx, id, requestedRevision)
	if err != nil {
		h.writeInternalError(w, now, "resolve action revision for invocation", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	payload, err := decodeShowActionPayloadForRead(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode show.action config payload for invocation", err)
		return
	}

	ac := authFromContext(ctx)
	auditAction := "action.invoke:" + payload.Target.Integration

	// --- Idempotency-first: a hit is answered as a replay (or a
	// conflict) WITHOUT ever reaching the audit write or dispatch below —
	// matching handleDispatchResolumeAction's identical ordering. ---

	existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
	switch {
	case lookupErr == nil:
		result, problem := h.resolveActionInvokeReplay(ctx, now, ac, existing, id)
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		jsonWrite(w, v1.ActionInvocationResponse{ServerTime: formatTime(h.now()), Result: result})
		return
	case errors.Is(lookupErr, store.ErrCommandNotFound):
		// Genuinely new key — fall through.
	default:
		h.writeInternalError(w, now, "look up action invocation by idempotency key", lookupErr)
		return
	}

	cmdID := uuid.NewString()
	requestedRevisionStr := strconv.FormatInt(rev.Revision, 10)
	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditAction, Target: id, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditDispatch, CommandID: cmdID, Params: map[string]any{"actionId": id, "label": payload.Label, "revision": rev.Revision},
	}
	rec := store.CommandRecord{
		ID: cmdID, IdempotencyKey: idempotencyKey, Action: auditAction,
		TargetKind: actionInvokeTargetKind, TargetID: id,
		IssuerPrincipalID: ac.result.Principal.ID, IssuerPrincipalName: ac.result.Principal.Name,
		RequestedRevision:  requestedRevisionStr,
		ConfirmationMethod: string(command.ConfirmationEvidence), State: "pending",
		OutcomeReason: actionInvokePendingOutcomeReason,
	}

	// AuditExempt reads straight off the stored action's own safetyClass
	// (ADR-024 decision 11's boundary, never re-derived here): "none" is
	// exempt from nothing, "blackout"/"stop"/"powerOff" are.
	auditExempt := payload.SafetyClass != config.ShowSafetyClassNone

	var dispatchDegraded bool
	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, err := tx.InsertCommand(ctx, rec); err != nil {
			return identity.AuditEntry{}, err
		}
		return dispatchEntry, nil
	})

	var dup *store.DuplicateCommandError
	switch {
	case errors.As(auditErr, &dup):
		result, problem := h.resolveActionInvokeReplay(ctx, now, ac, dup.Existing, id)
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		jsonWrite(w, v1.ActionInvocationResponse{ServerTime: formatTime(h.now()), Result: result})
		return
	case errors.Is(auditErr, identity.ErrAuditWrite):
		if !auditExempt {
			// Fail closed: the transaction above already rolled back in
			// full, so nothing is re-inserted and nothing is dispatched —
			// ADR-024 decision 11's default rule for every action outside
			// the three-member safety class.
			writeProblem(w, h.logger, now, actionInvokeAuditUnavailableProblem(id, auditErr))
			return
		}
		// Safety-class exemption: redo the insert through the plain,
		// non-transactional store method and proceed with degraded
		// attribution — mirroring dispatchFPPCommand/handleDispatchResolumeAction's
		// identical fallback.
		if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
			if errors.As(err, &dup) {
				result, problem := h.resolveActionInvokeReplay(ctx, now, ac, dup.Existing, id)
				if problem != nil {
					writeProblem(w, h.logger, now, *problem)
					return
				}
				jsonWrite(w, v1.ActionInvocationResponse{ServerTime: formatTime(h.now()), Result: result})
				return
			}
			h.writeInternalError(w, now, "insert action invocation command", err)
			return
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedAttributionReasonSafetyClassExemption)
		dispatchDegraded = true
	case auditErr != nil:
		h.writeInternalError(w, now, "insert action invocation command", auditErr)
		return
	}

	dispatchAttribution, dispatchAttributionReason := attributionStateComplete, actionInvokeDispatchCompleteReason
	if dispatchDegraded {
		dispatchAttribution, dispatchAttributionReason = attributionStateDegraded, degradedAttributionReasonSafetyClassExemption
	}

	// --- From here on, a detached context: an abandoned client must not
	// abort an in-flight dispatch or its bookkeeping, matching
	// dispatchFPPCommand/handleDispatchResolumeAction's identical bgCtx
	// cutover. ---
	bgCtx := context.WithoutCancel(ctx)

	// Persist the attribution axes known so far immediately, before
	// dispatch even starts: a replay racing this still-in-flight request
	// must read the SAME dispatch attribution this request just
	// determined, not a default zero value.
	interimResult, _ := json.Marshal(actionInvokeResultPayload{
		Label:               payload.Label,
		DispatchAttribution: dispatchAttribution, DispatchAttributionReason: dispatchAttributionReason,
		OutcomeAttribution: attributionStatePending, OutcomeAttributionReason: actionInvokeOutcomePendingReason,
	})
	interimResultStr := string(interimResult)
	if err := h.updateActionInvokeOutcomeBounded(bgCtx, cmdID, store.CommandOutcomeUpdate{ResultJSON: &interimResultStr}); err != nil {
		h.logWarn("failed to record action invocation dispatch attribution", "commandId", cmdID, "error", err)
	}

	dispatchCtx, dispatchCancel := context.WithTimeout(bgCtx, actionInvokeHTTPWriteDeadline-actionInvokeBookkeepingBudget)
	defer dispatchCancel()

	outcome, outcomeState, outcomeReason, dispatchedAt, resolvedAt := h.dispatchActionTarget(dispatchCtx, payload, cmdID, requestedRevisionStr, ac, h.clientAddr(r), auditExempt)

	// evidenceState is currently unused on the wire directly but kept for
	// parity with the outcome-audit entry below, which does carry it.
	evidenceState := outcomeState
	if evidenceState == "" && outcome == outcomeWordConfirmed {
		evidenceState = "current"
	}

	outcomeDegraded := h.writeActionInvokeAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditAction, Target: id, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditOutcome, CommandID: cmdID, Params: map[string]any{"actionId": id, "label": payload.Label, "revision": rev.Revision},
		Outcome: outcome, OutcomeState: evidenceState, OutcomeReason: outcomeReason,
	})
	outcomeAttribution, outcomeAttributionReason := attributionStateComplete, actionInvokeOutcomeCompleteReason
	if outcomeDegraded {
		outcomeAttribution, outcomeAttributionReason = attributionStateDegraded, degradedAttributionReasonPostDispatch
	}

	resolvedState := "resolved"
	finalResult, _ := json.Marshal(actionInvokeResultPayload{
		Label: payload.Label, Outcome: outcome,
		DispatchAttribution: dispatchAttribution, DispatchAttributionReason: dispatchAttributionReason,
		OutcomeAttribution: outcomeAttribution, OutcomeAttributionReason: outcomeAttributionReason,
	})
	finalResultStr := string(finalResult)
	persistErr := h.updateActionInvokeOutcomeBounded(bgCtx, cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: dispatchedAt, ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &evidenceState, OutcomeReason: &outcomeReason,
	})
	if persistErr != nil {
		h.logWarn("failed to record action invocation outcome", "commandId", cmdID, "error", persistErr)
	}

	// If the row itself never became durably "resolved", this response
	// must not claim it is — a concurrent replay reads the
	// still-"pending" row, and a later restart's reconciliation resolves
	// it independently; telling THIS caller "resolved" would be a third,
	// different story about the same dispatch.
	if persistErr != nil {
		jsonWrite(w, v1.ActionInvocationResponse{
			ServerTime: formatTime(h.now()),
			Result: v1.ActionInvocationResult{
				ID: cmdID, IdempotencyKey: idempotencyKey, ActionID: id, Revision: rev.Revision, Label: payload.Label,
				Replay: false, State: actionInvokeStatePending, Outcome: nil, OutcomeReason: actionInvokeOutcomeNotPersistedReason,
				DispatchAttribution: dispatchAttribution, DispatchAttributionReason: dispatchAttributionReason,
				OutcomeAttribution: attributionStateDegraded, OutcomeAttributionReason: actionInvokeOutcomeNotPersistedReason,
				AttributionDegraded: true,
				DispatchedAt:        formatTimePtr(dispatchedAt),
				ResolvedAt:          nil,
			},
		})
		return
	}

	outcomeCopy := outcome
	jsonWrite(w, v1.ActionInvocationResponse{
		ServerTime: formatTime(h.now()),
		Result: v1.ActionInvocationResult{
			ID: cmdID, IdempotencyKey: idempotencyKey, ActionID: id, Revision: rev.Revision, Label: payload.Label,
			Replay: false, State: actionInvokeStateResolved, Outcome: &outcomeCopy, OutcomeReason: outcomeReason,
			DispatchAttribution: dispatchAttribution, DispatchAttributionReason: dispatchAttributionReason,
			OutcomeAttribution: outcomeAttribution, OutcomeAttributionReason: outcomeAttributionReason,
			AttributionDegraded: dispatchDegraded || outcomeDegraded,
			DispatchedAt:        formatTimePtr(dispatchedAt),
			ResolvedAt:          formatTimePtr(&resolvedAt),
		},
	})
}

// resolveActionInvokeRevision loads either the show.action's currently
// active revision (requestedRevision == nil, an interactive "run
// whatever is current" call) or one EXACT pinned revision
// (requestedRevision != nil, a durable/queued caller naming the exact
// revision it queued against). Either way, the returned revision is what
// [v1.ActionInvocationResult.Revision] reports as having actually
// executed.
func (h *handlers) resolveActionInvokeRevision(ctx context.Context, id string, requestedRevision *int64) (store.ConfigRevisionRecord, *v1.Problem, error) {
	if requestedRevision == nil {
		rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowActionConfigKind, id)
		return rev, problem, err
	}
	if *requestedRevision <= 0 {
		p := invalidParameterProblem(fmt.Sprintf("requestedRevision must be a positive revision number, got %d", *requestedRevision))
		return store.ConfigRevisionRecord{}, &p, nil
	}
	if _, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, id); err != nil {
		if errors.Is(err, store.ErrConfigObjectNotFound) {
			p := showConfigObjectNotFoundProblem(config.ShowActionConfigKind, id)
			return store.ConfigRevisionRecord{}, &p, nil
		}
		return store.ConfigRevisionRecord{}, nil, err
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowActionConfigKind, id, *requestedRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		p := resourceNotFoundProblem(fmt.Sprintf("show.action %q has no revision %d", id, *requestedRevision))
		return store.ConfigRevisionRecord{}, &p, nil
	}
	if err != nil {
		return store.ConfigRevisionRecord{}, nil, err
	}
	return rev, nil, nil
}

// dispatchActionTarget routes payload.Target to the same in-process
// dispatch seam its own integration already uses. auditExempt is
// threaded into the FPP branch's own NeverWithholdOnAuditFailure so
// dispatchFPPCommand's independent internal audit check agrees with this
// handler's own outer decision. requestedRevision is threaded into the
// FPP branch's own child command row, so the nested dispatch
// carries the SAME pinned revision the outer invocation resolved against.
//
// outcomeState is empty for FPP and Resolume (the caller derives a
// pkg/observation-vocabulary fallback from outcome itself, unchanged) and
// carries DispatchMQTTAction's own mqttActionState* vocabulary for MQTT —
// macro/vocab.go's identical split, applied here instead of re-deriving a
// second classification for the same dispatch.
func (h *handlers) dispatchActionTarget(ctx context.Context, payload config.ShowActionPayload, cmdID, requestedRevision string, ac authContext, clientAddr string, auditExempt bool) (outcome, outcomeState, outcomeReason string, dispatchedAt *time.Time, resolvedAt time.Time) {
	target := payload.Target
	switch target.Integration {
	case config.ShowActionIntegrationFPP:
		in := FPPCommandInput{
			InstanceID: target.InstanceID, Action: target.Primitive, Params: target.Params,
			IdempotencyKey:    actionInvokeFPPChildIdempotencyKeyPrefix + cmdID,
			RequestedRevision: requestedRevision,
			Issuer: FPPCommandIssuer{
				PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
				Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: clientAddr,
			},
			NeverWithholdOnAuditFailure: auditExempt,
		}
		out, problem, err := h.dispatchFPPCommand(ctx, h.now(), in)
		resolvedAt = h.now()
		switch {
		case err != nil:
			return outcomeWordFailed, "", "this action could not be dispatched because of an internal coordinator error", nil, resolvedAt
		case problem != nil:
			return outcomeWordRefused, "", problem.Detail, nil, resolvedAt
		case out.DispatchFailed:
			return outcomeWordFailed, "", fmt.Sprintf("the request to %s did not succeed, so this action never reached it", in.InstanceID), out.DispatchedAt, resolvedAt
		case out.Outcome == outcomeWordConfirmed:
			return outcomeWordConfirmed, "", out.OutcomeReason, out.DispatchedAt, resolvedAt
		default:
			return outcomeWordUnconfirmed, "", out.OutcomeReason, out.DispatchedAt, resolvedAt
		}

	case config.ShowActionIntegrationResolume:
		dispatchedNow := h.now()
		result, err := h.deps.ResolumeActions.Dispatch(ctx, target.Action, target.Ref, dispatchedNow)
		resolvedAt = h.now()
		if err != nil {
			return outcomeWordFailed, "", "this action could not be dispatched because of an internal coordinator error", nil, resolvedAt
		}
		return mapResolumeOutcomeWord(result.Outcome), "", result.Reason, result.DispatchedAt, resolvedAt

	case config.ShowActionIntegrationMQTT:
		// Stamped BEFORE DispatchMQTTAction runs, mirroring
		// macro/step_mqtt.go's identical ordering: dispatchedAt is the
		// anchor ADR-003's "evidence post-dates dispatch" is measured
		// against, so it must be the instant the publish was attempted,
		// never the instant the wait resolved — an mqtt action can wait
		// up to expect.deadlineSeconds (120s max) between the two.
		dispatchAttemptedAt := h.now()
		res := DispatchMQTTAction(ctx, h.deps.MQTTBrokers, target, h.now)
		var dispatched *time.Time
		if res.PublishAttempted {
			dispatched = &dispatchAttemptedAt
		}
		return res.Outcome, res.OutcomeState, res.OutcomeReason, dispatched, res.ResolvedAt

	default:
		// Unreachable given write-time validation of target.integration's
		// closed enum.
		return outcomeWordFailed, "", fmt.Sprintf("action names an unrecognized integration %q", target.Integration), nil, h.now()
	}
}

// mapResolumeOutcomeWord converts [ResolumeActionOutcome] to this
// endpoint's own plain-string outcome vocabulary — the same five words.
func mapResolumeOutcomeWord(o ResolumeActionOutcome) string {
	switch o {
	case ResolumeOutcomeConfirmed:
		return outcomeWordConfirmed
	case ResolumeOutcomeUnconfirmed:
		return outcomeWordUnconfirmed
	case ResolumeOutcomeUnconfirmable:
		return outcomeWordUnconfirmable
	case ResolumeOutcomeRefused:
		return outcomeWordRefused
	default:
		return outcomeWordFailed
	}
}

// resolveActionInvokeReplay answers a replayed idempotency key: nothing
// is dispatched, and existing's own already-recorded result is returned
// verbatim — mirroring resolveResolumeActionReplay's identical reasoning.
// existing's own stored payload carries State/Outcome/attribution exactly
// as its original request (or a startup reconciliation) left them; this
// never recomputes them.
func (h *handlers) resolveActionInvokeReplay(ctx context.Context, now time.Time, ac authContext, existing store.CommandRecord, requestedActionID string) (v1.ActionInvocationResult, *v1.Problem) {
	if existing.TargetKind != actionInvokeTargetKind || existing.TargetID != requestedActionID {
		p := actionInvokeReplayConflictProblem(existing.ID, existing.TargetKind, existing.TargetID, requestedActionID)
		return v1.ActionInvocationResult{}, &p
	}

	degraded := h.writeBestEffortAudit(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID,
		Action: existing.Action, Target: existing.TargetID, IdempotencyKey: existing.IdempotencyKey,
		Kind: identity.AuditReplay, CommandID: existing.ID,
	})

	var payload actionInvokeResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &payload)

	revision, _ := strconv.ParseInt(existing.RequestedRevision, 10, 64)

	state := actionInvokeStatePending
	var outcomePtr *string
	if existing.State == "resolved" {
		state = actionInvokeStateResolved
		outcomeCopy := payload.Outcome
		outcomePtr = &outcomeCopy
	}
	outcomeReason := existing.OutcomeReason
	if outcomeReason == "" {
		outcomeReason = actionInvokePendingOutcomeReason
	}

	dispatchAttribution, dispatchAttributionReason := payload.DispatchAttribution, payload.DispatchAttributionReason
	if dispatchAttribution == "" {
		dispatchAttribution, dispatchAttributionReason = attributionStateComplete, actionInvokeDispatchCompleteReason
	}
	outcomeAttribution, outcomeAttributionReason := payload.OutcomeAttribution, payload.OutcomeAttributionReason
	if outcomeAttribution == "" {
		if state == actionInvokeStateResolved {
			outcomeAttribution, outcomeAttributionReason = attributionStateComplete, actionInvokeOutcomeCompleteReason
		} else {
			outcomeAttribution, outcomeAttributionReason = attributionStatePending, actionInvokeOutcomePendingReason
		}
	}
	if degraded {
		outcomeAttribution, outcomeAttributionReason = attributionStateDegraded, degradedAttributionReasonPostDispatch
	}

	return v1.ActionInvocationResult{
		ID: existing.ID, IdempotencyKey: existing.IdempotencyKey, ActionID: existing.TargetID, Revision: revision, Label: payload.Label,
		Replay: true, State: state, Outcome: outcomePtr, OutcomeReason: outcomeReason,
		DispatchAttribution: dispatchAttribution, DispatchAttributionReason: dispatchAttributionReason,
		OutcomeAttribution: outcomeAttribution, OutcomeAttributionReason: outcomeAttributionReason,
		AttributionDegraded: dispatchAttribution == attributionStateDegraded || outcomeAttribution == attributionStateDegraded,
		DispatchedAt:        formatTimePtr(existing.DispatchedAt),
		ResolvedAt:          formatTimePtr(existing.ResolvedAt),
	}, nil
}

// updateActionInvokeOutcomeBounded mirrors updateResolumeActionOutcomeBounded's
// identical reasoning one file over.
func (h *handlers) updateActionInvokeOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, actionInvokeBookkeepingBudget)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}

// writeActionInvokeAuditBounded mirrors writeResolumeActionAuditBounded's
// identical reasoning one file over.
func (h *handlers) writeActionInvokeAuditBounded(parent context.Context, now time.Time, reason string, entry identity.AuditEntry) (degraded bool) {
	ctx, cancel := context.WithTimeout(parent, actionInvokeBookkeepingBudget)
	defer cancel()
	return h.writeBestEffortAudit(ctx, now, reason, entry)
}
