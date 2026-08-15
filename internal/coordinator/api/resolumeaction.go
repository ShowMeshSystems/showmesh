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
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/command"
)

// This file is Track D seam D-3/B: the HTTP surface over TRACK-D-D3-SPEC.md
// section 2's seven-action Resolume vocabulary. It is deliberately shaped
// like fppcommand_handler.go/fppcommand_dispatch.go's own dispatch core —
// idempotency-first replay handling, the same atomic dispatch-audit-write
// with ADR-024 decision 11's safety-class fallback, the same best-effort
// post-dispatch outcome audit entry — because the underlying property
// (authenticate, authorize by scope, record durably before acting, never
// claim success on a bare 200) is identical for a second vendor's command
// surface, and a differently-shaped contract for the same property would
// be an inconsistency with no reason behind it.
//
// Unlike fppcommand_handler.go, this file owns none of the ACTUAL dispatch
// logic — the derived per-action deadline, the pre-dispatch baseline, the
// deck refusal, composition-identity gating, and confirmation are all
// [ResolumeActionDispatcher.Dispatch]'s job (resolumeaction_interfaces.go),
// implemented by Track D seam D-3/A
// (internal/coordinator/collector/resolume), built concurrently and
// reached only through that interface. This file's own job ends at:
// decode the wire request, resolve the requested action against
// [Dependencies.ResolumeActions.Actions], validate its params, authorize
// and record the dispatch attempt, call Dispatch, and render the outcome
// honestly.

// scopeResolumeAction exists only so api.go's route registration can take
// its address — see scopeFPPCommand's identical doc comment
// (fppcommand_handler.go) for why: [handlers.writeGuard] takes
// *identity.Scope, and a Go constant's address cannot be taken directly.
var scopeResolumeAction = identity.ScopeResolumeAction

// resolumeActionTargetKind and resolumeActionTargetID are this seam's own
// fixed (commands.target_kind, commands.target_id) pair — a FIXED
// constant, never derived from [Dependencies.ResolumeID], for the exact
// reason resolumeCompositionObjectIDConst is fixed rather than plumbed
// through cfg.ResolumeID (resolumecomposition.go's own doc comment):
// SHOWMESH_RESOLUME_ID is an operator-settable identifier for the live
// REST/WebSocket collector, unrelated to which coordinator subsystem is
// dispatching an action, and renaming it must never orphan this seam's own
// idempotency-key history the way it would orphan a stored composition
// under the same mistake. There is exactly one Resolume adapter per
// coordinator today (TRACK-D-D3-SPEC.md carries no per-instance concept
// anywhere in its vocabulary), so a fixed target needs no path parameter
// to name it — unlike POST /fpp/{instanceId}/commands, which addresses one
// of potentially several configured FPP hosts.
const (
	resolumeActionTargetKind = "resolume"
	resolumeActionTargetID   = "resolume"
)

// maxResolumeActionRequestBodyBytes bounds this endpoint's request body,
// mirroring maxFPPCommandRequestBodyBytes' identical reasoning
// (fppcommand_handler.go): a request this small (an action name, an
// idempotency key, at most two short parameters) has no legitimate reason
// to be large.
const maxResolumeActionRequestBodyBytes = 4 << 10 // 4 KiB

// resolumeActionDBWriteTimeout mirrors dbWriteTimeout's identical
// reasoning (fppcommand_handler.go): each individual piece of post-dispatch
// bookkeeping gets its own short, independent deadline once it is no
// longer tied to the client's own request context.
const resolumeActionDBWriteTimeout = 10 * time.Second

