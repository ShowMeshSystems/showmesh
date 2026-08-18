package pipeline

import (
	"context"
	"os/exec"
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

// TestRunProbeReturnsAvailableAsSoonAsMarkerFiresEvenWithNoSelfExit
// reproduces this seam's own captured defect: ProbeNDISend and
// ProbeVideoFormat's pipelines self-terminate (num-buffers) well inside
// probeTimeout, but a probe pipeline with nothing to make it exit on its
// own — a live source with no natural EOS, [ProbeFdsrcLive]'s exact shape —
// used to always run out the clock and report Available=false regardless
// of the marker, MEASURED against a real gst-launch-1.0 that had genuinely
// reached PLAYING. runProbe must return true, promptly, on the marker
// alone.
func TestRunProbeReturnsAvailableAsSoonAsMarkerFiresEvenWithNoSelfExit(t *testing.T) {
	withResolvedGstLaunch(t)
	prev := probeTimeout
	probeTimeout = 2 * time.Second
	t.Cleanup(func() { probeTimeout = prev })

	fs := &fakeStarter{onStart: func(_ *fakeProcess, onRunningMarker func()) {
		// Never exits on its own; only Kill() (from runProbe, once the
		// marker fires) ends it — see fakeProcess.Kill's default Signaled
		// exit.
		go onRunningMarker()
	}}

	start := time.Now()
	got := runProbe(context.Background(), fs.Start, []string{"fdsrc", "fd=0", "is-live=true", "!", "fakesink"}, genericUnavailableReason)
	elapsed := time.Since(start)

	if !got.Available {
		t.Fatalf("Available = false, want true: PLAYING was reached even though the process never exited on its own; reason = %q", got.Reason)
	}
	if elapsed >= probeTimeout {
		t.Fatalf("runProbe took %s, at or past probeTimeout (%s); it must return as soon as the marker fires, not wait out the clock", elapsed, probeTimeout)
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

// TestProbeFSEQSourceFormatBuildsRawvideoparsePropertyNickNotCaps is the
// regression test for the defect this seam fixed: ProbeVideoFormat's argv
// (a capsfilter caps string, upper-case correct) is not the argv
// FSEQSourceSpec actually emits for rawvideoparse's "format" property
// (lower-case nick required — see gstVideoFormat in spec.go). This asserts
// ProbeFSEQSourceFormat's argv is the real FSEQSourceSpec shape, not a
// stand-in: fdsrc/rawvideoparse present and format=rgb, never format=RGB.
func TestProbeFSEQSourceFormatBuildsRawvideoparsePropertyNickNotCaps(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: true, ExitCode: intPtr(0)})
	}}

	got := ProbeFSEQSourceFormat(context.Background(), fs.Start, "rgb", true)
	if !got.Available {
		t.Fatalf("Available = false, want true; reason = %q", got.Reason)
	}
	argv := fs.calls[0].argv
	if !containsArg(argv, "fdsrc") || !containsArg(argv, "rawvideoparse") {
		t.Fatalf("argv = %v, want the real FSEQSourceSpec shape (fdsrc ! rawvideoparse ...), not a standalone capsfilter probe", argv)
	}
	if !containsArg(argv, "format=rgb") {
		t.Errorf("argv = %v, want rawvideoparse's format PROPERTY as the lower-case nick \"rgb\", never the caps spelling \"RGB\"", argv)
	}
	if containsArg(argv, "format=RGB") {
		t.Errorf("argv = %v, contains format=RGB: this is exactly the bug this test guards against (rawvideoparse rejects it as a property on GStreamer < 1.26)", argv)
	}
}

