package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// This file is Step 9's own seam cut: STEP-9-SPEC.md section 4 requires the
// macro executor (internal/coordinator/macro, a different package, built in
// a later wave) to invoke one FPP primitive in-process — never by issuing
// itself an HTTP request against this coordinator's own API. Before this
// file existed, every piece a caller like that would need (the primitive
// registry, the evidence resolver, the confirmation loop, the safety-class
// audit fallback, the idempotency-first ordering) was package-private
// inside [handlers.handleFPPCommand] (fppcommand_handler.go).
//
// [handlers.dispatchFPPCommand] below is the ONE dispatch/confirm/audit
// core both that HTTP handler and this file's exported
// [FPPCommandDispatcher] call — the handler is now a thin wire adapter:
// decode the JSON body, resolve the wire action to build
// [FPPCommandInput], call this core, translate the result back to JSON.
// Nothing about WHAT happens on a dispatch changed; only where the code
// that decides it lives. Every Step 7/Step 8 review finding
// fppcommand_handler.go's own top comment lists is preserved here
// unconditionally — this file's own doc comments repeat which one each
// piece of ordering exists for, so a future edit does not have to
// rediscover it from fppcommand_handler.go's git history.

// FPPCommandIssuer identifies the principal an FPP command dispatch is
// attributed to, in an audit entry and in the command's own envelope —
// the in-process equivalent of what [authFromContext] plus
// [handlers.clientAddr] resolve from an *http.Request for the wire path.
// A caller outside this package (Step 9's macro executor) already holds
// this from whatever authenticated ITS OWN request to run the macro; it is
// not resolved here.
//
// Every field mirrors one [identity.AuditEntry] field of the identical
// name (Form, CredentialID, ClientAddr) or one [command.Issuer] field
// (PrincipalID, PrincipalName), so an in-process dispatch's audit trail and
// command envelope carry the exact same shape an HTTP-dispatched command's
// do — never a degraded "system" identity standing in for a real
// principal, which would make an in-process command dispatch
// unattributable in exactly the way ADR-024 exists to prevent.
type FPPCommandIssuer struct {
	PrincipalID   string
	PrincipalName string
	Form          identity.CredentialForm
	CredentialID  string

	// ClientAddr is empty for most in-process callers: there is no HTTP
	// client address to record, and [identity.AuditEntry.ClientAddr]'s own
	// doc comment already treats "empty unless a trusted proxy is
	// configured" as a normal, expected value on this field.
	ClientAddr string

	// RunID, when non-empty, is carried onto every [identity.AuditEntry]
	// this dispatch writes (dispatch, outcome, and replay) as
	// entry.Params["runId"] - STEP-9-SPEC.md section 2.9's "each step's
	// commands row records the issuing principal and the run id, so an
	// investigator reaching a command row can always get back to the
	// authorizing revision". There is no run_id column on the commands
	// table itself (schemaV7 links a run to its dispatched command the
	// other way, from macro_run_steps.command_id), so the audit trail -
	// already correlated to a command by CommandID, and already the
	// durable, investigator-facing record for every other fact this
	// dispatch attributes - is where this fact is recorded instead of a
	// second, redundant mechanism. Empty for every caller outside Step
	// 9's macro executor (an ordinary HTTP-dispatched command has no run
	// to name), and left out of entry.Params entirely rather than written
	// as an empty string, matching this package's own "absent, not
	// present-and-blank" rule for evidence.
	RunID string
}

