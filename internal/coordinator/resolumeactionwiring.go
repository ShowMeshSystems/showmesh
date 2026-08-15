package coordinator

// This file closes Track D seam D-3's integration gap: D-3/A
// (internal/coordinator/collector/resolume's ActionDispatcher, action.go/
// action_dispatch.go) and D-3/B (internal/coordinator/api's
// ResolumeActionDispatcher interface, resolumeaction_interfaces.go) were
// built concurrently against a contract neither side could see the other's
// real types for, and nothing ever joined them: Dependencies.ResolumeActions
// was never assigned in coordinator.go, so every request fell back to
// noResolumeActionDispatcher{} and no HTTP request to Resolume was ever
// reachable through the API, however green every test in either package
// was. This is Step 6's own lesson restated for Track D: a test suite
// cannot tell you that nothing calls the function.
//
// This file is a thin adapter in the exact sense apiwiring.go's own doc
// comment already establishes for this package: "nothing here is domain
// logic; every type is a thin adapter that makes one already-built package
// satisfy an interface another already-built package declared." Every
// semantic decision — the seven actions' own resolve/baseline/dispatch/
// confirm logic, the derived per-action deadline, the deck refusal, the
// composition-identity gate, and the ADR-024 decision 11 safety class per
// action — lives in resolume.ActionDispatcher and is READ here, never
// reimplemented or reinterpreted. The one thing this file DOES decide is
// pure translation: the wire "id" string <-> resolume.ObjectID (a numeric
// string is Resolume's own object-id encoding, not a ShowMesh policy
// choice).
//
// setLayerMaster's wire type was a SECOND translation here once: A modeled
// Master as a continuous ParamRange value from the start (matching capture
// section 7.3's own ParamRange bound), while B's independently-built wire
// vocabulary fixed "master" as a boolean identical in shape to "bypassed"
// — a boundary the two builders left disagreeing on because they were
// built concurrently against no shared contract for it yet. Fixed 2026-08-15
// (defect 3): a layer master that can only be 0 or 1 is not a master, and
// nothing had shipped, so there was no compatibility obligation to the
// boolean. The wire contract now carries A's own float64 straight through
// — see resolumeActionParamVocabulary's setLayerMaster entry and
// buildResolumeActionParams' identical case below, both of which do a
// direct pass-through rather than a translation.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
)

// resolumeActionCoordinatorRequiredLabel mirrors
// resolume.localFallbackClassCoordinatorRequired ("coordinator-required")
// BY VALUE, not by import — that constant is unexported, and even if it
// were exported this file follows the identical judgment call
// resolumeCollectorSourceID and resolumeCompositionConfigKind already make
// in this same package: duplicating one literal is a smaller coupling than
// reaching into a producer package's private vocabulary for it. Every entry
// [resolume.ActionDispatcher.Actions] returns carries this exact value
// today (action.go's own doc comment: "the ONE value every ActionDescriptor
// in actionRegistry carries"), so this is the one string this file compares
// against to decide [api.ResolumeActionDescriptor.CoordinatorRequired].
const resolumeActionCoordinatorRequiredLabel = "coordinator-required"

// resolumeActionParamVocabulary is this seam's own fixed answer to a gap
// neither builder's type could see: resolume.ActionDescriptor (A's own
// registry row) carries no parameter schema at all — only a name, a safety
// class, and a local-fallback class — while api.ResolumeActionDescriptor
// needs one to decode and validate a request's params object before
// Dispatch is ever called. This table is not invented here: it is
// TRACK-D-D3-SPEC.md section 2's own seven-action table, already fixed on
// the wire by api/openapi.yaml and exercised end to end by
// cmd/showmeshctl's own seven subcommands (cmd_resolume_action.go) — this
// map is this file's own transcription of that already-shipped contract,
// not a new decision. See this file's own top comment for setLayerMaster's
// "master" specifically.
var resolumeActionParamVocabulary = map[resolume.ActionName][]api.ResolumeActionParam{
	resolume.ActionLaunchClip:   {{Name: "id", Kind: api.ResolumeActionParamString, Required: true}},
	resolume.ActionClearLayer:   {{Name: "id", Kind: api.ResolumeActionParamString, Required: true}},
	resolume.ActionBlackout:     {},
	resolume.ActionLaunchColumn: {{Name: "id", Kind: api.ResolumeActionParamString, Required: true}},
	resolume.ActionSelectDeck:   {{Name: "id", Kind: api.ResolumeActionParamString, Required: true}},
	resolume.ActionSetLayerBypass: {
		{Name: "id", Kind: api.ResolumeActionParamString, Required: true},
		{Name: "bypassed", Kind: api.ResolumeActionParamBool, Required: true},
	},
	resolume.ActionSetLayerMaster: {
		{Name: "id", Kind: api.ResolumeActionParamString, Required: true},
		{Name: "master", Kind: api.ResolumeActionParamNumber, Required: true},
	},
}

