//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstcontroller"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// branch is one session's playback chain: a flat set of elements living
// directly in the engine's single output pipeline, from a file source
// through decode to one request pad on each of the program channel
// mixers it feeds. There is no per-branch sub-pipeline and no ghost pad —
// everything is a direct sibling in the same top-level bin, which is what
// lets a branch's pads link straight to the shared channel mixers.
type branch struct {
	id     uint64
	engine *Engine

	filesrc       gst.Element
	decodebin     gst.Element
	audioconvert  gst.Element
	audioresample gst.Element
	capsfilter    gst.Element
	volume        gst.Element
	queue         gst.Element
	deinterleave  gst.Element

	filesrcName   string
	decodebinName string

	// path is the resolved media file build linked filesrc to, read by
	// swapToPosition to build a replacement for the same media.
	path string

	// elementNames holds the GStreamer names of every element in
	// elements(), computed ahead of construction so [Engine.indexBranch]
	// can register them before the elements themselves exist — see that
	// method's doc comment for why.
	elementNames []string

	channelMixerPads    []gst.Pad // index k links to engine.channelMixers[k]
	deinterleaveSrcPads []gst.Pad // index k is deinterleave src_%u, the pad resyncMixerPads offsets
	linkedCount         atomic.Int32

	readyCh   chan struct{}
	readyOnce sync.Once
	loadErrCh chan error

	// holdProbeID is the buffer-only BLOCK probe on queue's own SRC pad
	// that parks this branch's data off the mixer until join removes it.
	// Guarded by mu; 0 once removed.
	holdProbeID uint32

	// heldSeq counts every buffer the hold probe has captured. prepare
	// snapshots it before seeking and waits for it to advance, telling a
	// post-seek buffer apart from one already parked before the seek.
	heldSeq atomic.Uint64

	// joined is true once join has linked this branch to the mixer.
	// Start, Seek, and Resume flush-seek in place only while false; once
	// true they swap in a replacement instead (see methods.go).
	joined bool

	// eosSeq counts every real EOS onEOS has observed, incremented before
	// its early return, so a Completed branch's later re-prepare can
	// still tell a fresh EOS apart from one already accounted for.
	eosSeq atomic.Uint64

	media    pkgaudio.MediaRef
	duration time.Duration

	mu       sync.Mutex
	state    pkgaudio.State
	frozen   bool // true when Position must come from frozenAt, not a live query
	frozenAt time.Duration

	// renderedPos is the PTS of the last buffer actually seen at volume's
	// own sink pad, kept current for the branch's whole lifetime by the
	// probe build installs. Pause freezes at this rather than at
	// queryPosition's live result, which resolves upstream toward the
	// source and over-reports by however far decode is running ahead.
	renderedPos time.Duration

	// segmentStart is the branch position the current GStreamer segment
	// began at: 0 until the first seek, then whatever position the most
	// recent seek targeted. resyncMixerPads uses it to translate a
	// resumed position into the shared pipeline's running time.
	segmentStart time.Duration

	// anchorUnknown is set once a seek's or a PLAYING transition's own
	// ctx deadline fires before its underlying call returns: the call was
	// still issued (or, for a transition, the mixer pads were already
	// re-anchored ahead of it) and can land later in the abandoned
	// goroutine with no way for this package to learn if or when it did,
	// so segmentStart or the mixer pad offsets can no longer be trusted
	// to match GStreamer's real segment. It never clears; see
	// errAnchorUnknown.
	anchorUnknown bool

	fadeActive bool
	// fadeStartPos anchors both the GstController ramp itself and
	// fadeArrived's completion bound in the branch's own raw stream
	// position, not segmentStart-relative local running time, which
	// seekTo resets on every seek including one back to where a paused
	// branch already was.
	//
	// It needs no correction for the gap fadeSyncedPos exists to fix:
	// the controller is anchored to fadeStartPos exactly as given, so it
	// already matches fadeSyncedPos's own clock; that gap was entirely
	// on the other side of fadeArrived's subtraction.
	fadeStartPos time.Duration
	// fadeStartGain is the ramp's value at fadeStartPos, recorded once
	// when the fade starts: reproducing the curve later (a swap
	// replacement) must use this, never gain sampled after it decayed.
	fadeStartGain  pkgaudio.Gain
	fadeDuration   time.Duration
	fadeTargetGain pkgaudio.Gain

	// fadeSyncedPos is the PTS of the last buffer actually seen at
	// volume's own sink pad while a fade is active: the exact input
	// GstController evaluates the "volume" property against for that
	// buffer, captured directly rather than approximated. queryPosition
	// is not a substitute for it: a position query on volume resolves
	// upstream toward the source and consistently answers with the
	// buffer volume is about to receive, one buffer period ahead of the
	// one it has actually synced against, which lets fadeArrived see an
	// elapsed bound that has not really been reached yet. fadeSyncedPos
	// only has a meaningful value while fadeSyncProbeID is nonzero.
	fadeSyncedPos time.Duration
	// fadeSyncProbeID is the pad probe on volume's sink pad maintaining
	// fadeSyncedPos, installed by startFade and removed the moment
	// fadeActive next clears (cancelFade, or observe's own arrival
	// check) so the probe's per-buffer cost exists only for the
	// duration of an actual fade, not for a branch's whole lifetime.
	fadeSyncProbeID uint32

	// blockProbeID is the pad probe holding this branch's own contribution
	// to the mix at queue's sink pad, or 0 when flow is not blocked. It is
	// how Pause and Stop genuinely halt data flow inside a pipeline that
	// stays PLAYING: unlike SetState(PAUSED) on a sibling of a PLAYING
	// bin, a blocking probe is authoritative regardless of the parent
	// bin's own state.
	blockProbeID uint32

	// pendingStateChanges counts calls to setElementsState whose own ctx
	// deadline fired before the underlying GStreamer call returned: that
	// call keeps running against this branch's elements in the
	// background. teardown must not touch a pad or an element while this
	// is nonzero; see awaitNoElementRace.
	pendingStateChanges atomic.Int32

	// released is true once teardown has actually removed this branch's
	// elements from the pipeline: the only outcome teardown caches. A
	// deferred attempt (see errTeardownDeferredForRace) is deliberately
	// not cached, so a retried teardown re-checks pendingStateChanges
	// fresh rather than repeating a stale refusal forever after the
	// condition that caused it has actually cleared.
	released bool

	// teardownClaimed is true from the moment teardown's real attempt
	// first starts, whether or not that attempt ends up succeeding.
	// blockFlow needs this rather than released: teardown releases the
	// flow block as its very first act, so a blockFlow that ran
	// concurrently after that point would reinstall a block nothing is
	// ever going to clear again, parking a streaming thread inside the
	// probe for the life of the process.
	teardownClaimed bool

	// teardownGate admits one caller at a time into doTeardown: if two
	// callers ever held this branch at once, both would otherwise pass
	// the released check while it is still false and both would run
	// setElementsState(NULL), ReleaseRequestPad, and bin.Remove over the
	// same elements and request pads. Lazily initialized under b.mu (see
	// teardown) rather than at construction, since a branch built by a
	// test literal has no constructor to call. See teardown's doc
	// comment for the invariant this buys.
	teardownGate chan struct{}
}

