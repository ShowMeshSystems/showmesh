//go:build cgo

package gstengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	glib "github.com/go-gst/go-glib/pkg/glib/v2"
	gobject "github.com/go-gst/go-glib/pkg/gobject/v2"
	"github.com/go-gst/go-gst/pkg/gst"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// requiredElementFactories are the GStreamer elements the output topology
// needs regardless of configuration. cfg.SinkFactory is checked
// separately since it is configuration, not a fixed requirement.
// interleaveSampleFormat is the sample format every channel negotiates
// immediately before interleave's sink pads, program mixer, silence, and
// LTC alike. interleave requires every sink pad to agree on one format;
// left to negotiate independently, different branches can settle on
// different ones depending on timing, which breaks interleave's per-pad
// width bookkeeping.
const interleaveSampleFormat = "F32LE"

var requiredElementFactories = []string{
	"audiomixer", "interleave", "deinterleave", "audioconvert",
	"audioresample", "capsfilter", "volume", "decodebin", "filesrc",
	"queue", "audiotestsrc", "appsrc",
}

// Engine is the real [agentaudio.Engine] backend: one continuously
// running GStreamer pipeline for the node's audio output device.
// Concurrent sessions are flat branches sharing that one pipeline, mixed
// per program channel and interleaved onto the configured output layout.
type Engine struct {
	cfg Config

	availOK     bool
	availReason string

	pipeline gst.Pipeline

	// channelMixers holds one audiomixer per cfg.ProgramChannels entry,
	// same order, each already linked into the interleave stage.
	channelMixers []gst.Element

	// ltc is non-nil exactly when cfg.LTCChannel != 0, owning that
	// channel's appsrc and the state of whatever LTC run drives it.
	ltc *ltcChannel

	mu      sync.Mutex
	handles map[agentaudio.EngineHandle]*branch

	// elementIndex maps every one of a branch's own element names (all
	// eight from [branch.elements], not only filesrc/decodebin — every
	// one of them is a direct sibling in the shared pipeline, per
	// branch's own doc comment, so any of them can be a bus error's
	// source, not only decodebin's dynamically created internal children)
	// to that branch, for [Engine.branchForSource] to attribute a bus
	// error to the right branch. It is populated as soon as a branch's
	// element names are known — before the elements themselves exist and
	// before the branch is confirmed loaded and added to handles — since
	// a decode error arrives exactly during that unconfirmed window.
	elementIndex map[string]*branch

	nextID atomic.Uint64

	brokenMu     sync.Mutex
	brokenReason string

	// startedAt is this Engine instance's counting epoch for
	// GlitchCounts.Since -- a fresh Engine on every rebind, so a
	// consumer reading a falling count can tell it apart from a reset.
	startedAt time.Time

	// streamWarningCount/resourceWarningCount/otherWarningCount and
	// qosCount are cumulative bus-message counts watchBus has observed
	// since startedAt, classified by GError domain (see
	// classifyWarningDomain). Plain atomics, not mutex-guarded, matching
	// nextID: watchBus runs on its own goroutine and these are read from
	// GlitchCounts by a report loop with no other reason to take e.mu.
	streamWarningCount   atomic.Uint64
	resourceWarningCount atomic.Uint64
	otherWarningCount    atomic.Uint64
	qosCount             atomic.Uint64

	// lastWarningLogNano bounds warning log volume (see watchBus); owned
	// solely by the watchBus goroutine, so a plain field, not an atomic.
	lastWarningLogNano int64

	// anyTeardownIncomplete latches true the moment any branch teardown
	// ever defers (see errTeardownDeferredForRace) or the final pipeline
	// transition to NULL times out. It is engine-scoped rather than
	// derived from Close's own fan-out because a branch can defer during
	// a Release that ran before Close and already deleted the handle
	// from e.handles, so Close's fan-out over the handles it still holds
	// would otherwise never see that branch and would report a false
	// clean Close.
	anyTeardownIncomplete atomic.Bool

	// pipelineStateAtClose is the pipeline's own GStreamer state, read
	// once by Close immediately before releasing its reference to
	// e.pipeline (see releaseBus: go-gst's finalizer does not reliably
	// reclaim a GStreamer object's own internal resources, so Close
	// drops this reference explicitly rather than leaving it for GC,
	// which makes the live object unsafe to query afterward). Zero value
	// when Close's own SetState(NULL) attempt was abandoned or deferred
	// rather than confirmed.
	pipelineStateAtClose gst.State

	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
}

