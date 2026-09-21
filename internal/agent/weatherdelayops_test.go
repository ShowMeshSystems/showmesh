package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Covers the node's alert sequence against a real *audio.Manager.

// weatherDelayCall is one recorded engine call: its kind ("gain", "start",
// "stop") in the order it actually reached the engine.
type weatherDelayCall struct {
	kind   string
	handle audio.EngineHandle
}

// weatherDelayRecordingEngine records every SetGain/Start/Stop call, in
// order, across every session.
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

// weatherDelayTestItemDuration is the decoded duration of every asset, so a
// test can complete an item by advancing the fake clock past it.
const weatherDelayTestItemDuration = 2 * time.Second

// weatherDelayDurationDecoder reports every path as a decodable asset with
// a known duration, which the repeat-count test needs.
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

// TestWeatherDelayStartOrdering proves other sessions are muted before the
// alert starts, and the alert starts before any other session stops.
func TestWeatherDelayStartOrdering(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	const other = pkgaudio.SessionID("other-session")
	otherRef := writeTestWAV(t, dir, "other.wav", "other-asset")
	t.Cleanup(weatherDelayBackground.Wait)
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
	played, reason, _ := ops.doStart(context.Background(), "delay", clock.now())
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
	t.Cleanup(weatherDelayBackground.Wait)
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
		played, reason, _ = ops.doStart(context.Background(), "delay", clock.now())
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
	played, reason, _ := ops.doStart(context.Background(), "delay", clock.now())
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

	// A tick send only proves the receive started, not that watchTick
	// finished, so ticks run on their own goroutine and the loop below
	// polls Snapshot instead of counting them.
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
	played, reason, _ := ops.doStart(context.Background(), "delay", clock.now())
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
	played1, reason1, _ := ops.doStart(context.Background(), "delay", clock.now())
	if !played1 {
		t.Fatalf("first start did not play: %s", reason1)
	}
	played2, reason2, _ := ops.doStart(context.Background(), "delay", clock.now())
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

// TestWeatherDelayChangeInPlaceReplacesTheAlert proves ADR-053 decision 1:
// while a delay's alert is playing, a start for cancelNight starts the
// cancel alert on its own session and stops the delay alert's session like
// any other audio, rather than stacking or being ignored as "already
// active."
func TestWeatherDelayChangeInPlaceReplacesTheAlert(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	delayHash := writeAssetFixture(t, dir, "delay.wav", []byte("delay alert audio"))
	cancelHash := writeAssetFixture(t, dir, "cancel.wav", []byte("cancel alert audio"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = mqttproto.WeatherDelayPlan{
		RepeatCount: 10,
		Delay:       &mqttproto.WeatherDelayAlertAssetRef{AssetID: "delay-asset", ContentHash: delayHash, Filename: "delay.wav"},
		CancelNight: &mqttproto.WeatherDelayAlertAssetRef{AssetID: "cancel-asset", ContentHash: cancelHash, Filename: "cancel.wav"},
	}

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played1, reason1, _ := ops.doStart(context.Background(), "delay", clock.now())
	if !played1 {
		t.Fatalf("delay start did not play: %s", reason1)
	}
	played2, reason2, _ := ops.doStart(context.Background(), "cancelNight", clock.now())
	if !played2 {
		t.Fatalf("cancelNight start did not play: %s", reason2)
	}

	state := holder.Current()
	if state.Kind != "cancelNight" {
		t.Fatalf("holder kind = %q, want cancelNight", state.Kind)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := engine.snapshot()
		startCount := 0
		for _, c := range calls {
			if c.kind == "start" {
				startCount++
			}
		}
		if startCount >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine Start calls = %d after 5s, want 2 (the cancel alert must replace the delay alert)", startCount)
		}
		time.Sleep(time.Millisecond)
	}

	// Polled, so a release that reaches the engine just after the
	// cancelNight start returned still counts.
	var live []audio.EngineHandle
	releaseDeadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		live, err = engine.LiveHandles(context.Background())
		if err != nil {
			t.Fatalf("LiveHandles: %v", err)
		}
		if len(live) == 1 {
			break
		}
		if time.Now().After(releaseDeadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Contains, not HasSuffix: every handle now carries a unique,
	// unpredictable per-manager sequence number after its item id (see
	// audio.Session.engineHandleFor), so "cancelNight-0" is a middle
	// segment, never the handle's own trailing text.
	if len(live) != 1 || !strings.Contains(string(live[0]), "/cancelNight-0/") {
		t.Fatalf("live engine handles = %v, want only the cancel alert (the delay alert released)", live)
	}
}

// TestWeatherDelayStateMessageKindChangeReplacesTheAlert proves the same
// change-in-place over the retained state topic path (SetFromMessage +
// react), not just the direct weatherdelay.start operation: StartedAt
// stays the delay's original start, and the alert switches.
func TestWeatherDelayStateMessageKindChangeReplacesTheAlert(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	delayHash := writeAssetFixture(t, dir, "delay.wav", []byte("delay alert audio"))
	cancelHash := writeAssetFixture(t, dir, "cancel.wav", []byte("cancel alert audio"))
	plan := mqttproto.WeatherDelayPlan{
		RepeatCount: 10,
		Delay:       &mqttproto.WeatherDelayAlertAssetRef{AssetID: "delay-asset", ContentHash: delayHash, Filename: "delay.wav"},
		CancelNight: &mqttproto.WeatherDelayAlertAssetRef{AssetID: "cancel-asset", ContentHash: cancelHash, Filename: "cancel.wav"},
	}

	holder := NewWeatherDelayHolder(dir, discardLogger())
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	startMsg, err := mqttproto.NewWeatherDelayMessage(true, "delay", startedAt, "op-1", 1, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(delay): %v", err)
	}
	if transition := holder.SetFromMessage(startMsg); transition != weatherDelayStarted {
		t.Fatalf("first message transition = %v, want weatherDelayStarted", transition)
	}
	ops.react(context.Background(), weatherDelayStarted, "delay")

	changeMsg, err := mqttproto.NewWeatherDelayMessage(true, "cancelNight", startedAt, "op-2", 2, plan, startedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(cancelNight): %v", err)
	}
	transition := holder.SetFromMessage(changeMsg)
	if transition != weatherDelayKindChanged {
		t.Fatalf("kind-change transition = %v, want weatherDelayKindChanged", transition)
	}
	ops.react(context.Background(), transition, "cancelNight")

	state := holder.Current()
	if state.Kind != "cancelNight" {
		t.Fatalf("holder kind = %q, want cancelNight", state.Kind)
	}
	if !state.StartedAt.Equal(startedAt) {
		t.Fatalf("holder startedAt = %v, want unchanged %v", state.StartedAt, startedAt)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		calls := engine.snapshot()
		startCount := 0
		for _, c := range calls {
			if c.kind == "start" {
				startCount++
			}
		}
		if startCount >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine Start calls = %d after 5s, want 2 (the cancel alert must replace the delay alert)", startCount)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWeatherDelayStartWithNoConfiguredAssetStillZeroesAndSilences proves a
// plan with no alert still mutes and stops other sessions, and reports why
// no alert played rather than failing.
func TestWeatherDelayStartWithNoConfiguredAssetStillZeroesAndSilences(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)

	const other = pkgaudio.SessionID("other-session")
	otherRef := writeTestWAV(t, dir, "other.wav", "other-asset")
	t.Cleanup(weatherDelayBackground.Wait)
	mgr.Apply(context.Background(), other, "apply-other", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(otherRef)})
	mgr.Start(context.Background(), other, "start-other", 2)

	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	// No plan asset configured at all (zero value plan).

	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	played, reason, _ := ops.doStart(context.Background(), "delay", clock.now())
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

// TestWeatherDelayStartOverTwoPathsDoesNotRestartTheAlert proves a start
// arriving over MQTT and again over HTTP, each with its own operations
// value as agent.go wires them, plays the alert once.
func TestWeatherDelayStartOverTwoPathsDoesNotRestartTheAlert(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)

	mqttPath := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	httpPath := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	if played, reason, _ := mqttPath.doStart(context.Background(), "delay", clock.now()); !played {
		t.Fatalf("first start did not play: %s", reason)
	}
	if played, reason, _ := httpPath.doStart(context.Background(), "delay", clock.now()); !played {
		t.Fatalf("second start reported not playing: %s", reason)
	}

	startCount := 0
	for _, c := range engine.snapshot() {
		if c.kind == "start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("engine Start calls = %d, want 1", startCount)
	}
}

// TestWeatherDelayConcurrentStartsPlayTheAlertOnce proves starts that
// arrive at the same moment over parallel paths play the alert once.
func TestWeatherDelayConcurrentStartsPlayTheAlertOnce(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	var wg sync.WaitGroup
	gate := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, _, _ = ops.doStart(context.Background(), "delay", clock.now())
		}()
	}
	close(gate)
	wg.Wait()

	startCount := 0
	for _, c := range engine.snapshot() {
		if c.kind == "start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("engine Start calls = %d, want 1", startCount)
	}
}

// TestWeatherDelayRepeatedStartAfterAnEmergencySilenceReportsNotPlaying
// proves the reported alertPlaying comes from the session and not from the
// kind alone: an emergency stop silences the alert during a delay, and a
// repeated start of the same kind reports the truth rather than claiming
// the alert it will not restart is still playing.
func TestWeatherDelayRepeatedStartAfterAnEmergencySilenceReportsNotPlaying(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	if played, reason, _ := ops.doStart(context.Background(), "delay", clock.now()); !played {
		t.Fatalf("delay start did not play: %s", reason)
	}
	mgr.SilenceAll(context.Background())

	played, _, _ := ops.doStart(context.Background(), "delay", clock.now())
	if played {
		t.Fatal("a repeated start reported the alert playing after an emergency stop had silenced it")
	}
}

// TestWeatherDelaySecondAlertOfTheNightRestartsFromItemZero proves a second
// delay in one night plays its full repeat count: each alert starts on a
// session that has never played.
func TestWeatherDelaySecondAlertOfTheNightRestartsFromItemZero(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManagerWithDecoder(t, dir, clock, weatherDelayDurationDecoder{})

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 5)
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	if played, reason, _ := ops.doStart(context.Background(), "delay", clock.now()); !played {
		t.Fatalf("first delay start did not play: %s", reason)
	}

	itemIndex := func() int {
		for _, s := range mgr.Snapshot(context.Background()) {
			if s.ID == weatherDelayAlertSessionID {
				return s.ItemIndex
			}
		}
		return -1
	}
	advanceAlertToItem(t, mgr, clock, itemIndex, 2)

	ops.doResume(context.Background())
	if played, reason, _ := ops.doStart(context.Background(), "delay", clock.now()); !played {
		t.Fatalf("second delay start did not play: %s", reason)
	}
	if got := itemIndex(); got != 0 {
		t.Fatalf("the second delay alert of the night starts at item %d, want 0 (it must play all 5 repeats, not the %d left over)", got, 5-got)
	}
}

