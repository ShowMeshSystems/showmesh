//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

var errUnavailable = fmt.Errorf("gstengine: engine is not available")

func (e *Engine) unavailableErr() error {
	_, reason := e.Available()
	return fmt.Errorf("%w: %s", errUnavailable, reason)
}

// brokenErr reports the shared output pipeline's own failure, classified
// so a caller sees a pipeline crash rather than a generic error. Empty
// reason means the pipeline is fine.
func (e *Engine) brokenErr() error {
	e.brokenMu.Lock()
	reason := e.brokenReason
	e.brokenMu.Unlock()
	if reason == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", pkgaudio.ErrEnginePipelineCrash, reason)
}

// branchFor is the choke point every handle-addressed method passes
// through, so the broken-pipeline check lives here rather than being
// repeated and eventually forgotten in one of them. A branch on a dead
// output pipeline must never answer with the state it last held.
func (e *Engine) branchFor(handle agentaudio.EngineHandle) (*branch, error) {
	if err := e.brokenErr(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	b, ok := e.handles[handle]
	if !ok {
		return nil, fmt.Errorf("gstengine: no loaded handle %q", handle)
	}
	return b, nil
}

// Load resolves media to a local path, stats it (a missing or
// unresolvable asset is [pkgaudio.ErrEngineMediaDisappeared] and never
// touches GStreamer), builds the branch's decode chain, brings it to
// PAUSED, and waits for either every dynamic pad to link or a decode
// error, bounded by ctx.
func (e *Engine) Load(ctx context.Context, handle agentaudio.EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (agentaudio.EngineObservation, error) {
	if err := e.brokenErr(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if ok, _ := e.Available(); !ok {
		return agentaudio.EngineObservation{}, e.unavailableErr()
	}
	if err := media.Validate(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	path, err := e.cfg.Resolve(media)
	if err != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: resolving asset: %v", pkgaudio.ErrEngineMediaDisappeared, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEngineMediaDisappeared, statErr)
	}

	b := &branch{id: e.nextID.Add(1), engine: e, media: media, duration: duration, state: pkgaudio.StateReady, frozen: true}
	if err := b.build(path); err != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEngineDecodeFailure, err)
	}

	if err := b.setElementsState(ctx, gst.StatePaused); err != nil {
		_ = b.teardown(context.Background())
		return agentaudio.EngineObservation{}, err
	}

	select {
	case <-b.readyCh:
	case err := <-b.loadErrCh:
		_ = b.teardown(context.Background())
		return agentaudio.EngineObservation{}, err
	case <-ctx.Done():
		_ = b.teardown(context.Background())
		return agentaudio.EngineObservation{}, ctx.Err()
	}

	e.mu.Lock()
	e.handles[handle] = b
	e.mu.Unlock()

	return b.observe(e.cfg.now()), nil
}

// Start seeks to position when non-zero, then brings the branch to
// PLAYING.
func (e *Engine) Start(ctx context.Context, handle agentaudio.EngineHandle, position time.Duration) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if position > 0 {
		if err := b.seekTo(ctx, position); err != nil {
			return agentaudio.EngineObservation{}, err
		}
	}
	b.resyncMixerPads(b.queryPosition())
	b.unfreeze()
	if err := b.setElementsState(ctx, gst.StatePlaying); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.setState(pkgaudio.StatePlaying)
	return b.observe(e.cfg.now()), nil
}

// Pause freezes the branch's position at its live value, then halts its
// own elements — the shared mixer's ignore-inactive-pads keeps the rest
// of the program bus running unaffected.
func (e *Engine) Pause(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	if err := b.setElementsState(ctx, gst.StatePaused); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.freezeAt(pos)
	b.setState(pkgaudio.StatePaused)
	return b.observe(e.cfg.now()), nil
}

// Resume continues playback from the position Pause left it at.
func (e *Engine) Resume(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.resyncMixerPads(b.queryPosition())
	b.unfreeze()
	if err := b.setElementsState(ctx, gst.StatePlaying); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.setState(pkgaudio.StatePlaying)
	return b.observe(e.cfg.now()), nil
}

// Seek re-anchors the branch to position — a discontinuity, never a
// continuation of pre-seek timing.
func (e *Engine) Seek(ctx context.Context, handle agentaudio.EngineHandle, position time.Duration) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.seekTo(ctx, position); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.resyncMixerPads(position)
	return b.observe(e.cfg.now()), nil
}

// Stop ends playback, freezing position and marking Stopped — permanently
// distinct from a branch that reaches Completed on its own.
func (e *Engine) Stop(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	if err := b.setElementsState(ctx, gst.StatePaused); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.freezeAt(pos)
	b.setState(pkgaudio.StateStopped)
	return b.observe(e.cfg.now()), nil
}

// SetGain sets handle's gain immediately, cancelling any in-progress
// fade.
func (e *Engine) SetGain(ctx context.Context, handle agentaudio.EngineHandle, gain pkgaudio.Gain) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := gain.Validate(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.cancelFade()
	b.volume.SetObjectProperty("volume", float64(gain))
	return b.observe(e.cfg.now()), nil
}

// Fade begins a GstController-driven ramp toward fade.TargetGain,
// replacing any fade already in progress.
func (e *Engine) Fade(ctx context.Context, handle agentaudio.EngineHandle, fade pkgaudio.Fade) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := fade.Validate(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	base := e.pipeline.GetCurrentRunningTime()
	if err := b.startFade(fade, base); err != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEnginePipelineCrash, err)
	}
	return b.observe(e.cfg.now()), nil
}

// Release discards handle. Releasing an already-released or never-loaded
// handle is not an error.
func (e *Engine) Release(ctx context.Context, handle agentaudio.EngineHandle) error {
	e.mu.Lock()
	b, ok := e.handles[handle]
	if ok {
		delete(e.handles, handle)
	}
	e.mu.Unlock()
	if !ok {
		return nil
	}
	return b.teardown(ctx)
}

// Observe returns handle's current state, collected fresh.
func (e *Engine) Observe(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	return b.observe(e.cfg.now()), nil
}

// seekTo issues a flushing, accurate seek on the branch and re-anchors
// its frozen position when the branch is not currently playing.
func (b *branch) seekTo(ctx context.Context, position time.Duration) error {
	err := boundedCall(ctx, func() error {
		ok := b.decodebin.Seek(1.0, gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagAccurate,
			gst.SeekTypeSet, position.Nanoseconds(), gst.SeekTypeNone, -1)
		if !ok {
			return fmt.Errorf("%w: seek to %s was refused", pkgaudio.ErrEngineDecodeFailure, position)
		}
		return nil
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.segmentStart = position
	frozen := b.frozen
	b.mu.Unlock()
	if frozen {
		b.freezeAt(position)
	}
	return nil
}

// teardown halts and removes every element this branch owns and releases
// its channel mixer request pads. Bounded by ctx; best-effort beyond
// that, since a handle being released should not become unreleasable.
func (b *branch) teardown(ctx context.Context) error {
	b.mu.Lock()
	if b.released {
		b.mu.Unlock()
		return nil
	}
	b.released = true
	b.mu.Unlock()

	b.engine.unindexBranch(b)
	_ = b.setElementsState(ctx, gst.StateNull)

	bin, ok := b.engine.pipeline.(gst.Bin)
	if ok {
		for k, pad := range b.channelMixerPads {
			if pad == nil {
				continue
			}
			b.engine.channelMixers[k].ReleaseRequestPad(pad)
		}
		for _, el := range b.elements() {
			if el != nil {
				bin.Remove(el)
			}
		}
	}
	return nil
}
