package audio

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

// TestProbeOutputAgainstRealNullDevice drives a real gst-launch-1.0
// subprocess (no fake runner, no fake gst-launch resolution) against
// ALSA's own "null" PCM device — the C0a-1 bench's own substrate
// (bench/audio-node/results/r6_null_sink.json: pipeline_reached_playing_
// and_eos=true, exit code 0), exercising this package's own parsing
// against real GStreamer output rather than only canned strings.
//
// This skips only when the environment genuinely cannot run it: no
// gst-launch-1.0 on PATH, or the pipeline never reached PLAYING at all
// (no alsasink/ALSA null device — every non-Linux host, and any Linux
// host without gstreamer1.0-alsa installed). A pipeline that DID reach
// PLAYING but still reports Available=false means this package's own caps
// parsing failed against real output — a genuine bug, not an environment
// gap — and must fail loudly rather than skip.
func TestProbeOutputAgainstRealNullDevice(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	got := ProbeOutput(context.Background(), "null", 0, 0)
	if !got.Available {
		if strings.Contains(got.Reason, "reached PLAYING") {
			t.Fatalf("real probe against the ALSA null device reached PLAYING but this package failed to parse its own evidence: %s", got.Reason)
		}
		t.Skipf("skipping: real probe against the ALSA null device unavailable in this environment (%s) — this test only exercises real evidence where alsasink and the null device exist (the C0a-1 bench's Debian 13 image, or a Linux host with gstreamer1.0-alsa installed)", got.Reason)
	}

	if got.Channels < 1 {
		t.Errorf("real probe: Channels = %d, want at least 1", got.Channels)
	}
	if got.Rate < 1 {
		t.Errorf("real probe: Rate = %d, want a positive negotiated rate", got.Rate)
	}
	if got.Format == "" {
		t.Error("real probe: Format is empty, want a negotiated format string")
	}
}
