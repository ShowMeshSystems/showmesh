package pipeline

import (
	"context"
	"strings"
	"sync"
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

// probeFSEQGeometry is the throwaway width/height/frameRate
// [ProbeFSEQSourceFormat] builds its [FSEQSourceSpec] with. Small enough
// that one dummy frame is trivial to build and feed repeatedly, and
// otherwise arbitrary: FSEQSourceSpec performs no scaling, so nothing about
// whether the pipeline reaches PLAYING depends on the actual value.
const (
	probeFSEQWidth     = 4
	probeFSEQHeight    = 4
	probeFSEQFrameRate = 25
)

// probeFrameInterval is how often [ProbeFSEQSourceFormat] writes another
// throwaway frame to the probe pipeline's stdin. fdsrc genuinely PREROLLs
// (see [FSEQSourceSpec]'s fdsrc doc comment), so a probe of this exact
// pipeline with nothing arriving on stdin would run out probeTimeout's
// clock every time rather than proving anything, unlike ProbeVideoFormat's
// self-terminating num-buffers pipeline. A var, not a const, for the same
// probeTimeout reason: only probe_test.go shrinks it, to exercise the feed
// loop in milliseconds.
var probeFrameInterval = 20 * time.Millisecond

// ProbeFSEQSourceFormat attempts a real NULL -> PLAYING transition of the
// EXACT pipeline [FSEQSourceSpec] builds for pixelFormat — fdsrc and
// rawvideoparse included, not a standalone capsfilter probe.
//
// This exists because [ProbeVideoFormat]'s caps-string probe and
// FSEQSourceSpec's rawvideoparse "format" *property* need different
// GStreamer spellings on GStreamer < 1.26 (see gstVideoFormat's doc
// comment in spec.go): a caps probe succeeding was never evidence the real
// render pipeline could be built. That gap let a node advertise
// render.surface support for a pipeline that then failed to construct on
// every subsequent apply — exactly the "compiles, passes tests, cannot be
// reached" failure shape this project has found before, just this time in
// the probe meant to catch it rather than in the thing being probed.
func ProbeFSEQSourceFormat(ctx context.Context, starter ProcessStarter, pixelFormat string, fdsrcIsLive bool) ProbeResult {
	spec, err := FSEQSourceSpec("probe", probeFSEQWidth, probeFSEQHeight, pixelFormat, probeFSEQFrameRate, fdsrcIsLive)
	if err != nil {
		return ProbeResult{Available: false, Reason: err.Error()}
	}
	argv, err := spec.BuildArgv()
	if err != nil {
		return ProbeResult{Available: false, Reason: err.Error()}
	}

	m, ok := gstVideoFormatsByPixelFormat[pixelFormat]
	if !ok {
		return ProbeResult{Available: false, Reason: "pipeline: no byte-size mapping for pixel format " + pixelFormat}
	}
	frame := make([]byte, probeFSEQWidth*probeFSEQHeight*m.bytesPerPixel)

	return runProbeWithFeed(ctx, starter, argv, frame, genericUnavailableReason)
}

// ProbeFdsrcLive attempts a real NULL -> PLAYING transition of a throwaway
// fdsrc(fd=0, is-live=true) -> fakesink pipeline, fd 0 left open and
// unfed. fdsrc's is-live property was added upstream in GStreamer 1.26;
// Available=false covers both an outright rejected pipeline (the property
// does not exist, MEASURED: gst-launch-1.0 1.24.2 on Ubuntu 24.04 exits 1
// with "no property \"is-live\" in element \"fdsrc\"" before any state
// change is attempted) and any other probe failure, and both are treated
// identically by [FSEQSourceSpec]'s caller: never emit the property.
func ProbeFdsrcLive(ctx context.Context, starter ProcessStarter) ProbeResult {
	argv := []string{
		"fdsrc", "fd=0", "is-live=true", "!",
		"fakesink", "sync=false",
	}
	return runProbe(ctx, starter, argv, genericUnavailableReason)
}

var (
	fdsrcLiveOnce      sync.Once
	fdsrcLiveSupported bool
)

// FdsrcSupportsIsLive reports whether this node's installed GStreamer
// accepts fdsrc's is-live property, probed at most once per process (see
// [ProbeFdsrcLive]) since the installed version cannot change while this
// process runs. A probe failure (missing gst-launch-1.0, a probe timeout,
// or the property genuinely not existing) caches false, the option that
// can never get a pipeline rejected outright — [FSEQSourceSpec] still
// reaches PLAYING with the property omitted, as long as its stdin is fed,
// which every caller in this codebase guarantees (a fresh render.surface.
// apply starts its [FrameWriter] immediately after [Supervisor.Apply]).
func FdsrcSupportsIsLive(starter ProcessStarter) bool {
	fdsrcLiveOnce.Do(func() {
		fdsrcLiveSupported = ProbeFdsrcLive(context.Background(), starter).Available
	})
	return fdsrcLiveSupported
}

// resetFdsrcLiveProbeCache clears [FdsrcSupportsIsLive]'s cache. Test-only:
// production never needs to re-probe within one process's life.
func resetFdsrcLiveProbeCache() {
	fdsrcLiveOnce = sync.Once{}
	fdsrcLiveSupported = false
}

// runProbe is the shared mechanism behind every probe in this file: resolve
// gst-launch-1.0, run argv, and report Available strictly from real
// evidence that PLAYING was reached — never from the element existing,
// never from the process merely starting.
//
// It kills and returns as soon as that evidence exists (the onRunning
// callback fires), rather than always waiting for the process to exit on
// its own. ProbeNDISend and ProbeVideoFormat self-terminate quickly
// (num-buffers), so this only shortens their wait; a probe pipeline with
// nothing to make it self-terminate (a live source with no natural EOS)
// would otherwise always run out the clock to probeTimeout and be
// misreported unavailable despite having reached PLAYING — MEASURED: this
// is exactly what a first version of [ProbeFdsrcLive] did on a real
// gst-launch-1.0 that had genuinely gone PLAYING.
func runProbe(ctx context.Context, starter ProcessStarter, argv []string, reasonFor func(stderrTail string) string) ProbeResult {
	return runProbeWithFeed(ctx, starter, argv, nil, reasonFor)
}

// runProbeWithFeed is [runProbe] plus an optional stdin feed: when frame is
// non-nil, it is written to the started process's stdin on
// [probeFrameInterval] until the process reaches PLAYING, exits, or the
// probe times out. [ProbeFSEQSourceFormat] is the only caller that needs
// this — its pipeline's fdsrc genuinely PREROLLs waiting for a first
// buffer, unlike every other probe in this file, which either has no
// stdin-fed source or self-terminates via num-buffers.
func runProbeWithFeed(ctx context.Context, starter ProcessStarter, argv []string, frame []byte, reasonFor func(stderrTail string) string) ProbeResult {
	if starter == nil {
		starter = startRealProcess
	}

	path, ok, reason := ResolveGstLaunch()
	if !ok {
		return ProbeResult{Available: false, Reason: reason}
	}

	sawMarker := make(chan struct{})
	var once sync.Once
	onRunning := func() { once.Do(func() { close(sawMarker) }) }

	proc, err := starter(ctx, path, argv, onRunning)
	if err != nil {
		return ProbeResult{Available: false, Reason: "starting gst-launch-1.0 for the probe: " + err.Error()}
	}

	if frame != nil {
		stopFeed := make(chan struct{})
		var feedDone sync.WaitGroup
		feedDone.Add(1)
		go func() {
			defer feedDone.Done()
			feedProbeFrames(proc, frame, sawMarker, stopFeed)
		}()
		// Waited on, not just signaled: probeFrameInterval is a
		// package-level var mutated by probe_test.go between test cases,
		// and returning while this goroutine might still be about to read
		// it for the first time (time.NewTicker inside feedProbeFrames) is
		// a data race a fire-and-forget close(stopFeed) does not close.
		defer func() {
			close(stopFeed)
			feedDone.Wait()
		}()
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
	case <-sawMarker:
		_ = proc.Kill()
		<-resultCh
		return ProbeResult{Available: true}
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

// feedProbeFrames writes frame to proc's stdin every [probeFrameInterval]
// until stop or sawMarker closes, or the write itself fails (the process
// exited or closed its stdin). Errors are swallowed deliberately: the
// caller's own select on resultCh/sawMarker/timer is the sole source of
// truth for the probe's outcome, and a write failure here is not new
// evidence — it is downstream of something that select will already see.
func feedProbeFrames(proc ProcessHandle, frame []byte, sawMarker, stop <-chan struct{}) {
	w, err := proc.Stdin()
	if err != nil {
		return
	}
	ticker := time.NewTicker(probeFrameInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-sawMarker:
			return
		case <-ticker.C:
			if _, err := w.Write(frame); err != nil {
				return
			}
		}
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
