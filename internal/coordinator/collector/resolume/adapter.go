package resolume

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Coalescing constants for Adapter's resolve loop (review finding A,
// 2026-08-14). All CHOSEN, not measured — the capture never profiled a
// safe caller-side scheduling policy, only the wire shapes and costs a
// policy has to respect. Each constant's own doc comment states its
// reasoning; see [Adapter.Run]'s doc comment for how they interact.
const (
	// resolveDebounceInterval coalesces a burst of HandleChange wake-ups
	// into one composition read. CHOSEN: the capture offers no natural
	// burst-duration number to derive this from — it profiled per-message
	// byte cost (section 5.3), not caller-side de-duplication opportunity.
	// 2s is comfortably longer than a tight sequence of REST calls a macro
	// might dispatch back to back (each of which pushes a full composition
	// per the capture's own measurement), while still short enough that an
	// operator watching the dashboard would not read it as stale.
	resolveDebounceInterval = 2 * time.Second

	// resolveMinInterval bounds steady-state resolve cost once a
	// connection is established and generating a stream of ordinary
	// change wake-ups. CHOSEN, but derived from the capture's own
	// arithmetic rather than picked freehand
	// (docs/bench/resolume-control-surface.md section 4.1 — see this
	// package's doc comment for why that citation belongs in a comment
	// and never in an operator-facing string): a full /composition read on
	// the one measured composition is ~2.26 MB, and the capture's own text
	// states that polling it at 1 Hz means "2.2 MB/s of JSON encode on the
	// render host, which is not viable." 30s turns that into roughly
	// 2.26 MB / 30s =~ 75 KB/s — the capture's own 30-second framing,
	// restated as a sustained rate rather than an interval. This bound is
	// waived exactly once per connection: see the "connect is exempt" rule
	// in [Adapter.Run]'s doc comment. It is a real, stated inefficiency
	// that this package still cannot avoid: capture section 5.3 measured a
	// full composition push on every clip connect, layer clear, and
	// disconnect-all — none of which changes a single object id — and
	// section 3.4 forbids parsing the push to learn which kind of change
	// it was. A cheaper structural discriminator than "read the whole
	// thing again" is an open question for a later seam, not something
	// invented here.
	resolveMinInterval = 30 * time.Second

	// resolveRetryMinBackoff and resolveRetryMaxBackoff bound a FAILED
	// resolve's own retry cadence — independent of resolveMinInterval,
	// which only bounds cost on the success path. CHOSEN to match
	// Watcher's own defaultMinBackoff/defaultMaxBackoff (watch.go) rather
	// than picked separately, so a maintainer reading both files sees one
	// retry philosophy for this package instead of two arbitrarily
	// different ones.
	resolveRetryMinBackoff = 500 * time.Millisecond
	resolveRetryMaxBackoff = 30 * time.Second

	// resolveConvergenceWindow bounds how long after a CONNECT a CHANGE
	// wake-up is still permitted to trigger a composition read. Outside
	// this window a change wakes nothing in the Adapter at all — no timer
	// is armed, no read happens. See [Adapter]'s own doc comment, section
	// "Why change-triggered resolution is time-bounded, not just
	// rate-bounded", for WHY this exists: bench evidence dated
	// 2026-08-14 measured GET /composition crashing the target Arena
	// build outright, which makes resolveMinInterval alone — a rate
	// bound that still permits a read every 30s for the life of a
	// connection — the wrong policy for a call now known to carry that
	// risk.
	//
	// CHOSEN, but anchored to the capture's own section 10.1 arithmetic
	// the same way resolveMinInterval is anchored to section 4.1: that
	// section measured a ~1.2s window, immediately after process launch,
	// in which /composition answers 200 OK describing a composition that
	// is not the show. 120s is that number with roughly two orders of
	// magnitude of headroom for a slower show host, while remaining
	// nowhere near a show-duration cadence — the exact cadence this
	// window exists to rule out.
	resolveConvergenceWindow = 120 * time.Second

	// resolveRetryMaxAttempts caps how many consecutive FAILED resolves may
	// be retried within a single connection, independent of
	// resolveConvergenceWindow. The window is what closes off automatic
	// retrying in the common case, but a resolve that fails FAST — a
	// connection refused, answered long before resolveRetryMinBackoff would
	// otherwise matter — burns through backoff's own growth curve much
	// faster than a resolve that fails on a timeout, so this cap has to
	// stand on its own rather than trust the window to always be the
	// tighter bound (a caller can override ConvergenceWindow independently
	// of RetryMinBackoff/RetryMaxBackoff via AdapterOptions, and this
	// package must not assume nobody ever will).
	//
	// CHOSEN at 8, derived from nextBackoff's own doubling curve
	// (watch.go) rather than picked freehand: starting at
	// resolveRetryMinBackoff (500ms) and doubling on every failure — 500ms,
	// 1s, 2s, 4s, 8s, 16s, 30s — reaches resolveRetryMaxBackoff (30s) after
	// 7 doublings. 8 gives that full ramp-to-ceiling one extra attempt at
	// the ceiling, then stops: a resolve that has failed 8 times in a row
	// has already been retried past the point backoff itself keeps slowing
	// it down, and letting it continue at a flat 30s cadence for the rest
	// of whatever window remains open is exactly the steady-state
	// /composition cadence this whole bound exists to prevent.
	resolveRetryMaxAttempts = 8
)

