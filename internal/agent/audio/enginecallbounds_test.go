package audio

import (
	"context"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// hangMethod names one [hangingCallEngine] method to wedge.
type hangMethod string

const (
	hangStartLTC hangMethod = "StartLTC"
	hangStopLTC  hangMethod = "StopLTC"
	hangSetGain  hangMethod = "SetGain"
	hangPause    hangMethod = "Pause"
	hangResume   hangMethod = "Resume"
	hangStart    hangMethod = "Start"
	hangStop     hangMethod = "Stop"
)

// hangingCallEngine wraps [FakeEngine] and makes ONE armed method (against
// one handle, or every handle for the two handle-less LTC calls) block
// until its own context is done -- these tests all rely on the bounded
// context under test to unblock the wedge, never a manual release, the
// general form of [hangingStartEngine]/[hangingLoadEngine], covering
// every other engine call the review found still running under an
// unbounded context:
// StartLTC/StopLTC (ltclifecycle.go), SetGain (mix.go, via
// removeDuckerLocked), and Pause/Resume/Start (interrupt.go, via
// restoreInterrupted).
type hangingCallEngine struct {
	*FakeEngine

	mu      sync.Mutex
	method  hangMethod
	handle  EngineHandle // "" matches every handle -- used for the handle-less LTC calls.
	release chan struct{}
}

func newHangingCallEngine(now func() time.Time) *hangingCallEngine {
	return &hangingCallEngine{FakeEngine: NewFakeEngine(now), release: make(chan struct{})}
}

func (e *hangingCallEngine) arm(method hangMethod, handle EngineHandle) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.method, e.handle = method, handle
}

func (e *hangingCallEngine) shouldHang(method hangMethod, handle EngineHandle) (chan struct{}, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	hang := e.method == method && (e.handle == "" || e.handle == handle)
	return e.release, hang
}

func (e *hangingCallEngine) waitOrDone(ctx context.Context, method hangMethod, handle EngineHandle) error {
	release, hang := e.shouldHang(method, handle)
	if !hang {
		return nil
	}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *hangingCallEngine) StartLTC(ctx context.Context, spec LTCSpec) (LTCObservation, error) {
	if err := e.waitOrDone(ctx, hangStartLTC, ""); err != nil {
		return LTCObservation{}, err
	}
	return e.FakeEngine.StartLTC(ctx, spec)
}

func (e *hangingCallEngine) StopLTC(ctx context.Context) (LTCObservation, error) {
	if err := e.waitOrDone(ctx, hangStopLTC, ""); err != nil {
		return LTCObservation{}, err
	}
	return e.FakeEngine.StopLTC(ctx)
}

func (e *hangingCallEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	if err := e.waitOrDone(ctx, hangSetGain, handle); err != nil {
		return EngineObservation{}, err
	}
	return e.FakeEngine.SetGain(ctx, handle, gain)
}

func (e *hangingCallEngine) Pause(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	if err := e.waitOrDone(ctx, hangPause, handle); err != nil {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Pause(ctx, handle)
}

func (e *hangingCallEngine) Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	if err := e.waitOrDone(ctx, hangResume, handle); err != nil {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Resume(ctx, handle)
}

func (e *hangingCallEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	if err := e.waitOrDone(ctx, hangStart, handle); err != nil {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Start(ctx, handle, position)
}

func (e *hangingCallEngine) Stop(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	if err := e.waitOrDone(ctx, hangStop, handle); err != nil {
		return EngineObservation{}, err
	}
	return e.FakeEngine.Stop(ctx, handle)
}

// callBoundsWaitBound mirrors watcherfreeze_test.go's wedgedWaitBound.
const callBoundsWaitBound = 5 * time.Second

// withShrunkEngineCallTimeout shrinks engineCallTimeout for the duration
// of one test and restores it via t.Cleanup.
func withShrunkEngineCallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := engineCallTimeout
	engineCallTimeout = d
	t.Cleanup(func() { engineCallTimeout = prev })
}

// TestStartLTCLockedBoundsAWedgedGenerator proves startLTCLocked's own
// gen.StartLTC call is bounded, not run under whatever unbounded ctx its
// caller (advanceLocked, Manager.Start, Manager.Resume, ...) was given.
func TestStartLTCLockedBoundsAWedgedGenerator(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, ok := m.get("show")
	if !ok {
		t.Fatal("session not created")
	}
	engine.arm(hangStartLTC, "")

	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		m.startLTCLocked(ctx, s, 0)
		s.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("startLTCLocked did not return within a bounded time despite a wedged StartLTC call")
	}
}

// TestStopLTCLockedBoundsAWedgedGenerator is TestStartLTCLockedBoundsAWedgedGenerator's
// counterpart for gen.StopLTC.
func TestStopLTCLockedBoundsAWedgedGenerator(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	configureLTC(m, pkgaudio.LTCFrameRate30, "00:00:00:00")
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "show.wav", "asset-show", []byte("show"))
	startPlaying(t, m, ctx, "show", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	s, ok := m.get("show")
	if !ok {
		t.Fatal("session not created")
	}
	engine.arm(hangStopLTC, "")

	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		m.stopLTCLocked(ctx, s)
		s.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("stopLTCLocked did not return within a bounded time despite a wedged StopLTC call")
	}
}

