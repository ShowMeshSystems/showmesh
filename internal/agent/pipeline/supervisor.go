package pipeline

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// State is a supervised pipeline's reported lifecycle state, matching
// mqttproto's RenderPipelineState* vocabulary. This package's own type,
// distinct from the wire strings, so a caller converts explicitly at the
// wire boundary rather than this package importing pkg/mqttproto for a
// handful of string constants.
type State string

const (
	StateStarting    State = "starting"
	StateRunning     State = "running"
	StateRestarting  State = "restarting"
	StateFailed      State = "failed"
	StateStopped     State = "stopped"
	StateUnsupported State = "unsupported"
)

// restartPolicy bounds the restart-backoff and fast-failure-lockout
// behaviour every runner uses. A field of [Supervisor] (not a package-level
// var): tests running concurrently with different policies would otherwise
// race on shared globals, and a Supervisor built with defaultRestartPolicy
// is what production code gets.
type restartPolicy struct {
	// maxConsecutiveFastFailures bounds how many consecutive start attempts
	// may fail to reach PLAYING before a runner gives up and reports
	// StateFailed, rather than retrying forever. This is the seam's own
	// named acceptance criterion: a silent infinite restart loop is the
	// failure mode that looks healthy from every angle except the wall, so
	// retrying forever is never an option. See loop()'s crash-handling
	// branch for why this is keyed to PLAYING evidence rather than a
	// wall-clock window (F11).
	maxConsecutiveFastFailures int64

	// initialBackoff and maxBackoff bound the exponential backoff between
	// restart attempts.
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// defaultRestartPolicy is what [NewSupervisor] uses.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for the right
// backoff shape. maxConsecutiveFastFailures=5 gives a transient failure
// (e.g. a momentarily busy device) a few chances without looping
// indefinitely on a permanently broken one.
var defaultRestartPolicy = restartPolicy{
	maxConsecutiveFastFailures: 5,
	initialBackoff:             500 * time.Millisecond,
	maxBackoff:                 30 * time.Second,
}

// Snapshot is one surface's supervised state, safe to read concurrently
// with the supervisor's own goroutine mutating it — see [Supervisor.
// Snapshot].
type Snapshot struct {
	SurfaceID string

	State  State
	Reason string
	Since  time.Time

	RestartCount        int64
	ConsecutiveFailures int64

	LastExitCode *int
	LastStderr   string

	// FramesWritten, FramesLate, and FramesDropped are B3's frame writer
	// counters, cumulative for the life of the current surface assignment
	// (reset to 0 by a fresh Apply, never by a pipeline crash/restart — the
	// frame writer goroutine outlives any one gst-launch-1.0 attempt and
	// keeps counting through a restart).
	FramesWritten int64
	FramesLate    int64
	FramesDropped int64

	// FramesRate is the frame writer's own measured achieved output rate in
	// frames/second, over its most recently completed sampling window — see
	// FrameWriter.sampleRate's doc comment for how the window is chosen. nil
	// until at least one full window has elapsed (ADR-040's obligation:
	// this is evidence, never a plausible-looking zero and never the
	// configured target rate echoed back).
	FramesRate *float64

	// FramesObservedAt is when FrameWriter.sampleRate actually sampled
	// FramesWritten/FramesLate/FramesDropped/FramesRate: its own
	// timestamp, deliberately separate from the shared ObservedAt below.
	// These four counters are continuously sampled measurements, not a
	// lifecycle transition, so they must never borrow ObservedAt's
	// setState-only stamp (that would either go stale the instant a report
	// republishes with no new counter evidence, or start moving on every
	// tick and defeat setState's own "a fresh ObservedAt means the state
	// actually moved" invariant, the same shape of bug
	// TransportObservedAt exists to avoid one field up). Zero until the
	// frame writer's first sampling window has closed (ADR-011: an
	// unmeasured value is nil/zero, never defaulted to "now").
	FramesObservedAt time.Time

	// Transport, TransportAvailable and TransportReason are "" / nil / ""
	// until [runner.setTransportProbe] records a real probe result — see
	// [mqttproto.RenderSurfaceReport.TransportAvailable]'s doc comment on
	// why nil means genuinely unprobed, not "false."
	Transport          string
	TransportAvailable *bool
	TransportReason    string

	// TransportObservedAt is when [runner.setTransportProbe] recorded THIS
	// transport evidence — separate from the shared ObservedAt below.
	// Carrying its own timestamp (rather than stamping the shared one, as
	// an earlier version of this package did) is what makes "a fresh
	// ObservedAt on the pipeline state means the state actually moved" true
	// with no exceptions: the coordinator's own render-command confirmation
	// fences on that exact invariant, and a probe refreshing the shared
	// ObservedAt with no state transition was a way to defeat it from a
	// second direction after [Supervisor.AwaitState]'s Generation check
	// closed the first (finding 2).
	TransportObservedAt time.Time

	// TimelineState is the [multisync.State] (as its wire string, e.g.
	// "playing", "unsynchronized", "opened") this surface's FrameWriter most
	// recently sampled from the shared timeline — "" until any tick has run.
	// Carried as a plain string (not multisync.State) so this package never
	// imports pkg/multisync's State type into its own reported shape; see
	// [FrameWriter.TimelineSource] for the interface boundary this crosses.
	TimelineState string

	// TimelinePositionMS is the timeline position this surface's FrameWriter
	// extracted its most recent CONTENT frame from — nil whenever the writer
	// is drawing idle output instead (finding 7's fix: a position is only
	// meaningful when content is actually being read from it, and ADR-011
	// says an inapplicable value is nil, never a stale or zero position).
	TimelinePositionMS *int64

	// Drawing is what the writer actually wrote to the pipeline's stdin on
	// its most recent tick: [DrawingContent], [DrawingIdle], or
	// [DrawingFailure]. This is the
	// evidence the build contract names explicitly: "the process is up" is
	// not "frames are arriving somewhere," and a report that only carries
	// PipelineState=running cannot tell an operator apart from a node
	// silently emitting black at full rate all night.
	Drawing string

	// IdleMode is the configured idle output ([IdleOutputBlack],
	// [IdleOutputHold], or [IdleOutputDiagnostic]) whenever Drawing is
	// [DrawingIdle]; "" for every other Drawing value, [DrawingFailure]
	// included, because a failure is not an idle mode.
	IdleMode string

	// FailureOutput is what a [DrawingFailure] tick actually wrote,
	// [FailureOutputAlert] or [FailureOutputBlack]; "" for every other
	// Drawing value.
	FailureOutput string

	// Generation identifies which process attempt r.snap's State currently
	// describes — see [runner.bumpProcGen]. Used internally by [Supervisor.
	// AwaitState] to require that a confirmed state actually MOVED past a
	// caller-supplied baseline, rather than trusting a stale snapshot whose
	// ObservedAt happened to be refreshed by something unrelated (finding
	// 2). Never sent over the wire; internal supervision bookkeeping only.
	Generation int64

	ObservedAt time.Time
}

// clone deep-copies s so a caller that mutates its own Snapshot (e.g. to
// stamp ObservedAt) never aliases this package's internal state.
func (s Snapshot) clone() Snapshot {
	out := s
	if s.LastExitCode != nil {
		v := *s.LastExitCode
		out.LastExitCode = &v
	}
	if s.TransportAvailable != nil {
		v := *s.TransportAvailable
		out.TransportAvailable = &v
	}
	if s.FramesRate != nil {
		v := *s.FramesRate
		out.FramesRate = &v
	}
	if s.TimelinePositionMS != nil {
		v := *s.TimelinePositionMS
		out.TimelinePositionMS = &v
	}
	return out
}

// command is one instruction sent to a runner's control loop.
type commandKind int

const (
	cmdApply commandKind = iota
	cmdClear
	cmdRestart
)

type command struct {
	kind commandKind
	spec Spec
}

// runner supervises exactly one surface's pipeline across its whole life:
// zero or more spec applications, zero or more crashes and restarts, until
// the Supervisor that owns it shuts down.
type runner struct {
	surfaceID string
	now       func() time.Time
	starter   ProcessStarter
	logger    Logger
	policy    restartPolicy

	cmds chan command
	stop chan struct{}
	done chan struct{}

	mu          sync.Mutex
	snap        Snapshot
	pid         int           // 0 when no process is currently running; diagnostics only, never part of the wire report
	currentProc ProcessHandle // nil when no process is currently running; see stdin()
	procGen     int64         // see bumpProcGen and setRunningIfCurrent

	// cleared is true from a processed cmdClear until the next cmdApply.
	// [Supervisor.SnapshotAll] (the render report publisher's source —
	// internal/agent/renderreport.go) excludes a cleared runner entirely,
	// so a cleared surface stops being reported rather than reporting
	// StateStopped forever — see [Supervisor.Clear]'s doc comment. Local
	// operation confirmation (Snapshot, AwaitState) is UNAFFECTED: those
	// still need to observe the Stopped transition immediately after
	// clearing to confirm the clear itself.
	cleared bool

	// degradedReason mirrors the currently-applied Spec's own
	// OutputDegradedReason, set at cmdApply and read by
	// setRunningIfCurrent — see that field's doc comment. Kept as its own
	// field (not read off a stored Spec) so a restart, which re-attempts
	// the SAME already-applied spec, does not need to keep the whole Spec
	// value around just for this one field.
	degradedReason string
}

// bumpProcGen advances the generation identifying which process attempt is
// currently live and returns the new value. Called every time proc's
// identity changes in [runner.loop] — a new attempt starting, or the
// current one dying or being stopped — so a callback captured from an
// attempt that is no longer current can be told so (F10): onRunningMarker
// fires from that attempt's own goroutine, with no ordering guarantee
// against the loop already having moved on (a fast-failure lockout with no
// restart scheduled, or a new Apply/Restart superseding it), and a fake
// process starter (unlike a real one) can even call it synchronously before
// Start returns.
func (r *runner) bumpProcGen() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procGen++
	return r.procGen
}

