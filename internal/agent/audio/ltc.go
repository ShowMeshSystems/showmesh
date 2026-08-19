package audio

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file supervises the external LTC generator process: generated
// live by an external libltc-based generator, streamed into the pipeline
// through fdsrc — a pre-rendered file is a hard no, and maintaining a
// GStreamer plugin is ruled out. It follows the same supervised-subprocess
// idiom internal/agent/pipeline uses for gst-launch-1.0 (start, watch,
// restart with bounded backoff, report state) rather than inventing a
// second one, narrowed to what this generator actually needs: it is not a
// GStreamer element chain, so it has no [pipeline.Spec]/argv-building
// counterpart here.
//
// Generator liveness is observed in its own right, never inferred from a
// pipeline still running. This package has no pipeline to infer from
// (Linear SM-68 is open — see [Manager]'s own doc comment), so the
// distinction this file actually enforces is narrower but still real: a
// generator process can be alive (its OS process has not exited) while
// producing nothing, and [LTCGeneratorSnapshot] never reports Running
// from process liveness alone — only from a heartbeat the generator
// itself emitted after the current attempt started.

// LTCGeneratorState is this supervisor's own lifecycle vocabulary,
// mirroring internal/agent/pipeline.State's shape one level down: starting
// (process launched, no heartbeat yet), running (a heartbeat has been
// seen), restarting (the process exited or went heartbeat-silent and a
// retry is scheduled), failed (bounded retries exhausted), stopped
// (never started, or explicitly stopped).
type LTCGeneratorState string

const (
	LTCGeneratorStarting    LTCGeneratorState = "starting"
	LTCGeneratorRunning     LTCGeneratorState = "running"
	LTCGeneratorRestarting  LTCGeneratorState = "restarting"
	LTCGeneratorFailed      LTCGeneratorState = "failed"
	LTCGeneratorStopped     LTCGeneratorState = "stopped"
	LTCGeneratorUnsupported LTCGeneratorState = "unsupported"
)

// LTCGeneratorSnapshot is the supervisor's current reported state — the
// evidence node.audio.ltc.generator.state/.reason/.frame_rate/.timecode
// carry (internal/agent/audioreport.go reads this on every report tick).
type LTCGeneratorSnapshot struct {
	State  LTCGeneratorState
	Reason string
	Since  time.Time

	RestartCount        int64
	ConsecutiveFailures int64
	LastExitCode        *int

	// FrameRateKnown/FrameRate report the rate the CURRENT (or
	// most-recently-applied) [LTCGeneratorSpec] was started against. False
	// before Start has ever been called — never a plausible-looking
	// default.
	FrameRateKnown bool
	FrameRate      pkgaudio.LTCFrameRate

	// TimecodeKnown/Timecode are the generator's own self-reported
	// position, from its most recent heartbeat line — see
	// [heartbeatPattern]. Present only while State is Running: a
	// generator that has gone silent or exited has no fresh evidence
	// about what it is currently emitting, and this type must never carry
	// a stale value forward as if it were current.
	TimecodeKnown bool
	Timecode      pkgaudio.LTCTimecode

	ObservedAt time.Time
}

func (s LTCGeneratorSnapshot) clone() LTCGeneratorSnapshot {
	out := s
	if s.LastExitCode != nil {
		v := *s.LastExitCode
		out.LastExitCode = &v
	}
	return out
}

// LTCGeneratorSpec is one run's configuration: the frame rate and start
// offset the generator process is invoked with. Both are validated by the
// caller (audio.settings/audio.session.apply's own decode paths) before
// reaching here; this package trusts them.
type LTCGeneratorSpec struct {
	FrameRate   pkgaudio.LTCFrameRate
	StartOffset pkgaudio.LTCTimecode
	SampleRate  int
}

// LTCProcessHandle is the live handle to one started generator process —
// this package's own narrow counterpart to
// [github.com/showmeshsystems/showmesh/internal/agent/pipeline.ProcessHandle],
// not a re-export: that type's Stdin() method assumes a gst-launch-1.0
// stdin frame writer, which has no meaning for a generator whose OWN
// stdout is the payload.
type LTCProcessHandle interface {
	// Wait blocks until the process exits and returns the outcome. Must
	// return exactly once, including after Kill.
	Wait() LTCExitResult
	// Kill forcibly terminates the process. Idempotent.
	Kill() error
	Pid() int
}