// newTeardownGate returns a one-slot gate ready for immediate acquire,
// for use as branch.teardownGate.
func newTeardownGate() chan struct{} {
	return make(chan struct{}, 1)
}

// elements returns every GStreamer element this branch owns, in link
// order, for state changes and teardown.
func (b *branch) elements() []gst.Element {
	return []gst.Element{
		b.filesrc, b.decodebin, b.audioconvert, b.audioresample,
		b.capsfilter, b.volume, b.queue, b.deinterleave,
	}
}

// isAudioPad reports whether pad's negotiated or proposed caps name an
// audio media type — decodebin can add a video pad for a file that
// carries one, and only the audio pad belongs in this chain.
func isAudioPad(pad gst.Pad) bool {
	caps := pad.GetCurrentCaps()
	if caps == nil {
		caps = pad.QueryCaps(nil)
	}
	if caps == nil || caps.GetSize() == 0 {
		return false
	}
	name := caps.GetStructure(0).GetName()
	return len(name) >= 5 && name[:5] == "audio"
}

// queueMaxSizeTime bounds how far this branch's queue may let decode run
// ahead of the mixer's real-time consumption; see its use in build.
const queueMaxSizeTime = 100 * time.Millisecond

// build constructs and links every element this branch owns except the
// dynamic pads decodebin and deinterleave create once they know their
// input: filesrc/decodebin's audio pad links to audioconvert as soon as
// it appears, and each of deinterleave's N mono src pads links to its
// program channel's mixer as soon as it appears. Both are wired here via
// pad-added callbacks; build itself only returns once every element is
// created, added to the pipeline, and every static-pad link is made.
func (b *branch) build(path string) error {
	e := b.engine
	n := len(e.cfg.ProgramChannels)
	b.path = path

	name := func(role string) string { return fmt.Sprintf("h%d-%s", b.id, role) }
	b.filesrcName = name("filesrc")
	b.decodebinName = name("decodebin")
	b.elementNames = []string{
		b.filesrcName, b.decodebinName,
		name("audioconvert"), name("audioresample"), name("capsfilter"),
		name("volume"), name("queue"), name("deinterleave"),
	}
	e.indexBranch(b)

	b.filesrc = gst.ElementFactoryMake("filesrc", b.filesrcName)
	b.decodebin = gst.ElementFactoryMake("decodebin", b.decodebinName)
	b.audioconvert = gst.ElementFactoryMake("audioconvert", name("audioconvert"))
	b.audioresample = gst.ElementFactoryMake("audioresample", name("audioresample"))
	b.capsfilter = gst.ElementFactoryMake("capsfilter", name("capsfilter"))
	b.volume = gst.ElementFactoryMake("volume", name("volume"))
	b.queue = gst.ElementFactoryMake("queue", name("queue"))
	b.deinterleave = gst.ElementFactoryMake("deinterleave", name("deinterleave"))
	for _, el := range b.elements() {
		if el == nil {
			return fmt.Errorf("gstengine: could not construct a branch element (registry check at construction should have caught this)")
		}
	}

	b.filesrc.SetObjectProperty("location", path)
	b.capsfilter.SetObjectProperty("caps", gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d", e.cfg.SampleRate, n)))
	b.volume.SetObjectProperty("volume", 1.0)
	// queue's byte/buffer caps are disabled so only queueMaxSizeTime
	// bounds how far decode may run ahead of the mixer's consumption.
	b.queue.SetObjectProperty("max-size-buffers", uint32(0))
	b.queue.SetObjectProperty("max-size-bytes", uint32(0))
	b.queue.SetObjectProperty("max-size-time", uint64(queueMaxSizeTime.Nanoseconds()))

	bin, ok := e.pipeline.(gst.Bin)
	if !ok {
		return fmt.Errorf("gstengine: engine pipeline is not a gst.Bin")
	}
	for _, el := range b.elements() {
		if !bin.Add(el) {
			return fmt.Errorf("gstengine: could not add branch element %q to pipeline", el.GetName())
		}
	}
	if !b.filesrc.Link(b.decodebin) {
		return fmt.Errorf("gstengine: could not link filesrc to decodebin")
	}
	if !b.audioconvert.Link(b.audioresample) || !b.audioresample.Link(b.capsfilter) ||
		!b.capsfilter.Link(b.volume) || !b.volume.Link(b.queue) || !b.queue.Link(b.deinterleave) {
		return fmt.Errorf("gstengine: could not link branch decode chain")
	}

	// Mixer sink pads are requested only at join, not here: a branch
	// that has never joined must be seekable entirely off the mixer.
	b.channelMixerPads = make([]gst.Pad, n)
	b.deinterleaveSrcPads = make([]gst.Pad, n)

	b.readyCh = make(chan struct{})
	b.loadErrCh = make(chan error, 1)

	b.decodebin.Connect("pad-added", func(self gst.Element, pad gst.Pad) {
		if !isAudioPad(pad) {
			return
		}
		sinkPad := b.audioconvert.GetStaticPad("sink")
		if sinkPad.IsLinked() {
			return
		}
		if pad.Link(sinkPad) != gst.PadLinkOK {
			select {
			case b.loadErrCh <- fmt.Errorf("%w: decodebin produced an audio pad that would not link", pkgaudio.ErrEngineDecodeFailure):
			default:
			}
		}
	})

	b.deinterleave.Connect("pad-added", func(self gst.Element, pad gst.Pad) {
		idx, ok := deinterleavePadIndex(pad.GetName())
		if !ok || idx < 0 || idx >= n {
			return
		}
		// Not linked to a mixer here: join does that once this branch is
		// ready to actually feed the mix (see join in this file).
		b.deinterleaveSrcPads[idx] = pad
		if b.linkedCount.Add(1) == int32(n) {
			b.maybeReady()
		}
	})

	// Keep renderedPos current for Pause: see its own field comment for
	// why this is more trustworthy than a live queryPosition call.
	b.volume.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			b.mu.Lock()
			b.renderedPos = time.Duration(buf.PTS())
			b.mu.Unlock()
		}
		return gst.PadProbeOK
	})

	// Watch this branch's own contribution to the mix for its natural
	// end: an EOS event on the queue's src pad is this branch finishing
	// on its own, distinct from a pipeline-wide EOS this engine never
	// expects to see (the shared output pipeline never runs out of
	// input — silence and other branches keep it alive).
	queueSrc := b.queue.GetStaticPad("src")
	queueSrc.AddProbe(gst.PadProbeTypeEventDownstream, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev != nil && ev.GetType() == gst.EventEOS {
			b.onEOS()
		}
		return gst.PadProbeOK
	})

	// The hold (see holdProbeID): buffer-only, so events still reach
	// deinterleave and let it create its pads, but no buffer crosses
	// until join removes the probe.
	b.holdProbeID = queueSrc.AddProbe(gst.PadProbeTypeBlock|gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		b.heldSeq.Add(1)
		b.maybeReady()
		return gst.PadProbeOK
	})

	return nil
}

