package resolume

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// This file is Track D seam D-3's own deliverable: TRACK-D-D3-SPEC.md §2's
// seven-action vocabulary — the first code in this package permitted to
// change anything on the wall. It follows internal/coordinator/api's own
// fppcommand_primitives.go/fppcommand_evidence.go precedent (Step 8) as
// closely as this package's own shape allows: a small declarative registry
// naming every action's safety class (§5.2), a shared dispatch/confirm
// orchestration every action runs through rather than seven hand-rolled
// copies of it, and a confirmation predicate that never trusts a dispatch
// response as evidence (§3.2) and never skips the post-dispatch fence
// (§4.1) or the pre-dispatch baseline (§4.2). action_dispatch.go holds the
// seven actions' own resolve/baseline/confirm logic; action_client.go holds
// the REST calls each one dispatches.
//
// # What this file deliberately does NOT do
//
// It never reads GET /composition, on any path — guardfullcomposition_test.go
// (D-2's own AST guard) scans every non-test file in this directory,
// including this one. It never recomputes composition identity or the
// selected deck: both pre-dispatch guards below read
// [Collector.LastSurveySnapshot], D-2's own cached result, and issue no
// HTTP request of their own to answer either question (§3.4, §3.6). It
// never reads a value out of the WebSocket — every confirmation read in
// this package is a fresh `by-id` GET through the exact same [Client]
// methods D-2 uses. And it never gates on Program Mode or Show Mode: ADR-033
// decision 4 is that no mode may refuse, delay, or degrade any action here,
// and this package reads no mode value at all.

// ActionName is one of TRACK-D-D3-SPEC.md §2's seven action names — the
// ENTIRE vocabulary this package accepts. `POST /composition/action`
// (undo/redo) and every `DELETE` are excluded outright, per that section's
// own "no action not in this table," and there is no eighth constant here
// to add without also updating that table.
type ActionName string

const (
	ActionLaunchClip     ActionName = "launchClip"
	ActionClearLayer     ActionName = "clearLayer"
	ActionBlackout       ActionName = "blackout"
	ActionLaunchColumn   ActionName = "launchColumn"
	ActionSelectDeck     ActionName = "selectDeck"
	ActionSetLayerBypass ActionName = "setLayerBypass"
	ActionSetLayerMaster ActionName = "setLayerMaster"
)

// ActionSafetyClass records, for one action, whether it is ADR-024 decision
// 11's own named exemption (proceeds on an audit-write failure with
// degraded attribution) or the default fail-closed rule (refused). Mirrors
// internal/coordinator/api's own fppSafetyClass exactly, including the
// reason its zero value is deliberately not a valid registry entry: a bare
// bool defaults to false with no way to tell "explicitly decided not
// exempt" from "nobody ever decided," which is how Step 8 inherited Step
// 7's one-primitive exemption onto all eight primitives unreviewed before
// that file's own review caught it. See [actionRegistry]'s own comment for
// this package's membership decision and TRACK-D-D3-SPEC.md §5.2 for the
// full reasoning, reproduced there and here rather than only in the spec:
// exempting setLayerBypass/setLayerMaster to protect the silencing
// direction would exempt the lighting direction with it, which is Step 8's
// exact shipped defect (a doc comment claimed one member was exempt while
// the code exempted all eight). The blackout path is [ActionBlackout], and
// it alone (with [ActionClearLayer], a stop scoped to one layer) is
// exempt — this package does NOT enforce that exemption itself
// (there is no audit write in this package at all); it only declares it,
// for D-3/B's handler to read and act on, the same division of labor
// fppcommand_primitives.go has with fppcommand_handler.go.
type ActionSafetyClass int

const (
	// ActionSafetyClassUndeclared is the zero value and is never a valid
	// registry entry — see this type's own doc comment.
	ActionSafetyClassUndeclared ActionSafetyClass = iota

	// ActionSafetyClassExempt: ADR-024 decision 11's blackout/stop/
	// power-off class. [ActionBlackout] and [ActionClearLayer] only.
	ActionSafetyClassExempt

	// ActionSafetyClassNotExempt: fails closed on a pre-dispatch
	// audit-write failure (D-3/B's own enforcement, not this package's).
	// Every action other than blackout and clearLayer.
	ActionSafetyClassNotExempt
)