// LTCExitResult is what Wait reports once a generator process has ended.
type LTCExitResult struct {
	ExitCode *int
	Signaled bool
	// SawHeartbeat is true if this process ever emitted a valid heartbeat
	// line before exiting — the generator-side counterpart to
	// [pipeline.ExitResult.SawRunningMarker], gating the identical
	// fast-failure lockout policy: an attempt that proved itself alive at
	// least once resets the consecutive-failure counter, one that never
	// did counts toward the lockout.
	SawHeartbeat bool
}

// LTCProcessStarter starts path with args, reporting every heartbeat line
// the process emits via onHeartbeat as it happens (not just the first —
// unlike pipeline's onRunningMarker, this supervisor needs every heartbeat
// to detect the process going silent while still alive).
type LTCProcessStarter func(ctx context.Context, path string, args []string, onHeartbeat func(pkgaudio.LTCTimecode)) (LTCProcessHandle, error)

// heartbeatPattern is the contract this seam's production generator binary
// must satisfy: one line to stderr per emitted frame (or at minimum,
// faster than [heartbeatTimeout]) of the form "HEARTBEAT HH:MM:SS:FF".
// bench/audio-node/ltcgen.c does not implement this — it is a one-shot
// file writer that only prints a summary at exit (see that file's own doc
// comment) — so this is a documented requirement on the not-yet-built
// production binary, not a description of shipped behavior.
var heartbeatPattern = regexp.MustCompile(`^HEARTBEAT ([0-9]{2}:[0-9]{2}:[0-9]{2}:[0-9]{2})$`)

// heartbeatTimeout bounds how long this supervisor waits for a fresh
// heartbeat before treating a nominally-still-running process as dead.
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists for the right
// bound; 3 seconds is chosen to be well above one video frame period at
// every supported frame rate while still catching a stall within one
// show cue.
const heartbeatTimeout = 3 * time.Second

// envLTCGenOverride names the environment variable an operator can set to
// point at the LTC generator binary outside PATH, mirroring
// [pipeline.envGstLaunchOverride]'s identical shape one package over.
const envLTCGenOverride = "SHOWMESH_LTCGEN"

var (
	ltcLookPathFunc  = exec.LookPath
	ltcLookupEnvFunc = os.LookupEnv
)

// ResolveLTCGen locates the external LTC generator binary: SHOWMESH_LTCGEN
// when set (used as-is), otherwise "showmesh-ltcgen" resolved via PATH. ok
// is false when neither yields a usable path — an absent binary degrades
// this supervisor to Unsupported, never stops the agent, the same rule
// ADR-025/ADR-026 already settled for a missing key or a missing NDI
// runtime.
func ResolveLTCGen() (path string, ok bool, reason string) {
	if override, set := ltcLookupEnvFunc(envLTCGenOverride); set && override != "" {
		return override, true, ""
	}
	resolved, err := ltcLookPathFunc("showmesh-ltcgen")
	if err != nil {
		return "", false, "showmesh-ltcgen not found on PATH and " + envLTCGenOverride + " is not set: " + err.Error()
	}
	return resolved, true, ""
}

// ltcGeneratorRestartPolicy mirrors pipeline's defaultRestartPolicy shape;
// SHOWMESH HYPOTHESIS, NOT MEASURED, same posture as that constant's own
// doc comment.
type ltcGeneratorRestartPolicy struct {
	maxConsecutiveFastFailures int64
	initialBackoff             time.Duration
	maxBackoff                 time.Duration

	// heartbeatTimeout is a policy field (not the package const directly)
	// so a test can shrink it — matching pipeline's own
	// newSupervisorWithPolicy rationale: every generator reads its own
	// policy's copy, never a shared package-level var, so concurrent
	// tests with different timeouts never race each other.
	heartbeatTimeout time.Duration
}

var defaultLTCGeneratorRestartPolicy = ltcGeneratorRestartPolicy{
	maxConsecutiveFastFailures: 5,
	initialBackoff:             500 * time.Millisecond,
	maxBackoff:                 30 * time.Second,
	heartbeatTimeout:           heartbeatTimeout,
}

type ltcCommandKind int

const (
	ltcCmdStart ltcCommandKind = iota
	ltcCmdStop
)

type ltcCommand struct {
	kind ltcCommandKind
	spec LTCGeneratorSpec
}