var _ agentaudio.Engine = (*Engine)(nil)
var _ agentaudio.GlitchObserver = (*Engine)(nil)

// New builds and starts the output pipeline described by cfg. It never
// fails on missing plugins, a missing device, or any other environment
// gap — those are reported truthfully by [Engine.Available] instead, so a
// caller can construct one Engine at startup and query it rather than
// handling a constructor error that would otherwise hide the reason.
// New only fails for a structurally invalid cfg.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	gst.Init()

	e := &Engine{
		cfg:          cfg,
		handles:      make(map[agentaudio.EngineHandle]*branch),
		elementIndex: make(map[string]*branch),
		done:         make(chan struct{}),
		startedAt:    time.Now(),
	}

	if reason := e.checkPrerequisites(); reason != "" {
		e.availReason = reason
		return e, nil
	}

	if err := e.buildPipeline(); err != nil {
		e.releaseAfterFailedBuild()
		e.availReason = fmt.Sprintf("could not build output pipeline: %v", err)
		return e, nil
	}
	// Started before the settle window below, not after it: LTC is the
	// only channel whose source is fed from Go, so its own pad does not
	// negotiate until the feeder pushes, and a layout the device refuses
	// on that pad would otherwise surface just after the build had
	// already reported success.
	if e.ltc != nil {
		e.ltc.feedStarted.Store(true)
		go e.runLTCFeeder(e.ltc)
	}
	if err := e.awaitSustainedPlaying(); err != nil {
		e.releaseAfterFailedBuild()
		e.availReason = fmt.Sprintf("could not build output pipeline: %v", err)
		return e, nil
	}

	e.availOK = true
	go e.watchBus()
	return e, nil
}

// releaseAfterFailedBuild returns a partially built pipeline to NULL so
// a build that could not reach sustained playback does not keep holding
// the output device. An engine reporting itself unavailable while its
// ALSA route reads as state OPEN in /proc/asound is unrecoverable short
// of restarting the agent, since nothing else can claim that device and
// no operator action reclaims it. It delegates to [Engine.Close], which
// already stops the LTC feeder before flushing the pipeline to NULL in
// the one order that does not deadlock a feeder blocked under
// backpressure; a later Close by the caller observes the same idempotent
// outcome. Available() keeps reporting the build's own reason rather
// than Close's, since availOK was never set.
func (e *Engine) releaseAfterFailedBuild() {
	if e.pipeline == nil {
		return
	}
	if err := e.Close(); err != nil {
		slog.Warn("gstengine: a failed output pipeline build did not fully release its device", "error", err)
	}
}

// NewUnavailable returns an Engine that was never built and reports
// false with reason from [Engine.Available], refusing every call the way
// any other unavailable Engine does. It exists so a caller that must
// refuse to build at all -- a node whose bound route has no probe
// evidence, where inventing a sample rate or channel count would open a
// real device with a layout it never advertised -- can still bind
// something that states why, rather than leaving nothing bound and
// having the node report a rebuild that is not actually in progress.
// It holds no device and needs no Close, though Close is safe.
func NewUnavailable(reason string) *Engine {
	return &Engine{
		availReason:  reason,
		handles:      make(map[agentaudio.EngineHandle]*branch),
		elementIndex: make(map[string]*branch),
		done:         make(chan struct{}),
	}
}

// checkPrerequisites reports the reason Available() would give right now
// were the pipeline never even attempted, or "" if every fixed and
// configured element is present and the sink is constructible.
func (e *Engine) checkPrerequisites() string {
	var missing []string
	for _, name := range requiredElementFactories {
		if gst.ElementFactoryFind(name) == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("required GStreamer elements not in registry: %s", strings.Join(missing, ", "))
	}
	probe := gst.ElementFactoryMake(e.cfg.SinkFactory, "")
	if probe == nil {
		return fmt.Sprintf("configured sink %q could not be constructed from the GStreamer registry", e.cfg.SinkFactory)
	}
	return ""
}

// Available reports whether this Engine can actually play anything.
// false is only ever backed by a specific reason recorded at
// construction or set by the bus watcher on a pipeline-level failure —
// never a bare false with no explanation, and never true because cfg
// merely claims a device exists.
func (e *Engine) Available() (bool, string) {
	if !e.availOK {
		return false, e.availReason
	}
	e.brokenMu.Lock()
	reason := e.brokenReason
	e.brokenMu.Unlock()
	if reason != "" {
		return false, reason
	}
	return true, ""
}

