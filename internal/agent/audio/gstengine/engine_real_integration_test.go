//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
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
// testCleanupTimeout bounds the harness's own pipeline teardown.
const testCleanupTimeout = 5 * time.Second

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
		// Bounded, not a bare SetState: a test that deliberately abandons a
		// state change leaves a goroutine inside gst_element_set_state
		// holding that element's state lock, and this bin-level transition
		// recurses into the same element. An unbounded call here blocks the
		// whole test binary until its timeout rather than the branch alone.
		ctx, cancel := context.WithTimeout(context.Background(), testCleanupTimeout)
		defer cancel()
		if err := boundedCall(ctx, func() error {
			e.pipeline.SetState(gst.StateNull)
			return nil
		}); err != nil {
			t.Logf("test engine cleanup abandoned the pipeline's NULL transition: %v", err)
		}
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

// engineOpTimeout bounds each engine call these tests make. It is
// deliberately far above any healthy call: what is under test is the
// engine's behaviour, not its latency, and a tight bound on a loaded
// host fails on contention rather than on a defect.
const engineOpTimeout = 30 * time.Second

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

// exhaustedLoadDeadline is deliberately not a small positive duration
// like a millisecond: Load was measured at 1.5-2.7ms on this
// environment, so a 1ms budget is genuinely marginal rather than
// reliably exhausted, and a change to setElementsState's own bounded-call
// shape (unrelated to correctness here) shifted that race enough to make
// both tests below flake at roughly 50% under repeated runs. A
// nanosecond deadline is already expired by the time Load is entered
// regardless of Load's own duration, which is what "an exhausted
// deadline" actually requires; see the same technique used throughout
// this package's other timed-out-call tests.
const exhaustedLoadDeadline = time.Nanosecond

// TestLoadDeadlineDoesNotLeakElements proves a Load that fails because its
// own ctx deadline fired before setElementsState(PAUSED) returned still
// tears the branch's elements out of the pipeline, rather than handing
// teardown the same exhausted ctx and leaving them attached forever.
func TestLoadDeadlineDoesNotLeakElements(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	nextID := e.nextID.Load() + 1
	filesrcName := fmt.Sprintf("h%d-filesrc", nextID)

	ctx, cancel := context.WithTimeout(context.Background(), exhaustedLoadDeadline)
	defer cancel()
	if _, err := e.Load(ctx, "leak1", mediaRef(wav), 3*time.Second); err == nil {
		t.Fatalf("Load with an exhausted deadline: err = nil, want an error")
	}

	bin, ok := e.pipeline.(gst.Bin)
	if !ok {
		t.Fatalf("engine pipeline is not a gst.Bin")
	}
	const settleWait = 3 * time.Second
	deadline := time.Now().Add(settleWait)
	for time.Now().Before(deadline) {
		if bin.GetByName(filesrcName) == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("branch element %q still present in the pipeline %s after a failed Load", filesrcName, settleWait)
}

// TestLoadTimeoutIsWrappedWithDistinctSentinel proves a Load that fails
// only because its own ctx deadline fired is wrapped with errLoadTimedOut,
// not left as a bare, opaque context.DeadlineExceeded.
func TestLoadTimeoutIsWrappedWithDistinctSentinel(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), exhaustedLoadDeadline)
	defer cancel()
	_, err := e.Load(ctx, "timeout1", mediaRef(wav), 3*time.Second)
	if err == nil {
		t.Fatalf("Load with an exhausted deadline: err = nil, want an error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Load with an exhausted deadline: err = %v, want context.DeadlineExceeded in its chain", err)
	}
	if !errors.Is(err, errLoadTimedOut) {
		t.Fatalf("Load with an exhausted deadline: err = %v, want errLoadTimedOut in its chain", err)
	}
	// FaultDecodeFailure was never reachable from a ctx-deadline path even
	// before errLoadTimedOut existed; this only guards against a future
	// regression that would make it reachable.
	if fault := pkgaudio.ClassifyFault(err); fault == pkgaudio.FaultDecodeFailure {
		t.Fatalf("Load timeout classified as %q, want anything but decode_failure", fault)
	}
}

func TestLoadStartObservesAdvancingPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

// TestFadeReachesTargetGain proves a fade started on a branch that
// joined well after the engine came up still completes on the duration
// passed to Fade, and FadeActive reflects that ramp rather than wall time.
func TestFadeReachesTargetGain(t *testing.T) {
	const engineUptime = 4 * time.Second
	const fadeDuration = 400 * time.Millisecond
	const checkAfter = 900 * time.Millisecond

	e, err := New(testConfig(resolveByRuntimeFilename))
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	time.Sleep(engineUptime)

	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "f1", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "f1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fadeObs, err := e.Fade(ctx, "f1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0.25})
	if err != nil {
		t.Fatalf("Fade: %v", err)
	}
	if !fadeObs.FadeActive {
		t.Fatalf("immediately after Fade: FadeActive = false, want true")
	}

	time.Sleep(checkAfter)
	obs, err := e.Observe(ctx, "f1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.FadeActive {
		t.Fatalf("%s after a %s fade started on a branch that joined at %s uptime: FadeActive = true, want false", checkAfter, fadeDuration, engineUptime)
	}
	if obs.Gain < 0.20 || obs.Gain > 0.30 {
		t.Fatalf("gain after fade completion = %v, want close to 0.25", obs.Gain)
	}

	_ = e.Release(context.Background(), "f1")
}

