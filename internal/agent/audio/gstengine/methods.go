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
// buffers into the aggregator's past. This is deliberately not wrapped
// in [pkgaudio.ErrEnginePipelineCrash]: the shared output pipeline is
// fine, only this one branch's anchoring is compromised, so it
// classifies as [pkgaudio.FaultOther], the class that exists exactly
// for an engine error outside the six pipeline-scoped ones, rather than
// overloading "the pipeline broke" onto a branch-scoped hazard. At the
// engine level, only Release followed by a fresh Load makes the branch
// usable again; whether anything above this package retries that
// automatically is a session-layer decision this package does not make.
var errAnchorUnknown = fmt.Errorf("gstengine: a seek timed out and may still land; this branch's position anchoring no longer matches GStreamer's actual segment")

// errTeardownDeferredForRace marks a teardown that refused to touch a
// branch's elements because an earlier operation on it abandoned its own
// state change to ctx's deadline and may still be driving those same
// elements: a timed-out Start left running toward PLAYING, say. The
// branch could not be torn down safely and its elements leak for the
// life of the process. Not wrapped in [pkgaudio.ErrEnginePipelineCrash]
// for the same reason as errAnchorUnknown: this is one branch's teardown
// failing, not the shared pipeline itself, so it classifies as
// [pkgaudio.FaultOther].
var errTeardownDeferredForRace = fmt.Errorf("gstengine: teardown deferred because an earlier abandoned state change may still be driving this branch's elements")

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

	b := &branch{id: e.nextID.Add(1), engine: e, media: media, duration: duration, state: pkgaudio.StateReady, frozen: true, teardownGate: newTeardownGate()}
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

// Start seeks to position, then brings the branch to PLAYING, even at
// position 0, so it always begins from the position it names. A never-
// joined branch is prepared here; a joined one swaps in a replacement.
func (e *Engine) Start(ctx context.Context, handle agentaudio.EngineHandle, position time.Duration) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}

	if b.hasJoined() {
		// Start always ends up Playing, regardless of the state a prior
		// Pause or Stop left the branch it swaps out in.
		return e.swapToPosition(ctx, handle, b, position, pkgaudio.StatePlaying)
	}

	if err := b.prepare(ctx, position); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if b.currentState() == pkgaudio.StateCompleted {
		// position landed at or past EOS: prepare's own wait already
		// reports this, and there is nothing left to join or play.
		return b.observe(e.cfg.now()), nil
	}
	// unfreeze only once PLAYING succeeds, so Position never reads live
	// while state stays non-playing; a timeout here still marks
	// anchorUnknown, since prepare already committed the segment.
	if err := b.setElementsState(ctx, gst.StatePlaying); err != nil {
		b.markAnchorUnknownOnCtxTimeout(err)
		return agentaudio.EngineObservation{}, err
	}
	if err := b.join(position, true); err != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEngineDecodeFailure, err)
	}
	b.unblockFlow()
	b.setState(pkgaudio.StatePlaying)
	// Observe while still frozen: join's own setup window lets decode
	// buffer ahead of position, and a live query would read that
	// decode-ahead distance instead of the position just committed to.
	obs := b.observe(e.cfg.now())
	b.unfreeze()
	return obs, nil
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
	pos := b.renderedPosition()
	b.blockFlow()
	b.freezeAt(pos)
	b.setState(pkgaudio.StatePaused)
	return b.observe(e.cfg.now()), nil
}

// Resume continues from the branch's own frozen position. It always
// swaps in a replacement (see [Engine.swapToPosition]) since it is only
// called on a branch already joined to the mixer.
func (e *Engine) Resume(ctx context.Context, handle agentaudio.EngineHandle) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	pos := b.queryPosition()
	// Resume always ends up Playing: that is its whole purpose.
	return e.swapToPosition(ctx, handle, b, pos, pkgaudio.StatePlaying)
}

// Seek re-anchors the branch to position, a discontinuity. A branch not
// yet joined is re-prepared in place; one already joined swaps in a
// replacement instead, keeping whatever state it was already in.
func (e *Engine) Seek(ctx context.Context, handle agentaudio.EngineHandle, position time.Duration) (agentaudio.EngineObservation, error) {
	b, err := e.branchFor(handle)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if err := b.checkAnchorKnown(); err != nil {
		return agentaudio.EngineObservation{}, err
	}

	if b.hasJoined() {
		return e.swapToPosition(ctx, handle, b, position, b.currentState())
	}

	if err := b.prepare(ctx, position); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	return b.observe(e.cfg.now()), nil
}