// closedReason is [Engine.Close]'s standing unavailability reason. A
// closed engine holds no device and must never report itself playable.
const closedReason = "engine was closed and released its output device"

// errCloseIncomplete marks a Close whose teardown did not fully finish
// within its bound: a branch teardown deferred to an abandoned state
// change (whether during this Close's own fan-out, or earlier via a
// Release Close never got to run itself), or the final pipeline
// transition to NULL itself timed out. A caller sees Available()==false
// and every further call refused either way, but this distinguishes
// "torn down cleanly, the device is free" from "teardown abandoned,
// elements or the device itself may still be live", the fact a caller
// rebuilding a replacement engine against the same device needs before
// assuming the device was actually released. No caller in this
// repository consumes that distinction yet. Not wrapped in
// [pkgaudio.ErrEnginePipelineCrash]: Close itself, and the branch or
// pipeline-NULL timeout that produced this, are not the shared pipeline
// reporting its own bus-level failure, the class brokenErr reports, so
// this classifies as [pkgaudio.FaultOther] instead of overloading a
// class that means something more specific elsewhere in this same file.
var errCloseIncomplete = fmt.Errorf("gstengine: Close's teardown did not fully complete before its timeout; some elements or the output device itself may still be live")

// Close tears every branch down, stops the bus watcher, and returns the
// output pipeline to NULL so the device it held is released. Required
// before building a replacement Engine against the same device, since
// the outgoing pipeline keeps the device open until it does. Idempotent:
// a second call observes the same closeErr the first one computed,
// rather than re-running teardown or reporting a different outcome.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		// markBroken's first-write-wins guard is what makes this
		// deterministic: recording closedReason before any teardown step
		// runs guarantees it beats any bus error that step goes on to
		// cause (an unindexed branch mid-teardown, the shared topology
		// during SetState(NULL)), rather than racing watchBus for it.
		e.markBroken(closedReason)
		close(e.done)
		e.mu.Lock()
		branches := make([]*branch, 0, len(e.handles))
		for h, b := range e.handles {
			branches = append(branches, b)
			delete(e.handles, h)
		}
		e.mu.Unlock()
		var wg sync.WaitGroup
		for _, b := range branches {
			wg.Add(1)
			go func(b *branch) {
				defer wg.Done()
				_ = bestEffortTeardown(b)
			}(b)
		}
		wg.Wait()

		// Signal first, then flush the pipeline to NULL: a feeder blocked
		// inside PushBuffer under backpressure only returns once the
		// pipeline stops accepting data.
		if e.ltc != nil {
			close(e.ltc.stopFeed)
		}
		// This still attempts the pipeline's own transition to NULL even
		// when a branch teardown already deferred: skipping it answers
		// the only question a caller like closeReplacedEngine asks,
		// whether the device is free, with a permanent no, converting a
		// branch-scoped element leak into a device-scoped one recoverable
		// only by restarting the process. Setting the bin to NULL does
		// recurse into any still-abandoned child element a deferred
		// branch left attached, which is the same concurrent SetState
		// the branch-level guard exists to avoid causing itself, but this
		// call is bounded by ctx, so the worst case is one bounded wait
		// before reporting incomplete, not an unbounded hang.
		if e.pipeline != nil {
			ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
			err := boundedCall(ctx, func() error {
				if e.pipeline.SetState(gst.StateNull) == gst.StateChangeFailure {
					return errors.New("output pipeline refused to reach NULL")
				}
				return nil
			})
			cancel()
			if err != nil {
				slog.Warn("gstengine: output pipeline did not reach NULL within the shutdown timeout", "error", err)
				e.anyTeardownIncomplete.Store(true)
			} else {
				// See pipelineStateAtClose's doc comment: read before
				// the reference is dropped, not after. Set to nil once
				// released, not left dangling, so any caller that still
				// checks "if e.pipeline != nil" (as this same block
				// does) sees a released pipeline as absent rather than
				// touching a freed object.
				e.pipelineStateAtClose, _, _ = e.pipeline.GetState(0)
				if obj, ok := e.pipeline.(gobject.Object); ok {
					gobject.UnsafeObjectUnref(obj)
				}
				e.pipeline = nil
			}
		}
		if e.ltc != nil {
			if e.ltc.feedStarted.Load() {
				select {
				case <-e.ltc.feedDone:
				case <-time.After(ltcFeederShutdownTimeout):
					slog.Warn("gstengine: LTC feeder did not exit within the shutdown timeout")
				}
			}
			e.ltc.closeEncoder()
		}

		if e.anyTeardownIncomplete.Load() {
			e.closeErr = errCloseIncomplete
		}
	})
	return e.closeErr
}

