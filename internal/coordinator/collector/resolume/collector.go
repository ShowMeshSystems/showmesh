package resolume

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift in either package is caught here.
var _ collector.Collector = (*Collector)(nil)

// sourceName is [observation.Observation.Source] for [SignalReachable] and
// [SignalProduct] — the two liveness signals produced on the steady-state
// poll timer. See surveySourceName immediately below for why the rest of
// this package's signals use a DIFFERENT source rather than this one.
const sourceName = "resolume-rest"

// surveySourceName is [observation.Observation.Source] for every signal a
// [Collector.Survey] call produces — composition identity, deck/selected
// deck, per-layer readiness and active clip, per-clip connected and
// transporttype, and composition name.
//
// This MUST be a different string than sourceName, not a stylistic choice:
// internal/coordinator/collector.Sink's own doc comment requires a
// complete=true delivery's pruning to scope to exactly the (resource,
// source) pairs PRESENT in that delivery. [Collector.Poll] always returns
// complete=true, and on the overwhelming majority of calls (no survey
// pending — TRACK-D-D2-SPEC.md §3.1's steady state) its returned batch
// carries ONLY sourceName observations. If survey-derived signals shared
// that same source, every ordinary liveness poll's complete=true delivery
// would prune them all away on its very next tick, because they would not
// be present in that batch — turning "a survey ran once" into "a survey's
// results live for at most one poll interval." A distinct source is what
// lets liveness and survey data age and prune independently, each only
// ever touched by the kind of poll that actually re-enumerates it.
const surveySourceName = "resolume-survey"

// DefaultPollInterval and DefaultValidFor are SHOWMESH HYPOTHESES, NOT
// MEASURED — the bench capture that produced this package never measured
// a safe polling cadence against a live show host, only /product's
// response shape and size (64 bytes) over loopback. Chosen the same way
// internal/coordinator/collector/fpp's own defaults are: frequent enough
// that a dashboard reads as reasonably live, infrequent enough that
// polling a live show host's REST API on a timer is not itself a concern
// — and /product is the cheapest possible probe this API offers, smaller
// than any single FPP endpoint this project already polls every 15s.
const (
	DefaultPollInterval = 10 * time.Second
	DefaultValidFor     = 3 * DefaultPollInterval

	// DefaultSurveyValidFor is deliberately much longer than DefaultValidFor:
	// a survey is event-driven (TRACK-D-D2-SPEC.md §3.1 — an explicit
	// request or a confirmed reconnect), never polled on a timer, so its
	// signals must not read as stale simply because nothing has happened
	// to warrant a new one. This bound still exists — TRACK-D-D2-SPEC.md
	// §11/acceptance criterion 11: "signals never read continuously ...
	// state their real freshness ... never rendered as though just
	// measured" — so a survey's evidence eventually does age into
	// [observation.StateStale] on its own via [observation.Observation.ValidFor],
	// exactly like any other observation; this only sets how long that
	// takes. SHOWMESH GUESS, NOT MEASURED.
	DefaultSurveyValidFor = 15 * time.Minute

	// DefaultTransitionSurveyMinInterval is this task's own "defect 3" fix's
	// rate limit: the minimum spacing between two survey runs that were
	// EACH triggered only by a liveness transition (§3.1's "no survey ever
	// runs in Show Mode" gap — see [Collector.Poll]'s own doc comment on
	// why a transition needs a trigger of its own now that the WebSocket
	// reconnect signal can be switched off per ADR-033). Deliberately does
	// NOT rate-limit an explicit [Collector.RequestSurvey] call or a
	// composition-revision change — an operator asking for a refresh, or
	// uploading a new show, must never be silently dropped by a limiter
	// that exists to protect Resolume from THIS package's own automatic
	// behavior, not from the operator.
	//
	// A survey is roughly 24-36 requests (§3.1's own budget row). Without
	// this bound, an Arena that is flapping — up, down, up, down — would
	// queue one full survey on every single return to "up", turning
	// "Resolume is unstable" into "ShowMesh adds sustained load to an
	// application that is already crashing," which is the exact failure
	// mode Track D's own crash investigation exists to avoid causing more
	// of. SHOWMESH GUESS, NOT MEASURED: chosen to be comfortably longer
	// than one ordinary liveness poll interval (so a single flap cannot
	// slip past the window before it is even checked) and comfortably
	// shorter than DefaultSurveyValidFor (so an operator who deliberately
	// restarts Arena during setup is not left looking at composition
	// signals that read as current for a quarter of an hour while actually
	// built from before the restart).
	DefaultTransitionSurveyMinInterval = 1 * time.Minute

	// DefaultRunnerCheckInterval is the interval resolumewiring.go registers
	// this Collector on internal/coordinator/collector.Runner with — NOT the
	// same thing as the liveness poll interval (see [FootprintControls]).
	// [Collector.Poll] self-throttles its own /product request against
	// [FootprintControls.PollInterval], re-read on every call, so Runner's
	// own fixed interval only needs to be short enough that a runtime change
	// to that dynamic value (or an explicit [Collector.RequestSurvey], via
	// Runner.Nudge) is noticed promptly — it does not, itself, determine how
	// often Resolume is actually contacted. See ADR-033 and
	// TRACK-D-D2-SPEC.md §3.3, added mid-build: a fixed interval baked into
	// one Runner.Add call could never be changed without reconstructing the
	// collector, which is exactly what this split avoids.
	DefaultRunnerCheckInterval = 1 * time.Second
)

// FootprintControls holds TRACK-D-D2-SPEC.md §3.3's two runtime-adjustable
// knobs: whether the WebSocket wake-up channel should be held open, and how
// often the liveness poll (GET /product) actually runs. Both are read
// FRESH at the point of decision — [Collector.Poll] re-reads PollInterval
// on every call, and resolumewiring.go's watcher supervisor re-reads
// WebSocketEnabled on its own short loop — rather than captured once at
// construction time.
//
// ADR-033 (decided mid-build, 2026-08-14): the real long-term driver of
// both knobs is an installation-wide "show mode" this seam does NOT build
// — see that record for the full reasoning. This type is deliberately the
// smallest thing that lets a future mode drive them later
// (SetWebSocketEnabled/SetPollInterval) without anyone reconstructing the
// Collector, the Watcher, or the coordinator to do it. A static bool or
// time.Duration baked into a constructor argument would have to be torn
// out for that; a value read through this type would not need to be.
//
// Safe for concurrent use. The zero value is not ready to use — construct
// with [NewFootprintControls], whose default has the WebSocket enabled
// (matching D-1's unconditional-watcher behavior, so a caller that never
// touches this type sees no change) and PollInterval() falling back to
// [DefaultPollInterval].
type FootprintControls struct {
	webSocketEnabled  atomic.Bool
	pollIntervalNanos atomic.Int64
}

// NewFootprintControls constructs a [FootprintControls] with the WebSocket
// enabled and the poll interval defaulted to [DefaultPollInterval].
func NewFootprintControls() *FootprintControls {
	f := &FootprintControls{}
	f.webSocketEnabled.Store(true)
	return f
}

// WebSocketEnabled reports whether the WebSocket wake-up channel should
// currently be held open.
func (f *FootprintControls) WebSocketEnabled() bool { return f.webSocketEnabled.Load() }

// SetWebSocketEnabled changes WebSocketEnabled's answer immediately, for
// every subsequent caller, with no reconstruction of anything required.
func (f *FootprintControls) SetWebSocketEnabled(enabled bool) { f.webSocketEnabled.Store(enabled) }

// PollInterval reports the current liveness poll interval, or
// [DefaultPollInterval] if never set (or set to a non-positive value).
func (f *FootprintControls) PollInterval() time.Duration {
	n := f.pollIntervalNanos.Load()
	if n <= 0 {
		return DefaultPollInterval
	}
	return time.Duration(n)
}

// SetPollInterval changes PollInterval's answer immediately. d <= 0 resets
// to the [DefaultPollInterval] fallback.
func (f *FootprintControls) SetPollInterval(d time.Duration) {
	if d <= 0 {
		f.pollIntervalNanos.Store(0)
		return
	}
	f.pollIntervalNanos.Store(int64(d))
}

