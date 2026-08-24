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

	channelMixerPads []gst.Pad // index k links to engine.channelMixers[k]
	linkedCount      atomic.Int32

	readyCh   chan struct{}
	readyOnce sync.Once
	loadErrCh chan error

	eosCh   chan struct{}
	eosOnce sync.Once

	media    pkgaudio.MediaRef
	duration time.Duration

	mu       sync.Mutex
	state    pkgaudio.State
	frozen   bool // true when Position must come from frozenAt, not a live query
	frozenAt time.Duration

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
	// fadeStartPos anchors both the GstController ramp itself and the
	// completion bound in the branch's own raw stream position
	// (queryPosition), not segmentStart-relative local running time.
	// GstController evaluates a buffer's control value against the
	// buffer's own PTS, which is stream time and stays continuous across
	// a same-position seek (Resume's own re-anchor included); local
	// running time does not, since seekTo resets segmentStart to the
	// seek target on every seek, including one back to where a paused
	// branch already was.
	fadeStartPos   time.Duration
	fadeDuration   time.Duration
	fadeTargetGain pkgaudio.Gain

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

	name := func(role string) string { return fmt.Sprintf("h%d-%s", b.id, role) }
	b.filesrcName = name("filesrc")
	b.decodebinName = name("decodebin")
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

	b.channelMixerPads = make([]gst.Pad, n)
	for k := 0; k < n; k++ {
		pad := e.channelMixers[k].RequestPadSimple("sink_%u")
		if pad == nil {
			return fmt.Errorf("gstengine: channel mixer %d refused a sink pad request", k)
		}
		b.channelMixerPads[k] = pad
		pad.AddProbe(gst.PadProbeTypeBlock|gst.PadProbeTypeBuffer, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
			return gst.PadProbeRemove
		})
	}

	b.readyCh = make(chan struct{})
	b.loadErrCh = make(chan error, 1)
	b.eosCh = make(chan struct{})

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
		if pad.Link(b.channelMixerPads[idx]) != gst.PadLinkOK {
			select {
			case b.loadErrCh <- fmt.Errorf("%w: deinterleave output %d would not link to its channel mixer", pkgaudio.ErrEngineDecodeFailure, idx):
			default:
			}
			return
		}
		if b.linkedCount.Add(1) == int32(n) {
			b.readyOnce.Do(func() { close(b.readyCh) })
		}
	})

	// Watch this branch's own contribution to the mix for its natural
	// end: an EOS event on the queue's src pad is this branch finishing
	// on its own, distinct from a pipeline-wide EOS this engine never
	// expects to see (the shared output pipeline never runs out of
	// input — silence and other branches keep it alive).
	b.queue.GetStaticPad("src").AddProbe(gst.PadProbeTypeEventDownstream, func(self gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		ev := info.GetEvent()
		if ev != nil && ev.GetType() == gst.EventEOS {
			b.onEOS()
		}
		return gst.PadProbeOK
	})

	return nil
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
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == pkgaudio.StateStopped || b.state == pkgaudio.StateCompleted {
		return
	}
	b.state = pkgaudio.StateCompleted
	b.frozen = true
	b.frozenAt = b.duration
	b.eosOnce.Do(func() { close(b.eosCh) })
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
	for _, el := range b.elements() {
		if el.SetState(state) == gst.StateChangeFailure {
			return fmt.Errorf("%w: element %q refused to reach state %v", pkgaudio.ErrEnginePipelineCrash, el.GetName(), state)
		}
	}
	return nil
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

// resyncMixerPads re-anchors every channel mixer sink pad this branch
// feeds so the next buffer lands at the shared pipeline's current
// running time rather than in GstAudioAggregator's past, which keeps
// advancing in real time regardless of whether this branch is playing.
// Callers run this synchronously, before the state change that resumes
// data flow. atPos is a value the caller already committed to (a seek
// target, or a position sampled immediately before calling this), so
// only pipelineRunningTime is read here.
//
// This synchronous read is measurably early by however long it then
// takes decode to actually restart and this branch's own queue to
// refill: a deferred version that instead computed and applied the
// offset from inside a probe on each pad's first post-resync buffer was
// attempted and reverted: it regressed TestStartAfterLoadGapPlaysFrom
// NamedPosition and TestSeekAfterGapReanchors outright (the branch build
// pad-added path installs its own first-buffer block probe on the same
// pad, and a second, later-registered one did not reliably still fire
// once the first satisfied the block), and did not improve
// TestResumeDoesNotDiscardTheHeldDuration either. See
// docs/build/BUILD-LOG.md for the measured per-resume loss this leaves
// as a named limitation rather than a silently accepted one.
func (b *branch) resyncMixerPads(atPos time.Duration) {
	offset := int64(b.pipelineRunningTime()) - b.localRunningTime(atPos).Nanoseconds()
	for _, pad := range b.channelMixerPads {
		if pad != nil {
			pad.SetOffset(offset)
		}
	}
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
	if fadeActive && fadeArrived(pos, b.fadeStartPos, b.fadeDuration, gain, b.fadeTargetGain) {
		fadeActive = false
		b.fadeActive = false
	}
	b.mu.Unlock()

	return agentaudio.EngineObservation{
		State:      state,
		Position:   pos,
		ObservedAt: now,
		Gain:       gain,
		FadeActive: fadeActive,
	}
}

// fadeArrived reports whether a fade started at fadeStartPos with
// fadeDuration should clear FadeActive: raw stream position must have
// advanced the fade's own duration past fadeStartPos AND Gain must
// actually equal target (see docs/build/BUILD-LOG.md for why the bound
// is stream position, not local running time or the shared pipeline's
// wall clock). A fade whose own clock says is due but whose gain has not
// arrived stays reported in progress rather than falsely complete: a
// stuck pending fade must be visible, a falsely completed one must not.
func fadeArrived(pos, fadeStartPos, fadeDuration time.Duration, gain, target pkgaudio.Gain) bool {
	elapsed := pos-fadeStartPos >= fadeDuration
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

// gstController is the pair a Fade dispatches: a fresh interpolation
// source and the absolute binding driving b.volume's "volume" property
// from it. NewDirectControlBindingAbsolute is required, not New() — the
// latter maps a 0..1 control value onto the property's full 0..10 range
// and turns a requested gain of 1.0 into a 10x boost (measured in the
// phase 1 spike, bench/audio-node/spike-phase1). basePos is the branch's
// raw stream position when the fade starts, the same clock GstController
// itself evaluates buffers against (their own PTS), not
// segmentStart-relative local running time, which a later seek resets.
func (b *branch) startFade(fade pkgaudio.Fade, basePos time.Duration) error {
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

	start := float64(b.currentGain())
	base := gst.ClockTime(basePos.Nanoseconds())
	tvcs.Set(base, start)
	tvcs.Set(base+gst.ClockTime(fade.Duration), float64(fade.TargetGain))

	binding := gstcontroller.NewDirectControlBindingAbsolute(volObj, "volume", cs)
	if !volObj.AddControlBinding(binding) {
		return fmt.Errorf("gstengine: could not attach fade control binding")
	}

	b.mu.Lock()
	b.fadeActive = true
	b.fadeStartPos = basePos
	b.fadeDuration = fade.Duration
	b.fadeTargetGain = fade.TargetGain
	b.mu.Unlock()
	return nil
}

func (b *branch) cancelFade() {
	volObj := b.volume.(gst.Object)
	volObj.SetControlBindingDisabled("volume", true)
	b.mu.Lock()
	b.fadeActive = false
	b.mu.Unlock()
}