// markTeardownDeferred latches anyTeardownIncomplete; see its doc
// comment for why this must be engine-scoped rather than derived only
// from Close's own fan-out.
func (e *Engine) markTeardownDeferred() {
	e.anyTeardownIncomplete.Store(true)
}

func (e *Engine) markBroken(reason string) {
	e.brokenMu.Lock()
	if e.brokenReason == "" {
		e.brokenReason = reason
	}
	e.brokenMu.Unlock()
}

// buildPipeline assembles the fixed topology: one audiomixer per program
// channel, an interleave stage with one sink pad requested per output
// channel in ascending 1-based order (request order is what places
// content on a given output channel index — see the phase 1 spike),
// silence on every channel cfg.ProgramChannels does not claim, and the
// configured sink. Channels left silent are deliberately the only shape
// of the LTC seam this package provides.
func (e *Engine) buildPipeline() error {
	pipelineElem := gst.ElementFactoryMake("pipeline", "")
	pipeline, ok := pipelineElem.(gst.Pipeline)
	if !ok {
		return errors.New("could not create a gst.Pipeline")
	}
	bin, ok := pipeline.(gst.Bin)
	if !ok {
		return errors.New("pipeline does not implement gst.Bin")
	}

	interleave := gst.ElementFactoryMake("interleave", "interleave")
	if interleave == nil {
		return errors.New("could not create interleave")
	}

	sink, err := newSinkFactoryElement(e.cfg)
	if err != nil {
		return err
	}
	// qos on: a GstBaseSink-derived sink otherwise never posts a QOS bus
	// message for a buffer it drops or skips to keep pace with the
	// clock, so GlitchCounts.QosEvents would read zero forever
	// regardless of what the pipeline actually does. Only a bin (a
	// test-only sink shape -- see sinkformat_real_integration_test.go)
	// lacks this GstBaseSink property directly; every real named sink
	// factory has it.
	if _, isBin := sink.(gst.Bin); !isBin {
		sink.SetObjectProperty("qos", true)
	}
	// positionBits is one GstAudioChannelPosition bitmask per output
	// channel (see [channelPositionBits]), or nil when the sink itself
	// does not require a positioned layout at e.cfg.ChannelCount --
	// discovered by querying sink's own accepted caps (see
	// [probeSinkChannelPositions]) rather than assumed from the channel
	// count alone. channel-positions-from-input is true only when
	// positionBits is set: each channel's own capsfilter below then
	// carries an explicit single-bit channel-mask, and interleave adopts
	// it verbatim rather than emitting the unpositioned mask a positioned
	// sink (e.g. a raw hw: ALSA route) refuses.
	positionBits := probeSinkChannelPositions(sink, e.cfg.ChannelCount, e.cfg.SampleRate)
	interleave.SetObjectProperty("channel-positions-from-input", len(positionBits) > 0)
	if !bin.Add(interleave) {
		return errors.New("could not add interleave to pipeline")
	}
	if !bin.Add(sink) {
		return errors.New("could not add sink to pipeline")
	}
	if err := linkInterleaveToSink(bin, interleave, sink, e.cfg.ChannelCount, positionBits); err != nil {
		return err
	}

	programSet := make(map[int]int, len(e.cfg.ProgramChannels)) // channel index -> position in ProgramChannels
	for pos, ch := range e.cfg.ProgramChannels {
		programSet[ch] = pos
	}

	channelMixers := make([]gst.Element, len(e.cfg.ProgramChannels))
	for ch := 1; ch <= e.cfg.ChannelCount; ch++ {
		sinkPad := interleave.RequestPadSimple("sink_%u")
		if sinkPad == nil {
			return fmt.Errorf("interleave refused a sink pad request for channel %d", ch)
		}
		var maskBit uint64
		if len(positionBits) > 0 {
			maskBit = positionBits[ch-1]
		}
		if pos, isProgram := programSet[ch]; isProgram {
			mixer := gst.ElementFactoryMake("audiomixer", fmt.Sprintf("mixer-ch%d", ch))
			if mixer == nil {
				return fmt.Errorf("could not create audiomixer for channel %d", ch)
			}
			mixer.SetObjectProperty("ignore-inactive-pads", true)
			if !bin.Add(mixer) {
				return fmt.Errorf("could not add audiomixer for channel %d", ch)
			}
			// Every sink pad interleave collects from must negotiate the
			// same sample format, or its cross-pad width bookkeeping
			// breaks; left unconstrained, this mixer path and the LTC or
			// silence chains on other channels can each independently
			// negotiate a different one. Pinning it here removes the race.
			mixerCaps := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("mixer-caps-ch%d", ch))
			if mixerCaps == nil {
				return fmt.Errorf("could not create mixer output capsfilter for channel %d", ch)
			}
			mixerCaps.SetObjectProperty("caps", gst.CapsFromString(channelCapsString(e.cfg.SampleRate, maskBit)))
			if !bin.Add(mixerCaps) {
				return fmt.Errorf("could not add mixer output capsfilter for channel %d", ch)
			}
			if !mixer.Link(mixerCaps) {
				return fmt.Errorf("could not link channel %d mixer to its output capsfilter", ch)
			}
			if mixerCaps.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
				return fmt.Errorf("could not link channel %d mixer to interleave", ch)
			}
			if err := addMixerKeepAlive(bin, mixer, ch, e.cfg.SampleRate); err != nil {
				return err
			}
			channelMixers[pos] = mixer
			continue
		}

		if ch == e.cfg.LTCChannel {
			ltc, err := newLTCChannel(bin, e.cfg.SampleRate, maskBit)
			if err != nil {
				return fmt.Errorf("could not build LTC chain for channel %d: %w", ch, err)
			}
			if ltc.queue.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
				return fmt.Errorf("could not link LTC chain to interleave for channel %d", ch)
			}
			e.ltc = ltc
			continue
		}

		silenceSrc := gst.ElementFactoryMake("audiotestsrc", fmt.Sprintf("silence-ch%d", ch))
		if silenceSrc == nil {
			return fmt.Errorf("could not create silence source for channel %d", ch)
		}
		silenceSrc.SetObjectProperty("is-live", true)
		silenceSrc.SetObjectProperty("wave", int32(4)) // GST_AUDIO_TEST_SRC_WAVE_SILENCE
		conv := gst.ElementFactoryMake("audioconvert", fmt.Sprintf("silence-conv-ch%d", ch))
		caps := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("silence-caps-ch%d", ch))
		if conv == nil || caps == nil {
			return fmt.Errorf("could not create silence chain for channel %d", ch)
		}
		caps.SetObjectProperty("caps", gst.CapsFromString(channelCapsString(e.cfg.SampleRate, maskBit)))
		for _, el := range []gst.Element{silenceSrc, conv, caps} {
			if !bin.Add(el) {
				return fmt.Errorf("could not add silence element to pipeline for channel %d", ch)
			}
		}
		if !silenceSrc.Link(conv) || !conv.Link(caps) {
			return fmt.Errorf("could not link silence chain for channel %d", ch)
		}
		if caps.GetStaticPad("src").Link(sinkPad) != gst.PadLinkOK {
			return fmt.Errorf("could not link silence chain to interleave for channel %d", ch)
		}
	}

	e.pipeline = pipeline
	e.channelMixers = channelMixers
	if e.ltc != nil {
		e.ltc.bindPipeline(pipeline)
	}

	if pipeline.SetState(gst.StatePlaying) == gst.StateChangeFailure {
		return errors.New("output pipeline refused to reach PLAYING")
	}
	return nil
}