// maybeReady closes readyCh once every deinterleave src pad exists and
// a buffer is parked at the hold. Called from both signals; readyOnce
// makes the double-call safe.
func (b *branch) maybeReady() {
	n := int32(len(b.deinterleaveSrcPads))
	if n == 0 {
		return
	}
	if b.linkedCount.Load() == n && b.heldSeq.Load() >= 1 {
		b.readyOnce.Do(func() { close(b.readyCh) })
	}
}

// deinterleavePadIndex parses deinterleave's "src_%u" pad name into its
// numeric channel index.
func deinterleavePadIndex(name string) (int, bool) {
	const prefix = "src_"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return 0, false
	}
	var n int
	for _, c := range name[len(prefix):] {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func (b *branch) reportLoadError(err error) {
	select {
	case b.loadErrCh <- err:
	default:
	}
}

func (b *branch) onEOS() {
	b.eosSeq.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == pkgaudio.StateStopped || b.state == pkgaudio.StateCompleted {
		return
	}
	b.state = pkgaudio.StateCompleted
	b.frozen = true
	b.frozenAt = b.duration
}

// setElementsState sets every branch element to state, bounded by ctx.
// A call abandoned to ctx's deadline keeps running against this branch's
// elements in the background: cgo has no mechanism to interrupt it, so
// pendingStateChanges stays incremented until it actually finishes,
// letting teardown (see awaitNoElementRace) tell whether one of these
// may still be touching this branch's elements before it starts
// touching them itself.
func (b *branch) setElementsState(ctx context.Context, state gst.State) error {
	b.pendingStateChanges.Add(1)
	done := make(chan error, 1)
	go func() {
		done <- setElementsStateNow(b, state)
	}()
	select {
	case err := <-done:
		b.pendingStateChanges.Add(-1)
		return err
	case <-ctx.Done():
		go func() {
			<-done
			b.pendingStateChanges.Add(-1)
		}()
		return ctx.Err()
	}
}

// setElementsStateNow runs the actual GStreamer state change for every
// element b owns, with no bound of its own; setElementsState is what
// bounds the caller's wait for it.
func setElementsStateNow(b *branch, state gst.State) error {
	for _, el := range stateChangeOrder(b.elements(), state) {
		if el.SetState(state) == gst.StateChangeFailure {
			return fmt.Errorf("%w: element %q refused to reach state %v", pkgaudio.ErrEnginePipelineCrash, el.GetName(), state)
		}
	}
	return nil
}

// stateChangeOrder is the order to walk els in for a transition to
// state: downstream first on the way up, source first on the way down,
// the same direction a GstBin uses for its own children. Bringing the
// source up first instead activates its src pad in push mode and starts
// a streaming task, which then races decodebin's switch to pull mode;
// when the task wins that race its push is refused and filesrc posts
// "Internal data stream error" against a branch that is still loading.
// The direction is read off the target state, which is correct only
// while NULL is the sole downward transition this package makes.
func stateChangeOrder(els []gst.Element, state gst.State) []gst.Element {
	if state == gst.StateNull {
		return els
	}
	out := make([]gst.Element, len(els))
	for i, el := range els {
		out[len(els)-1-i] = el
	}
	return out
}

// awaitNoElementRace blocks until no setElementsState call, including
// one abandoned to its own ctx's deadline, is still running against
// this branch's elements, or until ctx is done or timeout elapses,
// whichever comes first. Bounding by ctx as well as timeout is what
// keeps a caller like Release from being held past its own deadline: a
// caller holding a lock across Release must never be stalled for the
// full timeout merely because ctx asked for less. It reports whether
// pendingStateChanges actually drained.
func (b *branch) awaitNoElementRace(ctx context.Context, timeout time.Duration) bool {
	// ctx.Done() alone already unblocks on both a deadline and an
	// explicit cancel, so deadline here only needs to track timeout: a
	// separate ctx.Deadline() narrowing would be redundant with the
	// select's ctx.Done() case below, not an independent second bound.
	deadline := time.Now().Add(timeout)
	for {
		if b.pendingStateChanges.Load() == 0 {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return b.pendingStateChanges.Load() == 0
		}
		wait := 5 * time.Millisecond
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return b.pendingStateChanges.Load() == 0
		case <-time.After(wait):
		}
	}
}

