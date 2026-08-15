package api

import (
	"context"
	"time"
)

// This file declares this package's own consumer-side view of Track D
// seam D-3/A's action engine (internal/coordinator/collector/resolume,
// built concurrently by a different builder against
// docs/build/TRACK-D-D3-SPEC.md sections 2-4) — the same pattern
// interfaces.go already established for [FPPInstanceView]/[FPPLister] and
// [EventRecord]/[EventReader]: this package does not import a producer
// package that is being built in parallel, or that this package does not
// own the production wiring of; it declares the narrow shape it needs, and
// a later wiring task (or a thin adapter, if D-3/A's real return types do
// not already satisfy this shape verbatim) joins the two.
//
// This package's own job (D-3/B) ends at "decode the wire request,
// authorize it, record it durably per ADR-024 decision 11, hand it to
// [ResolumeActionDispatcher.Dispatch], and render the wire response
// honestly." Everything about HOW an action is dispatched — the derived
// per-action deadline, the pre-dispatch baseline, the deck refusal
// (TRACK-D-D3-SPEC.md section 3.4), composition-identity gating (section
// 3.6), and confirmation itself — is D-3/A's, reached only through this
// interface.

// ResolumeActionParamKind is the wire JSON type one action parameter's
// value must decode as — a ShowMesh object reference (a string), a
// boolean parameter value (setLayerBypass), or a continuous numeric
// parameter value (setLayerMaster). Deliberately narrower than
// fppcommand_primitives.go's fppParamKind (which also has an int kind no
// Resolume action in this vocabulary needs).
//
// ResolumeActionParamNumber was added 2026-08-15 (defect 3): setLayerMaster
// was originally shipped as a boolean, true/false mapped to Arena's own
// RangeParameter endpoints 1.0/0.0, because master's own wire contract had
// not been settled when D-3/A and D-3/B were built concurrently against
// disagreeing assumptions. A layer master that can only be 0 or 1 is not a
// master — dialling a layer to 40% is an ordinary operation the boolean
// shape could never express — so the wire contract now carries the real
// number through end to end. Nothing had shipped, so there is no
// compatibility obligation to a boolean "master" that ever reached a real
// client.
type ResolumeActionParamKind string

const (
	ResolumeActionParamString ResolumeActionParamKind = "string"
	ResolumeActionParamBool   ResolumeActionParamKind = "bool"
	ResolumeActionParamNumber ResolumeActionParamKind = "number"
)

// ResolumeActionParam describes one named parameter one action's "params"
// object may carry. Every parameter in this seven-action vocabulary is
// required — spec section 2 has no action with an optional parameter and a
// default to fall back to — so, unlike fppcommand_primitives.go's
// fppParamDef, this type carries no Default field: an absent required
// parameter is always a 400, never silently defaulted.
type ResolumeActionParam struct {
	Name     string
	Kind     ResolumeActionParamKind
	Required bool
}

// ResolumeActionDescriptor is one entry of TRACK-D-D3-SPEC.md section 2's
// seven-action vocabulary, as this package needs it to render
// GET /resolume/actions and to decide the ADR-024 decision 11 audit rule
// BEFORE ever calling Dispatch — the safety class has to be known
// pre-dispatch (fail closed vs. proceed degraded), and [Dispatch] itself
// only runs after that decision is already made, so it cannot be the
// thing that reports it.
type ResolumeActionDescriptor struct {
	// Name is this action's wire name (e.g. "launchClip"), matching one of
	// TRACK-D-D3-SPEC.md section 2's seven rows exactly.
	Name string

	// Params is this action's own parameter vocabulary — empty for
	// blackout, the one zero-parameter action in this vocabulary.
	Params []ResolumeActionParam

	// AuditExempt reports whether this action is a member of ADR-024
	// decision 11's safety class (TRACK-D-D3-SPEC.md section 5.2):
	// blackout and clearLayer are exempt (an audit-write failure degrades
	// attribution but never refuses them); the other five are not (an
	// audit-write failure refuses them before dispatch, dispatching
	// nothing). This package trusts the value it is handed here — it is
	// D-3/A's own registry that must never leave a member undeclared; see
	// this seam's own report for how that undeclared-zero-value hazard
	// (fppSafetyClassUndeclared's own documented failure mode) is guarded
	// on that side.
	AuditExempt bool

	// CoordinatorRequired is carried on the wire (TRACK-D-D3-SPEC.md
	// section 5.3) rather than assumed by a caller: the Resolume adapter is
	// coordinator-hosted and Resolume holds no local fallback, so every
	// action in this vocabulary is coordinator-required today. Structural,
	// not decorative — a macro author reads it off the API rather than
	// having to know Resolume's own architecture out of band.
	CoordinatorRequired bool
}

// ResolumeActionOutcome is TRACK-D-D3-SPEC.md section 4/7's fixed,
// five-member outcome vocabulary. Every value here reaches the wire inside
// a 200 response (v1.ResolumeActionResult.Outcome) — never as an HTTP
// error status — because a 200 here reports "this coordinator answered
// honestly about what happened," not "the action succeeded." Widening
// this set is a spec change, not a call-site decision.
type ResolumeActionOutcome string

