package resolume

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// TRACK-D-D3-SPEC.md §2's seven-action vocabulary: the registry, the
// end-to-end dispatch window, the pre-dispatch guards, and the confirmation
// poll. action_dispatch.go holds each action's own resolve/baseline/confirm
// logic; action_client.go holds the REST calls it dispatches.

// ActionName is one of TRACK-D-D3-SPEC.md §2's seven action names — the
// entire vocabulary this package accepts. `POST /composition/action`
// (undo/redo) and every `DELETE` are excluded outright.
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

// ActionSafetyClass records whether an action is ADR-024 decision 11's named
// exemption or the default fail-closed rule. A bare bool cannot distinguish
// "explicitly decided not exempt" from "nobody ever decided", so this is a
// three-valued enum whose zero value fails [TestEveryActionDeclaresASafetyClass].
// This package declares the class; D-3/B's handler enforces it (there is no
// audit write here at all).
type ActionSafetyClass int

const (
	// ActionSafetyClassUndeclared is the zero value and is never a valid
	// registry entry.
	ActionSafetyClassUndeclared ActionSafetyClass = iota

	// ActionSafetyClassExempt: ADR-024 decision 11's blackout/stop/power-off
	// class. [ActionBlackout] and [ActionClearLayer] only.
	ActionSafetyClassExempt

	// ActionSafetyClassNotExempt: fails closed on a pre-dispatch audit-write
	// failure. Every action other than blackout and clearLayer.
	ActionSafetyClassNotExempt
)

// localFallbackClassCoordinatorRequired is every action's declared
// [ActionDescriptor.LocalFallbackClass] (§5.3, ADR-016). No exceptions: the
// adapter is coordinator-hosted and Resolume holds no fallback.
const localFallbackClassCoordinatorRequired = "coordinator-required"

// ActionDescriptor is one row of [actionRegistry]: the metadata D-3/B needs
// about an action without dispatching it. It carries no dispatch logic.
type ActionDescriptor struct {
	Name               ActionName
	SafetyClass        ActionSafetyClass
	LocalFallbackClass string
}