// setRunningIfCurrent applies StateRunning only if gen still names the
// live process's current generation, so a marker from an already-dead or
// already-superseded attempt can never stamp state onto whatever is
// current now. Reason carries r.degradedReason rather than always "": a
// pipeline whose sink is a diagnostic fakesink because its requested
// output.transport has no real sink in this build (or was misconfigured)
// genuinely reaches PLAYING, but that must never read as a plain, silent
// "running" — see Spec.OutputDegradedReason's doc comment.
func (r *runner) setRunningIfCurrent(gen int64) {
	r.mu.Lock()
	current := r.procGen
	reason := r.degradedReason
	r.mu.Unlock()
	if gen != current {
		return
	}
	r.setState(StateRunning, reason)
}

// setDegradedReason records why the currently-applied spec's output is not
// a real transport-backed sink, or "" when it is. Called at cmdApply from
// the newly-applied Spec's own OutputDegradedReason, and cleared at
// cmdClear — see [runner.setRunningIfCurrent].
func (r *runner) setDegradedReason(reason string) {
	r.mu.Lock()
	r.degradedReason = reason
	r.mu.Unlock()
}

// Logger is the minimal logging surface this package needs, so it does not
// import log/slog's full API surface into every call site — internal/agent
// passes its *slog.Logger, which satisfies this trivially.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func newRunner(surfaceID string, now func() time.Time, starter ProcessStarter, logger Logger, policy restartPolicy) *runner {
	r := &runner{
		surfaceID: surfaceID,
		now:       now,
		starter:   starter,
		logger:    logger,
		policy:    policy,
		cmds:      make(chan command, 4),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		snap: Snapshot{
			SurfaceID:  surfaceID,
			State:      StateStopped,
			Reason:     "no assignment applied yet",
			Since:      now(),
			ObservedAt: now(),
		},
	}
	go r.loop()
	return r
}