// resolumeActionDispatcherAdapter joins a real *resolume.ActionDispatcher
// (D-3/A) to the api.ResolumeActionDispatcher interface (D-3/B declared it,
// at the consumer, never importing A's package — see
// resolumeaction_interfaces.go's own doc comment for why). Constructed only
// when a live Resolume instance is configured — see
// newResolumeActionDispatcherAdapter and this file's own construction site
// in coordinator.go.
type resolumeActionDispatcherAdapter struct {
	dispatcher *resolume.ActionDispatcher
	now        func() time.Time
}

// newResolumeActionDispatcherAdapter builds the real dispatcher over
// collector and wraps it. collector must be non-nil — see
// resolume.NewActionDispatcher's own doc comment ("collector must be
// non-nil"); this file's only caller (coordinator.go's Run) already gates
// on resolumeWire.collector != nil before calling this, the identical gate
// resolumeWire.RunWatcherSupervisor's own goroutine is started under.
func newResolumeActionDispatcherAdapter(collector *resolume.Collector) *resolumeActionDispatcherAdapter {
	return &resolumeActionDispatcherAdapter{
		dispatcher: resolume.NewActionDispatcher(collector, resolume.ActionDispatcherOptions{}),
		now:        time.Now,
	}
}

var _ api.ResolumeActionDispatcher = (*resolumeActionDispatcherAdapter)(nil)

// Actions translates A's actionRegistry (resolume.ActionDescriptor) into
// B's wire-facing api.ResolumeActionDescriptor, one field at a time, with
// no policy of its own:
//
//   - Name is a straight string conversion.
//   - Params comes from resolumeActionParamVocabulary (this file's own doc
//     comment explains why that table exists at all rather than being read
//     off d).
//   - AuditExempt is [mapActionSafetyClass] applied to d.SafetyClass — A's
//     own ADR-024 decision 11 classification, read, never re-decided.
//   - CoordinatorRequired is a literal string compare against
//     [resolumeActionCoordinatorRequiredLabel] — see that constant's own
//     doc comment.
//
// A translation failure here (an undeclared safety class) panics rather
// than silently defaulting: resolume's own TestEveryActionDeclaresASafetyClass
// already fails the build before a binary carrying an undeclared entry can
// exist, so this is defense against that guarantee itself breaking, not a
// runtime condition this seam expects to hit — matching
// notYetPolledObservations' own "unreachable, so panic rather than degrade"
// posture (apiwiring.go) for the identical reason: silently defaulting a
// safety class is exactly the "doc comment claimed one thing, the code did
// another" shape Step 8's own review finding already cost this project once.
func (a *resolumeActionDispatcherAdapter) Actions() []api.ResolumeActionDescriptor {
	descriptors := a.dispatcher.Actions()
	out := make([]api.ResolumeActionDescriptor, 0, len(descriptors))
	for _, d := range descriptors {
		auditExempt, err := mapActionSafetyClass(d.SafetyClass)
		if err != nil {
			panic(fmt.Sprintf("coordinator: resolume action adapter: action %q: %v — unreachable unless "+
				"resolume.actionRegistry's own build-time safety-class test has stopped catching an undeclared entry",
				d.Name, err))
		}
		params, ok := resolumeActionParamVocabulary[d.Name]
		if !ok {
			panic(fmt.Sprintf("coordinator: resolume action adapter: action %q has no entry in "+
				"resolumeActionParamVocabulary (resolumeactionwiring.go) — every member of resolume.actionRegistry "+
				"must have one", d.Name))
		}
		out = append(out, api.ResolumeActionDescriptor{
			Name:                string(d.Name),
			Params:              params,
			AuditExempt:         auditExempt,
			CoordinatorRequired: d.LocalFallbackClass == resolumeActionCoordinatorRequiredLabel,
		})
	}
	return out
}

