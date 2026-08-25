//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstapp"

	agentaudio "github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/ltcgen"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

var _ agentaudio.LTCGenerator = (*Engine)(nil)

// ltcSilenceChunk is how much silence one push covers while no LTC run is
// active, and also the retry interval after a push the appsrc did not
// accept (see waitBeforeRetry).
const ltcSilenceChunk = 20 * time.Millisecond

// ltcLivenessTimeout bounds how long a run may go without a confirmed
// emission before ObserveLTC stops reporting it as running: ten silence
// chunks, generous against scheduling jitter while still catching a real
// stall within a fraction of a second.
const ltcLivenessTimeout = 10 * ltcSilenceChunk

// ltcAppSrcLeadSeconds bounds appsrc's internal queue. block=true on the
// appsrc makes [gstapp.AppSrc.PushBuffer] itself the pacing mechanism —
// it returns once GStreamer has room, so the feeder goroutine runs no
// faster or slower than the pipeline actually consumes. A Go-side sleep
// must never be reintroduced here: it adds latency interleave has no way
// to recover from, since interleave paces the whole pipeline to its
// slowest sink pad.
const ltcAppSrcLeadSeconds = 0.2

// ltcAppSrcLeadDuration is [ltcAppSrcLeadSeconds] as a [time.Duration]: the
// steady-state amount of already-queued audio a buffer sits behind between
// being pushed and actually reaching the wire, since block=true keeps the
// appsrc queue near max-bytes whenever the feeder is keeping up. StartLTC
// advances a new run's start timecode by this much so the frame that
// eventually plays at the requested position carries the requested value,
// and the feeder's own observation reporting subtracts it back out so a
// reported timecode reflects what is audible rather than what was just
// pushed ahead of the existing queue.
const ltcAppSrcLeadDuration = time.Duration(ltcAppSrcLeadSeconds * float64(time.Second))

// ltcFeederShutdownTimeout bounds how long [Engine.Close] waits for the
// feeder goroutine to exit after unblocking it. It is a backstop, not the
// normal path: setting the pipeline to NULL should return a blocked
// PushBuffer immediately.
const ltcFeederShutdownTimeout = 5 * time.Second

// ltcChannel owns the appsrc feeding cfg.LTCChannel and the state of
// whatever LTC run, if any, is currently driving it.
type ltcChannel struct {
	src        gstapp.AppSrc
	capsfilter gst.Element
	pipeline   gst.Pipeline
	sampleRate int

	mu         sync.Mutex
	encoder    *ltcgen.Encoder
	rate       pkgaudio.LTCFrameRate
	active     bool
	generation uint64

	// emittedGeneration is the generation of the most recent buffer this
	// channel's capsfilter src pad actually passed, written by the pad
	// probe [newLTCChannel] installs. A full appsrc can return FlowOK for
	// a buffer it never delivers, so this is the only evidence
	// [Engine.runLTCFeeder] trusts before reporting [agentaudio.LTCRunning].
	emittedGeneration atomic.Uint64

	// obs and lastConfirmed are guarded by mu, alongside
	// encoder/rate/active/generation, so that checking whether a run is
	// still current and writing the evidence for it is always one
	// critical section, never two. lastConfirmed is the time of the most
	// recent confirmed emission; [ltcChannel.observe] downgrades a stale
	// LTCRunning to LTCFailed once it is older than ltcLivenessTimeout.
	obs           agentaudio.LTCObservation
	lastConfirmed time.Time

	// feedStarted is set once [Engine.runLTCFeeder] is actually launched
	// for this channel, distinguishing that from a structural pipeline
	// failure after the channel is built but before the feeder starts.
	feedStarted atomic.Bool
	stopFeed    chan struct{}
	feedDone    chan struct{}

	// feedAnchor, anchorKnown and feedSamples are owned exclusively by the
	// feeder goroutine: the running-time each buffer's PTS is computed
	// from by adding an exact sample count, never by re-reading the
	// pipeline clock per buffer, so the LTC stream stays gapless.
	// anchorKnown is false until a genuine (non-ClockTimeNone) running
	// time has been read; until then feedAnchor is not a real anchor and
	// no confirmed emission may be reported as [agentaudio.LTCRunning].
	feedAnchor  gst.ClockTime
	anchorKnown bool
	feedSamples uint64
}

