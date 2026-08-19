package audio

import (
	"context"
	"strings"
	"testing"
)

// fakeGstLaunch makes resolveGstLaunch succeed for a test without depending
// on whether gst-launch-1.0 is actually installed on the test host.
func fakeGstLaunch(t *testing.T) {
	t.Helper()
	orig := resolveGstLaunch
	resolveGstLaunch = func() (string, bool, string) { return "/usr/bin/gst-launch-1.0", true, "" }
	t.Cleanup(func() { resolveGstLaunch = orig })
}

func withProbeRunner(t *testing.T, fn probeRunner) {
	t.Helper()
	orig := runProbeProcess
	runProbeProcess = fn
	t.Cleanup(func() { runProbeProcess = orig })
}

func playingOutput(rate, channels int, format string) string {
	return "Setting pipeline to PLAYING ...\n" +
		"/GstPipeline:pipeline0/GstAlsaSink:alsasink0.GstPad:sink: caps = audio/x-raw, rate=(int)" +
		itoa(rate) + ", format=(string)" + format + ", channels=(int)" + itoa(channels) +
		", layout=(string)interleaved\nGot EOS from element \"pipeline0\".\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// TestProbeOutputReportsAchievedNeverRequested proves the sharpest form:
// a caller requesting 6 channels at 96000 Hz gets back exactly what the
// pipeline negotiated (2 channels at 44100 Hz), never the request echoed
// back.
func TestProbeOutputReportsAchievedNeverRequested(t *testing.T) {
	fakeGstLaunch(t)
	withProbeRunner(t, func(ctx context.Context, path string, argv []string) (string, bool) {
		return playingOutput(44100, 2, "F64LE"), true
	})

	got := ProbeOutput(context.Background(), "hw:CARD=PCH,DEV=0", 6, 96000)

	if !got.Available {
		t.Fatalf("ProbeOutput.Available = false, want true; reason=%q", got.Reason)
	}
	if got.Channels != 2 || got.Rate != 44100 || got.Format != "F64LE" {
		t.Errorf("ProbeOutput = %+v, want achieved {Channels:2 Rate:44100 Format:F64LE}, not the requested 6/96000", got)
	}
}

// TestProbeOutputNeverEmitsAnAudibleSignal proves the pipeline argv this
// package shells out with is silent by construction: wave=silence and
// volume=0, both present, regardless of the requested channels/rate.
func TestProbeOutputNeverEmitsAnAudibleSignal(t *testing.T) {
	fakeGstLaunch(t)
	var gotArgv []string
	withProbeRunner(t, func(ctx context.Context, path string, argv []string) (string, bool) {
		gotArgv = argv
		return playingOutput(44100, 2, "F64LE"), true
	})

	ProbeOutput(context.Background(), "hw:CARD=PCH,DEV=0", 6, 96000)

	joined := strings.Join(gotArgv, " ")
	if !strings.Contains(joined, "wave=silence") {
		t.Errorf("probe argv = %v, want wave=silence present", gotArgv)
	}
	if !strings.Contains(joined, "volume=0") {
		t.Errorf("probe argv = %v, want volume=0 present", gotArgv)
	}
}

// TestProbeOutputReportsBusyDistinctFromUnavailable proves a device held
// open by something else is told apart from a device that genuinely does
// not work — ALSA's own -EBUSY wording surfaces as Busy=true.
func TestProbeOutputReportsBusyDistinctFromUnavailable(t *testing.T) {
	fakeGstLaunch(t)
	withProbeRunner(t, func(ctx context.Context, path string, argv []string) (string, bool) {
		return "ERROR: from element ...: Could not open audio device for playback.\n" +
			"Device or resource busy\n", false
	})

	got := ProbeOutput(context.Background(), "hw:CARD=X,DEV=0", 0, 0)
	if got.Available {
		t.Fatal("ProbeOutput.Available = true, want false")
	}
	if !got.Busy {
		t.Error("ProbeOutput.Busy = false, want true for an EBUSY failure")
	}
}

func TestProbeOutputUnavailableWhenNeverReachedPlaying(t *testing.T) {
	fakeGstLaunch(t)
	withProbeRunner(t, func(ctx context.Context, path string, argv []string) (string, bool) {
		return "ERROR: from element ...: could not open audio device\n", false
	})

	got := ProbeOutput(context.Background(), "hw:CARD=GHOST,DEV=0", 0, 0)
	if got.Available {
		t.Fatal("ProbeOutput.Available = true, want false: pipeline never reported PLAYING and exited non-zero")
	}
	if got.Reason == "" {
		t.Error("ProbeOutput.Reason is empty, want a stated reason")
	}
}

func TestProbeOutputUnavailableWhenExitedNonZeroDespitePlayingText(t *testing.T) {
	fakeGstLaunch(t)
	withProbeRunner(t, func(ctx context.Context, path string, argv []string) (string, bool) {
		// "Setting pipeline to PLAYING" is printed even on some failed
		// transitions (measured behavior documented in
		// internal/agent/pipeline/process.go's runningStateMarker comment);
		// a non-zero exit must still refuse Available.
		return "Setting pipeline to PLAYING ...\nERROR: state change failed\n", false
	})

	got := ProbeOutput(context.Background(), "hw:CARD=X,DEV=0", 0, 0)
	if got.Available {
		t.Fatal("ProbeOutput.Available = true, want false: process exited non-zero")
	}
}

func TestProbeOutputMissingGstLaunchReportsUnavailable(t *testing.T) {
	orig := resolveGstLaunch
	resolveGstLaunch = func() (string, bool, string) { return "", false, "gst-launch-1.0 not found on PATH" }
	t.Cleanup(func() { resolveGstLaunch = orig })

	got := ProbeOutput(context.Background(), "hw:CARD=X,DEV=0", 0, 0)
	if got.Available {
		t.Fatal("ProbeOutput.Available = true, want false when gst-launch-1.0 cannot be resolved")
	}
	if !strings.Contains(got.Reason, "not found") {
		t.Errorf("ProbeOutput.Reason = %q, want it to explain the missing binary", got.Reason)
	}
}

func TestProbeOutputNeverProbesVirtualDeviceAsIfItWereReal(t *testing.T) {
	// Sanity check on the constant this package's discovery layer depends
	// on: probing "null" is a deliberate engine check, not accidental.
	if alwaysPresentProbeDevice != "null" {
		t.Fatalf("alwaysPresentProbeDevice = %q, want %q", alwaysPresentProbeDevice, "null")
	}
}