// Options configures a [Collector]. Every field left at its zero value is
// replaced by a documented default.
type Options struct {
	// HTTPClient is passed through to the [Client] this Collector builds
	// internally. See [ClientOptions.HTTPClient].
	HTTPClient *http.Client

	// ValidFor is set as [observation.WithValidFor] on the two liveness
	// signals (SignalReachable, SignalProduct). See DefaultValidFor.
	ValidFor time.Duration

	// SurveyValidFor is set as [observation.WithValidFor] on every signal a
	// [Collector.Survey] call produces. See DefaultSurveyValidFor.
	SurveyValidFor time.Duration

	// RequestTimeout bounds every individual request this Collector makes,
	// liveness or survey. See [DefaultRequestTimeout].
	RequestTimeout time.Duration

	// Now is the clock used for ObservedAt/CollectedAt on every
	// observation, and for [Collector.Poll]'s own liveness-interval
	// throttle. nil (the default in production) means time.Now; tests
	// inject a fake clock.
	Now func() time.Time

	// Logger is this Collector's diagnostic logger — Poll and Survey
	// themselves never log; every outcome is an observation. nil means
	// slog.Default().
	Logger *slog.Logger

	// CompositionStore is where [Collector.Survey] reads the currently
	// tracked composition from (Track D seam D-2/B). nil means a
	// collector with nothing ever uploaded to it — every survey then
	// reports [ErrCompositionNotUploaded] on every composition-level
	// signal and produces no per-layer/per-clip signals, exactly as if a
	// real *CompositionStore existed but had never loaded a revision (see
	// that type's own zero-value doc comment).
	CompositionStore *CompositionStore

	// Footprint is this Collector's [FootprintControls]. nil constructs a
	// fresh one via [NewFootprintControls]. A caller that wants to change
	// the poll interval or WebSocket posture at runtime (today: only
	// resolumewiring.go's config-derived startup value; later: ADR-033's
	// show mode) must supply its OWN *FootprintControls here and keep the
	// reference, since [Collector.Footprint] merely returns whatever was
	// stored at construction.
	Footprint *FootprintControls

	// OnReachableTransition is Track D seam D-3a's own hook (§5 term 1):
	// called on a GENUINE reachable->unreachable->reachable return — never
	// on this Collector's very first liveness result ever, which is not a
	// "return" (a coordinator that starts while Resolume is already
	// reachable must not fire the crash-recovery gate for a crash that
	// never happened). Never throttled: the crash-recovery gate must
	// still fire on a second crash inside [DefaultTransitionSurveyMinInterval]
	// (§5's own bypass, criterion 12) — that limiter applies only to the
	// ordinary transition SURVEY trigger below, a separate condition.
	// Invoked in its own goroutine, never synchronously from
	// [Collector.Poll] — this Collector has no idea how long the callback
	// takes and must never block on it. nil means no hook.
	OnReachableTransition func(returnedAt time.Time)

	// OnUnreachableTransition is Track D seam D-3a's own crash-detection
	// hook: called on a genuine reachable->unreachable transition, i.e.
	// the crash itself. Invoked SYNCHRONOUSLY from
	// [Collector.Poll] — unlike OnReachableTransition — because the
	// callback's own job (snapshotting the recovery record) must complete
	// before any LATER Poll call could detect the eventual return;
	// internal/coordinator/collector.Runner never calls Poll concurrently
	// with itself for one collector, so a synchronous call here is
	// guaranteed ordered against every subsequent call. Must be fast and
	// must never issue a request to Resolume — see
	// [Recovery.CaptureCrashTarget], the only intended implementation.
	// nil means no hook.
	OnUnreachableTransition func(at time.Time)

	// TransitionSurveySettle is Track D seam D-3a §5 term 2's own settle
	// delay, applied to THIS Collector's own transition-triggered survey
	// (the noteLivenessAndCheckTransition trigger below) on a genuine
	// crash-return: the ~30-read survey must not land on the same poll
	// cycle that first observes the return (criterion 11). [Recovery]'s
	// own explicit confirming survey (recovery.go,
	// Recovery.HandleReachableTransition) applies an equal delay
	// independently via its own RecoveryOptions.Settle — production
	// wiring (resolumewiring.go) configures both from the SAME
	// cfg.ResolumeRecoverySettle value rather than letting them drift,
	// but each enforces its own copy, because this Collector must hold
	// the rule even with no Recovery wired at all. Zero (the default)
	// disables deferral entirely — every pre-existing test in this
	// package, none of which sets this field, keeps behaving exactly as
	// before.
	TransitionSurveySettle time.Duration
}

// Collector polls one Resolume Arena instance's REST API. On the liveness
// timer it performs a single GET /api/v1/product; when a survey has been
// requested ([Collector.RequestSurvey]) it additionally performs Track D
// seam D-2/C's own composition survey (TRACK-D-D2-SPEC.md §3.1) as part of
// that same poll cycle. It implements internal/coordinator/collector.Collector.
type Collector struct {
	id       string
	client   *Client
	validFor time.Duration
	now      func() time.Time
	logger   *slog.Logger

	surveyValidFor   time.Duration
	compositionStore *CompositionStore
	footprint        *FootprintControls

	// pollMu guards lastLivenessPollAt, [Collector.Poll]'s own throttle
	// state. internal/coordinator/collector.Runner never calls Poll
	// concurrently with itself for one collector (see that package's own
	// doc comment), so this is defensive rather than load-bearing against
	// Runner — it IS load-bearing against a test or a future caller
	// invoking Poll directly from more than one goroutine.
	pollMu             sync.Mutex
	lastLivenessPollAt time.Time

	// requestMu guards ONLY the pending-survey request flags
	// (surveyPending/surveyAfterReconnect) — deliberately a separate,
	// always-fast lock from surveyMu below, so [Collector.RequestSurvey]
	// (called synchronously from the Watcher's own connection-handling
	// goroutine — see watch.go's OnConnect doc comment) never blocks
	// behind an in-progress, multi-second [Collector.survey] call.
	requestMu            sync.Mutex
	surveyPending        bool
	surveyAfterReconnect bool

	// surveyMu guards identityConfirmed (TRACK-D-D2-SPEC.md §7's
	// load-window state) and is held for the full duration of
	// [Collector.survey], which is what gives this package its own
	// version of the "never two in-flight requests to the same instance"
	// property internal/coordinator/collector/fpp gets for free from
	// Runner's single-goroutine-per-collector loop — a defensive
	// serialization against a future or test caller invoking Poll
	// concurrently with itself, which internal/coordinator/collector.Runner
	// itself never does.
	surveyMu          sync.Mutex
	identityConfirmed bool

	// snapshotMu guards the small last-completed-survey snapshot Track D
	// seam D-3's own action vocabulary (action.go) consults for two
	// pre-dispatch guards that must never themselves issue an HTTP
	// request to Resolume: the composition-identity gate
	// (TRACK-D-D3-SPEC.md §3.6, "D-2 already computes identity; D-3
	// consumes it and does not recompute it") and the clip deck refusal's
	// own "which deck is selected" reading (§3.4, "the selected deck
	// reading is itself evidence and is fenced"). Both consult the MOST
	// RECENT completed survey's own already-fenced result — via
	// [Collector.LastSurveySnapshot] — rather than issuing a fresh read
	// of their own, which is what keeps the deck refusal's own acceptance
	// criterion true: "issuing no HTTP request to Resolume at all." A
	// dedicated lock rather than reusing surveyMu: a reader of the
	// snapshot must never block behind an in-progress, multi-second
	// [Collector.survey] call the way a second survey attempt correctly
	// does.
	snapshotMu   sync.Mutex
	lastSnapshot SurveySnapshot

	// transitionMu guards every field below — this task's own "defect 3"
	// fix's transition/composition-change survey triggers, deliberately its
	// own small lock in this file's established granular-locking style
	// (pollMu/requestMu/surveyMu each guard one narrow slice of state; see
	// each of their own doc comments).
	transitionMu sync.Mutex

	// livenessKnown is false only before this Collector's very first
	// liveness attempt (success OR failure) has completed.
	// lastLivenessReachable is meaningful only once livenessKnown is true.
	// Together these detect BOTH of TRACK-D-D2-SPEC.md's own defect-3
	// triggers with one condition: "the previous liveness result was a
	// failure" (down->up) and "this is the very first successful poll ever"
	// (livenessKnown false) collapse into the identical test —
	// !livenessKnown || !lastLivenessReachable — evaluated the instant a
	// NEW /product success is observed, before either field is updated for
	// this cycle. See [Collector.noteLivenessAndCheckTransition].
	livenessKnown          bool
	lastLivenessReachable  bool
	lastTransitionSurveyAt time.Time // zero means "never yet" — the first transition-triggered survey this Collector ever runs is never rate-limited

	// transitionSurveySettle is [Options.TransitionSurveySettle], stored
	// verbatim; zero disables deferral (see that field's own doc comment).
	transitionSurveySettle time.Duration

	// transitionSurveyDueAt is when a survey deferred by a genuine
	// crash-return may run — the zero value means none is currently
	// deferred. Set only inside [Collector.noteLivenessAndCheckTransition]'s
	// own gateTransition branch; consumed by
	// [Collector.takeDueDeferredTransitionSurvey].
	transitionSurveyDueAt time.Time

	// lastSeenCompositionRevision mirrors [CompositionStore]'s own "0 ==
	// nothing loaded yet" convention exactly (idmap.go): starting this at
	// the Go zero value means a Collector constructed before anything was
	// ever uploaded does NOT spuriously treat its own first check as a
	// change (0 == 0), while a genuinely new upload — whose revision is
	// always >= 1 — always does.
	lastSeenCompositionRevision int64

	// onReachableTransition is [Options.OnReachableTransition], stored
	// verbatim; nil is a legitimate value (no crash-recovery gate wired
	// in — most tests, and any coordinator with no Resolume instance
	// configured).
	onReachableTransition func(time.Time)

	// onUnreachableTransition is [Options.OnUnreachableTransition], stored
	// verbatim; nil is legitimate for the identical reasons
	// onReachableTransition's nil is.
	onUnreachableTransition func(time.Time)

	// recoveryMu guards recoveryRecord — Track D seam D-3a's own recovery
	// record (recovery.go), deliberately its own lock in this file's
	// established granular-locking style: a reader of the record
	// ([Collector.RecoveryRecord]) must never block behind an in-progress
	// survey the way [Collector.surveyMu] correctly does for a second
	// survey attempt.
	recoveryMu     sync.Mutex
	recoveryRecord map[ObjectID]recoveryEntry
}

