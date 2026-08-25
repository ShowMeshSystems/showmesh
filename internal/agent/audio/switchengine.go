package audio

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// SwitchableEngineNoBindingReason is [SwitchableEngine.Available]'s
// reason before [SwitchableEngine.Set] is ever called with a real
// engine: a node that has never received an audio.node configuration
// must not claim it can play. Once a binding HAS been delivered, a later
// detached state reports [SwitchableEngineRebindInProgressReason]
// instead, never this: a caller must be able to tell "never configured"
// apart from "configured, currently between engines".
const SwitchableEngineNoBindingReason = "no audio.node configuration has been delivered to this node yet; nothing plays audio"

// SwitchableEngineRebindInProgressReason is [SwitchableEngine.Available]'s
// reason while a rebind has detached the outgoing engine and has not yet
// bound its replacement (see [Manager.RebindEngine] and
// audioEngineRebuilder.rebuild in the agent package). A binding WAS
// delivered here, so a caller must not read this the way it would read
// [SwitchableEngineNoBindingReason].
const SwitchableEngineRebindInProgressReason = "an audio.node.configure rebuild is replacing this node's engine; no engine is bound yet"

// ErrNoEngineBinding is the sentinel every SwitchableEngine call fails
// with before its first Set. Callers that need to tell "no engine bound
// yet" apart from a genuine engine failure — restoreOne does, see
// restore.go's deferRestoreLocked — check for this with errors.Is rather
// than matching SwitchableEngineNoBindingReason's text.
var ErrNoEngineBinding = errors.New(SwitchableEngineNoBindingReason)

// SwitchableEngine is an [Engine] whose backing implementation can be
// replaced at runtime, so a [Manager] built once at agent startup can be
// bound to a real engine once an audio.node configuration is delivered,
// and rebound again whenever that configuration changes, without
// restarting the agent (ADR-036). Every call delegates to whatever
// engine is currently set; before the first [SwitchableEngine.Set] with a
// real engine, Available reports [SwitchableEngineNoBindingReason] and
// every other method fails with the same reason. After that, a later
// detached state (current nil again) reports
// [SwitchableEngineRebindInProgressReason] instead, classified as
// [pkgaudio.FaultRouteChanged] rather than [pkgaudio.FaultOther]: the
// node's engine changed out from under a caller, the same fact
// [Manager.RebindEngine] already reports to a session it invalidates.
type SwitchableEngine struct {
	mu        sync.RWMutex
	current   Engine
	everBound bool
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
	if engine != nil {
		e.everBound = true
	}
	e.mu.Unlock()
	return prev
}

func (e *SwitchableEngine) get() (Engine, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current, e.current != nil
}

// unbound reports the reason and classifiable error for a detached
// current engine: [SwitchableEngineNoBindingReason] before any real
// engine was ever bound (wrapping [ErrNoEngineBinding] so restore can
// defer rather than fail, see restore.go), [SwitchableEngineRebindInProgressReason] after
// (wrapping [pkgaudio.ErrEngineRouteChanged] so [pkgaudio.ClassifyFault]
// reports [pkgaudio.FaultRouteChanged] rather than [pkgaudio.FaultOther]
// for a command attempted in that window).
func (e *SwitchableEngine) unbound() (reason string, err error) {
	e.mu.RLock()
	everBound := e.everBound
	e.mu.RUnlock()
	if everBound {
		return SwitchableEngineRebindInProgressReason,
			fmt.Errorf("%w: %s", pkgaudio.ErrEngineRouteChanged, SwitchableEngineRebindInProgressReason)
	}
	return SwitchableEngineNoBindingReason, fmt.Errorf("audio: %w", ErrNoEngineBinding)
}

// Available reports the currently set engine's own availability, or
// false with [SwitchableEngine.unbound]'s reason when nothing is
// currently set.
func (e *SwitchableEngine) Available() (bool, string) {
	cur, ok := e.get()
	if !ok {
		reason, _ := e.unbound()
		return false, reason
	}
	return cur.Available()
}

func (e *SwitchableEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Load(ctx, handle, media, duration)
}

func (e *SwitchableEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Start(ctx, handle, position)
}

func (e *SwitchableEngine) Pause(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Pause(ctx, handle)
}

func (e *SwitchableEngine) Resume(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Resume(ctx, handle)
}

func (e *SwitchableEngine) Seek(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Seek(ctx, handle, position)
}

func (e *SwitchableEngine) Stop(ctx context.Context, handle EngineHandle) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Stop(ctx, handle)
}

func (e *SwitchableEngine) SetGain(ctx context.Context, handle EngineHandle, gain pkgaudio.Gain) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.SetGain(ctx, handle, gain)
}

func (e *SwitchableEngine) Fade(ctx context.Context, handle EngineHandle, fade pkgaudio.Fade) (EngineObservation, error) {
	cur, ok := e.get()
	if !ok {
		_, err := e.unbound()
		return EngineObservation{}, err
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
		_, err := e.unbound()
		return EngineObservation{}, err
	}
	return cur.Observe(ctx, handle)
}

// StartLTC, StopLTC, and ObserveLTC forward to whatever engine is
// currently bound, so an [LTCGenerator] assertion against this value
// survives every rebind. A never-bound engine, or a bound one that cannot
// generate LTC, reports [LTCUnsupported] rather than executing anything.
func (e *SwitchableEngine) StartLTC(ctx context.Context, spec LTCSpec) (LTCObservation, error) {
	cur, ok := e.get()
	if !ok {
		reason, err := e.unbound()
		return LTCObservation{State: LTCUnsupported, Reason: reason, ObservedAt: time.Now()}, err
	}
	gen, ok := cur.(LTCGenerator)
	if !ok {
		return LTCObservation{State: LTCUnsupported, Reason: noLTCGeneratorReason, ObservedAt: time.Now()}, fmt.Errorf("audio: %s", noLTCGeneratorReason)
	}
	return gen.StartLTC(ctx, spec)
}

func (e *SwitchableEngine) StopLTC(ctx context.Context) (LTCObservation, error) {
	cur, ok := e.get()
	if !ok {
		reason, err := e.unbound()
		return LTCObservation{State: LTCUnsupported, Reason: reason, ObservedAt: time.Now()}, err
	}
	gen, ok := cur.(LTCGenerator)
	if !ok {
		return LTCObservation{State: LTCUnsupported, Reason: noLTCGeneratorReason, ObservedAt: time.Now()}, fmt.Errorf("audio: %s", noLTCGeneratorReason)
	}
	return gen.StopLTC(ctx)
}

func (e *SwitchableEngine) ObserveLTC(ctx context.Context) LTCObservation {
	cur, ok := e.get()
	if !ok {
		reason, _ := e.unbound()
		return LTCObservation{State: LTCUnsupported, Reason: reason, ObservedAt: time.Now()}
	}
	return ObserveEngineLTC(ctx, cur, time.Now())
}

// GlitchCounts forwards to whatever engine is currently bound. A rebind
// DOES reset the count: audio.node.configure builds a brand new Engine
// (audioEngineRebuilder.rebuild), and this simply reports that fresh
// engine's own count from zero. GlitchCounts.Since is how a caller tells
// that reset apart from a genuinely quiet period, rather than reading a
// falling count as though nothing happened. A never-bound engine, or a
// bound one that does not implement [GlitchObserver], reports (zero,
// false) — not collected, never a fabricated healthy zero.
func (e *SwitchableEngine) GlitchCounts() (GlitchCounts, bool) {
	cur, ok := e.get()
	if !ok {
		return GlitchCounts{}, false
	}
	return ObserveEngineGlitches(cur)
}