// newLTCChannel builds the appsrc -> audioconvert -> capsfilter chain for
// one LTC output channel and adds it to bin, returning it unlinked from
// the interleave sink pad the caller still owns requesting.
//
// maskBit is the single GstAudioChannelPosition bit [channelPositionBits]
// assigned this output channel, or 0 when the device's channel count has
// no positioned fallback layout. When set, it is a negotiation label,
// not a description of the signal: this channel carries generated LTC,
// not a directional speaker feed, and calling it e.g. "rear left" to
// satisfy a positioned sink's channel-mask must never be read as this
// engine claiming to emit a surround layout.
func newLTCChannel(bin gst.Bin, sampleRate int, maskBit uint64) (*ltcChannel, error) {
	srcElem := gst.ElementFactoryMake("appsrc", "ltc-appsrc")
	conv := gst.ElementFactoryMake("audioconvert", "ltc-convert")
	caps := gst.ElementFactoryMake("capsfilter", "ltc-caps")
	if srcElem == nil || conv == nil || caps == nil {
		return nil, fmt.Errorf("could not construct the LTC appsrc chain")
	}
	src, ok := srcElem.(gstapp.AppSrc)
	if !ok {
		return nil, fmt.Errorf("appsrc element does not implement gstapp.AppSrc")
	}

	src.SetObjectProperty("caps", gst.CapsFromString(
		fmt.Sprintf("audio/x-raw,format=%s,rate=%d,channels=1,layout=interleaved", ltcgen.SampleFormat, sampleRate)))
	src.SetObjectProperty("format", gst.FormatTime)
	src.SetObjectProperty("is-live", true)
	// block makes PushBuffer the pacing mechanism: it returns once the
	// queue below max-bytes has room, not a leaky discard reported as sent.
	src.SetObjectProperty("block", true)
	src.SetObjectProperty("max-bytes", uint64(float64(sampleRate)*2*ltcAppSrcLeadSeconds))

	caps.SetObjectProperty("caps", gst.CapsFromString(channelCapsString(sampleRate, maskBit)))

	for _, el := range []gst.Element{srcElem, conv, caps} {
		if !bin.Add(el) {
			return nil, fmt.Errorf("could not add LTC element %q to pipeline", el.GetName())
		}
	}
	if !srcElem.Link(conv) || !conv.Link(caps) {
		return nil, fmt.Errorf("could not link LTC chain")
	}

	ch := &ltcChannel{
		src:        src,
		capsfilter: caps,
		sampleRate: sampleRate,
		stopFeed:   make(chan struct{}),
		feedDone:   make(chan struct{}),
		obs:        agentaudio.LTCObservation{State: agentaudio.LTCStopped, Reason: "no LTC run has been requested"},
	}

	// A buffer that reaches this probe left the LTC chain, which is not
	// proof the sink consumed it. Each pushed buffer carries its
	// generation in Offset (see pushLTCSamples), so the feeder can tell
	// whether the confirmation belongs to the run it is reporting on.
	caps.GetStaticPad("src").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
		if buf := info.GetBuffer(); buf != nil {
			ch.emittedGeneration.Store(buf.Offset())
		}
		return gst.PadProbeOK
	})

	return ch, nil
}

// bindPipeline records the running pipeline ch's buffers are timestamped
// against. Called once buildPipeline has a live gst.Pipeline value.
func (ch *ltcChannel) bindPipeline(p gst.Pipeline) {
	ch.pipeline = p
}

// resolveFeedAnchor sets feedAnchor from the pipeline's current running
// time, once, the first time that value is not [gst.ClockTimeNone].
// Called only from the feeder goroutine, which is the sole owner of
// feedAnchor and anchorKnown.
func (ch *ltcChannel) resolveFeedAnchor() {
	if ch.anchorKnown || ch.pipeline == nil {
		return
	}
	if t := ch.pipeline.GetCurrentRunningTime(); t != gst.ClockTimeNone {
		ch.feedAnchor = t
		ch.anchorKnown = true
	}
}