// TestProbeFSEQSourceFormatFeedsStdinUntilMarker proves ProbeFSEQSourceFormat
// actually feeds frame bytes to the process's stdin while waiting for
// PLAYING — FSEQSourceSpec's fdsrc genuinely PREROLLs with nothing on
// stdin, so a probe of this exact pipeline that never writes anything would
// hang every time until probeTimeout, never proving the pipeline works.
func TestProbeFSEQSourceFormatFeedsStdinUntilMarker(t *testing.T) {
	withResolvedGstLaunch(t)
	prev := probeFrameInterval
	probeFrameInterval = time.Millisecond
	t.Cleanup(func() { probeFrameInterval = prev })

	var proc *fakeProcess
	fs := &fakeStarter{onStart: func(p *fakeProcess, onRunningMarker func()) {
		proc = p
		// Fire the marker only after this fake process has actually
		// received at least one fed frame, so a false pass (marker firing
		// with an empty feed loop) cannot happen.
		go func() {
			for {
				if len(p.stdinSnapshot()) > 0 {
					onRunningMarker()
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}}

	got := ProbeFSEQSourceFormat(context.Background(), fs.Start, "rgb", true)
	if !got.Available {
		t.Fatalf("Available = false, want true; reason = %q", got.Reason)
	}
	if len(proc.stdinSnapshot()) == 0 {
		t.Fatalf("no bytes were ever written to the probe process's stdin")
	}
}

// TestProbeFSEQSourceFormatUnrecognizedPixelFormat proves an unmapped pixel
// format is refused before any process is started, matching
// FSEQSourceSpec's own refusal — never a probe that silently guesses.
func TestProbeFSEQSourceFormatUnrecognizedPixelFormat(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{}

	got := ProbeFSEQSourceFormat(context.Background(), fs.Start, "rgbw", true)
	if got.Available {
		t.Fatalf("Available = true, want false: rgbw has no known GStreamer raw-video mapping")
	}
	if fs.callCount() != 0 {
		t.Errorf("callCount = %d, want 0: an unrecognized pixel format must never start a process", fs.callCount())
	}
}

// TestProbeFSEQSourceFormatRealMachine runs ProbeFSEQSourceFormat against
// the REAL gst-launch-1.0 on whatever machine this test executes on,
// matching TestProbeFdsrcLiveRealMachine's shape. "rgb" is expected
// Available on any GStreamer install this project targets — this is the
// same argv render.surface.apply itself builds.
func TestProbeFSEQSourceFormatRealMachine(t *testing.T) {
	requireGstLaunch(t)

	got := ProbeFSEQSourceFormat(context.Background(), nil, "rgb", FdsrcSupportsIsLive(nil))
	t.Logf("ProbeFSEQSourceFormat(rgb) on this machine: Available=%v Reason=%q", got.Available, got.Reason)

	if !got.Available {
		t.Errorf("Available = false, want true for \"rgb\" on this project's target GStreamer versions; reason = %q", got.Reason)
	}
}

// capturingHandle wraps a real [ProcessHandle] and records its ExitResult
// into out, so a test can inspect the raw captured stderr a real
// gst-launch-1.0 process actually produced — the same result ProbeNDISend
// itself received and turned into a Reason string — rather than only ever
// seeing a fake's scripted stderr.
type capturingHandle struct {
	ProcessHandle
	out *ExitResult
}

func (c *capturingHandle) Wait() ExitResult {
	r := c.ProcessHandle.Wait()
	*c.out = r
	return r
}

// capturingRealStarter is a [ProcessStarter] that runs the REAL
// [startRealProcess] (a genuine gst-launch-1.0 child process, not a fake)
// and captures its ExitResult into out via [capturingHandle].
func capturingRealStarter(out *ExitResult) ProcessStarter {
	return func(ctx context.Context, path string, args []string, onRunningMarker func()) (ProcessHandle, error) {
		h, err := startRealProcess(ctx, path, args, onRunningMarker)
		if err != nil {
			return nil, err
		}
		return &capturingHandle{ProcessHandle: h, out: out}, nil
	}
}

// TestProbeNDISendRealMachine runs ProbeNDISend against the REAL
// gst-launch-1.0 on whatever machine this test executes on — no fake
// starter, no stubbed ResolveGstLaunch — closing the exact gap the build
// contract's B4 section calls out: every other test in this file drives
// the probe through a simulation of a real process, so none of them prove
// the probe reports a REAL machine's REAL state correctly.
//
// It deliberately does not assert a fixed Available value, because that
// depends on whether the node running this test has the NDI runtime
// installed. What it does assert is the invariant this whole seam exists
// to guarantee (Available implies no reason needed; !Available implies a
// real, non-empty, actionable reason — never "unavailable" with nothing
// else said), and it logs the actual outcome so a human reading test
// output learns what this specific machine reported.
//
// On the machine this test was written and verified on, gst-launch-1.0 is
// installed with the ndisink element present but the NDI runtime library
// itself absent — see this seam's own report — so the second half of this
// test (gated on `gst-inspect-1.0 ndisink` actually finding the element)
// additionally proves ndiUnavailableReasonMarker matched genuinely captured
// stderr from a real process, not merely the string probe_test.go's own
// fakeStarter tests were told to emit.
func TestProbeNDISendRealMachine(t *testing.T) {
	requireGstLaunch(t)

	var captured ExitResult
	got := ProbeNDISend(context.Background(), capturingRealStarter(&captured))
	t.Logf("ProbeNDISend on this machine: Available=%v Reason=%q rawStderr=%q",
		got.Available, got.Reason, captured.StderrTail)

	if got.Available && got.Reason != "" {
		t.Errorf("Available=true but Reason=%q, want empty", got.Reason)
	}
	if !got.Available && got.Reason == "" {
		t.Errorf("Available=false but Reason is empty, want a non-empty, actionable reason")
	}

	if got.Available {
		t.Logf("this machine has a usable NDI runtime; nothing further to check")
		return
	}

	if _, err := exec.LookPath("gst-inspect-1.0"); err != nil {
		t.Logf("gst-inspect-1.0 not found; skipping the marker-specific check (cannot tell element-absent from runtime-absent here)")
		return
	}
	if err := exec.Command("gst-inspect-1.0", "ndisink").Run(); err != nil {
		t.Logf("gst-inspect-1.0 ndisink reports the element absent on this machine (%v); a missing-element failure is a different case than the runtime-dlopen trap this seam targets, so the marker check does not apply here", err)
		return
	}

	// ndisink IS present (proven above, independently of ProbeNDISend), so
	// a failed transition here is specifically the SDK-dlopen trap this
	// whole seam exists to catch — real stderr must actually contain the
	// marker, and Reason must be the actionable install pointer, not the
	// generic fallback.
	if !strings.Contains(captured.StderrTail, ndiUnavailableReasonMarker) {
		t.Errorf("ndisink is present but this real run's stderr does not contain %q: got %q — the marker this package matches on was never actually exercised against real gst-launch-1.0 output",
			ndiUnavailableReasonMarker, captured.StderrTail)
	}
	if !strings.Contains(got.Reason, "install the NDI SDK runtime") {
		t.Errorf("Reason = %q, want the actionable NDI install pointer (ndiUnavailableReason's non-generic branch)", got.Reason)
	}
}

// TestProbeFdsrcLiveBuildsExpectedArgv proves the probe pipeline shape:
// fdsrc fd=0 is-live=true, exactly what FSEQSourceSpec would build with
// fdsrcIsLive=true, so a positive result is real evidence about that same
// argv shape.
func TestProbeFdsrcLiveBuildsExpectedArgv(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: true, ExitCode: intPtr(0)})
	}}

	got := ProbeFdsrcLive(context.Background(), fs.Start)
	if !got.Available {
		t.Fatalf("Available = false, want true; reason = %q", got.Reason)
	}
	argv := fs.calls[0].argv
	if !containsArg(argv, "fdsrc") || !containsArg(argv, "is-live=true") {
		t.Errorf("argv = %v, want fdsrc with is-live=true", argv)
	}
}

