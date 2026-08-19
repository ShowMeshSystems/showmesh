package audio

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// probeNumBuffers bounds the throwaway audiotestsrc source so the probe
// pipeline self-terminates via EOS rather than running until killed —
// MEASURED, bench/audio-node/results/r6_null_sink.log: 50 buffers reached
// PLAYING and EOS in 1.6 ms against the ALSA null device.
const probeNumBuffers = 50

// probeWave and probeVolume make every probe silent on a real output:
// wave=silence is audiotestsrc's own documented zero-signal content, and
// volume=0 is a second, independent guard, because this pipeline reaches
// PLAYING against whatever device a caller names, including a
// commissioned node's live program or LTC output.
const (
	probeWave   = "silence"
	probeVolume = "0"
)

// probeTimeout bounds one probe's wait for gst-launch-1.0 to exit. A var,
// not a const, so probe_test.go can shrink it to exercise the timeout path
// in milliseconds; no runtime configuration ever reassigns it.
var probeTimeout = 8 * time.Second

// ProbeResult is one real state-transition probe's outcome. Available is
// only ever true when the pipeline actually reached PLAYING; Channels,
// Rate, and Format are what alsasink's own sink-pad caps negotiated —
// never what was requested.
type ProbeResult struct {
	Available bool
	Reason    string
	Channels  int
	Rate      int
	Format    string

	// Busy is true when Available is false because ALSA reported the
	// device already held open by something else, never because the
	// device does not exist or does not work — a caller must not treat
	// this the same as a confirmed-absent route, and must never let it
	// overwrite an earlier good result for the same device.
	Busy bool
}

// alsaBusyMarkers are ALSA/GStreamer's own wording for "this device is
// already open elsewhere" — MEASURED against alsasink's actual error text
// (snd_pcm_open returning -EBUSY surfaces as "Device or resource busy").
var alsaBusyMarkers = []string{"device or resource busy", "resource busy"}

func isBusyOutput(out string) bool {
	lower := strings.ToLower(out)
	for _, m := range alsaBusyMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// probeRunner runs gst-launch-1.0's argv and returns its combined
// stdout+stderr and whether it exited 0, substituted in tests so caps
// parsing and PLAYING detection can be proven from canned output without
// shelling out for real.
type probeRunner func(ctx context.Context, path string, argv []string) (output string, exitedZero bool)

var runProbeProcess probeRunner = runRealProbeProcess

// resolveGstLaunch wraps [pipeline.ResolveGstLaunch], a package-level var
// (matching runProbeProcess's own injection convention) so probe_test.go
// can exercise ProbeOutput without depending on whether gst-launch-1.0 is
// actually installed on the test host.
var resolveGstLaunch = pipeline.ResolveGstLaunch

func runRealProbeProcess(ctx context.Context, path string, argv []string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, argv...).CombinedOutput()
	return string(out), err == nil
}

// playingMarker is gst-launch-1.0's own default-output evidence that the
// pipeline actually reached PLAYING (matches
// internal/agent/pipeline.runningStateMarker; kept as this package's own
// unexported copy rather than an import, since that package is shaped for
// a long-lived supervised pipeline, not a self-terminating probe run).
const playingMarker = "Setting pipeline to PLAYING"

// alsaSinkCapsPattern extracts the achieved rate, format, and channel count
// from gst-launch-1.0 -v's sink-pad caps notification for alsasink —
// MEASURED, r6_null_sink.log:
//
//	.../GstAlsaSink:alsasink0.GstPad:sink: caps = audio/x-raw, rate=(int)44100, format=(string)F64LE, channels=(int)2, ...
var alsaSinkCapsPattern = regexp.MustCompile(`AlsaSink:\S+\.GstPad:sink: caps = audio/x-raw, rate=\(int\)(\d+), format=\(string\)(\S+?), channels=\(int\)(\d+)`)

// ProbeOutput attempts a real NULL -> PLAYING transition of a throwaway
// audiotestsrc -> capsfilter(channels,rate) -> audioconvert -> audioresample
// -> alsasink(device) pipeline (r6_null_sink.sh's own shape) and reports
// what alsasink's own sink pad negotiated — never the requested
// channels/rate echoed back. requestedChannels/requestedRate of 0 lets
// GStreamer negotiate its own default, matching r6/r7's unconstrained runs.
func ProbeOutput(ctx context.Context, device string, requestedChannels, requestedRate int) ProbeResult {
	path, ok, reason := resolveGstLaunch()
	if !ok {
		return ProbeResult{Available: false, Reason: reason}
	}

	argv := []string{"-v", "audiotestsrc", fmt.Sprintf("num-buffers=%d", probeNumBuffers),
		"wave=" + probeWave, "volume=" + probeVolume}
	if requestedChannels > 0 || requestedRate > 0 {
		caps := "audio/x-raw"
		if requestedRate > 0 {
			caps += fmt.Sprintf(",rate=%d", requestedRate)
		}
		if requestedChannels > 0 {
			caps += fmt.Sprintf(",channels=%d", requestedChannels)
		}
		argv = append(argv, "!", caps)
	}
	argv = append(argv, "!", "audioconvert", "!", "audioresample", "!",
		"alsasink", "device="+device, "sync=false")

	out, exitedZero := runProbeProcess(ctx, path, argv)
	if !exitedZero || !strings.Contains(out, playingMarker) {
		return ProbeResult{Available: false, Reason: genericProbeReason(out, exitedZero), Busy: isBusyOutput(out)}
	}

	m := alsaSinkCapsPattern.FindStringSubmatch(out)
	if m == nil {
		return ProbeResult{Available: false, Reason: "pipeline reached PLAYING but reported no alsasink caps negotiation"}
	}
	rate, _ := strconv.Atoi(m[1])
	channels, _ := strconv.Atoi(m[3])

	return ProbeResult{Available: true, Rate: rate, Format: m[2], Channels: channels}
}

func genericProbeReason(out string, exitedZero bool) string {
	if !exitedZero {
		return "gst-launch-1.0 did not exit cleanly: " + lastNonEmptyLine(out)
	}
	return "gst-launch-1.0 exited cleanly but never reported reaching PLAYING"
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}