// AdapterOptions configures an [Adapter]. Every field left at its zero
// value is replaced by the matching documented default.
type AdapterOptions struct {
	// Logger receives the resolve loop's lifecycle and diagnostic events.
	// Defaults to slog.Default() if nil.
	Logger *slog.Logger

	// Now returns the current time, for ResolvedAt stamps, for measuring
	// resolveMinInterval, and for tests. Defaults to time.Now if nil.
	Now func() time.Time

	// DebounceInterval overrides resolveDebounceInterval. Zero or negative
	// means the default.
	DebounceInterval time.Duration

	// MinResolveInterval overrides resolveMinInterval. Zero or negative
	// means the default.
	MinResolveInterval time.Duration

	// RetryMinBackoff and RetryMaxBackoff override
	// resolveRetryMinBackoff/resolveRetryMaxBackoff. Zero or negative
	// means the default. Mirrors [WatcherOptions.MinBackoff] /
	// [WatcherOptions.MaxBackoff]'s own validation: MinBackoff must not
	// exceed MaxBackoff.
	RetryMinBackoff time.Duration
	RetryMaxBackoff time.Duration

	// ConvergenceWindow overrides resolveConvergenceWindow. Zero or
	// negative means the default.
	ConvergenceWindow time.Duration

	// RetryMaxAttempts overrides resolveRetryMaxAttempts. Zero or negative
	// means the default.
	RetryMaxAttempts int
}

