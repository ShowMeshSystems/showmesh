//go:build !cgo

package gstengine

import (
	"context"
	"fmt"
	"time"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// unavailableReason is truthful about this build, never a fake or a test
// double: this binary was compiled with CGO_ENABLED=0, so go-gst's cgo
// bindings are not linked in and no GStreamer backend exists here at all.
const unavailableReason = "built without cgo: no GStreamer backend is linked into this binary"

// Engine is a same-shaped, non-functional stand-in for the cgo-built
// Engine, present so this package always compiles under
// CGO_ENABLED=0 (the coordinator's static, distroless build). Every call
// fails with the same reason [Engine.Available] reports.
type Engine struct{}

// New validates cfg and returns an [Engine] whose [Engine.Available]
// always reports false with [unavailableReason] — this build has no
// GStreamer backend regardless of cfg.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Engine{}, nil
}

func (e *Engine) unavailable() error {
	return fmt.Errorf("gstengine: %s", unavailableReason)
}

func (e *Engine) Load(context.Context, agentaudio.EngineHandle, pkgaudio.MediaRef, time.Duration) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Start(context.Context, agentaudio.EngineHandle, time.Duration) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Pause(context.Context, agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Resume(context.Context, agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Seek(context.Context, agentaudio.EngineHandle, time.Duration) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Stop(context.Context, agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) SetGain(context.Context, agentaudio.EngineHandle, pkgaudio.Gain) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Fade(context.Context, agentaudio.EngineHandle, pkgaudio.Fade) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

func (e *Engine) Release(context.Context, agentaudio.EngineHandle) error {
	return e.unavailable()
}

func (e *Engine) Observe(context.Context, agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	return agentaudio.EngineObservation{}, e.unavailable()
}

// Available always reports false: this build has no GStreamer backend.
func (e *Engine) Available() (bool, string) {
	return false, unavailableReason
}

var _ agentaudio.Engine = (*Engine)(nil)
