package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file covers ADR-053 decision 7's node-side alert sequence
// (weatherdelayops.go) against a real *audio.Manager, matching this
// package's established "prove behavior against real collaborators, not
// mocks of them" convention (cueactivationaudio_test.go).

// weatherDelayCall is one recorded engine call: its kind ("gain", "start",
// "stop") in the order it actually reached the engine.
type weatherDelayCall struct {
	kind   string
	handle audio.EngineHandle
}

// weatherDelayRecordingEngine wraps [activationAvailableEngine] and
// records every SetGain/Start/Stop call, in order, across every session —
// the evidence TestWeatherDelayStartOrdering needs to prove ADR-053
// decision 7's own ordering requirement.
type weatherDelayRecordingEngine struct {
	activationAvailableEngine
	mu    sync.Mutex
	calls []weatherDelayCall
}

func (e *weatherDelayRecordingEngine) record(kind string, handle audio.EngineHandle) {
	e.mu.Lock()
	e.calls = append(e.calls, weatherDelayCall{kind: kind, handle: handle})
	e.mu.Unlock()
}

func (e *weatherDelayRecordingEngine) SetGain(ctx context.Context, handle audio.EngineHandle, gain pkgaudio.Gain) (audio.EngineObservation, error) {
	e.record("gain", handle)
	return e.activationAvailableEngine.SetGain(ctx, handle, gain)
}

func (e *weatherDelayRecordingEngine) Start(ctx context.Context, handle audio.EngineHandle, position time.Duration) (audio.EngineObservation, error) {
	e.record("start", handle)
	return e.activationAvailableEngine.Start(ctx, handle, position)
}

func (e *weatherDelayRecordingEngine) Stop(ctx context.Context, handle audio.EngineHandle) (audio.EngineObservation, error) {
	e.record("stop", handle)
	return e.activationAvailableEngine.Stop(ctx, handle)
}

func (e *weatherDelayRecordingEngine) snapshot() []weatherDelayCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]weatherDelayCall, len(e.calls))
	copy(out, e.calls)
	return out
}

// weatherDelayTestItemDuration is the fixed decoded duration
// weatherDelayDurationDecoder reports for every asset, so a test can
// drive natural playlist-item completion by advancing the fake clock
// past it.
const weatherDelayTestItemDuration = 2 * time.Second

// weatherDelayDurationDecoder reports every path as a valid, decodable
// asset with a known duration — cueactivationaudio_test.go's
// fixedAudioDecoder reports no duration at all, which is fine for that
// package's tests (none drive natural completion) but not for this
// file's repeat-count test.
type weatherDelayDurationDecoder struct{}

func (weatherDelayDurationDecoder) Decode(_ context.Context, _ string) audio.DecodeResult {
	return audio.DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/x-wav",
		Decoded: true, Codec: "pcm", Channels: 2, SampleRate: 44100,
		Discoverer: audio.DiscovererEvidence{Ran: true, Duration: weatherDelayTestItemDuration},
	}
}

// firstIndex/lastIndex find the first/last call of kind, or -1.
func firstIndex(calls []weatherDelayCall, kind string) int {
	for i, c := range calls {
		if c.kind == kind {
			return i
		}
	}
	return -1
}

// newWeatherDelayTestManager builds a real *audio.Manager against a
// recording engine and a fixed decoder, matching newTestAudioManager
// (cueactivationaudio_test.go) except for the engine wrapper.
func newWeatherDelayTestManager(t *testing.T, dir string, clock *fakeClock) (*audio.Manager, *weatherDelayRecordingEngine) {
	t.Helper()
	return newWeatherDelayTestManagerWithDecoder(t, dir, clock, fixedAudioDecoder{})
}

func newWeatherDelayTestManagerWithDecoder(t *testing.T, dir string, clock *fakeClock, decoder audio.Decoder) (*audio.Manager, *weatherDelayRecordingEngine) {
	t.Helper()
	fake := audio.NewFakeEngine(clock.now)
	engine := &weatherDelayRecordingEngine{activationAvailableEngine: activationAvailableEngine{fake}}
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, decoder, clock.now, nil)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
	})
	return mgr, engine
}

