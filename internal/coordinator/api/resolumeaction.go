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
	"github.com/showmeshsystems/showmesh/pkg/observation"
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

// resolumeActionBookkeepingBudget bounds each individual piece of
// post-dispatch bookkeeping THIS handler does once
// [ResolumeActionDispatcher.Dispatch] has already returned (the
// commands-row outcome update, the outcome audit entry) — the same
// property dbWriteTimeout (fppcommand_handler.go) protects for FPP, named
// and sized independently here rather than reusing that constant: Review
// fix 4 (2026-08-15) rebuilds [resolumeActionHTTPWriteDeadline] from ITS
// OWN complete list of terms, and a deadline that has to reach into
// another file's unrelated constant to state its own worst case is exactly
// the kind of arithmetic that drifts apart unnoticed. 5s is generous for a
// single local SQLite write — this endpoint spends it twice (see
// [resolumeActionHTTPWriteDeadline]).
const resolumeActionBookkeepingBudget = 5 * time.Second

// resolumeActionWriteDeadlineMargin is headroom [resolumeActionHTTPWriteDeadline]
// carries on top of its own known worst-case work, never zero: a deadline
// computed with no slack over its own inputs is indistinguishable from one
// that merely happens to be large enough, which is exactly the defect two
// independent review passes found in this endpoint's PREVIOUS write
// deadline (docs/private/HANDOFF-d3-review-fixes.md) — an unrelated
// 30-second literal added to a 30-second confirm clamp that covered
// neither the pre-dispatch baseline phase nor a poll loop that only checks
// its own deadline BETWEEN iterations.
const resolumeActionWriteDeadlineMargin = 5 * time.Second

// resolumeActionMaxDispatchDuration bounds [ResolumeActionDispatcher.Dispatch]
// itself, end to end — the pre-dispatch baseline phase, the write, and
// confirmation — and is handed to Dispatch as an actual context deadline
// (see this file's own call site), not merely consulted here as an upper
// bound to build a write deadline from. It MUST equal
// resolume.MaxDispatchDuration (internal/coordinator/collector/resolume,
// action.go): that constant's own doc comment is explicit that an EARLIER
// revision of this file exported resolume.MaxActionConfirmDeadline (the
// confirm-poll clamp alone, 30s) as its bound, which review fix 4 found was
// false — the confirm clamp never covered the baseline phase (unbounded
// per layer, 18 layers x 5s request timeout is 90s before dispatch is even
// attempted) or the write phase, and Dispatch itself ran on a context with
// no deadline at all (context.WithoutCancel(ctx), correct for surviving a
// client abort, wrong for being bounded).
//
// This package deliberately does not import
// internal/coordinator/collector/resolume in production code (the same
// decoupling resolumeaction_interfaces.go's own doc comment states for the
// dispatcher interface itself, and the same "duplicate the literal, name
// the coupling" judgment apiwiring.go's own fppMQTTCollectorSourceID etc.
// already make throughout this codebase) — so this constant stays a
// literal, but it is no longer a coincidence: TestResolumeActionMaxDispatchDurationEqualsRegistryMax
// (resolumeaction_test.go, a test file, which CAN import the producer
// package without creating a production dependency) fails the build the
// moment this value and resolume.MaxDispatchDuration disagree.
const resolumeActionMaxDispatchDuration = 40 * time.Second

// resolumeActionHTTPWriteDeadline bounds this endpoint's own HTTP write
// deadline (set before the request body is even read, so before this
// coordinator knows which action is about to run) — rebuilt from its real
// terms (Review fix 4, 2026-08-15) rather than an independently asserted
// guess: [resolumeActionMaxDispatchDuration] for Dispatch itself, TWO
// rounds of [resolumeActionBookkeepingBudget] for the commands-row update
// and the outcome audit entry this handler performs AFTER Dispatch
// returns (both previously spent outside every existing bound), plus
// [resolumeActionWriteDeadlineMargin]. cmd/showmeshctl's own
// minResolumeActionClientTimeout (cmd_resolume_action.go) is reconciled
// against THIS value — see TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget
// below for why that boundary stays two independently chosen literals
// rather than one shared constant (that program does not import this
// package, and its own importgraph_test.go forbids the reverse).
const resolumeActionHTTPWriteDeadline = resolumeActionMaxDispatchDuration + 2*resolumeActionBookkeepingBudget + resolumeActionWriteDeadlineMargin

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