// setState updates the reported state under lock, stamping Since and
// ObservedAt from the same clock read so the two never disagree about when
// "now" was, and Generation from r.procGen (same lock, so this is always the
// generation the state transition actually happened under) — see
// [Snapshot.Generation]'s doc comment on why AwaitState needs this and
// finding 2 for the defect it closes.
func (r *runner) setState(state State, reason string) {
	r.mu.Lock()
	r.snap.State = state
	r.snap.Reason = reason
	r.snap.Since = r.now()
	r.snap.ObservedAt = r.snap.Since
	r.snap.Generation = r.procGen
	r.mu.Unlock()
}

// setCleared records whether this runner's surface is currently cleared —
// see the [runner.cleared] field's own doc comment.
func (r *runner) setCleared(v bool) {
	r.mu.Lock()
	r.cleared = v
	r.mu.Unlock()
}

// isCleared reports [runner.cleared] under lock.
func (r *runner) isCleared() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cleared
}

// setCounters updates the restart-accounting fields under lock, without
// touching State/Reason/Since — used where a state transition is reported
// separately, in the very next call, under its own r.mu acquisition.
func (r *runner) setCounters(restartCount, consecutiveFailures int64, lastExitCode *int, lastStderr string) {
	r.mu.Lock()
	r.snap.RestartCount = restartCount
	r.snap.ConsecutiveFailures = consecutiveFailures
	r.snap.LastExitCode = lastExitCode
	r.snap.LastStderr = lastStderr
	r.mu.Unlock()
}

// setFrameCounts overwrites the reported frame counters and, once
// measured, the achieved frame rate, called by B3's FrameWriter as it runs.
// rate is nil until FrameWriter.sampleRate has completed its first window —
// never defaulted or synthesized here. observedAt is that same sampling
// window's own close-of-window timestamp (zero until the first window
// closes), stored on FramesObservedAt, never on the shared ObservedAt.
// Deliberately does not touch State/Reason/Since/ObservedAt: frame counts
// are a separate evidence stream from pipeline lifecycle state and must not
// perturb AwaitState's post-dispatch confirmation check (see [Supervisor.
// AwaitState]).
func (r *runner) setFrameCounts(written, late, dropped int64, rate *float64, observedAt time.Time) {
	r.mu.Lock()
	r.snap.FramesWritten = written
	r.snap.FramesLate = late
	r.snap.FramesDropped = dropped
	r.snap.FramesRate = rate
	r.snap.FramesObservedAt = observedAt
	r.mu.Unlock()
}

// DrawingContent, DrawingIdle, and DrawingFailure are the three values
// [Snapshot.Drawing] (and [mqttproto.RenderSurfaceReport.Drawing]) can
// carry. See frame.go's FrameWriter, the only writer of this evidence.
const (
	DrawingContent = "content"
	DrawingIdle    = "idle"
	DrawingFailure = "failure"
)

