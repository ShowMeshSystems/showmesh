package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// weatherDelayFixture is one node with a real audio manager, a holder that
// knows an alert plan, and one other session already playing.
type weatherDelayFixture struct {
	dir    string
	clock  *fakeClock
	mgr    *audio.Manager
	engine *weatherDelayRecordingEngine
	holder *WeatherDelayHolder
	ops    *weatherDelayOperations
	plan   mqttproto.WeatherDelayPlan
}

const weatherDelayOtherSession = pkgaudio.SessionID("other-session")

func newWeatherDelayFixture(t *testing.T) *weatherDelayFixture {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)
	startOtherSession(t, mgr, dir)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	plan := weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)
	holder := NewWeatherDelayHolder(dir, discardLogger())
	holder.SetFromMessage(mqttproto.WeatherDelayMessage{Active: false, Revision: 5, Plan: plan})
	return &weatherDelayFixture{
		dir: dir, clock: clock, mgr: mgr, engine: engine, holder: holder, plan: plan,
		ops: &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir},
	}
}

func startOtherSession(t *testing.T, mgr *audio.Manager, dir string) {
	t.Helper()
	t.Cleanup(weatherDelayBackground.Wait)
	ref := writeTestWAV(t, dir, "other.wav", "other-asset")
	if r := mgr.Apply(context.Background(), weatherDelayOtherSession, "apply-other", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply other: %+v", r)
	}
	if r := mgr.Start(context.Background(), weatherDelayOtherSession, "start-other", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start other: %+v", r)
	}
}

// deliver applies a retained state message the way registerWeatherDelay
// does, but waits for the audio side to finish.
func (f *weatherDelayFixture) deliver(active bool, revision int64) weatherDelayTransition {
	msg := mqttproto.WeatherDelayMessage{Active: active, Revision: revision, Plan: f.plan}
	if active {
		msg.Kind = "delay"
	}
	transition := f.holder.SetFromMessage(msg)
	f.ops.react(context.Background(), transition, msg.Kind)
	return transition
}

func (f *weatherDelayFixture) sessionState(id pkgaudio.SessionID) pkgaudio.State {
	for _, s := range f.mgr.Snapshot(context.Background()) {
		if s.ID == id {
			return s.State
		}
	}
	return ""
}

func (f *weatherDelayFixture) alertStarts() int {
	n := 0
	for _, c := range f.engine.snapshot() {
		if c.kind == "start" && strings.HasPrefix(string(c.handle), string(weatherDelayAlertSessionID)) {
			n++
		}
	}
	return n
}

// wedgedGainEngine never returns from SetGain for the other session once
// wedged is set, and records when the alert reached the engine.
type wedgedGainEngine struct {
	*weatherDelayRecordingEngine
	wedged       *atomic.Bool
	alertStarted chan time.Time
}

func (e wedgedGainEngine) Start(ctx context.Context, handle audio.EngineHandle, position time.Duration) (audio.EngineObservation, error) {
	if strings.HasPrefix(string(handle), string(weatherDelayAlertSessionID)) {
		select {
		case e.alertStarted <- time.Now():
		default:
		}
	}
	return e.weatherDelayRecordingEngine.Start(ctx, handle, position)
}

func (e wedgedGainEngine) SetGain(ctx context.Context, handle audio.EngineHandle, gain pkgaudio.Gain) (audio.EngineObservation, error) {
	if e.wedged.Load() && strings.HasPrefix(string(handle), string(weatherDelayOtherSession)) {
		<-ctx.Done()
		return audio.EngineObservation{}, ctx.Err()
	}
	return e.weatherDelayRecordingEngine.SetGain(ctx, handle, gain)
}