// FPPCommandInput is one FPP primitive invocation, named by its wire
// action (the same vocabulary POST /api/v1/fpp/{instanceId}/commands
// accepts — see [fppCommandWireActions]), addressed at one FPP instance,
// with the caller's own idempotency key and issuing principal.
//
// Params carries already-normalized, natively-typed values (string, bool,
// int64) — the SAME shape [decodeFPPCommandParams] produces from a wire
// JSON body, but never JSON itself: an in-process caller has no wire body
// to decode, and STEP-9-SPEC.md section 5.3 has the macro system's own
// config surface validate a bound action's params against
// [fppPrimitive.ValidateParams] at AUTHORING time, so by the time a macro
// step reaches this struct its params are already known-good, normalized
// Go values, not JSON needing this endpoint's own absent/null/empty
// distinction applied to it. [handlers.dispatchFPPCommand] still runs
// ValidateParams again unconditionally (defense in depth: never trust a
// caller's own claim that its params were already validated), it just
// never runs [decodeFPPCommandParams] itself for this path — that
// function's whole job is distinguishing an ABSENT wire key from an
// explicit JSON null, a distinction that does not exist for a Go
// map[string]any built by code, not decoded from bytes.
type FPPCommandInput struct {
	InstanceID     string
	Action         string
	Params         map[string]any
	IdempotencyKey string
	Issuer         FPPCommandIssuer

	// RequestedRevision carries [command.Envelope.RequestedRevision]
	// ("the configuration revision this command was issued against, when
	// the command is revision-sensitive") into the dispatched command's
	// own store.CommandRecord.RequestedRevision column. Empty for an
	// ordinary HTTP-dispatched command, which names no revision. STEP-9-
	// SPEC.md section 6.1 requires a macro step's dispatch to carry its
	// run's pinned macro revision here, "formatted so a macro-issued
	// command is distinguishable from an operator-issued one" - that
	// formatting is Step 9's macro executor's own decision to make (it
	// holds the run id and the pinned revision; this package does not),
	// so this field is deliberately an opaque caller-supplied string
	// rather than a structured type this package would have to guess the
	// shape of.
	RequestedRevision string

	// NeverWithholdOnAuditFailure disables the ADR-024 decision 11
	// fail-closed branch for THIS dispatch: when the audit store cannot be
	// written, the command is dispatched anyway with degraded attribution
	// rather than refused, whatever its safety class.
	//
	// OWNER DECISION, 2026-08-14. Set by Step 9's macro executor on every
	// step it dispatches, and by nothing else. The rule in the owner's own
	// words: "the run needs to always RUN all steps, no matter what. If
	// something doesn't confirm, or we can't record it, that doesn't
	// matter. it should still send the command. we cannot risk the show
	// because a logging or audit system is down, that's not how show
	// critical infrastructure works."
	//
	// Decision 11's closed list of three exempt actions (blackout, stop,
	// power-off) survives for the ordinary single-command HTTP path, where
	// an operator is present, sees the 503, and can retry deliberately.
	// It does not survive inside a macro run, where nobody is watching and
	// a refused step is a hole in a show.
	NeverWithholdOnAuditFailure bool
}

// FPPCommandOutcome is the result of dispatching one FPP primitive through
// [FPPCommandDispatcher.Dispatch] — the in-process sibling of
// [v1.FPPCommandResult], carrying the identical facts
// [handlers.handleFPPCommand]'s own HTTP response renders, as Go values
// rather than wire JSON strings. DispatchedAt/ResolvedAt are already UTC
// ([utcTimePtr]/[time.Time.UTC]), matching Step 8 review finding 14's own
// reasoning for why a fresh dispatch and a replay must render identically
// regardless of which one produced a given instant — a caller comparing
// two FPPCommandOutcome values (a macro step re-running after a
// coordinator restart, for instance) gets that same guarantee for free.
type FPPCommandOutcome struct {
	CommandID      string
	IdempotencyKey string
	Action         string
	InstanceID     string
	Params         map[string]any

	// Replay is true when IdempotencyKey named an already-dispatched
	// command: nothing was dispatched by THIS call, and every other field
	// reports what the ORIGINAL dispatch already recorded — see
	// [handlers.resolveFPPCommandReplay]'s own doc comment for the
	// three-way replay rule (same key+action+target+params replays;
	// anything else is refused via the returned *v1.Problem instead of
	// ever reaching this struct).
	Replay bool

	// Outcome is "confirmed" or "unconfirmed" for a resolved dispatch. It
	// may be empty for a Replay observed mid-flight — see
	// [handlers.resolveFPPCommandReplay]'s own doc comment for the one
	// accepted race this covers, carried over unchanged from Step 7/8.
	Outcome             string
	OutcomeState        string
	OutcomeReason       string
	AttributionDegraded bool
	DispatchedAt        *time.Time
	ResolvedAt          *time.Time

	// DispatchFailed is true when the request to FPP itself did not
	// succeed: the client could not be built, the connection was
	// refused, DNS failed, or FPP answered a non-2xx status. It is
	// false whenever FPP accepted the command, whatever the
	// confirmation then concluded.
	//
	// Outcome cannot answer this. It is only ever "confirmed" or
	// "unconfirmed", so a host that is powered off arrives as
	// "unconfirmed", indistinguishable from a command FPP accepted and
	// whose effect no evidence reached us about. That distinction is
	// invisible on the single-command HTTP path, which reports the same
	// two words either way, and load-bearing for a macro run: ADR-031
	// decision 2 routes "failed" and "unconfirmed" onto two separate
	// policy axes, and a step that never reached its host is a failure
	// of the show, not a gap in ShowMesh's own evidence pipeline.
	// Without this field a four-step macro against a dead host
	// dispatched nothing and reported completed: true.
	//
	// OutcomeState is not a usable substitute: "collection_failed" is
	// also what a failed read of this coordinator's OWN observation
	// store returns during confirmation (fppcommand_evidence.go), which
	// is genuinely a monitoring gap, so keying on it would push that
	// case onto the failure axis and abort a run for a condition that
	// must never stop a show.
	DispatchFailed bool
}

// timePtr returns a pointer to a copy of t — used by
// [handlers.dispatchFPPCommand]'s fresh-dispatch path, where resolvedAt is
// always known (unlike DispatchedAt, which is nil until a dispatch is
// actually attempted — see [fppCommandRecordFor]'s own "defect 9" note in
// fppcommand_handler.go).
func timePtr(t time.Time) *time.Time { return &t }