// weatherDelayTestPlan builds a plan naming one alert asset (already
// written to dir) for kind, with repeatCount repeats.
func weatherDelayTestPlan(kind, assetID, contentHash, filename string, repeatCount int) mqttproto.WeatherDelayPlan {
	ref := &mqttproto.WeatherDelayAlertAssetRef{AssetID: assetID, ContentHash: contentHash, Filename: filename}
	plan := mqttproto.WeatherDelayPlan{RepeatCount: repeatCount}
	switch kind {
	case "delay":
		plan.Delay = ref
	case "cancelNight":
		plan.CancelNight = ref
	}
	return plan
}

// TestWeatherDelayStartOrdering proves ADR-053 decision 7's own ordering
// requirement: every other session's gain reaches zero before the
// alert's Start, and the alert's Start reaches the engine before any
// other session's Stop.
func TestWeatherDelayStartOrdering(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	const other = pkgaudio.SessionID("other-session")
	otherRef := writeTestWAV(t, dir, "other.wav", "other-asset")
	if r := mgr.Apply(context.Background(), other, "apply-other", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(otherRef)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply other: %+v", r)
	}
	if r := mgr.Start(context.Background(), other, "start-other", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start other: %+v", r)
	}

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 3)

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played, reason := ops.doStart(context.Background(), "delay", clock.now())
	if !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := engine.snapshot()
		if firstIndex(calls, "stop") != -1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SilenceAllExcept's stop of %q never reached the engine; calls so far: %+v", other, calls)
		}
		time.Sleep(time.Millisecond)
	}

	calls := engine.snapshot()
	gainIdx := firstIndex(calls, "gain")
	startIdx := firstIndex(calls, "start")
	stopIdx := firstIndex(calls, "stop")
	if gainIdx == -1 || startIdx == -1 || stopIdx == -1 {
		t.Fatalf("missing an expected call kind in %+v", calls)
	}
	if gainIdx >= startIdx || startIdx >= stopIdx {
		t.Fatalf("call order = %+v, want gain < start < stop", calls)
	}
}

// wedgingStopEngine wraps [activationAvailableEngine] and blocks forever
// on Stop for one named handle, proving the alert still starts even
// though (c) would never finish.
type wedgingStopEngine struct {
	activationAvailableEngine
	wedge audio.EngineHandle
}

func (e *wedgingStopEngine) Stop(ctx context.Context, handle audio.EngineHandle) (audio.EngineObservation, error) {
	if handle == e.wedge {
		<-ctx.Done()
		return audio.EngineObservation{}, ctx.Err()
	}
	return e.activationAvailableEngine.Stop(ctx, handle)
}

// TestWeatherDelayStartsAlertEvenWhenAnotherSessionsStopBlocksForever
// proves decision 7's own guarantee: (b) never waits on (c).
func TestWeatherDelayStartsAlertEvenWhenAnotherSessionsStopBlocksForever(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	fake := audio.NewFakeEngine(clock.now)
	engine := &wedgingStopEngine{activationAvailableEngine: activationAvailableEngine{fake}}
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, clock.now, nil)
	mgr.SetSettings(audio.Settings{DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500})

	const wedged = pkgaudio.SessionID("wedged-session")
	wedgedRef := writeTestWAV(t, dir, "wedged.wav", "wedged-asset")
	mgr.Apply(context.Background(), wedged, "apply-wedged", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(wedgedRef)})
	mgr.Start(context.Background(), wedged, "start-wedged", 2)
	wedgedHandle, ok := fake.LastLoadedHandle()
	if !ok {
		t.Fatal("no engine handle recorded for the wedged session")
	}
	engine.wedge = wedgedHandle

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 3)

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	done := make(chan struct{})
	var played bool
	var reason string
	go func() {
		played, reason = ops.doStart(context.Background(), "delay", clock.now())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("doStart did not return within 5s; the alert must never wait on a wedged Stop")
	}
	if !played {
		t.Fatalf("alert did not play: %s", reason)
	}
}