// Adapter owns the lifetime of the one [Resolution] this package holds in
// memory, and — as of review finding A (2026-08-14) — owns the ONLY
// goroutine that ever performs a composition read. It is NOT a collector —
// it produces no [observation.Observation], ever, and satisfies no
// collector interface — it exists purely so a connect/disconnect/change
// lifecycle (driven, in production, by the WebSocket wake-up this
// package's watch.go implements) has one place to re-resolve and hold
// identity, per this package's doc comment on the parameter-id lifecycle
// rule.
//
// # Why this has its own Run loop
//
// The first version of this type resolved synchronously, once, off
// [HandleConnect] alone. That was wrong in a way a live Arena restart
// found directly: the WebSocket comes back up (and fires OnConnect) BEFORE
// the composition finishes loading — capture section 7.2/10.1 measured a
// ~1.2 second window during which /composition answers 200 OK with
// Arena's default EMPTY composition — and Arena never drops the socket
// again once loading finishes, so nothing ever told the old HandleConnect
// to re-resolve. The held Resolution stayed wrong, silently, for as long
// as the connection stayed up: measured live, the operator's real
// `Christmas 25` (18 layers, 14 columns) sat behind a held Resolution of
// the default empty composition (3 layers, 9 columns) for over ninety
// seconds before this defect was found. Three more defects shared the
// same root cause: a failed HandleConnect was never retried; backoff
// reset before a connection proved itself, so a peer that accepted and
// instantly dropped the socket looped at 250-500ms intervals each firing
// a multi-megabyte GET; and the old wiring called HandleConnect
// synchronously from the WebSocket's own OnConnect callback, blocking the
// read loop for up to the composition fetch's own timeout.
//
// All four were one defect: the resolve was driven synchronously, inline,
// off transport callbacks, with no loop of its own to retry, coalesce, or
// bound cost. [Run] is that loop. [HandleConnect], [HandleDisconnect], and
// [HandleChange] are now cheap, non-blocking signal producers ONLY — they
// perform no I/O and never block, so calling them synchronously from
// Watcher's own read loop (as watch.go does) is safe. Run is the only
// thing that ever performs a composition read.
//
// # What Run promises, and what it explicitly does not
//
// Run converges the held Resolution toward whatever Arena is currently
// serving, on every connect (immediately), on every change wake-up
// (coalesced — see resolveDebounceInterval/resolveMinInterval), and after
// every failure (retried with capped backoff — see
// resolveRetryMinBackoff/resolveRetryMaxBackoff). That is the WHOLE of
// what it promises.
//
// It does NOT detect the load window described above, and it does NOT
// assert which composition is loaded — that is composition identity
// (adapter specification section 3.8), which is a later seam and
// deliberately out of scope here. A Resolution this Adapter holds may be
// of a composition that is not the show; this package has no way to tell,
// and does not claim to. Converging on the truth eventually is the whole
// of what this loop does; recognizing what the truth IS belongs to
// whichever seam builds composition identity.
//
// # Coalescing policy
//
//   - A CONNECT resolves immediately and is EXEMPT from resolveMinInterval
//     — this is the load-window case above, and waiting out 30 seconds of
//     a known-wrong Resolution after every reconnect would be the defect
//     restated with a delay instead of removed.
//   - A CHANGE resolves after resolveDebounceInterval, so a burst of
//     wake-ups (the capture's own section 5.3: every clip connect, layer
//     clear, and disconnect-all pushes a full composition) coalesces into
//     one read rather than one per message — but ONLY while within
//     resolveConvergenceWindow of the most recent successful CONNECT. See
//     "Why change-triggered resolution is time-bounded, not just
//     rate-bounded" below. Outside that window a change wakes nothing: no
//     timer is armed and no composition read happens, ever, until the
//     next connect.
//   - resolveMinInterval floors the gap between two change-triggered
//     resolves, with a TRAILING resolve always guaranteed: a change that
//     arrives while a resolve is already scheduled is still folded into
//     that scheduled resolve rather than dropped, so the last change
//     before a quiet period is never lost. This floor only ever matters
//     inside the convergence window — outside it, nothing is scheduled at
//     all.
//   - A resolve that FAILS retries on its own capped backoff
//     (resolveRetryMinBackoff..resolveRetryMaxBackoff), which does not
//     wedge the loop: connect, disconnect, and change events are all still
//     handled immediately while a retry is pending. This no longer means
//     "forever": a retry may only be SCHEDULED while
//     resolveConvergenceWindow is still open, and the count of retries
//     scheduled in one connection is additionally capped at
//     resolveRetryMaxAttempts — see that constant's own doc comment and
//     "Why the retry path is bound by the same two mechanisms" below. When
//     either bound is hit — the window closes with a retry pending, or the
//     attempt cap is reached — retrying stops, any pending retry timer is
//     cancelled, and nothing further is attempted until the next connect;
//     this is logged once at WARN so the Adapter going quiet is a visible
//     transition, not silence to infer.
//   - The convergence window closing is logged once, at INFO, per
//     connection — see [Adapter.Run]'s own doc comment — so that a change
//     going quiet after a busy period is a visible transition, not a
//     silent behavior change an operator has to infer.
//
// # Why change-triggered resolution is time-bounded, not just rate-bounded
//
// Bench evidence dated 2026-08-14, measured against the operator's real
// Arena 7.23.2, found GET /composition crashing that Arena build outright:
// EXC_BAD_ACCESS/SIGSEGV on a background thread, four times, with a
// byte-identical faulting-frame signature each time — including once
// produced by curl alone, with no ShowMesh code running at all, which is
// what rules this package out as the cause. Two other reads were
// exercised at length and did NOT crash it: GET /product polled every 10s
// for 5 minutes (30 polls, all survived), and a WebSocket held open for 5
// minutes reading nothing over REST (survived, 3 messages). So this is
// specifically a /composition hazard, not a "reading Resolume is
// dangerous" one, and this package should never be read as implying the
// latter.
//
// The crash count did not fit a simple threshold, which is why this is a
// TIME bound and not a request-count bound either: one run crashed after
// 9 reads spaced 30s apart (about 4.5 minutes), another after 2046
// back-to-back reads (149s). The mechanism is unknown — "crashes Arena" is
// what was observed, not a diagnosis. /composition is also the ONLY
// enumeration path (section 2.3 of the capture: /composition/layers,
// /columns, /decks, and /layergroups all 404), so this loop cannot avoid
// calling it once; what it can do is stop calling it on a cadence that
// approaches show duration. resolveConvergenceWindow is that bound: a
// CHANGE wake-up only triggers a re-resolve within resolveConvergenceWindow
// of the most recent successful CONNECT. Outside that window a change
// wakes nothing in this Adapter — no timer, no read — though the wiring
// still nudges the collector's own /product poll (64 bytes, measured safe)
// on the identical wake-up; see resolumewiring.go's own comment on
// OnChange.
//
// One more crash was measured after this window shipped, bringing the
// running total to seven, all sharing the identical faulting-frame
// signature above, and it raises the stakes rather than closing the
// question: a run that made exactly TWO composition reads — one on
// connect, one change-triggered inside the window — crashed Arena 26
// seconds after the SECOND read. Measured 2026-08-14, same machine, same
// Arena build, same composition as every other number in this comment.
// Two reads, 26 seconds apart, is well inside any bound this package could
// plausibly choose without refusing to read /composition at all. So
// resolveConvergenceWindow, and resolveRetryMaxAttempts below, REDUCE
// exposure to this crash. They do NOT eliminate it. Every /composition
// call this Adapter makes, on any trigger, is a risk that has now been
// measured to materialize from as few as two calls; nothing about a call
// landing inside the window or under the retry cap makes it safe, only
// less frequent than the alternative of not bounding it at all. Read every
// mitigation in this file as reducing rate, never as establishing safety.
//
// # Why the retry path is bound by the same two mechanisms
//
// A resolve that fails — at connect, change-triggered, or on a prior retry
// — retries on its own capped backoff (resolveRetryMinBackoff to
// resolveRetryMaxBackoff), and an earlier version of this package left
// that retry loop unbounded by the convergence window entirely: it kept
// retrying on a steady-state cadence (capped at resolveRetryMaxBackoff,
// 30s) for as long as the failure persisted, with no reference to whether
// resolveConvergenceWindow was still open. That is precisely the
// /composition cadence this whole window exists to rule out, reached by
// the one path — failure — that is most likely exactly when the window is
// open (a resolve is most likely to fail during the same load window that
// arms the window in the first place). A retry is now only ever SCHEDULED
// while the window is open, checked at the moment the retry would be
// armed, and is additionally capped at resolveRetryMaxAttempts scheduled
// retries per connection regardless of how much of the window remains —
// see that constant's own doc comment for why a time bound alone was not
// judged sufficient. Whichever bound is hit first — window closes with a
// retry pending, or the attempt cap is reached — retrying is abandoned:
// any pending retry timer is cancelled, and nothing further is attempted
// until the next connect. Abandonment is logged exactly once per
// connection at WARN, naming the reason, so that the Adapter holding no
// resolution and making no further attempt is a stated fact in the log,
// never something an operator has to infer from silence.
//
// This deliberately gives something up: a composition swapped into Arena
// WITHOUT restarting it will leave this Adapter holding a stale Resolution
// indefinitely once the window has closed, because nothing re-enumerates.
// That gap is already an open item the capture itself recorded and did not
// close (section 13, "a controlled save-and-reload"). It is now a
// deliberate deferral, not an oversight: the alternative is calling the
// one API measured to crash the target, on a cadence a real, hours-long
// show would reach.
//
// Every number above is from one machine (the owner's arm64 MacBook Pro),
// one Arena build (7.23.2), and one composition (`Christmas 25`). Whether
// any of it reproduces on the deployed show host, a different Arena
// build, or a different composition is unmeasured.
//
// A held Resolution is always replaced wholesale, never merged or
// diffed — every successful resolve either replaces the held Resolution or
// (on failure) drops it entirely; [HandleDisconnect] drops it
// unconditionally. This mirrors ADR-020's non-resumable-stream rule for
// the identical reason: after any interruption, re-fetch an authoritative
// snapshot rather than trying to reconcile forward from whatever was held
// before, because Resolume's own parameter ids do not survive a restart
// and nothing announces when they have changed (capture section 3.2).
type Adapter struct {
	client *Client
	logger *slog.Logger
	now    func() time.Time

	debounce          time.Duration
	minInterval       time.Duration
	retryMinWait      time.Duration
	retryMaxWait      time.Duration
	convergenceWindow time.Duration
	retryMaxAttempts  int

	connectCh    chan struct{}
	disconnectCh chan struct{}
	changeCh     chan struct{}

	mu  sync.Mutex
	res *Resolution

	// failing is touched ONLY from within Run's own goroutine (never
	// concurrently, by construction: Run is the sole reader of
	// connectCh/disconnectCh/changeCh and the sole writer of a.res via
	// a.replace/a.drop). It exists purely to decide whether a resolve
	// failure is the FIRST in a run of consecutive failures, per this
	// type's own "does not repeat identically on every retry" logging
	// rule — see resolve()'s doc comment.
	failing bool
}