// buildSettleWindow is how long [New] watches the freshly started
// pipeline's own bus before reporting a successful build. A device that refuses the
// negotiated layout accepts the state change and then fails from its
// streaming thread instead, so SetState(PLAYING) succeeding is not
// evidence the route opened -- MEASURED against a sink demanding a
// channel layout the pipeline cannot produce, the error lands roughly
// 30ms after the transition returns. Without this window an engine
// reports available=true and goes unavailable within the same second,
// which reads as a working rebuild that later broke rather than as the
// failed rebuild it is.
const buildSettleWindow = 300 * time.Millisecond

// awaitSustainedPlaying reports the first error a freshly started
// pipeline puts on its own bus within [buildSettleWindow], or nil if it
// is still running at the end of it. Messages consumed here never reach
// [Engine.watchBus], which only starts once this returns nil.
func (e *Engine) awaitSustainedPlaying() error {
	bus := e.pipeline.GetBus()
	defer releaseBus(bus)
	deadline := time.Now().Add(buildSettleWindow)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil
		}
		msg := bus.TimedPop(gst.ClockTime(remaining))
		if msg == nil {
			return nil
		}
		msgType := msg.Type()
		var text string
		if msgType == gst.MessageError {
			text, _ = msg.ParseError()
		}
		gst.UnsafeMessageUnref(msg)
		if msgType == gst.MessageError {
			return fmt.Errorf("output pipeline failed to sustain PLAYING: %s", text)
		}
	}
}