// localFallbackClassCoordinatorRequired is every action's declared
// [ActionDescriptor.LocalFallbackClass] (TRACK-D-D3-SPEC.md §5.3, ADR-016):
// the Resolume adapter is coordinator-hosted, Resolume holds no fallback,
// and the composition is reachable only through the coordinator's own
// network path, so every action here is coordinator-required with no
// exception. The vocabulary supplies this label so a macro author never has
// to know it — this is the ONE value every [ActionDescriptor] in
// [actionRegistry] carries.
const localFallbackClassCoordinatorRequired = "coordinator-required"

// ActionDescriptor is one row of [actionRegistry]: the declarative metadata
// D-3/B (and, later, a macro author) needs about an action WITHOUT
// dispatching it — its safety class and its local-fallback class. It
// carries no dispatch logic; see action_dispatch.go for that.
type ActionDescriptor struct {
	Name               ActionName
	SafetyClass        ActionSafetyClass
	LocalFallbackClass string
}

// actionRegistry is TRACK-D-D3-SPEC.md §2's table in full, safety class per
// §5.2's own reasoning (see [ActionSafetyClass]'s doc comment). Every entry
// carries [localFallbackClassCoordinatorRequired] — §5.3 has no exceptions.
// This slice, and only this slice, is what [ActionDispatcher.Actions] and
// TestEveryActionDeclaresASafetyClass (action_test.go) walk; adding an
// eighth action anywhere else in this package without adding it here is a
// gap this package's own tests cannot see.
var actionRegistry = []ActionDescriptor{
	{Name: ActionLaunchClip, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionClearLayer, SafetyClass: ActionSafetyClassExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionBlackout, SafetyClass: ActionSafetyClassExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionLaunchColumn, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSelectDeck, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSetLayerBypass, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSetLayerMaster, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
}

// ActionOutcomeState is the five-way result TRACK-D-D3-SPEC.md's own
// vocabulary requires throughout §3-§4: never collapsed to a bool, and
// "confirmed" is never the default a caller falls back to when nothing else
// fits.
type ActionOutcomeState string

const (
	// ActionConfirmed: post-dispatch evidence, collected after this
	// command's own dispatch, positively matches the action's confirming
	// predicate.
	ActionConfirmed ActionOutcomeState = "confirmed"

	// ActionUnconfirmed: the action was dispatched, but no confirming
	// evidence arrived before its deadline. Never "failed" — §3.3's own
	// rule: a deadline expiring is a claim about ShowMesh's evidence
	// pipeline, not about the show.
	ActionUnconfirmed ActionOutcomeState = "unconfirmed"

	// ActionUnconfirmable: the action was dispatched, but its confirming
	// predicate already held true before dispatch (§3.5, §4.2), so no
	// post-dispatch evidence could ever prove this dispatch caused
	// anything. Never reported as ActionConfirmed.
	ActionUnconfirmable ActionOutcomeState = "unconfirmable"

	// ActionRefused: the action was NOT dispatched — a pre-dispatch guard
	// (composition identity, a clip's deck, an id absent from the stored
	// composition, or an unreadable pre-dispatch baseline) stopped it
	// before any HTTP request reached Resolume for the guards that
	// require zero requests (§3.4, §3.6), or before the WRITE request
	// specifically for a baseline-read failure.
	ActionRefused ActionOutcomeState = "refused"

	// ActionFailed: Resolume's own response to the dispatch request
	// itself was a definite negative — a non-2xx status or a transport
	// failure — so there is positive evidence this action did not
	// execute. A claim about the show, per §3.3's own distinction from
	// ActionUnconfirmed.
	ActionFailed ActionOutcomeState = "failed"
)

// ActionOutcome is [ActionDispatcher.Dispatch]'s result — the structured
// verdict D-3/B renders to an operator or a macro caller. Reason is never
// empty: every branch that constructs one states why, including a
// confirmed outcome (mirroring fppcommand_evidence.go's own "state the
// confirming evidence even on success" rule) so an operator reading
// "confirmed" can see what evidence backs it, not just the word.
type ActionOutcome struct {
	Action ActionName
	State  ActionOutcomeState
	Reason string

	// DispatchedAt is the zero time.Time when State == ActionRefused (no
	// dispatch occurred). Otherwise it is the instant this dispatcher
	// issued the write request — the fence every confirmation read below
	// is checked against (§4.1).
	DispatchedAt time.Time

	// ConfirmedAt is meaningful only when State == ActionConfirmed: the
	// instant the confirming evidence was actually read, always strictly
	// after DispatchedAt (see evidenceIsPostDispatch).
	ConfirmedAt time.Time
}

// ActionParams is the typed parameter bag for one [ActionDispatcher.Dispatch]
// call. Which fields matter depends on Action — see each action's own
// doc comment in action_dispatch.go. Every id here is a "ShowMesh
// reference": [ActionDispatcher.Dispatch] resolves it against the stored
// composition ([CompositionStore]) before issuing any request, and an id
// this composition does not contain is refused, never passed through to
// Resolume verbatim (TRACK-D-D3-SPEC.md §2's own "no raw object id" rule).
// Unlike fppcommand's map[string]any (that package decodes untrusted wire
// JSON before it knows which primitive it is validating against), D-3/B
// already knows which action it is building a call for by the time it
// reaches this package, so there is no wire-decode step here to justify a
// stringly-typed bag — a Go struct lets the compiler catch a caller passing
// a clip id to blackout.
type ActionParams struct {
	// ClipID: launchClip. The id of either a deck clip or a persistent
	// clip in the stored composition — [ActionDispatcher.Dispatch] tries
	// both, since a bare id does not itself say which kind it is (ADR-032
	// decision 6).
	ClipID ObjectID

	// LayerID: clearLayer, setLayerBypass, setLayerMaster.
	LayerID ObjectID

	// ColumnID: launchColumn.
	ColumnID ObjectID

	// DeckID: selectDeck.
	DeckID ObjectID

	// Bypassed: setLayerMaster's OWN sibling field, unused. The requested
	// value for setLayerBypass.
	Bypassed bool

	// Master: the requested continuous value for setLayerMaster. The
	// bench capture observed [0, 1] on this layer's own RangeParameter,
	// but that is one measured instance, not a contract — Arena's own
	// specification declares min/max per parameter, and
	// dispatchSetLayerMaster (action_dispatch.go) validates against THIS
	// layer's own declared bound, read fresh off the pre-dispatch
	// baseline, rather than assuming [0, 1] holds universally. D-3/B still
	// owns wire-level shape validation (is this JSON a number at all),
	// exactly as fppcommand_primitives.go's own ValidateParams owns FPP's
	// equivalent (this package's Dispatch is the trusted-caller boundary,
	// not the untrusted-wire one) — but the RANGE check is this
	// package's own, because only this package has read Arena's own
	// declared bound by the time Dispatch is called.
	Master float64
}

// ActionDispatcherOptions configures an [ActionDispatcher]. Every field
// left at its zero value is replaced by a documented default.
type ActionDispatcherOptions struct {
	// Now is the clock used for every dispatch/confirm timestamp in this
	// package, including the post-dispatch fence (§4.1). nil (the default
	// in production) means time.Now; tests inject a fake, and MUST pair it
	// with a Sleep that advances the same fake clock — see Sleep's own doc
	// comment.
	Now func() time.Time

	// Sleep is called between confirmation poll attempts. nil (the
	// default in production) means time.Sleep. A test that injects a fake
	// Now MUST inject a Sleep that advances it, or the poll loop below
	// will wait out a real deadline using a clock that never proves it
	// arrived (or, if Now is real but Sleep is stubbed to a no-op, the
	// loop will spin — this package's own tests use a paired fake
	// clock/sleep, never one without the other).
	Sleep func(time.Duration)

	// PollInterval bounds how often a confirmation re-reads Resolume
	// while waiting for its deadline. See [DefaultActionConfirmPollInterval].
	PollInterval time.Duration
}

// ActionDispatcher dispatches one of [actionRegistry]'s seven actions
// against one [Collector]'s underlying [Client] and [CompositionStore],
// running the full resolve / pre-dispatch guard / dispatch / confirmation
// lifecycle in one call. It is the seam D-3/B (internal/coordinator/api) is
// built against — see [ActionDispatcher.Dispatch]'s own doc comment for the
// stability contract that boundary relies on.
type ActionDispatcher struct {
	collector    *Collector
	now          func() time.Time
	sleep        func(time.Duration)
	pollInterval time.Duration
}

// NewActionDispatcher constructs an [ActionDispatcher] over collector.
// collector must be non-nil.
func NewActionDispatcher(collector *Collector, opts ActionDispatcherOptions) *ActionDispatcher {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultActionConfirmPollInterval
	}
	return &ActionDispatcher{collector: collector, now: now, sleep: sleep, pollInterval: pollInterval}
}