// mapActionSafetyClass translates resolume.ActionSafetyClass (A's own
// three-valued, build-guarded enum) to the boolean
// api.ResolumeActionDescriptor.AuditExempt B's contract already fixes.
// ActionSafetyClassUndeclared — the zero value, and per A's own doc
// comment never a valid registry entry — is deliberately NOT mapped to
// false here: an exhaustive switch with no default-to-a-value branch is
// what keeps "nobody decided" from silently reading as "decided not
// exempt" one layer up from where A's own type already prevents it.
func mapActionSafetyClass(c resolume.ActionSafetyClass) (auditExempt bool, err error) {
	switch c {
	case resolume.ActionSafetyClassExempt:
		return true, nil
	case resolume.ActionSafetyClassNotExempt:
		return false, nil
	default:
		return false, fmt.Errorf("safety class %d is not a recognized, declared value", c)
	}
}

// Dispatch translates one wire dispatch request into a call against A's
// real *resolume.ActionDispatcher and translates its result back. Every
// outcome, deadline, refusal reason, and safety-class consequence already
// decided by A survives this call unchanged — see mapActionOutcomeState and
// buildResolumeActionParams for the only two things this method decides for
// itself, both pure boundary translation (this file's own top comment).
func (a *resolumeActionDispatcherAdapter) Dispatch(ctx context.Context, action string, params map[string]any, _ time.Time) (api.ResolumeActionResult, error) {
	name, ok := resolumeActionNameFromWire(action)
	if !ok {
		// Unreachable through the real handler: handleDispatchResolumeAction
		// (resolumeaction.go) resolves action against Actions()'s own
		// output via findResolumeActionDescriptor before Dispatch is ever
		// called, so an unrecognized name here means that guard was bypassed
		// — a caller mistake this package's own Dispatch doc comment
		// reserves the error return for, mirrored here for the identical
		// reason.
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume action adapter: dispatch called with unrecognized action %q", action)
	}

	actionParams, refusalReason, err := buildResolumeActionParams(name, params)
	if err != nil {
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume action adapter: building params for %q: %w", action, err)
	}
	if refusalReason != "" {
		// A malformed "id" (not a base-10 integer — Resolume's own object-id
		// encoding, composition.go's [ObjectID]) can never resolve against
		// the stored composition either way, so this is the identical
		// outcome A's own Dispatch would reach after issuing zero HTTP
		// requests — reported directly, without paying for a round trip
		// through A only to have it refuse for the same reason a second way.
		return api.ResolumeActionResult{Outcome: api.ResolumeOutcomeRefused, Reason: refusalReason, Dispatched: false}, nil
	}

	outcome, err := a.dispatcher.Dispatch(ctx, name, actionParams)
	if err != nil {
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume action adapter: dispatch %q: %w", action, err)
	}
	return a.translateActionOutcome(outcome)
}

// translateActionOutcome maps one resolume.ActionOutcome to
// api.ResolumeActionResult. Reason and every timestamp A computed pass
// through unchanged; State goes through [mapActionOutcomeState], the one
// place a translation failure here is reported as a Go error (a 500 to the
// operator) rather than guessed at — see that function's own doc comment.
func (a *resolumeActionDispatcherAdapter) translateActionOutcome(outcome resolume.ActionOutcome) (api.ResolumeActionResult, error) {
	state, err := mapActionOutcomeState(outcome.State)
	if err != nil {
		return api.ResolumeActionResult{}, fmt.Errorf("coordinator: resolume action adapter: action %q: %w", outcome.Action, err)
	}

	// A's own invariant (ActionOutcome.DispatchedAt's doc comment): the zero
	// time.Time exactly when State == ActionRefused, never otherwise. Dispatched
	// is derived from the TRANSLATED state rather than re-testing the zero
	// time here a second, possibly disagreeing way.
	dispatched := state != api.ResolumeOutcomeRefused
	var dispatchedAt *time.Time
	if dispatched && !outcome.DispatchedAt.IsZero() {
		t := outcome.DispatchedAt
		dispatchedAt = &t
	}

	// ResolvedAt: this adapter's own wall-clock reading taken the instant
	// A's Dispatch call has returned — api.ResolumeActionResult's own doc
	// comment says this is "not expected in practice" to be nil, so it
	// never is, for every outcome including a refusal.
	resolvedAt := a.now()

	return api.ResolumeActionResult{
		Outcome:      state,
		Reason:       sanitizeResolumeActionReason(outcome.Action, outcome.Reason),
		Dispatched:   dispatched,
		DispatchedAt: dispatchedAt,
		ResolvedAt:   &resolvedAt,
	}, nil
}

