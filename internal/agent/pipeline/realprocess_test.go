package pipeline

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// requireGstLaunch skips the calling test, with a stated reason (never a
// silent pass), when gst-launch-1.0 is not on PATH — so CI without
// GStreamer installed does not break, per this seam's explicit requirement.
func requireGstLaunch(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("gst-launch-1.0")
	if err != nil {
		t.Skipf("skipping: gst-launch-1.0 not found on PATH (%v) — this test exercises the real binary and cannot substitute a fake for it", err)
	}
	return path
}

// TestRealProcessStartsAndExitsCleanly runs a genuinely trivial pipeline
// (videotestsrc with a bounded buffer count, so it exits on its own) through
// [startRealProcess] against the real gst-launch-1.0 binary, and confirms
// this package observes both the running marker and a clean exit.
func TestRealProcessStartsAndExitsCleanly(t *testing.T) {
	path := requireGstLaunch(t)

	var sawRunning bool
	onRunning := func() { sawRunning = true }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proc, err := startRealProcess(ctx, path, []string{
		"videotestsrc", "num-buffers=10", "!", "fakesink",
	}, onRunning)
	if err != nil {
		t.Fatalf("startRealProcess: %v", err)
	}

	result := waitWithTimeout(t, proc, 5*time.Second)

	if !sawRunning {
		t.Fatalf("onRunningMarker was never called: real gst-launch-1.0 output was not recognized as reaching PLAYING")
	}
	if result.Signaled {
		t.Fatalf("process reported Signaled=true for a clean, self-terminating pipeline")
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("ExitCode = %v, want 0", result.ExitCode)
	}
}

// TestRealProcessKillMinusNineIsDetectedAndSupervisorRestarts is this
// seam's named Track B acceptance criterion: start a real gst-launch-1.0
// pipeline through the actual [Supervisor], send it SIGKILL exactly as an
// operator's `kill -9` would, and confirm the supervisor (a) detects the
// exit, (b) reports it as a distinct, non-healthy state rather than staying
// silent, and (c) restarts it — proven end to end against the real binary,
// not a fake standing in for process-death semantics.
func TestRealProcessKillMinusNineIsDetectedAndSupervisorRestarts(t *testing.T) {
	requireGstLaunch(t)

	policy := defaultRestartPolicy
	policy.initialBackoff = 50 * time.Millisecond
	policy.maxBackoff = 200 * time.Millisecond

	sup := newSupervisorWithPolicy(time.Now, nil, testLogger{}, policy)
	shutdownSupervisor(t, sup)

	// A long-running pipeline (no num-buffers bound) so it is still alive
	// when this test sends the kill.
	spec := Spec{
		SurfaceID: "kill-test",
		Stages: []Stage{
			{Label: "source", Elements: []Element{{Factory: "videotestsrc", Properties: []Property{{Key: "is-live", Value: "true"}}}}},
			{Label: "sink", Elements: []Element{{Factory: "fakesink"}}},
		},
	}
	dispatch := time.Now()
	if err := sup.Apply(spec); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, ok := sup.AwaitState(ctx, "kill-test", []State{StateRunning}, dispatch, 20*time.Millisecond)
	if !ok {
		t.Fatalf("real pipeline never reached StateRunning")
	}

	pid := findRealPipelinePid(t, sup, "kill-test")
	if pid == 0 {
		t.Fatalf("could not determine the running process's PID to kill")
	}

	killDispatch := time.Now()
	if err := killMinusNine(pid); err != nil {
		t.Fatalf("kill -9 %d: %v", pid, err)
	}

	// (a) and (b): the supervisor must observe the death and report a
	// distinct, non-running state — never silently stay "running".
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	crashed, ok := sup.AwaitState(ctx2, "kill-test", []State{StateRestarting, StateFailed}, killDispatch, 20*time.Millisecond)
	if !ok {
		t.Fatalf("supervisor did not report a non-running state after kill -9; last known snapshot before kill: %+v", snap)
	}
	if crashed.RestartCount < 1 {
		t.Fatalf("RestartCount = %d after kill -9, want >= 1", crashed.RestartCount)
	}

	// (c): the supervisor restarts it — a fresh StateRunning, with evidence
	// dated after the kill.
	ctx3, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()
	restarted, ok := sup.AwaitState(ctx3, "kill-test", []State{StateRunning}, killDispatch, 20*time.Millisecond)
	if !ok {
		t.Fatalf("supervisor did not restart the pipeline after kill -9")
	}
	if restarted.ObservedAt.Before(killDispatch) {
		t.Fatalf("post-restart ObservedAt %s is before the kill at %s", restarted.ObservedAt, killDispatch)
	}
}

// findRealPipelinePid polls sup for surfaceID's running pid, since the
// pid is only stamped after AwaitState already observed StateRunning — a
// tiny window that in practice never needs more than one extra poll.
func findRealPipelinePid(t *testing.T, sup *Supervisor, surfaceID string) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		if pid, ok := sup.Pid(surfaceID); ok {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0
}

// killMinusNine sends SIGKILL directly to pid — exactly what an operator's
// `kill -9 <pid>` does, unlike this package's own Kill() method, which is
// tested implicitly by every other test; this test proves detection of an
// EXTERNAL, unrequested kill, the scenario the seam names explicitly.
func killMinusNine(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// waitWithTimeout calls proc.Wait() on its own goroutine and fails the test
// if it does not return within timeout, rather than hanging the test suite
// if this package's exit detection is broken.
func waitWithTimeout(t *testing.T, proc ProcessHandle, timeout time.Duration) ExitResult {
	t.Helper()
	ch := make(chan ExitResult, 1)
	go func() { ch <- proc.Wait() }()
	select {
	case res := <-ch:
		return res
	case <-time.After(timeout):
		t.Fatalf("proc.Wait() did not return within %s", timeout)
		return ExitResult{}
	}
}