// checkAnchorKnown returns errAnchorUnknown once a prior timed-out seek
// or PLAYING transition has made this branch's anchoring unreliable;
// see anchorUnknown. Every method that would anchor the mixer or a fade
// to segmentStart calls this before doing so.
func (b *branch) checkAnchorKnown() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.anchorUnknown {
		return errAnchorUnknown
	}
	return nil
}

// markAnchorUnknownOnCtxTimeout sets anchorUnknown when err is exactly
// the caller's own ctx giving up (a deadline or an explicit cancel),
// never for a genuine GStreamer refusal returned by the underlying call
// itself. Callers use this after any step that mutated segmentStart or
// the mixer pad offsets unconditionally ahead of a state change whose
// own ctx can still time out: seekTo's seek, and Start/Resume's
// transition to PLAYING after their own resync already ran.
func (b *branch) markAnchorUnknownOnCtxTimeout(err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		b.mu.Lock()
		b.anchorUnknown = true
		b.mu.Unlock()
	}
}

// queryPosition returns the branch's live position, or the last frozen
// position when the branch is frozen. The live query targets volume, not
// the GstBin decodebin, which has been measured racing real time by seconds.
func (b *branch) queryPosition() time.Duration {
	b.mu.Lock()
	frozen, frozenAt := b.frozen, b.frozenAt
	b.mu.Unlock()
	if frozen {
		return frozenAt
	}
	ns, ok := b.volume.QueryPosition(gst.FormatTime)
	if !ok || ns < 0 {
		return frozenAt
	}
	return time.Duration(ns)
}