// LTCGenerator supervises exactly one LTC generator process across its
// whole life: zero or more Start calls, zero or more crashes and
// heartbeat-timeouts, until Shutdown. One node runs at most one — LTC is a
// node-level signal (node.audio.ltc.*), not a per-session one.
type LTCGenerator struct {
	now     func() time.Time
	starter LTCProcessStarter
	policy  ltcGeneratorRestartPolicy

	cmds chan ltcCommand
	stop chan struct{}
	done chan struct{}

	mu   sync.Mutex
	snap LTCGeneratorSnapshot
}

// NewLTCGenerator builds an LTCGenerator. now/starter default to
// time.Now/[startRealLTCProcess] when nil, matching
// [github.com/showmeshsystems/showmesh/internal/agent/pipeline.NewSupervisor]'s
// identical injection convention.
func NewLTCGenerator(now func() time.Time, starter LTCProcessStarter) *LTCGenerator {
	return newLTCGeneratorWithPolicy(now, starter, defaultLTCGeneratorRestartPolicy)
}

func newLTCGeneratorWithPolicy(now func() time.Time, starter LTCProcessStarter, policy ltcGeneratorRestartPolicy) *LTCGenerator {
	if now == nil {
		now = time.Now
	}
	if starter == nil {
		starter = startRealLTCProcess
	}
	g := &LTCGenerator{
		now: now, starter: starter, policy: policy,
		cmds: make(chan ltcCommand, 4),
		stop: make(chan struct{}),
		done: make(chan struct{}),
		snap: LTCGeneratorSnapshot{
			State: LTCGeneratorStopped, Reason: "never started",
			Since: now(), ObservedAt: now(),
		},
	}
	go g.loop()
	return g
}

// Start applies spec: begins (or restarts, if already running) the
// generator. Asynchronous — returns immediately; Snapshot reports progress.
func (g *LTCGenerator) Start(spec LTCGeneratorSpec) {
	g.cmds <- ltcCommand{kind: ltcCmdStart, spec: spec}
}

// Stop halts the generator and marks it Stopped.
func (g *LTCGenerator) Stop() {
	g.cmds <- ltcCommand{kind: ltcCmdStop}
}

// Snapshot returns exactly what is stored, including its own ObservedAt —
// never stamped from the current clock read, matching
// [pipeline.runner.Snapshot]'s identical rule and the reason given there:
// a caller comparing ObservedAt against a dispatch time must see real
// evidence age, not a freshened illusion of it.
func (g *LTCGenerator) Snapshot() LTCGeneratorSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snap.clone()
}

func (g *LTCGenerator) setState(state LTCGeneratorState, reason string) {
	g.mu.Lock()
	g.snap.State = state
	g.snap.Reason = reason
	g.snap.Since = g.now()
	g.snap.ObservedAt = g.snap.Since
	if state != LTCGeneratorRunning {
		g.snap.TimecodeKnown = false
	}
	g.mu.Unlock()
}

func (g *LTCGenerator) setCounters(restartCount, consecFails int64, lastExitCode *int) {
	g.mu.Lock()
	g.snap.RestartCount = restartCount
	g.snap.ConsecutiveFailures = consecFails
	g.snap.LastExitCode = lastExitCode
	g.mu.Unlock()
}

func (g *LTCGenerator) setFrameRate(rate pkgaudio.LTCFrameRate) {
	g.mu.Lock()
	g.snap.FrameRateKnown = true
	g.snap.FrameRate = rate
	g.mu.Unlock()
}

// setHeartbeat records a fresh heartbeat's timecode. Called only while a
// generation the loop still considers current is alive — see
// [LTCGenerator.loop]'s onHeartbeat closure — so a heartbeat from an
// attempt the loop has already moved past can never mark a superseded
// process Running.
func (g *LTCGenerator) setHeartbeat(tc pkgaudio.LTCTimecode) {
	g.mu.Lock()
	g.snap.State = LTCGeneratorRunning
	g.snap.Reason = ""
	g.snap.TimecodeKnown = true
	g.snap.Timecode = tc
	now := g.now()
	g.snap.Since = now
	g.snap.ObservedAt = now
	g.mu.Unlock()
}