// fppCommandInternalError names WHICH internal dependency call
// [handlers.dispatchFPPCommand] failed against, so the wire adapter
// ([handlers.handleFPPCommand]) can still call [handlers.writeInternalError]
// with the exact same action label this endpoint has always logged
// (unchanged by this refactor — see this file's own top comment), while an
// in-process caller ([FPPCommandDispatcher.Dispatch]) gets a plain error it
// can log or wrap without needing to know this package's own label
// vocabulary. Never a *v1.Problem: every case this type wraps is this
// coordinator's own dependency failing, not a caller mistake, and
// [handlers.writeInternalError] deliberately never discloses err's own text
// on the wire (see that method's doc comment) — the label is what a
// maintainer's log line needs; err is what it explains.
type fppCommandInternalError struct {
	label string
	err   error
}

func (e *fppCommandInternalError) Error() string { return e.label + ": " + e.err.Error() }
func (e *fppCommandInternalError) Unwrap() error { return e.err }

// FPPCommandDispatcher is the in-process, exported entry point onto
// [handlers.dispatchFPPCommand] — the SAME dispatch/confirm/audit core
// POST /api/v1/fpp/{instanceId}/commands uses over HTTP. A caller in
// another package, in the same coordinator process, already authorized and
// already holding a resolved principal (Step 9's macro executor,
// internal/coordinator/macro), uses this instead of issuing itself an HTTP
// request against its own coordinator — see this file's own top comment
// for why that alternative was rejected.
type FPPCommandDispatcher struct {
	h *handlers
}

// NewFPPCommandDispatcher builds a [FPPCommandDispatcher] from deps and
// opts, applying the identical defaulting [New] itself applies
// ([Dependencies.withDefaults]/[Options.withDefaults]) before building its
// own *handlers. Calling this AND [New] with the SAME deps/opts is the
// supported pattern for a coordinator wiring both this API's HTTP surface
// and Step 9's macro executor: both then dispatch through the identical
// core, and — because neither [handlers.dispatchFPPCommand] nor anything
// it calls reads or writes handlers.discoveryRunInFlight or
// handlers.loginLimiter (the only two *handlers fields this constructor
// does not set) — two independently-constructed *handlers values sharing
// one Dependencies behave identically to one shared value for every
// purpose this type exists for.
//
// This constructor does not live in api.go's [New] because building one
// there would require exporting *handlers itself (New's own doc comment:
// "[New] is the only supported way to build one, so that Options' zero-
// value defaults... are always applied" — an exported *handlers would let
// a caller sidestep that), or adding a field to [API] naming a type this
// package's HTTP route table has no use for. Duplicating the ~6-line
// construction here, in this package's own Step 9 seam, was judged cheaper
// than either — see this task's own report for the tradeoff.
func NewFPPCommandDispatcher(deps Dependencies, opts Options) *FPPCommandDispatcher {
	deps = deps.withDefaults()
	opts = opts.withDefaults()
	return &FPPCommandDispatcher{h: &handlers{
		deps:                      deps,
		clock:                     opts.Clock,
		logger:                    opts.Logger,
		fppCommandConfirmDeadline: opts.FPPCommandConfirmDeadline,
		fppCommandPollInterval:    opts.FPPCommandPollInterval,
	}}
}

// Dispatch runs one FPP primitive in-process, through the exact same
// dispatch/confirm/audit core the HTTP handler uses. The returned
// *v1.Problem is a caller-facing refusal (bad action, bad params, a busy
// guard, a replay conflict, an audit-store failure on a non-exempt
// primitive, ...) — the same class of thing [handlers.handleFPPCommand]
// would answer with a 4xx/503; err is this coordinator's own dependency
// failing (a store error, an unreachable observation source), the same
// class [handlers.writeInternalError] answers with a 500. Exactly one of
// (a non-empty FPPCommandOutcome.CommandID, problem, err) is meaningful on
// return — never more than one signals success.
func (d *FPPCommandDispatcher) Dispatch(ctx context.Context, in FPPCommandInput) (FPPCommandOutcome, *v1.Problem, error) {
	return d.h.dispatchFPPCommand(ctx, d.h.now(), in)
}

// FPPCommandSafetyClass is the exported mirror of [fppSafetyClass]'s two
// meaningful members — never [fppSafetyClassUndeclared], which cannot
// occur here: [FPPCommandSafetyClassForAction] only ever returns a value
// read from a registry entry [fppCommandPrimitives_test.go]'s own
// TestEveryFPPCommandPrimitiveDeclaresSafetyClass already guarantees is
// never left undeclared.
type FPPCommandSafetyClass int

const (
	// FPPCommandSafetyClassNotExempt is every primitive except the two
	// ADR-024 decision 11 names — see [fppSafetyClass]'s own doc comment
	// for the full membership reasoning, unchanged and un-duplicated here.
	FPPCommandSafetyClassNotExempt FPPCommandSafetyClass = iota
	// FPPCommandSafetyClassExempt is stopPlaylist and
	// stopPlaylistGracefully: an audit-write failure degrades attribution
	// rather than refusing the command.
	FPPCommandSafetyClassExempt
)

