// Package collector holds the small, source-neutral shape every
// observation source in the coordinator implements, and a Runner that owns
// their lifecycle (start, poll on a cadence, stop cleanly).
//
// This package is deliberately not a plugin framework. Step 3 ships one
// concrete Collector — internal/coordinator/collector/fpp — but the
// interface exists so a second source (FPP MQTT, PJLink, NUT; see the Step
// 3 contract section 5.5 for what is explicitly out of scope for now) slots
// in later without reshaping anything here. Do not add configuration,
// registries, or hooks this package has no second implementation to
// justify.
package collector

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector is a source of observations for one or more resources. A
// Collector owns its own identity, its own request timeouts, and its own
// failure handling: Poll must apply whatever timeout is appropriate for
// this source internally (per OBSERVABILITY section 5's requirement that
// collectors "apply bounded timeouts, backoff, and concurrency limits so
// monitoring cannot impair show devices") and must not block past ctx's
// cancellation.
//
// Poll deliberately returns no error. A network failure, a decode failure,
// an unreachable device — all of these are themselves observations (an
// absence, per pkg/observation's StateCollectionFailed), not a Go error the
// caller has to separately interpret. This is the same discipline the FPP
// collector's design note makes explicit: "never let a failed read become
// a negative answer" applies just as much to "never let it become a
// silently dropped answer". A Collector implementation that panics or
// returns is a bug in that implementation, not a condition Runner tries to
// recover from generically.
type Collector interface {
	// ID identifies this collector for logging and the API's collectors[]
	// list (Step 3 contract section 6.10's snapshot shape). Stable for the
	// lifetime of the process.
	ID() string

	// Poll performs one collection cycle and returns the observations
	// produced, plus a completeness claim about them.
	//
	// observations may be empty (for example, a Collector deliberately
	// skipping this cycle under backoff) without that being an error.
	//
	// complete answers a question distinct from "did anything fail":
	// is observations the FULL current set of every signal this Collector
	// owns for the resource(s) it is reporting on this cycle, such that any
	// previously-stored signal for the same (resource, source) NOT present
	// in observations is known to no longer exist (a removed sensor, a
	// port that dropped out of a reconfigured cape, an instance that now
	// reports nothing where it once reported many) — as opposed to simply
	// "not (re-)checked this cycle" for a reason that says nothing about
	// whether it still exists.
	//
	// This is the distinction a Sink needs to prune stale rows safely: a
	// skipped-under-backoff poll returning zero observations must NEVER be
	// read as "this source now owns zero signals" (see the fpp package's
	// backoff-skip path, which returns complete=false for exactly this
	// reason), while a poll that reports collection_failed for every signal
	// it knows about — a real attempt that found the instance unreachable —
	// legitimately IS the complete current answer and may replace whatever
	// was stored before. A Collector that has no notion of a partial or
	// skipped cycle at all (see the fppmqtt package, which always renders
	// every statically-known signal for every configured host, using an
	// absence state rather than omission when nothing has arrived yet) may
	// always return complete=true.
	Poll(ctx context.Context) (observations []observation.Observation, complete bool)
}

// Sink is where a Runner delivers each poll's observations. Declared here,
// at the consumer, per the Step 3 contract section 5's "declare interfaces
// at the consumer, not the producer": this package does not import
// internal/coordinator/store, so Task C (this package and
// internal/coordinator/collector/fpp) does not block on Task B's store
// package existing or its exact method names. Whatever wires the two
// together (Task F, in internal/coordinator/coordinator.go) supplies an
// adapter that satisfies this interface.
type Sink interface {
	// RecordObservations delivers the observations from one Poll call,
	// along with that call's completeness claim (see [Collector.Poll]).
	// Implementations must not block indefinitely: a slow or wedged sink
	// would otherwise stall every collector sharing this Runner.
	//
	// A Sink that prunes stale rows using complete must scope that pruning
	// to exactly the (resource, source) pairs present in observations this
	// call — complete says nothing about a resource this Collector has
	// never mentioned at all.
	RecordObservations(ctx context.Context, observations []observation.Observation, complete bool)
}

// entry pairs one Collector with its own poll interval and its own
// out-of-band poll request channel (see [Runner.Nudge]), so different
// sources can be polled at different cadences from a single Runner, and a
// caller elsewhere in the process can ask any one of them to poll sooner
// than its own cadence without touching the others.
type entry struct {
	collector Collector
	interval  time.Duration

	// nudge is buffered 1: a pending, not-yet-consumed request coalesces
	// with a second one rather than queuing (see Nudge) — this collector
	// is about to poll anyway, and a second "poll now" before the first
	// has even run adds nothing.
	nudge chan struct{}
}