// releaseBus drops this package's own reference to a bus returned by
// GetBus. Each GetBus call takes a fresh reference go-gst's own
// finalizer never reclaims in practice, so every caller must release it
// explicitly rather than letting it go out of scope.
func releaseBus(bus gst.Bus) {
	if obj, ok := bus.(gobject.Object); ok {
		gobject.UnsafeObjectUnref(obj)
	}
}

// newSinkFactoryElement constructs the pipeline's terminal sink element
// from cfg.SinkFactory/cfg.SinkProperties. A package var, matching this
// package's own test-injection convention elsewhere in this codebase
// (e.g. probe.go's runProbeProcess), so a test can substitute a
// caps-restricted sink bin that no single registered element factory
// name alone can express -- see sinkformat_real_integration_test.go.
var newSinkFactoryElement = func(cfg Config) (gst.Element, error) {
	sink := gst.ElementFactoryMake(cfg.SinkFactory, "sink")
	if sink == nil {
		return nil, fmt.Errorf("could not create sink %q", cfg.SinkFactory)
	}
	for k, v := range cfg.SinkProperties {
		sink.SetObjectProperty(k, v)
	}
	return sink, nil
}

// channelCapsString builds the single-channel caps string every chain
// feeding one of interleave's request pads negotiates: the fixed interior
// format and rate, and, when maskBit is nonzero, an explicit
// channel-mask claiming exactly the one [channelPositionBits] position
// assigned to that channel. maskBit is 0 for a sink
// [probeSinkChannelPositions] found no positioned layout for, in which
// case the caps carry no mask and interleave falls back to its own
// unpositioned output.
func channelCapsString(sampleRate int, maskBit uint64) string {
	caps := fmt.Sprintf("audio/x-raw,format=%s,rate=%d,channels=1", interleaveSampleFormat, sampleRate)
	if maskBit != 0 {
		caps += fmt.Sprintf(",channel-mask=(bitmask)0x%x", maskBit)
	}
	return caps
}

// linkInterleaveToSink connects interleave's output to sink through an
// audioconvert/audioresample pair, so the sink negotiates its own format
// and rate rather than being handed the interior pipeline's fixed
// interleaveSampleFormat directly. A discovery probe of the same device
// already runs its throwaway signal through exactly this
// audioconvert!audioresample shape (probe.go's ProbeOutput) before
// reaching alsasink; linking interleave straight to sink instead builds
// a pipeline discovery never actually proved, and a raw hw: route that
// only accepts an integer PCM format (S24_3LE, S32LE, ...) goes
// not-negotiated even though discovery already confirmed it works. At a
// rate the sink already shares, audioresample is a measured passthrough;
// a genuinely different rate is measured to actually resample every
// channel this chain carries, LTC's generated waveform included -- LTC
// is not a special case here, it just rides the same interleave-to-sink
// boundary every other channel does.
//
// channelCount, and positionBits' combined mask when positionBits is
// set, are pinned in a capsfilter placed after the resampler.
// Pinning channelCount alone is not enough once interleave can emit a
// positioned layout: audioconvert refuses to remix one unpositioned
// layout onto another (measured, and still what protects an
// unpositioned channelCount this sink has no fallback for), but given
// two positioned layouts it will build a downmix/remix matrix between
// them rather than refuse -- measured directly: with only channelCount
// pinned, a sink fixed to a positioned mask interleave was NOT assigned
// still negotiates, and each output channel becomes a silent blend of
// several input channels instead of the exact one it was assigned. The
// mask pin turns that into a loud link-time refusal instead, because a
// show channel silently landing on the wrong physical output, or
// several blended together, is worse than this route failing to open.
func linkInterleaveToSink(bin gst.Bin, interleave, sink gst.Element, channelCount int, positionBits []uint64) error {
	convert := gst.ElementFactoryMake("audioconvert", "sink-convert")
	resample := gst.ElementFactoryMake("audioresample", "sink-resample")
	caps := gst.ElementFactoryMake("capsfilter", "sink-caps")
	if convert == nil || resample == nil || caps == nil {
		return errors.New("could not create sink-side format adaptation chain")
	}
	sinkCaps := fmt.Sprintf("audio/x-raw,channels=%d", channelCount)
	if len(positionBits) > 0 {
		var mask uint64
		for _, bit := range positionBits {
			mask |= bit
		}
		sinkCaps += fmt.Sprintf(",channel-mask=(bitmask)0x%x", mask)
	}
	caps.SetObjectProperty("caps", gst.CapsFromString(sinkCaps))
	for _, el := range []gst.Element{convert, resample, caps} {
		if !bin.Add(el) {
			return errors.New("could not add sink format adaptation element to pipeline")
		}
	}
	if !interleave.Link(convert) {
		return errors.New("could not link interleave to sink format converter")
	}
	if !convert.Link(resample) {
		return errors.New("could not link sink format converter to resampler")
	}
	if !resample.Link(caps) {
		return errors.New("could not link sink resampler to channel-count capsfilter")
	}
	if !caps.Link(sink) {
		return errors.New("could not link interleave's format adaptation chain to sink")
	}
	return nil
}

