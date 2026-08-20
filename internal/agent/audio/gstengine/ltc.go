//go:build cgo

package gstengine

import (
	"context"
	"fmt"
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
// active. interleave blocks until every requested pad has a buffer, so
// this channel must never fall idle waiting on a push — a starved LTC
// channel would stall program audio on every other channel of the same
// pipeline — and this chunk size is small enough that idle and active
// pushes keep the same steady cadence.
const ltcSilenceChunk = 20 * time.Millisecond

// ltcAppSrcLeadSeconds bounds appsrc's internal queue. block=true on the
// appsrc makes [gstapp.AppSrc.PushBuffer] itself the pacing mechanism —
// it returns once GStreamer has room, so the feeder goroutine runs no
// faster or slower than the pipeline actually consumes. A Go-side sleep
// must never be reintroduced here: it adds latency interleave has no way
// to recover from, since interleave paces the whole pipeline to its
// slowest sink pad.
const ltcAppSrcLeadSeconds = 0.2

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

	// obs is guarded by mu, alongside encoder/rate/active/generation, so
	// that checking whether a run is still current and writing the
	// evidence for it is always one critical section, never two.
	obs agentaudio.LTCObservation

	// feedStarted is set once [Engine.runLTCFeeder] is actually launched
	// for this channel, distinguishing that from a structural pipeline
	// failure after the channel is built but before the feeder starts.
	feedStarted atomic.Bool
	stopFeed    chan struct{}
	feedDone    chan struct{}

	// feedAnchor and feedSamples are owned exclusively by the feeder
	// goroutine: the running-time each buffer's PTS is computed from by
	// adding an exact sample count, never by re-reading the pipeline
	// clock per buffer, so the LTC stream stays gapless.
	feedAnchor  gst.ClockTime
	feedSamples uint64
}

// newLTCChannel builds the appsrc -> audioconvert -> capsfilter chain for
// one LTC output channel and adds it to bin, returning it unlinked from
// the interleave sink pad the caller still owns requesting.
func newLTCChannel(bin gst.Bin, sampleRate int) (*ltcChannel, error) {
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
	// block makes PushBuffer itself the pacing mechanism: it returns once
	// the queue below max-bytes has room, so the feeder runs at exactly
	// the rate the pipeline consumes. No leaky-type: a full queue must
	// block the producer, not silently discard a buffer that PushBuffer
	// would still report as sent.
	src.SetObjectProperty("block", true)
	src.SetObjectProperty("max-bytes", uint64(float64(sampleRate)*2*ltcAppSrcLeadSeconds))

	caps.SetObjectProperty("caps", gst.CapsFromString(fmt.Sprintf("audio/x-raw,format=%s,rate=%d,channels=1", interleaveSampleFormat, sampleRate)))

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

	// A buffer that reaches this probe has actually left the LTC chain,
	// unlike a PushBuffer return value alone. Each pushed buffer carries
	// its generation in Offset (see pushLTCSamples), so the feeder can
	// tell whether the confirmation belongs to the run it is currently
	// reporting on.
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

func (ch *ltcChannel) observe(now time.Time) agentaudio.LTCObservation {
	ch.mu.Lock()
	o := ch.obs
	ch.mu.Unlock()
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

	enc, err := ltcgen.NewEncoder(spec.FrameRate, spec.StartTimecode, e.ltc.sampleRate)
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
// samples while a run is active, silence otherwise. It never sleeps —
// pacing comes entirely from appsrc's own backpressure (block=true, a
// small max-bytes lead), since interleave paces the whole pipeline to its
// slowest sink pad and a Go-side sleep on top of that backpressure is
// pure added latency the pipeline can never recover.
func (e *Engine) runLTCFeeder(ch *ltcChannel) {
	defer close(ch.feedDone)

	if ch.pipeline != nil {
		ch.feedAnchor = ch.pipeline.GetCurrentRunningTime()
	}

	for {
		select {
		case <-ch.stopFeed:
			return
		default:
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
			e.pushLTCSilence(ch, gen)
			continue
		}

		if !haveFrame {
			e.pushLTCSilence(ch, gen)
			continue
		}

		pushed := e.pushLTCSamples(ch, raw, gen)

		ch.mu.Lock()
		if pushed && ch.generation == gen && ch.active && ch.emittedGeneration.Load() == gen {
			ch.obs = agentaudio.LTCObservation{
				State:          agentaudio.LTCRunning,
				FrameRateKnown: true,
				FrameRate:      rate,
				TimecodeKnown:  true,
				Timecode:       tc,
			}
		}
		ch.mu.Unlock()
	}
}

// pushLTCSamples wraps raw (already-encoded [ltcgen.SampleFormat] PCM) in
// a timestamped buffer tagged with gen and pushes it, reporting whether
// appsrc accepted it. PTS is derived from ch.feedAnchor plus the exact
// sample count fed so far, never from re-reading the pipeline clock per
// buffer, so the stream stays gapless regardless of how long this push
// blocks under backpressure. Accepted is not the same as emitted: the
// capsfilter pad probe installed in [newLTCChannel] is the evidence the
// buffer actually left this channel.
func (e *Engine) pushLTCSamples(ch *ltcChannel, raw []byte, gen uint64) bool {
	buf := gst.NewBufferAllocate(nil, uint(len(raw)), nil)
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