// swapToPosition replaces old, a branch already joined to the mixer,
// rather than flush-seeking it in place. A failed preparation tears
// down only the replacement; old and its anchoring stay untouched.
func (e *Engine) swapToPosition(ctx context.Context, handle agentaudio.EngineHandle, old *branch, position time.Duration, targetState pkgaudio.State) (agentaudio.EngineObservation, error) {
	old.mu.Lock()
	fadeActive := old.fadeActive
	fadeStartPos, fadeStartGain := old.fadeStartPos, old.fadeStartGain
	fadeDuration, fadeTargetGain := old.fadeDuration, old.fadeTargetGain
	old.mu.Unlock()
	gain := old.currentGain()

	replacement := &branch{
		id: e.nextID.Add(1), engine: e, media: old.media, duration: old.duration,
		state: pkgaudio.StateReady, frozen: true, teardownGate: newTeardownGate(),
	}
	if err := replacement.build(old.path); err != nil {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEngineDecodeFailure, err)
	}
	// Tracked only once build has actually created replacement's
	// elements: Close's fan-out would otherwise reach a branch whose
	// element fields are still nil.
	e.trackReplacement(replacement)
	defer e.untrackReplacement(replacement)
	cleanup := func() { _ = bestEffortTeardown(replacement) }

	// Set before any state change: volume sits upstream of queue, so
	// setting it only after PLAYING would let queued audio already
	// reach the mix at unity gain first.
	replacement.volume.SetObjectProperty("volume", float64(gain))
	if fadeActive {
		fade := pkgaudio.Fade{Duration: fadeDuration, TargetGain: fadeTargetGain}
		if err := replacement.startFadeFrom(fade, fadeStartPos, fadeStartGain); err != nil {
			slog.Warn("gstengine: could not re-attach an active fade to a swap replacement", "branch", replacement.id, "error", err)
		}
	}

	if err := replacement.setElementsState(ctx, gst.StatePaused); err != nil {
		cleanup()
		return agentaudio.EngineObservation{}, err
	}
	select {
	case <-replacement.readyCh:
	case err := <-replacement.loadErrCh:
		cleanup()
		return agentaudio.EngineObservation{}, err
	case <-ctx.Done():
		cleanup()
		return agentaudio.EngineObservation{}, ctx.Err()
	}

	if err := replacement.prepare(ctx, position); err != nil {
		cleanup()
		return agentaudio.EngineObservation{}, err
	}
	if replacement.currentState() == pkgaudio.StateCompleted {
		// position landed at or past EOS: swap the handle to the
		// replacement anyway, so it correctly reports Completed, but
		// there is nothing left to play or join.
		return e.commitSwap(handle, old, replacement, replacement.observe(e.cfg.now())), nil
	}
	if targetState != pkgaudio.StatePlaying {
		// blockFlow is for API consistency (blockProbeID reads blocked);
		// join's retained hold is what actually keeps this silent.
		replacement.blockFlow()
	}
	if err := replacement.setElementsState(ctx, gst.StatePlaying); err != nil {
		cleanup()
		return agentaudio.EngineObservation{}, err
	}
	if err := replacement.join(position, targetState == pkgaudio.StatePlaying); err != nil {
		cleanup()
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: %v", pkgaudio.ErrEngineDecodeFailure, err)
	}

	replacement.setState(targetState)
	// Observe while still frozen, so it reports position exactly, not a
	// live query racing however far decode buffered ahead during join's
	// own setup window. unfreeze runs after, once this swap is done.
	obs := replacement.observe(e.cfg.now())
	return e.commitSwap(handle, old, replacement, obs), nil
}

// commitSwap mutes old's mixer pads, re-points handle to replacement,
// unfreezes it if it is Playing, and retires old in the background.
// Shared by swapToPosition's playing/paused path and its EOS shortcut.
func (e *Engine) commitSwap(handle agentaudio.EngineHandle, old, replacement *branch, obs agentaudio.EngineObservation) agentaudio.EngineObservation {
	e.mu.Lock()
	for _, pad := range old.channelMixerPads {
		if pad != nil {
			pad.SetObjectProperty("mute", true)
		}
	}
	e.handles[handle] = replacement
	e.mu.Unlock()

	if obs.State == pkgaudio.StatePlaying {
		replacement.unfreeze()
	}
	e.retireAsync(old)
	return obs
}