// loop is this generator's entire control-and-supervision state machine.
// Structurally the same shape as [pipeline.runner.loop] one level down:
// stopCurrent kills synchronously before starting anything new, a
// heartbeat timer distinguishes "alive" from "producing," and a bounded
// consecutive-failure counter prevents a silently-infinite restart loop —
// see that method's doc comment for why the lockout is keyed to proven
// liveness (SawHeartbeat) rather than a wall-clock window.
func (g *LTCGenerator) loop() {
	defer close(g.done)

	var (
		spec         LTCGeneratorSpec
		haveSpec     bool
		proc         LTCProcessHandle
		exitCh       chan LTCExitResult
		backoff      = g.policy.initialBackoff
		consecFails  int64
		restartCount int64
		backoffTimer *time.Timer
		heartbeatT   *time.Timer
		gen          int64
	)

	stopTimer := func(t **time.Timer) {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}

	stopCurrent := func() {
		if proc == nil {
			return
		}
		_ = proc.Kill()
		<-exitCh
		proc, exitCh = nil, nil
		stopTimer(&heartbeatT)
		gen++
	}

	attemptStart := func() {
		path, ok, reason := ResolveLTCGen()
		if !ok {
			g.setState(LTCGeneratorUnsupported, reason)
			return
		}
		g.setState(LTCGeneratorStarting, "process launched; no heartbeat yet")
		g.setFrameRate(spec.FrameRate)

		myGen := gen
		ec := make(chan LTCExitResult, 1)
		onHeartbeat := func(tc pkgaudio.LTCTimecode) {
			g.mu.Lock()
			current := gen
			g.mu.Unlock()
			if myGen != current {
				return
			}
			g.setHeartbeat(tc)
		}

		startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := g.starter(startCtx, path, ltcGeneratorArgs(spec), onHeartbeat)
		cancel()
		if err != nil {
			consecFails++
			g.setCounters(restartCount, consecFails, nil)
			g.setState(LTCGeneratorFailed, "starting generator: "+err.Error())
			return
		}
		proc, exitCh = p, ec
		go func() { ec <- p.Wait() }()

		stopTimer(&heartbeatT)
		heartbeatT = time.NewTimer(g.policy.heartbeatTimeout)
	}

	for {
		select {
		case <-g.stop:
			stopTimer(&backoffTimer)
			stopCurrent()
			return

		case cmd := <-g.cmds:
			stopTimer(&backoffTimer)
			switch cmd.kind {
			case ltcCmdStart:
				stopCurrent()
				spec, haveSpec = cmd.spec, true
				backoff, consecFails = g.policy.initialBackoff, 0
				g.setCounters(restartCount, 0, nil)
				attemptStart()
			case ltcCmdStop:
				stopCurrent()
				haveSpec = false
				g.setState(LTCGeneratorStopped, "stopped by operator")
			}

		case res := <-nonNilLTCExitCh(exitCh):
			proc, exitCh = nil, nil
			stopTimer(&heartbeatT)
			gen++

			restartCount++
			if res.SawHeartbeat {
				consecFails = 0
			} else {
				consecFails++
			}
			g.setCounters(restartCount, consecFails, res.ExitCode)

			if consecFails >= g.policy.maxConsecutiveFastFailures {
				g.setState(LTCGeneratorFailed, fmt.Sprintf("generator failed %d consecutive times without a heartbeat", consecFails))
				continue
			}
			g.setState(LTCGeneratorRestarting, "generator process exited unexpectedly; scheduling restart")
			backoffTimer = time.NewTimer(backoff)
			backoff *= 2
			if backoff > g.policy.maxBackoff {
				backoff = g.policy.maxBackoff
			}

		case <-nonNilLTCTimerCh(heartbeatT):
			// The process is (as far as we know) still alive, but has
			// produced no heartbeat within the timeout — ruling 4's exact
			// case: liveness is not inferred from the process merely
			// existing. Kill it and let the crash-handling branch above
			// schedule a restart through the identical backoff/lockout
			// path a genuine exit would.
			heartbeatT = nil
			if proc != nil {
				_ = proc.Kill()
			}
			g.setState(LTCGeneratorRestarting, fmt.Sprintf("no heartbeat within %s; generator process may still be running", g.policy.heartbeatTimeout))

		case <-nonNilLTCTimerCh(backoffTimer):
			backoffTimer = nil
			if haveSpec {
				attemptStart()
			}
		}
	}
}

func nonNilLTCExitCh(ch chan LTCExitResult) chan LTCExitResult { return ch }
func nonNilLTCTimerCh(t *time.Timer) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}

// Shutdown stops the generator's current process (if any) and waits for
// the control loop to exit, bounded by ctx.
func (g *LTCGenerator) Shutdown(ctx context.Context) {
	close(g.stop)
	select {
	case <-g.done:
	case <-ctx.Done():
	}
}

