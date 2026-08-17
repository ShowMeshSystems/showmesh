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

	h := &realProcess{cmd: cmd, done: make(chan ExitResult, 1)}

	go h.watchStdout(stdout, onRunningMarker)
	go h.watchStderr(stderr)
	go h.wait()

	return h, nil
}

// realProcess is the production [ProcessHandle].
type realProcess struct {
	cmd  *exec.Cmd
	done chan ExitResult

	mu               sync.Mutex
	stderrTail       []byte
	sawRunningMarker bool

	killOnce sync.Once
	killErr  error
}

func (h *realProcess) watchStdout(r io.Reader, onRunningMarker func()) {
	scanner := bufio.NewScanner(r)
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
}

func (h *realProcess) watchStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		h.mu.Lock()
		h.stderrTail = appendBounded(h.stderrTail, line, maxStderrTailBytes)
		h.mu.Unlock()
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