// FPPCommandSafetyClassForAction reports action's declared ADR-024
// decision 11 safety class, without exposing [fppCommandPrimitives] (the
// private registry) itself. This is what Step 9's macro executor needs for
// ADR-031 decision 5 as accepted: "A step's own action decides whether
// that step is exempt." The exemption is evaluated per step, not per run:
// decision 5's own record quotes and rejects a draft that made a run
// exempt if any one step was ("a stop step becomes a laundering
// mechanism"), so a caller uses this function once per step, at that
// step's own dispatch decision, never aggregated across a run to produce
// one run-wide class. Computing a step's class requires knowing it before
// that step dispatches, which means BEFORE calling
// [FPPCommandDispatcher.Dispatch] at all — a fact [FPPCommandOutcome]
// (a POST-dispatch result) cannot supply. ok is false when action is not
// one of this coordinator's registered wire actions, mirroring
// [FPPCommandDispatcher.Dispatch]'s own "unsupported action" refusal
// rather than panicking or guessing a class for a name this registry does
// not recognize.
func FPPCommandSafetyClassForAction(action string) (class FPPCommandSafetyClass, ok bool) {
	p, ok := fppPrimitivesByWireAction[action]
	if !ok {
		return 0, false
	}
	if p.SafetyClass == fppSafetyClassExempt {
		return FPPCommandSafetyClassExempt, true
	}
	return FPPCommandSafetyClassNotExempt, true
}

// FPPCommandDecision11Class is ADR-024 decision 11's own four-member
// safety-class vocabulary, in the exact wire form STEP-9-SPEC.md section
// 5.3's show.action.safetyClass field uses: "none", "blackout", "stop",
// "powerOff", and no fifth value. Kept distinct from [FPPCommandSafetyClass]
// (this file's own pre-existing, narrower exempt/not-exempt split) rather
// than replacing it: that type answers "does an audit-write failure
// refuse this dispatch", which only ever needs two values for an FPP
// primitive; this type answers a different question a config layer
// consuming show.action needs answered in the SAME vocabulary an
// operator-declared external action already uses, so the two can be
// compared directly instead of through a second translation.
type FPPCommandDecision11Class string

const (
	FPPCommandDecision11ClassNone     FPPCommandDecision11Class = "none"
	FPPCommandDecision11ClassBlackout FPPCommandDecision11Class = "blackout"
	FPPCommandDecision11ClassStop     FPPCommandDecision11Class = "stop"
	FPPCommandDecision11ClassPowerOff FPPCommandDecision11Class = "powerOff"
)

// FPPCommandDecision11ClassForAction reports action's registered safety
// class in [FPPCommandDecision11Class]'s wire vocabulary, so STEP-9-
// SPEC.md section 5.3's write-time rule ("safetyClass must agree with the
// primitive's own registered class... rejected... if it does not") is one
// direct comparison against a show.action's declared safetyClass field,
// never a second, hand-maintained mapping between two different
// enumerations of the same fact that could drift from this package's own
// registry. ok is false under the identical condition
// [FPPCommandSafetyClassForAction] reports it for: action names no
// registered primitive.
//
// This function can only ever return [FPPCommandDecision11ClassNone] or
// [FPPCommandDecision11ClassStop] for an FPP action, never Blackout or
// PowerOff. That is NOT a fact about the registry data this function
// happens to read today; it is a hard limit of the private
// [fppSafetyClass] type underneath it, which has only two meaningful
// members (exempt, not-exempt) and has no way to represent a third or
// fourth decision-11 class. Registering a future primitive against a
// different member does not make this function report it correctly: no
// such member exists to register against.
//
// So: before any FPP primitive whose true decision-11 class is blackout
// or power-off is added (a matrix blackout primitive, say), widen
// [fppSafetyClass] to decision 11's full four-member vocabulary first,
// and update every place that switches on it, including this function and
// [FPPCommandSafetyClassForAction]. Registering such a primitive against
// fppSafetyClassExempt as a stand-in, without doing that widening, makes
// this function silently report it as "stop": STEP-9-SPEC.md section
// 5.3's write-time agreement check then rejects any show.action correctly
// declaring "blackout" for it, and the remedy that rejection implies
// (declare "stop" instead) puts a wrong safety class into configuration.
//
// That mistake does not depend on anyone reading this comment.
// TestFPPCommandSafetyClassMembershipIsExactlyStopPlaylistPair walks every
// registered primitive and fails if any primitive other than the two stops
// is registered exempt, so the stand-in registration described above lands
// as a red test rather than a silent misreport. This comment explains why
// that test is failing; the test is what stops it shipping.
func FPPCommandDecision11ClassForAction(action string) (class FPPCommandDecision11Class, ok bool) {
	p, ok := fppPrimitivesByWireAction[action]
	if !ok {
		return "", false
	}
	if p.SafetyClass == fppSafetyClassExempt {
		return FPPCommandDecision11ClassStop, true
	}
	return FPPCommandDecision11ClassNone, true
}

