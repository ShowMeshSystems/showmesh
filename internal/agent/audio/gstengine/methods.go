//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

var errUnavailable = fmt.Errorf("gstengine: engine is not available")

// errFadeBeforeStart is returned by Fade when the branch has never left
// StateReady: its ramp would run against decode-ahead preroll and replay
// in full once Start's flushing seek resets the segment to zero. SetGain
// is the correct verb for presetting a gain before playback.
var errFadeBeforeStart = fmt.Errorf("gstengine: cannot fade a branch that has not been started")

// errLoadTimedOut marks a Load that failed only because its caller's ctx
// expired, never because the asset itself was undecodable.
var errLoadTimedOut = fmt.Errorf("gstengine: Load did not complete before its context deadline")

// errAnchorUnknown marks a branch whose real GStreamer segment may no
// longer match segmentStart: a seek's own ctx deadline fired before its
// abandoned goroutine's call to decodebin.Seek returned, and that call
// is still free to land later with no way for this package to learn if
// or when it did. Every operation that would anchor the mixer or a fade
// to segmentStart refuses with this instead of running the branch's
// buffers into the aggregator's past. It wraps
// [pkgaudio.ErrEnginePipelineCrash]: from a session's perspective this
// branch is exactly as unrecoverable as one, and only Release followed
// by a fresh Load makes it usable again.
var errAnchorUnknown = fmt.Errorf("%w: gstengine: a seek timed out and may still land; this branch's position anchoring no longer matches GStreamer's actual segment", pkgaudio.ErrEnginePipelineCrash)

// errTeardownDeferredForRace marks a teardown that refused to touch a
// branch's elements because an earlier operation on it abandoned its own
// state change to ctx's deadline and may still be driving those same
// elements — a timed-out Start left running toward PLAYING, say. It
// wraps [pkgaudio.ErrEnginePipelineCrash]: the branch could not be torn
// down safely and its elements leak for the life of the process.
var errTeardownDeferredForRace = fmt.Errorf("%w: gstengine: teardown deferred because an earlier abandoned state change may still be driving this branch's elements", pkgaudio.ErrEnginePipelineCrash)

// asLoadTimeout wraps a bare context deadline with errLoadTimedOut, and
// returns any other error (a pipeline crash boundedCall produced) unchanged.
func asLoadTimeout(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errLoadTimedOut, err)
	}
	return err
}

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
		// A boundedCall failure here can mean ctx's own deadline fired,
		// which leaves ctx already exhausted, so teardown needs a budget
		// of its own rather than one that has already run out.
		_ = bestEffortTeardown(b)
		return agentaudio.EngineObservation{}, asLoadTimeout(err)
	}

	select {
	case <-b.readyCh:
	case err := <-b.loadErrCh:
		_ = b.teardown(ctx)
		return agentaudio.EngineObservation{}, err
	case <-ctx.Done():
		// ctx is already exhausted here, unlike the two cases above, so
		// teardown needs a budget of its own rather than one that has
		// already run out.
		_ = bestEffortTeardown(b)
		return agentaudio.EngineObservation{}, asLoadTimeout(ctx.Err())
	}

	e.mu.Lock()
	e.handles[handle] = b
	e.mu.Unlock()

	return b.observe(e.cfg.now()), nil
}

// Start seeks to position, then brings the branch to PLAYING. The seek
// runs even for position 0: a branch loaded ahead of Start may have kept
// decoding while frozen, so only an unconditional seek guarantees Start
// begins producing from the position it names rather than from wherever
// the branch had drifted to. A source that refuses this seek — including
// at position 0 — fails Start as [pkgaudio.ErrEngineDecodeFailure],
// indistinguishable from an undecodable asset. Start also clears any flow
// block a prior Stop left behind: its own contract promises playback, not
// only that it requires a Resume first, and a Start that reports Playing
// while nothing flows is exactly the kind of stale claim this package
// must not make.
func (e *Engine) Start(ctx context.Context, handle agentaudio.EngineHandle, position time.Duration) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.unblockFlow()
	if err := b.seekTo(ctx, position, func() { b.resyncMixerPads(position) }); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	// unfreeze only after the transition to PLAYING actually succeeds: it
	// switches Position reporting from the frozen bookmark to a live
	// query, and a caller must never see that live query while the
	// session's own state stays non-playing because this failed.
	if err := b.setElementsState(ctx, gst.StatePlaying); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.unfreeze()
	b.setState(pkgaudio.StatePlaying)
	return b.observe(e.cfg.now()), nil
}

// Pause halts the branch's own contribution to the mix by blocking its
// data flow (see blockFlow) and freezes its reported position at the
// value it held the instant before the block took effect. Setting an
// element's own state to PAUSED does not reliably stop dataflow while it
// remains a sibling inside a pipeline that stays PLAYING, so this never
// does that. The shared mixer's ignore-inactive-pads keeps the rest of
// the program bus running unaffected.
func (e *Engine) Pause(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	b.blockFlow()
	b.freezeAt(pos)
	b.setState(pkgaudio.StatePaused)
	return b.observe(e.cfg.now()), nil
}