// resolumeActionMaxConfirmDeadline bounds this endpoint's own HTTP write
// deadline (set before the request body is even read, so before this
// coordinator knows which action — and therefore which of D-3/A's own
// derived deadlines — is about to run).
//
// This is no longer an independently asserted guess: it MUST equal
// resolume.MaxActionConfirmDeadline (internal/coordinator/collector/resolume,
// action.go), the real, structurally-enforced upper bound D-3/A's own
// deadline model clamps every derived deadline to — see that constant's own
// doc comment for why a clamp, not a larger constant, is the correct answer
// to TRACK-D-D3-SPEC.md section 3.3's deadlines being DERIVED per action
// from a layer's own live, operator-configured transition duration, an
// input with no registry-known bound the way every FPP primitive's own
// ConfirmDeadline function has ([fppMaxConfirmDeadline] reads that function
// directly because it can; that shape does not exist for a value read off
// live Resolume state).
//
// This package deliberately does not import
// internal/coordinator/collector/resolume in production code (the same
// decoupling resolumeaction_interfaces.go's own doc comment states for the
// dispatcher interface itself, and the same "duplicate the literal, name
// the coupling" judgment apiwiring.go's own fppMQTTCollectorSourceID etc.
// already make throughout this codebase) — so this constant stays a
// literal, but it is no longer a coincidence: TestResolumeActionMaxConfirmDeadlineEqualsRegistryMax
// (resolumeaction_test.go, a test file, which CAN import the producer
// package without creating a production dependency) fails the build the
// moment this value and resolume.MaxActionConfirmDeadline disagree.
//
// cmd/showmeshctl's own minResolumeActionClientTimeout
// (cmd_resolume_action.go) is reconciled against THIS value the same way —
// see TestResolumeActionMaxConfirmDeadlineFitsWithinCLIClientBudget below
// for why that boundary stays two independently chosen literals rather than
// one shared constant (that program does not import this package, and its
// own importgraph_test.go forbids the reverse).
const resolumeActionMaxConfirmDeadline = 30 * time.Second

// ProblemTypeResolumeActionRefusedAuditUnavailable is this seam's own
// ADR-024 decision 11 fail-closed refusal — the Resolume-action sibling of
// [ProblemTypeFPPCommandRefusedAuditUnavailable]: a non-exempt action's
// pre-dispatch audit write failed, the whole transaction rolled back, and
// nothing was recorded or dispatched.
const ProblemTypeResolumeActionRefusedAuditUnavailable = problemBaseURI + "resolume-action-refused-audit-unavailable"

// resolumeActionAuditUnavailableProblem is
// [ProblemTypeResolumeActionRefusedAuditUnavailable]'s own constructor,
// mirroring fppCommandAuditUnavailableProblem's identical reasoning
// (problem.go) including its 503 status: this names a specific, transient
// dependency condition (the audit store could not be appended to right
// now), not an unspecified internal defect.
func resolumeActionAuditUnavailableProblem(action string, cause error) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResolumeActionRefusedAuditUnavailable,
		Title:  "Action refused: it could not be durably recorded",
		Status: http.StatusServiceUnavailable,
		Detail: fmt.Sprintf(
			"%q was refused before anything was sent to Resolume: it must be durably recorded before dispatch, and "+
				"this coordinator's audit store is currently unavailable (%v). Nothing was recorded and nothing was "+
				"dispatched; retry once the audit store is writable again.",
			action, cause),
	}
}

// resolumeActionReplayConflictProblem mirrors
// fppCommandReplayConflictProblem's identical reasoning (problem.go), for
// an idempotency key reused against a DIFFERENT action than it was first
// used against. There is no target dimension to also check — see
// [resolumeActionTargetID]'s own doc comment for why every request in this
// seam names the same fixed target.
func resolumeActionReplayConflictProblem(existingID, existingAction, requestedAction string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used for a different action",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q); this request names a different action %q. "+
				"Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, existingAction, requestedAction),
	}
}

// resolumeActionReplayParamsConflictProblem mirrors
// fppCommandReplayParamsConflictProblem's identical reasoning (problem.go):
// the SAME idempotency key and the SAME action, but DIFFERENT normalized
// params, is also a conflict, never a replay.
func resolumeActionReplayParamsConflictProblem(existingID, action, existingParamsJSON, requestedParamsJSON string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "Idempotency key already used with different parameters",
		Status: http.StatusConflict,
		Detail: fmt.Sprintf(
			"idempotencyKey was already used for command %s (action %q) with params %s; this request has the SAME "+
				"action but DIFFERENT params: %s. Mint a fresh idempotencyKey for a genuinely new request.",
			existingID, action, existingParamsJSON, requestedParamsJSON),
	}
}

// resolumeActionResultPayload is what this handler stores in
// store.CommandRecord.ResultJSON — the Resolume-action sibling of
// commandResultPayload (fppcommand_handler.go), narrowed to what this seam
// actually needs on replay: the outcome word itself (Resolume issues no
// HTTP status/body worth echoing back the way an FPP dispatch's raw
// response does, since [ResolumeActionDispatcher.Dispatch] never surfaces
// one to this package — see that method's own doc comment).
type resolumeActionResultPayload struct {
	Outcome string `json:"outcome,omitempty"`
}

