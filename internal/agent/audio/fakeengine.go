package audio

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// FakeEngineUnavailableReason is [FakeEngine.Available]'s constant reason.
// It names the open owner decision this engine stands in for, so anything
// that surfaces the reason to an operator points at the actual blocker
// rather than an unexplained "false".
const FakeEngineUnavailableReason = "no pipeline backend is implemented (Linear SM-68 is open); FakeEngine never plays audio"

// FakeEngine is a deterministic, in-memory [Engine] with no real playback:
// position is computed from an injected clock, and Available always
// reports false. It exists to prove the session state machine's behavior
// against a substitutable Engine — never to be reported, logged, or
// tested as a working audio engine. Nothing built against FakeEngine may
// claim otherwise.
type FakeEngine struct {
	now func() time.Time

	mu       sync.Mutex
	handles  map[EngineHandle]*fakeHandle
	failNext map[EngineHandle]error
}

type fakeHandle struct {
	media    pkgaudio.MediaRef
	duration time.Duration
	state    pkgaudio.State
	// position is the playback position as of the last state change;
	// while playing, the true current position is position plus elapsed
	// time since playStartedAt, computed lazily on read.
	position      time.Duration
	playStartedAt time.Time // zero when not currently playing

	// gain is the handle's output gain as of the last resolved change;
	// while fade is non-nil, the true current gain is interpolated
	// lazily on read exactly as position is derived from playStartedAt.
	gain pkgaudio.Gain
	fade *fakeFade
}

// fakeFade is one in-progress [Engine.Fade] ramp: linear interpolation
// between the gain the fade started at and its target, over its
// duration, timed against the engine's own injected clock.
type fakeFade struct {
	startGain  pkgaudio.Gain
	targetGain pkgaudio.Gain
	duration   time.Duration
	startedAt  time.Time
}

// NewFakeEngine returns a FakeEngine using now for its internal clock.
func NewFakeEngine(now func() time.Time) *FakeEngine {
	return &FakeEngine{now: now, handles: make(map[EngineHandle]*fakeHandle), failNext: make(map[EngineHandle]error)}
}

// InjectFailure arms handle so its next call to Load, Start, Pause,
// Resume, Seek, Stop, Release, or Observe returns err instead of running
// normally, then
// disarms itself — a one-shot fault, not a standing one, so a test
// controls exactly which call fails. There is no real backend to produce
// a pipeline crash, a freeze, a route change, or timing-authority loss
// (AUDIO-ENGINE section 11.4); this is how a test exercises the session
// layer's classification of those four against something that can never
// happen on its own here. err should wrap one of this package's
// ErrEngine* sentinels ([pkgaudio.ClassifyFault]) for a test to assert a
// specific fault class, or an unwrapped error to exercise [pkgaudio.
// FaultOther].
func (e *FakeEngine) InjectFailure(handle EngineHandle, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failNext[handle] = err
}

// takeFailure returns and clears handle's armed failure, if any. Callers
// hold e.mu.
func (e *FakeEngine) takeFailure(handle EngineHandle) error {
	err, ok := e.failNext[handle]
	if !ok {
		return nil
	}
	delete(e.failNext, handle)
	return err
}

// Available always reports false with [FakeEngineUnavailableReason].
func (e *FakeEngine) Available() (bool, string) {
	return false, FakeEngineUnavailableReason
}

func (e *FakeEngine) obs(h *fakeHandle) EngineObservation {
	position := e.currentPosition(h)
	gain := e.currentGain(h)
	return EngineObservation{
		State: h.state, Position: position, ObservedAt: e.now(),
		Gain: gain, FadeActive: h.fade != nil,
	}
}

// currentGain resolves h.fade to h.gain and clears it, exactly once, the
// instant the ramp's elapsed wall time reaches its duration — never
// before, and never inferred by the caller from duration alone. Callers
// hold e.mu.
func (e *FakeEngine) currentGain(h *fakeHandle) pkgaudio.Gain {
	if h.fade == nil {
		return h.gain
	}
	elapsed := e.now().Sub(h.fade.startedAt)
	if elapsed >= h.fade.duration {
		h.gain = h.fade.targetGain
		h.fade = nil
		return h.gain
	}
	frac := float64(elapsed) / float64(h.fade.duration)
	span := float64(h.fade.targetGain) - float64(h.fade.startGain)
	return pkgaudio.Gain(float64(h.fade.startGain) + span*frac)
}

// currentPosition advances h.state to Completed, and clamps position to
// h.duration, when a Playing handle's elapsed wall time has reached its
// known duration. Callers holding e.mu.
func (e *FakeEngine) currentPosition(h *fakeHandle) time.Duration {
	if h.state != pkgaudio.StatePlaying || h.playStartedAt.IsZero() {
		return h.position
	}
	elapsed := h.position + e.now().Sub(h.playStartedAt)
	if h.duration > 0 && elapsed >= h.duration {
		h.position = h.duration
		h.state = pkgaudio.StateCompleted
		h.playStartedAt = time.Time{}
		return h.duration
	}
	return elapsed
}

func (e *FakeEngine) Load(_ context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h := &fakeHandle{media: media, duration: duration, state: pkgaudio.StateReady, gain: 1}
	e.handles[handle] = h
	return e.obs(h), nil
}

func (e *FakeEngine) get(handle EngineHandle) (*fakeHandle, error) {
	h, ok := e.handles[handle]
	if !ok {
		return nil, fmt.Errorf("audio: fake engine has no loaded handle %q", handle)
	}
	return h, nil
}

func (e *FakeEngine) Start(_ context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = position
	h.state = pkgaudio.StatePlaying
	h.playStartedAt = e.now()
	return e.obs(h), nil
}

func (e *FakeEngine) Pause(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = e.currentPosition(h)
	if h.state == pkgaudio.StatePlaying {
		h.state = pkgaudio.StatePaused
	}
	h.playStartedAt = time.Time{}
	return e.obs(h), nil
}

func (e *FakeEngine) Resume(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if h.state != pkgaudio.StatePaused {
		return EngineObservation{}, fmt.Errorf("audio: fake engine cannot resume handle %q from state %q", handle, h.state)
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.state = pkgaudio.StatePlaying
	h.playStartedAt = e.now()
	return e.obs(h), nil
}

func (e *FakeEngine) Seek(_ context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.position = position
	if h.state == pkgaudio.StatePlaying {
		h.playStartedAt = e.now()
	}
	return e.obs(h), nil
}

func (e *FakeEngine) Stop(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	h.state = pkgaudio.StateStopped
	h.playStartedAt = time.Time{}
	return e.obs(h), nil
}

func (e *FakeEngine) SetGain(_ context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	h.gain = gain
	h.fade = nil
	return e.obs(h), nil
}

func (e *FakeEngine) Fade(_ context.Context, handle EngineHandle, fade pkgaudio.Fade) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	start := e.currentGain(h)
	h.fade = &fakeFade{startGain: start, targetGain: fade.TargetGain, duration: fade.Duration, startedAt: e.now()}
	return e.obs(h), nil
}

func (e *FakeEngine) Release(_ context.Context, handle EngineHandle) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.takeFailure(handle); err != nil {
		return err
	}
	delete(e.handles, handle)
	return nil
}

func (e *FakeEngine) Observe(_ context.Context, handle EngineHandle) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := e.get(handle)
	if err != nil {
		return EngineObservation{}, err
	}
	if err := e.takeFailure(handle); err != nil {
		return EngineObservation{}, err
	}
	return e.obs(h), nil
}