// FailureOutputAlert and FailureOutputBlack are the two values
// [Snapshot.FailureOutput] can carry: which fallback a [DrawingFailure]
// tick actually put on the wire, decided by the node's operating mode at
// the moment of the failure (see frame.go's FrameWriter).
const (
	FailureOutputAlert = "alert"
	FailureOutputBlack = "black"
)

// DrawState is one tick's draw evidence, passed whole rather than as five
// positional arguments so a caller cannot silently transpose two strings
// that mean very different things.
type DrawState struct {
	TimelineState string
	PositionMS    *int64
	Drawing       string
	IdleMode      string
	FailureOutput string
}

// setDrawState records what a surface's FrameWriter actually wrote to the
// pipeline's stdin on its most recent tick — finding 7's fix. Deliberately
// does not touch State/Reason/Since/ObservedAt/Generation: this is a
// separate evidence stream from pipeline lifecycle state, the same rule
// [runner.setFrameCounts] already follows and for the same reason (must
// never perturb AwaitState's post-dispatch confirmation check).
func (r *runner) setDrawState(st DrawState) {
	r.mu.Lock()
	r.snap.TimelineState = st.TimelineState
	r.snap.TimelinePositionMS = st.PositionMS
	r.snap.Drawing = st.Drawing
	r.snap.IdleMode = st.IdleMode
	r.snap.FailureOutput = st.FailureOutput
	r.mu.Unlock()
}

// stdin returns the currently-running process's stdin writer, or an error
// if nothing is currently running. Called on every frame tick (never
// cached across ticks) so a mid-session pipeline restart is picked up on
// the very next frame rather than writing into a dead process's pipe.
func (r *runner) stdin() (io.Writer, error) {
	r.mu.Lock()
	p := r.currentProc
	r.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("pipeline: surface %q has no running process", r.surfaceID)
	}
	return p.Stdin()
}

// Snapshot returns exactly what is stored, including its own ObservedAt —
// it must NOT stamp ObservedAt from the current clock read. ObservedAt is
// evidence of when the reported state was actually confirmed (set by
// setState, at the moment of a real transition), and a caller comparing it
// against a dispatch time (see [Supervisor.AwaitState]) depends on that
// staying true. Overwriting it here would make every read report itself as
// fresh regardless of how stale the underlying state actually is — the
// exact defect this project already shipped once (a command "confirmed"
// 179 microseconds after its own dispatch off a stale reading).
func (r *runner) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snap.clone()
}

// setTransportProbe stores a probe's outcome under lock, touching only
// Transport/TransportAvailable/TransportReason/TransportObservedAt — NEVER
// the shared State/ObservedAt. An earlier version of this method stamped
// the shared ObservedAt too, reasoning that AwaitState's freshness check
// made it safe; it did not, because ObservedAt freshness with no state
// transition is exactly what let a re-apply confirm off the pipeline it had
// just killed (finding 2). The fix is this method never claiming evidence
// about State at all — only [runner.setState] may move ObservedAt, so "a
// fresh ObservedAt means the state actually moved" holds with no
// exceptions, which the coordinator's own render-command confirmation now
// depends on directly.
func (r *runner) setTransportProbe(transport string, available bool, reason string, observedAt time.Time) {
	r.mu.Lock()
	r.snap.Transport = transport
	v := available
	r.snap.TransportAvailable = &v
	r.snap.TransportReason = reason
	r.snap.TransportObservedAt = observedAt
	r.mu.Unlock()
}

// Pid reports the OS pid of the currently-running process, or 0 when
// nothing is currently running. Diagnostics only (e.g. an operator
// inspecting a stuck node); never part of the wire report.
func (r *runner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pid
}