// resolumeActionReasonURLMarker flags a reason string that leaked a URL —
// which, for every endpoint this package's own Client calls, necessarily
// carries BOTH Resolume's own host and a raw object id in its path
// (ADR-029: neither may reach an operator-facing surface). Review fix 2
// (2026-08-15), reproduced: a connection reset mid-response — the
// documented signature of the Arena use-after-free this whole track is
// engineered around (ADR-032, BUILD-LOG 2026-08-14) — has no entry in
// ClassifyError's vocabulary (client.go), so it falls through to the bare
// *url.Error text, whose own Error() method is ALWAYS
// `"%s %q: %s", Op, URL, Err` (net/url), which is exactly this shape.
const resolumeActionReasonURLMarker = "://"

// sanitizeResolumeActionReason is this file's own last line of defense —
// see this file's own top comment for why THIS package does the
// translation rather than fixing resolume.ClassifyError itself (a
// different, concurrently-built package this task does not own; that
// package still needs a connection-reset vocabulary entry — see this
// task's own report). A reason containing a URL is replaced WHOLESALE,
// never patched in place: a URL can appear anywhere in an arbitrary error
// string, and a partial redaction is an easy way to still leak a fragment
// of the path, which is exactly where the raw object id lives.
func sanitizeResolumeActionReason(action resolume.ActionName, reason string) string {
	if !strings.Contains(reason, resolumeActionReasonURLMarker) {
		return reason
	}
	return fmt.Sprintf(
		"%s: the underlying Resolume request failed with a transport error this coordinator has no named "+
			"classification for; the original error named a request URL, which never reaches an operator-facing "+
			"surface (ADR-029)",
		action)
}

// mapActionOutcomeState translates resolume.ActionOutcomeState (A's own
// five-member outcome vocabulary) to api.ResolumeActionOutcome (B's own,
// separately declared five-member vocabulary) by exhaustive switch. The
// default branch is the load-bearing line in this file: an outcome state
// this switch does not recognize returns an error instead of falling
// through to a plausible-looking default, because a silently substituted
// default is exactly ADR-029's own named defect ("an action whose effect
// cannot be observed reports as unconfirmable, never as success") one layer
// removed — a translation bug that turned an unrecognized value into
// "confirmed" would be indistinguishable from success at every layer above
// this one. Broken deliberately and restored, to confirm this branch
// actually fires: see resolumeactionwiring_test.go's
// TestMapActionOutcomeStateFailsLoudlyOnUnmappedValue.
func mapActionOutcomeState(s resolume.ActionOutcomeState) (api.ResolumeActionOutcome, error) {
	switch s {
	case resolume.ActionConfirmed:
		return api.ResolumeOutcomeConfirmed, nil
	case resolume.ActionUnconfirmed:
		return api.ResolumeOutcomeUnconfirmed, nil
	case resolume.ActionUnconfirmable:
		return api.ResolumeOutcomeUnconfirmable, nil
	case resolume.ActionRefused:
		return api.ResolumeOutcomeRefused, nil
	case resolume.ActionFailed:
		return api.ResolumeOutcomeFailed, nil
	default:
		return "", fmt.Errorf("dispatch returned an unrecognized outcome state %q — refusing to guess which of the five outcomes this means", s)
	}
}

// resolumeActionNameFromWire maps a wire action name to A's own
// resolume.ActionName constant — the exhaustive inverse of
// resolumeActionAuditNames (resolumeaction.go), which this file
// deliberately does not import or reuse: that map is B's own audit-log
// naming concern, unrelated to which of A's seven ActionName constants a
// wire string identifies.
func resolumeActionNameFromWire(action string) (resolume.ActionName, bool) {
	switch action {
	case string(resolume.ActionLaunchClip):
		return resolume.ActionLaunchClip, true
	case string(resolume.ActionClearLayer):
		return resolume.ActionClearLayer, true
	case string(resolume.ActionBlackout):
		return resolume.ActionBlackout, true
	case string(resolume.ActionLaunchColumn):
		return resolume.ActionLaunchColumn, true
	case string(resolume.ActionSelectDeck):
		return resolume.ActionSelectDeck, true
	case string(resolume.ActionSetLayerBypass):
		return resolume.ActionSetLayerBypass, true
	case string(resolume.ActionSetLayerMaster):
		return resolume.ActionSetLayerMaster, true
	default:
		return "", false
	}
}