// resolumeActionRequestBodyTooLargeProblem reuses
// [ProblemTypeResolumeCompositionTooLarge]'s wire value ("payload-too-large",
// resolumecomposition.go) rather than minting a second 413 class: unlike
// the busy/evidence-not-current split (problem.go), where two 409s needed
// different types because their REMEDIES differ, every "too large" refusal
// in this API has the identical remedy ("shrink the request"), so one
// generic type serves every producer of it.
//
// Review fix 5 (2026-08-15): before this, a body over
// maxResolumeActionRequestBodyBytes was silently truncated by the
// LimitReader and the resulting truncated-JSON parse failure was reported
// as "request body must be a JSON object matching ..." — a size refusal
// disguised as a syntax error, whose stated remedy (fix your JSON) does
// not fix anything.
func resolumeActionRequestBodyTooLargeProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeResolumeCompositionTooLarge,
		Title:  "Payload too large",
		Status: http.StatusRequestEntityTooLarge,
		Detail: fmt.Sprintf(
			"the request body exceeds this endpoint's %d byte limit; an action name, an idempotency key, and at "+
				"most two short parameters never legitimately need more",
			maxResolumeActionRequestBodyBytes),
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

// resolumeActionEvidenceState maps outcome to pkg/observation's
// evidence-state vocabulary, for the two fields this handler populates
// that are documented as carrying THAT vocabulary and not this endpoint's
// own five-word outcome — store.CommandRecord.OutcomeState (commands.go:
// "OutcomeState uses pkg/observation's state vocabulary") and
// identity.AuditEntry.OutcomeState (identity/types.go: "OutcomeState uses
// ADR-020's evidence-state vocabulary (observation.State)"). Review fix 3
// (2026-08-15): before this, both fields were set directly from the
// Resolume outcome word itself (e.g. "confirmed", "refused"), which is not
// a member of that vocabulary at all — an audit reader filtering on
// evidence states would silently drop or misread every Resolume entry.
//
// [ResolumeActionResult] carries no per-observation freshness signal the
// way FPPCommandResult.outcomeState does (fppcommand_evidence.go's
// resolveConfirmationEvidence reads a real [observation.Observation] and
// reports the state IT was decided from) — D-3/A's own Dispatch returns a
// five-word outcome plus a reason, nothing finer. TRACK-D-D3-SPEC.md
// section 4.1 makes "confirmed" the one outcome this package can state
// honestly anyway: confirming evidence read strictly after dispatch is, by
// that section's own definition, current. Every other outcome leaves the
// field genuinely absent rather than filling it with a plausible-looking
// guess — ADR-020 requires absence be STATED, which this endpoint's own
// always-non-empty outcomeReason already does for every one of these
// cases, not that every field be forced to hold a value it cannot back.
func resolumeActionEvidenceState(outcome ResolumeActionOutcome) string {
	if outcome == ResolumeOutcomeConfirmed {
		return string(observation.StateCurrent)
	}
	return ""
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
// rule decodeFPPCommandParams (fppcommand_primitives.go) enforces for FPP:
// an absent OPTIONAL parameter (def.Required == false) is left out of the
// returned map entirely — never defaulted to a value here, since what an
// absence means is a resolution rule its caller applies (see
// [ResolumeActionParam]'s own doc comment) — while an absent REQUIRED
// parameter is always a 400. An explicit null is refused for every
// declared parameter regardless of Required: absent and null are two
// different things on this wire, and "optional" only ever means "may be
// absent," never "null is acceptable."
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
	// absent. The refusal also names the expected keys (ADR-037: with "id"
	// retired, a caller who still sends it must be told what replaced it,
	// not just that it was rejected).
	var unknown []string
	for k := range fields {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		expected := make([]string, 0, len(desc.Params))
		for _, def := range desc.Params {
			expected = append(expected, def.Name)
		}
		sort.Strings(expected)
		p := invalidParameterProblem(fmt.Sprintf(
			"params contains unrecognized key(s) for action %q: %s (a typo'd parameter name is refused rather than "+
				"silently ignored; this action expects: %s)", desc.Name, strings.Join(unknown, ", "), strings.Join(expected, ", ")))
		return nil, &p
	}

	normalized := make(map[string]any, len(desc.Params))
	for _, def := range desc.Params {
		raw, present := fields[def.Name]
		switch {
		case !present:
			if !def.Required {
				// Absent and optional: left out of the map entirely, never
				// defaulted here — see this function's own doc comment.
				continue
			}
			p := invalidParameterProblem(fmt.Sprintf("params.%s is required and was not provided", def.Name))
			return nil, &p
		case isJSONNull(raw):
			p := invalidParameterProblem(fmt.Sprintf(
				"params.%s must not be null (an explicit null is not the same as an omitted field)", def.Name))
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
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(resolumeActionHTTPWriteDeadline))

	// Read the whole (bounded) body BEFORE decoding it (Review fix 5,
	// 2026-08-15): the previous version handed json.Decoder a
	// io.LimitReader(..., maxResolumeActionRequestBodyBytes+1) directly, so
	// an oversized body was silently truncated at the limit and the
	// resulting truncated-JSON parse failure was reported as an "invalid
	// parameter" syntax error — a size refusal wearing the wrong problem
	// type, whose stated remedy (fix your JSON) fixes nothing. Reading
	// first makes the two conditions distinguishable: len(bodyBytes) >
	// maxResolumeActionRequestBodyBytes is a size refusal; anything else
	// that fails to unmarshal is a genuine syntax error.
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, maxResolumeActionRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read resolume action request body", err)
		return
	}
	if len(bodyBytes) > maxResolumeActionRequestBodyBytes {
		writeProblem(w, h.logger, now, resolumeActionRequestBodyTooLargeProblem())
		return
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &top); err != nil {
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
	// fppcommand_dispatch.go). Review fix 4 (2026-08-15): bgCtx alone
	// carries no deadline of any kind (correct for surviving a client
	// abort, wrong for being bounded) — dispatchCtx adds
	// [resolumeActionMaxDispatchDuration] as an actual timeout on top,
	// WITHOUT losing the WithoutCancel detachment, so a stalled Dispatch
	// call can no longer run unbounded.
	bgCtx := context.WithoutCancel(ctx)
	dispatchCtx, dispatchCancel := context.WithTimeout(bgCtx, resolumeActionMaxDispatchDuration)
	defer dispatchCancel()
	result, dispatchErr := h.deps.ResolumeActions.Dispatch(dispatchCtx, action, normalizedParams, now)
	if dispatchErr != nil {
		h.writeInternalError(w, now, "dispatch resolume action", dispatchErr)
		return
	}

	resolvedAt := h.now()
	resolvedState := "resolved"
	outcomeStr := string(result.Outcome)
	// evidenceState carries pkg/observation's vocabulary, NOT outcomeStr —
	// see [resolumeActionEvidenceState]'s own doc comment (Review fix 3,
	// 2026-08-15): store.CommandRecord.OutcomeState and
	// identity.AuditEntry.OutcomeState are both documented as that
	// vocabulary, not this endpoint's own five-word outcome.
	evidenceState := resolumeActionEvidenceState(result.Outcome)
	finalResult, _ := json.Marshal(resolumeActionResultPayload{Outcome: outcomeStr})
	finalResultStr := string(finalResult)
	if err := h.updateResolumeActionOutcomeBounded(bgCtx, cmdID, store.CommandOutcomeUpdate{
		DispatchedAt: result.DispatchedAt, ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &evidenceState, OutcomeReason: &result.Reason,
	}); err != nil {
		h.logWarn("failed to record resolume action outcome", "commandId", cmdID, "error", err)
	}

	// Outcome audit entry: a SEPARATE, correlated entry, best-effort for
	// EVERY action regardless of AuditExempt — see
	// degradedAttributionReasonPostDispatch's own doc comment
	// (fppcommand_handler.go) for why this one is never refused: by this
	// point the action has already been dispatched, or the attempt already
	// made, and refusing to record it would only deny the operator the
	// record of it (ADR-024: "you cannot see," never acceptable). Bounded
	// by this seam's own [resolumeActionBookkeepingBudget], not FPP's
	// shared dbWriteTimeout — see [resolumeActionHTTPWriteDeadline]'s own
	// doc comment for why this seam names and bounds its own post-dispatch
	// bookkeeping rather than borrowing another file's constant.
	outcomeDegraded := h.writeResolumeActionAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: auditAction, Target: resolumeActionTargetID, IdempotencyKey: idempotencyKey,
		Kind: identity.AuditOutcome, CommandID: cmdID,
		Outcome: outcomeStr, OutcomeState: evidenceState, OutcomeReason: result.Reason,
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
//
// existing.ResultJSON (and therefore payload.Outcome) may be "" in the
// identical narrow, accepted race resolveFPPCommandReplay's own doc
// comment names: a row this coordinator has recorded but not yet
// resolved. That is a real, honest value (ADR-020), not a bug — see
// ResolumeActionResult.outcome's own description (api/openapi.yaml) for
// why the wire enum accepts it. Review fix 1 (2026-08-15) is what keeps
// this race distinguishable from PERMANENT blankness: before it,
// ReconcileStrandedFPPCommands (fppcommand_reconcile.go) skipped every row
// whose TargetKind was not "fpp", so a Resolume row a prior process left
// unresolved replayed "" forever. ReconcileStrandedResolumeActions
// (resolumeaction_reconcile.go) closes that gap the same way
// ReconcileStrandedFPPCommands closes it for FPP.
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
// resolumeActionBookkeepingBudget, mirroring updateCommandOutcomeBounded's
// identical reasoning (fppcommand_handler.go) — one of the two writes
// [resolumeActionHTTPWriteDeadline] names and bounds by that constant.
func (h *handlers) updateResolumeActionOutcomeBounded(parent context.Context, id string, upd store.CommandOutcomeUpdate) error {
	ctx, cancel := context.WithTimeout(parent, resolumeActionBookkeepingBudget)
	defer cancel()
	return h.deps.Commands.UpdateCommandOutcome(ctx, id, upd)
}

// writeResolumeActionAuditBounded is [handlers.writeBestEffortAudit] with
// this seam's own resolumeActionBookkeepingBudget applied to parent — the
// Resolume-action sibling of writeBestEffortAuditBounded
// (fppcommand_handler.go), deliberately NOT that shared helper: this seam
// bounds its own post-dispatch bookkeeping by a constant it names itself
// (see [resolumeActionHTTPWriteDeadline]'s own doc comment), rather than
// borrowing dbWriteTimeout, an FPP-owned constant this file has no reason
// to reach into.
func (h *handlers) writeResolumeActionAuditBounded(parent context.Context, now time.Time, reason string, entry identity.AuditEntry) (degraded bool) {
	ctx, cancel := context.WithTimeout(parent, resolumeActionBookkeepingBudget)
	defer cancel()
	return h.writeBestEffortAudit(ctx, now, reason, entry)
}