// noResolumeActionDispatcher is [Dependencies.ResolumeActions]'s nil-safe
// default. Actions reports an empty vocabulary (so GET /resolume/actions
// renders honestly as "nothing configured" rather than a 500) and Dispatch
// refuses loudly — matching [noCommandStore]'s identical "an unwired WRITE
// dependency refuses loudly, never fabricates success" posture
// (api.go) — though in practice Dispatch is unreachable through the normal
// request path once Actions is empty: every action name is unsupported
// before this method could ever be called.
type noResolumeActionDispatcher struct{}

var errResolumeActionsNotConfigured = errors.New("api: no ResolumeActionDispatcher was wired into this API's Dependencies")

func (noResolumeActionDispatcher) Actions() []ResolumeActionDescriptor { return nil }

func (noResolumeActionDispatcher) Dispatch(context.Context, string, map[string]any, time.Time) (ResolumeActionResult, error) {
	return ResolumeActionResult{}, errResolumeActionsNotConfigured
}

// resolumeActionAuditNames is TRACK-D-D3-SPEC.md section 2's seven wire
// action names, mapped to this coordinator's own internal, namespaced
// audit-action identifier — matching this codebase's standing convention
// (fpp.stop_playlist, fpp.start_playlist, ...). Fixed by the spec table
// itself, not invented independently: a future action D-3/A's registry
// adds that is not yet in this map falls through to the generic
// resolumeActionAuditAction fallback below rather than failing to build a
// name at all.
var resolumeActionAuditNames = map[string]string{
	"launchClip":     "resolume.launch_clip",
	"clearLayer":     "resolume.clear_layer",
	"blackout":       "resolume.blackout",
	"launchColumn":   "resolume.launch_column",
	"selectDeck":     "resolume.select_deck",
	"setLayerBypass": "resolume.set_layer_bypass",
	"setLayerMaster": "resolume.set_layer_master",
}

// resolumeActionAuditAction returns action's internal audit-action name.
func resolumeActionAuditAction(action string) string {
	if name, ok := resolumeActionAuditNames[action]; ok {
		return name
	}
	return "resolume." + action
}

// findResolumeActionDescriptor returns the descriptor named action from
// descriptors, or ok=false if none matches.
func findResolumeActionDescriptor(descriptors []ResolumeActionDescriptor, action string) (ResolumeActionDescriptor, bool) {
	for _, d := range descriptors {
		if d.Name == action {
			return d, true
		}
	}
	return ResolumeActionDescriptor{}, false
}

// quotedResolumeActionNames formats descriptors' own names for an
// "unsupported action" problem detail, sorted and quoted — mirroring
// fppQuotedWireActions' identical shape (fppcommand_handler.go).
func quotedResolumeActionNames(descriptors []ResolumeActionDescriptor) string {
	names := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(quoted) == 0 {
		return "(none — no ResolumeActionDispatcher is configured on this coordinator)"
	}
	return strings.Join(quoted, ", ")
}