// buildResolumeActionParams translates B's already-decoded, natively-typed
// params map (string/bool values only — decodeResolumeActionParams,
// resolumeaction.go, has already enforced presence, non-null, and kind
// against resolumeActionParamVocabulary before this is ever called) into
// A's resolume.ActionParams. refusalReason is non-empty only for a
// malformed "id" — see [resolveWireObjectID] — in which case params/err are
// both zero and the caller must not call Dispatch at all. err is non-nil
// only for a shape decodeResolumeActionParams should have already made
// impossible (a missing or wrong-typed key for the action's own declared
// vocabulary): a genuine internal inconsistency between this file's own
// resolumeActionParamVocabulary and B's decode step, surfaced loudly rather
// than silently building a zero-valued ActionParams and dispatching it.
func buildResolumeActionParams(name resolume.ActionName, params map[string]any) (out resolume.ActionParams, refusalReason string, err error) {
	switch name {
	case resolume.ActionLaunchClip:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		return resolume.ActionParams{ClipID: id}, "", nil

	case resolume.ActionClearLayer:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		return resolume.ActionParams{LayerID: id}, "", nil

	case resolume.ActionBlackout:
		return resolume.ActionParams{}, "", nil

	case resolume.ActionLaunchColumn:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		return resolume.ActionParams{ColumnID: id}, "", nil

	case resolume.ActionSelectDeck:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		return resolume.ActionParams{DeckID: id}, "", nil

	case resolume.ActionSetLayerBypass:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		bypassed, ok := params["bypassed"].(bool)
		if !ok {
			return resolume.ActionParams{}, "", fmt.Errorf(`setLayerBypass dispatched without a boolean "bypassed" param (got %#v)`, params["bypassed"])
		}
		return resolume.ActionParams{LayerID: id, Bypassed: bypassed}, "", nil

	case resolume.ActionSetLayerMaster:
		id, refusal, err := resolveWireObjectID(params, "id")
		if err != nil || refusal != "" {
			return resolume.ActionParams{}, refusal, err
		}
		master, ok := params["master"].(float64)
		if !ok {
			return resolume.ActionParams{}, "", fmt.Errorf(`setLayerMaster dispatched without a numeric "master" param (got %#v)`, params["master"])
		}
		// Direct pass-through since defect 3 (2026-08-15): the wire number
		// IS A's own continuous [0, 1]-shaped domain — see this file's own
		// top comment. Range validation against Arena's OWN declared bound
		// for this specific parameter happens in
		// resolume.ActionDispatcher.dispatchSetLayerMaster
		// (action_dispatch.go), which is the only place in this codebase
		// that has read it by the time a request reaches here.
		return resolume.ActionParams{LayerID: id, Master: master}, "", nil

	default:
		return resolume.ActionParams{}, "", fmt.Errorf("no param-building rule for action %q", name)
	}
}

// resolveWireObjectID reads params[key] as a non-empty string and parses it
// as a resolume.ObjectID (a base-10 integer — composition.go's own id
// encoding, and idmap.go's parseObjectID applies the identical rule when a
// composition file is first loaded). A non-numeric id can never resolve
// against the stored composition either way — see this file's own
// [resolumeActionDispatcherAdapter.Dispatch] for why that is reported as a
// refusal, not a Go error.
func resolveWireObjectID(params map[string]any, key string) (id resolume.ObjectID, refusalReason string, err error) {
	raw, ok := params[key].(string)
	if !ok || raw == "" {
		return 0, "", fmt.Errorf("dispatched without a non-empty string %q param (got %#v)", key, params[key])
	}
	n, perr := strconv.ParseInt(raw, 10, 64)
	if perr != nil {
		return 0, fmt.Sprintf("%q is not a valid ShowMesh object reference (expected a numeric id, got %q)", key, raw), nil
	}
	return resolume.ObjectID(n), "", nil
}