func (ch *ltcChannel) observe(now time.Time) agentaudio.LTCObservation {
	ch.mu.Lock()
	o := ch.obs
	lastConfirmed := ch.lastConfirmed
	ch.mu.Unlock()
	if o.State == agentaudio.LTCRunning && now.Sub(lastConfirmed) > ltcLivenessTimeout {
		o = agentaudio.LTCObservation{
			State:  agentaudio.LTCFailed,
			Reason: fmt.Sprintf("no confirmed LTC emission within %s", ltcLivenessTimeout),
		}
	}
	o.ObservedAt = now
	return o
}

// closeEncoder releases whatever encoder ch currently holds. Safe to call
// with no run active.
func (ch *ltcChannel) closeEncoder() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.encoder != nil {
		ch.encoder.Close()
		ch.encoder = nil
	}
}

const ltcNoChannelReason = "this node has no configured LTC output channel"

// StartLTC builds a fresh encoder at spec's rate and start timecode and
// makes it the channel's current run, replacing and closing whatever run
// preceded it — the realignment a seek uses. It never leaves the channel
// without data: on any failure the feeder keeps emitting silence.
func (e *Engine) StartLTC(ctx context.Context, spec agentaudio.LTCSpec) (agentaudio.LTCObservation, error) {
	now := e.cfg.now()
	if e.ltc == nil {
		obs := agentaudio.LTCObservation{State: agentaudio.LTCUnsupported, Reason: ltcNoChannelReason, ObservedAt: now}
		return obs, fmt.Errorf("gstengine: %s", ltcNoChannelReason)
	}
	if err := e.brokenErr(); err != nil {
		return e.ltc.observe(now), err
	}
	if err := spec.FrameRate.Validate(); err != nil {
		return e.ltc.observe(now), err
	}
	if err := spec.StartTimecode.Validate(); err != nil {
		return e.ltc.observe(now), err
	}

	// The appsrc this encoder feeds already has up to ltcAppSrcLeadDuration
	// of previously-queued audio ahead of whatever gets pushed next (see
	// ltcAppSrcLeadDuration): this run's first pushed frame will not
	// actually reach the wire until that backlog drains. Starting the
	// encoder that far ahead of the requested timecode, rather than at it,
	// is what makes the frame audible at spec.StartTimecode's position
	// carry spec.StartTimecode's value instead of reading late by the
	// queue depth.
	compensatedStart, err := spec.StartTimecode.Advance(ltcAppSrcLeadDuration, spec.FrameRate)
	if err != nil {
		obs := agentaudio.LTCObservation{State: agentaudio.LTCFailed, Reason: err.Error()}
		e.ltc.mu.Lock()
		e.ltc.active = false
		e.ltc.obs = obs
		e.ltc.mu.Unlock()
		obs.ObservedAt = now
		return obs, err
	}

	enc, err := ltcgen.NewEncoder(spec.FrameRate, compensatedStart, e.ltc.sampleRate)
	if err != nil {
		obs := agentaudio.LTCObservation{State: agentaudio.LTCFailed, Reason: err.Error()}
		e.ltc.mu.Lock()
		e.ltc.active = false
		e.ltc.obs = obs
		e.ltc.mu.Unlock()
		obs.ObservedAt = now
		return obs, err
	}

	// The requested run has no confirmed output yet, so this must not
	// claim LTCRunning: the feeder reports that once it actually pushes
	// this run's first frame. generation, active and obs change together
	// under one lock so a concurrent feeder confirmation can never land
	// between the generation bump and the observation it belongs to.
	e.ltc.mu.Lock()
	old := e.ltc.encoder
	e.ltc.encoder = enc
	e.ltc.rate = spec.FrameRate
	e.ltc.generation++
	e.ltc.active = true
	e.ltc.obs = agentaudio.LTCObservation{State: agentaudio.LTCStopped, Reason: "LTC run requested; no output confirmed yet"}
	e.ltc.mu.Unlock()
	if old != nil {
		old.Close()
	}

	return e.ltc.observe(now), nil
}