// New constructs a Collector for one Resolume Arena instance. id must
// satisfy [mqttproto.ValidateNodeID] — the same identifier syntax
// internal/coordinator/collector/fpp.New requires of its own instance
// ids, applied here for the same reason: Resolume is a controlled device
// (ADR-016), not a ShowMesh node, but its collector still needs a stable
// id for logging and the API's collectors[] list. baseURL is validated by
// [NewClient]; see its doc comment.
func New(id string, baseURL string, opts Options) (*Collector, error) {
	if err := mqttproto.ValidateNodeID(id); err != nil {
		return nil, fmt.Errorf("resolume collector: %w", err)
	}

	client, err := NewClient(baseURL, ClientOptions{
		HTTPClient:     opts.HTTPClient,
		RequestTimeout: opts.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("resolume collector %q: %w", id, err)
	}

	validFor := opts.ValidFor
	if validFor <= 0 {
		validFor = DefaultValidFor
	}
	surveyValidFor := opts.SurveyValidFor
	if surveyValidFor <= 0 {
		surveyValidFor = DefaultSurveyValidFor
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	compositionStore := opts.CompositionStore
	if compositionStore == nil {
		compositionStore = &CompositionStore{}
	}
	footprint := opts.Footprint
	if footprint == nil {
		footprint = NewFootprintControls()
	}

	return &Collector{
		id:                      id,
		client:                  client,
		validFor:                validFor,
		now:                     now,
		logger:                  logger,
		surveyValidFor:          surveyValidFor,
		compositionStore:        compositionStore,
		footprint:               footprint,
		onReachableTransition:   opts.OnReachableTransition,
		onUnreachableTransition: opts.OnUnreachableTransition,
		transitionSurveySettle:  opts.TransitionSurveySettle,
	}, nil
}

// ID returns this instance's identifier.
func (c *Collector) ID() string { return c.id }

// Footprint returns this Collector's [FootprintControls] — the same
// instance passed via [Options.Footprint], or the one [New] constructed if
// none was supplied. resolumewiring.go's watcher supervisor reads
// WebSocketEnabled from this to decide whether to hold the WebSocket open.
func (c *Collector) Footprint() *FootprintControls { return c.footprint }

// SurveySnapshot is the small slice of the most recently completed
// survey's own result that Track D seam D-3's action vocabulary
// (action.go) consults for its two pre-dispatch guards — see
// [Collector.LastSurveySnapshot]'s own doc comment for why these two facts,
// and only these two, are cached here rather than each guard issuing a read
// of its own.
type SurveySnapshot struct {
	// SurveyRan is false only before this Collector has ever completed a
	// survey (or, transiently, while it does not know identity or a
	// selected deck for any other reason — see IdentityKnown/
	// SelectedDeckKnown, which are the fields a caller actually gates on).
	// Carried for observability only.
	SurveyRan bool
	SurveyAt  time.Time

	// Identity is the outcome of the most recent survey's own
	// [CheckIdentity] call. IdentityKnown is false only before this
	// Collector has ever completed a survey at all — once a survey has
	// run, Identity ALWAYS carries a real [IdentityOutcome], including
	// [IdentityUnknown] (a survey can determinately conclude "unknown"),
	// so a caller must inspect Identity itself, not only IdentityKnown, to
	// decide whether to proceed. IdentityObservedAt is the survey's own
	// timestamp — this is a CACHED reading, and TRACK-D-D3-SPEC.md §3.4's
	// own "the reading is itself evidence and is fenced" rule applies to
	// it exactly as it does to SelectedDeck below: a caller basing a
	// refusal on this value states how old it is.
	IdentityKnown      bool
	Identity           IdentityOutcome
	IdentityObservedAt time.Time

	// SelectedDeck is the deck the most recent survey found selected
	// (empty/zero and SelectedDeckKnown=false if none reported itself
	// selected, or no survey has run). Mirrors exactly what
	// [Collector.deckObservations] publishes as
	// resolume.composition.selected_deck, cached here so
	// TRACK-D-D3-SPEC.md §3.4's clip deck refusal can read it WITHOUT
	// issuing its own HTTP request — see this type's own doc comment.
	SelectedDeckKnown      bool
	SelectedDeckID         ObjectID
	SelectedDeckName       string
	SelectedDeckObservedAt time.Time
}

// LastSurveySnapshot returns the most recently completed survey's own
// identity and selected-deck result. Cheap: a mutex-guarded struct copy,
// safe to call from any goroutine, including concurrently with an
// in-progress [Collector.survey] (a concurrent caller sees either the
// complete previous snapshot or the complete new one, never a partial
// update, since [Collector.survey] only ever replaces this value as one
// whole assignment under [Collector.snapshotMu]).
//
// Before this Collector has ever completed a survey, every field is at its
// zero value (SurveyRan, IdentityKnown, and SelectedDeckKnown all false) —
// this is honest: nothing has been observed yet, and a caller gating a
// dispatch on IdentityKnown correctly refuses in this state, per
// TRACK-D-D3-SPEC.md §3.6 ("no action dispatches while composition identity
// is unknown or false").
func (c *Collector) LastSurveySnapshot() SurveySnapshot {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	return c.lastSnapshot
}

// recordSurveySnapshot installs snap as the new [Collector.LastSurveySnapshot]
// result — called once per [Collector.survey] call, after that survey's own
// identity check and deck selection are both known, whether or not a
// composition was even uploaded (see [Collector.survey]'s own call sites:
// the "nothing uploaded" branch records an IdentityUnknown/SelectedDeck-
// unknown snapshot rather than leaving a stale one from a since-removed
// composition in place).
func (c *Collector) recordSurveySnapshot(snap SurveySnapshot) {
	c.snapshotMu.Lock()
	defer c.snapshotMu.Unlock()
	c.lastSnapshot = snap
}

// RequestSurvey queues Track D seam D-2/C's own composition survey
// (TRACK-D-D2-SPEC.md §3.1) to run as part of the NEXT [Collector.Poll]
// call, in addition to that call's ordinary liveness /product request —
// never on its own timer, per this package's central rule. Safe to call
// from any goroutine, including concurrently with an in-progress Poll (the
// request is then honored on the poll after that).
//
// afterReconnect must be true when this call is triggered by a confirmed
// WebSocket reconnect (§3.1's other survey trigger, alongside an explicit
// operator request) — it resets TRACK-D-D2-SPEC.md §7's load-window state,
// so every layer's readiness and the composition identity signal report
// unknown, naming the load window, until a subsequent survey (this one or
// a later one) produces a determinate identity result. Pass false for an
// explicit operator-requested refresh, which must NOT reopen a load window
// that has already closed.
//
// A second call before the first is consumed coalesces rather than queuing
// twice — matching internal/coordinator/collector.Runner.Nudge's own
// coalescing behavior for the identical reason: a poll about to run anyway
// makes a second request redundant. If either call passed afterReconnect
// true, the coalesced request is treated as afterReconnect true — a
// pending "this might be stale evidence" state must never be silently
// downgraded to "ordinary refresh" by an explicit request that happens to
// land in the same narrow window.
func (c *Collector) RequestSurvey(afterReconnect bool) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.surveyPending = true
	if afterReconnect {
		c.surveyAfterReconnect = true
	}
}

// takePendingSurvey reports and clears any pending survey request.
func (c *Collector) takePendingSurvey() (afterReconnect bool, pending bool) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	pending = c.surveyPending
	afterReconnect = c.surveyAfterReconnect
	c.surveyPending = false
	c.surveyAfterReconnect = false
	return afterReconnect, pending
}

// requeueSurvey restores a pending survey request — used when liveness
// itself failed this cycle (Resolume unreachable), so there is nothing to
// survey yet: the request is not lost, it is retried on a later poll once
// /product succeeds again.
func (c *Collector) requeueSurvey(afterReconnect bool) {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.surveyPending = true
	if afterReconnect {
		c.surveyAfterReconnect = true
	}
}