// NewAdapter constructs an Adapter. Call [Adapter.Run] to start its
// resolve loop; constructing an Adapter performs no I/O by itself.
func NewAdapter(c *Client, opts AdapterOptions) *Adapter {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	debounce := opts.DebounceInterval
	if debounce <= 0 {
		debounce = resolveDebounceInterval
	}
	minInterval := opts.MinResolveInterval
	if minInterval <= 0 {
		minInterval = resolveMinInterval
	}
	retryMin := opts.RetryMinBackoff
	if retryMin <= 0 {
		retryMin = resolveRetryMinBackoff
	}
	retryMax := opts.RetryMaxBackoff
	if retryMax <= 0 {
		retryMax = resolveRetryMaxBackoff
	}
	if retryMin > retryMax {
		retryMax = retryMin
	}
	convergenceWindow := opts.ConvergenceWindow
	if convergenceWindow <= 0 {
		convergenceWindow = resolveConvergenceWindow
	}
	retryMaxAttempts := opts.RetryMaxAttempts
	if retryMaxAttempts <= 0 {
		retryMaxAttempts = resolveRetryMaxAttempts
	}

	return &Adapter{
		client:            c,
		logger:            logger,
		now:               now,
		debounce:          debounce,
		minInterval:       minInterval,
		retryMinWait:      retryMin,
		retryMaxWait:      retryMax,
		convergenceWindow: convergenceWindow,
		retryMaxAttempts:  retryMaxAttempts,
		connectCh:         make(chan struct{}, 1),
		disconnectCh:      make(chan struct{}, 1),
		changeCh:          make(chan struct{}, 1),
	}
}