// ltcGeneratorArgs builds the generator binary's argv from spec. The exact
// flag shape is this supervisor's side of a contract with the
// not-yet-built production binary (bench/audio-node/ltcgen.c is a
// fixed-duration bench tool, not this contract's implementation — see
// [heartbeatPattern]'s doc comment): --rate, --offset, and
// --sample-rate, run with no --duration so the process runs until killed,
// which is what "supervised long-running generator, not a one-shot file
// writer" requires.
func ltcGeneratorArgs(spec LTCGeneratorSpec) []string {
	sr := spec.SampleRate
	if sr <= 0 {
		sr = 48000
	}
	return []string{
		"--rate", string(spec.FrameRate),
		"--offset", string(spec.StartOffset),
		"--sample-rate", fmt.Sprintf("%d", sr),
	}
}

// startRealLTCProcess is the production [LTCProcessStarter]: exec.Command
// with stderr scanned line by line for [heartbeatPattern], calling
// onHeartbeat on every match (not just the first, unlike
// [pipeline.startRealProcess]'s one-shot marker — see
// [LTCProcessStarter]'s doc comment). Stdout (the raw PCM payload) is
// drained to io.Discard: no pipeline exists yet in this repository to
// consume it (Linear SM-68 is open), and a process whose stdout pipe
// fills blocks in write(2) forever, which would silently defeat this
// supervisor's own heartbeat-timeout detection.
func startRealLTCProcess(ctx context.Context, path string, args []string, onHeartbeat func(pkgaudio.LTCTimecode)) (LTCProcessHandle, error) {
	cmd := exec.Command(path, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("audio: ltc generator stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("audio: ltc generator stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("audio: starting %s: %w", path, err)
	}

	h := &realLTCProcess{cmd: cmd, done: make(chan LTCExitResult, 1)}
	h.pipes.Add(2)
	go h.drainStdout(stdout)
	go h.watchStderr(stderr, onHeartbeat)
	go h.wait()

	return h, nil
}

type realLTCProcess struct {
	cmd  *exec.Cmd
	done chan LTCExitResult

	mu           sync.Mutex
	sawHeartbeat bool

	pipes sync.WaitGroup
}

func (h *realLTCProcess) drainStdout(r io.Reader) {
	defer h.pipes.Done()
	_, _ = io.Copy(io.Discard, r)
}

func (h *realLTCProcess) watchStderr(r io.Reader, onHeartbeat func(pkgaudio.LTCTimecode)) {
	defer h.pipes.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		m := heartbeatPattern.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		tc := pkgaudio.LTCTimecode(m[1])
		if tc.Validate() != nil {
			continue
		}
		h.mu.Lock()
		h.sawHeartbeat = true
		h.mu.Unlock()
		if onHeartbeat != nil {
			onHeartbeat(tc)
		}
	}
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, r)
	}
}

func (h *realLTCProcess) wait() {
	h.pipes.Wait()
	err := h.cmd.Wait()

	h.mu.Lock()
	saw := h.sawHeartbeat
	h.mu.Unlock()

	result := LTCExitResult{SawHeartbeat: saw}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code := 0
		result.ExitCode = &code
	case isExitError(err, &exitErr):
		if exitErr.ProcessState != nil {
			if exitErr.Exited() {
				code := exitErr.ExitCode()
				result.ExitCode = &code
			} else {
				result.Signaled = true
			}
		}
	default:
		result.Signaled = true
	}
	h.done <- result
}

// isExitError narrows err to *exec.ExitError without importing "errors"
// twice over in this file for one call site.
func isExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func (h *realLTCProcess) Wait() LTCExitResult { return <-h.done }

func (h *realLTCProcess) Kill() error {
	if h.cmd.Process == nil {
		return nil
	}
	return h.cmd.Process.Kill()
}

func (h *realLTCProcess) Pid() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

// ResolveLTCStartOffset implements this project's start-offset precedence
// exactly: a session's own override, when present, always wins; otherwise the
// coordinator's audio.settings default applies. This is the one function
// that resolution rule lives in, so whatever eventually triggers a
// generator Start (session prepare/start, once a pipeline backend exists
// — Linear SM-68) calls this rather than re-deciding the precedence at
// its own call site.
func ResolveLTCStartOffset(sessionOverride *pkgaudio.LTCTimecode, settingsDefault pkgaudio.LTCTimecode) pkgaudio.LTCTimecode {
	if sessionOverride != nil {
		return *sessionOverride
	}
	return settingsDefault
}
