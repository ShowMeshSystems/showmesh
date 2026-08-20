//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This suite drives a real GStreamer pipeline through go-gst — no fake
// engine, no canned observations. It uses "fakesink" with sync=true (a
// real element, not a test double for GStreamer itself) so the pipeline
// paces to the clock without needing a physical audio device, matching
// how internal/agent/audio's own real-integration tests (probe_real_
// integration_test.go, mediaprobe_real_integration_test.go) gate on
// actual tool/environment availability rather than skipping silently on
// any failure.

func testConfig(resolve AssetResolver) Config {
	return Config{
		SinkFactory:     "fakesink",
		SinkProperties:  map[string]any{"sync": true},
		ProgramChannels: []int{1, 2},
		ChannelCount:    3, // channel 3 is the LTC seam, deliberately left silent
		SampleRate:      44100,
		Resolve:         resolve,
	}
}

func resolveByRuntimeFilename(m pkgaudio.MediaRef) (string, error) {
	if m.RuntimeFilename == "" {
		return "", errors.New("no runtime filename set")
	}
	return m.RuntimeFilename, nil
}

// newTestEngine skips the calling test when this environment cannot
// actually build and run the output pipeline (missing GStreamer plugins,
// no fakesink) rather than failing on an environment gap that isn't a
// bug in this package.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(testConfig(resolveByRuntimeFilename))
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() {
		e.pipeline.SetState(gst.StateNull)
	})
	return e
}

func mediaRef(path string) pkgaudio.MediaRef {
	return pkgaudio.MediaRef{AssetID: "spike-fixture", ContentHash: "test-content-hash", RuntimeFilename: path}
}

// generateWAV renders a real mono WAV fixture of approximately
// seconds duration via a real GStreamer pipeline (audiotestsrc !
// audioconvert ! wavenc ! filesink), run synchronously to EOS. It skips
// the calling test rather than failing it when this environment cannot
// even build that generating pipeline, since building the fixture is not
// itself the thing under test.
func generateWAV(t *testing.T, path string, seconds float64) {
	t.Helper()
	const sampleRate = 44100
	const samplesPerBuffer = 1024
	numBuffers := int(seconds*sampleRate/samplesPerBuffer) + 1

	launch := "audiotestsrc num-buffers=" + itoa(numBuffers) +
		" samplesperbuffer=" + itoa(samplesPerBuffer) +
		" ! audioconvert ! wavenc ! filesink location=" + path
	pl, err := gst.ParseLaunch(launch)
	if err != nil {
		t.Skipf("skipping: could not parse fixture-generation pipeline: %v", err)
	}
	pipeline, ok := pl.(gst.Pipeline)
	if !ok {
		t.Skipf("skipping: fixture-generation launch did not produce a pipeline")
	}
	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		t.Skipf("skipping: fixture-generation pipeline would not reach PLAYING")
	}
	bus := pipeline.GetBus()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		msg := bus.TimedPop(gst.ClockTime(200 * time.Millisecond))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageEOS:
			pipeline.SetState(gst.StateNull)
			return
		case gst.MessageError:
			text, _ := msg.ParseError()
			pipeline.SetState(gst.StateNull)
			t.Skipf("skipping: fixture-generation pipeline errored: %s", text)
		}
	}
	pipeline.SetState(gst.StateNull)
	t.Skipf("skipping: fixture-generation pipeline never reached EOS")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func writeGarbage(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("this is not any recognizable media container format, just bytes"), 0o644); err != nil {
		t.Fatalf("writing garbage fixture: %v", err)
	}
}

func waitForPosition(t *testing.T, e *Engine, handle string, min time.Duration, timeout time.Duration) pkgaudio.State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last time.Duration
	for time.Now().Before(deadline) {
		obs, err := e.Observe(context.Background(), agentaudio.EngineHandle(handle))
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs.Position
		if obs.Position >= min {
			return obs.State
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("position never reached %s within %s (last observed %s)", min, timeout, last)
	return ""
}

func TestBoundedCallReturnsOnContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := make(chan struct{})
	err := boundedCall(ctx, func() error {
		close(started)
		time.Sleep(2 * time.Second)
		return nil
	})
	<-started
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("boundedCall returned %v, want context.DeadlineExceeded", err)
	}
}

func TestLoadStartObservesAdvancingPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	obs, err := e.Load(ctx, "h1", mediaRef(wav), 3*time.Second)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if obs.State != pkgaudio.StateReady {
		t.Fatalf("after Load: state = %q, want ready", obs.State)
	}

	obs, err = e.Start(ctx, "h1", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if obs.State != pkgaudio.StatePlaying {
		t.Fatalf("after Start: state = %q, want playing", obs.State)
	}

	state := waitForPosition(t, e, "h1", 200*time.Millisecond, 5*time.Second)
	if state != pkgaudio.StatePlaying {
		t.Fatalf("state while position was advancing = %q, want playing", state)
	}

	if err := e.Release(context.Background(), "h1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestPauseFreezesPositionWhileOtherBranchPlays(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wavA := filepath.Join(dir, "a.wav")
	wavB := filepath.Join(dir, "b.wav")
	generateWAV(t, wavA, 3)
	generateWAV(t, wavB, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "a", mediaRef(wavA), 3*time.Second); err != nil {
		t.Fatalf("Load a: %v", err)
	}
	if _, err := e.Load(ctx, "b", mediaRef(wavB), 3*time.Second); err != nil {
		t.Fatalf("Load b: %v", err)
	}
	if _, err := e.Start(ctx, "a", 0); err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if _, err := e.Start(ctx, "b", 0); err != nil {
		t.Fatalf("Start b: %v", err)
	}

	waitForPosition(t, e, "a", 150*time.Millisecond, 5*time.Second)

	pauseObs, err := e.Pause(ctx, "a")
	if err != nil {
		t.Fatalf("Pause a: %v", err)
	}
	if pauseObs.State != pkgaudio.StatePaused {
		t.Fatalf("after Pause: state = %q, want paused", pauseObs.State)
	}
	frozenAt := pauseObs.Position

	// b must keep advancing while a is frozen.
	waitForPosition(t, e, "b", frozenAt+200*time.Millisecond, 5*time.Second)

	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		obs, err := e.Observe(ctx, "a")
		if err != nil {
			t.Fatalf("Observe a: %v", err)
		}
		if obs.Position != frozenAt {
			t.Fatalf("paused branch position moved: was %s, now %s", frozenAt, obs.Position)
		}
		if obs.State != pkgaudio.StatePaused {
			t.Fatalf("paused branch state changed to %q", obs.State)
		}
	}

	resumeObs, err := e.Resume(ctx, "a")
	if err != nil {
		t.Fatalf("Resume a: %v", err)
	}
	if resumeObs.State != pkgaudio.StatePlaying {
		t.Fatalf("after Resume: state = %q, want playing", resumeObs.State)
	}
	waitForPosition(t, e, "a", frozenAt+150*time.Millisecond, 5*time.Second)

	_ = e.Release(context.Background(), "a")
	_ = e.Release(context.Background(), "b")
}

func TestSeekReanchorsPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 4)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "s1", mediaRef(wav), 4*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "s1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "s1", 100*time.Millisecond, 5*time.Second)

	target := 2 * time.Second
	obs, err := e.Seek(ctx, "s1", target)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}
	// A flushing accurate seek re-anchors immediately; allow modest
	// tolerance for the observation racing the seek's own completion.
	if obs.Position < target-200*time.Millisecond || obs.Position > target+500*time.Millisecond {
		t.Fatalf("after Seek to %s: observed position %s, want close to target", target, obs.Position)
	}

	_ = e.Release(context.Background(), "s1")
}