// HandleConnect signals Run's loop that a connection just came up and a
// fresh, immediate, resolveMinInterval-exempt resolve is owed. It performs
// NO I/O and NEVER BLOCKS — it is a non-blocking, coalescing send to an
// internal channel — which is what makes it safe to call synchronously
// from Watcher's own read loop (watch.go's OnConnect contract) without
// blocking message delivery. If Run is not currently running, or has not
// yet drained a previously-queued connect signal, this is a harmless
// no-op: the next Run to observe the channel picks up the (single,
// coalesced) pending signal.
func (a *Adapter) HandleConnect(context.Context) {
	select {
	case a.connectCh <- struct{}{}:
	default:
	}
}

// HandleDisconnect signals Run's loop that the connection just went down:
// any pending or in-flight scheduling is cancelled and the held Resolution
// is dropped unconditionally, because parameter ids do not survive a
// reconnect (capture section 3.2) and neither should a Resolution built
// against the connection that just ended. Performs no I/O and never
// blocks, per [HandleConnect]'s own doc comment.
func (a *Adapter) HandleDisconnect(context.Context) {
	select {
	case a.disconnectCh <- struct{}{}:
	default:
	}
}

// HandleChange signals Run's loop that the composition may have changed
// and a resolve is owed, subject to the coalescing policy in [Adapter]'s
// own doc comment (debounce, then resolveMinInterval, and only within
// resolveConvergenceWindow of the most recent connect — outside that
// window this call still never blocks and never errors, but Run's loop
// discards the signal without reading anything). Performs no I/O and
// never blocks, per [HandleConnect]'s own doc comment. This is the
// callback watch.go's WatcherOptions.OnChange is wired to, IN ADDITION TO
// (never instead of) nudging this seam's Collector — see
// resolumewiring.go's own comment for why both are wired from the one
// wake-up.
func (a *Adapter) HandleChange(context.Context) {
	select {
	case a.changeCh <- struct{}{}:
	default:
	}
}