// addMixerKeepAlive gives mixer one permanently connected silent sink pad,
// so a GstAggregator-based mixer with no branch loaded still produces
// output instead of stalling the whole interleave chain downstream of it.
// ignore-inactive-pads, already set on mixer, is what lets a branch's own
// pad go quiet without stalling this one.
func addMixerKeepAlive(bin gst.Bin, mixer gst.Element, ch int, sampleRate int) error {
	src := gst.ElementFactoryMake("audiotestsrc", fmt.Sprintf("keepalive-ch%d", ch))
	conv := gst.ElementFactoryMake("audioconvert", fmt.Sprintf("keepalive-conv-ch%d", ch))
	resample := gst.ElementFactoryMake("audioresample", fmt.Sprintf("keepalive-resample-ch%d", ch))
	caps := gst.ElementFactoryMake("capsfilter", fmt.Sprintf("keepalive-caps-ch%d", ch))
	if src == nil || conv == nil || resample == nil || caps == nil {
		return fmt.Errorf("could not create mixer keep-alive chain for channel %d", ch)
	}
	src.SetObjectProperty("is-live", true)
	src.SetObjectProperty("wave", int32(4)) // GST_AUDIO_TEST_SRC_WAVE_SILENCE
	caps.SetObjectProperty("caps", gst.CapsFromString(fmt.Sprintf("audio/x-raw,format=%s,rate=%d,channels=1", interleaveSampleFormat, sampleRate)))
	for _, el := range []gst.Element{src, conv, resample, caps} {
		if !bin.Add(el) {
			return fmt.Errorf("could not add mixer keep-alive element for channel %d", ch)
		}
	}
	if !src.Link(conv) || !conv.Link(resample) || !resample.Link(caps) {
		return fmt.Errorf("could not link mixer keep-alive chain for channel %d", ch)
	}
	pad := mixer.RequestPadSimple("sink_%u")
	if pad == nil {
		return fmt.Errorf("mixer for channel %d refused a keep-alive sink pad", ch)
	}
	if caps.GetStaticPad("src").Link(pad) != gst.PadLinkOK {
		return fmt.Errorf("could not link mixer keep-alive source for channel %d", ch)
	}
	return nil
}

func (e *Engine) watchBus() {
	bus := e.pipeline.GetBus()
	defer releaseBus(bus)
	for {
		select {
		case <-e.done:
			return
		default:
		}
		msg := bus.TimedPop(gst.ClockTime(200 * time.Millisecond))
		if msg == nil {
			continue
		}
		switch msg.Type() {
		case gst.MessageError:
			text, gerr := msg.ParseError()
			if b := e.branchForSource(msg.Source()); b != nil {
				b.reportLoadError(classifyBranchError(text, gerr))
				gst.UnsafeMessageUnref(msg)
				continue
			}
			e.markBroken(fmt.Sprintf("output pipeline error: %s", text))
		case gst.MessageWarning:
			// Counts unconditionally; classifyWarningDomain and
			// maybeLogWarning explain the split and the log's rate limit.
			text, gerr := msg.ParseWarning()
			switch classifyWarningDomain(gerr) {
			case warningDomainStream:
				e.streamWarningCount.Add(1)
			case warningDomainResource:
				e.resourceWarningCount.Add(1)
			default:
				e.otherWarningCount.Add(1)
			}
			e.maybeLogWarning(msg, text, gerr)
		case gst.MessageQos:
			// A downstream element dropped/skipped a buffer for the clock.
			e.qosCount.Add(1)
		}
		gst.UnsafeMessageUnref(msg)
	}
}