// renderedPosition returns renderedPos, the actually-rendered position
// Pause freezes at instead of queryPosition's read-ahead result.
func (b *branch) renderedPosition() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.renderedPos
}

// localRunningTime returns atPos translated into the running time this
// branch's own elements (everything upstream of the mixer sink pads, the
// volume element included) actually run on: elapsed time since the
// current segment began, unaffected by resyncMixerPads' pad offset.
func (b *branch) localRunningTime(atPos time.Duration) time.Duration {
	b.mu.Lock()
	segmentStart := b.segmentStart
	b.mu.Unlock()
	local := atPos - segmentStart
	if local < 0 {
		local = 0
	}
	return local
}

// pipelineRunningTime returns the shared output pipeline's current
// running time, or 0 if the pipeline has none yet (before it first
// reaches PLAYING). This is the clock GstController evaluates every
// control binding against, independent of any single branch's own state.
func (b *branch) pipelineRunningTime() time.Duration {
	rt := b.engine.pipeline.GetCurrentRunningTime()
	if rt == gst.ClockTimeNone {
		return 0
	}
	return time.Duration(rt)
}

// resyncMixerPads re-anchors this branch's deinterleave src pads so the
// next buffer lands at the pipeline's current running time, not in
// GstAudioAggregator's past. join is the only caller, while held.
func (b *branch) resyncMixerPads(atPos time.Duration) {
	offset := int64(b.pipelineRunningTime()) - b.localRunningTime(atPos).Nanoseconds()
	// A pad offset is only reliable on a source pad, so this offsets deinterleave's src pads, not the mixer's sink pads.
	for _, pad := range b.deinterleaveSrcPads {
		if pad != nil {
			pad.SetOffset(offset)
		}
	}
}