// loop is this runner's entire control-and-supervision state machine,
// running on its own goroutine for the runner's whole life. It owns every
// mutable piece of restart-policy state (backoff, consecutiveFailures,
// current process handle) as plain local variables — none of it needs a
// lock because nothing outside this goroutine ever touches it; only
// r.snap (read via [runner.Snapshot]) is shared, and that is guarded by
// r.mu independently.
func (r *runner) loop() {
	defer close(r.done)

	var (
		spec         Spec
		haveSpec     bool
		proc         ProcessHandle
		exitCh       chan ExitResult
		backoff      = r.policy.initialBackoff
		consecFails  int64
		restartCount int64
		backoffTimer *time.Timer
	)

	stopBackoffTimer := func() {
		if backoffTimer != nil {
			backoffTimer.Stop()
			backoffTimer = nil
		}
	}

	// stopCurrent kills whatever process is currently running (if any) and
	// synchronously waits for its exit result, entirely within this call —
	// it never leaves an exit for the main select loop's crash-handling
	// branch to see. That branch (case res := <-exitCh below) therefore
	// only ever fires for a genuine, unrequested exit; it does not need to
	// distinguish "did we ask for this" because it never observes an exit
	// this function caused. Safe to call when proc is nil.
	stopCurrent := func() {
		if proc == nil {
			return
		}
		_ = proc.Kill()
		<-exitCh
		proc = nil
		exitCh = nil
		r.mu.Lock()
		r.pid = 0
		r.currentProc = nil
		r.mu.Unlock()
		r.bumpProcGen() // this attempt is no longer current; see F10
	}

	attemptStart := func() {
		path, ok, reason := ResolveGstLaunch()
		if !ok {
			r.setState(StateUnsupported, reason)
			return
		}

		argv, err := spec.BuildArgv()
		if err != nil {
			r.setState(StateFailed, "invalid pipeline spec: "+err.Error())
			return
		}

		r.setState(StateStarting, "pipeline started; PLAYING not yet observed")

		// A new generation for this attempt, captured by onRunning before
		// the starter is even called: some starters (the test fake) invoke
		// the marker synchronously, before Start returns.
		myGen := r.bumpProcGen()
		ec := make(chan ExitResult, 1)
		onRunning := func() {
			r.setRunningIfCurrent(myGen)
		}

		startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := r.starter(startCtx, path, argv, onRunning)
		cancel()
		if err != nil {
			consecFails++
			r.setCounters(restartCount, consecFails, nil, "")
			r.setState(StateFailed, "starting gst-launch-1.0: "+err.Error())
			return
		}

		proc = p
		exitCh = ec

		r.mu.Lock()
		r.pid = p.Pid()
		r.currentProc = p
		r.mu.Unlock()

		go func() {
			ec <- p.Wait()
		}()
	}

	for {
		select {
		case <-r.stop:
			stopBackoffTimer()
			stopCurrent()
			return

		case cmd := <-r.cmds:
			stopBackoffTimer()
			switch cmd.kind {
			case cmdApply:
				stopCurrent()
				spec = cmd.spec
				haveSpec = true
				backoff = r.policy.initialBackoff
				consecFails = 0
				r.setCounters(restartCount, 0, nil, "")
				// A fresh Apply is a new rendering session: frame counts
				// from whatever surface (or spec) was previously applied to
				// this ID describe a session that has ended, not this one.
				// FramesObservedAt resets to zero along with them: no
				// sampling window has closed yet for this new session.
				r.setFrameCounts(0, 0, 0, nil, time.Time{})
				r.setCleared(false)
				r.setDegradedReason(spec.OutputDegradedReason)
				attemptStart()
			case cmdClear:
				stopCurrent()
				haveSpec = false
				r.setFrameCounts(0, 0, 0, nil, time.Time{})
				r.setDegradedReason("")
				r.setState(StateStopped, "cleared by operator")
				r.setCleared(true)
			case cmdRestart:
				stopCurrent()
				backoff = r.policy.initialBackoff
				consecFails = 0
				r.setCounters(restartCount, 0, nil, "")
				if haveSpec {
					attemptStart()
				} else {
					r.setState(StateStopped, "restart requested with no surface assignment applied")
				}
			}

		// This branch only ever fires for a genuine, unrequested exit:
		// stopCurrent (above) always synchronously drains exitCh itself
		// before returning, so a Kill this runner issued is never observed
		// here.
		case res := <-nonNilExitCh(exitCh):
			proc = nil
			exitCh = nil
			r.mu.Lock()
			r.pid = 0
			r.currentProc = nil
			r.mu.Unlock()
			// This attempt is dead whether or not a restart follows —
			// bump now so a marker still in flight from it (F10) is
			// stale even when no new attempt starts, e.g. the lockout
			// branch below, which never calls attemptStart again.
			r.bumpProcGen()

			// Policy: consecFails counts consecutive attempts that never
			// reached PLAYING. An attempt that did reach PLAYING before
			// dying resets it, because that is real evidence the pipeline
			// spec itself works — deliberately NOT keyed to how much
			// wall-clock time elapsed before the crash. The prior
			// wall-clock-window version reset on any exit past
			// fastFailureWindow, so a pipeline crashing every ~3.1s (just
			// outside a 3s window) reset to 0 every time and could never
			// reach the lockout below, restarting forever at the backoff
			// cap — the exact silent infinite-restart shape this lockout
			// exists to prevent. A crash before PLAYING is still counted
			// even if it took a while, and a crash after PLAYING is never
			// counted even if it happened fast.
			restartCount++
			if res.SawRunningMarker {
				consecFails = 0
			} else {
				consecFails++
			}

			exitCode := res.ExitCode
			stderrTail := truncateStderr(res.StderrTail, maxStderrTailBytes, mqttproto.RenderStderrTruncatedSuffix)
			r.setCounters(restartCount, consecFails, exitCode, stderrTail)

			if consecFails >= r.policy.maxConsecutiveFastFailures {
				reason := fmt.Sprintf("pipeline failed %d consecutive times without reaching PLAYING; last stderr: %s",
					consecFails, oneLine(stderrTail))
				r.setState(StateFailed, reason)
				r.logger.Warn("pipeline entered failed lockout after repeated fast failures",
					"surface_id", r.surfaceID, "consecutive_failures", consecFails, "reason", reason)
				continue
			}

			reason := fmt.Sprintf("pipeline exited unexpectedly; last stderr: %s", oneLine(stderrTail))
			r.setState(StateRestarting, reason)
			r.logger.Warn("pipeline crashed; scheduling restart", "surface_id", r.surfaceID, "backoff", backoff, "reason", reason)

			backoffTimer = time.NewTimer(backoff)
			backoff *= 2
			if backoff > r.policy.maxBackoff {
				backoff = r.policy.maxBackoff
			}

		case <-timerC(backoffTimer):
			backoffTimer = nil
			attemptStart()
		}
	}
}