// SurveyNow runs a survey immediately, bypassing both the liveness poll
// cadence and [DefaultTransitionSurveyMinInterval] — the "explicit path"
// that limiter already exempts (its own doc comment). Used only by the
// crash-recovery gate (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md §5's bypass),
// which must still be able to restore on a second crash inside that
// minute. Returns the resulting [SurveySnapshot] directly, so a caller
// never needs to poll [Collector.LastSurveySnapshot] afterward to learn
// what its own call just produced.
func (c *Collector) SurveyNow(ctx context.Context, afterReconnect bool) SurveySnapshot {
	c.survey(ctx, afterReconnect, c.now())
	return c.LastSurveySnapshot()
}

// Poll performs one collection cycle.
//
// Liveness (SignalReachable, SignalProduct, source [sourceName]):
// self-throttled against [FootprintControls.PollInterval], re-read on
// every call rather than a fixed interval captured once — ADR-033 and
// TRACK-D-D2-SPEC.md §3.3, added mid-build: ShowMesh's own
// internal/coordinator/collector.Runner requires one fixed interval per
// [collector.Runner.Add] call, which cannot itself be changed at runtime,
// so resolumewiring.go registers this Collector at a short fixed
// [DefaultRunnerCheckInterval] and Poll decides, EVERY time Runner calls
// it, whether enough of the dynamic PollInterval has actually elapsed to
// justify a real request. When it has not, Poll returns (nil, false) —
// the documented [collector.Collector.Poll] skip shape, identical to how
// internal/coordinator/collector/fpp.Collector skips a cycle under
// backoff: an empty result that must never be read as "this source now
// owns zero signals."
//
// A pending survey request (RequestSurvey) always makes this poll due,
// bypassing the liveness throttle: a confirmed reconnect or an explicit
// refresh is a real event, not routine cadence, and must not wait out
// however much of the liveness interval happens to remain.
//
// On liveness success, SignalReachable is measured true
// ([observation.QualityDerived]) and SignalProduct carries [Product.String]'s
// canonical form ([observation.QualityDirect]). On failure, BOTH become
// [observation.StateCollectionFailed] with [ClassifyError]'s reason — never a
// fabricated false, never an omission — and any pending survey is requeued
// rather than attempted or dropped (there is nothing to survey if Resolume
// itself did not answer).
//
// A successful liveness result can ALSO queue and immediately run a survey
// in this SAME call, with no [Collector.RequestSurvey] ever having been
// called — this task's own "defect 3" fix, 2026-08-14. §3.1's original two
// survey triggers (an explicit request, and a confirmed WebSocket
// reconnect) both depend on something outside this method ever running:
// once ADR-033 Show Mode is wired up to close the WebSocket, and until the
// D-4 explicit-refresh API ships, NEITHER can fire at all, so every
// composition-derived signal would stay empty forever. Two additional
// triggers, both derived only from state this method already has:
//
//   - [Collector.noteLivenessAndCheckTransition]: this liveness success
//     followed a failure, or is this Collector's first ever — an
//     unreachable-to-reachable transition, covering a restarted Arena, a
//     network coming back, or a coordinator starting while Arena is
//     already up. Rate-limited ([DefaultTransitionSurveyMinInterval]) so a
//     flapping Arena cannot turn into a survey storm; runs afterReconnect
//     true, exactly like a WebSocket reconnect would, so TRACK-D-D2-SPEC.md
//     §7's post-restart load window still applies (Resolume can answer
//     /product before it has finished loading the real composition).
//   - [Collector.compositionRevisionChanged]: the stored composition id map
//     moved to a new revision since this method last checked — an upload
//     landing with no other trigger available must not leave the dashboard
//     describing the previous show. Never rate-limited: an upload is an
//     explicit operator action, not something that can flap.
//
// Both triggers are evaluated only once this cycle's liveness result is
// already known, so there is nothing to check, and nothing rate-limited,
// on a throttled skip (due == false above) or on a liveness failure.
// compositionRevisionChanged runs its survey in THIS poll cycle, not a
// queued one for next time. The first trigger does too, UNLESS this
// success is a GENUINE crash-return (gateTransition) and
// [Options.TransitionSurveySettle] is configured — TRACK-D-D3A-CRASH-RECOVERY-SPEC.md
// §5 term 2 / criterion 11: that survey is deferred until the settle
// window elapses, picked up by [Collector.takeDueDeferredTransitionSurvey]
// on a later call, with no external trigger required — see
// [Collector.noteLivenessAndCheckTransition]'s own doc comment.
//
// complete is ALWAYS true when this poll actually ran (liveness, or
// liveness plus survey): see [surveySourceName]'s own doc comment for why
// that is safe for a batch that sometimes does, and sometimes does not,
// include survey-sourced observations.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	now := c.now()

	afterReconnect, hasPendingSurvey := c.takePendingSurvey()

	c.pollMu.Lock()
	due := hasPendingSurvey || c.lastLivenessPollAt.IsZero() || now.Sub(c.lastLivenessPollAt) >= c.footprint.PollInterval()
	if !due {
		c.pollMu.Unlock()
		if hasPendingSurvey {
			// Unreachable in practice (hasPendingSurvey forces due=true
			// above), kept only so a future edit to the due expression
			// cannot silently drop a pending survey request.
			c.requeueSurvey(afterReconnect)
		}
		return nil, false
	}
	c.lastLivenessPollAt = now
	c.pollMu.Unlock()

	product, err := c.client.Product(ctx)
	if err != nil {
		reason := ClassifyError(err)
		obs := []observation.Observation{
			c.failed(SignalReachable, reason, now),
			c.failed(SignalProduct, reason, now),
		}
		if hasPendingSurvey {
			c.requeueSurvey(afterReconnect)
		}
		c.noteLivenessAndCheckTransition(false, now)
		return obs, true
	}

	reachable := c.measured(SignalReachable, true, now, observation.WithQuality(observation.QualityDerived))
	productObs := c.measured(SignalProduct, product.String(), now, observation.WithQuality(observation.QualityDirect))
	obs := []observation.Observation{reachable, productObs}

	runSurvey := hasPendingSurvey
	surveyAfterReconnect := afterReconnect

	if c.noteLivenessAndCheckTransition(true, now) {
		runSurvey = true
		surveyAfterReconnect = true
	}
	if c.takeDueDeferredTransitionSurvey(now) {
		runSurvey = true
		surveyAfterReconnect = true
	}
	if c.compositionRevisionChanged() {
		runSurvey = true
	}

	if runSurvey {
		obs = append(obs, c.survey(ctx, surveyAfterReconnect, now)...)
	}

	return obs, true
}

// noteLivenessAndCheckTransition records this cycle's liveness outcome
// (reachable or not) and reports whether a SUCCESSFUL outcome should queue
// a survey because it followed a failure, or because it is this
// Collector's very first liveness result ever — see [Collector.Poll]'s own
// doc comment for why that trigger exists at all. Must be called with the
// outcome of THIS cycle's own /product call exactly once per [Collector.Poll]
// call that actually ran (never on a throttled skip).
//
// Rate-limited via [DefaultTransitionSurveyMinInterval]: a transition that
// would otherwise queue a survey, but arrives within that interval of the
// last transition-triggered survey THIS Collector actually ran, is still
// recorded (lastLivenessReachable updates either way) but does not itself
// queue one — see that constant's own doc comment for why. This limiter
// applies ONLY to this trigger; it has no effect on an explicit
// [Collector.RequestSurvey] or on [Collector.compositionRevisionChanged].
func (c *Collector) noteLivenessAndCheckTransition(reachable bool, now time.Time) bool {
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()

	wasKnown, wasReachable := c.livenessKnown, c.lastLivenessReachable

	// isTransition: "the previous liveness result was a failure" (down->up)
	// or "this is the very first successful poll ever" (livenessKnown
	// still false) — evaluated BEFORE either field is updated for this
	// cycle. This is the SURVEY trigger: a first-ever observation is
	// legitimately worth surveying (there is nothing yet to compare it
	// against).
	isTransition := reachable && (!wasKnown || !wasReachable)

	// gateTransition/crashTransition are a GENUINE return/crash only — never
	// this Collector's very first liveness result, which is not a "return":
	// a coordinator started while Resolume is already reachable must not
	// report a phantom automatic restore. Both require wasKnown, unlike
	// isTransition above.
	gateTransition := reachable && wasKnown && !wasReachable
	crashTransition := !reachable && wasKnown && wasReachable

	c.livenessKnown = true
	c.lastLivenessReachable = reachable

	if gateTransition && c.onReachableTransition != nil {
		// Fired on EVERY genuine return, unrate-limited — see
		// [Options.OnReachableTransition]'s own doc comment for why this
		// must not share the transition-survey rate limit below. Spawned
		// so a slow or blocking hook (the crash-recovery gate's own
		// settle wait) can never stall this Poll call.
		cb, at := c.onReachableTransition, now
		go cb(at)
	}
	if crashTransition && c.onUnreachableTransition != nil {
		// Called SYNCHRONOUSLY, unlike the return hook above — see
		// [Options.OnUnreachableTransition]'s own doc comment for why
		// ordering against a later Poll call depends on this completing
		// before this call returns.
		c.onUnreachableTransition(now)
	}

	if !isTransition {
		return false
	}
	if !c.lastTransitionSurveyAt.IsZero() && now.Sub(c.lastTransitionSurveyAt) < DefaultTransitionSurveyMinInterval {
		return false
	}
	c.lastTransitionSurveyAt = now

	// Track D seam D-3a §5 term 2 / criterion 11: a GENUINE crash-return
	// (gateTransition — never this Collector's first-ever liveness
	// result, which has no crash to settle from) must not survey on the
	// SAME cycle that observes the return. The rate-limit check above
	// still decides WHETHER this transition earns a survey at all; this
	// only changes WHEN an approved one actually runs — deferred until
	// transitionSurveySettle elapses, then picked up by a later
	// [Collector.Poll] call via [Collector.takeDueDeferredTransitionSurvey],
	// with no external trigger required.
	if gateTransition && c.transitionSurveySettle > 0 {
		c.transitionSurveyDueAt = now.Add(c.transitionSurveySettle)
		return false
	}
	return true
}