// prepare seeks to position, then waits, bounded only by ctx, for the
// first post-seek buffer to reach the hold, or for that seek to land at
// or past EOS instead; reachedEOS tells the two apart, since onEOS has
// already set state and frozenAt for the EOS case by the time this
// returns. When the seek instead holds a buffer, prepare resets a
// leftover Completed state from an earlier EOS back to Ready, matching
// what a freshly built, never-joined branch would report. Run only on a
// branch not yet joined. A ctx-driven failure from either step marks
// anchorUnknown.
func (b *branch) prepare(ctx context.Context, position time.Duration) (reachedEOS bool, err error) {
	b.unblockFlow()
	beforeHeld := b.heldSeq.Load()
	beforeEOS := b.eosSeq.Load()
	if err := b.seekTo(ctx, position, nil); err != nil {
		return false, err
	}
	reachedEOS, err = b.awaitHeldOrEOS(ctx, beforeHeld, beforeEOS)
	if err != nil {
		b.markAnchorUnknownOnCtxTimeout(err)
		return false, err
	}
	if !reachedEOS {
		b.mu.Lock()
		if b.state == pkgaudio.StateCompleted {
			b.state = pkgaudio.StateReady
		}
		b.mu.Unlock()
	}
	return reachedEOS, nil
}

// awaitHeldOrEOS blocks until heldSeq advances past beforeHeld (a buffer
// reached the hold), eosSeq advances past beforeEOS (the seek landed at
// or past the last sample, so no buffer will ever be held), or ctx ends.
// No fixed timeout, the same rule awaitNoElementRace follows.
func (b *branch) awaitHeldOrEOS(ctx context.Context, beforeHeld, beforeEOS uint64) (reachedEOS bool, err error) {
	for {
		if b.heldSeq.Load() > beforeHeld {
			return false, nil
		}
		if b.eosSeq.Load() > beforeEOS {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// join requests mixer sink pads, resyncs offsets at atPos, links them,
// and releases the hold only when releaseHold is true: for a paused or
// stopped target, only the retained hold keeps queue's buffer silent.
func (b *branch) join(atPos time.Duration, releaseHold bool) error {
	e := b.engine
	n := len(e.cfg.ProgramChannels)
	for k := 0; k < n; k++ {
		pad := e.channelMixers[k].RequestPadSimple("sink_%u")
		if pad == nil {
			return fmt.Errorf("gstengine: channel mixer %d refused a sink pad request during join", k)
		}
		b.channelMixerPads[k] = pad
	}
	b.resyncMixerPads(atPos)
	for k := 0; k < n; k++ {
		if b.deinterleaveSrcPads[k].Link(b.channelMixerPads[k]) != gst.PadLinkOK {
			return fmt.Errorf("gstengine: could not link deinterleave output %d to its channel mixer during join", k)
		}
	}
	if releaseHold {
		b.removeHold()
	}
	b.mu.Lock()
	b.joined = true
	b.mu.Unlock()
	return nil
}

// removeHold detaches the hold probe join installed at build, letting
// this branch's buffers reach its now-linked mixer pads. A no-op once
// already removed.
func (b *branch) removeHold() {
	b.mu.Lock()
	id := b.holdProbeID
	b.holdProbeID = 0
	b.mu.Unlock()
	if id != 0 {
		b.queue.GetStaticPad("src").RemoveProbe(id)
	}
}

// removeHoldForTeardown starts a flush on queue's src pad and leaves it
// flushing, so any held or in-flight buffer is dropped instead of
// reaching deinterleave's still-unlinked pads (NOT_LINKED). It never
// sends FLUSH_STOP: that could clear FLUSHING before a woken streaming
// thread rechecks it, letting a held buffer through anyway. NULL state
// clears the flag on deactivation.
func (b *branch) removeHoldForTeardown() {
	b.mu.Lock()
	id := b.holdProbeID
	b.mu.Unlock()
	if id == 0 {
		return
	}
	pad := b.queue.GetStaticPad("src")
	pad.PushEvent(gst.NewEventFlushStart())
	b.removeHold()
}

// hasJoined reports whether join has already linked this branch to the
// shared mixers, which Start, Seek, and Resume use to choose between an
// in-place flush-seek and a swap (see methods.go).
func (b *branch) hasJoined() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.joined
}

func (b *branch) currentGain() pkgaudio.Gain {
	v := b.volume.(gst.Object).ObjectProperty("volume")
	f, _ := v.(float64)
	return pkgaudio.Gain(f)
}

// fadeGainTolerance is how close currentGain must be to a fade's target
// before the elapsed-duration completion bound in observe may treat the
// fade as arrived, rather than merely timed out.
const fadeGainTolerance = 1e-3

// observe collects a fresh [agentaudio.EngineObservation] as of now,
// which must be called strictly after any state-changing action this
// observation reports on.
func (b *branch) observe(now time.Time) agentaudio.EngineObservation {
	pos := b.queryPosition()
	gain := b.currentGain()

	b.mu.Lock()
	state := b.state
	fadeActive := b.fadeActive
	arrived := fadeActive && fadeArrived(b.fadeSyncedPos, b.fadeStartPos, b.fadeDuration, gain, b.fadeTargetGain)
	if arrived {
		fadeActive = false
		b.fadeActive = false
	}
	b.mu.Unlock()
	if arrived {
		b.removeFadeSyncProbe()
	}

	return agentaudio.EngineObservation{
		State:      state,
		Position:   pos,
		ObservedAt: now,
		Gain:       gain,
		FadeActive: fadeActive,
	}
}

// fadeArrived reports whether a fade started at fadeStartPos with
// fadeDuration should clear FadeActive: syncedPos, the PTS of the buffer
// actually synced through volume (see fadeSyncedPos), must have advanced
// the fade's own duration past fadeStartPos AND Gain must actually equal
// target (see docs/build/BUILD-LOG.md for why the bound is stream
// position, not local running time or the shared pipeline's wall clock).
// syncedPos, not a fresh queryPosition() call: a position query on
// volume resolves upstream toward the source and answers with whatever
// buffer volume is about to receive, one buffer period ahead of the one
// it has actually synced its gain against, which previously let this
// bound see an elapsed duration that had not really been reached yet. A
// fade whose own clock says is due but whose gain has not arrived stays
// reported in progress rather than falsely complete: a stuck pending
// fade must be visible, a falsely completed one must not.
func fadeArrived(syncedPos, fadeStartPos, fadeDuration time.Duration, gain, target pkgaudio.Gain) bool {
	elapsed := syncedPos-fadeStartPos >= fadeDuration
	return elapsed && gainWithin(gain, target, fadeGainTolerance)
}

// gainWithin reports whether g is within tolerance of target.
func gainWithin(g, target pkgaudio.Gain, tolerance float64) bool {
	diff := float64(g - target)
	if diff < 0 {
		diff = -diff
	}
	return diff <= tolerance
}

// currentState returns b.state. Seek uses this to keep a swap's
// replacement in whatever state the branch it replaces was already in,
// since Seek's own contract never changes it.
func (b *branch) currentState() pkgaudio.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *branch) setState(s pkgaudio.State) {
	b.mu.Lock()
	b.state = s
	b.mu.Unlock()
}

// hasStarted reports whether Start has ever brought this branch out of its
// post-Load StateReady. A branch that has never started is still prerolled
// and decoding, which is what makes a fade issued against it replay once
// Start's flushing seek resets the segment — see startFade's caller in Fade.
func (b *branch) hasStarted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state != pkgaudio.StateReady
}

