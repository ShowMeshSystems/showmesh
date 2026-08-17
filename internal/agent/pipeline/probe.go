package pipeline

import (
	"context"
	"strings"
	"time"
)

// probeSourceName is the throwaway NDI source name every transport probe
// in this file uses. Named recognizably as scaffolding (CLAUDE.md's
// live-network rule): it must never be mistaken for a real show surface if
// it is ever glimpsed by an NDI discovery tool on the local network.
const probeSourceName = "showmesh-probe"

// probeTimeout bounds one probe's wait for gst-launch-1.0 to either reach
// PLAYING or exit. Measured on this seam's own reproduction, the failure
// path (missing NDI runtime) returns in well under a second; this is
// margin for a slow disk or a genuinely first-time dlopen, not a tuned
// figure. A probe that does not finish within this bound is killed and
// reported unavailable rather than left to hang a caller (advertise.go's
// publishHello and renderops.go's probeTransport both run this inline,
// with their own outer deadlines from the same context). A var, not a
// const, only so probe_test.go can shrink it to exercise the timeout path
// in milliseconds; no runtime configuration ever reassigns it.
var probeTimeout = 8 * time.Second

// ProbeResult is one real state-transition probe's outcome. Reason is
// empty exactly when Available is true.
type ProbeResult struct {
	Available bool
	Reason    string
}

// ProbeNDISend attempts a real NULL -> PLAYING transition of a throwaway
// gst-launch-1.0 pipeline ending in ndisink, using starter (nil selects
// the real process starter, [startRealProcess]).
//
// Element presence is not runtime presence: gst-plugins-rs's ndisink
// dlopens the actual NDI runtime at state change, not at plugin load, so
// `gst-inspect-1.0 ndisink` succeeding proves only that the plugin is
// installed, never that a frame can be sent. Reproduced on the seam's own
// development machine: `gst-inspect-1.0 ndisink` exits 0, while running
// this exact probe pipeline exits 255 with stderr "Failed loading NDI
// SDK" from net/ndi/src/ndisink/imp.rs:182, and gst-launch-1.0's stdout
// never contains "Setting pipeline to PLAYING". This function is the only
// thing in this codebase allowed to answer "is transport.ndi.send usable"
// and it always attempts the transition; it must never be replaced by an
// element-existence check.
func ProbeNDISend(ctx context.Context, starter ProcessStarter) ProbeResult {
	argv := []string{
		"videotestsrc", "num-buffers=5", "is-live=true", "!",
		"video/x-raw,format=UYVY,width=64,height=64,framerate=10/1", "!",
		"ndisink", "ndi-name=" + probeSourceName, "sync=false",
	}
	return runProbe(ctx, starter, argv, ndiUnavailableReason)
}

// ProbeVideoFormat attempts a real NULL -> PLAYING transition of a
// throwaway videotestsrc -> capsfilter(format) -> fakesink pipeline, real
// evidence for whether this node's GStreamer install actually negotiates
// format rather than an assumption that every base-plugins install
// supports every format this project might want.
func ProbeVideoFormat(ctx context.Context, starter ProcessStarter, format string) ProbeResult {
	argv := []string{
		"videotestsrc", "num-buffers=3", "!",
		"video/x-raw,format=" + format + ",width=64,height=64", "!",
		"fakesink", "sync=false",
	}
	return runProbe(ctx, starter, argv, genericUnavailableReason)
}

// runProbe is the shared mechanism behind every probe in this file: resolve
// gst-launch-1.0, run argv to completion (or kill it at probeTimeout), and
// report Available strictly from [ExitResult.SawRunningMarker] — never from
// the element existing, never from the process merely starting.
func runProbe(ctx context.Context, starter ProcessStarter, argv []string, reasonFor func(stderrTail string) string) ProbeResult {
	if starter == nil {
		starter = startRealProcess
	}

	path, ok, reason := ResolveGstLaunch()
	if !ok {
		return ProbeResult{Available: false, Reason: reason}
	}

	proc, err := starter(ctx, path, argv, nil)
	if err != nil {
		return ProbeResult{Available: false, Reason: "starting gst-launch-1.0 for the probe: " + err.Error()}
	}

	resultCh := make(chan ExitResult, 1)
	go func() { resultCh <- proc.Wait() }()

	timer := time.NewTimer(probeTimeout)
	defer timer.Stop()

	select {
	case res := <-resultCh:
		if res.SawRunningMarker {
			return ProbeResult{Available: true}
		}
		return ProbeResult{Available: false, Reason: reasonFor(res.StderrTail)}
	case <-timer.C:
		_ = proc.Kill()
		<-resultCh
		return ProbeResult{Available: false, Reason: "probe timed out waiting for a state transition"}
	case <-ctx.Done():
		_ = proc.Kill()
		<-resultCh
		return ProbeResult{Available: false, Reason: "probe canceled: " + ctx.Err().Error()}
	}
}

// ndiUnavailableReasonMarker is the exact substring gst-plugins-rs's
// ndisink writes to stderr when it cannot dlopen the NDI runtime
// (net/ndi/src/ndisink/imp.rs:182, reproduced on this seam's own
// development machine). Matched, never parsed further: a future
// gst-plugins-rs release changing this string degrades the reason text to
// the generic fallback below, not to a crash.
const ndiUnavailableReasonMarker = "Failed loading NDI SDK"

// ndiUnavailableReason turns a captured stderr tail into an actionable
// installation pointer. The gst-plugins-rs ndisink element itself reads
// NDI_RUNTIME_DIR_V6 (or NDI_RUNTIME_DIR_V5 for older SDKs, both present as
// literal strings in this seam's own installed libgstndi build) to locate
// the runtime outside its default search path — that is the NDI SDK's own
// dlopen mechanism, not a ShowMesh-invented variable, and ShowMesh never
// vendors, links, or overrides it (ADR-010, ADR-026 decision 6).
func ndiUnavailableReason(stderrTail string) string {
	if strings.Contains(stderrTail, ndiUnavailableReasonMarker) {
		return "NDI runtime not found: install the NDI SDK runtime library, then either leave it at its default install location or point NDI_RUNTIME_DIR_V6 (NDI_RUNTIME_DIR_V5 for older SDKs) at its lib directory"
	}
	return genericUnavailableReason(stderrTail)
}

func genericUnavailableReason(stderrTail string) string {
	if stderrTail == "" {
		return "gst-launch-1.0 did not reach PLAYING and reported no stderr"
	}
	return "gst-launch-1.0 did not reach PLAYING: " + strings.TrimSpace(lastStderrLine(stderrTail))
}

// lastStderrLine returns s's last non-empty line, for a one-line reason
// string; [ExitResult.StderrTail] (and Snapshot.LastStderr) still carry the
// full captured tail for anyone who needs it.
func lastStderrLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if idx := strings.LastIndexByte(s, '\n'); idx >= 0 {
		return s[idx+1:]
	}
	return s
}