// takeDueDeferredTransitionSurvey reports whether a survey deferred by
// [Collector.noteLivenessAndCheckTransition]'s own settle gate is due, and
// clears the deferral if so. Called once per successful [Collector.Poll]
// cycle. Returns false, leaving any deferral in place, both when nothing
// is deferred and when the settle window has not yet elapsed — a later
// Poll call re-checks it the identical way.
func (c *Collector) takeDueDeferredTransitionSurvey(now time.Time) bool {
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()
	if c.transitionSurveyDueAt.IsZero() || now.Before(c.transitionSurveyDueAt) {
		return false
	}
	c.transitionSurveyDueAt = time.Time{}
	return true
}

// compositionRevisionChanged reports whether [Collector.compositionStore]'s
// currently loaded revision has moved since the last time this method
// observed it, and records the new value either way — see [Collector.Poll]'s
// own doc comment for why this trigger exists. Cheap:
// [CompositionStore.LoadedRevision] is a single atomic load, not a network
// call, so checking it on every liveness success carries no footprint
// concern of its own.
//
// Never rate-limited (unlike [Collector.noteLivenessAndCheckTransition]):
// an upload is an explicit operator action, not something Resolume can
// flap into existence repeatedly, so there is no storm to protect against
// here.
func (c *Collector) compositionRevisionChanged() bool {
	current := c.compositionStore.LoadedRevision()

	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()
	if current == c.lastSeenCompositionRevision {
		return false
	}
	c.lastSeenCompositionRevision = current
	return true
}

func (c *Collector) resource() observation.ResourceRef {
	return observation.ResourceRef{Kind: observation.ResourceResolume, ID: c.id}
}

// measured builds a StateCurrent observation via [observation.Measured],
// stamped with this Collector's source, ValidFor, and clock.
func (c *Collector) measured(sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) observation.Observation {
	allOpts := append([]observation.Option{
		observation.WithSource(sourceName),
		observation.WithValidFor(c.validFor),
		observation.WithCollectedAt(observedAt),
	}, opts...)

	o, err := observation.Measured(c.resource(), sig, value, observedAt, allOpts...)
	if err != nil {
		// Unreachable in practice: both call sites above pass a value type
		// Validate accepts (bool, string) and a non-empty signal/resource
		// ID. Surfaced as a collection_failed observation rather than
		// dropped or panicking, so a future bug in this file is visible on
		// the API instead of invisible — mirrors
		// internal/coordinator/collector/fpp.Collector.measured exactly.
		return c.failed(sig, fmt.Sprintf("internal error building observation: %v", err), observedAt)
	}
	return o
}

// failed builds a StateCollectionFailed observation via
// [observation.CollectionFailed]. reason must be non-empty; every call
// site already guarantees that.
func (c *Collector) failed(sig observation.SignalID, reason string, observedAt time.Time) observation.Observation {
	o, err := observation.CollectionFailed(c.resource(), sig, reason,
		observation.WithSource(sourceName), observation.WithCollectedAt(observedAt))
	if err != nil {
		// reason is non-empty and sig/resource are always set by this
		// file; a failure here is a bug in this file, not a runtime
		// condition to degrade gracefully from.
		panic(fmt.Sprintf("resolume collector %q: CollectionFailed(%q) unexpectedly failed: %v", c.id, sig, err))
	}
	return o
}

// --- survey-sourced observation builders ------------------------------
//
// Mirror [Collector.measured]/[Collector.failed] exactly, except stamped
// with [surveySourceName]/[Collector.surveyValidFor] instead of
// [sourceName]/[Collector.validFor] — see surveySourceName's own doc
// comment for why that distinction is load-bearing.

func (c *Collector) surveyMeasured(sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) observation.Observation {
	allOpts := append([]observation.Option{
		observation.WithSource(surveySourceName),
		observation.WithValidFor(c.surveyValidFor),
		observation.WithCollectedAt(observedAt),
	}, opts...)

	o, err := observation.Measured(c.resource(), sig, value, observedAt, allOpts...)
	if err != nil {
		return c.surveyFailed(sig, fmt.Sprintf("internal error building observation: %v", err), observedAt)
	}
	return o
}

// surveyAbsence builds a survey-sourced absence observation in state.
// state must be one of StateNotCollected, StateCollectionFailed, or
// StateUnsupported — the three [pkg/observation] absence states — anything
// else is a bug in this file, not a runtime condition, so it panics rather
// than degrading silently (mirroring [Collector.failed]'s own reasoning).
func (c *Collector) surveyAbsence(sig observation.SignalID, state observation.State, reason string, now time.Time) observation.Observation {
	opts := []observation.Option{observation.WithSource(surveySourceName), observation.WithCollectedAt(now)}

	var o observation.Observation
	var err error
	switch state {
	case observation.StateNotCollected:
		o, err = observation.NotCollected(c.resource(), sig, reason, opts...)
	case observation.StateCollectionFailed:
		o, err = observation.CollectionFailed(c.resource(), sig, reason, opts...)
	case observation.StateUnsupported:
		o, err = observation.Unsupported(c.resource(), sig, reason, opts...)
	default:
		panic(fmt.Sprintf("resolume collector %q: surveyAbsence called with unsupported state %q", c.id, state))
	}
	if err != nil {
		panic(fmt.Sprintf("resolume collector %q: building a %q observation for %q unexpectedly failed: %v", c.id, state, sig, err))
	}
	return o
}

func (c *Collector) surveyNotCollected(sig observation.SignalID, reason string, now time.Time) observation.Observation {
	return c.surveyAbsence(sig, observation.StateNotCollected, reason, now)
}

func (c *Collector) surveyFailed(sig observation.SignalID, reason string, now time.Time) observation.Observation {
	return c.surveyAbsence(sig, observation.StateCollectionFailed, reason, now)
}

func (c *Collector) surveyUnsupported(sig observation.SignalID, reason string, now time.Time) observation.Observation {
	return c.surveyAbsence(sig, observation.StateUnsupported, reason, now)
}

// --- Composition-level readiness terms and resolume.composition.name: permanently unavailable ---
//
// composition.bypassed, composition.master, and resolume.composition.name
// were once pursued through a ladder that tried GET /composition/{term}
// before reporting the term unreadable. Arena's own OpenAPI specification
// settled the question: there is no such path on this API — see client.go's
// own doc comment for the full reasoning — so the ladder is deleted rather
// than left switched off, and the two composition-level readiness terms
// plus the composition name are permanently unavailable by any path this
// package may use. The functions below replace what the ladder used to
// compute with a fixed, unconditional answer: never Known=true, never a
// measured value, always the same stated reason.

// compositionLevelUnavailableReason is the fixed, operator-facing text for
// every one of the three signals above — named once so all three state the
// identical fact rather than three slightly different sentences.
const compositionLevelUnavailableReason = "this Arena build does not expose this value without reading the full composition, which this system never does"

// compositionLevelReadinessTerm builds the fixed, permanently-unknown
// [ReadinessTermInput] for composition.bypassed or composition.master —
// never Known=true, because no path this package may use can ever read
// either term (see this section's own top comment). fieldLabel names the
// term the same way every other [ReadinessTermInput] in this file does.
func compositionLevelReadinessTerm(fieldLabel string) ReadinessTermInput {
	return ReadinessTermInput{UnknownReason: fieldLabel + ": " + compositionLevelUnavailableReason}
}

