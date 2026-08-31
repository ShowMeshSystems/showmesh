package agent

import (
	"log"
	"runtime/debug"
	"sync"
)

// capabilityDetectionGate serializes and generation-stamps every
// scheduleCapabilityDetection run, so overlapping triggers (a reconnect
// racing a rebuild-driven republish, several rebuilds arriving during a
// retry backoff, or an available-to-unavailable withdrawal racing a
// later recovery) can never let a slower run's stale result overwrite a
// faster, newer run's genuinely current one, in either direction.
//
// generation is captured AT TRIGGER TIME, before a run's own up-to-
// capabilityDetectionTimeout probe sequence even starts, and a run only
// actually publishes if its generation is STILL current once its
// detection finishes; a superseded run's result is dropped instead of
// published, since a newer trigger already means a fresher picture is
// on its way.
//
// running/pending implement single-flight: at most one detection
// actually executes at a time. A trigger that arrives mid-run is
// coalesced into exactly one queued follow-up rather than run
// concurrently (chosen over cancel-and-restart because capabilityDetector
// shells out to real subprocesses, gst-launch-1.0, with no early-
// cancellation path today, so cancelling an in-flight run would still
// have to wait out its own probes anyway; the generation check alone
// already makes a stale RESULT harmless even without cutting a stale
// RUN short). The queued follow-up re-reads the current generation AND
// the current run closure right before it starts, so a burst of several
// triggers arriving while one run is in flight collapses onto ONE
// follow-up carrying the LATEST of them, never one per trigger, and
// never a stale closure from whichever trigger started the loop: this
// gate has two callers with two different lifetimes (publishAdvertisement
// passes connCtx, which survives SIGTERM; the rebuild hook passes
// sigCtx, which is cancelled at SIGTERM), so reusing the FIRST trigger's
// closure for a LATER caller's coalesced follow-up would run that
// follow-up under the wrong context, either outliving shutdown under a
// stale connCtx or losing its final publish under an already-cancelled
// sigCtx.
type capabilityDetectionGate struct {
	mu         sync.Mutex
	generation uint64
	running    bool
	pending    bool
	// latestRun is the run closure the most recent trigger call passed,
	// always overwritten on every trigger regardless of whether that
	// call started a new loop or only coalesced into one already
	// running. runLoop re-reads this field, never the closure it started
	// with, immediately before each iteration.
	latestRun func(gen uint64)
}

// capabilityGate is this node's single capability-detection gate,
// process-lifetime state shared across every scheduleCapabilityDetection
// caller, matching detectedCapabilityCache's own package-level
// convention.
var capabilityGate = &capabilityDetectionGate{}

// trigger arranges for one detect+publish cycle to execute, tagged with
// a generation captured now, using run's own closure (which carries its
// caller's own context and other captured state). If a cycle is already
// running, this call only bumps the generation, records run as the
// latest one, and marks a follow-up pending; it never starts a second
// concurrent run, and the already-running loop will pick up THIS call's
// run closure (not whatever closure started it) once it gets there.
func (g *capabilityDetectionGate) trigger(run func(gen uint64)) {
	g.mu.Lock()
	g.generation++
	g.latestRun = run
	if g.running {
		g.pending = true
		g.mu.Unlock()
		return
	}
	g.running = true
	gen := g.generation
	g.mu.Unlock()
	go g.runLoop(gen)
}

// runLoop executes the gate's current latestRun for gen, then either
// stops (no follow-up was requested while it ran) or immediately
// executes again for whatever generation and run closure are current by
// then. Always runs on its own goroutine, started by trigger.
func (g *capabilityDetectionGate) runLoop(gen uint64) {
	for {
		g.mu.Lock()
		run := g.latestRun
		g.mu.Unlock()
		g.runRecovered(run, gen)
		g.mu.Lock()
		if !g.pending {
			g.running = false
			g.mu.Unlock()
			return
		}
		g.pending = false
		gen = g.generation
		g.mu.Unlock()
	}
}

// runRecovered calls run for gen, recovering any panic. runLoop's own
// goroutine (started bare by trigger, with no recover anywhere else in
// its call stack) would otherwise crash this node's ENTIRE process on
// any panic inside run (a nil dereference in a caller's own closure,
// or any other bug reached along the way), and even if some outer
// recover somewhere caught it instead, g.running would never clear,
// silently disabling every future trigger (each one only ever sets
// pending and returns) for the rest of the process's life. Neither
// outcome is acceptable for one bad detection run. This has no
// *slog.Logger of its own (the gate is caller-injected only through the
// run closure itself), so a recovered panic is reported through the
// standard library's default logger as a last resort, never silently
// swallowed.
func (g *capabilityDetectionGate) runRecovered(run func(gen uint64), gen uint64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("capabilityDetectionGate: recovered a panic in a capability detection run (generation %d): %v\n%s", gen, r, debug.Stack())
		}
	}()
	run(gen)
}

// isCurrent reports whether gen is still this gate's most recent
// generation, for run's own body to check immediately before it would
// otherwise publish a result.
func (g *capabilityDetectionGate) isCurrent(gen uint64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return gen == g.generation
}

// reset returns the gate to its zero value. Test-only, matching
// detectedCapabilityCache.reset's own convention: this gate is
// process-lifetime state shared across every test in this package.
func (g *capabilityDetectionGate) reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.generation = 0
	g.latestRun = nil
	g.running = false
	g.pending = false
}
