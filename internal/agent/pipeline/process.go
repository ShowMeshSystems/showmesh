package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// ErrNoStdin is returned by [ProcessHandle.Stdin] when the process was
// started without a usable stdin pipe. [startRealProcess] always requests
// one, so this only fires if pipe creation itself failed before Start.
var ErrNoStdin = errors.New("pipeline: process has no stdin pipe")

// maxStderrTailBytes bounds the in-memory stderr ring buffer this package
// keeps per process attempt. Matches [mqttproto]'s maxRenderStderrBytes so a
// tail captured here never has to be truncated a second time at the wire
// boundary — it may still be, defensively, but the common case is exact.
const maxStderrTailBytes = 4 * 1024

// runningStateMarker is the exact substring gst-launch-1.0 writes to stdout
// (with no "-v" flag needed — this is default output) when a pipeline
// finishes its PAUSED->PLAYING transition. Watched for, never parsed
// further: this is a measured fact about gst-launch-1.0's default output,
// not a documented, versioned contract, so a future GStreamer release
// changing this string degrades detection to "process alive, PLAYING never
// observed" rather than crashing anything.
const runningStateMarker = "Setting pipeline to PLAYING"

// ProcessHandle is the live handle to one started child process. The
// production implementation wraps os/exec; tests substitute a fake to
// exercise restart/backoff policy without spawning anything, and a second
// class of test runs the real gst-launch-1.0 binary end to end (see
// supervisor_realprocess_test.go).
type ProcessHandle interface {
	// Wait blocks until the process exits (however it exits — clean,
	// errored, or signalled) and returns the outcome. It must return
	// exactly once and must return even when the process was Kill()ed.
	Wait() ExitResult

	// Kill forcibly terminates the process (SIGKILL, not a graceful
	// request) — used both for a genuine kill -9 style forced stop and for
	// this package's own "stop the old pipeline before starting the new
	// one" transition. Idempotent: calling it after the process has
	// already exited must not error.
	Kill() error

	// Pid reports the OS process id, for logging only.
	Pid() int

	// Stdin returns the process's stdin pipe, for B3's frame writer to
	// write raw frame buffers into (fed to gst-launch-1.0's fdsrc/stdin
	// source stage — see spec.go's FSEQSourceSpec). Returns ErrNoStdin if
	// none is available. The returned writer is only valid for the life of
	// this ProcessHandle; a caller must re-fetch it (via the supervisor,
	// never cached across a restart) after every process transition.
	Stdin() (io.Writer, error)
}

// ExitResult is what Wait reports once a process has actually ended.
type ExitResult struct {
	// ExitCode is nil when the process was killed by a signal rather than
	// exiting on its own (mirrors [os.ProcessState.ExitCode]'s own -1
	// convention, but as a pointer so "no exit code because it never
	// started" and "exited 0" are not confused with each other upstream).
	ExitCode *int

	// Signaled is true when the process ended via a signal (e.g. this
	// package's own Kill, or an external kill -9), false when it exited on
	// its own.
	Signaled bool

	// StderrTail is the last [maxStderrTailBytes] of stderr this process
	// wrote, oldest bytes dropped first.
	StderrTail string

	// SawRunningMarker is true if this process's stdout ever contained
	// [runningStateMarker] before it exited — i.e. this process reached
	// PLAYING at some point during its life, even if it later crashed.
	SawRunningMarker bool
}

// ProcessStarter starts path with args and returns a live handle. A
// package-level var (matching internal/agent/assets.go's assetHTTPClient
// and readBackAssetFunc convention) so tests can prove the call site
// actually invokes it rather than trusting an assumption about process
// behaviour.
type ProcessStarter func(ctx context.Context, path string, args []string, onRunningMarker func()) (ProcessHandle, error)

// startRealProcess is the production [ProcessStarter]: exec.Command, with
// stdout scanned line by line for [runningStateMarker] (calling
// onRunningMarker the first time it appears) and stderr captured into a
// bounded ring buffer. ctx is NOT used to kill the process (os/exec's
// CommandContext-on-cancel would race this package's own explicit Kill
// bookkeeping); ctx only bounds the time spent launching it.
func startRealProcess(ctx context.Context, path string, args []string, onRunningMarker func()) (ProcessHandle, error) {
	cmd := exec.Command(path, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pipeline: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipeline: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pipeline: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pipeline: starting %s: %w", path, err)
	}

	h := &realProcess{cmd: cmd, stdin: stdin, done: make(chan ExitResult, 1)}

	// os/exec's StdoutPipe/StderrPipe doc states this verbatim: Wait closes
	// both pipes as soon as the process exits, so calling it before every
	// pipe reader has finished loses in-flight output. h.scanners is joined
	// before h.wait calls cmd.Wait() to honour that rule.
	h.scanners.Add(2)
	go h.watchStdout(stdout, onRunningMarker)
	go h.watchStderr(stderr)
	go h.wait()

	return h, nil
}

