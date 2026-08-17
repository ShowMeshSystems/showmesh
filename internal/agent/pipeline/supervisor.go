package pipeline

import (
	"context"
	"fmt"
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
	// fastFailureWindow is how soon after a start attempt an exit counts as
	// a "fast failure" for the consecutive-failure lockout, rather than a
	// normal end to a pipeline that ran for a while.
	fastFailureWindow time.Duration

	// maxConsecutiveFastFailures bounds how many fast failures in a row a
	// runner will retry before giving up and reporting StateFailed. This is
	// the seam's own named acceptance criterion: a silent infinite restart
	// loop is the failure mode that looks healthy from every angle except
	// the wall, so retrying forever is never an option.
	maxConsecutiveFastFailures int64

	// initialBackoff and maxBackoff bound the exponential backoff between
	// restart attempts that are NOT fast failures (i.e. failures that
	// haven't yet tripped the lockout).
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// defaultRestartPolicy is what [NewSupervisor] uses.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for how long a
// genuinely broken pipeline takes to fail versus how long a healthy one
// takes to reach PLAYING, nor for the right backoff shape. These are
// conservative guesses — fastFailureWindow=3s comfortably longer than
// gst-launch's own startup cost on the reference hardware class (OptiPlex
// 7040) and comfortably shorter than "the pipeline ran the show";
// maxConsecutiveFastFailures=5 gives a transient failure (e.g. a
// momentarily busy device) a few chances without looping indefinitely on a
// permanently broken one.
var defaultRestartPolicy = restartPolicy{
	fastFailureWindow:          3 * time.Second,
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

	// Transport and TransportAvailable are always "" / nil in this seam —
	// B2a's pipeline has no real output stage, so there is nothing to
	// report and nothing has been probed. See [mqttproto.
	// RenderSurfaceReport.TransportAvailable]'s doc comment on why nil
	// means genuinely unprobed, not "false."
	Transport          string
	TransportAvailable *bool

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

	mu   sync.Mutex
	snap Snapshot
	pid  int // 0 when no process is currently running; diagnostics only, never part of the wire report
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
// "now" was.
func (r *runner) setState(state State, reason string) {
	r.mu.Lock()
	r.snap.State = state
	r.snap.Reason = reason
	r.snap.Since = r.now()
	r.snap.ObservedAt = r.snap.Since
	r.mu.Unlock()
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
		startedAt    time.Time
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
		r.mu.Unlock()
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

		r.setState(StateStarting, "")

		ec := make(chan ExitResult, 1)
		onRunning := func() {
			r.setState(StateRunning, "")
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
		startedAt = r.now()

		r.mu.Lock()
		r.pid = p.Pid()
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
				attemptStart()
			case cmdClear:
				stopCurrent()
				haveSpec = false
				r.setState(StateStopped, "cleared by operator")
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
			r.mu.Unlock()

			// A crash, whether or not the process ever reached PLAYING
			// first (a pipeline that starts, plays for a while, and then
			// dies is still a crash from here on).
			fast := r.now().Sub(startedAt) < r.policy.fastFailureWindow
			restartCount++
			if fast {
				consecFails++
			} else {
				consecFails = 0
			}

			exitCode := res.ExitCode
			stderrTail := truncateStderr(res.StderrTail, maxStderrTailBytes, mqttproto.RenderStderrTruncatedSuffix)
			r.setCounters(restartCount, consecFails, exitCode, stderrTail)

			if fast && consecFails >= r.policy.maxConsecutiveFastFailures {
				reason := fmt.Sprintf("pipeline failed %d consecutive times within %s of starting; last stderr: %s",
					consecFails, r.policy.fastFailureWindow, oneLine(stderrTail))
				r.setState(StateFailed, reason)
				r.logger.Warn("pipeline entered failed lockout after repeated fast failures",
					"surface_id", r.surfaceID, "consecutive_failures", consecFails)
				continue
			}

			reason := fmt.Sprintf("pipeline exited unexpectedly; last stderr: %s", oneLine(stderrTail))
			r.setState(StateRestarting, reason)
			r.logger.Warn("pipeline crashed; scheduling restart", "surface_id", r.surfaceID, "backoff", backoff)

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
// ever been applied for surfaceID — there is nothing to restart.
func (s *Supervisor) Restart(surfaceID string) error {
	if surfaceID == "" {
		return fmt.Errorf("pipeline: surfaceID must not be empty")
	}
	r := s.runnerFor(surfaceID)
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

// SnapshotAll reports every surface this Supervisor currently knows about,
// for the render report publisher — see internal/agent/renderreport.go.
func (s *Supervisor) SnapshotAll() []Snapshot {
	s.mu.Lock()
	runners := make([]*runner, 0, len(s.runners))
	for _, r := range s.runners {
		runners = append(runners, r)
	}
	s.mu.Unlock()

	out := make([]Snapshot, 0, len(runners))
	for _, r := range runners {
		out = append(out, r.Snapshot())
	}
	return out
}

// AwaitState polls surfaceID's snapshot until it reports one of want with
// evidence (ObservedAt) at or after notBefore, or ctx is done — whichever
// comes first. This is this seam's confirmation mechanism: starting a
// pipeline is asynchronous, so "confirmed" must rest on a poll that
// actually observed the desired state after the dispatch that requested it,
// never on the dispatch call returning without error. See
// internal/agent/command.go's OperationResult doc comment, which names this
// exact case (a GStreamer pipeline reaching PLAYING) in advance.
//
// found is false when ctx expired first; the caller should treat that as
// "unconfirmed," never as an error and never as success.
func (s *Supervisor) AwaitState(ctx context.Context, surfaceID string, want []State, notBefore time.Time, pollInterval time.Duration) (Snapshot, bool) {
	wantSet := make(map[State]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		snap, ok := s.Snapshot(surfaceID)
		if ok && wantSet[snap.State] && !snap.ObservedAt.Before(notBefore) {
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
