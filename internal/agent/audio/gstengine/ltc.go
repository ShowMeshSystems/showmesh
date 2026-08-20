//go:build cgo

package gstengine

import (
	"context"
	"fmt"
	"sync"
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

	obsMu sync.Mutex
	obs   agentaudio.LTCObservation

	stopFeed chan struct{}
	feedDone chan struct{}
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
	src.SetObjectProperty("block", false)
	src.SetObjectProperty("leaky-type", gstapp.AppLeakyTypeDownstream)
	src.SetObjectProperty("max-buffers", uint64(4))

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
	return ch, nil
}

// bindPipeline records the running pipeline ch's buffers are timestamped
// against. Called once buildPipeline has a live gst.Pipeline value.
func (ch *ltcChannel) bindPipeline(p gst.Pipeline) {
	ch.pipeline = p
}

func (ch *ltcChannel) observe(now time.Time) agentaudio.LTCObservation {
	ch.obsMu.Lock()
	o := ch.obs
	ch.obsMu.Unlock()
	o.ObservedAt = now
	return o
}

func (ch *ltcChannel) setObservation(o agentaudio.LTCObservation) {
	ch.obsMu.Lock()
	ch.obs = o
	ch.obsMu.Unlock()
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
		e.ltc.mu.Lock()
		e.ltc.active = false
		e.ltc.mu.Unlock()
		obs := agentaudio.LTCObservation{State: agentaudio.LTCFailed, Reason: err.Error()}
		e.ltc.setObservation(obs)
		obs.ObservedAt = now
		return obs, err
	}

	e.ltc.mu.Lock()
	old := e.ltc.encoder
	e.ltc.encoder = enc
	e.ltc.rate = spec.FrameRate
	e.ltc.generation++
	e.ltc.active = true
	e.ltc.mu.Unlock()
	if old != nil {
		old.Close()
	}

	// The requested run has no confirmed output yet, so this must not
	// claim LTCRunning: the feeder reports that once it actually pushes
	// this run's first frame.
	e.ltc.setObservation(agentaudio.LTCObservation{State: agentaudio.LTCStopped, Reason: "LTC run requested; no output confirmed yet"})
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
	e.ltc.mu.Unlock()
	if old != nil {
		old.Close()
	}

	e.ltc.setObservation(agentaudio.LTCObservation{State: agentaudio.LTCStopped, Reason: "stopped"})
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

// runLTCFeeder keeps ch's appsrc fed for as long as the engine lives:
// LTC samples while a run is active, silence otherwise, always paced to
// roughly real time so interleave never starves waiting on this pad.
func (e *Engine) runLTCFeeder(ch *ltcChannel) {
	defer close(ch.feedDone)
	for {
		select {
		case <-ch.stopFeed:
			return
		default:
		}

		var samples []int16
		var tc pkgaudio.LTCTimecode
		var rate pkgaudio.LTCFrameRate
		var frameErr error
		var gen uint64
		haveFrame := false

		ch.mu.Lock()
		gen = ch.generation
		if ch.active && ch.encoder != nil {
			samples, tc, frameErr = ch.encoder.NextFrame()
			rate = ch.rate
			if frameErr != nil {
				ch.active = false
			} else {
				haveFrame = true
			}
		}
		ch.mu.Unlock()

		if frameErr != nil {
			ch.setObservation(agentaudio.LTCObservation{State: agentaudio.LTCFailed, Reason: frameErr.Error()})
			if !sleepOrStop(ch.stopFeed, ltcSilenceChunk) {
				return
			}
			continue
		}

		if !haveFrame {
			e.pushLTCSilence(ch)
			if !sleepOrStop(ch.stopFeed, ltcSilenceChunk) {
				return
			}
			continue
		}

		dur := time.Duration(float64(len(samples)) / float64(ch.sampleRate) * float64(time.Second))
		pushed := e.pushLTCSamples(ch, samples)

		ch.mu.Lock()
		stillCurrent := pushed && ch.generation == gen && ch.active
		ch.mu.Unlock()
		if stillCurrent {
			ch.setObservation(agentaudio.LTCObservation{
				State:          agentaudio.LTCRunning,
				FrameRateKnown: true,
				FrameRate:      rate,
				TimecodeKnown:  true,
				Timecode:       tc,
			})
		}
		if !sleepOrStop(ch.stopFeed, dur) {
			return
		}
	}
}

// pushLTCSamples converts samples to a timestamped [ltcgen.SampleFormat]
// buffer and pushes it, reporting whether the pipeline accepted it.
func (e *Engine) pushLTCSamples(ch *ltcChannel, samples []int16) bool {
	buf := gst.NewBufferAllocate(nil, uint(len(samples)*2), nil)
	mapped, ok := buf.Map(gst.MapWrite)
	if !ok {
		return false
	}
	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		raw[2*i] = byte(uint16(s))
		raw[2*i+1] = byte(uint16(s) >> 8)
	}
	_, _ = mapped.Write(raw)
	mapped.Unmap()

	if ch.pipeline != nil {
		buf.SetPTS(ch.pipeline.GetCurrentRunningTime())
	}
	buf.SetDuration(gst.ClockTime(time.Duration(len(samples)) * time.Second / time.Duration(ch.sampleRate)))
	return ch.src.PushBuffer(buf) == gst.FlowOK
}

// pushLTCSilence pushes one [ltcSilenceChunk] of digital silence.
func (e *Engine) pushLTCSilence(ch *ltcChannel) {
	n := int(float64(ch.sampleRate) * ltcSilenceChunk.Seconds())
	e.pushLTCSamples(ch, make([]int16, n))
}

// sleepOrStop waits for d, or returns false immediately if stop closes
// first.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