// FPPCommandDecision11Exempt reports whether class is one of ADR-024
// decision 11's three named exempt members (blackout, stop, power-off) -
// [FPPCommandDecision11ClassNone] is the vocabulary's only non-exempt
// value, and anything outside the four declared constants (including the
// empty string) is treated as not exempt: this function fails closed on
// an unrecognized value rather than guessing, matching every other
// safety-class decision in this package.
func FPPCommandDecision11Exempt(class FPPCommandDecision11Class) bool {
	switch class {
	case FPPCommandDecision11ClassBlackout, FPPCommandDecision11ClassStop, FPPCommandDecision11ClassPowerOff:
		return true
	default:
		return false
	}
}

// fppCommandAuditRunParams builds the [identity.AuditEntry.Params] value
// naming which macro run caused a dispatch, outcome, or replay audit
// entry - see [FPPCommandIssuer.RunID]'s own doc comment for why this is
// carried in Params rather than a dedicated column. Returns nil (not an
// empty, present map) for an empty runID, so an ordinary HTTP-dispatched
// command's audit entries encode no params at all, matching every
// pre-Step-9 entry this package has ever written.
func fppCommandAuditRunParams(runID string) map[string]any {
	if runID == "" {
		return nil
	}
	return map[string]any{"runId": runID}
}