// realProcess is the production [ProcessHandle].
type realProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan ExitResult

	mu               sync.Mutex
	stderrTail       []byte
	sawRunningMarker bool

	// scanners is released once by each of watchStdout/watchStderr, however
	// they exit (normal EOF or a scan error), so wait() never calls
	// cmd.Wait() while a pipe reader is still in flight.
	scanners sync.WaitGroup

	killOnce sync.Once
	killErr  error
}

// maxScanLineBytes bounds a single scanned line. Larger than the bufio
// default (64 KiB) so ordinary long lines never trip it, but still finite:
// a scan error must never stop draining, or the child blocks forever in
// write(2) once the pipe's kernel buffer fills.
const maxScanLineBytes = 1 << 20

func (h *realProcess) watchStdout(r io.Reader, onRunningMarker func()) {
	defer h.scanners.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLineBytes)
	fired := false
	for scanner.Scan() {
		line := scanner.Text()
		if !fired && strings.Contains(line, runningStateMarker) {
			fired = true
			h.mu.Lock()
			h.sawRunningMarker = true
			h.mu.Unlock()
			if onRunningMarker != nil {
				onRunningMarker()
			}
		}
	}
	// A scan error (e.g. ErrTooLong) stops Scan from returning more lines,
	// but the pipe itself is still open and the child can still be writing
	// to it. Keep draining so the child's write(2) never blocks forever.
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, r)
	}
}

func (h *realProcess) watchStderr(r io.Reader) {
	defer h.scanners.Done()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanLineBytes)
	for scanner.Scan() {
		line := scanner.Bytes()
		h.mu.Lock()
		h.stderrTail = appendBounded(h.stderrTail, line, maxStderrTailBytes)
		h.mu.Unlock()
	}
	if scanner.Err() != nil {
		_, _ = io.Copy(io.Discard, r)
	}
}

// appendBounded appends line (plus a newline) to tail, dropping the oldest
// bytes first whenever the result would exceed max. Kept as a free function
// (not a method) so supervisor.go's ring-buffer-adjacent formatting can
// reuse identical truncation behaviour without going through a
// *realProcess.
func appendBounded(tail, line []byte, max int) []byte {
	tail = append(tail, line...)
	tail = append(tail, '\n')
	if len(tail) > max {
		tail = tail[len(tail)-max:]
	}
	return tail
}

func (h *realProcess) wait() {
	h.scanners.Wait()
	err := h.cmd.Wait()

	h.mu.Lock()
	tail := string(h.stderrTail)
	saw := h.sawRunningMarker
	h.mu.Unlock()

	result := ExitResult{StderrTail: tail, SawRunningMarker: saw}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code := 0
		result.ExitCode = &code
	case errors.As(err, &exitErr):
		if exitErr.ProcessState != nil {
			if exitErr.Exited() {
				code := exitErr.ExitCode()
				result.ExitCode = &code
			} else {
				result.Signaled = true
			}
		}
	default:
		// Start()-adjacent failure surfaced through Wait rather than Start
		// itself; treat as signalled-equivalent (no exit code) so the
		// supervisor's fast-failure counting still applies.
		result.Signaled = true
	}

	h.done <- result
}

func (h *realProcess) Wait() ExitResult {
	return <-h.done
}

func (h *realProcess) Kill() error {
	h.killOnce.Do(func() {
		if h.cmd.Process == nil {
			return
		}
		h.killErr = h.cmd.Process.Kill()
	})
	return h.killErr
}

func (h *realProcess) Pid() int {
	if h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
}

// Stdin returns the process's stdin pipe. A write to this pipe after the
// process has exited returns an error (broken pipe) rather than blocking or
// panicking; the frame writer treats that as a dropped frame and relies on
// this package's own crash detection (via Wait) to notice the exit and
// restart, never killing or restarting the process itself.
func (h *realProcess) Stdin() (io.Writer, error) {
	if h.stdin == nil {
		return nil, ErrNoStdin
	}
	return h.stdin, nil
}

// truncateStderr bounds s to max bytes, appending suffix (visibly) when
// truncation actually happened — the mqttproto-boundary counterpart to
// [appendBounded]'s streaming truncation, applied once more at publish time
// so a caller of this package never has to trust that the ring buffer was
// sized identically to the wire cap.
func truncateStderr(s string, max int, suffix string) string {
	if len(s) <= max {
		return s
	}
	cut := max - len(suffix)
	if cut < 0 {
		cut = 0
	}
	return s[len(s)-cut:] + suffix
}
