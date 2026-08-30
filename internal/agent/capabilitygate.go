package agent

import "sync"

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
// RUN short). The queued follow-up re-reads the current generation right
// before it starts, so a burst of several triggers arriving while one
// run is in flight collapses onto ONE follow-up carrying the LATEST of
// them, never one per trigger.
type capabilityDetectionGate struct {
	mu         sync.Mutex
	generation uint64
	running    bool
	pending    bool
}

// capabilityGate is this node's single capability-detection gate,
// process-lifetime state shared across every scheduleCapabilityDetection
// caller, matching detectedCapabilityCache's own package-level
// convention.
var capabilityGate = &capabilityDetectionGate{}

// trigger arranges for one detect+publish cycle (run) to execute, tagged
// with a generation captured now. If a cycle is already running, this
// call only bumps the generation and marks a follow-up pending; it never
// starts a second concurrent run.
func (g *capabilityDetectionGate) trigger(run func(gen uint64)) {
	g.mu.Lock()
	g.generation++
	if g.running {
		g.pending = true
		g.mu.Unlock()
		return
	}
	g.running = true
	gen := g.generation
	g.mu.Unlock()
	go g.runLoop(run, gen)
}

// runLoop executes run for gen, then either stops (no follow-up was
// requested while it ran) or immediately executes run again for
// whatever generation is current by then. Always runs on its own
// goroutine, started by trigger.
func (g *capabilityDetectionGate) runLoop(run func(gen uint64), gen uint64) {
	for {
		run(gen)
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
	g.running = false
	g.pending = false
}