// compositionNameObservation builds resolume.composition.name's
// observation: always [observation.StateUnsupported], never a measured
// value and never backfilled from the uploaded composition file — the file
// states what ShowMesh expects, and reporting an expectation as an
// observation is the defect this project has caught repeatedly (CLAUDE.md).
func (c *Collector) compositionNameObservation(now time.Time) observation.Observation {
	return c.surveyUnsupported(SignalCompositionName, compositionLevelUnavailableReason, now)
}

// --- Track D seam D-2/C: the survey itself ---------------------------------

// deckReadResult, layerReadResult, groupReadResult, and clipFetchResult
// pair one by-id read's decoded value with its error, so every helper
// below can tell "resolved" from "did not" without a second lookup.
type deckReadResult struct {
	deck Deck
	err  error
}

type layerReadResult struct {
	layer Layer
	err   error
}

type groupReadResult struct {
	group LayerGroup
	err   error
}

type clipFetchResult struct {
	clip Clip
	err  error
}

func (c *Collector) readDecks(ctx context.Context, tc *TrackedComposition) map[ObjectID]deckReadResult {
	out := make(map[ObjectID]deckReadResult, len(tc.Decks()))
	for _, d := range tc.Decks() {
		deck, err := c.client.Deck(ctx, d.ID)
		out[d.ID] = deckReadResult{deck: deck, err: err}
	}
	return out
}

// selectedDeckFrom walks order (a TrackedComposition's own deck list, so
// the result is deterministic regardless of Go's randomized map iteration)
// and returns the first deck in results whose Selected field resolved
// present-and-true.
func selectedDeckFrom(order []TrackedDeck, results map[ObjectID]deckReadResult) (id ObjectID, name string, known bool) {
	for _, d := range order {
		r, ok := results[d.ID]
		if !ok || r.err != nil {
			continue
		}
		if selected, ok := r.deck.Selected.Bool(); ok && selected {
			n, _ := r.deck.Name.String()
			return d.ID, n, true
		}
	}
	return 0, "", false
}

func (c *Collector) readLayers(ctx context.Context, tc *TrackedComposition) map[ObjectID]layerReadResult {
	out := make(map[ObjectID]layerReadResult, len(tc.Layers()))
	for _, l := range tc.Layers() {
		layer, err := c.client.Layer(ctx, l.ID)
		out[l.ID] = layerReadResult{layer: layer, err: err}
	}
	return out
}

func (c *Collector) readGroups(ctx context.Context, tc *TrackedComposition) map[ObjectID]groupReadResult {
	out := make(map[ObjectID]groupReadResult, len(tc.LayerGroups()))
	for _, g := range tc.LayerGroups() {
		group, err := c.client.LayerGroup(ctx, g.ID)
		out[g.ID] = groupReadResult{group: group, err: err}
	}
	return out
}

// clipFetchOrder builds the deduplicated, deterministically ordered list of
// clip ids one survey must fetch: every id §6's identity sample names,
// plus every tracked layer's currently-resolved active_clip id (§3.4's
// own bound — "per-clip signals exist only for clips that are currently a
// tracked layer's active_clip, plus the four PersistentClips"). The two
// sets overlap but are not identical (a sampled deck clip need not be
// anyone's active_clip), so this is a union, deduplicated once, not two
// separate fetch passes.
func clipFetchOrder(sample IdentitySample, layers []TrackedLayer, layerResults map[ObjectID]layerReadResult) []ObjectID {
	seen := make(map[ObjectID]bool)
	var order []ObjectID
	add := func(id ObjectID) {
		if seen[id] {
			return
		}
		seen[id] = true
		order = append(order, id)
	}
	for _, sc := range sample.DeckClips {
		add(sc.ID)
	}
	for _, sc := range sample.PersistentClips {
		add(sc.ID)
	}
	for _, l := range layers {
		r, ok := layerResults[l.ID]
		if !ok || r.err != nil {
			continue
		}
		if r.layer.ActiveClip.Presence == PresencePresent {
			add(r.layer.ActiveClip.Clip.ID)
		}
	}
	return order
}

func (c *Collector) fetchClips(ctx context.Context, ids []ObjectID) map[ObjectID]clipFetchResult {
	out := make(map[ObjectID]clipFetchResult, len(ids))
	for _, id := range ids {
		clip, err := c.client.Clip(ctx, id)
		out[id] = clipFetchResult{clip: clip, err: err}
	}
	return out
}

// deckClipMissing reports whether any of sample's DeckClips failed to
// resolve — the trigger for a deck recheck (§6.4's own "the deck reading
// is itself evidence and is fenced" rule): a recheck costs a handful of
// extra requests and is only worth issuing when there is something to
// explain.
func deckClipMissing(sample IdentitySample, resolved map[ObjectID]bool) bool {
	for _, sc := range sample.DeckClips {
		if !resolved[sc.ID] {
			return true
		}
	}
	return false
}

// survey performs Track D seam D-2/C's own composition survey
// (TRACK-D-D2-SPEC.md §3.1's "survey" read mode): every tracked layer,
// layer group and deck by id, the composition-identity sample (§6), and
// every clip either that sample names or a layer's active_clip names —
// never GET /composition, and never on a timer (see [Collector.RequestSurvey]).
// Called only from [Collector.Poll], holding surveyMu for its entire
// duration.
//
// Every by-id read this method performs is treated as having been
// observed at surveyedAt, the single timestamp this call was entered
// with, rather than a separately tracked time per request: a survey
// completes in low single-digit seconds, and pkg/observation's own
// ValidFor/staleness machinery is what governs freshness afterward, so
// threading a distinct clock reading through roughly thirty individual
// by-id calls would add real complexity for no evidence anyone could act
// on differently. composition.bypassed and composition.master carry no
// such exception any longer — they are permanently unavailable (defect 2,
// 2026-08-15) and never contribute an observedAt of their own at all; see
// [compositionLevelReadinessTerm].
func (c *Collector) survey(ctx context.Context, afterReconnect bool, surveyedAt time.Time) []observation.Observation {
	c.surveyMu.Lock()
	defer c.surveyMu.Unlock()

	if afterReconnect {
		// TRACK-D-D2-SPEC.md §7: a reconnect reopens the load window. If
		// THIS survey's own identity check resolves determinately below,
		// it closes again in the same call — see the identityConfirmed
		// update further down.
		c.identityConfirmed = false
	}

	tc, err := c.compositionStore.Current()
	if err != nil {
		c.recordSurveySnapshot(SurveySnapshot{
			SurveyRan: true, SurveyAt: surveyedAt,
			IdentityKnown: true, Identity: IdentityUnknown, IdentityObservedAt: surveyedAt,
		})
		return c.surveyNoCompositionObservations(surveyedAt)
	}

	// Pass 1: every tracked deck, to pick the identity sample's deck.
	deckResults := c.readDecks(ctx, tc)
	selectedID, selectedName, selectedKnown := selectedDeckFrom(tc.Decks(), deckResults)

	// selectedID is the zero ObjectID when !selectedKnown; IdentitySample's
	// own doc comment already treats a deck this composition does not
	// contain (or the zero value) as "not an error, simply zero
	// DeckClips" — CheckIdentity's own "nothing resolved" branch is what
	// absorbs that case into IdentityUnknown, never a false or a deck
	// mismatch invented from missing information.
	sample := tc.IdentitySample(selectedID)

	layerResults := c.readLayers(ctx, tc)
	groupResults := c.readGroups(ctx, tc)

	clipIDs := clipFetchOrder(sample, tc.Layers(), layerResults)
	clipResults := c.fetchClips(ctx, clipIDs)

	resolved := make(map[ObjectID]bool, len(clipResults))
	for id, r := range clipResults {
		resolved[id] = r.err == nil
	}

	var recheck *DeckRecheck
	if selectedKnown && deckClipMissing(sample, resolved) {
		recheckResults := c.readDecks(ctx, tc)
		stillID, stillName, stillKnown := selectedDeckFrom(tc.Decks(), recheckResults)
		recheck = &DeckRecheck{
			StillSelected:        stillKnown && stillID == selectedID,
			CurrentSelectedKnown: stillKnown,
			CurrentSelectedID:    stillID,
			CurrentSelectedName:  stillName,
		}
	}

	identity := CheckIdentity(IdentityCheck{Sample: sample, Resolved: resolved, DeckRecheck: recheck})
	if identity.Outcome == IdentityTrue || identity.Outcome == IdentityFalse {
		c.identityConfirmed = true
	}
	loadWindow := !c.identityConfirmed

	// Track D seam D-3a §4 rule 2/§2.3, gated on THIS survey confirming
	// our composition is actually loaded (review finding B2): an
	// unconfirmed-identity survey — mid-load-window, or reading the wrong
	// composition — is not evidence about our composition and must never
	// demote an action-sourced recovery entry, because that demotion is
	// exactly what left the manual restore with nothing usable after a
	// crash. Fed from the reads this survey already performed, never a
	// separate poll of its own (criterion 8).
	if identity.Outcome == IdentityTrue {
		c.recoveryUpdateFromSurvey(layerResults, surveyedAt)
	}

	// Track D seam D-3's action vocabulary consumes this survey's identity
	// and selected-deck result — never a fresh read of its own — via
	// [Collector.LastSurveySnapshot]. Recorded here, once, after both are
	// known for this survey, using this survey's own IdentityOutcome
	// UNCHANGED by the load-window handling below: a snapshot reader (a
	// pre-dispatch guard) needs the real classification rather than the
	// load-window-adjusted "unknown" string the identified-signal
	// observation below renders for an operator dashboard — see
	// TRACK-D-D3-SPEC.md §3.6's own "consumes it, does not recompute it."
	snapIdentity := identity.Outcome
	if loadWindow {
		snapIdentity = IdentityUnknown
	}
	c.recordSurveySnapshot(SurveySnapshot{
		SurveyRan: true, SurveyAt: surveyedAt,
		IdentityKnown: true, Identity: snapIdentity, IdentityObservedAt: surveyedAt,
		SelectedDeckKnown: selectedKnown, SelectedDeckID: selectedID, SelectedDeckName: selectedName, SelectedDeckObservedAt: surveyedAt,
	})

	var obs []observation.Observation
	obs = append(obs, c.compositionNameObservation(surveyedAt))
	obs = append(obs, c.identityObservation(identity, loadWindow, surveyedAt))
	obs = append(obs, c.deckObservations(deckResults, tc.Decks(), selectedID, selectedName, selectedKnown, surveyedAt)...)
	obs = append(obs, c.layerObservations(tc.Layers(), layerResults, groupResults, loadWindow, surveyedAt)...)
	obs = append(obs, c.clipObservations(clipIDs, clipResults, surveyedAt)...)

	return obs
}

