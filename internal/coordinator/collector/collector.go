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
	// produced. It may return an empty slice (for example, a Collector
	// deliberately skipping this cycle under backoff) without that being
	// an error.
	Poll(ctx context.Context) []observation.Observation
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
	// RecordObservations delivers the observations from one Poll call.
	// Implementations must not block indefinitely: a slow or wedged sink
	// would otherwise stall every collector sharing this Runner.
	RecordObservations(ctx context.Context, observations []observation.Observation)
}

// entry pairs one Collector with its own poll interval, so different
// sources can be polled at different cadences from a single Runner.
type entry struct {
	collector Collector
	interval  time.Duration
}

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
}

// NewRunner constructs a Runner that delivers to sink and logs through
// logger. logger must not be nil; pass slog.Default() if the caller has no
// specific logger.
func NewRunner(sink Sink, logger *slog.Logger) *Runner {
	return &Runner{sink: sink, logger: logger}
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
	r.entries = append(r.entries, entry{collector: c, interval: interval})
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
			r.loop(ctx, e.collector, e.interval)
		}(e)
	}
	wg.Wait()
}

// loop drives one collector: poll immediately, then wait interval (or
// ctx.Done) before polling again. An immediate first poll means a
// freshly-started collector's evidence appears without waiting a full
// interval, which matters for Step 3's not_collected story: an operator
// checking the API right after startup should see a real first attempt's
// result, not "not collected" for up to a full poll interval longer than
// necessary.
func (r *Runner) loop(ctx context.Context, c Collector, interval time.Duration) {
	for {
		obs := c.Poll(ctx)
		if ctx.Err() != nil {
			// Cancelled during (or exactly at the end of) this Poll call:
			// do not deliver a possibly-partial result from a shutdown in
			// progress. The sink itself may already be tearing down.
			return
		}
		r.sink.RecordObservations(ctx, obs)

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