func TestWeatherDelayWedgedSessionDelaysTheAlertNoMoreThanTheMuteBound(t *testing.T) {
	prev := weatherDelayMuteBound
	weatherDelayMuteBound = 100 * time.Millisecond
	t.Cleanup(func() { weatherDelayMuteBound = prev })

	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	fake := audio.NewFakeEngine(clock.now)
	recording := &weatherDelayRecordingEngine{activationAvailableEngine: activationAvailableEngine{fake}}
	wedged := &atomic.Bool{}
	alertStarted := make(chan time.Time, 1)
	mgr := audio.NewManager(wedgedGainEngine{recording, wedged, alertStarted}, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, clock.now, nil)
	startOtherSession(t, mgr, dir)

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	holder := NewWeatherDelayHolder(dir, discardLogger())
	holder.SetFromMessage(mqttproto.WeatherDelayMessage{Plan: weatherDelayTestPlan("delay", "alert-asset", hash, "alert.wav", 10)})
	ops := &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}
	wedged.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	began := time.Now()
	played, reason, unsilenced := ops.doStart(ctx, "delay", clock.now())

	if !played {
		t.Fatalf("alert did not play: %s", reason)
	}
	if delay := (<-alertStarted).Sub(began); delay > weatherDelayMuteBound+400*time.Millisecond {
		t.Fatalf("alert reached the engine %v after the start with a wedged session, want about the %v mute bound", delay, weatherDelayMuteBound)
	}
	want := "Audio session other-session could not be silenced before the alert started."
	if len(unsilenced) != 1 || unsilenced[0] != want {
		t.Fatalf("unsilenced = %q, want [%q]", unsilenced, want)
	}
}

func TestWeatherDelayStaleNotActiveMessageDoesNotClearAnHTTPStartedDelay(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if played, reason, _ := f.ops.doStart(context.Background(), "delay", f.clock.now()); !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	if got := f.deliver(false, 5); got != weatherDelayUnchanged {
		t.Fatalf("transition = %v, want unchanged", got)
	}
	if !f.holder.Current().Active {
		t.Fatal("a not-active message no newer than the held revision cleared the delay")
	}
	if got := f.sessionState(weatherDelayAlertSessionID); got != pkgaudio.StatePlaying {
		t.Fatalf("alert state = %q, want playing", got)
	}
}

func TestWeatherDelayNewerNotActiveMessageClearsAndStopsTheAlert(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if played, reason, _ := f.ops.doStart(context.Background(), "delay", f.clock.now()); !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	if got := f.deliver(false, 6); got != weatherDelayCleared {
		t.Fatalf("transition = %v, want cleared", got)
	}
	if f.holder.Current().Active {
		t.Fatal("a newer not-active message did not clear the delay")
	}
	if got := f.sessionState(weatherDelayAlertSessionID); got == pkgaudio.StatePlaying {
		t.Fatal("alert still playing after a newer not-active message")
	}
}

func TestWeatherDelayResumeClearsWhateverRevisionIsHeld(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if got := f.deliver(true, 9); got != weatherDelayStarted {
		t.Fatalf("transition = %v, want started", got)
	}
	if _, err := f.ops.resume(context.Background(), map[string]any{}, f.clock.now); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if f.holder.Current().Active {
		t.Fatal("weatherdelay.resume did not clear the delay")
	}
	if got := f.sessionState(weatherDelayAlertSessionID); got == pkgaudio.StatePlaying {
		t.Fatal("alert still playing after weatherdelay.resume")
	}
}

func TestWeatherDelayResumeWithNoAlertSessionSucceeds(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if _, err := f.ops.resume(context.Background(), map[string]any{}, f.clock.now); err != nil {
		t.Fatalf("resume with no alert session: %v", err)
	}
}