// decodeResolumeActionParams implements the identical absent/null/empty
// rule decodeFPPCommandParams (fppcommand_primitives.go) enforces for FPP,
// narrowed to this vocabulary's own property: every declared parameter is
// REQUIRED (see [ResolumeActionParam]'s own doc comment), so there is no
// Default branch to apply — an absent or null required parameter is
// always a 400.
func decodeResolumeActionParams(desc ResolumeActionDescriptor, top map[string]json.RawMessage) (map[string]any, *v1.Problem) {
	rawParams, hasParams := top["params"]
	if hasParams && isJSONNull(rawParams) {
		p := invalidParameterProblem(fmt.Sprintf(
			"params must not be null for action %q; omit the field entirely (or send {}) — an explicit null is not "+
				"the same as an omitted field", desc.Name))
		return nil, &p
	}

	var fields map[string]json.RawMessage
	if hasParams {
		if err := json.Unmarshal(rawParams, &fields); err != nil {
			p := invalidParameterProblem("params must be a JSON object: " + err.Error())
			return nil, &p
		}
	}

	if len(desc.Params) == 0 {
		if len(fields) > 0 {
			keys := make([]string, 0, len(fields))
			for k := range fields {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			p := invalidParameterProblem(fmt.Sprintf(
				"action %q takes no parameters, but params named: %s", desc.Name, strings.Join(keys, ", ")))
			return nil, &p
		}
		return map[string]any{}, nil
	}

	known := make(map[string]bool, len(desc.Params))
	for _, def := range desc.Params {
		known[def.Name] = true
	}

	// Unknown-key sweep first — see decodeFPPCommandParams' own doc
	// comment (Step 8 review finding 14) for why this must run before the
	// per-parameter loop: a misspelled required parameter must be reported
	// as an unrecognized key, not as the correctly-spelled one being
	// absent.
	var unknown []string
	for k := range fields {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		p := invalidParameterProblem(fmt.Sprintf(
			"params contains unrecognized key(s) for action %q: %s (a typo'd parameter name is refused rather than "+
				"silently ignored)", desc.Name, strings.Join(unknown, ", ")))
		return nil, &p
	}

	normalized := make(map[string]any, len(desc.Params))
	for _, def := range desc.Params {
		raw, present := fields[def.Name]
		switch {
		case !present:
			p := invalidParameterProblem(fmt.Sprintf("params.%s is required and was not provided", def.Name))
			return nil, &p
		case isJSONNull(raw):
			p := invalidParameterProblem(fmt.Sprintf(
				"params.%s is required and must not be null (an explicit null is not the same as an omitted field)", def.Name))
			return nil, &p
		default:
			val, err := decodeResolumeActionParamValue(def, raw)
			if err != nil {
				p := invalidParameterProblem(fmt.Sprintf("params.%s: %v", def.Name, err))
				return nil, &p
			}
			normalized[def.Name] = val
		}
	}
	return normalized, nil
}

// decodeResolumeActionParamValue decodes one present, non-null raw JSON
// value against def's own kind — the Resolume-action sibling of
// decodeFPPParamValue (fppcommand_primitives.go), narrowed to this
// vocabulary's two kinds.
func decodeResolumeActionParamValue(def ResolumeActionParam, raw json.RawMessage) (any, error) {
	switch def.Kind {
	case ResolumeActionParamString:
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("must be a string")
		}
		if s == "" {
			return nil, fmt.Errorf("must not be an empty string (an explicit empty string is not the same as omitted or null)")
		}
		return s, nil
	case ResolumeActionParamBool:
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("must be a boolean")
		}
		return b, nil
	case ResolumeActionParamNumber:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("must be a number")
		}
		return f, nil
	default:
		return nil, fmt.Errorf("unsupported parameter kind")
	}
}

// handleListResolumeActions serves GET /api/v1/resolume/actions: the
// fixed vocabulary, with each action's own parameters, audit-exempt class,
// and coordinator-required flag — never gated behind any scope, since this
// is static capability metadata (identical for every coordinator running
// this software version), not a resource an operator's own credential
// controls visibility of.
func (h *handlers) handleListResolumeActions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	descriptors := h.deps.ResolumeActions.Actions()

	actions := make([]v1.ResolumeAction, 0, len(descriptors))
	for _, d := range descriptors {
		params := make([]v1.ResolumeActionParam, 0, len(d.Params))
		for _, p := range d.Params {
			params = append(params, v1.ResolumeActionParam{Name: p.Name, Kind: string(p.Kind), Required: p.Required})
		}
		actions = append(actions, v1.ResolumeAction{
			Name: d.Name, Params: params, AuditExempt: d.AuditExempt, CoordinatorRequired: d.CoordinatorRequired,
		})
	}
	jsonWrite(w, v1.ResolumeActionsResponse{ServerTime: formatTime(now), Actions: actions})
}