// Resolution returns the currently held Resolution, if any. ok is false
// before the first successful resolve, after any resolve failure, and
// after a disconnect.
func (a *Adapter) Resolution() (*Resolution, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.res == nil {
		return nil, false
	}
	return a.res, true
}

// Run is the Adapter's own resolve loop, and per this package's doc
// comment it is the ONLY thing that ever performs a composition read. It
// blocks until ctx is done and returns with no goroutine of its own left
// running — Run starts no goroutine at all; every wait inside it is a
// select over channels and timers owned by this call.
//
// Callers: [HandleConnect], [HandleDisconnect], and [HandleChange] are the
// only way to drive this loop from outside it. Wiring code (see
// resolumewiring.go) is expected to run this alongside a [Watcher] whose
// OnConnect/OnDisconnect/OnChange callbacks call exactly those three
// methods and nothing else — this method contains no WebSocket or REST
// networking of its own beyond the composition reads [Client.Composition]
// performs.
//
// See [Adapter]'s own doc comment for the coalescing policy this
// implements (connect-is-immediate-and-exempt, change-is-debounced-and-
// interval-floored, failure-retries-with-backoff-without-wedging-the-
// loop).
func (a *Adapter) Run(ctx context.Context) {
	var (
		timer         *time.Timer
		timerC        <-chan time.Time
		retrying      bool
		retryBackoff  time.Duration
		retryAttempts int
		lastResolveAt time.Time
		connected     bool

		// windowTimer/windowTimerC/windowOpen implement
		// resolveConvergenceWindow (see [Adapter]'s own doc comment,
		// "Why change-triggered resolution is time-bounded, not just
		// rate-bounded"). windowOpen is true from a successful CONNECT
		// until either windowTimer fires or a DISCONNECT arrives; a
		// CHANGE wake-up is discarded outright — no timer armed, no
		// composition read — whenever windowOpen is false. A retry is
		// bound by the identical rule (see "Why the retry path is bound
		// by the same two mechanisms" on [Adapter]'s own doc comment): it
		// may only be SCHEDULED while windowOpen is true, checked in
		// doResolve's failure branch below, and windowTimerC firing while
		// a retry is pending cancels it via abandonRetries.
		windowTimer  *time.Timer
		windowTimerC <-chan time.Time
		windowOpen   bool

		// retryAbandonLogged guards the "abandoning retries" WARN (see
		// abandonRetries below) to exactly once per connection, mirroring
		// a.failing's own "first failure only" logging rule one level up
		// the stack — a retry that keeps failing after being abandoned
		// (which cannot happen: abandonment stops scheduling) must not be
		// able to repeat the line anyway. Reset on every connect and
		// disconnect.
		retryAbandonLogged bool
	)

	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
			timerC = nil
		}
	}
	defer stopTimer()

	stopWindowTimer := func() {
		if windowTimer != nil {
			windowTimer.Stop()
			windowTimer = nil
			windowTimerC = nil
		}
	}
	defer stopWindowTimer()

	// abandonRetries cancels any pending retry timer and stops the retry
	// loop from scheduling another until the next connect. Called from two
	// places: doResolve's failure branch, at the moment a NEW retry would
	// otherwise be scheduled but the window is already closed or the
	// attempt cap (resolveRetryMaxAttempts) is reached; and the
	// windowTimerC case, when the window closes while a retry is already
	// pending. Logs once per connection at WARN, never silently — this
	// package has already shipped one silent-failure defect (the dead
	// WebSocket dial in watch.go, quiet for 3.5 minutes) and must not ship
	// a second.
	abandonRetries := func(reason string) {
		stopTimer()
		retrying = false
		retryBackoff = 0
		if !retryAbandonLogged {
			retryAbandonLogged = true
			a.logger.Warn("resolume adapter: abandoning composition resolve retries; the Adapter holds no resolution and will not attempt another until the next connection",
				"reason", reason, "attempts", retryAttempts)
		}
	}

	scheduleRetry := func() {
		stopTimer()
		if retryBackoff <= 0 {
			retryBackoff = a.retryMinWait
		} else {
			retryBackoff = nextBackoff(retryBackoff, a.retryMaxWait)
		}
		retrying = true
		timer = time.NewTimer(retryBackoff)
		timerC = timer.C
	}

	doResolve := func(trigger string) {
		ok := a.resolve(ctx, trigger)
		lastResolveAt = a.now()
		if ok {
			retrying = false
			retryBackoff = 0
			return
		}
		// The resolve failed. A retry may only be SCHEDULED while the
		// convergence window is still open, and only up to
		// resolveRetryMaxAttempts scheduled retries per connection — see
		// [Adapter]'s own doc comment, "Why the retry path is bound by
		// the same two mechanisms". Either bound being already exceeded
		// abandons retrying outright rather than scheduling one more.
		if !windowOpen {
			abandonRetries("convergence window is not open")
			return
		}
		if retryAttempts >= a.retryMaxAttempts {
			abandonRetries("retry attempt cap reached")
			return
		}
		retryAttempts++
		scheduleRetry()
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-a.connectCh:
			connected = true
			stopTimer()
			retrying = false
			retryBackoff = 0
			retryAttempts = 0
			retryAbandonLogged = false
			// A fresh connect reopens the convergence window regardless
			// of whether one was already open or had already closed —
			// "reconnect reopens the window" is not a special case, it
			// falls out of unconditionally re-arming windowTimer here.
			// It resets the retry attempt counter the identical way, for
			// the identical reason: a new connection has earned a fresh
			// budget, independent of how many retries the previous one
			// spent.
			stopWindowTimer()
			windowOpen = true
			windowTimer = time.NewTimer(a.convergenceWindow)
			windowTimerC = windowTimer.C
			doResolve("connect")

		case <-a.disconnectCh:
			connected = false
			stopTimer()
			stopWindowTimer()
			windowOpen = false
			retrying = false
			retryBackoff = 0
			retryAttempts = 0
			retryAbandonLogged = false
			a.failing = false
			a.drop()
			a.logger.Info("resolume adapter: dropped held resolution on disconnect")

		case <-a.changeCh:
			if !connected {
				// A change wake-up with no live connection is stale by
				// construction (HandleDisconnect already dropped whatever
				// was held) — nothing to schedule against.
				continue
			}
			if !windowOpen {
				// Past the convergence window: per this package's own doc
				// comment, a change wakes NOTHING here — no timer armed,
				// no composition read, ever, until the next connect. The
				// wiring's own runner.Nudge (a /product poll, not a
				// composition read) still ran off this same wake-up
				// independently of this loop; see resolumewiring.go.
				continue
			}
			if timer == nil {
				wait := a.debounce
				if since := a.now().Sub(lastResolveAt); since < a.minInterval {
					if floor := a.minInterval - since; floor > wait {
						wait = floor
					}
				}
				retrying = false
				timer = time.NewTimer(wait)
				timerC = timer.C
			}
			// timer already scheduled (debounce or retry): this change is
			// folded into whatever resolve that timer will trigger — the
			// "trailing resolve guaranteed" half of the coalescing policy.

		case <-timerC:
			timer = nil
			timerC = nil
			if retrying {
				doResolve("retry")
			} else {
				doResolve("change")
			}

		case <-windowTimerC:
			windowTimer = nil
			windowTimerC = nil
			windowOpen = false
			if retrying {
				// A retry was pending when the window closed: cancel it
				// and abandon the retry loop rather than let it fire once
				// more just outside the window. This is the hole review
				// finding B found: without this, a resolve that keeps
				// failing keeps retrying forever on its own capped
				// backoff, entirely outside the bound this window exists
				// to enforce.
				//
				// Deliberately scoped to the RETRY path only: a pending
				// CHANGE-triggered debounce timer (retrying == false) is
				// left alone here, unchanged from this package's
				// pre-existing behavior, and is out of scope for this
				// fix — see TestAdapterReconnectReopensConvergenceWindow,
				// which depends on a debounce timer armed just inside the
				// window still firing shortly after it closes.
				abandonRetries("convergence window closed with a retry pending")
			}
			a.logger.Info("resolume adapter: change-triggered re-resolve window closed; further composition changes will not be re-resolved until the next connect",
				"window", a.convergenceWindow.String())
		}
	}
}