// Actions returns [actionRegistry]'s seven entries, sorted by Name for a
// deterministic listing (an API surface D-3/B builds a discovery endpoint
// from should not depend on Go's own slice-literal order).
func (d *ActionDispatcher) Actions() []ActionDescriptor {
	out := make([]ActionDescriptor, len(actionRegistry))
	copy(out, actionRegistry)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Dispatch runs one action end to end: the universal composition-identity
// gate (§3.6), the action's own resolve-against-the-stored-map step, its
// own pre-dispatch guard (only launchClip has one today — the deck
// refusal, §3.4), its pre-dispatch baseline capture (§4.2, every action),
// the write request itself, and — unless the baseline already satisfied the
// confirming predicate (§3.5, §4.2) — a bounded confirmation poll (§4.1,
// §4.3) against the action's own DERIVED deadline (§3.3).
//
// The returned error is reserved for a caller mistake this package can
// detect statically — an unrecognized name, today the only such case —
// never for anything Resolume said or failed to say: a definite negative
// from Resolume's own dispatch response is [ActionFailed], a timeout on
// confirmation is [ActionUnconfirmed], and a guard refusal is
// [ActionRefused] — all three are values in the returned [ActionOutcome],
// not a Go error, because D-3/B needs a structured verdict to render to an
// operator, not a bare error string standing in for one of five different
// outcomes.
//
// This signature — an [ActionName], an [ActionParams], a returned
// [ActionOutcome] plus error — is the stability contract D-3/B is built
// against; see [ActionDescriptor] and [ActionOutcome] for the other two
// halves of it.
func (d *ActionDispatcher) Dispatch(ctx context.Context, name ActionName, params ActionParams) (ActionOutcome, error) {
	switch name {
	case ActionLaunchClip:
		return d.dispatchLaunchClip(ctx, params), nil
	case ActionClearLayer:
		return d.dispatchClearLayer(ctx, params), nil
	case ActionBlackout:
		return d.dispatchBlackout(ctx, params), nil
	case ActionLaunchColumn:
		return d.dispatchLaunchColumn(ctx, params), nil
	case ActionSelectDeck:
		return d.dispatchSelectDeck(ctx, params), nil
	case ActionSetLayerBypass:
		return d.dispatchSetLayerBypass(ctx, params), nil
	case ActionSetLayerMaster:
		return d.dispatchSetLayerMaster(ctx, params), nil
	default:
		return ActionOutcome{}, fmt.Errorf("resolume: dispatch: unrecognized action %q", name)
	}
}

// --- Deadline constants (§3.3) --------------------------------------------
//
// Every one of these is a NAMED constant, never a literal at a call site —
// §3.3's own rule, stated because a fixed deadline is wrong by 35x on the
// capture's own measurement (connect: 4-64ms; disconnect: 75ms-4,068ms
// depending on transition.duration).

const (
	// DefaultActionConfirmDeadline is a start/parameter action's own fixed
	// budget (launchClip, launchColumn, selectDeck, setLayerBypass,
	// setLayerMaster) — capture §7's own connect measurement (4-64ms) with
	// generous headroom. SHOWMESH GUESS, NOT MEASURED for the exact
	// number: only the ORDER of headroom above the measured range is
	// justified by the capture, not this specific value.
	DefaultActionConfirmDeadline = 2 * time.Second

	// DefaultActionConfirmMargin is added to a layer's own measured
	// transition.duration for clearLayer and blackout. Capture §7.2
	// measured 75-113ms of overshoot past the transition boundary in
	// every run; this margin is roughly an order of magnitude above that
	// measured overshoot, per the capture's own stated conclusion ("a 1s
	// margin is an order of magnitude of headroom").
	DefaultActionConfirmMargin = 1 * time.Second

	// DefaultActionConfirmDeadlineUnknownTransition is clearLayer/blackout's
	// own fallback ONLY when transition.duration itself could not be read
	// for any affected layer — never a silent 0, which would collapse
	// "unknown" into "instant" exactly the way CLAUDE.md's own "ma": null
	// defect collapsed absence into zero. SHOWMESH GUESS, NOT MEASURED:
	// chosen larger than the largest transition duration this capture
	// measured (5.0s, observed at 4,068ms to confirm) plus
	// [DefaultActionConfirmMargin], so an ordinary transition still has
	// room to complete even when this seam cannot read how long it will
	// take.
	DefaultActionConfirmDeadlineUnknownTransition = 10 * time.Second

	// DefaultActionConfirmPollInterval bounds how often a confirmation
	// re-reads Resolume while waiting for its own deadline. This is an
	// IMPLEMENTATION choice, not a capture-derived number: nothing in
	// TRACK-D-D3-SPEC.md or the bench capture measured a safe re-read
	// cadence for a single action's own bounded confirmation window (as
	// opposed to D-2's own steady-state /product cadence, which this
	// package does not touch — see [DefaultActionConfirmPollInterval]'s
	// own budget note below). SHOWMESH GUESS, NOT MEASURED: frequent
	// enough to catch a 4-64ms connect within its own 2s budget without
	// meaningful added latency, infrequent enough that even a full
	// blackout confirmation window (worst case ~11s: the unknown-transition
	// fallback above) issues on the order of tens, not thousands, of
	// requests — a bounded, action-triggered burst, nothing like D-2's own
	// continuous steady-state traffic, and negligible against capture
	// §14.3's own measured 209,916 by-id reads in five minutes with no
	// crash.
	DefaultActionConfirmPollInterval = 50 * time.Millisecond

	// layerMasterEpsilon is setLayerMaster's own float-equality tolerance:
	// a ParamRange round-trips through Resolume's own JSON encoding, and
	// this package does not assume that encoding is exact to the last bit
	// for a value ShowMesh itself just wrote. SHOWMESH GUESS, NOT
	// MEASURED.
	layerMasterEpsilon = 1e-6

	// MaxActionConfirmDeadline is the single, exported upper bound on how
	// long ANY call to [ActionDispatcher.Dispatch] will wait for confirming
	// evidence before returning — the "registry maximum" a caller sizing an
	// HTTP write deadline before it even knows which of the seven actions is
	// about to run (internal/coordinator/api's own handleDispatchResolumeAction,
	// which sets its write deadline before the request body is read) needs
	// to be able to trust. [DefaultActionConfirmDeadline],
	// [DefaultActionConfirmDeadlineUnknownTransition], and
	// [DefaultActionConfirmMargin] together already bound every ACTION
	// CLASS's deadline for the two cases this package can size in advance
	// (a fixed-budget action; a clearLayer/blackout whose transition.duration
	// could not be read at all) — but clearLayer and blackout's OTHER case,
	// a layer whose transition.duration WAS read, derives its deadline from
	// that value directly (§3.3), and transition.duration is live state this
	// package reads off Resolume, not a value any registry here can bound in
	// advance: an operator is free to configure a layer transition of any
	// length in Arena's own UI. TRACK-D-D3-SPEC.md's request for this task
	// names this directly: "the registry's maximum is itself a computed
	// bound," and a computed bound over an genuinely unbounded input has no
	// true maximum to compute.
	//
	// So this constant is not a value read OFF the deadline model — it is a
	// clamp APPLIED to it, by [clampActionConfirmDeadline], everywhere
	// action_dispatch.go derives a deadline from a live transition.duration
	// (deriveClearDeadline, and dispatchBlackout's own per-layer maximum).
	// That is what makes it true rather than optimistic: no deadline this
	// package's own Dispatch computes can ever exceed it, because the code
	// that computes one is the code that enforces it, in the same package,
	// not a caller trusting a separately-asserted number. A layer transition
	// configured longer than this bound still confirms correctly if it
	// finishes in time; if it does not, the action reports [ActionUnconfirmed]
	// with a stated reason at this bound rather than at the transition's own
	// true (longer) length — never [ActionFailed] (§3.3's own rule: a
	// deadline expiring is a claim about this package's own evidence
	// pipeline, not about the show) and never an unbounded wait a caller's
	// own write deadline cannot be sized against. This is an explicit,
	// disclosed limitation, not a papered-over one: see this constant's own
	// value below for the reasoning behind the specific number.
	//
	// SHOWMESH GUESS, NOT MEASURED for the exact value: chosen well above
	// every other named constant in this block — 5x
	// [DefaultActionConfirmDeadlineUnknownTransition], which was itself
	// already chosen larger than the largest transition.duration this
	// package's own capture ever measured (5.0s) — so an ordinary,
	// real-world transition has ample room to finish inside it, while still
	// being small enough that internal/coordinator/api's own HTTP write
	// deadline (sized from this value plus its own round-trip margin) stays
	// a bounded, human-scale number rather than an effectively unbounded
	// one. internal/coordinator/api/resolumeaction.go's own
	// resolumeActionMaxConfirmDeadline is checked against this exact value
	// by a test that fails the build the moment the two disagree
	// (resolumeaction_test.go); cmd/showmeshctl cannot import this package
	// (its own import-graph test forbids it) and so keeps its own
	// independently chosen, test-reconciled literal, the identical shape
	// [command.MaxFPPCommandConfirmDeadline]'s own doc comment already
	// defends for FPP's equivalent CLI/server boundary.
	MaxActionConfirmDeadline = 30 * time.Second
)

// clampActionConfirmDeadline bounds d to [MaxActionConfirmDeadline], never
// upward: a shorter, already-derived deadline (a fixed budget, or a short
// transition) is returned unchanged, and only a deadline that would exceed
// the clamp is shortened to it. See [MaxActionConfirmDeadline]'s own doc
// comment for why this exists at all — deriveClearDeadline and
// dispatchBlackout are this function's only two callers, both because both
// derive a deadline from a layer's own live, operator-configured
// transition.duration, the one input in this package's whole deadline model
// with no registry-known bound.
func clampActionConfirmDeadline(d time.Duration) time.Duration {
	if d > MaxActionConfirmDeadline {
		return MaxActionConfirmDeadline
	}
	return d
}

// evidenceIsPostDispatch is TRACK-D-D3-SPEC.md §4.1's own fence, made a
// single named function so a test can mutate or remove it and observe every
// confirmation predicate in action_dispatch.go start accepting pre-dispatch
// evidence — the exact 179-microsecond defect CLAUDE.md records against
// Step 7's own equivalent (fppcommand_evidence.go's [resolveConfirmationEvidence],
// which checks o.CollectedAt.Before(notBefore) the same way). readAt must
// be STRICTLY after dispatchedAt: a read collected in the same instant as
// dispatch (possible with a coarse or stubbed clock) proves nothing about
// what happened AFTER that instant.
func evidenceIsPostDispatch(readAt, dispatchedAt time.Time) bool {
	return readAt.After(dispatchedAt)
}

// --- Shared outcome constructors ------------------------------------------

func refusedOutcome(name ActionName, reason string) ActionOutcome {
	return ActionOutcome{Action: name, State: ActionRefused, Reason: reason}
}

func failedOutcome(name ActionName, dispatchedAt time.Time, reason string) ActionOutcome {
	return ActionOutcome{Action: name, State: ActionFailed, DispatchedAt: dispatchedAt, Reason: reason}
}

func unconfirmableOutcome(name ActionName, dispatchedAt time.Time, reason string) ActionOutcome {
	return ActionOutcome{Action: name, State: ActionUnconfirmable, DispatchedAt: dispatchedAt, Reason: reason}
}

// --- The composition-identity gate (§3.6) ---------------------------------

// identityGateRefusal reports whether snap allows ANY action to dispatch,
// per TRACK-D-D3-SPEC.md §3.6: "no action dispatches while composition
// identity is unknown or false." Consumes [Collector.LastSurveySnapshot]
// exclusively — see this file's own top comment — never issuing an HTTP
// request of its own. The reason names when identity was last checked and
// what it found, per §3.4's own "the reading is itself evidence and is
// fenced" rule extended to this gate as well.
func identityGateRefusal(snap SurveySnapshot) (reason string, refuse bool) {
	if !snap.SurveyRan || !snap.IdentityKnown {
		return "no composition survey has completed yet, so composition identity is not known", true
	}
	if snap.Identity == IdentityTrue {
		return "", false
	}
	return fmt.Sprintf(
		"composition identity is not confirmed (last checked %s, state %q); an action is not dispatched until identity is confirmed",
		snap.IdentityObservedAt.Format(time.RFC3339), snap.Identity), true
}

// --- The clip deck refusal (§3.4) -----------------------------------------

// deckRefusal reports whether a clip stored against expectedDeck may
// dispatch, per §3.4: a clip whose deck is not the currently selected deck
// is refused before dispatch, naming both decks, off the cached
// [SurveySnapshot] alone — never a fresh HTTP request (acceptance criterion
// 3: "issuing no HTTP request to Resolume at all"). tc supplies
// expectedDeck's own display name when known, purely for the message; the
// comparison itself is by id.
func deckRefusal(tc *TrackedComposition, expectedDeck ObjectID, snap SurveySnapshot) (reason string, refuse bool) {
	expectedName := ""
	if tc != nil {
		if d, ok := tc.DeckByID(expectedDeck); ok {
			expectedName = d.Name
		}
	}
	if !snap.SelectedDeckKnown {
		return fmt.Sprintf(
			"this clip belongs to %s, and the currently selected deck is not known, so its deck cannot be verified",
			formatRef(expectedDeck, expectedName)), true
	}
	if snap.SelectedDeckID == expectedDeck {
		return "", false
	}
	return fmt.Sprintf(
		"this clip belongs to %s, but the currently selected deck (as of %s) is %s",
		formatRef(expectedDeck, expectedName),
		snap.SelectedDeckObservedAt.Format(time.RFC3339),
		formatRef(snap.SelectedDeckID, snap.SelectedDeckName)), true
}

// --- The confirmation poll (§4.1, §4.3) -----------------------------------

// pollUntilConfirmedOrDeadline retries check at d's own poll interval until
// either check reports confirmed, ctx is done, or [ActionDispatcher.now]
// passes dispatchedAt+deadline — whichever comes first. check performs
// exactly the "1 to 3 targeted by-id reads" TRACK-D-D3-SPEC.md §4.3
// describes for one action (never a poll-loop signal added anywhere else —
// see this package's own doc comment) and must itself apply
// [evidenceIsPostDispatch] to every value it reads before reporting
// confirmed; this function does not re-check that fence, only the deadline.
func (d *ActionDispatcher) pollUntilConfirmedOrDeadline(
	ctx context.Context,
	name ActionName,
	dispatchedAt time.Time,
	deadline time.Duration,
	check func() (confirmed bool, confirmedAt time.Time, reason string),
) ActionOutcome {
	deadlineAt := dispatchedAt.Add(deadline)
	lastReason := "no confirming evidence has arrived yet"

	for {
		if confirmed, confirmedAt, reason := check(); confirmed {
			return ActionOutcome{Action: name, State: ActionConfirmed, DispatchedAt: dispatchedAt, ConfirmedAt: confirmedAt, Reason: reason}
		} else if reason != "" {
			lastReason = reason
		}

		if ctx.Err() != nil {
			return ActionOutcome{Action: name, State: ActionUnconfirmed, DispatchedAt: dispatchedAt,
				Reason: fmt.Sprintf("the request was canceled before confirming evidence arrived: %s", lastReason)}
		}
		if !d.now().Before(deadlineAt) {
			return ActionOutcome{Action: name, State: ActionUnconfirmed, DispatchedAt: dispatchedAt,
				Reason: fmt.Sprintf("no confirming evidence arrived within %s of dispatch: %s", deadline, lastReason)}
		}
		d.sleep(d.pollInterval)
	}
}
