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

	mu      sync.Mutex
	handles map[EngineHandle]*fakeHandle
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
}

// NewFakeEngine returns a FakeEngine using now for its internal clock.
func NewFakeEngine(now func() time.Time) *FakeEngine {
	return &FakeEngine{now: now, handles: make(map[EngineHandle]*fakeHandle)}
}

// Available always reports false with [FakeEngineUnavailableReason].
func (e *FakeEngine) Available() (bool, string) {
	return false, FakeEngineUnavailableReason
}

func (e *FakeEngine) obs(h *fakeHandle) EngineObservation {
	return EngineObservation{State: h.state, Position: e.currentPosition(h), ObservedAt: e.now()}
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
	h := &fakeHandle{media: media, duration: duration, state: pkgaudio.StateReady}
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
	h.state = pkgaudio.StateStopped
	h.playStartedAt = time.Time{}
	return e.obs(h), nil
}

func (e *FakeEngine) Release(_ context.Context, handle EngineHandle) error {
	e.mu.Lock()
	defer e.mu.Unlock()
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
	return e.obs(h), nil
}