// handleDispatchResolumeAction serves POST /api/v1/resolume/actions,
// behind writeGuard(&scopeResolumeAction, ...) — so by the time this
// method runs, ADR-024 decision 4's scope check and decision 6's CSRF
// check have both already passed, and [authFromContext] is guaranteed to
// report ac.ok == true with a principal holding resolume:action.
//
// No state change here is reachable by GET (ADR-024): this is the ONLY
// handler in this file that can dispatch anything, and it is registered
// on POST alone.
func (h *handlers) handleDispatchResolumeAction(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	// Set before the request body is even read, matching
	// handleFPPCommand's identical reasoning (fppcommand_handler.go) for
	// the identical hazard: this handler can legitimately hold the
	// connection open past net/http.Server's own WriteTimeout while
	// [ResolumeActionDispatcher.Dispatch] runs out its own confirmation
	// wait.
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(resolumeActionMaxConfirmDeadline + 30*time.Second))

	var top map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxResolumeActionRequestBodyBytes+1))
	if err := dec.Decode(&top); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			`request body must be a JSON object matching {"action":string,"idempotencyKey":string,"params":object?}`))
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

	descriptors := h.deps.ResolumeActions.Actions()
	desc, ok := findResolumeActionDescriptor(descriptors, action)
	if !ok {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf(
			"unsupported action %q; this coordinator supports: %s", action, quotedResolumeActionNames(descriptors))))
		return
	}

	normalizedParams, paramProblem := decodeResolumeActionParams(desc, top)
	if paramProblem != nil {
		writeProblem(w, h.logger, now, *paramProblem)
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

	paramsJSON, err := canonicalParamsJSON(normalizedParams)
	if err != nil {
		h.writeInternalError(w, now, "canonicalize resolume action params", err)
		return
	}

	ac := authFromContext(ctx)
	auditAction := resolumeActionAuditAction(action)

	// --- Idempotency-first: a hit is answered as a replay (or a conflict)
	// WITHOUT ever reaching the audit write or Dispatch below — matching
	// dispatchFPPCommand's identical ordering (fppcommand_dispatch.go). ---

	existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, idempotencyKey)
	switch {
	case lookupErr == nil:
		result, problem := h.resolveResolumeActionReplay(ctx, now, ac, existing, action, auditAction, paramsJSON)
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		jsonWrite(w, v1.ResolumeActionResponse{ServerTime: formatTime(h.now()), Result: result})
		return
	case errors.Is(lookupErr, store.ErrCommandNotFound):
		// Genuinely new key — fall through.
	default:
		h.writeInternalError(w, now, "look up resolume action by idempotency key", lookupErr)
		return
	}

	cmdID := uuid.NewString()
	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditAction, Target: resolumeActionTargetID, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditDispatch, CommandID: cmdID,
	}
	rec := store.CommandRecord{
		ID: cmdID, IdempotencyKey: idempotencyKey, Action: auditAction,
		TargetKind: resolumeActionTargetKind, TargetID: resolumeActionTargetID, ParamsJSON: paramsJSON,
		IssuerPrincipalID: ac.result.Principal.ID, IssuerPrincipalName: ac.result.Principal.Name,
		ConfirmationMethod: string(command.ConfirmationEvidence), State: "pending",
	}

	// --- Insert, dispatch audit entry — atomically when the audit store
	// is healthy; ADR-024 decision 11's safety-class fallback otherwise. ---

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
		result, problem := h.resolveResolumeActionReplay(ctx, now, ac, dup.Existing, action, auditAction, paramsJSON)
		if problem != nil {
			writeProblem(w, h.logger, now, *problem)
			return
		}
		jsonWrite(w, v1.ResolumeActionResponse{ServerTime: formatTime(h.now()), Result: result})
		return
	case errors.Is(auditErr, identity.ErrAuditWrite):
		if !desc.AuditExempt {
			// Fail closed: the transaction above already rolled back in
			// full, so nothing is re-inserted and nothing is dispatched —
			// TRACK-D-D3-SPEC.md section 5.2's default rule for every
			// action except blackout and clearLayer.
			p := resolumeActionAuditUnavailableProblem(action, auditErr)
			writeProblem(w, h.logger, now, p)
			return
		}
		// Safety-class exemption: redo the insert through the plain,
		// non-transactional store method and proceed with degraded
		// attribution — mirroring dispatchFPPCommand's identical fallback
		// (fppcommand_dispatch.go).
		if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
			if errors.As(err, &dup) {
				result, problem := h.resolveResolumeActionReplay(ctx, now, ac, dup.Existing, action, auditAction, paramsJSON)
				if problem != nil {
					writeProblem(w, h.logger, now, *problem)
					return
				}
				jsonWrite(w, v1.ResolumeActionResponse{ServerTime: formatTime(h.now()), Result: result})
				return
			}
			h.writeInternalError(w, now, "insert resolume action command", err)
			return
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedAttributionReasonSafetyClassExemption)
		dispatchDegraded = true
	case auditErr != nil:
		h.writeInternalError(w, now, "insert resolume action command", auditErr)
		return
	}

	// --- Dispatch — outside any transaction and on a detached context, so
	// an abandoned client cannot abort an in-flight dispatch or its
	// bookkeeping (matching dispatchFPPCommand's identical bgCtx cutover,
	// fppcommand_dispatch.go). ---

	bgCtx := context.WithoutCancel(ctx)
	result, dispatchErr := h.deps.ResolumeActions.Dispatch(bgCtx, action, normalizedParams, now)
	if dispatchErr != nil {
		h.writeInternalError(w, now, "dispatch resolume action", dispatchErr)
		return
	}

	resolvedAt := h.now()
	resolvedState := "resolved"
	outcomeStr := string(result.Outcome)
	finalResult, _ := json.Marshal(resolumeActionResultPayload{Outcome: outcomeStr})
	finalResultStr := string(finalResult)
	if err := h.updateResolumeActionOutcomeBounded(bgCtx, cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: result.DispatchedAt, ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &outcomeStr, OutcomeReason: &result.Reason,
	}); err != nil {
		h.logWarn("failed to record resolume action outcome", "commandId", cmdID, "error", err)
	}

	// Outcome audit entry: a SEPARATE, correlated entry, best-effort for
	// EVERY action regardless of AuditExempt — see
	// degradedAttributionReasonPostDispatch's own doc comment
	// (fppcommand_handler.go) for why this one is never refused: by this
	// point the action has already been dispatched, or the attempt already
	// made, and refusing to record it would only deny the operator the
	// record of it (ADR-024: "you cannot see," never acceptable).
	outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditAction, Target: resolumeActionTargetID, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditOutcome, CommandID: cmdID,
		Outcome: outcomeStr, OutcomeState: outcomeStr, OutcomeReason: result.Reason,
	})

	jsonWrite(w, v1.ResolumeActionResponse{
		ServerTime: formatTime(h.now()),
		Result: v1.ResolumeActionResult{
			ID: cmdID, IdempotencyKey: idempotencyKey, Action: action, Params: normalizedParams,
			Replay: false, Outcome: outcomeStr, OutcomeReason: result.Reason,
			AttributionDegraded: dispatchDegraded || outcomeDegraded,
			DispatchedAt:        formatTimePtr(result.DispatchedAt),
			ResolvedAt:          formatTimePtr(&resolvedAt),
		},
	})
}