// StopLTC ends the current run, if any, and releases its encoder. The
// channel keeps emitting silence afterward.
func (e *Engine) StopLTC(ctx context.Context) (agentaudio.LTCObservation, error) {
	now := e.cfg.now()
	if e.ltc == nil {
		obs := agentaudio.LTCObservation{State: agentaudio.LTCUnsupported, Reason: ltcNoChannelReason, ObservedAt: now}
		return obs, fmt.Errorf("gstengine: %s", ltcNoChannelReason)
	}

	e.ltc.mu.Lock()
	old := e.ltc.encoder
	e.ltc.encoder = nil
	e.ltc.active = false
	e.ltc.generation++
	e.ltc.obs = agentaudio.LTCObservation{State: agentaudio.LTCStopped, Reason: "stopped"}
	e.ltc.mu.Unlock()
	if old != nil {
		old.Close()
	}

	return e.ltc.observe(now), nil
}

// ObserveLTC returns fresh evidence about the LTC channel: running only
// when a sample of the current run has actually been accepted downstream.
func (e *Engine) ObserveLTC(ctx context.Context) agentaudio.LTCObservation {
	now := e.cfg.now()
	if e.ltc == nil {
		return agentaudio.LTCObservation{State: agentaudio.LTCUnsupported, Reason: ltcNoChannelReason, ObservedAt: now}
	}
	return e.ltc.observe(now)
}

// runLTCFeeder keeps ch's appsrc fed for as long as the engine lives: LTC
// samples while a run is active, silence otherwise. Pacing comes from
// appsrc's own backpressure (block=true, a small max-bytes lead) rather
// than a Go-side sleep, which would add latency the pipeline could never
// recover; waitBeforeRetry is the sole exception, bounding the retry rate
// when a push is not accepted at all.
func (e *Engine) runLTCFeeder(ch *ltcChannel) {
	defer close(ch.feedDone)
	// A subsystem problem must never stop the show: a cgo call failing
	// under memory pressure (see pushLTCSamples) panics rather than
	// returning an error, and an unrecovered panic on this goroutine
	// would kill the whole agent process, program audio included.
	defer func() {
		if r := recover(); r != nil {
			ch.mu.Lock()
			ch.obs = agentaudio.LTCObservation{
				State:  agentaudio.LTCFailed,
				Reason: fmt.Sprintf("LTC feeder recovered from a panic: %v", r),
			}
			ch.mu.Unlock()
			slog.Error("gstengine: LTC feeder recovered from a panic; LTC output stopped, program audio unaffected", "panic", r)
		}
	}()

	// The pipeline's PLAYING transition can still be pending here on a
	// real, asynchronous sink, so this may not resolve yet. Never block
	// on gst.Pipeline.GetState to wait it out: that call waits on the
	// whole pipeline's state, shared with every other branch, and can
	// starve interleave's LTC sink pad long enough to drag program audio
	// down with it. The per-iteration retry below is the non-blocking path.
	ch.resolveFeedAnchor()
	if !ch.anchorKnown {
		ch.mu.Lock()
		ch.obs = agentaudio.LTCObservation{
			State:  agentaudio.LTCFailed,
			Reason: "pipeline running time not yet known; LTC PTS anchor not yet established",
		}
		ch.mu.Unlock()
	}

	for {
		select {
		case <-ch.stopFeed:
			return
		default:
		}

		if !ch.anchorKnown {
			ch.resolveFeedAnchor()
		}

		var raw []byte
		var tc pkgaudio.LTCTimecode
		var rate pkgaudio.LTCFrameRate
		var frameErr error
		var gen uint64
		haveFrame := false

		ch.mu.Lock()
		gen = ch.generation
		if ch.active && ch.encoder != nil {
			raw, tc, frameErr = ch.encoder.NextFrame()
			rate = ch.rate
			if frameErr != nil {
				ch.active = false
			} else {
				haveFrame = true
			}
		}
		ch.mu.Unlock()

		if frameErr != nil {
			ch.mu.Lock()
			if ch.generation == gen {
				ch.obs = agentaudio.LTCObservation{State: agentaudio.LTCFailed, Reason: frameErr.Error()}
			}
			ch.mu.Unlock()
			if !e.pushLTCSilence(ch, gen) && !ch.waitBeforeRetry() {
				return
			}
			continue
		}

		if !haveFrame {
			if !e.pushLTCSilence(ch, gen) && !ch.waitBeforeRetry() {
				return
			}
			continue
		}

		pushed := e.pushLTCSamples(ch, raw, gen)
		if !pushed && !ch.waitBeforeRetry() {
			return
		}

		// tc is the timecode of the frame just handed to appsrc, which is
		// not the frame currently audible: it sits behind whatever this
		// channel already had queued (see ltcAppSrcLeadDuration). Reporting
		// tc directly would have this observation lead the audible output
		// by the same queue depth, the mirror image of the start-time bug
		// StartLTC compensates for above.
		played := tc
		if playedTC, err := tc.Advance(-ltcAppSrcLeadDuration, rate); err == nil {
			played = playedTC
		}

		ch.mu.Lock()
		if pushed && ch.generation == gen && ch.active && ch.anchorKnown && ch.emittedGeneration.Load() == gen {
			ch.lastConfirmed = e.cfg.now()
			ch.obs = agentaudio.LTCObservation{
				State:          agentaudio.LTCRunning,
				FrameRateKnown: true,
				FrameRate:      rate,
				TimecodeKnown:  true,
				Timecode:       played,
			}
		}
		ch.mu.Unlock()
	}
}