func TestWeatherDelayRetainedActiveMessageRunsTheStartSequence(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if got := f.deliver(true, 6); got != weatherDelayStarted {
		t.Fatalf("transition = %v, want started", got)
	}
	if !f.holder.Current().Active {
		t.Fatal("holder not active after a retained active message")
	}
	if got := f.sessionState(weatherDelayAlertSessionID); got != pkgaudio.StatePlaying {
		t.Fatalf("alert state = %q, want playing", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.sessionState(weatherDelayOtherSession) != pkgaudio.StateStopped {
		if time.Now().After(deadline) {
			t.Fatal("the other session was never stopped after a retained active message")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWeatherDelayRetainedActiveWhileActiveDoesNotRestartTheAlert(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if played, reason, _ := f.ops.doStart(context.Background(), "delay", f.clock.now()); !played {
		t.Fatalf("alert did not play: %s", reason)
	}
	if got := f.deliver(true, 6); got != weatherDelayUnchanged {
		t.Fatalf("transition = %v, want unchanged", got)
	}
	if n := f.alertStarts(); n != 1 {
		t.Fatalf("alert engine starts = %d, want 1", n)
	}
}

func TestWeatherDelayStateFileSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal("delay", time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, weatherDelayStateSubdir))
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != weatherDelayStateFile {
		t.Fatalf("state dir holds %v, want only %s", entries, weatherDelayStateFile)
	}
}

func TestWeatherDelayUnreadableStateFileLoadsAsNotActive(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, weatherDelayStateSubdir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, weatherDelayStateFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if NewWeatherDelayHolder(dir, discardLogger()).Current().Active {
		t.Fatal("an unreadable state file loaded as active")
	}
}

func TestBootDuringADelayMarksPersistedSessionsStoppedOnDisk(t *testing.T) {
	f := newWeatherDelayFixture(t)

	duringDelay, _ := newWeatherDelayTestManager(t, f.dir, f.clock)
	restoreAudioSessionsAtBoot(context.Background(), duringDelay, true, discardLogger())
	assertNotPlayingAfterRestore(t, duringDelay, weatherDelayOtherSession)

	nextBoot, _ := newWeatherDelayTestManager(t, f.dir, f.clock)
	restoreAudioSessionsAtBoot(context.Background(), nextBoot, false, discardLogger())
	assertNotPlayingAfterRestore(t, nextBoot, weatherDelayOtherSession)
}

func TestBootNeverRestoresTheAlertAsPlaying(t *testing.T) {
	f := newWeatherDelayFixture(t)
	if played, reason, _ := f.ops.doStart(context.Background(), "delay", f.clock.now()); !played {
		t.Fatalf("alert did not play: %s", reason)
	}

	rebooted, _ := newWeatherDelayTestManager(t, f.dir, f.clock)
	restoreAudioSessionsAtBoot(context.Background(), rebooted, false, discardLogger())
	assertNotPlayingAfterRestore(t, rebooted, weatherDelayAlertSessionID)

	ops := &weatherDelayOperations{holder: f.holder, audioMgr: rebooted, assetDir: f.dir}
	if _, err := ops.resume(context.Background(), map[string]any{}, f.clock.now); err != nil {
		t.Fatalf("resume after a restore: %v", err)
	}
}

func assertNotPlayingAfterRestore(t *testing.T, mgr *audio.Manager, id pkgaudio.SessionID) {
	t.Helper()
	for _, s := range mgr.Snapshot(context.Background()) {
		if s.ID == id && s.State == pkgaudio.StatePlaying {
			t.Fatalf("session %s restored as playing", id)
		}
	}
}

func TestRenderTimelineIgnoresMultiSyncDuringADelay(t *testing.T) {
	holder := NewWeatherDelayHolder(t.TempDir(), discardLogger())
	timeline := multisync.NewTimeline(time.Now, multisync.Config{})
	ops := &renderOperations{timeline: timeline, weatherDelay: holder}
	timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionStart, Filename: "show.fseq"}, "test")
	timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionSync, FrameNumber: 100, SecondsElapsed: 2.5, Filename: "show.fseq"}, "test")

	if got := ops.frameTimeline().Snapshot().State; got != multisync.StatePlaying {
		t.Fatalf("state with no delay = %q, want playing", got)
	}
	if err := holder.SetActiveLocal("delay", time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}
	if got := ops.frameTimeline().Snapshot().State; got != multisync.StateStopped {
		t.Fatalf("state during a delay = %q, want stopped", got)
	}
	if err := holder.ClearLocal(); err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}
	if got := ops.frameTimeline().Snapshot().State; got != multisync.StatePlaying {
		t.Fatalf("state after the delay = %q, want playing", got)
	}
}