// resolveResolumeActionReplay answers a replayed idempotency key: nothing
// is dispatched, and existing's own already-recorded result is returned
// verbatim — mirroring resolveFPPCommandReplay's identical reasoning
// (fppcommand_dispatch.go), narrowed to this seam's own fixed target (no
// per-instance dimension to also check — see [resolumeActionTargetID]'s
// own doc comment).
func (h *handlers) resolveResolumeActionReplay(ctx context.Context, now time.Time, ac authContext, existing store.CommandRecord, requestedAction, requestedAuditAction, requestedParamsJSON string) (v1.ResolumeActionResult, *v1.Problem) {
	if existing.Action != requestedAuditAction {
		p := resolumeActionReplayConflictProblem(existing.ID, existing.Action, requestedAction)
		return v1.ResolumeActionResult{}, &p
	}
	existingParamsJSON := existing.ParamsJSON
	if existingParamsJSON == "" {
		existingParamsJSON = "{}"
	}
	if existingParamsJSON != requestedParamsJSON {
		p := resolumeActionReplayParamsConflictProblem(existing.ID, existing.Action, existingParamsJSON, requestedParamsJSON)
		return v1.ResolumeActionResult{}, &p
	}

	degraded := h.writeBestEffortAudit(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID,
		Action: existing.Action, Target: resolumeActionTargetID, IdempotencyKey: existing.IdempotencyKey,
		Kind: identity.AuditReplay, CommandID: existing.ID,
	})

	var payload resolumeActionResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &payload)

	var replayParams map[string]any
	_ = json.Unmarshal([]byte(existingParamsJSON), &replayParams)
	if replayParams == nil {
		replayParams = map[string]any{}
	}

	return v1.ResolumeActionResult{
		ID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Action: requestedAction, Params: replayParams,
		Replay: true, Outcome: payload.Outcome, OutcomeReason: existing.OutcomeReason,
		AttributionDegraded: degraded,
		DispatchedAt:        formatTimePtr(existing.DispatchedAt),
		ResolvedAt:          formatTimePtr(existing.ResolvedAt),
	}, nil
}

// updateResolumeActionOutcomeBounded wraps
// h.deps.Commands.UpdateCommandOutcome with its own
// resolumeActionDBWriteTimeout, mirroring updateCommandOutcomeBounded's
// identical reasoning (fppcommand_handler.go).
func (h *handlers) updateResolumeActionOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, resolumeActionDBWriteTimeout)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}