// blockFlow halts this branch's contribution to the mix by blocking
// queue's sink pad, where volume's output enters queue. Blocking there
// parks the thread doing the pushing: decodebin's own streaming thread,
// which runs the whole convert/resample/capsfilter/volume chain
// synchronously, so decode itself stops immediately, not merely a tap
// further downstream. queue sits on the far side of the block: it keeps
// draining whatever it already held (bounded by queueMaxSizeTime), which
// is the small amount of audio that still reaches the mixer right after
// Pause, not a delay before decode actually stops. Idempotent: a no-op
// once already blocked, and once teardown has claimed the branch.
func (b *branch) blockFlow() {
	b.mu.Lock()
	if b.teardownClaimed || b.released || b.blockProbeID != 0 {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	pad := b.queue.GetStaticPad("sink")
	id := pad.AddProbe(gst.PadProbeTypeBlockDownstream, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		return gst.PadProbeOK
	})

	b.mu.Lock()
	if b.teardownClaimed || b.released {
		b.mu.Unlock()
		pad.RemoveProbe(id)
		return
	}
	b.blockProbeID = id
	b.mu.Unlock()
}

// unblockFlow releases a block installed by blockFlow, letting this
// branch's data flow resume. A no-op when nothing is blocked.
func (b *branch) unblockFlow() {
	b.mu.Lock()
	id := b.blockProbeID
	b.blockProbeID = 0
	b.mu.Unlock()
	if id != 0 {
		b.queue.GetStaticPad("sink").RemoveProbe(id)
	}
}