// dispatchFPPCommand is the ONE dispatch/confirm/audit core both
// [handlers.handleFPPCommand] (the wire adapter, fppcommand_handler.go)
// and [FPPCommandDispatcher.Dispatch] (this file) call. It reproduces that
// handler's own numbered steps exactly — see this file's own top comment
// and fppcommand_handler.go's [handlers.handleFPPCommand] doc comment for
// the full reasoning behind each one; only the numbering below is
// restated, not re-derived:
//
//  1. Resolve in.Action against the registry, validate in.InstanceID's
//     syntax, run the primitive's own ValidateParams, validate the
//     idempotency key, and canonicalize params
//     ([canonicalParamsJSON]) — needed below to tell a true replay apart
//     from a params conflict before anything else runs. Any failure here
//     dispatches nothing and stores nothing.
//  2. Look up the target FPP instance (404 if unconfigured).
//  3. Look up the idempotency key BEFORE anything decides whether this
//     request is even allowed to proceed — idempotency-first (Step 8
//     review finding 4): a HIT is answered via
//     [handlers.resolveFPPCommandReplay] WITHOUT ever running step 4's
//     guard.
//  4. Run the primitive's own PreDispatchCheck, if it has one. A refusal
//     here dispatches nothing and stores nothing.
//  5. Mint the command id, insert the commands row, record desired state,
//     and write the DISPATCH audit entry BEFORE dispatching, ALL IN ONE
//     TRANSACTION when the audit store is healthy (Step 7 seam C review
//     defect 8). On an AUDIT-WRITE failure specifically, the primitive's
//     own SafetyClass decides: exempt proceeds with degraded attribution;
//     not-exempt fails closed with a 503-class problem.
//  6. Capture any pre-dispatch baseline, then dispatch to FPP — OUTSIDE
//     any transaction and, as of Step 8 review finding 14, on
//     context.WithoutCancel(ctx) (bgCtx) rather than ctx itself: a caller
//     walking away (an abandoned HTTP client, or a macro run cancelled by
//     coordinator shutdown) must not be able to abort an in-flight
//     dispatch or its bookkeeping.
//  7. On a successful dispatch, best-effort nudge the FPP collector, then
//     confirm by evidence against the primitive's own deadline, and write
//     the OUTCOME as a separate, correlated audit entry.
//
// now is the caller's own h.now()-derived instant, read once by the
// caller (not re-read here) so a caller with its own pre-existing "now"
// (the wire adapter's own write-deadline calculation) agrees with this
// call about what it means — matching [handlers.confirmFPPCommand]'s
// identical existing contract for its own now parameter.
func (h *handlers) dispatchFPPCommand(ctx context.Context, now time.Time, in FPPCommandInput) (FPPCommandOutcome, *v1.Problem, error) {
	// --- 1. Resolve, validate, canonicalize. ---

	primitive, ok := fppPrimitivesByWireAction[in.Action]
	if !ok {
		p := invalidParameterProblem(fmt.Sprintf(
			"unsupported action %q; this coordinator supports: %s", in.Action, fppQuotedWireActions()))
		return FPPCommandOutcome{}, &p, nil
	}

	if err := mqttproto.ValidateNodeID(in.InstanceID); err != nil {
		p := invalidParameterProblem("instanceId is not a syntactically valid instance ID: " + err.Error())
		return FPPCommandOutcome{}, &p, nil
	}

	if primitive.ValidateParams != nil {
		if err := primitive.ValidateParams(in.Params); err != nil {
			p := invalidParameterProblem(err.Error())
			return FPPCommandOutcome{}, &p, nil
		}
	}

	if err := command.ValidateIdempotencyKey(in.IdempotencyKey); err != nil {
		p := invalidParameterProblem("idempotencyKey: " + err.Error())
		return FPPCommandOutcome{}, &p, nil
	}

	paramsJSON, err := canonicalParamsJSON(in.Params)
	if err != nil {
		return FPPCommandOutcome{}, nil, &fppCommandInternalError{"canonicalize fpp command params", err}
	}

	// --- 2. Look up the target FPP instance. ---

	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		return FPPCommandOutcome{}, nil, &fppCommandInternalError{"list fpp instances for command dispatch", err}
	}
	var target *FPPInstanceView
	for i := range views {
		if views[i].InstanceID == in.InstanceID {
			target = &views[i]
			break
		}
	}
	if target == nil {
		p := resourceNotFoundProblem("no FPP instance with id " + strconv.Quote(in.InstanceID) + " is configured")
		return FPPCommandOutcome{}, &p, nil
	}

	// --- 3. Idempotency-first: a hit is answered as a replay (or a
	// conflict) WITHOUT ever running step 4's guard. ---

	existing, lookupErr := h.deps.Commands.GetCommandByIdempotencyKey(ctx, in.IdempotencyKey)
	switch {
	case lookupErr == nil:
		outcome, problem := h.resolveFPPCommandReplay(ctx, now, in.Issuer, existing, in.InstanceID, primitive.AuditAction, paramsJSON)
		return outcome, problem, nil
	case errors.Is(lookupErr, store.ErrCommandNotFound):
		// Genuinely new key — fall through to the guard and the insert.
	default:
		return FPPCommandOutcome{}, nil, &fppCommandInternalError{"look up fpp command by idempotency key", lookupErr}
	}

	// --- 4. The primitive's own pre-dispatch guard, if it has one. ---

	var ifNotRunning bool
	if primitive.PreDispatchCheck != nil {
		var refusal *v1.Problem
		ifNotRunning, refusal = primitive.PreDispatchCheck(ctx, h.deps.Observations, in.InstanceID, in.Params, now)
		if refusal != nil {
			return FPPCommandOutcome{}, refusal, nil
		}
	}

	env := command.Envelope{
		ID:                 uuid.NewString(),
		IdempotencyKey:     in.IdempotencyKey,
		Action:             primitive.AuditAction,
		Target:             command.Target{Kind: string(observation.ResourceFPP), ID: in.InstanceID},
		Params:             in.Params,
		Issuer:             command.Issuer{PrincipalID: in.Issuer.PrincipalID, PrincipalName: in.Issuer.PrincipalName},
		ConfirmationMethod: command.ConfirmationEvidence,
		RequestedRevision:  in.RequestedRevision,
	}
	deadline := now.Add(primitive.ConfirmDeadline(h.fppCommandConfirmDeadline))
	env.Deadline = &deadline

	dispatchEntry := identity.AuditEntry{
		Timestamp: now, PrincipalID: in.Issuer.PrincipalID, PrincipalName: in.Issuer.PrincipalName,
		Form: in.Issuer.Form, CredentialID: in.Issuer.CredentialID, ClientAddr: in.Issuer.ClientAddr,
		Action: env.Action, Target: in.InstanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditDispatch, CommandID: env.ID,
		Params: fppCommandAuditRunParams(in.Issuer.RunID),
	}

	// --- 5. Insert, desired state, dispatch audit entry — atomically when
	// the audit store is healthy. ---

	var dispatchDegraded bool
	auditErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, err := tx.InsertCommand(ctx, fppCommandRecordFor(env, paramsJSON)); err != nil {
			return identity.AuditEntry{}, err
		}
		h.setFPPCommandDesiredState(ctx, tx, primitive, env, now, in.Params)
		return dispatchEntry, nil
	})

	var dup *store.DuplicateCommandError
	switch {
	case errors.As(auditErr, &dup):
		outcome, problem := h.resolveFPPCommandReplay(ctx, now, in.Issuer, dup.Existing, in.InstanceID, env.Action, paramsJSON)
		return outcome, problem, nil
	case errors.Is(auditErr, identity.ErrAuditWrite):
		if primitive.SafetyClass != fppSafetyClassExempt && !in.NeverWithholdOnAuditFailure {
			// Fail closed: the transaction above already rolled back in
			// full, so nothing is re-inserted and nothing is dispatched.
			// See [FPPCommandInput.NeverWithholdOnAuditFailure] for the one
			// caller that turns this branch off and the owner decision
			// behind it.
			p := fppCommandAuditUnavailableProblem(primitive.WireAction, auditErr)
			return FPPCommandOutcome{}, &p, nil
		}
		// Safety-class exemption (ADR-024 decision 11): redo the insert
		// and desired-state writes through the plain, non-transactional
		// store methods, and proceed with degraded attribution. Still on
		// ctx (not bgCtx — that detachment does not exist yet at this
		// point in the sequence, matching fppcommand_handler.go's own
		// pre-refactor ordering exactly).
		rec, err := h.deps.Commands.InsertCommand(ctx, fppCommandRecordFor(env, paramsJSON))
		if errors.As(err, &dup) {
			outcome, problem := h.resolveFPPCommandReplay(ctx, now, in.Issuer, dup.Existing, in.InstanceID, env.Action, paramsJSON)
			return outcome, problem, nil
		}
		if err != nil {
			return FPPCommandOutcome{}, nil, &fppCommandInternalError{"insert fpp command", err}
		}
		_ = rec
		h.setFPPCommandDesiredStateNonTx(ctx, primitive, env, now, in.Params)
		degradedReason := degradedAttributionReasonSafetyClassExemption
		if primitive.SafetyClass != fppSafetyClassExempt {
			// Reached only via NeverWithholdOnAuditFailure: this step's own
			// safety class would have failed closed on its own.
			degradedReason = degradedAttributionReasonMacroRunNeverWithheld
		}
		h.reportDegradedAttribution(now, dispatchEntry, auditErr, degradedReason)
		dispatchDegraded = true
	case auditErr != nil:
		return FPPCommandOutcome{}, nil, &fppCommandInternalError{"insert fpp command", auditErr}
	}

	// --- 6. Capture any pre-dispatch baseline, then dispatch — outside
	// any transaction and on bgCtx, which carries none of ctx's
	// cancellation from this point forward (Step 7 seam C review defect
	// 4; Step 8 review finding 14 moved the cutover to before the
	// dispatch ATTEMPT itself). ---

	bgCtx := context.WithoutCancel(ctx)

	dispatchAttemptedAt := h.now()
	var baseline fppBaseline
	if primitive.CaptureBaseline != nil {
		baseline = primitive.CaptureBaseline(bgCtx, h.deps.Observations, in.InstanceID, dispatchAttemptedAt)
	}

	var (
		dispatchOutcome fppcommand.Outcome
		dispatchErr     error
		dispatchedAt    *time.Time // nil unless dispatch was actually ATTEMPTED
	)
	client, cerr := fppcommand.New(target.Endpoint, fppcommand.Options{})
	if cerr != nil {
		dispatchErr = fmt.Errorf("building fpp command client: %w", cerr)
	} else {
		dispatchedAt = &dispatchAttemptedAt
		dispatchOutcome, dispatchErr = primitive.Dispatch(bgCtx, client, in.Params, ifNotRunning)
	}

	dispatchState := "dispatched"
	prelimResult, _ := json.Marshal(commandResultPayload{StatusCode: dispatchOutcome.StatusCode, Body: dispatchOutcome.Body})
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
		// Post-dispatch poll nudge — best-effort, rate-limited; its bool
		// return is deliberately not consulted here (see
		// [FPPPollNudger]'s own doc comment). confirmFPPCommand below
		// reads the exact same [ObservationLister] through the exact same
		// notBefore fence either way.
		h.deps.Nudger.NudgePoll(in.InstanceID)

		confirmed, outcomeState, outcomeReason = h.confirmFPPCommand(bgCtx, primitive, in.InstanceID, in.Params, baseline, dispatchAttemptedAt)
	}

	outcomeWord := "unconfirmed"
	if confirmed {
		outcomeWord = "confirmed"
	}

	resolvedAt := h.now()
	resolvedState := "resolved"
	finalResult, _ := json.Marshal(commandResultPayload{Outcome: outcomeWord, StatusCode: dispatchOutcome.StatusCode, Body: dispatchOutcome.Body})
	finalResultStr := string(finalResult)
	if err := h.updateCommandOutcomeBounded(bgCtx, env.ID, store.CommandOutcomeUpdate{
		ResolvedAt: &resolvedAt, State: &resolvedState, ResultJSON: &finalResultStr,
		OutcomeState: &outcomeState, OutcomeReason: &outcomeReason,
	}); err != nil {
		h.logWarn("failed to record fpp command outcome", "commandId", env.ID, "error", err)
	}

	// --- Outcome audit entry: a SEPARATE, correlated entry, best-effort
	// for EVERY primitive regardless of SafetyClass — see
	// [degradedAttributionReasonPostDispatch]'s own doc comment
	// (fppcommand_handler.go) for why this one is never refused. ---

	outcomeDegraded := h.writeBestEffortAuditBounded(bgCtx, resolvedAt, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: resolvedAt, PrincipalID: in.Issuer.PrincipalID, PrincipalName: in.Issuer.PrincipalName,
		Form: in.Issuer.Form, CredentialID: in.Issuer.CredentialID, ClientAddr: in.Issuer.ClientAddr,
		Action: env.Action, Target: in.InstanceID, IdempotencyKey: env.IdempotencyKey,
		Kind: identity.AuditOutcome, CommandID: env.ID,
		Outcome: outcomeWord, OutcomeState: outcomeState, OutcomeReason: outcomeReason,
		Params: fppCommandAuditRunParams(in.Issuer.RunID),
	})

	return FPPCommandOutcome{
		CommandID: env.ID, IdempotencyKey: env.IdempotencyKey, Action: env.Action, InstanceID: in.InstanceID,
		Params:              in.Params,
		Replay:              false,
		Outcome:             outcomeWord,
		OutcomeState:        outcomeState,
		OutcomeReason:       outcomeReason,
		AttributionDegraded: dispatchDegraded || outcomeDegraded,
		DispatchedAt:        utcTimePtr(dispatchedAt),
		ResolvedAt:          timePtr(resolvedAt.UTC()),
		DispatchFailed:      dispatchErr != nil,
	}, nil, nil
}

