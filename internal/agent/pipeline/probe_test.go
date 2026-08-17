package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// withResolvedGstLaunch stubs ResolveGstLaunch to succeed with a fake path,
// restoring the real lookup on cleanup — matches resolve_test.go's own
// lookPathFunc/lookupEnvFunc injection convention.
func withResolvedGstLaunch(t *testing.T) {
	t.Helper()
	prevLookPath := lookPathFunc
	prevLookupEnv := lookupEnvFunc
	lookPathFunc = func(string) (string, error) { return "/usr/bin/gst-launch-1.0", nil }
	lookupEnvFunc = func(string) (string, bool) { return "", false }
	t.Cleanup(func() {
		lookPathFunc = prevLookPath
		lookupEnvFunc = prevLookupEnv
	})
}

func TestProbeNDISendAvailableOnRealStateTransition(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: true, ExitCode: intPtr(0)})
	}}

	got := ProbeNDISend(context.Background(), fs.Start)
	if !got.Available {
		t.Fatalf("Available = false, want true (a real PLAYING transition was observed); reason = %q", got.Reason)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want empty when Available", got.Reason)
	}
	if fs.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1: the probe must actually invoke the starter, not merely check element existence", fs.callCount())
	}
	argv := fs.calls[0].argv
	if !containsArg(argv, "ndi-name="+probeSourceName) {
		t.Errorf("argv = %v, want ndi-name=%s naming the probe recognizably (never a real show surface name)", argv, probeSourceName)
	}
}

// TestProbeNDISendUnavailableReportsInstallPointer reproduces this seam's
// own captured evidence: gst-plugins-rs's ndisink dlopens the NDI runtime
// at state change, not at plugin load, so a process that starts, never
// reaches PLAYING, and exits with "Failed loading NDI SDK" on stderr must
// be reported unavailable with an actionable installation pointer — never
// as available merely because the element itself started successfully.
func TestProbeNDISendUnavailableReportsInstallPointer(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		code := 255
		p.exitNow(ExitResult{
			SawRunningMarker: false,
			ExitCode:         &code,
			StderrTail: "ERROR: from element /GstPipeline:pipeline0/GstNdiSink:ndisink0: Failed loading NDI SDK\n" +
				"ERROR: pipeline doesn't want to preroll.\nFailed to set pipeline to PAUSED.\n",
		})
	}}

	got := ProbeNDISend(context.Background(), fs.Start)
	if got.Available {
		t.Fatalf("Available = true, want false: the process never reached PLAYING")
	}
	if !strings.Contains(got.Reason, "NDI runtime not found") || !strings.Contains(got.Reason, "NDI_RUNTIME_DIR_V6") {
		t.Errorf("Reason = %q, want an actionable pointer naming NDI_RUNTIME_DIR_V6", got.Reason)
	}
}

// TestProbeNDISendUnavailableOtherFailureIsNotMisreportedAsMissingRuntime
// proves the reason text is derived from what stderr actually says, not
// hardcoded to the missing-runtime message regardless of cause — a
// different ndisink failure must not be misreported as "install the SDK."
func TestProbeNDISendUnavailableOtherFailureIsNotMisreportedAsMissingRuntime(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: false, StderrTail: "ERROR: some other ndisink failure entirely\n"})
	}}

	got := ProbeNDISend(context.Background(), fs.Start)
	if got.Available {
		t.Fatalf("Available = true, want false")
	}
	if strings.Contains(got.Reason, "NDI runtime not found") {
		t.Errorf("Reason = %q, must not claim a missing runtime when stderr never said so", got.Reason)
	}
	if !strings.Contains(got.Reason, "some other ndisink failure") {
		t.Errorf("Reason = %q, want the actual captured stderr line", got.Reason)
	}
}

func TestProbeNDISendGstLaunchNotFound(t *testing.T) {
	prevLookPath := lookPathFunc
	prevLookupEnv := lookupEnvFunc
	lookPathFunc = func(string) (string, error) { return "", errNotFoundForTest }
	lookupEnvFunc = func(string) (string, bool) { return "", false }
	t.Cleanup(func() {
		lookPathFunc = prevLookPath
		lookupEnvFunc = prevLookupEnv
	})

	got := ProbeNDISend(context.Background(), nil)
	if got.Available {
		t.Fatalf("Available = true, want false: no gst-launch-1.0 means nothing was probed")
	}
	if !strings.Contains(got.Reason, "gst-launch-1.0 not found") {
		t.Errorf("Reason = %q, want it to name the missing binary", got.Reason)
	}
}

// TestProbeCanceledContextKillsAndReportsUnavailable proves a probe never
// hangs a caller indefinitely: if ctx is done before the process reports
// anything, the probe kills it and returns promptly rather than blocking
// past its caller's own deadline.
func TestProbeCanceledContextKillsAndReportsUnavailable(t *testing.T) {
	withResolvedGstLaunch(t)
	killed := make(chan struct{})
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.killFunc = func() {
			close(killed)
			p.exitNow(ExitResult{Signaled: true})
		}
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan ProbeResult, 1)
	go func() { done <- ProbeNDISend(ctx, fs.Start) }()

	select {
	case got := <-done:
		if got.Available {
			t.Fatalf("Available = true, want false: ctx was already canceled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ProbeNDISend did not return promptly after ctx was already canceled")
	}
	select {
	case <-killed:
	default:
		t.Errorf("the probe's process was never killed after ctx canceled")
	}
}

// TestProbeTimeoutKillsAndReportsUnavailable exercises probeTimeout's own
// kill path (distinct from ctx cancellation) with the var shrunk so the
// test runs in milliseconds rather than the real 8s bound.
func TestProbeTimeoutKillsAndReportsUnavailable(t *testing.T) {
	withResolvedGstLaunch(t)
	prev := probeTimeout
	probeTimeout = 20 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prev })

	killed := make(chan struct{})
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.killFunc = func() {
			close(killed)
			p.exitNow(ExitResult{Signaled: true})
		}
	}}

	got := ProbeNDISend(context.Background(), fs.Start)
	if got.Available {
		t.Fatalf("Available = true, want false: the process never reported anything before probeTimeout")
	}
	if !strings.Contains(got.Reason, "timed out") {
		t.Errorf("Reason = %q, want it to name the timeout", got.Reason)
	}
	select {
	case <-killed:
	default:
		t.Errorf("the probe's process was never killed after probeTimeout elapsed")
	}
}

func TestProbeVideoFormatBuildsCapsFilterForRequestedFormat(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: true})
	}}

	got := ProbeVideoFormat(context.Background(), fs.Start, "UYVY")
	if !got.Available {
		t.Fatalf("Available = false, want true; reason = %q", got.Reason)
	}
	argv := fs.calls[0].argv
	if !containsArg(argv, "video/x-raw,format=UYVY,width=64,height=64") {
		t.Errorf("argv = %v, want a capsfilter caps string naming format=UYVY", argv)
	}
}

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func intPtr(v int) *int { return &v }