// Resume issues a flushing seek back to the branch's own frozen position,
// exactly as Start does for a branch that sat loaded and frozen for a
// while: an offset-only re-anchor is not enough, because
// GstAudioAggregator keeps advancing its own output clock for the whole
// hold, and buffers carrying pre-hold timestamps land in its past and are
// discarded outright, not merely played back too fast. A flushing seek
// gives the branch a fresh segment at the current pipeline running time,
// which is what actually makes the resumed audio continuous instead of
// dropping the entire held duration.
func (e *Engine) Resume(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	if err := b.seekTo(ctx, pos, func() { b.resyncMixerPads(pos) }); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	b.unfreeze()
	b.unblockFlow()
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
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.seekTo(ctx, position, func() { b.resyncMixerPads(position) }); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	return b.observe(e.cfg.now()), nil
}

// Stop ends playback, blocking data flow exactly as Pause does and
// freezing position, and marking Stopped, permanently distinct from a
// branch that reaches Completed on its own. The flow block is released
// only by Resume or by teardown at Release; a Stopped branch has none
// left to give the mix.
func (e *Engine) Stop(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	b.blockFlow()
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
// replacing any fade already in progress. FadeActive clears once the
// fade's own duration has elapsed AND Gain has actually arrived at
// fade.TargetGain (see fadeArrived); an elapsed fade whose gain has not
// yet arrived, such as one Pause or Stop has held short of target, stays
// reported in progress. Fade refuses a branch that has never been
// Start'd — see errFadeBeforeStart — since preroll decode would run the
// ramp to completion before playback exists to hear it; use SetGain to
// preset a gain ahead of Start instead.
func (e *Engine) Fade(ctx context.Context, handle agentaudio.EngineHandle, fade pkgaudio.Fade) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := fade.Validate(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if !b.hasStarted() {
		return agentaudio.EngineObservation{}, errFadeBeforeStart
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.startFade(fade, b.queryPosition()); err != nil {
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
// segmentStart, after, and the frozen position are mutated only once
// boundedCall has returned successfully, so a seek abandoned to ctx's
// deadline never rewrites them later from its still-running goroutine.
// That goroutine keeps running the seek it already issued, though, and
// may still land it against the real GStreamer segment — see
// errAnchorUnknown for what this does about the resulting staleness.
func (b *branch) seekTo(ctx context.Context, position time.Duration, after func()) error {
	err := boundedCall(ctx, func() error {
		if !b.decodebin.Seek(1.0, gst.FormatTime, gst.SeekFlagFlush|gst.SeekFlagAccurate,
			gst.SeekTypeSet, position.Nanoseconds(), gst.SeekTypeNone, -1) {
			return fmt.Errorf("%w: seek to %s was refused", pkgaudio.ErrEngineDecodeFailure, position)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			// The seek was issued and may still land from the abandoned
			// goroutine; segmentStart cannot be moved to position from
			// here without risking a second write racing that goroutine's
			// own eventual (never observed) completion, so the branch is
			// marked permanently unusable for anchoring instead.
			b.mu.Lock()
			b.anchorUnknown = true
			b.mu.Unlock()
		}
		return err
	}

	b.mu.Lock()
	b.segmentStart = position
	frozen := b.frozen
	b.mu.Unlock()
	if after != nil {
		after()
	}
	if frozen {
		b.freezeAt(position)
	}
	return nil
}

// teardownTimeout bounds a best-effort teardown triggered by a failure
// this package cannot otherwise recover from. A stuck GStreamer state
// change must end the caller's wait, never extend it indefinitely.
var teardownTimeout = 5 * time.Second // var, not const: shrunk by tests exercising the bound itself

// bestEffortTeardown tears b down under a fresh bounded context, for
// call sites that hold no ctx of their own (a cleanup path already
// past its caller's deadline, or one triggered by that deadline).
func bestEffortTeardown(b *branch) error {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	return b.teardown(ctx)
}

// teardown halts and removes every element this branch owns and releases
// its channel mixer request pads. Bounded by ctx. On timeout it returns
// without touching a pad or an element, leaking them rather than
// manipulating elements a still-running abandoned goroutine holds. The
// same hazard applies when an earlier operation on this branch — a
// timed-out Start left running toward PLAYING, say — abandoned its own
// state change: teardown waits up to teardownTimeout for every such call
// to finish before it starts its own, and refuses to touch elements at
// all rather than race one still outstanding past that bound.
func (b *branch) teardown(ctx context.Context) error {
	b.mu.Lock()
	if b.released {
		b.mu.Unlock()
		return nil
	}
	b.released = true
	b.mu.Unlock()

	b.engine.unindexBranch(b)
	// A blocked pad holds a streaming thread waiting inside the probe;
	// the state change below must never race that wait, so the block is
	// always released first, whether or not this branch was ever paused.
	// This also runs ahead of the drain wait below, because an abandoned
	// state change is exactly the thing a blocked pad can be holding up.
	b.unblockFlow()

	if !b.awaitNoElementRace(teardownTimeout) {
		slog.Warn("gstengine: teardown deferred because an earlier abandoned state change may still be driving this branch's elements; leaving them in the pipeline rather than racing it", "branch", b.id)
		return errTeardownDeferredForRace
	}

	if err := b.setElementsState(ctx, gst.StateNull); err != nil {
		slog.Warn("gstengine: branch teardown did not reach NULL in time; leaving its elements in the pipeline rather than removing them concurrently with the abandoned state change", "branch", b.id, "error", err)
		return err
	}

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