// TestApplyEffectiveGainLockedBoundsAWedgedSetGain proves
// applyEffectiveGainLocked's engine.SetGain call is bounded -- the single
// choke point both Manager.GainSet/GainFade/mute/duck AND
// removeDuckerLocked's own post-duck-release gain reapply go through.
func TestApplyEffectiveGainLockedBoundsAWedgedSetGain(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", ref, pkgaudio.SourceRoleBackground, pkgaudio.MixPolicyMix)

	s, ok := m.get("bg")
	if !ok {
		t.Fatal("session not created")
	}
	s.mu.Lock()
	handle := s.handle
	s.mu.Unlock()
	engine.arm(hangSetGain, handle)

	done := make(chan struct{})
	go func() {
		s.mu.Lock()
		m.applyEffectiveGainBestEffortLocked(ctx, s)
		s.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("applyEffectiveGainBestEffortLocked did not return within a bounded time despite a wedged SetGain call")
	}
}

// TestInterruptOneLockedBoundsAWedgedPause proves interruptOneLocked's own
// engine.Pause call is bounded -- reached from interruptLowerPriority,
// which runs while holding the LOWER-priority session's own mutex, never
// the interrupter's.
func TestInterruptOneLockedBoundsAWedgedPause(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("session not created")
	}
	bg.mu.Lock()
	handle := bg.handle
	bg.mu.Unlock()
	engine.arm(hangPause, handle)

	done := make(chan struct{})
	go func() {
		bg.mu.Lock()
		m.interruptOneLocked(ctx, bg, "ann")
		bg.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("interruptOneLocked did not return within a bounded time despite a wedged Pause call")
	}
}

// TestRemoveInterrupterLockedBoundsAWedgedResume proves
// removeInterrupterLocked's Resume-path engine.Resume call is bounded --
// reached from restoreInterrupted, which watchTick itself calls for
// every session a completed announcement no longer suspends.
func TestRemoveInterrupterLockedBoundsAWedgedResume(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	ref := writeTestAsset(t, m.assetDir, "bg.wav", "asset-bg", []byte("bg"))
	startPlaying(t, m, ctx, "bg", ref, pkgaudio.SourceRoleShow, pkgaudio.MixPolicyMix)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("session not created")
	}
	bg.mu.Lock()
	m.interruptOneLocked(ctx, bg, "ann") // suspends bg: Paused, handle still loaded.
	handle := bg.handle
	state := bg.state
	bg.mu.Unlock()
	if state != pkgaudio.StatePaused {
		t.Fatalf("precondition: bg state = %q, want paused after interruptOneLocked", state)
	}
	engine.arm(hangResume, handle)

	done := make(chan struct{})
	go func() {
		bg.mu.Lock()
		m.removeInterrupterLocked(ctx, bg, "ann")
		bg.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("removeInterrupterLocked did not return within a bounded time despite a wedged Resume call")
	}
}

// TestRemoveInterrupterLockedBoundsAWedgedStart is
// TestRemoveInterrupterLockedBoundsAWedgedResume's counterpart for the
// stale-handle route: removeInterrupterLocked always tries Engine.Resume
// first — it no longer honors t's own Resume policy for an announcement
// release — so the only way to reach its
// release+prepare+Start path is a handle that went stale while
// suspended, forced here by mutating loadedIdentity directly rather than
// through a real Apply/Start cycle.
func TestRemoveInterrupterLockedBoundsAWedgedStart(t *testing.T) {
	withShrunkEngineCallTimeout(t, 200*time.Millisecond)

	c := newClock(time.Now())
	dir := t.TempDir()
	engine := newHangingCallEngine(c.now)
	m := NewManager(engine, NewFileSessionStore(dir), dir, staticDecoder{duration: 2 * time.Second}, c.now, nil)
	ctx := context.Background()

	playlist := twoItemPlaylist(t, m.assetDir)
	m.Apply(ctx, "bg", "inv-bg-apply", 1, pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
		Playlist:   pkgaudio.SetField(playlist),
	})
	m.Start(ctx, "bg", "inv-bg-start", 2)

	bg, ok := m.get("bg")
	if !ok {
		t.Fatal("session not created")
	}
	bg.mu.Lock()
	state := bg.state
	bg.mu.Unlock()
	if state != pkgaudio.StatePlaying {
		t.Fatalf("precondition: bg not playing: state=%s", state)
	}

	bg.mu.Lock()
	m.interruptOneLocked(ctx, bg, "ann") // suspends bg: Paused, handle still loaded.
	bg.loadedIdentity = "stale-identity" // force removeInterrupterLocked's stale-handle route.
	bg.mu.Unlock()

	nextHandle := bg.engineHandleFor("item-a")
	engine.arm(hangStart, nextHandle)

	done := make(chan struct{})
	go func() {
		bg.mu.Lock()
		m.removeInterrupterLocked(ctx, bg, "ann")
		bg.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(callBoundsWaitBound):
		t.Fatal("removeInterrupterLocked did not return within a bounded time despite a wedged Start call")
	}
}