// waitBeforeRetry bounds the feeder's retry rate after appsrc refuses a
// push (downstream error, EOS, or flushing), so a stalled sink idles
// instead of busy-spinning. Reports false when stopFeed fired during the
// wait, telling the caller to exit rather than retry.
func (ch *ltcChannel) waitBeforeRetry() bool {
	select {
	case <-ch.stopFeed:
		return false
	case <-time.After(ltcSilenceChunk):
		return true
	}
}

// pushLTCSamples wraps raw (already-encoded [ltcgen.SampleFormat] PCM) in
// a timestamped buffer tagged with gen and pushes it, reporting whether
// appsrc accepted it. PTS is ch.feedAnchor plus the exact sample count fed
// so far, never a re-read of the pipeline clock, so the stream stays
// gapless regardless of how long a push blocks. Accepted is not emitted:
// see the capsfilter pad probe in [newLTCChannel].
func (e *Engine) pushLTCSamples(ch *ltcChannel, raw []byte, gen uint64) bool {
	buf := gst.NewBufferAllocate(nil, uint(len(raw)), nil)
	if buf == nil {
		return false
	}
	mapped, ok := buf.Map(gst.MapWrite)
	if !ok {
		return false
	}
	_, _ = mapped.Write(raw)
	mapped.Unmap()

	sampleCount := len(raw) / 2
	buf.SetPTS(ch.feedAnchor + gst.ClockTime(time.Duration(ch.feedSamples)*time.Second/time.Duration(ch.sampleRate)))
	buf.SetDuration(gst.ClockTime(time.Duration(sampleCount) * time.Second / time.Duration(ch.sampleRate)))
	buf.SetOffset(gen)
	ch.feedSamples += uint64(sampleCount)

	return ch.src.PushBuffer(buf) == gst.FlowOK
}

// pushLTCSilence pushes one [ltcSilenceChunk] of digital silence tagged
// with gen, reporting whether appsrc accepted it.
func (e *Engine) pushLTCSilence(ch *ltcChannel, gen uint64) bool {
	n := int(float64(ch.sampleRate) * ltcSilenceChunk.Seconds())
	return e.pushLTCSamples(ch, make([]byte, n*2), gen)
}
