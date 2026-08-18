package pipeline

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRealProcessWaitJoinsScannersBeforeReporting is a structural regression
// test for F9: os/exec's StdoutPipe/StderrPipe doc requires every reader to
// finish before Wait is called, because Wait closes both pipes as soon as
// the process exits. This drives realProcess by hand with reader pipes that
// are NOT connected to the child process, so the child (which exits almost
// instantly) can never race the scanners by luck — the only way h.done can
// fire before the readers are released is if wait() fails to join them.
func TestRealProcessWaitJoinsScannersBeforeReporting(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting `true`: %v", err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	h := &realProcess{cmd: cmd, done: make(chan ExitResult, 1)}
	h.scanners.Add(2)
	go h.watchStdout(stdoutR, nil)
	go h.watchStderr(stderrR)
	go h.wait()

	// `true` exits within microseconds, well inside this window. If wait()
	// does not join the scanners first, h.done fires here.
	select {
	case <-h.done:
		t.Fatalf("wait() reported an outcome before the stdout/stderr scanners finished draining — the cmd.Wait() pipe-close race is back")
	case <-time.After(200 * time.Millisecond):
	}

	if _, err := stdoutW.Write([]byte(runningStateMarker + "\n")); err != nil {
		t.Fatalf("write stdout: %v", err)
	}
	if err := stdoutW.Close(); err != nil {
		t.Fatalf("close stdout: %v", err)
	}
	if _, err := stderrW.Write([]byte("the one diagnostic line\n")); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
	if err := stderrW.Close(); err != nil {
		t.Fatalf("close stderr: %v", err)
	}

	select {
	case res := <-h.done:
		if !strings.Contains(res.StderrTail, "the one diagnostic line") {
			t.Fatalf("StderrTail = %q, missing the line written before close", res.StderrTail)
		}
		if !res.SawRunningMarker {
			t.Fatalf("SawRunningMarker = false, want true: the marker was written before close")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("wait() never reported an outcome after both scanners were released")
	}
}

// TestWatchStderrKeepsDrainingAfterScanTooLong is a structural regression
// test for F9a: a line longer than the scanner's buffer must not stop the
// pipe from being drained, or a real child's write(2) blocks forever with
// nothing to detect it. io.Pipe is synchronous, so the writer goroutine can
// only finish this write if something keeps reading past the scan error;
// otherwise it hangs and the test times out.
func TestWatchStderrKeepsDrainingAfterScanTooLong(t *testing.T) {
	r, w := io.Pipe()
	h := &realProcess{done: make(chan ExitResult, 1)}
	h.scanners.Add(1)
	watcherDone := make(chan struct{})
	go func() {
		h.watchStderr(r)
		close(watcherDone)
	}()

	oversized := bytes.Repeat([]byte("x"), maxScanLineBytes+4096)
	writeDone := make(chan error, 1)
	go func() {
		_, err := w.Write(oversized)
		if err == nil {
			_, err = w.Write([]byte("\ntail\n"))
		}
		writeDone <- err
		_ = w.Close()
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("writer blocked forever: watchStderr stopped draining the pipe after a too-long line (F9a)")
	}

	select {
	case <-watcherDone:
	case <-time.After(1 * time.Second):
		t.Fatalf("watchStderr goroutine never returned after the pipe was closed")
	}
}