// nonNilExitCh returns ch unmodified when non-nil, or a nil channel
// otherwise — a nil channel blocks forever in a select, which is exactly
// "this case is not currently armed" without a separate boolean guard per
// branch.
func nonNilExitCh(ch chan ExitResult) chan ExitResult { return ch }

// timerC returns t.C when t is non-nil, or nil otherwise, for the same
// reason as [nonNilExitCh].
func timerC(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// oneLine collapses stderrTail to its last line (or a fixed-length prefix)
// for a one-line log/reason string; the full tail is still available via
// Snapshot.LastStderr.
func oneLine(s string) string {
	const max = 200
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}

// Supervisor owns one [runner] per surface assignment. ADR-026 decision 3
// requires N surfaces expressible even though v1 runs exactly one; nothing
// here assumes a single key.
type Supervisor struct {
	now     func() time.Time
	starter ProcessStarter
	logger  Logger
	policy  restartPolicy

	mu      sync.Mutex
	runners map[string]*runner
}

// NewSupervisor builds a Supervisor using [defaultRestartPolicy]. now and
// starter are injected (matching this codebase's clock/dependency-injection
// convention — see internal/agent/heartbeat.go); passing nil for either
// selects the production default (time.Now, [startRealProcess]).
func NewSupervisor(now func() time.Time, starter ProcessStarter, logger Logger) *Supervisor {
	return newSupervisorWithPolicy(now, starter, logger, defaultRestartPolicy)
}

// newSupervisorWithPolicy is [NewSupervisor] with the restart policy made
// explicit, so tests can shrink backoff/lockout timing without racing on a
// shared package-level var (every runner reads its Supervisor's own policy
// field, never a global).
func newSupervisorWithPolicy(now func() time.Time, starter ProcessStarter, logger Logger, policy restartPolicy) *Supervisor {
	if now == nil {
		now = time.Now
	}
	if starter == nil {
		starter = startRealProcess
	}
	return &Supervisor{
		now:     now,
		starter: starter,
		logger:  logger,
		policy:  policy,
		runners: make(map[string]*runner),
	}
}

func (s *Supervisor) runnerFor(surfaceID string) *runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runners[surfaceID]
	if !ok {
		r = newRunner(surfaceID, s.now, s.starter, s.logger, s.policy)
		s.runners[surfaceID] = r
	}
	return r
}

// runnerForExisting looks up surfaceID's runner WITHOUT creating one.
// Finding 18: Apply is this package's one deliberate bootstrap operation —
// every other entry point that used to go through [Supervisor.runnerFor]
// (Restart, SetTransportProbe) could manufacture a permanent phantom
// `surface` for a caller's typo'd or stale id, immediately visible in
// [Supervisor.SnapshotAll] with no discoverable removal path (its only
// clear path, Clear, has no reason to occur to an operator for a surface
// that never existed). Mirrors [Supervisor.Snapshot]'s own
// lookup-without-create shape.
func (s *Supervisor) runnerForExisting(surfaceID string) (*runner, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runners[surfaceID]
	return r, ok
}

// Apply starts (or restarts, if one is already running) spec's surface.
// Asynchronous: it hands the spec to the runner's control loop and returns
// immediately, before the process has started — see [Supervisor.AwaitState]
// for how a caller collects post-dispatch evidence.
func (s *Supervisor) Apply(spec Spec) error {
	if spec.SurfaceID == "" {
		return fmt.Errorf("pipeline: spec.SurfaceID must not be empty")
	}
	r := s.runnerFor(spec.SurfaceID)
	r.cmds <- command{kind: cmdApply, spec: spec}
	return nil
}

// Clear stops surfaceID's pipeline (if any) and marks it Stopped. A
// surfaceID that was never applied is a no-op that still ends up Stopped.
func (s *Supervisor) Clear(surfaceID string) error {
	if surfaceID == "" {
		return fmt.Errorf("pipeline: surfaceID must not be empty")
	}
	r := s.runnerFor(surfaceID)
	r.cmds <- command{kind: cmdClear}
	return nil
}

// Restart clears the failed-lockout counters and (re)starts surfaceID's
// pipeline from its currently-applied spec. Returns an error if no spec has
// ever been applied for surfaceID — there is nothing to restart. Finding
// 18: this doc comment used to be aspirational — runnerFor's create-on-
// demand meant a Restart against an unknown id silently manufactured a
// runner instead of erroring, so this is now backed by
// [Supervisor.runnerForExisting].
func (s *Supervisor) Restart(surfaceID string) error {
	if surfaceID == "" {
		return fmt.Errorf("pipeline: surfaceID must not be empty")
	}
	r, ok := s.runnerForExisting(surfaceID)
	if !ok {
		return fmt.Errorf("pipeline: no spec has ever been applied for surface %q; nothing to restart", surfaceID)
	}
	r.cmds <- command{kind: cmdRestart}
	return nil
}