// advanceAlertToItem drives the item watcher until index reports want.
func advanceAlertToItem(t *testing.T, mgr *audio.Manager, clock *fakeClock, index func() int, want int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); mgr.RunWatcher(ctx, ticks) }()
	deadline := time.Now().Add(5 * time.Second)
	for index() < want && time.Now().Before(deadline) {
		clock.advance(weatherDelayTestItemDuration + time.Second)
		select {
		case ticks <- clock.now():
		case <-time.After(time.Second):
		}
	}
	cancel()
	<-done
	if got := index(); got < want {
		t.Fatalf("the alert never reached item %d (last %d)", want, got)
	}
}

// TestWeatherDelayStartOperationPersistsThePlanCarriedInItsOwnParams proves
// a node that never received the retained state topic still knows what to
// play, because weatherdelay.start's own command carries the plan too.
func TestWeatherDelayStartOperationPersistsThePlanCarriedInItsOwnParams(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)
	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	plan := weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 3)

	// A fresh holder has never seen the retained topic or any prior
	// command: its plan starts empty.
	holder := NewWeatherDelayHolder(dir, discardLogger())
	if holder.Current().Plan.Delay != nil {
		t.Fatal("a fresh holder already has a plan; test setup is wrong")
	}
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	// params arrives as it would over the wire: a JSON round trip through
	// a generic map, not the Go struct directly.
	planParam := jsonRoundTrip(t, plan)
	result, err := ops.start(context.Background(), map[string]any{"kind": "delay", "plan": planParam}, clock.now)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	value, ok := result.Value.(map[string]any)
	if !ok || !value["alertPlaying"].(bool) {
		t.Fatalf("Value = %+v, want alertPlaying true; the plan carried in params should have been enough to play", result.Value)
	}

	if holder.Current().Plan.Delay == nil || holder.Current().Plan.Delay.AssetID != "alert-asset" {
		t.Fatalf("holder plan after start = %+v, want it persisted from params.plan", holder.Current().Plan)
	}

	startCount := 0
	for _, c := range engine.snapshot() {
		if c.kind == "start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("engine Start calls = %d, want 1", startCount)
	}
}