// TestFadeHeldByPauseStaysActiveAndCompletesOnResume proves a fade Pause
// catches mid-ramp is genuinely held, not merely reported held: gain does
// not move further while paused (the ramp cannot advance without flow),
// FadeActive stays true for as long as it is held short of target (see
// fadeArrived's doc comment), and Resume lets the remaining ramp play out
// to completion, at which point FadeActive finally clears and gain
// reports the target.
func TestFadeHeldByPauseStaysActiveAndCompletesOnResume(t *testing.T) {
	const fadeDuration = 300 * time.Millisecond

	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "fp1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "fp1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A multi-second pre-fade wait, not a token 100ms one: fadeStartPos
	// must be large enough that a broken segment-relative anchor (reset
	// toward zero by Resume's own re-anchoring seek) would visibly throw
	// FadeActive's completion far off, rather than hiding the defect
	// under a wait so short the miscalculation rounds away.
	waitForPosition(t, e, "fp1", 2*time.Second, 5*time.Second)

	if _, err := e.Fade(ctx, "fp1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}
	// Catch the ramp while it is still clearly in flight, well short of
	// fadeDuration, so pausing genuinely interrupts it rather than racing
	// its natural completion.
	time.Sleep(fadeDuration / 3)
	pauseObs, err := e.Pause(ctx, "fp1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	t.Logf("paused mid-fade: state=%s gain=%v fadeActive=%v", pauseObs.State, pauseObs.Gain, pauseObs.FadeActive)
	if !pauseObs.FadeActive {
		t.Fatalf("immediately after Pause interrupted a fade short of its target: FadeActive = false, want true")
	}
	if pauseObs.Gain <= 0 {
		t.Fatalf("gain at Pause = %v, want > 0 (fade must not have already reached its 0 target)", pauseObs.Gain)
	}

	// The block sits upstream of queue (see blockFlow's doc comment), so
	// up to one queue's worth of already-buffered, already-ramped content
	// still reaches volume immediately after Pause, bounded by
	// queueMaxSizeTime. Settle past that bound before treating gain as
	// the held value the rest of this test checks stays put; that brief
	// drain is not the defect this test is proving.
	time.Sleep(4 * queueMaxSizeTime)
	settled, err := e.Observe(ctx, "fp1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	heldGain := settled.Gain

	// Held well past fadeDuration: a genuinely blocked ramp cannot
	// advance, so gain must stay exactly where the settled value was and
	// FadeActive must stay reported true throughout, never flipping false
	// from wall-clock time alone.
	deadline := time.Now().Add(fadeDuration * 5)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		obs, err := e.Observe(ctx, "fp1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.Gain != heldGain {
			t.Fatalf("gain moved while paused mid-fade: was %v, now %v", heldGain, obs.Gain)
		}
		if !obs.FadeActive {
			t.Fatalf("FadeActive cleared while the fade was held by Pause short of its target (gain=%v, target=0)", obs.Gain)
		}
	}

	if _, err := e.Resume(ctx, "fp1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	deadline = time.Now().Add(engineOpTimeout)
	var last agentaudio.EngineObservation
	for time.Now().Before(deadline) {
		obs, err := e.Observe(ctx, "fp1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs
		if !obs.FadeActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last.FadeActive {
		t.Fatalf("FadeActive never cleared after Resume let the remaining ramp play out")
	}
	if last.Gain > 0.05 {
		t.Fatalf("gain after Resume completed the fade = %v, want close to 0", last.Gain)
	}

	_ = e.Release(context.Background(), "fp1")
}

// TestFadeAnchorSurvivesResumeSeek proves the fade completion bound
// (fadeArrived) is unaffected by Resume's own flushing seek. That seek
// resets segmentStart, which is the branch position localRunningTime
// measures from; a fadeStartPos of several seconds, not the ~100ms a
// fade dispatched right after Start would give, is what exposes a bound
// still keyed off segment-relative local running time, since a small
// fadeStartPos rounds the resulting error away under this test's own
// deadline.
func TestFadeAnchorSurvivesResumeSeek(t *testing.T) {
	const fileDuration = 20 * time.Second
	const preFadeWait = 4 * time.Second
	const fadeDuration = 2 * time.Second
	const pauseIntoFade = 700 * time.Millisecond
	const holdDuration = 1 * time.Second

	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, fileDuration.Seconds())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := e.Load(ctx, "fa1", mediaRef(wav), fileDuration); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "fa1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "fa1", preFadeWait, 10*time.Second)

	if _, err := e.Fade(ctx, "fa1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}
	time.Sleep(pauseIntoFade)
	if _, err := e.Pause(ctx, "fa1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	time.Sleep(holdDuration)

	resumedAt := time.Now()
	resumeObs, err := e.Resume(ctx, "fa1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Logf("resumed at position %s, %s into a %s fade at Pause", resumeObs.Position, pauseIntoFade, fadeDuration)

	// The remaining ramp is roughly fadeDuration-pauseIntoFade; allow a
	// generous margin for scheduling jitter and the queue's own settle,
	// but nowhere near fadeStartPos (preFadeWait, ~4s) or fadeDuration
	// itself (2s): a broken segment-relative anchor reported this test's
	// own scenario clearing 5.85s after Resume, not the ~1.3s expected.
	const clearWithin = 3 * time.Second
	deadline := time.Now().Add(clearWithin)
	var last agentaudio.EngineObservation
	for time.Now().Before(deadline) {
		obs, err := e.Observe(ctx, "fa1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs
		if !obs.FadeActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	elapsedSinceResume := time.Since(resumedAt)
	t.Logf("FadeActive=%v gain=%v position=%v elapsed since Resume=%s", last.FadeActive, last.Gain, last.Position, elapsedSinceResume)

	if last.FadeActive {
		t.Fatalf("FadeActive still true %s after Resume (want cleared within %s): position=%s gain=%v, "+
			"a fade that finished is being reported as still in progress", elapsedSinceResume, clearWithin, last.Position, last.Gain)
	}
	if last.Gain > 0.05 {
		t.Fatalf("gain once FadeActive cleared = %v, want close to 0", last.Gain)
	}

	_ = e.Release(context.Background(), "fa1")
}

// TestFadeHeldByStopStaysActive proves Stop, like Pause, genuinely halts
// a fade's ramp rather than merely reporting it halted: catching a fade
// short of its target and then Stopping must hold both gain and
// FadeActive exactly where Stop left them, since with flow genuinely
// blocked (see Stop's and blockFlow's doc comments) nothing remains to
// carry the ramp the rest of the way. This engine call never resolves
// the fade to a terminal outcome on its own; see Manager.Stop and
// resolveFadePendingStrandedLocked in package audio for the layer that
// does.
func TestFadeHeldByStopStaysActive(t *testing.T) {
	const fadeDuration = 300 * time.Millisecond

	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "fs1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "fs1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "fs1", 100*time.Millisecond, 5*time.Second)

	if _, err := e.Fade(ctx, "fs1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0}); err != nil {
		t.Fatalf("Fade: %v", err)
	}
	time.Sleep(fadeDuration / 3)
	stopObs, err := e.Stop(ctx, "fs1")
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	t.Logf("stopped mid-fade: state=%s gain=%v fadeActive=%v", stopObs.State, stopObs.Gain, stopObs.FadeActive)
	if !stopObs.FadeActive {
		t.Fatalf("immediately after Stop interrupted a fade short of its target: FadeActive = false, want true")
	}
	if stopObs.Gain <= 0 {
		t.Fatalf("gain at Stop = %v, want > 0 (fade must not have already reached its 0 target)", stopObs.Gain)
	}

	// See TestFadeHeldByPauseStaysActiveAndCompletesOnResume for why this
	// settle happens before the held value is captured: the block sits
	// upstream of queue, so up to one queue's worth of already-buffered
	// content still drains immediately after Stop.
	time.Sleep(4 * queueMaxSizeTime)
	settled, err := e.Observe(ctx, "fs1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	heldGain := settled.Gain

	var last agentaudio.EngineObservation
	for i := 1; i <= 3; i++ {
		time.Sleep(time.Second)
		obs, err := e.Observe(ctx, "fs1")
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		last = obs
		t.Logf("  +%ds: state=%s gain=%v fadeActive=%v", i, obs.State, obs.Gain, obs.FadeActive)
	}
	if last.Gain != heldGain {
		t.Fatalf("gain moved after Stop interrupted a fade: was %v, now %v", heldGain, last.Gain)
	}
	if !last.FadeActive {
		t.Fatalf("3 seconds after Stop interrupted a %s fade short of target: FadeActive = false, want true", fadeDuration)
	}

	_ = e.Release(context.Background(), "fs1")
}

// TestFadeIssuedBeforeStartIsRefused proves Fade refuses a branch that
// has never been Start'd, rather than running a ramp against preroll
// decode that a later Start's flushing seek would replay across real
// playback. SetGain remains available for presetting a gain before Start.
func TestFadeIssuedBeforeStartIsRefused(t *testing.T) {
	const fadeDuration = 300 * time.Millisecond

	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 5)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "fb1", mediaRef(wav), 5*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, err := e.Fade(ctx, "fb1", pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: fadeDuration, TargetGain: 0.4}); !errors.Is(err, errFadeBeforeStart) {
		t.Fatalf("Fade before Start: err = %v, want errFadeBeforeStart", err)
	}

	obs, err := e.Observe(ctx, "fb1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.FadeActive {
		t.Fatalf("after a refused Fade issued before Start: FadeActive = true, want false")
	}
	if !gainWithin(obs.Gain, 1.0, fadeGainTolerance) {
		t.Fatalf("after a refused Fade issued before Start: Gain = %v, want unchanged 1.0", obs.Gain)
	}

	setObs, err := e.SetGain(ctx, "fb1", 0.4)
	if err != nil {
		t.Fatalf("SetGain before Start: %v", err)
	}
	if !gainWithin(setObs.Gain, 0.4, fadeGainTolerance) {
		t.Fatalf("SetGain before Start: Gain = %v, want close to 0.4", setObs.Gain)
	}

	_ = e.Release(context.Background(), "fb1")
}

func TestNaturalCompletionReportsCompleted(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "short.wav")
	generateWAV(t, wav, 1)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "c1", mediaRef(wav), 1*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "c1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(engineOpTimeout)
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

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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
	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
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

// TestBrokenOutputPipelineStopsAnsweringWithStaleState proves a session
// on a dead output pipeline never keeps answering with the state it last
// held. Telemetry does not pass through the session layer's availability
// gate, so an engine that reports playing here reports playing to the
// operator while the output is dead.
func TestBrokenOutputPipelineStopsAnsweringWithStaleState(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "bp1", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "bp1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "bp1", 100*time.Millisecond, 5*time.Second)

	e.markBroken("output sink failed")

	if ok, reason := e.Available(); ok {
		t.Fatal("Available reports true after the output pipeline failed")
	} else if reason == "" {
		t.Fatal("Available reports false with no reason")
	}

	calls := map[string]func() (agentaudio.EngineObservation, error){
		"Observe": func() (agentaudio.EngineObservation, error) { return e.Observe(ctx, "bp1") },
		"Start":   func() (agentaudio.EngineObservation, error) { return e.Start(ctx, "bp1", 0) },
		"Pause":   func() (agentaudio.EngineObservation, error) { return e.Pause(ctx, "bp1") },
		"Resume":  func() (agentaudio.EngineObservation, error) { return e.Resume(ctx, "bp1") },
		"Seek":    func() (agentaudio.EngineObservation, error) { return e.Seek(ctx, "bp1", 0) },
		"Stop":    func() (agentaudio.EngineObservation, error) { return e.Stop(ctx, "bp1") },
		"SetGain": func() (agentaudio.EngineObservation, error) { return e.SetGain(ctx, "bp1", 0.5) },
	}
	for name, call := range calls {
		obs, err := call()
		if err == nil {
			t.Errorf("%s on a broken pipeline returned state %q with no error", name, obs.State)
			continue
		}
		if pkgaudio.ClassifyFault(err) != pkgaudio.FaultPipelineCrash {
			t.Errorf("%s error classified %q, want pipeline_crash: %v", name, pkgaudio.ClassifyFault(err), err)
		}
	}

	if _, err := e.Load(ctx, "bp2", mediaRef(wav), 3*time.Second); err == nil {
		t.Error("Load on a broken pipeline succeeded")
	}
	if err := e.Release(ctx, "bp1"); err != nil {
		t.Errorf("Release on a broken pipeline: %v, want it to stay possible", err)
	}
}

// TestLateJoiningBranchPlaysFromOwnStartPosition proves a branch that
// Loads and Starts after the output pipeline has been running for a while
// reports its own elapsed play time, not the pipeline's own uptime folded
// in. GstAudioAggregator (audiomixer, interleave) advances its output
// running time continuously once anything is connected, so a branch whose
// buffers arrive at running time 0 lands in the aggregator's past and is
// silently discarded unless its mixer sink pads are re-anchored to "now".
func TestLateJoiningBranchPlaysFromOwnStartPosition(t *testing.T) {
	const engineUptime = 4 * time.Second
	const settleWait = 800 * time.Millisecond
	// pass bound is deliberately well above settleWait (scheduling jitter
	// under load) and well below engineUptime (what the regression folds in).
	const passBound = 2 * time.Second

	// Both cases exercise the same program-audio late-join property; the
	// second only proves that property is unaffected by an LTC channel
	// also being configured. Neither case starts or observes an LTC run,
	// so neither is evidence about LTC's own late-join or anchoring
	// behavior — see ltc_real_integration_test.go for that.
	cases := []struct {
		name string
		cfg  func(AssetResolver) Config
	}{
		{"LTCChannel unset", testConfig},
		{"LTCChannel also configured, program late-join only", ltcTestConfig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := New(tc.cfg(resolveByRuntimeFilename))
			if err != nil {
				t.Fatalf("New: unexpected structural config error: %v", err)
			}
			if ok, reason := e.Available(); !ok {
				t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
			}
			t.Cleanup(func() { _ = e.Close() })

			time.Sleep(engineUptime)

			dir := t.TempDir()
			wav := filepath.Join(dir, "fixture.wav")
			generateWAV(t, wav, 8)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			if _, err := e.Load(ctx, "late", mediaRef(wav), 8*time.Second); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := e.Start(ctx, "late", 0); err != nil {
				t.Fatalf("Start: %v", err)
			}
			time.Sleep(settleWait)

			obs, err := e.Observe(ctx, "late")
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if obs.Position >= passBound {
				t.Fatalf("position %s is close to the engine's %s uptime; branch played from the middle of the file", obs.Position, engineUptime)
			}
			if obs.Position < settleWait/2 {
				t.Fatalf("position %s did not advance after Start", obs.Position)
			}

			_ = e.Release(context.Background(), "late")
		})
	}
}

// TestStartAfterLoadGapPlaysFromNamedPosition proves Start begins
// producing from the position it names at the moment it is called, not
// from wherever the branch had drifted to while loaded and frozen.
func TestStartAfterLoadGapPlaysFromNamedPosition(t *testing.T) {
	const loadToStartGap = 3 * time.Second
	const settleWait = 800 * time.Millisecond
	const passBound = 2 * time.Second

	cases := []struct {
		name    string
		startAt time.Duration
	}{
		{"zero position", 0},
		{"non-zero position", 2 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(t)
			dir := t.TempDir()
			wav := filepath.Join(dir, "fixture.wav")
			generateWAV(t, wav, 8)

			ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
			defer cancel()

			if _, err := e.Load(ctx, "gap", mediaRef(wav), 8*time.Second); err != nil {
				t.Fatalf("Load: %v", err)
			}
			time.Sleep(loadToStartGap)

			if _, err := e.Start(ctx, "gap", tc.startAt); err != nil {
				t.Fatalf("Start: %v", err)
			}
			time.Sleep(settleWait)

			obs, err := e.Observe(ctx, "gap")
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			elapsedSinceStart := obs.Position - tc.startAt
			if elapsedSinceStart >= passBound {
				t.Fatalf("position %s is %s past the named start position %s; the Load-to-Start gap folded in", obs.Position, elapsedSinceStart, tc.startAt)
			}
			if elapsedSinceStart < settleWait/2 {
				t.Fatalf("position %s did not advance from the named start position after Start", obs.Position)
			}

			_ = e.Release(context.Background(), "gap")
		})
	}
}

// TestRepeatStartWhilePlayingReanchorsToNamedPosition proves a repeat
// Start on an already-playing branch, at a non-zero position, seeks and
// re-anchors to that position rather than being a no-op.
func TestRepeatStartWhilePlayingReanchorsToNamedPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "r1", mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "r1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "r1", 500*time.Millisecond, 5*time.Second)

	const target = 3 * time.Second
	obs, err := e.Start(ctx, "r1", target)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if obs.Position < target-200*time.Millisecond || obs.Position > target+900*time.Millisecond {
		t.Fatalf("position %s immediately after a repeat Start to %s; want close to the named position", obs.Position, target)
	}

	_ = e.Release(context.Background(), "r1")
}

// TestResumeReanchorsImmediatelyAfterPause proves Resume re-invokes
// resyncMixerPads by checking playback continues tightly from Pause's
// reported position when Resume follows it back to back, with no held
// interval between them.
func TestResumeReanchorsImmediatelyAfterPause(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "res1", mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "res1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPosition(t, e, "res1", 200*time.Millisecond, 5*time.Second)

	pauseObs, err := e.Pause(ctx, "res1")
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	frozenAt := pauseObs.Position

	if _, err := e.Resume(ctx, "res1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	const settleWait = 300 * time.Millisecond
	time.Sleep(settleWait)

	obs, err := e.Observe(ctx, "res1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	elapsedSinceResume := obs.Position - frozenAt
	if elapsedSinceResume < settleWait/2 || elapsedSinceResume > settleWait+500*time.Millisecond {
		t.Fatalf("position %s is %s from where Pause left it after a %s settle; want tight to elapsed play time", obs.Position, elapsedSinceResume, settleWait)
	}

	_ = e.Release(context.Background(), "res1")
}

// TestSeekAfterGapReanchors proves Seek re-anchors the mixer offset
// against the pipeline's current running time at the moment of the seek,
// not the one in effect when the branch started.
func TestSeekAfterGapReanchors(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "sk1", mediaRef(wav), 6*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "sk1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const preSeekGap = 3 * time.Second
	time.Sleep(preSeekGap)

	target := 1 * time.Second
	if _, err := e.Seek(ctx, "sk1", target); err != nil {
		t.Fatalf("Seek: %v", err)
	}

	const settleWait = 500 * time.Millisecond
	time.Sleep(settleWait)

	obs, err := e.Observe(ctx, "sk1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	elapsedSinceSeek := obs.Position - target
	if elapsedSinceSeek >= preSeekGap {
		t.Fatalf("position %s is %s past the seek target %s; pipeline uptime folded in on Seek", obs.Position, elapsedSinceSeek, target)
	}

	_ = e.Release(context.Background(), "sk1")
}

// TestConcurrentBranchesStartedAtDifferentTimesEachReportOwnPosition
// proves two branches, joined the shared pipeline at different moments,
// each report their own elapsed play time rather than a pipeline-relative
// one shared between them.
func TestConcurrentBranchesStartedAtDifferentTimesEachReportOwnPosition(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wavA := filepath.Join(dir, "early.wav")
	wavB := filepath.Join(dir, "late.wav")
	generateWAV(t, wavA, 6)
	generateWAV(t, wavB, 6)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "early", mediaRef(wavA), 6*time.Second); err != nil {
		t.Fatalf("Load early: %v", err)
	}
	if _, err := e.Start(ctx, "early", 0); err != nil {
		t.Fatalf("Start early: %v", err)
	}

	const headStart = 3 * time.Second
	time.Sleep(headStart)

	if _, err := e.Load(ctx, "late", mediaRef(wavB), 6*time.Second); err != nil {
		t.Fatalf("Load late: %v", err)
	}
	if _, err := e.Start(ctx, "late", 0); err != nil {
		t.Fatalf("Start late: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	earlyObs, err := e.Observe(ctx, "early")
	if err != nil {
		t.Fatalf("Observe early: %v", err)
	}
	lateObs, err := e.Observe(ctx, "late")
	if err != nil {
		t.Fatalf("Observe late: %v", err)
	}

	const latePassBound = headStart - 500*time.Millisecond
	if lateObs.Position >= latePassBound {
		t.Fatalf("late branch position %s folded in the early branch's %s head start", lateObs.Position, headStart)
	}
	if earlyObs.Position < headStart-1*time.Second {
		t.Fatalf("early branch position %s did not keep advancing while late joined", earlyObs.Position)
	}

	_ = e.Release(context.Background(), "early")
	_ = e.Release(context.Background(), "late")
}

// TestCloseReleasesTheOutputDeviceAndStopsAnswering proves a closed
// engine reports itself unavailable and refuses every call. A rebind
// builds a replacement against the same device, so an outgoing engine
// that kept answering would also still be holding that device.
func TestCloseReleasesTheOutputDeviceAndStopsAnswering(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "fixture.wav")
	generateWAV(t, wav, 3)

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()

	if _, err := e.Load(ctx, "cl1", mediaRef(wav), 3*time.Second); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := e.Start(ctx, "cl1", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second Close is not idempotent: %v", err)
	}

	ok, reason := e.Available()
	if ok {
		t.Fatal("a closed engine reports itself available")
	}
	if reason != closedReason {
		t.Fatalf("closed reason = %q, want %q", reason, closedReason)
	}
	if _, err := e.Observe(ctx, "cl1"); err == nil {
		t.Fatal("Observe on a closed engine returned no error")
	}
	if _, err := e.Load(ctx, "cl2", mediaRef(wav), 3*time.Second); err == nil {
		t.Fatal("Load on a closed engine succeeded")
	}
}