func TestFadeReachesTargetGain(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "f1", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "f1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fadeObs, err := e.Fade(ctx, "f1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: 400 * time.Millisecond, TargetGain: 0.25})
	if err != nil {
		t.Fatalf("Fade: %v", err)
	}
	if !fadeObs.FadeActive {
		t.Fatalf("immediately after Fade: FadeActive = false, want true")
	}

	time.Sleep(700 * time.Millisecond)
	obs, err := e.Observe(ctx, "f1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.FadeActive {
		t.Fatalf("700ms after a 400ms fade: FadeActive = true, want false")
	}
	if obs.Gain < 0.20 || obs.Gain > 0.30 {
		t.Fatalf("gain after fade completion = %v, want close to 0.25 (no 10x scale defect)", obs.Gain)
	}

	_ = e.Release(context.Background(), "f1")
}

func TestNaturalCompletionReportsCompleted(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "short.wav")
	generateWAV(t, wav, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "c1", mediaRef(wav), 1*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "c1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var last agentaudio.EngineObservation
	for time.Now().Before(deadline) {
		obs, err := e.Observe(ctx, "c1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs
		if obs.State == pkgaudio.StateCompleted {
			_ = e.Release(context.Background(), "c1")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("branch never reported Completed, last observed state %q position %s", last.State, last.Position)
}

func TestCommandedStopReportsStopped(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "st1", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "st1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "st1", 100*time.Millisecond, 5*time.Second)

	obs, err := e.Stop(ctx, "st1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if obs.State != pkgaudio.StateStopped {
		t.Fatalf("after Stop: state = %q, want stopped (never completed)", obs.State)
	}

	time.Sleep(300 * time.Millisecond)
	after, err := e.Observe(ctx, "st1")
	if err != nil {
		t.Fatalf("Observe after Stop: %v", err)
	}
	if after.State != pkgaudio.StateStopped {
		t.Fatalf("state drifted from stopped to %q after waiting", after.State)
	}

	_ = e.Release(context.Background(), "st1")
}

func TestReleaseIsIdempotent(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "r1", mediaRef(wav), 1*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := e.Release(context.Background(), "r1"); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := e.Release(context.Background(), "r1"); err != nil {
		t.Fatalf("second Release on an already-released handle: %v, want nil", err)
	}
	if err := e.Release(context.Background(), "never-loaded"); err != nil {
		t.Fatalf("Release on a never-loaded handle: %v, want nil", err)
	}
}

func TestLoadMissingFileIsMediaDisappeared(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := e.Load(ctx, "m1", mediaRef("/nonexistent/path/does-not-exist.wav"), time.Second)
	if err == nil {
		t.Fatalf("Load of a missing file: got nil error")
	}
	if !errors.Is(err, pkgaudio.ErrEngineMediaDisappeared) {
		t.Fatalf("Load of a missing file: err = %v, want ErrEngineMediaDisappeared", err)
	}
	if got := pkgaudio.ClassifyFault(err); got != pkgaudio.FaultMediaDisappeared {
		t.Fatalf("ClassifyFault(missing file) = %q, want %q", got, pkgaudio.FaultMediaDisappeared)
	}
}

func TestLoadUndecodableFileIsDecodeFailure(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.bin")
	writeGarbage(t, garbage)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := e.Load(ctx, "d1", mediaRef(garbage), time.Second)
	if err == nil {
		t.Fatalf("Load of an undecodable file: got nil error")
	}
	if !errors.Is(err, pkgaudio.ErrEngineDecodeFailure) {
		t.Fatalf("Load of an undecodable file: err = %v, want ErrEngineDecodeFailure", err)
	}
	if got := pkgaudio.ClassifyFault(err); got != pkgaudio.FaultDecodeFailure {
		t.Fatalf("ClassifyFault(undecodable file) = %q, want %q", got, pkgaudio.FaultDecodeFailure)
	}
	// Distinct from the missing-file case, not merely "an error".
	if errors.Is(err, pkgaudio.ErrEngineMediaDisappeared) {
		t.Fatalf("Load of an undecodable file was classified as media_disappeared, want decode_failure only")
	}
}
