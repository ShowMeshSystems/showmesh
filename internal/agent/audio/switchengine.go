package audio

import (
	"context"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SwitchableEngineNoBindingReason is [SwitchableEngine.Available]'s
// reason before [SwitchableEngine.Set] is ever called: a node that has
// never received an audio.node configuration must not claim it can play.
const SwitchableEngineNoBindingReason = "no audio.node configuration has been delivered to this node yet; nothing plays audio"

// SwitchableEngine is an [Engine] whose backing implementation can be
// replaced at runtime, so a [Manager] built once at agent startup can be
// bound to a real engine once an audio.node configuration is delivered,
// and rebound again whenever that configuration changes, without
// restarting the agent (ADR-036). Every call delegates to whatever
// engine is currently set; before the first [SwitchableEngine.Set],
// Available reports [SwitchableEngineNoBindingReason] and every other
// method fails with the same reason.
type SwitchableEngine struct {
	mu      sync.RWMutex
	current Engine
}

// NewSwitchableEngine returns a SwitchableEngine with no backing engine
// set.
func NewSwitchableEngine() *SwitchableEngine {
	return &SwitchableEngine{}
}

// Set replaces the backing engine and returns the one it replaced, or
// nil when nothing was set. The caller is responsible for invalidating
// any session state referring to handles on the previous engine before
// calling Set, and for releasing the returned engine's own resources:
// an outgoing engine holding an output device keeps holding it until
// something closes it. See [Manager.RebindEngine], which does the
// invalidation and hands the previous engine back.
func (e *SwitchableEngine) Set(engine Engine) Engine {
	e.mu.Lock()
	prev := e.current
	e.current = engine
	e.mu.Unlock()
	return prev
}

func (e *SwitchableEngine) get() (Engine, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current, e.current != nil
}

// Available reports the currently set engine's own availability, or
// false with [SwitchableEngineNoBindingReason] when nothing has ever
// been set.
func (e *SwitchableEngine) Available() (bool, string) {
	cur, ok := e.get()
	if !ok {
		return false, SwitchableEngineNoBindingReason
	}
	return cur.Available()
}

func (e *SwitchableEngine) errNoBinding() error {
	return fmt.Errorf("audio: %s", SwitchableEngineNoBindingReason)
}

func (e *SwitchableEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Load(ctx, handle, media, duration)
}

func (e *SwitchableEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Start(ctx, handle, position)
}

func (e *SwitchableEngine) Pause(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Pause(ctx, handle)
}

func (e *SwitchableEngine) Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Resume(ctx, handle)
}

func (e *SwitchableEngine) Seek(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Seek(ctx, handle, position)
}

func (e *SwitchableEngine) Stop(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Stop(ctx, handle)
}

func (e *SwitchableEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.SetGain(ctx, handle, gain)
}

func (e *SwitchableEngine) Fade(ctx context.Context, handle EngineHandle, fade pkgaudio.Fade) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Fade(ctx, handle, fade)
}

func (e *SwitchableEngine) Release(ctx context.Context, handle EngineHandle) error {
	cur, ok := e.get()
	if !ok {
		// Release is idempotent on an unknown handle per Engine's own
		// contract; a never-bound engine has never loaded any handle
		// either, so there is nothing to release.
		return nil
	}
	return cur.Release(ctx, handle)
}

func (e *SwitchableEngine) Observe(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		return EngineObservation{}, e.errNoBinding()
	}
	return cur.Observe(ctx, handle)
}