func (c *Collector) surveyNoCompositionObservations(now time.Time) []observation.Observation {
	reason := "no composition has been uploaded to this coordinator yet"
	return []observation.Observation{
		c.surveyNotCollected(SignalCompositionName, reason, now),
		c.surveyNotCollected(SignalCompositionIdentified, reason, now),
		c.surveyNotCollected(SignalCompositionDecks, reason, now),
		c.surveyNotCollected(SignalCompositionSelectedDeck, reason, now),
	}
}

// identityObservation builds resolume.composition.identified's observation.
// A deck mismatch is not skipped: the survey's other resolume-survey rows
// are delivered complete=true in the same batch (survey's own caller), so
// omitting this signal here does not leave it "unchanged" — ReplaceObservations
// prunes any resolume-survey row not present in a complete=true batch, which
// deletes the evidence instead of preserving it.
func (c *Collector) identityObservation(identity IdentityResult, loadWindow bool, now time.Time) observation.Observation {
	if identity.Outcome == IdentityDeckMismatch {
		return c.surveyMeasured(SignalCompositionIdentified, "deck_mismatch: "+identity.Reason+" ("+deckMismatchDetail(identity)+")", now)
	}

	switch {
	case loadWindow:
		return c.surveyMeasured(SignalCompositionIdentified,
			"unknown: still within the post-connect load window; Resolume may not have finished loading the composition yet", now)
	case identity.Outcome == IdentityTrue:
		return c.surveyMeasured(SignalCompositionIdentified, "identified", now)
	case identity.Outcome == IdentityFalse:
		return c.surveyMeasured(SignalCompositionIdentified, "not_identified: "+identity.Reason+formatMissing(identity.MissingIDs), now)
	default: // IdentityUnknown
		return c.surveyMeasured(SignalCompositionIdentified, "unknown: "+identity.Reason, now)
	}
}

// deckMismatchDetail names both decks per §6.4's "naming both decks"
// requirement, using the same formatRef every other deck/clip reference in
// this file uses.
func deckMismatchDetail(identity IdentityResult) string {
	expected := "expected deck " + formatRef(identity.ExpectedDeck.ID, "")
	if !identity.ActualDeckKnown {
		return expected + ", now selected deck could not be re-identified"
	}
	return expected + ", now selected " + formatRef(identity.ActualDeck, identity.ActualDeckName)
}

func formatMissing(ids []IdentitySampleClip) string {
	if len(ids) == 0 {
		return ""
	}
	s := " ("
	for i, ref := range ids {
		if i > 0 {
			s += ", "
		}
		s += formatRef(ref.ID, ref.Name)
	}
	return s + ")"
}

// formatRef renders an object id with its name when known, or the bare id
// otherwise — shared by every observation value in this file that names a
// deck or a clip, so the two never drift into slightly different formats.
func formatRef(id ObjectID, name string) string {
	if name == "" {
		return fmt.Sprintf("id %s", id)
	}
	return fmt.Sprintf("%s (id %s)", name, id)
}

func (c *Collector) deckObservations(results map[ObjectID]deckReadResult, order []TrackedDeck, selectedID ObjectID, selectedName string, selectedKnown bool, now time.Time) []observation.Observation {
	count := 0
	for _, d := range order {
		if r, ok := results[d.ID]; ok && r.err == nil {
			count++
		}
	}

	var selectedValue string
	if selectedKnown {
		selectedValue = formatRef(selectedID, selectedName)
	} else {
		selectedValue = "unknown: no tracked deck currently reports itself selected"
	}

	return []observation.Observation{
		c.surveyMeasured(SignalCompositionDecks, int64(count), now),
		c.surveyMeasured(SignalCompositionSelectedDeck, selectedValue, now),
	}
}

// readinessObservedAt derives one layer readiness observation's ObservedAt
// from its own [Readiness]: the real evidence time for Ready/NotReady, or
// now for Unknown, since [Readiness.ObservedAt] is deliberately the zero
// time for an unknown verdict — see that field's own doc comment for why
// there is no "evidence time" to report for the absence of a definite
// answer, and why that is a different kind of uncertainty than
// [observation.MeasuredUnknownAge] exists for (this file's own report
// covers that distinction).
func readinessObservedAt(r Readiness, now time.Time) time.Time {
	if r.ObservedAt.IsZero() {
		return now
	}
	return r.ObservedAt
}

func formatReadiness(r Readiness) string {
	switch r.State {
	case ReadinessReady:
		return "ready"
	case ReadinessNotReady:
		return "not_ready: " + joinTerms(r.FailingTerms)
	default:
		return "unknown: " + joinTermsWithReasons(r.UnknownTerms, r.UnknownReasons)
	}
}

func joinTerms(terms []ReadinessTerm) string {
	s := ""
	for i, t := range terms {
		if i > 0 {
			s += ", "
		}
		s += string(t)
	}
	return s
}

func joinTermsWithReasons(terms []ReadinessTerm, reasons []string) string {
	s := ""
	for i, t := range terms {
		if i > 0 {
			s += "; "
		}
		s += string(t) + " (" + reasons[i] + ")"
	}
	return s
}

// soloParam reports whether f's own solo field is confirmed true — see
// [ParamBooleanField.Bool]'s own doc comment: only a fully-present envelope
// AND value counts, exactly like every other consumer in this package.
func soloParam(f ParamBooleanField) bool {
	v, ok := f.Bool()
	return ok && v
}

// compositionHasActiveSolo reports whether ANY tracked layer or layer group
// in this survey reports solo=true — the global condition
// [ApplySoloOverride] needs, computed once per survey rather than once per
// layer, since it depends on every OTHER layer/group too, not only the one
// [layerReadyObservation] is currently building a verdict for.
func compositionHasActiveSolo(layerResults map[ObjectID]layerReadResult, groupResults map[ObjectID]groupReadResult) bool {
	for _, r := range layerResults {
		if r.err == nil && soloParam(r.layer.Solo) {
			return true
		}
	}
	for _, r := range groupResults {
		if r.err == nil && soloParam(r.group.Solo) {
			return true
		}
	}
	return false
}

// layerIsSoloed reports whether l itself is exempt from
// [ApplySoloOverride]: its own solo field is true, or its containing
// group's is. A layer whose solo state cannot be read (an errored or
// missing read) is treated as NOT soloed — the conservative direction,
// since a false negative here only ever means the override is applied,
// never that it is skipped when it should not be.
func layerIsSoloed(l TrackedLayer, layer Layer, groupResults map[ObjectID]groupReadResult) bool {
	if soloParam(layer.Solo) {
		return true
	}
	if l.LayerGroupID != nil {
		if gr, ok := groupResults[*l.LayerGroupID]; ok && gr.err == nil {
			return soloParam(gr.group.Solo)
		}
	}
	return false
}