const (
	// ResolumeOutcomeConfirmed: the action's effect was observed on
	// evidence collected strictly after dispatch (section 4.1).
	ResolumeOutcomeConfirmed ResolumeActionOutcome = "confirmed"

	// ResolumeOutcomeUnconfirmed: the action's own derived deadline
	// (section 3.3) expired before confirming evidence arrived. Never
	// "failed" — this is a claim about this coordinator's own evidence
	// pipeline, not about the show (section 3.3's own closing rule).
	ResolumeOutcomeUnconfirmed ResolumeActionOutcome = "unconfirmed"

	// ResolumeOutcomeUnconfirmable: the action was dispatched, but its own
	// effect could not be told apart from the pre-dispatch state (section
	// 3.5 — an already-playing clip is the named case) or the confirming
	// predicate could not be evaluated for a reason unrelated to a
	// deadline. Not an error, and must never render as one (ADR-029: an
	// action whose effect cannot be observed reports as unconfirmable,
	// never as success — a step that always reports success is worse than
	// no step at all).
	ResolumeOutcomeUnconfirmable ResolumeActionOutcome = "unconfirmable"

	// ResolumeOutcomeRefused: the action was refused before dispatch —
	// a clip's deck was not selected (section 3.4), the composition
	// identity was unknown or false (section 3.6), or an audit-write
	// failure refused a non-exempt action (section 5.2). Dispatched is
	// false whenever Outcome is this value.
	ResolumeOutcomeRefused ResolumeActionOutcome = "refused"

	// ResolumeOutcomeFailed: dispatch was attempted and the attempt itself
	// failed (a transport error, an unexpected response from Resolume) —
	// distinct from ResolumeOutcomeUnconfirmed, which means dispatch
	// succeeded and only confirmation timed out.
	ResolumeOutcomeFailed ResolumeActionOutcome = "failed"
)

// ResolumeActionResult is one dispatched (or refused) action's outcome, as
// this package needs it from [ResolumeActionDispatcher.Dispatch] to build
// the wire response and the post-dispatch audit/bookkeeping entries.
type ResolumeActionResult struct {
	Outcome ResolumeActionOutcome

	// Reason is a short, human-readable explanation, non-empty for every
	// value of Outcome — see [ResolumeActionOutcome]'s own doc comment: a
	// bare "confirmed" with no evidence stated is exactly the shape Step 7
	// and Step 8 both found defects in.
	Reason string

	// Dispatched reports whether an HTTP request against Resolume was ever
	// issued. False for every ResolumeOutcomeRefused result (nothing was
	// sent — TRACK-D-D3-SPEC.md acceptance criterion 3: "issuing no HTTP
	// request to Resolume at all"); true otherwise. This package does not
	// itself verify this claim against real traffic — that proof belongs
	// to D-3/A's own tests, against the concrete client — but carries the
	// field through to its own bookkeeping (DispatchedAt is nil exactly
	// when this is false) rather than inferring it from Outcome alone,
	// since ResolumeOutcomeFailed can be true (a dispatch was attempted
	// and failed) while ResolumeOutcomeRefused is always false.
	Dispatched bool

	// DispatchedAt and ResolvedAt are nil-safe timestamps — nil exactly
	// when Dispatched is false (DispatchedAt) or when this result could not
	// be resolved at all (ResolvedAt, not expected in practice: every
	// dispatch this package's handler waits for synchronously resolves
	// before returning).
	DispatchedAt *time.Time
	ResolvedAt   *time.Time
}

// ResolumeActionDispatcher is what this package needs from D-3/A's action
// engine: enumerate the vocabulary (for GET /resolume/actions and for this
// package's own pre-dispatch decoding/validation against each action's
// declared parameters), and dispatch one action by name, already
// normalized, returning a structured outcome per
// TRACK-D-D3-SPEC.md sections 3-4.
//
// A nil field in [Dependencies] is replaced by [noResolumeActionDispatcher]
// (resolumeaction.go), matching every other dependency in this package's
// standing "an unwired dependency is not this API failing" posture.
type ResolumeActionDispatcher interface {
	// Actions returns the fixed vocabulary, in any order. This package
	// calls it per request rather than caching it once: the data is small
	// (seven entries) and static per coordinator build, so the cost of
	// calling it every time is not worth the risk of this package holding
	// a stale copy across a hot-reloadable implementation D-3/A might
	// build later.
	Actions() []ResolumeActionDescriptor

	// Dispatch runs one action, already resolved against [Actions] and
	// already parameter-validated by this package's own decode step,
	// through D-3/A's own dispatch/confirm core (the derived deadline, the
	// pre-dispatch baseline, the deck refusal, and confirmation — none of
	// which this package re-implements or second-guesses). params carries
	// already-normalized, natively-typed values (string, bool), matching
	// [FPPCommandInput.Params]'s identical "not raw JSON" contract
	// (fppcommand_dispatch.go) for the identical reason: this package's
	// own decode step has already resolved the wire absent/null/empty
	// distinction before this method is ever called.
	//
	// The returned error is a dependency failure this package's own caller
	// answers with a 500 (an internal error unrelated to what the
	// principal asked for) — never a caller-facing refusal, which is
	// reported through [ResolumeActionResult.Outcome] instead (exactly the
	// same split [FPPCommandDispatcher.Dispatch]'s own doc comment
	// documents for its (outcome, problem, err) triple, narrowed here to
	// (result, err) because every REFUSAL this seam can produce — deck
	// mismatch, unknown composition identity — is D-3/A's own domain
	// decision, expressed as [ResolumeOutcomeRefused] rather than as a
	// *v1.Problem this interface would otherwise have to carry).
	Dispatch(ctx context.Context, action string, params map[string]any, now time.Time) (ResolumeActionResult, error)
}
