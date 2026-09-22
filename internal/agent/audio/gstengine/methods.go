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
		return nil, fmt.Errorf("%w: gstengine: no loaded handle %q", agentaudio.ErrHandleNotLoaded, handle)
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
	if _, exists := e.handles[handle]; exists {
		e.mu.Unlock()
		// A live branch already answers to this name: taking it over here
		// would orphan whatever the existing branch holds, unaddressable by
		// any later call against the same handle (this package's own
		// handle-uniqueness contract, see agentaudio.Session.engineHandleFor,
		// is what a caller relies on instead of ever reaching this path in
		// production). The newly built branch, never indexed, is torn down
		// rather than leaked.
		_ = b.teardown(ctx)
		return agentaudio.EngineObservation{}, fmt.Errorf("gstengine: a live branch is already loaded under handle %q", handle)
	}
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

	reachedEOS, err := b.prepare(ctx, position)
	if err != nil {
		return agentaudio.EngineObservation{}, err
	}
	if reachedEOS {
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
	pos := b.renderedEndPosition()
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

	if _, err := b.prepare(ctx, position); err != nil {
		return agentaudio.EngineObservation{}, err
	}
	return b.observe(e.cfg.now()), nil
}

// swapToPosition replaces old, a branch already joined to the mixer,
// rather than flush-seeking it in place. A failed preparation tears
// down only the replacement; old and its anchoring stay untouched.
func (e *Engine) swapToPosition(ctx context.Context, handle agentaudio.EngineHandle, old *branch, position time.Duration, targetState pkgaudio.State) (agentaudio.EngineObservation, error) {
	// Refused before a single element is built when handle no longer
	// names old: a stop already took it, and everything built here would
	// only have to be torn down again behind that stop.
	e.mu.Lock()
	current := e.handles[handle]
	e.mu.Unlock()
	if current != old {
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: gstengine: handle was released while its swap was still in flight", agentaudio.ErrHandleNotLoaded)
	}

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
	e.trackReplacement(replacement, handle)
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

	reachedEOS, err := replacement.prepare(ctx, position)
	if err != nil {
		cleanup()
		return agentaudio.EngineObservation{}, err
	}
	if reachedEOS {
		// position landed at or past EOS: swap the handle to the
		// replacement anyway, so it correctly reports Completed, but
		// there is nothing left to play or join.
		return e.commitSwap(handle, old, replacement, replacement.observe(e.cfg.now()))
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
	return e.commitSwap(handle, old, replacement, obs)
}

// muteBranch mutes every one of b's channel mixer pads: a property set,
// never a GStreamer state change, so it can run synchronously without
// ever blocking on the pipeline. The pads are read under b.mu, which
// join also writes them under, so this is safe against a branch another
// goroutine is still joining. A branch never joined has nothing to mute:
// it cannot already be audible.
func muteBranch(b *branch) {
	for _, pad := range b.mixerPads() {
		if pad != nil {
			pad.SetObjectProperty("mute", true)
		}
	}
}

// commitSwap mutes old's mixer pads, re-points handle to replacement,
// unfreezes it if it is Playing, and retires old in the background.
// Shared by swapToPosition's playing/paused path and its EOS shortcut.
//
// Refuses to publish replacement when e.handles[handle] no longer names
// old: the only way that happens is a concurrent Release or [Engine.
// ReleaseAll] already tearing old down and forgetting handle while this
// swap was still building its replacement (a released handle's name is
// never reused -- every handle is minted fresh, and [Engine.Load]
// refuses to overwrite a live branch). Without this check, an emergency
// stop that overtakes a swap already in flight would otherwise be
// silently undone the moment that swap committed, re-arming a branch the
// caller already believes was released.
//
// replacement is muted immediately, on this goroutine, the only one that
// may safely touch its still-possibly-unfinished elements: it may
// already be joined and flowing (join releases queue's hold before this
// runs, for a Playing target), so refusing to publish it is not enough
// on its own to guarantee silence. Its own inFlightReplacements entry is
// removed here too, under the same lock as this whole decision, so a
// ReleaseAll snapshotting after this point can never claim a branch
// whose fate this call already decided.
//
// Torn down next -- by this goroutine directly, UNLESS a ReleaseAll had
// already claimed this exact branch (entry.handoff set, under e.mu,
// before this check ran), in which case the branch is handed off
// instead: that ReleaseAll is waiting to tear it down itself, one branch
// at a time across its whole sweep, never contending with whatever this
// goroutine might still be doing to the same elements. See [Engine.
// ReleaseAll]'s own doc comment for why concurrent teardown on this
// shared pipeline must never happen at all.
func (e *Engine) commitSwap(handle agentaudio.EngineHandle, old, replacement *branch, obs agentaudio.EngineObservation) (agentaudio.EngineObservation, error) {
	e.mu.Lock()
	entry := e.inFlightReplacements[replacement]
	delete(e.inFlightReplacements, replacement)
	var handoff chan *branch
	if entry != nil {
		handoff = entry.handoff
	}
	if e.handles[handle] != old {
		muteBranch(replacement)
		e.mu.Unlock()
		if handoff != nil {
			handoff <- replacement
		} else {
			_ = bestEffortTeardown(replacement)
		}
		return agentaudio.EngineObservation{}, fmt.Errorf("%w: gstengine: handle was released while its swap was still in flight", agentaudio.ErrHandleNotLoaded)
	}
	muteBranch(old)
	e.handles[handle] = replacement
	e.mu.Unlock()
	if handoff != nil {
		handoff <- nil
	}

	if obs.State == pkgaudio.StatePlaying {
		replacement.unfreeze()
	}
	e.retireAsync(old)
	return obs, nil
}

// retireAsync tears old down in the background, since it is already
// muted and unreachable. It is also the reaper [Engine.ReleaseAll] hands
// a branch it could not finish inside its own budget. old stays in
// retiringBranches until a teardown actually succeeds, so a branch that
// never finishes is still reachable by Close and by a later stop rather
// than dropped.
func (e *Engine) retireAsync(old *branch) {
	e.mu.Lock()
	e.retiringBranches[old] = struct{}{}
	e.mu.Unlock()
	go func() {
		if err := bestEffortTeardown(old); err != nil {
			slog.Warn("gstengine: retired branch teardown after a swap did not complete cleanly", "branch", old.id, "error", err)
			return
		}
		e.mu.Lock()
		delete(e.retiringBranches, old)
		e.mu.Unlock()
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

// LiveHandles returns every handle this pipeline currently has a branch
// for: the mixer's single output stream otherwise hides them all.
func (e *Engine) LiveHandles(context.Context) ([]agentaudio.EngineHandle, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]agentaudio.EngineHandle, 0, len(e.handles))
	for h := range e.handles {
		out = append(out, h)
	}
	return out, nil
}

// branchStopBudget is one branch's share of an emergency stop: its wait
// for an in-flight swap to settle, and its own teardown. A branch that
// overruns is left muted and parked for the background reaper so the
// sweep moves on, which is what keeps a stop's whole cost proportional
// to how many branches it has rather than hostage to the worst one.
var branchStopBudget = 1500 * time.Millisecond // var, not const: shrunk by tests exercising the bound itself

// ReleaseAll silences, then tears down, every branch this pipeline
// currently holds except the handles named in except, and reports
// exactly which ones it released: never a count computed separately from
// what it can also name, so the two can never drift apart. except also
// covers a swap still in flight, by the handle it is replacing, so a
// replacement swapping in for an excepted handle is left to complete
// rather than silenced and torn down out from under it.
//
// Ordered in three passes, all required by this pipeline's own shape --
// every branch is flat elements sharing ONE GStreamer pipeline, so a
// GStreamer state change against one branch's elements can contend with
// another's on the same underlying pipeline, and two branches with a
// state change in flight at once were observed to do exactly that,
// deferring behind each other for the length of their own bound,
// repeatedly, well past any single branch's own timeout. old and its own
// in-flight replacement are two SEPARATE branch objects, so this applies
// even to a single swap: tearing old down while its own replacement is
// still being built contends exactly the same way.
//
//  1. Silence first, under e.mu alone, waiting on nothing. Every
//     targeted branch in e.handles (minus except) is muted right here
//     before anything else runs: a property set, never a state change,
//     so it can never itself contend or block. Each is also taken out of
//     e.handles and parked in retiringBranches in the same step, so a
//     branch this call cannot finish is still reachable by Close and by
//     the next stop. A swap already in flight targeting an excepted
//     handle is left alone; one that is not is claimed, so [Engine.
//     commitSwap] knows this call is waiting for it. By the time this
//     method returns, nothing it targeted can be heard, whatever the two
//     passes below still have left to do.
//  2. Wait for every claimed swap to settle on its OWN goroutine, the
//     only one that may safely touch its still-being-built elements.
//     Each wait has its own budget; one that overruns is handed to a
//     collector goroutine so its branch is still reaped later, and this
//     pass moves on.
//  3. Tear branches down, each with its own budget: e.handles' own
//     branches first, then the ones handed over in pass 2, then whatever
//     was already parked. A branch that overruns stays muted and parked,
//     is reported in the error, and is left to the background reaper;
//     the sweep moves on to the next one either way.
//
// The teardowns themselves are serialized against every other teardown
// on this pipeline by [branch.teardown], not by this method, so a swap
// or a Close running alongside this sweep can never have a state change
// in flight at the same time as it.
//
// Worst case, this returns within the caller's own ctx, and no later
// than roughly the number of claimed swaps, targeted branches, handed
// over replacements and already-parked branches, multiplied by
// [branchStopBudget], plus however long a sweep already in progress
// holds releaseGate.
//
// Only what was genuinely in e.handles, not excepted, and actually torn
// down counts toward the return value: an in-flight or retiring leftover
// of an ordinary Seek, Resume, or Start is normal bookkeeping, not an
// orphan, and is never counted, whether or not this call happens to
// touch it.
func (e *Engine) ReleaseAll(ctx context.Context, except ...agentaudio.EngineHandle) ([]agentaudio.EngineHandle, error) {
	skip := make(map[agentaudio.EngineHandle]struct{}, len(except))
	for _, h := range except {
		skip[h] = struct{}{}
	}

	type target struct {
		handle agentaudio.EngineHandle
		branch *branch
	}
	e.mu.Lock()
	var toRelease []target
	for h, b := range e.handles {
		if _, ok := skip[h]; ok {
			continue
		}
		toRelease = append(toRelease, target{h, b})
	}
	var handoffs []chan *branch
	for _, entry := range e.inFlightReplacements {
		if _, ok := skip[entry.handle]; ok {
			continue
		}
		if entry.handoff != nil {
			// Another sweep already claimed this swap and owns it.
			continue
		}
		entry.handoff = make(chan *branch, 1)
		handoffs = append(handoffs, entry.handoff)
	}
	// Silence first: every branch below is either fully joined already
	// or was never joined at all (nothing to mute either way), and no
	// other goroutine can be concurrently building it -- unlike a
	// claimed in-flight replacement, which only its own swap goroutine
	// may safely touch (see commitSwap's own muting of it).
	targeted := make(map[*branch]struct{}, len(toRelease))
	for _, t := range toRelease {
		muteBranch(t.branch)
		delete(e.handles, t.handle)
		e.retiringBranches[t.branch] = struct{}{}
		targeted[t.branch] = struct{}{}
	}
	var parked []*branch
	for b := range e.retiringBranches {
		if _, own := targeted[b]; own {
			continue
		}
		parked = append(parked, b)
	}
	e.mu.Unlock()

	var firstErr error
	setErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// collect keeps a claim this call can no longer wait for: the branch
	// is still answered for and still reaped, never left muted in a
	// channel nobody reads.
	collect := func(handoff chan *branch) {
		go func() {
			if b := <-handoff; b != nil {
				e.retireAsync(b)
			}
		}()
	}
	select {
	case e.releaseGate <- struct{}{}:
	case <-ctx.Done():
		// Everything targeted is already silent and already parked, so a
		// caller out of budget here loses only the teardown, never the
		// stop. Only a claimed swap still needs answering.
		for _, handoff := range handoffs {
			collect(handoff)
		}
		return nil, ctx.Err()
	}
	defer func() { <-e.releaseGate }()

	// Pass 2: let every claimed swap resolve before this call ever
	// touches the pipeline itself.
	var handedOff []*branch
	for _, handoff := range handoffs {
		wait, cancel := context.WithTimeout(ctx, branchStopBudget)
		select {
		case b := <-handoff:
			if b != nil {
				e.mu.Lock()
				e.retiringBranches[b] = struct{}{}
				e.mu.Unlock()
				handedOff = append(handedOff, b)
			}
		case <-wait.Done():
			collect(handoff)
			if ctx.Err() != nil {
				setErr(ctx.Err())
			} else {
				setErr(fmt.Errorf("gstengine: a swap still in flight did not settle within this stop's per-branch budget"))
			}
		}
		cancel()
	}

	// Pass 3: one branch at a time, each on its own budget, moving on
	// past whatever it cannot finish.
	tearDown := func(b *branch) error {
		branchCtx, cancel := context.WithTimeout(ctx, branchStopBudget)
		defer cancel()
		if err := b.teardown(branchCtx); err != nil {
			return err
		}
		e.mu.Lock()
		delete(e.retiringBranches, b)
		e.mu.Unlock()
		return nil
	}
	// Only a branch whose teardown was actually attempted and overran is
	// reaped, and only once the sweep is over: a reaper started mid-sweep
	// would hold the teardown turn on that very branch and stall every
	// branch behind it. One never reached stays parked for Close or the
	// next stop, already silent either way.
	var reap []*branch
	defer func() {
		for _, b := range reap {
			e.retireAsync(b)
		}
	}()

	var released []agentaudio.EngineHandle
	for _, t := range toRelease {
		if ctx.Err() != nil {
			setErr(ctx.Err())
			break
		}
		if err := tearDown(t.branch); err != nil {
			setErr(err)
			reap = append(reap, t.branch)
			continue
		}
		released = append(released, t.handle)
	}
	for _, b := range handedOff {
		if ctx.Err() != nil {
			setErr(ctx.Err())
			break
		}
		if err := tearDown(b); err != nil {
			setErr(err)
			reap = append(reap, b)
		}
	}
	// Already-parked branches are not this call's own targets: they never
	// count toward released or firstErr, and a reaper already owns each
	// of them. They are attempted here so a stop that overran last time
	// is finished by the next one.
	for _, b := range parked {
		if ctx.Err() != nil {
			break
		}
		_ = tearDown(b)
	}

	return released, firstErr
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

	// The same two-step against the engine's own turn: every teardown
	// caller passes through here, so this pipeline never has two
	// teardowns changing element state at once.
	select {
	case b.engine.teardownTurn <- struct{}{}:
	default:
		select {
		case b.engine.teardownTurn <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() { <-b.engine.teardownTurn }()

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
	// races it. A never-joined branch flushes the hold away, since a
	// plainly released buffer would answer NOT_LINKED.
	b.unblockFlow()
	if b.hasJoined() {
		b.removeHold()
	} else {
		b.removeHoldForTeardown()
	}

	if !b.awaitNoElementRace(ctx, teardownTimeout) {
		slog.Warn("gstengine: teardown deferred because an earlier abandoned state change may still be driving this branch's elements; leaving them in the pipeline rather than racing it", "branch", b.id)
		b.engine.markTeardownDeferred()
		b.silenceDeferredBranch()
		return errTeardownDeferredForRace
	}

	// teardownTimeout as well as ctx, never ctx alone: a caller with no
	// deadline of its own would otherwise wait out a GStreamer NULL
	// transition that never returns, and hold this pipeline's teardown
	// turn with it.
	stateCtx, cancel := context.WithTimeout(ctx, teardownTimeout)
	defer cancel()
	if err := b.setElementsState(stateCtx, gst.StateNull); err != nil {
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
	bin, ok := b.engine.pipeline.(gst.Bin)
	if ok {
		for k, pad := range b.mixerPads() {
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
	// Unindexed only once the elements are out of the pipeline: until
	// then a bus error they posted must still attribute back to this
	// branch rather than reading as the shared pipeline's own fault.
	b.engine.unindexBranch(b)

	b.mu.Lock()
	b.released = true
	b.mu.Unlock()
	return nil
}