// actionRegistry is TRACK-D-D3-SPEC.md §2's table in full, with §5.2's safety
// classes. This slice, and only this slice, is what [ActionDispatcher.Actions]
// and this package's registry tests walk.
var actionRegistry = []ActionDescriptor{
	{Name: ActionLaunchClip, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionClearLayer, SafetyClass: ActionSafetyClassExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionBlackout, SafetyClass: ActionSafetyClassExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionLaunchColumn, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSelectDeck, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSetLayerBypass, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
	{Name: ActionSetLayerMaster, SafetyClass: ActionSafetyClassNotExempt, LocalFallbackClass: localFallbackClassCoordinatorRequired},
}

// actionSafetyClass returns name's declared class, or
// [ActionSafetyClassUndeclared] for a name not in the registry.
func actionSafetyClass(name ActionName) ActionSafetyClass {
	for _, e := range actionRegistry {
		if e.Name == name {
			return e.SafetyClass
		}
	}
	return ActionSafetyClassUndeclared
}

// ActionOutcomeState is the five-way result TRACK-D-D3-SPEC.md's §3-§4
// vocabulary requires: never collapsed to a bool, and "confirmed" is never a
// default a caller falls back to.
type ActionOutcomeState string

const (
	// ActionConfirmed: evidence collected strictly after this dispatch
	// matches the action's confirming predicate.
	ActionConfirmed ActionOutcomeState = "confirmed"

	// ActionUnconfirmed: dispatched, but no confirming evidence arrived
	// before the deadline. §3.3: a deadline expiring is a claim about
	// ShowMesh's evidence pipeline, never about the show.
	ActionUnconfirmed ActionOutcomeState = "unconfirmed"

	// ActionUnconfirmable: dispatched, but no post-dispatch evidence could
	// ever be attributed to it — the predicate already held, or no
	// pre-dispatch baseline could be read (§3.5, §4.2).
	ActionUnconfirmable ActionOutcomeState = "unconfirmable"

	// ActionRefused: NOT dispatched. A pre-dispatch guard stopped it.
	ActionRefused ActionOutcomeState = "refused"

	// ActionFailed: Resolume's own response to the dispatch was a definite
	// negative, so there is positive evidence this action did not execute.
	ActionFailed ActionOutcomeState = "failed"
)

// ActionOutcome is [ActionDispatcher.Dispatch]'s structured verdict. Reason
// is never empty on any branch, confirmed included, so an operator reading
// "confirmed" can see what evidence backs it.
type ActionOutcome struct {
	Action ActionName
	State  ActionOutcomeState
	Reason string

	// DispatchedAt is the zero time.Time when State == ActionRefused.
	// Otherwise it is the instant the write request was issued — the fence
	// every confirmation read is checked against (§4.1).
	DispatchedAt time.Time

	// ConfirmedAt is meaningful only when State == ActionConfirmed, and is
	// always strictly after DispatchedAt.
	ConfirmedAt time.Time

	// SelectedDeckChanged records whether the selected deck changed
	// between this action's decision and its confirmation (TRACK-D-ADAPTER-SPEC.md
	// §3.8) — evidence carried alongside the outcome, never a refusal
	// (making it one would reintroduce the fail-closed inversion D-3's own
	// review found three times in one diff). nil means either this is not
	// [ActionLaunchClip] (the only action that races a deck — layers are
	// deck-independent) or the deck could not be read at confirmation
	// time; nil is NEVER coerced to false.
	SelectedDeckChanged *bool
}

// ActionParams is the typed parameter bag for one Dispatch call; which fields
// matter depends on the action. Every id is resolved against the stored
// composition before any request is issued, and an id this composition does
// not contain is refused rather than passed through to Resolume.
type ActionParams struct {
	// ClipID: launchClip. Either a deck clip or a persistent clip —
	// Dispatch tries both, since a bare id does not say which it is.
	ClipID ObjectID

	// LayerID: clearLayer, setLayerBypass, setLayerMaster.
	LayerID ObjectID

	// ColumnID: launchColumn.
	ColumnID ObjectID

	// DeckID: selectDeck.
	DeckID ObjectID

	// Bypassed: the requested value for setLayerBypass.
	Bypassed bool

	// Master: the requested value for setLayerMaster. Range-validated
	// against THIS layer's own declared bound read off the pre-dispatch
	// baseline, never against the [0, 1] the bench capture happened to see.
	Master float64

	// ResolvedAtRevision is the CompositionStore revision that was current
	// when a caller above this dispatcher resolved a name into one of the
	// id fields above — 0 when nothing was resolved against a composition
	// (blackout). Dispatch refuses before doing anything else if the store
	// has since moved past this revision: Arena preserves an object's own
	// id across a rename, so a resolved id does not fail safe on its own
	// against a rename-and-re-upload that lands between resolution and
	// dispatch.
	ResolvedAtRevision int64
}

// ActionDispatcherOptions configures an [ActionDispatcher]. Every field left
// at its zero value is replaced by a documented default.
type ActionDispatcherOptions struct {
	// Now is the clock used for every timestamp and every budget check in
	// this package, including the post-dispatch fence (§4.1). nil means
	// time.Now; a test injecting a fake MUST pair it with Sleep.
	Now func() time.Time

	// Sleep is called between confirmation poll attempts. nil means
	// time.Sleep. A test that injects a fake Now MUST inject a Sleep that
	// advances the same fake clock, or the poll loop will wait out a real
	// deadline using a clock that never proves it arrived (and if Now is
	// real while Sleep is a no-op, the loop spins).
	Sleep func(time.Duration)

	// PollInterval is the FIRST delay between confirmation re-reads; the
	// interval grows from there. See [DefaultActionConfirmPollInterval].
	PollInterval time.Duration
}

// ActionDispatcher dispatches one of [actionRegistry]'s seven actions against
// one [Collector]'s [Client] and [CompositionStore], running the whole
// resolve / guard / dispatch / confirm lifecycle in one call. It is the seam
// D-3/B (internal/coordinator/api) is built against.
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

// CurrentCompositionWithRevision returns the stored composition Dispatch
// itself resolves every id against, together with the CompositionStore
// revision it was read at — for a caller that resolves a name into an
// [ActionParams] field (setting [ActionParams.ResolvedAtRevision] to the
// returned revision) before Dispatch is ever called.
//
// Revision is read BEFORE the composition, never after: Refresh always
// stores a new composition strictly before it stores the new revision
// number, so this ordering can only under-report freshness, never pair a
// revision with a composition older than the one actually resolved
// against.
func (d *ActionDispatcher) CurrentCompositionWithRevision() (*TrackedComposition, int64, error) {
	revision := d.collector.compositionStore.LoadedRevision()
	tc, err := d.collector.compositionStore.Current()
	return tc, revision, err
}

// Actions returns [actionRegistry]'s entries, sorted by Name so a discovery
// endpoint built from them does not depend on Go's slice-literal order.
func (d *ActionDispatcher) Actions() []ActionDescriptor {
	out := make([]ActionDescriptor, len(actionRegistry))
	copy(out, actionRegistry)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Dispatch runs one action end to end under a [MaxDispatchDuration] window:
// the identity gate (§3.6), the resolve against the stored composition, the
// deck refusal (§3.4), the pre-dispatch baseline (§4.2), the write, and a
// bounded confirmation poll against a derived deadline (§3.3).
//
// The returned error is reserved for a caller mistake this package can detect
// statically — an unrecognized name, today the only such case. Everything
// Resolume said or failed to say is a state in the returned [ActionOutcome].
// That signature is the stability contract D-3/B is built against.
func (d *ActionDispatcher) Dispatch(ctx context.Context, name ActionName, params ActionParams) (ActionOutcome, error) {
	if params.ResolvedAtRevision != 0 {
		if current := d.collector.compositionStore.LoadedRevision(); current != params.ResolvedAtRevision {
			return refusedOutcome(name, fmt.Sprintf(
				"the composition was replaced (revision %d, now %d) while this command was being prepared; the "+
					"resolved reference may no longer name the intended object — re-issue the command",
				params.ResolvedAtRevision, current)), nil
		}
	}

	w := d.openWindow()
	ctx, cancel := context.WithDeadline(ctx, w.endAt)
	defer cancel()

	var outcome ActionOutcome
	switch name {
	case ActionLaunchClip:
		outcome = d.dispatchLaunchClip(ctx, w, params)
	case ActionClearLayer:
		outcome = d.dispatchClearLayer(ctx, w, params)
	case ActionBlackout:
		outcome = d.dispatchBlackout(ctx, w)
	case ActionLaunchColumn:
		outcome = d.dispatchLaunchColumn(ctx, w, params)
	case ActionSelectDeck:
		outcome = d.dispatchSelectDeck(ctx, w, params)
	case ActionSetLayerBypass:
		outcome = d.dispatchSetLayerBypass(ctx, w, params)
	case ActionSetLayerMaster:
		outcome = d.dispatchSetLayerMaster(ctx, w, params)
	default:
		return ActionOutcome{}, fmt.Errorf("resolume: dispatch: unrecognized action %q", name)
	}

	// Track D seam D-3a §4 rule 1: the recovery record updates at
	// confirmation, never at dispatch (ADR-003). Centralized here, once,
	// rather than in each dispatchX method, so every action's confirmed
	// path updates the record identically.
	if outcome.State == ActionConfirmed {
		d.collector.applyConfirmedActionToRecoveryRecord(name, params, outcome.ConfirmedAt)
	}

	return outcome, nil
}

// --- Deadline and budget constants (§3.3) ---------------------------------
//
// Every one of these is a named constant, never a literal at a call site: a
// fixed deadline is wrong by 35x on the capture's own measurement (connect
// 4-64ms; disconnect 75ms-4,068ms depending on transition.duration).

const (
	// DefaultActionConfirmDeadline is a start/parameter action's fixed budget
	// (launchClip, launchColumn, selectDeck, setLayerBypass, setLayerMaster).
	// SHOWMESH GUESS, NOT MEASURED for the exact number: only the order of
	// headroom above the measured 4-64ms connect is justified by the capture.
	DefaultActionConfirmDeadline = 2 * time.Second

	// DefaultActionConfirmMargin is added to a layer's measured
	// transition.duration for clearLayer and blackout. Capture §7.2 measured
	// 75-113ms of overshoot past the transition boundary in every run.
	DefaultActionConfirmMargin = 1 * time.Second

	// DefaultActionConfirmDeadlineUnknownTransition is clearLayer's and
	// blackout's fallback when transition.duration could not be read at all —
	// never a silent 0, which would collapse "unknown" into "instant".
	// SHOWMESH GUESS, NOT MEASURED: larger than the longest transition the
	// capture measured (5.0s, confirming at 4,068ms) plus the margin.
	DefaultActionConfirmDeadlineUnknownTransition = 10 * time.Second

	// DefaultActionConfirmPollInterval is the FIRST delay between
	// confirmation re-reads. Each subsequent interval doubles, capped at
	// [maxActionConfirmPollInterval], so a 4-64ms connect still confirms on
	// the first or second attempt without the tail of a long deadline
	// hammering Arena. SHOWMESH GUESS, NOT MEASURED.
	DefaultActionConfirmPollInterval = 25 * time.Millisecond

	// maxActionConfirmPollInterval caps that growth. Real footprint at these
	// two values, measured by TestConfirmationFootprintStaysBounded and by
	// hand: a single-object action spends 8 attempts (9 reads including its
	// baseline) across a full 2s budget, and blackout — which re-reads every
	// tracked layer per attempt — at most 7 attempts (about 126 reads at 18
	// layers) for a 1.5s derived deadline and 26 (about 470) at the
	// unknown-transition fallback, less whenever the walk stops early on a
	// layer that has not cleared. Not "1 to 3 reads", and not thousands.
	maxActionConfirmPollInterval = 500 * time.Millisecond

	// layerMasterEpsilon is setLayerMaster's own float-equality tolerance: a
	// ParamRange round-trips through Resolume's own JSON encoding, and this
	// package does not assume that encoding is exact to the last bit for a
	// value ShowMesh itself just wrote. SHOWMESH GUESS, NOT MEASURED.
	layerMasterEpsilon = 1e-6

	// MinActionConfirmDeadline floors a derived deadline.
	// transition.duration is live operator-set state, and a negative one
	// would otherwise produce a deadline already in the past: a confirmation
	// window that never opens and reports unconfirmed without one read.
	MinActionConfirmDeadline = DefaultActionConfirmMargin

	// MaxActionConfirmDeadline clamps how long the confirmation poll waits.
	// An operator can configure any transition length in Arena, so a deadline
	// derived from one has no natural maximum. Exceeding it is
	// [ActionUnconfirmed], never [ActionFailed]. SHOWMESH GUESS, NOT
	// MEASURED; the longest transition the capture measured was 5.0s.
	MaxActionConfirmDeadline = 30 * time.Second

	// MaxBaselinePhaseBudget bounds the WHOLE pre-dispatch baseline phase,
	// not one read within it. blackout reads a baseline for every tracked
	// layer sequentially, so a per-request timeout alone leaves the phase
	// unbounded in layer count.
	MaxBaselinePhaseBudget = 5 * time.Second

	// MaxWritePhaseBudget bounds the dispatch write itself.
	MaxWritePhaseBudget = 5 * time.Second

	// MaxDispatchDuration is the total wall clock [ActionDispatcher.Dispatch]
	// may spend on any action. Dispatch enforces it as a window on its own
	// clock, so a caller sizing an HTTP write deadline before it knows which
	// action is about to run can size from this number.
	//
	// The bound is real only because every phase is separately bounded. An
	// earlier revision exported MaxActionConfirmDeadline as this bound while
	// the baseline phase and the in-flight confirmation check ran outside it,
	// which made the exported number an underestimate by a factor that grew
	// with layer count.
	MaxDispatchDuration = MaxBaselinePhaseBudget + MaxWritePhaseBudget + MaxActionConfirmDeadline

	// MaxIdentityEvidenceAge fences the cached composition-identity reading
	// the §3.6 gate rests on. Equal to [DefaultSurveyValidFor] so an action
	// refuses exactly when the dashboard renders that same reading stale.
	// Surveys are event-driven (and under ADR-033 Show Mode the WebSocket
	// that triggers one is closed), so a reading past this age also asks the
	// collector for a fresh survey rather than only refusing.
	MaxIdentityEvidenceAge = DefaultSurveyValidFor
)

// clampActionConfirmDeadline bounds a derived deadline to
// [MinActionConfirmDeadline, MaxActionConfirmDeadline]. Both ends matter: the
// upper because transition.duration is unbounded operator state, the lower
// because a negative one produces a deadline already in the past.
func clampActionConfirmDeadline(d time.Duration) time.Duration {
	if d > MaxActionConfirmDeadline {
		return MaxActionConfirmDeadline
	}
	if d < MinActionConfirmDeadline {
		return MinActionConfirmDeadline
	}
	return d
}

// --- The dispatch window ---------------------------------------------------

// dispatchWindow is one Dispatch call's [MaxDispatchDuration] bound, held on
// [ActionDispatcher.now]'s clock rather than only as a context deadline: a
// context timer runs on real time, so a loop that must stop when the budget
// is gone has to test the same clock the tests drive.
type dispatchWindow struct {
	d     *ActionDispatcher
	endAt time.Time
}

func (d *ActionDispatcher) openWindow() dispatchWindow {
	return dispatchWindow{d: d, endAt: d.now().Add(MaxDispatchDuration)}
}

// phase returns a context for one phase's requests plus the instant that
// phase must end, bounded by the smaller of budget and what is left of the
// whole window.
func (w dispatchWindow) phase(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc, time.Time) {
	now := w.d.now()
	if rem := w.endAt.Sub(now); rem < budget {
		budget = rem
	}
	if budget < 0 {
		budget = 0
	}
	pctx, cancel := context.WithTimeout(ctx, budget)
	return pctx, cancel, now.Add(budget)
}

// expired reports whether at has passed on this dispatcher's clock.
func (d *ActionDispatcher) expired(at time.Time) bool {
	return !d.now().Before(at)
}

// --- The pre-dispatch baseline phase (§4.2) --------------------------------

// baselineReader runs one action's pre-dispatch reads under a single shared
// [MaxBaselinePhaseBudget]. A read is skipped rather than attempted once the
// budget is gone, so the phase is bounded in the number of objects an action
// reads and not only per request.
type baselineReader struct {
	d      *ActionDispatcher
	ctx    context.Context
	cancel context.CancelFunc
	endAt  time.Time
	err    error
	spent  bool
}

func (w dispatchWindow) beginBaseline(ctx context.Context) *baselineReader {
	bctx, cancel, endAt := w.phase(ctx, MaxBaselinePhaseBudget)
	return &baselineReader{d: w.d, ctx: bctx, cancel: cancel, endAt: endAt}
}

// read performs one baseline read, reporting whether it ran and succeeded.
func (b *baselineReader) read(fn func(context.Context) error) bool {
	if b.err != nil || b.spent {
		return false
	}
	if b.d.expired(b.endAt) {
		b.spent = true
		return false
	}
	if err := fn(b.ctx); err != nil {
		b.err = err
		return false
	}
	return true
}

func (b *baselineReader) close() { b.cancel() }

// failure states why the baseline phase did not complete, or "" when it did.
func (b *baselineReader) failure() string {
	switch {
	case b.spent:
		return fmt.Sprintf("the pre-dispatch reads did not finish within their %s budget", MaxBaselinePhaseBudget)
	case b.err != nil:
		return ClassifyError(b.err)
	default:
		return ""
	}
}

// baselineFailureOutcome applies the rule for an action whose pre-dispatch
// baseline could not be read. A not-exempt action refuses: refusing a start
// costs only that it does not start. An exempt action (blackout, clearLayer)
// dispatches anyway and reports unconfirmable, because refusing a stop for
// want of a pre-dispatch READ is the fail-closed inversion ADR-024 decision
// 11 already settled for the audit write; blackout's confirming predicate is
// absolute, not a delta, so the baseline is needed only for the
// already-satisfied test. No confirmation poll runs either way — without a
// baseline it could not mean anything, and it costs requests.
func (d *ActionDispatcher) baselineFailureOutcome(ctx context.Context, w dispatchWindow, name ActionName, why string, write func(context.Context) error) ActionOutcome {
	if actionSafetyClass(name) != ActionSafetyClassExempt {
		return refusedOutcome(name, fmt.Sprintf("could not read a pre-dispatch baseline for this action: %s", why))
	}
	dispatchedAt, bad := d.writePhase(ctx, w, name, string(name), write)
	if bad != nil {
		return *bad
	}
	return unconfirmableOutcome(name, dispatchedAt, fmt.Sprintf(
		"the pre-dispatch baseline could not be read (%s), so post-dispatch evidence cannot distinguish this action's effect from the state it found", why))
}

// --- The write phase --------------------------------------------------------

// writePhase issues one action's dispatch request under
// [MaxWritePhaseBudget], re-checking the identity gate immediately before it:
// the gate at the top of a dispatch ran before the baseline reads, which can
// be seconds, and re-reading the cached snapshot is free. bad is non-nil when
// the outcome is already decided.
func (d *ActionDispatcher) writePhase(ctx context.Context, w dispatchWindow, name ActionName, what string, fn func(context.Context) error) (time.Time, *ActionOutcome) {
	if reason, refuse := d.identityGate(name, d.collector.LastSurveySnapshot()); refuse {
		out := refusedOutcome(name, "composition identity stopped being confirmed before this action was dispatched: "+reason)
		return time.Time{}, &out
	}

	wctx, cancel, endAt := w.phase(ctx, MaxWritePhaseBudget)
	defer cancel()

	dispatchedAt := d.now()
	err := fn(wctx)
	if err == nil {
		return dispatchedAt, nil
	}
	// A write that ran out of budget may or may not have reached Arena, so it
	// is unconfirmed. Only a definite negative from Resolume is failed.
	if d.expired(endAt) {
		out := ActionOutcome{Action: name, State: ActionUnconfirmed, DispatchedAt: dispatchedAt, Reason: fmt.Sprintf(
			"dispatching %s ran out of its %s write budget, so whether Resolume received it is not known: %s", what, MaxWritePhaseBudget, ClassifyError(err))}
		return dispatchedAt, &out
	}
	out := failedOutcome(name, dispatchedAt, fmt.Sprintf("dispatching %s failed: %s", what, ClassifyError(err)))
	return dispatchedAt, &out
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

// evidenceIsPostDispatch is §4.1's fence, a single named function so a test
// can remove it and watch every confirmation predicate start accepting
// pre-dispatch evidence. readAt must be STRICTLY after dispatchedAt: a read
// collected in the same instant as dispatch proves nothing about what
// happened after that instant, which is Step 7's 179-microsecond defect.
func evidenceIsPostDispatch(readAt, dispatchedAt time.Time) bool {
	return readAt.After(dispatchedAt)
}

// --- The composition-identity gate (§3.6) ---------------------------------

// identityGateRefusal reports whether snap allows any action to dispatch:
// §3.6's "no action dispatches while composition identity is unknown or
// false", plus an age fence, since a survey runs only on an event and the
// reading can otherwise be arbitrarily old. stale distinguishes the age
// refusal from the others so a caller can nudge a fresh survey.
func identityGateRefusal(snap SurveySnapshot, now time.Time) (reason string, refuse, stale bool) {
	if !snap.SurveyRan || !snap.IdentityKnown {
		return "no composition survey has completed yet, so composition identity is not known", true, false
	}
	if age := now.Sub(snap.IdentityObservedAt); age > MaxIdentityEvidenceAge {
		return fmt.Sprintf(
			"the composition identity evidence is %s old (last checked %s), past the %s an action may rest on; a fresh check has been requested",
			age.Round(time.Second), snap.IdentityObservedAt.Format(time.RFC3339), MaxIdentityEvidenceAge), true, true
	}
	if snap.Identity == IdentityTrue {
		return "", false, false
	}
	return fmt.Sprintf(
		"composition identity is not confirmed (last checked %s, state %q); an action is not dispatched until identity is confirmed",
		snap.IdentityObservedAt.Format(time.RFC3339), snap.Identity), true, false
}

// identityGate applies [identityGateRefusal] and, when the reading is too old
// to rest a decision on, asks for a fresh survey so the next attempt is not
// refused for the same reason.
//
// An exempt action is not refused for a STALE reading. Staleness is a fact
// about this package's own evidence pipeline, and refusing a stop for want of
// our own evidence is the fail-closed inversion ADR-024 decision 11 settled
// for the audit write. An identity of unknown or false is a fact about the
// composition, so §3.6's refusal still applies to every action.
func (d *ActionDispatcher) identityGate(name ActionName, snap SurveySnapshot) (string, bool) {
	reason, refuse, stale := identityGateRefusal(snap, d.now())
	if stale {
		d.collector.RequestSurvey(false)
		if actionSafetyClass(name) == ActionSafetyClassExempt {
			return "", false
		}
	}
	return reason, refuse
}

// --- The clip deck refusal (§3.4) -----------------------------------------

// deckSelectionRefusal decides §3.4's deck term from deck, a by-id read of
// the clip's OWN deck taken at readAt. It rests on that fresh read rather
// than on the cached survey snapshot, which is event-driven and under Show
// Mode can be hours old — refusing a legitimate clip from a stale deck
// reading is the disguise §3.4 names. snap contributes only the last-known
// selected deck for the message, attributed with its own age.
func deckSelectionRefusal(tc *TrackedComposition, expectedDeck ObjectID, deck Deck, readAt time.Time, snap SurveySnapshot) (reason string, refuse bool) {
	expectedName := ""
	if tc != nil {
		if dk, ok := tc.DeckByID(expectedDeck); ok {
			expectedName = dk.Name
		}
	}
	selected, ok := deck.Selected.Bool()
	if !ok {
		return fmt.Sprintf(
			"this clip belongs to %s, and that deck did not report whether it is selected when read at %s, so its deck cannot be verified",
			formatRef(expectedDeck, expectedName), readAt.Format(time.RFC3339)), true
	}
	if selected {
		return "", false
	}
	other := "the currently selected deck is not known"
	if snap.SelectedDeckKnown {
		other = fmt.Sprintf("the most recently observed selected deck is %s (as of %s)",
			formatRef(snap.SelectedDeckID, snap.SelectedDeckName), snap.SelectedDeckObservedAt.Format(time.RFC3339))
	}
	return fmt.Sprintf("this clip belongs to %s, and that deck is not selected (read at %s); %s",
		formatRef(expectedDeck, expectedName), readAt.Format(time.RFC3339), other), true
}

// --- The confirmation poll (§4.1, §4.3) -----------------------------------

// confirmScope is what one confirmation check is handed: a context bounded by
// what is left of the deadline, and expired, which a check walking many
// objects must test between reads so one in-flight check cannot outrun the
// deadline it is being polled against.
type confirmScope struct {
	ctx     context.Context
	expired func() bool
}

// pollUntilConfirmedOrDeadline retries check on a growing interval until
// check confirms, ctx is done, or the deadline passes — whichever comes
// first. The deadline is the smaller of dispatchedAt+deadline and what is
// left of the dispatch window. check applies [evidenceIsPostDispatch] to
// every value it reads; this function only enforces the deadline.
func (d *ActionDispatcher) pollUntilConfirmedOrDeadline(
	ctx context.Context,
	w dispatchWindow,
	name ActionName,
	dispatchedAt time.Time,
	deadline time.Duration,
	check func(confirmScope) (confirmed bool, confirmedAt time.Time, reason string),
) ActionOutcome {
	deadlineAt := dispatchedAt.Add(deadline)
	windowBound := false
	if deadlineAt.After(w.endAt) {
		deadlineAt, windowBound = w.endAt, true
	}
	expired := func() bool { return d.expired(deadlineAt) }

	lastReason := "no confirming evidence has arrived yet"
	interval := d.pollInterval

	for {
		if expired() {
			return d.unconfirmedOutcome(name, dispatchedAt, deadline, windowBound, lastReason)
		}

		cctx, cancel := context.WithTimeout(ctx, deadlineAt.Sub(d.now()))
		confirmed, confirmedAt, reason := check(confirmScope{ctx: cctx, expired: expired})
		cancel()

		if confirmed {
			return ActionOutcome{Action: name, State: ActionConfirmed, DispatchedAt: dispatchedAt, ConfirmedAt: confirmedAt, Reason: reason}
		}
		if reason != "" {
			lastReason = reason
		}
		if ctx.Err() != nil && !expired() {
			return ActionOutcome{Action: name, State: ActionUnconfirmed, DispatchedAt: dispatchedAt,
				Reason: fmt.Sprintf("the request was canceled before confirming evidence arrived: %s", lastReason)}
		}
		if expired() {
			return d.unconfirmedOutcome(name, dispatchedAt, deadline, windowBound, lastReason)
		}

		// Never sleep past the deadline: the last nap of a poll loop is the
		// one place a bound enforced between attempts can still overshoot it.
		nap := interval
		if left := deadlineAt.Sub(d.now()); nap > left {
			nap = left
		}
		d.sleep(nap)
		if interval < maxActionConfirmPollInterval {
			if interval *= 2; interval > maxActionConfirmPollInterval {
				interval = maxActionConfirmPollInterval
			}
		}
	}
}

// unconfirmedOutcome names which budget ran out — the action's own derived
// confirmation deadline, or the total dispatch window that cut it short.
func (d *ActionDispatcher) unconfirmedOutcome(name ActionName, dispatchedAt time.Time, deadline time.Duration, windowBound bool, lastReason string) ActionOutcome {
	reason := fmt.Sprintf("no confirming evidence arrived within %s of dispatch: %s", deadline, lastReason)
	if windowBound {
		reason = fmt.Sprintf("the confirmation phase ran out of the %s total dispatch budget before evidence arrived: %s", MaxDispatchDuration, lastReason)
	}
	return ActionOutcome{Action: name, State: ActionUnconfirmed, DispatchedAt: dispatchedAt, Reason: reason}
}