// silenceDeferredBranch mutes a branch whose teardown just deferred.
// doTeardown always unblocks its flow ahead of the state change it may
// go on to abandon (see doTeardown's own comment on that ordering), so a
// deferred branch is left unblocked and possibly still PLAYING, holding
// its mixer request pads: without this it can keep sounding under
// whatever the session loads next. A property set on volume needs no
// state-changing call of its own, so it cannot race the abandoned
// SetState this branch's elements may still be driving. cancelFade runs
// first: a live GstController binding re-syncs "volume" from its own
// interpolation source on the pipeline's next buffer regardless of what
// this call just set, which would silently un-mute the branch again.
func (b *branch) silenceDeferredBranch() {
	b.cancelFade()
	b.volume.SetObjectProperty("volume", float64(0))
}

func (b *branch) freezeAt(pos time.Duration) {
	b.mu.Lock()
	b.frozen = true
	b.frozenAt = pos
	b.mu.Unlock()
}

func (b *branch) unfreeze() {
	b.mu.Lock()
	b.frozen = false
	b.mu.Unlock()
}

// startFade dispatches a fade sampling the branch's own current gain as
// the ramp's start value. See startFadeFrom.
func (b *branch) startFade(fade pkgaudio.Fade, basePos time.Duration) error {
	return b.startFadeFrom(fade, basePos, b.currentGain())
}

// startFadeFrom ramps from startGain at basePos to fade.TargetGain at
// basePos+fade.Duration, keyed on buffer PTS. Use NewDirectControlBindingAbsolute,
// not New(), which maps onto 0..10 and turns a gain of 1.0 into a 10x boost.
func (b *branch) startFadeFrom(fade pkgaudio.Fade, basePos time.Duration, startGain pkgaudio.Gain) error {
	volObj := b.volume.(gst.Object)
	volObj.SetControlBindingDisabled("volume", false)

	cs := gstcontroller.NewInterpolationControlSource()
	tvcs, ok := cs.(gstcontroller.TimedValueControlSource)
	if !ok {
		return fmt.Errorf("gstengine: interpolation control source does not implement TimedValueControlSource")
	}
	csObj, ok := cs.(gst.Object)
	if !ok {
		return fmt.Errorf("gstengine: interpolation control source does not implement gst.Object")
	}
	csObj.SetObjectProperty("mode", gstcontroller.InterpolationModeLinear)

	base := gst.ClockTime(basePos.Nanoseconds())
	tvcs.Set(base, float64(startGain))
	tvcs.Set(base+gst.ClockTime(fade.Duration), float64(fade.TargetGain))

	binding := gstcontroller.NewDirectControlBindingAbsolute(volObj, "volume", cs)
	if !volObj.AddControlBinding(binding) {
		return fmt.Errorf("gstengine: could not attach fade control binding")
	}

	b.mu.Lock()
	b.fadeActive = true
	b.fadeStartPos = basePos
	b.fadeStartGain = startGain
	b.fadeDuration = fade.Duration
	b.fadeTargetGain = fade.TargetGain
	b.fadeSyncedPos = basePos
	b.mu.Unlock()
	// A superseding fade (see Engine.Fade) must remove the prior fade's
	// probe before installing its own, or the superseded probe keeps
	// paying its per-buffer cost with no fade left to serve.
	b.removeFadeSyncProbe()
	b.installFadeSyncProbe()
	return nil
}

// installFadeSyncProbe attaches a buffer probe to volume's own sink pad
// that keeps fadeSyncedPos current: the PTS of the buffer volume has
// actually synced its gain against, as fadeArrived's own doc comment
// explains. Called only from startFade, once per fade.
func (b *branch) installFadeSyncProbe() {
	pad := b.volume.GetStaticPad("sink")
	id := pad.AddProbe(gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			b.mu.Lock()
			b.fadeSyncedPos = time.Duration(buf.PTS())
			b.mu.Unlock()
		}
		return gst.PadProbeOK
	})
	b.mu.Lock()
	b.fadeSyncProbeID = id
	b.mu.Unlock()
}

// removeFadeSyncProbe detaches the probe installFadeSyncProbe attached,
// or is a no-op if none is attached. Called wherever fadeActive next
// clears (cancelFade, or observe's own arrival check) so the probe's
// per-buffer cost lasts only as long as an actual fade does.
func (b *branch) removeFadeSyncProbe() {
	b.mu.Lock()
	id := b.fadeSyncProbeID
	b.fadeSyncProbeID = 0
	b.mu.Unlock()
	if id != 0 {
		b.volume.GetStaticPad("sink").RemoveProbe(id)
	}
}

func (b *branch) cancelFade() {
	volObj := b.volume.(gst.Object)
	volObj.SetControlBindingDisabled("volume", true)
	b.mu.Lock()
	b.fadeActive = false
	b.mu.Unlock()
	b.removeFadeSyncProbe()
}