// TestProbeFdsrcLiveUnavailableOnRejectedPipeline reproduces this seam's
// own captured evidence: GStreamer 1.24.2 rejects the whole pipeline at
// construction (never reaching PLAYING) when fdsrc's is-live property does
// not exist, exiting with "no property \"is-live\" in element \"fdsrc\""
// on stderr and no SawRunningMarker.
func TestProbeFdsrcLiveUnavailableOnRejectedPipeline(t *testing.T) {
	withResolvedGstLaunch(t)
	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		code := 1
		p.exitNow(ExitResult{
			SawRunningMarker: false,
			ExitCode:         &code,
			StderrTail:       "WARNING: erroneous pipeline: no property \"is-live\" in element \"fdsrc\"\n",
		})
	}}

	got := ProbeFdsrcLive(context.Background(), fs.Start)
	if got.Available {
		t.Fatalf("Available = true, want false: the pipeline was rejected, never reached PLAYING")
	}
	if !strings.Contains(got.Reason, "is-live") {
		t.Errorf("Reason = %q, want the captured rejection text", got.Reason)
	}
}

// TestFdsrcSupportsIsLiveProbesOnceAndCaches proves the cache: a second
// call must not invoke the starter again, and must keep returning the
// first call's answer even if a later call would pass a starter that would
// answer differently.
func TestFdsrcSupportsIsLiveProbesOnceAndCaches(t *testing.T) {
	withResolvedGstLaunch(t)
	resetFdsrcLiveProbeCache()
	t.Cleanup(resetFdsrcLiveProbeCache)

	fs := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: true, ExitCode: intPtr(0)})
	}}

	if got := FdsrcSupportsIsLive(fs.Start); !got {
		t.Fatalf("FdsrcSupportsIsLive = false, want true on first call")
	}
	if fs.callCount() != 1 {
		t.Fatalf("callCount = %d, want 1", fs.callCount())
	}

	fsUnavailable := &fakeStarter{onStart: func(p *fakeProcess, _ func()) {
		p.exitNow(ExitResult{SawRunningMarker: false, ExitCode: intPtr(1)})
	}}
	if got := FdsrcSupportsIsLive(fsUnavailable.Start); !got {
		t.Fatalf("FdsrcSupportsIsLive on a second call = %v, want the CACHED true answer, never re-probed", got)
	}
	if fsUnavailable.callCount() != 0 {
		t.Fatalf("a second FdsrcSupportsIsLive call invoked the starter (callCount=%d); the probe must run at most once per process",
			fsUnavailable.callCount())
	}
}