// Snapshot reports surfaceID's current state, or ok=false if no runner has
// ever been created for it (i.e. Apply/Clear/Restart has never been called
// with that surface ID).
func (s *Supervisor) Snapshot(surfaceID string) (Snapshot, bool) {
	s.mu.Lock()
	r, ok := s.runners[surfaceID]
	s.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return r.Snapshot(), true
}

// Pid reports surfaceID's currently-running process id, or (0, false) when
// no runner exists for it or nothing is currently running. Diagnostics
// only.
func (s *Supervisor) Pid(surfaceID string) (int, bool) {
	s.mu.Lock()
	r, ok := s.runners[surfaceID]
	s.mu.Unlock()
	if !ok {
		return 0, false
	}
	pid := r.Pid()
	return pid, pid != 0
}

// Stdin returns surfaceID's currently-running process's stdin writer, for
// B3's frame writer. Returns an error if no runner exists for surfaceID or
// nothing is currently running for it — the caller (the frame writer) treats
// that as "nowhere to write this frame" and counts it, per this package's
// "never restart or stop the pipeline from here" rule; the supervisor's own
// crash detection is what reacts to a dead process.
func (s *Supervisor) Stdin(surfaceID string) (io.Writer, error) {
	s.mu.Lock()
	r, ok := s.runners[surfaceID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("pipeline: no runner for surface %q", surfaceID)
	}
	return r.stdin()
}

// SetFrameCounts overwrites surfaceID's reported frame counters and
// achieved rate (nil rate until measured, see FrameWriter.sampleRate).
// observedAt is that sampling window's own close-of-window instant (zero
// until the first window closes), recorded onto Snapshot.FramesObservedAt
// only, never the shared Snapshot.ObservedAt setState alone stamps. A
// no-op (never creates a runner) if surfaceID has never been applied: the
// frame writer that would call this only exists after Apply has already
// created one.
func (s *Supervisor) SetFrameCounts(surfaceID string, written, late, dropped int64, rate *float64, observedAt time.Time) {
	s.mu.Lock()
	r, ok := s.runners[surfaceID]
	s.mu.Unlock()
	if !ok {
		return
	}
	r.setFrameCounts(written, late, dropped, rate, observedAt)
}

// SetDrawState overwrites surfaceID's reported timeline/drawing evidence —
// finding 7's agent-side fix, called by B3's FrameWriter every tick. A no-op
// (never creates a runner) if surfaceID has never been applied, matching
// [Supervisor.SetFrameCounts]'s identical rule.
func (s *Supervisor) SetDrawState(surfaceID string, st DrawState) {
	s.mu.Lock()
	r, ok := s.runners[surfaceID]
	s.mu.Unlock()
	if !ok {
		return
	}
	r.setDrawState(st)
}

// Generation reports surfaceID's current process-attempt generation, or 0 if
// no runner exists yet for it — see [Snapshot.Generation]. Callers use this
// to capture a pre-dispatch baseline before Apply/Restart, so AwaitState can
// require the confirmed state to describe a strictly later attempt (finding
// 2), never a stale one whose ObservedAt happened to be refreshed by
// something unrelated (e.g. [Supervisor.SetTransportProbe]).
func (s *Supervisor) Generation(surfaceID string) int64 {
	snap, ok := s.Snapshot(surfaceID)
	if !ok {
		return 0
	}
	return snap.Generation
}

// MarkResumeFailed records that surfaceID holds a persisted assignment this
// node could not resume at boot (FSEQ missing, content-hash mismatch,
// unparseable persisted params, or any other buildAssignedSpec failure — see
// internal/agent/agent.go's boot-resume loop). Creates the runner if none
// exists yet, matching Apply/Clear/Restart/SetTransportProbe's own runnerFor
// use, so the next render report includes this surface as StateFailed with
// reason rather than silently omitting it (finding 9): the node knows it
// holds an assignment it could not honour, and logging that is not the same
// as reporting it.
func (s *Supervisor) MarkResumeFailed(surfaceID, reason string) {
	r := s.runnerFor(surfaceID)
	r.setState(StateFailed, reason)
}

// SnapshotAll reports every surface this Supervisor currently supervises,
// for the render report publisher — see internal/agent/renderreport.go. A
// CLEARED surface (Clear called and no Apply since) is excluded entirely,
// not reported as StateStopped: this is what lets the coordinator's
// noderender.Collector tell "this node cleared the surface" apart from
// "this node has nothing to say about it yet" and emit an explicit absence
// instead of a ghost StateStopped row that would otherwise persist forever
// — see that package's Poll doc comment. A runner still exists internally
// after Clear (Apply can reuse it), it is just no longer surfaced here.
func (s *Supervisor) SnapshotAll() []Snapshot {
	s.mu.Lock()
	runners := make([]*runner, 0, len(s.runners))
	for _, r := range s.runners {
		runners = append(runners, r)
	}
	s.mu.Unlock()

	out := make([]Snapshot, 0, len(runners))
	for _, r := range runners {
		if r.isCleared() {
			continue
		}
		out = append(out, r.Snapshot())
	}
	return out
}

