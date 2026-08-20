//go:build cgo

package gstengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This suite drives a real GStreamer pipeline through go-gst with
// "fakesink" — no physical audio device, no claim about ALSA or real
// hardware output. What it proves is that this package's own topology and
// state machine behave correctly against a real pipeline.

func ltcTestConfig(resolve AssetResolver) Config {
	return Config{
		SinkFactory:     "fakesink",
		SinkProperties:  map[string]any{"sync": true},
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    3,
		SampleRate:      44100,
		Resolve:         resolve,
	}
}

func newLTCTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(ltcTestConfig(resolveByRuntimeFilename))
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() {
		_ = e.Close()
	})
	return e
}

func waitForLTCState(t *testing.T, e *Engine, want agentaudio.LTCState, timeout time.Duration) agentaudio.LTCObservation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last agentaudio.LTCObservation
	for time.Now().Before(deadline) {
		last = e.ObserveLTC(context.Background())
		if last.State == want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("LTC state never reached %q within %s (last observed %+v)", want, timeout, last)
	return agentaudio.LTCObservation{}
}

const ltcOpTimeout = 30 * time.Second

func TestLTCStartReachesRunningWithAdvancingTimecode(t *testing.T) {
	e := newLTCTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	spec := agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate25, StartTimecode: "00:10:00:00"}
	if _, err := e.StartLTC(ctx, spec); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}

	first := waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)
	if !first.FrameRateKnown || first.FrameRate != pkgaudio.LTCFrameRate25 {
		t.Fatalf("first running observation has unknown or wrong frame rate: %+v", first)
	}
	if !first.TimecodeKnown || first.Timecode < spec.StartTimecode {
		t.Fatalf("first running observation's timecode %q is not at or after the start offset %q", first.Timecode, spec.StartTimecode)
	}

	deadline := time.Now().Add(ltcOpTimeout)
	for time.Now().Before(deadline) {
		obs := e.ObserveLTC(context.Background())
		if obs.State != agentaudio.LTCRunning {
			t.Fatalf("LTC dropped out of running: %+v", obs)
		}
		if obs.TimecodeKnown && obs.Timecode > first.Timecode {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("LTC timecode never advanced past the first observed value %q", first.Timecode)
}

func TestLTCStopReturnsToStoppedAndHalts(t *testing.T) {
	e := newLTCTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	spec := agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate30, StartTimecode: "00:00:00:00"}
	if _, err := e.StartLTC(ctx, spec); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)

	obs, err := e.StopLTC(ctx)
	if err != nil {
		t.Fatalf("StopLTC: %v", err)
	}
	if obs.State != agentaudio.LTCStopped || obs.Reason == "" {
		t.Fatalf("StopLTC observation = %+v, want stopped with a reason", obs)
	}
	if obs.TimecodeKnown {
		t.Fatalf("stopped observation still claims a known timecode: %+v", obs)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if o := e.ObserveLTC(context.Background()); o.State == agentaudio.LTCRunning {
			t.Fatalf("LTC resumed running after Stop: %+v", o)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLTCRestartReanchorsTimecode(t *testing.T) {
	e := newLTCTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate25, StartTimecode: "01:00:00:00"}); err != nil {
		t.Fatalf("StartLTC (first run): %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)

	restartAt := pkgaudio.LTCTimecode("00:05:00:00")
	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate25, StartTimecode: restartAt}); err != nil {
		t.Fatalf("StartLTC (restart): %v", err)
	}

	deadline := time.Now().Add(ltcOpTimeout)
	for time.Now().Before(deadline) {
		obs := e.ObserveLTC(context.Background())
		if obs.State == agentaudio.LTCRunning && obs.TimecodeKnown {
			if obs.Timecode < restartAt {
				t.Fatalf("restart observation's timecode %q is before the new start offset %q", obs.Timecode, restartAt)
			}
			if obs.Timecode < "00:59:00:00" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("restart never produced a timecode anchored at %q", restartAt)
}

func TestLTCRunsAlongsideProgramPlayback(t *testing.T) {
	e := newLTCTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate30, StartTimecode: "00:00:00:00"}); err != nil {
		t.Fatalf("StartLTC: %v", err)
	}
	waitForLTCState(t, e, agentaudio.LTCRunning, ltcOpTimeout)

	if _, err := e.Load(ctx, "prog", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "prog", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// This one shared output pipeline carries both signals — proof, not
	// assertion, since a second pipeline would have no way to keep this
	// program branch playing while the LTC channel above is polled.
	state := waitForPosition(t, e, "prog", 500*time.Millisecond, ltcOpTimeout)
	if state != pkgaudio.StatePlaying {
		t.Fatalf("program state = %q, want playing", state)
	}
	if obs := e.ObserveLTC(context.Background()); obs.State != agentaudio.LTCRunning {
		t.Fatalf("LTC state after program playback advanced = %+v, want running", obs)
	}
}

func TestLTCChannelZeroNeverReportsRunning(t *testing.T) {
	e, err := New(testConfig(resolveByRuntimeFilename)) // LTCChannel is unset (0) here
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), ltcOpTimeout)
	defer cancel()

	obs := e.ObserveLTC(context.Background())
	if obs.State != agentaudio.LTCUnsupported || obs.Reason == "" {
		t.Fatalf("ObserveLTC on an LTCChannel:0 engine = %+v, want unsupported with a reason", obs)
	}

	if _, err := e.StartLTC(ctx, agentaudio.LTCSpec{FrameRate: pkgaudio.LTCFrameRate25, StartTimecode: "00:00:00:00"}); err == nil {
		t.Fatalf("StartLTC on an LTCChannel:0 engine succeeded, want an error")
	}
	if obs := e.ObserveLTC(context.Background()); obs.State == agentaudio.LTCRunning {
		t.Fatalf("StartLTC on an LTCChannel:0 engine reported running: %+v", obs)
	}
}