// TestProbeFdsrcLiveRealMachine runs ProbeFdsrcLive against the REAL
// gst-launch-1.0 on whatever machine this test executes on, matching
// TestProbeNDISendRealMachine's own real-process shape. It asserts only
// this package's cross-version invariant (Available implies no reason
// needed; !Available implies a real, non-empty reason) and logs the actual
// outcome, since the right answer depends on which GStreamer this machine
// has installed.
func TestProbeFdsrcLiveRealMachine(t *testing.T) {
	requireGstLaunch(t)

	got := ProbeFdsrcLive(context.Background(), nil)
	t.Logf("ProbeFdsrcLive on this machine (gst-launch-1.0): Available=%v Reason=%q", got.Available, got.Reason)

	if got.Available && got.Reason != "" {
		t.Errorf("Available=true but Reason=%q, want empty", got.Reason)
	}
	if !got.Available && got.Reason == "" {
		t.Errorf("Available=false but Reason is empty, want a non-empty reason")
	}
}

// Compile-time check that *capturingHandle still satisfies ProcessHandle
// (embedding hides a signature change that would otherwise only surface as
// a runtime panic on Kill/Pid/Stdin).
var _ ProcessHandle = (*capturingHandle)(nil)

func containsArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func intPtr(v int) *int { return &v }