// TestWeatherDelayAlertPlaysExactlyRepeatCountTimesThenEnds proves the
// alert plays repeatCount times (as distinct playlist items, RepeatNone)
// and then stops on its own.
func TestWeatherDelayAlertPlaysExactlyRepeatCountTimesThenEnds(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManagerWithDecoder(t, dir, clock, weatherDelayDurationDecoder{})

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 3)

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played, reason := ops.doStart(context.Background(), "delay", clock.now())
	if !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		mgr.RunWatcher(ctx, ticks)
	}()

	// Ticks are sent continuously, on their own goroutine, so this test
	// never assumes a send on the unbuffered ticks channel means
	// watchTick has already finished running by the time it returns
	// (rendezvous only guarantees the receive has started) — the
	// foreground loop below polls Snapshot instead of counting ticks.
	tickerDone := make(chan struct{})
	go func() {
		defer close(tickerDone)
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				clock.advance(weatherDelayTestItemDuration + time.Second)
				select {
				case ticks <- clock.now():
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	var final pkgaudio.State
	for {
		snaps := mgr.Snapshot(context.Background())
		for _, s := range snaps {
			if s.ID == weatherDelayAlertSessionID {
				final = s.State
			}
		}
		if final == pkgaudio.StateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alert session never reached completed after %d repeats; last state %s", 3, final)
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-tickerDone
	<-watcherDone
}

// TestWeatherDelayResumeStopsAlertMidPlay proves weatherdelay.resume
// stops the alert and clears the holder, restoring or restarting
// nothing else.
func TestWeatherDelayResumeStopsAlertMidPlay(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	if err := holder.SetActiveLocal("delay", clock.now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}
	played, reason := ops.doStart(context.Background(), "delay", clock.now())
	if !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	ops.doResume(context.Background())

	if holder.Current().Active {
		t.Fatal("holder still active after weatherdelay.resume")
	}
	snaps := mgr.Snapshot(context.Background())
	for _, s := range snaps {
		if s.ID == weatherDelayAlertSessionID && s.State == pkgaudio.StatePlaying {
			t.Fatalf("alert session still playing after resume: %+v", s)
		}
	}
}

// TestWeatherDelaySecondStartDoesNotStackTheAlert proves a second
// weatherdelay.start while the alert is already playing does not
// restart or stack it: exactly one Start call reaches the engine.
func TestWeatherDelaySecondStartDoesNotStackTheAlert(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played1, reason1 := ops.doStart(context.Background(), "delay", clock.now())
	if !played1 {
		t.Fatalf("first start did not play: %s", reason1)
	}
	played2, reason2 := ops.doStart(context.Background(), "delay", clock.now())
	if !played2 {
		t.Fatalf("second start reported not playing: %s", reason2)
	}

	calls := engine.snapshot()
	startCount := 0
	for _, c := range calls {
		if c.kind == "start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("engine Start calls = %d, want exactly 1 (a second weatherdelay.start must not restart the alert)", startCount)
	}
}

// TestWeatherDelayStartWithNoConfiguredAssetStillZeroesAndSilences proves
// a plan with no asset for the requested kind is a valid configuration:
// (a) and (c) still run, and the node reports no alert played, not an
// error.
func TestWeatherDelayStartWithNoConfiguredAssetStillZeroesAndSilences(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)

	const other = pkgaudio.SessionID("other-session")
	otherRef := writeTestWAV(t, dir, "other.wav", "other-asset")
	mgr.Apply(context.Background(), other, "apply-other", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(otherRef)})
	mgr.Start(context.Background(), other, "start-other", 2)

	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	// No plan asset configured at all (zero value plan).

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played, reason := ops.doStart(context.Background(), "delay", clock.now())
	if played {
		t.Fatalf("alert reported playing with no configured asset: reason=%q", reason)
	}
	if reason == "" {
		t.Fatal("expected a stated reason for no alert playing")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snaps := mgr.Snapshot(context.Background())
		var stopped bool
		for _, s := range snaps {
			if s.ID == other && s.State == pkgaudio.StateStopped {
				stopped = true
			}
		}
		if stopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("other session was never silenced despite no alert asset being configured")
		}
		time.Sleep(time.Millisecond)
	}
}

// writeTestWAV writes a fake wav asset under dir and returns a
// [pkgaudio.MediaRef] matching it, mirroring cueactivationaudio_test.go's
// asset-writing convention but with a caller-chosen filename/asset id.
func writeTestWAV(t *testing.T, dir, filename, assetID string) pkgaudio.MediaRef {
	t.Helper()
	hash := writeAssetFixture(t, dir, filename, []byte("pretend this is wav audio content: "+filename))
	return pkgaudio.MediaRef{AssetID: assetID, ContentHash: hash, RuntimeFilename: filename}
}