// DefaultNudgeMinInterval is [Runner]'s default minimum spacing between two
// accepted [Runner.Nudge] calls for the same collector id. SHOWMESH
// HYPOTHESIS, NOT MEASURED — RES-009 (failure-mode testing) owns real
// evidence; this exists only to keep a burst of dispatched commands (or,
// once Step 9 ships, a macro's own sequence of primitives) from turning
// into a poll storm against one live FPP host, per OBSERVABILITY section
// 5's "monitoring cannot impair show devices." Chosen short enough that
// back-to-back commands touching DIFFERENT instances are each still
// nudged (Nudge is keyed per id, not global), and long enough that two
// commands touching the SAME instance within one confirmation wait cannot
// each demand their own out-of-band poll.
const DefaultNudgeMinInterval = 2 * time.Second

// Runner owns the lifecycle of a set of Collectors: it polls each on its
// own cadence, delivers every poll's observations to a Sink, and stops all
// of them cleanly when its context is cancelled. Runner has no goroutine or
// timer of its own until Run is called, and Run does not return until
// every collector's loop has exited — a sync.WaitGroup, the same mechanism
// the Step 3 contract requires of the SSE hub's shutdown (section 6.4) to
// make "no leaked goroutines" a property a test can assert rather than a
// hope.
//
// A zero-value Runner is not usable; construct one with NewRunner.
type Runner struct {
	sink    Sink
	logger  *slog.Logger
	entries []entry

	nudgeMinInterval time.Duration
	now              func() time.Time

	// mu guards nudgeChans and lastNudgeAt below — the only Runner state
	// [Nudge] touches, and the only Runner state ever written after Run
	// has started (entries itself is only ever appended by Add, which must
	// be called before Run — see Add's own doc comment). Nudge can be
	// called concurrently with itself, from any number of goroutines
	// (concurrent command dispatches), which entries' own single-threaded
	// construction never has to tolerate.
	mu          sync.Mutex
	nudgeChans  map[string]chan struct{}
	lastNudgeAt map[string]time.Time
}

// RunnerOption configures optional [Runner] behavior not every caller needs
// to override — see [WithNudgeMinInterval] and [WithNudgeClock].
type RunnerOption func(*Runner)

// WithNudgeMinInterval overrides [DefaultNudgeMinInterval].
func WithNudgeMinInterval(d time.Duration) RunnerOption {
	return func(r *Runner) { r.nudgeMinInterval = d }
}

// WithNudgeClock overrides the clock [Runner.Nudge]'s rate limiting reads.
// Defaults to time.Now; production has no reason to override this — it
// exists so a test can prove the rate limit's boundary deterministically
// instead of racing real wall-clock time.
func WithNudgeClock(now func() time.Time) RunnerOption {
	return func(r *Runner) { r.now = now }
}