// warningDomainStream, warningDomainResource, and warningDomainOther are
// classifyWarningDomain's result vocabulary.
const (
	warningDomainStream   = "stream"
	warningDomainResource = "resource"
	warningDomainOther    = "other"
)

// classifyWarningDomain buckets a WARNING's GError by domain. Not
// confirmed to identify an ALSA xrun/underrun: alsasink logs a
// recovered xrun via GST_WARNING_OBJECT, a debug call, never a bus
// message, so no domain here is known to correlate with one.
func classifyWarningDomain(gerr error) string {
	ge, ok := gerr.(*glib.GError)
	if !ok {
		return warningDomainOther
	}
	switch ge.Quark() {
	case uint32(gst.StreamErrorQuark()):
		return warningDomainStream
	case uint32(gst.ResourceErrorQuark()):
		return warningDomainResource
	default:
		return warningDomainOther
	}
}

// warningLogInterval bounds watchBus's WARNING log rate; the count is
// never gated by this, only the synchronous log write is.
const warningLogInterval = time.Second

// maybeLogWarning logs msg at most once per warningLogInterval.
func (e *Engine) maybeLogWarning(msg *gst.Message, text string, gerr error) {
	now := time.Now().UnixNano()
	if now-e.lastWarningLogNano < int64(warningLogInterval) {
		return
	}
	e.lastWarningLogNano = now
	source := ""
	if src := msg.Source(); src != nil {
		source = src.GetName()
	}
	slog.Warn("gstengine: output pipeline warning", "source", source, "error", warningText(text, gerr))
}

// warningText prefers gerr's own message when GStreamer supplied one,
// matching [classifyBranchError]'s identical text/gerr precedence for
// MessageError.
func warningText(text string, gerr error) string {
	if gerr != nil {
		return gerr.Error()
	}
	return text
}

// GlitchCounts reports this Engine's cumulative bus-level glitch counts
// since e.startedAt. Always known=true: a real Engine always counts,
// even when the count is genuinely zero -- the distinction
// [agentaudio.GlitchObserver] exists to preserve against a backend (e.g.
// gstengine's own cgo-less stub) that never collects this evidence.
func (e *Engine) GlitchCounts() (agentaudio.GlitchCounts, bool) {
	return agentaudio.GlitchCounts{
		Since:            e.startedAt,
		StreamWarnings:   e.streamWarningCount.Load(),
		ResourceWarnings: e.resourceWarningCount.Load(),
		OtherWarnings:    e.otherWarningCount.Load(),
		QosEvents:        e.qosCount.Load(),
	}, true
}

// branchForSource walks msg's originating object up through its parents,
// returning the branch it belongs to, or nil when the error is not
// attributable to any live branch (a fault in the shared output topology
// itself, e.g. the sink or a channel mixer).
// e.mu is taken only around each individual map read, never across
// obj.GetParent() — a cgo call that can walk arbitrary depth into
// decodebin's internal decoder chain — so a slow or deep walk never
// blocks every other handle-addressed engine call behind it.
func (e *Engine) branchForSource(src gst.Object) *branch {
	for obj := src; obj != nil; obj = obj.GetParent() {
		name := obj.GetName()
		e.mu.Lock()
		b, ok := e.elementIndex[name]
		e.mu.Unlock()
		if ok {
			return b
		}
	}
	return nil
}

// indexBranch registers every one of b's own element names so a bus
// error naming any of them, or any of their internal children, attributes
// back to b — see elementIndex's doc comment for why this happens before
// b is loaded.
func (e *Engine) indexBranch(b *branch) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, name := range b.elementNames {
		e.elementIndex[name] = b
	}
}

func (e *Engine) unindexBranch(b *branch) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, name := range b.elementNames {
		delete(e.elementIndex, name)
	}
}

// classifyBranchError maps a branch-scoped GStreamer error onto this
// package's fault sentinels. A media_disappeared classification never
// reaches here: Load stats the resolved path before touching GStreamer
// at all, so every error a branch's own elements can still produce is a
// decode-class failure — the file existed but could not be prepared.
func classifyBranchError(text string, gerr error) error {
	msg := text
	if gerr != nil {
		msg = gerr.Error()
	}
	return fmt.Errorf("%w: %s", pkgaudio.ErrEngineDecodeFailure, msg)
}

// boundedCall runs fn on its own goroutine and returns its error, or
// ctx.Err() if ctx is done first. A blocking C call that ctx gives up on
// keeps running in the abandoned goroutine — cgo has no mechanism to
// interrupt it — so this bounds the caller, not the underlying call.
func boundedCall(ctx context.Context, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