// resolveFPPCommandReplay answers a replayed idempotency key: NOTHING is
// dispatched, and existing's own already-recorded result is returned
// verbatim — ADR-024 decision 11: "a replay is precisely the case an
// investigator wants to see, because it means the operator did not get
// their response." A replay audit entry is written best-effort, for EVERY
// primitive regardless of SafetyClass, on ctx (NOT a detached context —
// there is nothing in flight to protect from cancellation here: a replay
// dispatches nothing, by construction, matching this function's
// pre-refactor behavior in fppcommand_handler.go exactly).
//
// requestedAction is this request's own internal action identifier (e.g.
// "fpp.stop_playlist"), never the wire action. Step 7 seam C review defect
// 6: idempotencyKey alone is not enough — a key reused against a DIFFERENT
// action or target is a CONFLICT, not a replay, checked FIRST.
// requestedParamsJSON is Step 8's own extension (canonical — see
// [canonicalParamsJSON]): the SAME key against the SAME action and target
// but DIFFERENT normalized params is ALSO a conflict, never a replay.
//
// existing.Outcome (decoded from existing.ResultJSON) may be empty in one
// narrow, accepted race between two concurrent requests presenting the
// SAME idempotency key: two calls into [store.Store.InsertCommand] at
// nearly the same instant, SQLite's single-writer serialization picks
// exactly one winner, and the loser can observe the winner's row before
// the winner's own dispatch has finished confirming. This function does
// not wait for that: it returns whatever the row honestly holds right now
// (Outcome/OutcomeState/OutcomeReason empty, DispatchedAt/ResolvedAt nil),
// per ADR-020's "absence of evidence is stated, never omitted" — an empty
// Outcome is not a bug, it is the true, current state of a command still
// in flight. See fppcommand_reconcile.go's own doc comment for why a
// startup sweep is what keeps this race distinguishable from the
// permanent blankness a coordinator restart could otherwise leave behind.
// Unchanged by this refactor from its pre-Step-9 behavior.
func (h *handlers) resolveFPPCommandReplay(ctx context.Context, now time.Time, issuer FPPCommandIssuer, existing store.CommandRecord, instanceID, requestedAction, requestedParamsJSON string) (FPPCommandOutcome, *v1.Problem) {
	if existing.Action != requestedAction || existing.TargetID != instanceID {
		p := fppCommandReplayConflictProblem(existing.ID, existing.Action, existing.TargetID, requestedAction, instanceID)
		return FPPCommandOutcome{}, &p
	}
	existingParamsJSON := existing.ParamsJSON
	if existingParamsJSON == "" {
		existingParamsJSON = "{}"
	}
	if existingParamsJSON != requestedParamsJSON {
		p := fppCommandReplayParamsConflictProblem(existing.ID, existing.Action, existing.TargetID, existingParamsJSON, requestedParamsJSON)
		return FPPCommandOutcome{}, &p
	}

	degraded := h.writeBestEffortAudit(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: issuer.PrincipalID, PrincipalName: issuer.PrincipalName,
		Form: issuer.Form, CredentialID: issuer.CredentialID, ClientAddr: issuer.ClientAddr,
		Action: existing.Action, Target: instanceID, IdempotencyKey: existing.IdempotencyKey,
		Kind: identity.AuditReplay, CommandID: existing.ID,
		Params: fppCommandAuditRunParams(issuer.RunID),
	})

	var payload commandResultPayload
	_ = json.Unmarshal([]byte(existing.ResultJSON), &payload) // best-effort; "{}" or malformed decodes to the zero value

	var replayParams map[string]any
	_ = json.Unmarshal([]byte(existingParamsJSON), &replayParams) // canonical JSON always decodes; best-effort regardless
	if replayParams == nil {
		replayParams = map[string]any{}
	}

	return FPPCommandOutcome{
		CommandID: existing.ID, IdempotencyKey: existing.IdempotencyKey, Action: existing.Action, InstanceID: instanceID,
		Params:              replayParams,
		Replay:              true,
		Outcome:             payload.Outcome,
		OutcomeState:        existing.OutcomeState,
		OutcomeReason:       existing.OutcomeReason,
		AttributionDegraded: degraded,
		DispatchedAt:        existing.DispatchedAt,
		ResolvedAt:          existing.ResolvedAt,
	}, nil
}