func (c *Collector) layerObservations(order []TrackedLayer, layerResults map[ObjectID]layerReadResult, groupResults map[ObjectID]groupReadResult, loadWindow bool, now time.Time) []observation.Observation {
	soloActive := compositionHasActiveSolo(layerResults, groupResults)

	var obs []observation.Observation
	for _, l := range order {
		lr, ok := layerResults[l.ID]
		if !ok || lr.err != nil {
			reason := "layer by-id read did not complete"
			if ok {
				reason = ClassifyError(lr.err)
			}
			obs = append(obs, c.surveyFailed(LayerReadySignal(l.ID), reason, now))
			obs = append(obs, c.surveyFailed(LayerActiveClipSignal(l.ID), reason, now))
			continue
		}

		thisSoloed := layerIsSoloed(l, lr.layer, groupResults)
		obs = append(obs, c.layerReadyObservation(l, lr.layer, groupResults, loadWindow, soloActive, thisSoloed, now))
		obs = append(obs, c.layerActiveClipObservation(l.ID, lr.layer, now))
	}
	return obs
}

func (c *Collector) layerReadyObservation(l TrackedLayer, layer Layer, groupResults map[ObjectID]groupReadResult, loadWindow bool, soloActive bool, thisSoloed bool, now time.Time) observation.Observation {
	if loadWindow {
		return c.surveyMeasured(LayerReadySignal(l.ID),
			"unknown: still within the post-connect load window; Resolume may not have finished loading the composition yet", now)
	}

	in := ReadinessInputs{
		LayerBypassed:     boolTermHoldsWhenFalse(layer.Bypassed, now, string(ReadinessTermLayerBypassed)),
		LayerMaster:       rangeTermHoldsWhenPositive(layer.Master, now, string(ReadinessTermLayerMaster)),
		LayerVideoOpacity: rangeTermHoldsWhenPositive(layer.VideoOpacity(), now, string(ReadinessTermLayerVideoOpacity)),
	}

	if l.LayerGroupID != nil {
		if gr, ok := groupResults[*l.LayerGroupID]; ok && gr.err == nil {
			in.GroupBypassed = boolTermHoldsWhenFalse(gr.group.Bypassed, now, string(ReadinessTermGroupBypassed))
			in.GroupMaster = rangeTermHoldsWhenPositive(gr.group.Master, now, string(ReadinessTermGroupMaster))
		} else {
			reason := "layer group by-id read did not complete"
			if ok {
				reason = ClassifyError(gr.err)
			}
			in.GroupBypassed = ReadinessTermInput{UnknownReason: reason}
			in.GroupMaster = ReadinessTermInput{UnknownReason: reason}
		}
	} else {
		reason := "this layer has no layer group in the uploaded composition"
		in.GroupBypassed = ReadinessTermInput{UnknownReason: reason}
		in.GroupMaster = ReadinessTermInput{UnknownReason: reason}
	}

	in.CompositionBypassed = compositionLevelReadinessTerm(string(ReadinessTermCompositionBypassed))
	in.CompositionMaster = compositionLevelReadinessTerm(string(ReadinessTermCompositionMaster))

	readiness := ApplySoloOverride(LayerReady(in), soloActive, thisSoloed)
	return c.surveyMeasured(LayerReadySignal(l.ID), formatReadiness(readiness), readinessObservedAt(readiness, now))
}

// activeClipNoneValue is resolume.layer.<id>.active_clip's explicit,
// non-empty MEASURED value when Resolume reports "active_clip": null
// (capture section 4.4) — this task's own "defect 1" fix, 2026-08-14. A
// null active_clip is a real, successfully-measured fact ("nothing is
// playing on this layer right now"), not an absence of evidence, so it is
// carried as a value, never collapsed to "" (CLAUDE.md: "a missing field
// renders as blank and blank reads as fine") and never confused with
// PresenceAbsent below, which genuinely IS an absence of evidence.
// Deliberately shaped so it can never be mistaken for [formatRef]'s "id N"
// / "name (id N)" rendering of an actual clip reference.
const activeClipNoneValue = "none: no clip is connected on this layer"

// layerActiveClipObservation formats a layer's active_clip field as three
// distinguishable outcomes, per [Presence] — fixed 2026-08-14 (this task's
// own "defect 1"): the previous version collapsed PresenceNull (Resolume
// explicitly reported nothing playing) and PresenceAbsent (the key was
// missing from the response entirely — ShowMesh does not know) into the
// SAME measured empty string, which is both the "missing field renders as
// blank" defect CLAUDE.md names and a loss of a real distinction between
// two different facts.
func (c *Collector) layerActiveClipObservation(layerID ObjectID, layer Layer, now time.Time) observation.Observation {
	switch layer.ActiveClip.Presence {
	case PresencePresent:
		return c.surveyMeasured(LayerActiveClipSignal(layerID), formatRef(layer.ActiveClip.Clip.ID, ""), now)
	case PresenceNull:
		// A real, successfully measured value — capture section 4.4's own
		// observed shape for "nothing playing" — never an absence.
		return c.surveyMeasured(LayerActiveClipSignal(layerID), activeClipNoneValue, now)
	default: // PresenceAbsent
		// An attempt was made (the layer object itself decoded fine — this
		// method is only ever called with one) and this one field could not
		// be obtained: [observation.CollectionFailed]'s own definition,
		// never a fabricated "nothing playing" and never a blank string.
		return c.surveyFailed(LayerActiveClipSignal(layerID),
			"the active_clip field was absent from Resolume's response for this layer", now)
	}
}

// clipObservations builds resolume.clip.<id>.connected and
// .transporttype for every fetched clip, walking order (clipFetchOrder's
// own deterministic sequence) rather than ranging over the results map
// directly, so the returned slice's ordering never depends on Go's
// randomized map iteration.
func (c *Collector) clipObservations(order []ObjectID, results map[ObjectID]clipFetchResult, now time.Time) []observation.Observation {
	var obs []observation.Observation
	for _, id := range order {
		r := results[id]
		if r.err != nil {
			reason := ClassifyError(r.err)
			if IsNotFound(r.err) {
				reason = "no longer resolves (404) — see the layer/composition identity signals for whether this is a stale reference or a deck mismatch"
			}
			obs = append(obs, c.surveyFailed(ClipConnectedSignal(id), reason, now))
			obs = append(obs, c.surveyFailed(ClipTransportTypeSignal(id), reason, now))
			continue
		}

		obs = append(obs, c.clipConnectedObservation(id, r.clip, now))
		obs = append(obs, c.clipTransportTypeObservation(id, r.clip, now))
	}
	return obs
}

// clipConnectedObservation formats one clip's `connected` field as three
// distinguishable outcomes, per [Presence] — this task's own "defect 1"
// fix, 2026-08-14: the previous version collapsed PresenceNull and
// PresenceAbsent into the same measured empty string, exactly the pattern
// fixed for [Collector.layerActiveClipObservation] above. Unlike
// active_clip's null, this package has no capture evidence that
// "connected": null carries any established meaning of its own (the
// five-state Connected/Disconnected/Previewing/etc. vocabulary already has
// its own way to say "nothing here" — "Empty" — so a null here is not
// known to mean the same thing), so both PresenceNull and PresenceAbsent
// are reported as StateCollectionFailed rather than a fabricated measured
// value, with distinct reasons so which one occurred is still visible.
func (c *Collector) clipConnectedObservation(id ObjectID, clip Clip, now time.Time) observation.Observation {
	if v, ok := clip.Connected.String(); ok {
		return c.surveyMeasured(ClipConnectedSignal(id), v, now)
	}
	switch clip.Connected.Presence {
	case PresenceNull:
		return c.surveyFailed(ClipConnectedSignal(id), "the connected field was explicitly null in Resolume's response for this clip", now)
	case PresencePresent:
		// The envelope arrived, but its own "value" key did not — capture
		// §17.3's own headline finding, applied here: no schema in Arena's
		// specification requires "value" at all.
		return c.surveyFailed(ClipConnectedSignal(id), "the connected field answered but its value was absent from Resolume's response for this clip", now)
	default: // PresenceAbsent
		return c.surveyFailed(ClipConnectedSignal(id), "the connected field was absent from Resolume's response for this clip", now)
	}
}

// clipTransportTypeObservation formats one clip's `transporttype` field.
// See [Collector.clipConnectedObservation]'s doc comment; the shape and the
// reasoning are identical.
func (c *Collector) clipTransportTypeObservation(id ObjectID, clip Clip, now time.Time) observation.Observation {
	if v, ok := clip.TransportType.String(); ok {
		return c.surveyMeasured(ClipTransportTypeSignal(id), v, now)
	}
	switch clip.TransportType.Presence {
	case PresenceNull:
		return c.surveyFailed(ClipTransportTypeSignal(id), "the transporttype field was explicitly null in Resolume's response for this clip", now)
	case PresencePresent:
		return c.surveyFailed(ClipTransportTypeSignal(id), "the transporttype field answered but its value was absent from Resolume's response for this clip", now)
	default: // PresenceAbsent
		return c.surveyFailed(ClipTransportTypeSignal(id), "the transporttype field was absent from Resolume's response for this clip", now)
	}
}