// resolve performs exactly one composition read and either replaces the
// held Resolution wholesale or drops it entirely — it never keeps a stale
// Resolution and never manufactures one. trigger names which coalescing
// path caused this call ("connect", "change", or "retry") purely for the
// log line; it has no effect on behavior.
//
// On success, if a Resolution was already held, its fingerprints are
// compared against the new one and a difference is logged at INFO naming
// BOTH (review finding A's own requirement — this is what makes a
// load-window swap like the one this type's doc comment describes visible
// in the log instead of silent). On failure the held Resolution is
// dropped and a WARN is logged, but ONLY on the first failure since the
// last success or disconnect — [Adapter.failing]'s own doc comment — so a
// long retry-backoff run does not become a log storm repeating the
// identical line.
func (a *Adapter) resolve(ctx context.Context, trigger string) bool {
	comp, err := a.client.Composition(ctx)
	if err != nil {
		a.onResolveFailure(trigger, ClassifyError(err))
		return false
	}

	res, err := Resolve(comp, a.now())
	if err != nil {
		a.onResolveFailure(trigger, err.Error())
		return false
	}

	a.mu.Lock()
	prev := a.res
	a.res = res
	a.mu.Unlock()
	a.failing = false

	objFP := res.ObjectFingerprint()
	paramFP := res.ParameterFingerprint()

	attrs := []any{
		"trigger", trigger,
		"clip_count", len(res.clipIDs),
		"layer_count", len(res.layerIDs),
		"column_count", len(res.columnIDs),
		"deck_count", len(res.deckIDs),
		"selected_deck", res.SelectedDeckName,
		"object_fingerprint", objFP,
		"parameter_fingerprint", paramFP,
		"selected_deck_object_fingerprint", res.SelectedDeckObjectFingerprint(),
	}

	if prev != nil {
		prevObjFP, prevParamFP := prev.ObjectFingerprint(), prev.ParameterFingerprint()
		if prevObjFP != objFP || prevParamFP != paramFP {
			attrs = append(attrs,
				"previous_object_fingerprint", prevObjFP,
				"previous_parameter_fingerprint", prevParamFP,
			)
			a.logger.Info("resolume adapter: resolved composition differs from the one it replaces", attrs...)
			return true
		}
	}

	a.logger.Info("resolume adapter: resolved composition", attrs...)
	return true
}

// onResolveFailure drops the held Resolution and logs at WARN, but only
// when a.failing is false — i.e. only for the FIRST failure since the
// last success or disconnect. Every subsequent retry failure in the same
// run sets nothing new to log: a.failing is already true, so the WARN is
// suppressed. This is the "does not repeat identically on every retry"
// half of review finding A's own requirement.
//
// This method does not know, and deliberately does not say, whether a
// retry will follow: that decision belongs to Run's own doResolve closure,
// which knows the convergence window and attempt-cap state this method
// does not. Whether retrying continues or is abandoned is logged
// separately, by abandonRetries, so this WARN's wording stays true in both
// outcomes rather than promising a retry that a bound might immediately
// cut off.
func (a *Adapter) onResolveFailure(trigger, reason string) {
	a.drop()
	if !a.failing {
		a.failing = true
		a.logger.Warn("resolume adapter: composition resolve failed",
			"trigger", trigger, "reason", reason)
	}
}

func (a *Adapter) drop() {
	a.mu.Lock()
	a.res = nil
	a.mu.Unlock()
}