// retireAsync tears old down in the background, since it is already
// muted and unreachable. Tracked so Close can still find and finish it;
// branch.teardown's own gate makes a concurrent Close safe.
func (e *Engine) retireAsync(old *branch) {
	e.mu.Lock()
	e.retiringBranches[old] = struct{}{}
	e.mu.Unlock()
	go func() {
		defer func() {
			e.mu.Lock()
			delete(e.retiringBranches, old)
			e.mu.Unlock()
		}()
		if err := bestEffortTeardown(old); err != nil {
			slog.Warn("gstengine: retired branch teardown after a swap did not complete cleanly", "branch", old.id, "error", err)
		}
	}()
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
// may still land it against the real GStreamer segment; see
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
		// The seek was issued and may still land from the abandoned
		// goroutine; segmentStart cannot be moved to position from here
		// without risking a second write racing that goroutine's own
		// eventual (never observed) completion, so the branch is marked
		// permanently unusable for anchoring instead, but only if err is
		// ctx's own doing rather than a genuine seek refusal.
		b.markAnchorUnknownOnCtxTimeout(err)
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
// its channel mixer request pads. Bounded by min(ctx's own deadline,
// teardownTimeout), never by teardownTimeout alone, since a caller can
// hold a lock across this call (Session.releaseEngineLocked does, across
// Release) and must not be stalled past its own budget merely because
// teardownTimeout asks for more. On timeout it returns without touching
// a pad or an element, leaking them rather than manipulating elements a
// still-running abandoned goroutine holds. The same hazard applies when
// an earlier operation on this branch, a timed-out Start left running
// toward PLAYING, say, abandoned its own state change: teardown checks
// pendingStateChanges and waits for it to drain before touching
// elements, which narrows the window rather than closing it.
// awaitNoElementRace holds no lock across that check, so a Start already
// past branchFor but not yet at its own setElementsState call can still
// slip in between teardown's check and its own SetState(NULL); this is a
// pre-existing, real hazard this change reduces but does not eliminate.
// Only a genuine success is cached (see released): a deferred attempt
// returns errTeardownDeferredForRace but is retried fresh on the next
// call, since the condition that caused it can clear.
//
// At most one caller runs doTeardown for this branch at a time.
// teardownGate admits only one caller to the real attempt; a caller that
// loses that race blocks on ctx rather than on an uninterruptible lock,
// so it can never be held past its own budget by the winner's attempt.
// Once admitted, it re-checks released: if the winner it waited behind
// already succeeded, it returns nil rather than repeating doTeardown,
// and if the winner instead deferred, it proceeds to run doTeardown
// itself, fresh. This guards against two callers ever holding the same
// branch at once (see teardownGate's field comment); only one caller can
// obtain a given branch today, so the gate is presently uncontended
// defense in depth, not a resolution of a live race.
func (b *branch) teardown(ctx context.Context) error {
	b.mu.Lock()
	if b.teardownGate == nil {
		b.teardownGate = newTeardownGate()
	}
	gate := b.teardownGate
	if b.released {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	// Tried non-blocking first so an uncontended caller is never turned
	// away by an already-expired ctx: doTeardown's own ctx handling
	// (setElementsState, awaitNoElementRace) is what must decide an
	// uncontended call's outcome. Only a genuinely contended caller
	// blocks here, and then only up to ctx, never past it.
	select {
	case gate <- struct{}{}:
	default:
		select {
		case gate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-gate }()

	b.mu.Lock()
	if b.released {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()

	return b.doTeardown(ctx)
}

// doTeardown is teardown's real attempt, reached only while teardown
// holds teardownGate: it never runs concurrently with itself on the
// same branch. Unlike a genuine success, a deferral is never cached, so
// a retried teardown reaches here again and re-checks pendingStateChanges
// fresh.
func (b *branch) doTeardown(ctx context.Context) error {
	b.mu.Lock()
	b.teardownClaimed = true
	b.mu.Unlock()

	// Release any flow block first so the state change below never
	// races it. The hold stays if never joined: releasing it could
	// reach deinterleave's still-unlinked pads; NULL deactivates it.
	b.unblockFlow()
	if b.hasJoined() {
		b.removeHold()
	}

	if !b.awaitNoElementRace(ctx, teardownTimeout) {
		slog.Warn("gstengine: teardown deferred because an earlier abandoned state change may still be driving this branch's elements; leaving them in the pipeline rather than racing it", "branch", b.id)
		b.engine.markTeardownDeferred()
		b.silenceDeferredBranch()
		return errTeardownDeferredForRace
	}

	if err := b.setElementsState(ctx, gst.StateNull); err != nil {
		slog.Warn("gstengine: branch teardown did not reach NULL in time; leaving its elements in the pipeline rather than removing them concurrently with the abandoned state change", "branch", b.id, "error", err)
		b.engine.markTeardownDeferred()
		b.silenceDeferredBranch()
		return err
	}

	// Unindexed only once teardown is actually going to remove the
	// elements: a deferred attempt above leaves the branch findable by
	// [Engine.branchForSource] on purpose, so a bus error its still-live,
	// unremoved elements produce later attributes back to this branch
	// (a harmless no-op via reportLoadError, nothing reads loadErrCh once
	// Release has returned) instead of falling through to
	// [Engine.markBroken] and poisoning every other branch's next Load.
	b.engine.unindexBranch(b)

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

	b.mu.Lock()
	b.released = true
	b.mu.Unlock()
	return nil
}