// TestWeatherDelayStartOperationRejectsAMalformedPlanParam proves a plan
// that fails validation refuses the operation rather than silently
// dropping it.
func TestWeatherDelayStartOperationRejectsAMalformedPlanParam(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)
	holder := NewWeatherDelayHolder(dir, discardLogger())
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	_, err := ops.start(context.Background(), map[string]any{
		"kind": "delay",
		"plan": map[string]any{"delay": map[string]any{"assetId": "a"}}, // missing contentHash/filename
	}, clock.now)
	if err == nil {
		t.Fatal("start with an invalid plan param succeeded, want a refusal")
	}
}

// jsonRoundTrip encodes v to JSON and decodes it back into a generic
// map[string]any, matching how a command envelope's params actually
// arrive over the wire.
func jsonRoundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestWeatherDelayStartOperationKeepsItsPlanWhenTheCommandCarriesAnEmptyOne
// proves the coordinator failing to build a plan does not cost this node
// the plan it already had. The coordinator sends params.plan on every
// start, including when it could not build one, and an empty plan means
// "I do not know", never "there is no alert".
func TestWeatherDelayStartOperationKeepsItsPlanWhenTheCommandCarriesAnEmptyOne(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)
	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	plan := weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 3)

	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetPlanLocal(plan); err != nil {
		t.Fatalf("SetPlanLocal: %v", err)
	}
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	result, err := ops.start(context.Background(), map[string]any{
		"kind": "delay",
		"plan": jsonRoundTrip(t, mqttproto.WeatherDelayPlan{}),
	}, clock.now)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if holder.Current().Plan.Delay == nil || holder.Current().Plan.Delay.AssetID != "alert-asset" {
		t.Fatalf("holder plan after an empty plan param = %+v, want the plan this node already had", holder.Current().Plan)
	}
	value, ok := result.Value.(map[string]any)
	if !ok || !value["alertPlaying"].(bool) {
		t.Fatalf("Value = %+v, want alertPlaying true: the node's own plan should still have played", result.Value)
	}
	startCount := 0
	for _, c := range engine.snapshot() {
		if c.kind == "start" {
			startCount++
		}
	}
	if startCount != 1 {
		t.Fatalf("engine Start calls = %d, want 1", startCount)
	}
}

// TestWeatherDelayStartOperationRefusesAPlanNamingAFileOutsideTheAssetDir
// proves a plan arriving in the command cannot name a file outside this
// node's asset directory.
func TestWeatherDelayStartOperationRefusesAPlanNamingAFileOutsideTheAssetDir(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManager(t, dir, clock)
	holder := NewWeatherDelayHolder(dir, discardLogger())
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}

	for _, filename := range []string{"../../etc/passwd", "/etc/passwd", "sub/alert.wav"} {
		_, err := ops.start(context.Background(), map[string]any{
			"kind": "delay",
			"plan": map[string]any{"delay": map[string]any{
				"assetId": "a", "contentHash": "h", "filename": filename,
			}},
		}, clock.now)
		if err == nil {
			t.Fatalf("start with filename %q succeeded, want a refusal", filename)
		}
	}
}