// NewRunner constructs a Runner that delivers to sink and logs through
// logger. logger must not be nil; pass slog.Default() if the caller has no
// specific logger.
func NewRunner(sink Sink, logger *slog.Logger, opts ...RunnerOption) *Runner {
	r := &Runner{
		sink:             sink,
		logger:           logger,
		nudgeMinInterval: DefaultNudgeMinInterval,
		now:              time.Now,
		nudgeChans:       make(map[string]chan struct{}),
		lastNudgeAt:      make(map[string]time.Time),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Add registers a Collector to be polled every interval once Run starts.
// Add must be called before Run; adding a collector after Run has started
// has no effect on that Run call (Run snapshots the registered collectors
// once, at entry, rather than supporting a dynamic registry this package
// has no current need for). interval must be positive; Add panics
// otherwise, the same way e.g. time.NewTicker does — a zero or negative
// poll interval is a programming error to catch at wiring time, not a
// runtime condition to handle gracefully.
func (r *Runner) Add(c Collector, interval time.Duration) {
	if interval <= 0 {
		panic("collector: Add: interval must be positive")
	}
	ch := make(chan struct{}, 1)
	r.entries = append(r.entries, entry{collector: c, interval: interval, nudge: ch})
	r.nudgeChans[c.ID()] = ch
}

// Nudge requests an out-of-band poll of the collector registered under id,
// as soon as its current (or next) ordinary Poll call returns, instead of
// waiting out its own interval. It changes WHEN that collector's next
// ordinary poll happens and nothing else: the observations it produces,
// and how a caller decides what they mean, are entirely unaffected —
// Nudge itself never manufactures an observation and is not evidence of
// anything.
//
// Nudge is rate-limited per id via [DefaultNudgeMinInterval] (or
// [WithNudgeMinInterval]'s override): a call for an id nudged more
// recently than that interval is suppressed. So is a call for an id with
// an already-pending, not-yet-consumed nudge (the buffered channel is full
// — see [entry.nudge]'s doc comment), and a call for an id [Add] was never
// given. None of these are errors: Nudge returns false and the caller's
// only correct response is to do nothing — the collector's own scheduled
// cadence is entirely unaffected either way. This degrade-on-suppression
// behavior is deliberate, not a limitation to work around: see this
// method's callers for why a suppressed or unknown nudge must never become
// a failure the operator sees.
//
// Safe to call concurrently, from any goroutine, at any time, including
// before Run starts (the request is simply queued for the collector's
// first poll, which happens immediately on Run's own start regardless —
// see loop) and after ctx has been cancelled (a no-op once nothing is
// listening any more; the buffered send never blocks).
func (r *Runner) Nudge(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	ch, ok := r.nudgeChans[id]
	if !ok {
		return false
	}

	now := r.now()
	if last, seen := r.lastNudgeAt[id]; seen && now.Sub(last) < r.nudgeMinInterval {
		return false
	}

	select {
	case ch <- struct{}{}:
		r.lastNudgeAt[id] = now
		return true
	default:
		// A nudge is already queued for this id and has not been consumed
		// yet — coalesce rather than block or accumulate a second one.
		return false
	}
}

// Run polls every registered collector on its own cadence until ctx is
// cancelled, then waits for every poll loop to exit before returning. Each
// collector gets its own goroutine and its own self-paced timer (started
// fresh after each Poll call returns, not a shared ticker), which is what
// keeps a slow collector's Poll call from ever overlapping with itself: the
// next tick is scheduled only after the previous Poll has already
// returned. See the fpp package's Collector for why this single property —
// enforced here, once, for every Collector — is what satisfies "never two
// in-flight requests to the same instance" without that package needing
// any in-flight bookkeeping of its own.
//
// Run returns once every collector's loop has exited, so a caller doing
// `go runner.Run(ctx); cancel(); <assert goroutine count back to baseline>`
// gets an accurate answer: nothing is left running in the background after
// Run returns.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, e := range r.entries {
		wg.Add(1)
		go func(e entry) {
			defer wg.Done()
			r.loop(ctx, e)
		}(e)
	}
	wg.Wait()
}

// loop drives one collector: poll immediately, then wait interval (or
// ctx.Done, or a [Nudge] for this same entry) before polling again. An
// immediate first poll means a freshly-started collector's evidence
// appears without waiting a full interval, which matters for Step 3's
// not_collected story: an operator checking the API right after startup
// should see a real first attempt's result, not "not collected" for up to
// a full poll interval longer than necessary.
//
// A nudge received while this loop is between the timer's start and its
// fire (the only window it can be observed at all — see e.nudge's own doc
// comment for why a nudge received DURING Poll itself is still honored,
// just slightly delayed) cuts interval's remaining wait short and loops
// back to poll immediately, exactly like an ordinary tick — the collector
// this delivers to has no notion of "why now", only "poll now", which is
// what keeps this the smallest addition that fits Poll's existing
// contract (never called concurrently with itself for the same
// collector — see the fpp package's own serialization contract, unaffected
// by this: a nudge changes only how soon the NEXT call happens).
func (r *Runner) loop(ctx context.Context, e entry) {
	for {
		obs, complete := e.collector.Poll(ctx)
		if ctx.Err() != nil {
			// Cancelled during (or exactly at the end of) this Poll call:
			// do not deliver a possibly-partial result from a shutdown in
			// progress. The sink itself may already be tearing down.
			return
		}
		r.sink.RecordObservations(ctx, obs, complete)

		timer := time.NewTimer(e.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-e.nudge:
			// Deliberately no timer.Stop() drain: this timer is discarded
			// either way (a fresh one is created on the next iteration),
			// and Stop's return value only matters to a caller that reuses
			// the timer or must free its channel for GC before the timer
			// fires — neither applies here.
			timer.Stop()
		}
	}
}