// SetTransportProbe records surfaceID's most recent transport probe result
// directly on its snapshot, independent of the runner's own apply/clear/
// restart control loop — a probe (internal/agent/renderops.go's
// "render.transport.probe") is diagnostic evidence about a transport, not a
// pipeline lifecycle transition.
//
// Finding 18: this used to create the runner on demand if surfaceID had
// none yet, on the reasoning that a probe may legitimately run before any
// surface has ever been applied — but the caller that reasoning was
// written for is applySurface's own internal recordDegradedTransportEvidence
// call (renderops.go), which only ever runs after [Supervisor.Apply] has
// already created the runner for that exact surface. The externally
// reachable "render.transport.probe" command has no such guarantee, and a
// caller's typo'd or stale surface id used to manufacture a permanent
// phantom `surface` resource with no discoverable removal path. Returns
// false (recording nothing) when surfaceID has never been applied on this
// node — renderops.go's probeTransport checks this before running the
// probe at all, so a refusal is reported honestly rather than a probe
// running for evidence nobody asked to keep.
//
// observedAt is stamped onto the snapshot's OWN TransportObservedAt field,
// never onto the shared State/ObservedAt — see [runner.setTransportProbe]'s
// doc comment for why: this evidence is real and belongs on the snapshot,
// but it is evidence about the TRANSPORT, not about whether pipeline state
// moved, and conflating the two is precisely finding 2's defect.
func (s *Supervisor) SetTransportProbe(surfaceID, transport string, available bool, reason string, observedAt time.Time) bool {
	r, ok := s.runnerForExisting(surfaceID)
	if !ok {
		return false
	}
	r.setTransportProbe(transport, available, reason, observedAt)
	return true
}

// AwaitState polls surfaceID's snapshot until it reports one of want with
// evidence (ObservedAt) at or after notBefore AND a Generation strictly
// after afterGeneration, or ctx is done — whichever comes first. This is
// this seam's confirmation mechanism: starting a pipeline is asynchronous,
// so "confirmed" must rest on a poll that actually observed the desired
// state after the dispatch that requested it, never on the dispatch call
// returning without error. See internal/agent/command.go's OperationResult
// doc comment, which names this exact case (a GStreamer pipeline reaching
// PLAYING) in advance.
//
// ObservedAt freshness alone rests on an invariant this package now holds
// with no exceptions: ONLY [runner.setState] moves the shared State/
// ObservedAt pair, so a fresh ObservedAt always means the state actually
// transitioned (see [runner.setTransportProbe]'s own doc comment — an
// earlier version of this package violated that invariant, which was
// finding 2's real defect: a probe refreshing ObservedAt with no state
// change let a re-apply confirm off a STALE "running" snapshot describing
// the pipeline just killed). The Generation check below is defense in
// depth on top of that invariant, not a substitute for it: it additionally
// requires the confirmed snapshot to describe a strictly later process
// attempt than whatever was live before this caller's own dispatch, which
// protects against a FUTURE addition re-introducing an ObservedAt-without-
// a-transition path the way setTransportProbe once did. Pass afterGeneration
// = -1 to disable it for a caller with no analogous risk: clearSurface
// always synchronously calls setState(Stopped, ...) as part of processing
// the very command being awaited, so a generation floor there would only
// make an already-cleared surface's repeat Clear fail to confirm for no
// reason.
//
// found is false when ctx expired first; the caller should treat that as
// "unconfirmed," never as an error and never as success.
func (s *Supervisor) AwaitState(ctx context.Context, surfaceID string, want []State, notBefore time.Time, afterGeneration int64, pollInterval time.Duration) (Snapshot, bool) {
	wantSet := make(map[State]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		snap, ok := s.Snapshot(surfaceID)
		if ok && wantSet[snap.State] && !snap.ObservedAt.Before(notBefore) && snap.Generation > afterGeneration {
			return snap, true
		}

		select {
		case <-ctx.Done():
			snap, _ := s.Snapshot(surfaceID)
			return snap, false
		case <-ticker.C:
		}
	}
}

// Shutdown stops every runner's current process (marking each stop
// intentional) and waits for every control loop to exit. Bounded by ctx.
func (s *Supervisor) Shutdown(ctx context.Context) {
	s.mu.Lock()
	runners := make([]*runner, 0, len(s.runners))
	for _, r := range s.runners {
		runners = append(runners, r)
	}
	s.mu.Unlock()

	for _, r := range runners {
		close(r.stop)
	}
	for _, r := range runners {
		select {
		case <-r.done:
		case <-ctx.Done():
			return
		}
	}
}
