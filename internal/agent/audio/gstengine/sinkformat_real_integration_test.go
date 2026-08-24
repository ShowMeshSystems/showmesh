//go:build cgo

package gstengine

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// capsRestrictedSink is a real, data-consuming "device" for these tests:
// a capsfilter with one fixed caps value feeding a real fakesink, parsed
// as a single bin. This stands in for a class-compliant USB interface's
// raw hw: route -- fixed caps reject anything the upstream chain does
// not already match, exactly like alsasink against a device whose own
// negotiated format differs from the F32LE this package's output
// topology is pinned to internally -- but unlike a bare capsfilter with
// nothing downstream, fakesink actually consumes buffers, so a pipeline
// that only links without ever sustaining data flow is caught rather
// than mistaken for a working one.
type capsRestrictedSink struct {
	element gst.Element
	buffers *atomic.Int64
}

// newCapsRestrictedSink parses "capsfilter caps=<caps> ! fakesink" as one
// bin (ghosting the capsfilter's otherwise-dangling sink pad) and
// attaches a buffer-counting probe on the real fakesink's own sink pad,
// so a test can assert not just that the pipeline reached PLAYING but
// that data actually kept arriving at the sink.
func newCapsRestrictedSink(t *testing.T, caps string) capsRestrictedSink {
	t.Helper()
	gst.Init() // ParseBinFromDescription below needs GStreamer initialized before New would otherwise do it
	desc := fmt.Sprintf(`capsfilter caps="%s" ! fakesink name=innersink sync=true async=false`, caps)
	b, err := gst.ParseBinFromDescription(desc, true)
	if err != nil {
		t.Fatalf("ParseBinFromDescription(%q): %v", desc, err)
	}
	inner := b.GetByName("innersink")
	if inner == nil {
		t.Fatalf("parsed sink bin %q has no element named innersink", desc)
	}
	var count atomic.Int64
	inner.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return capsRestrictedSink{element: b, buffers: &count}
}

// useSinkElement substitutes el for whatever newSinkFactoryElement would
// otherwise construct from cfg.SinkFactory/cfg.SinkProperties -- the
// seam this package exposes because no single registered element
// factory name can express a caps-restricted sink bin. cfg.SinkFactory
// must still name a real, constructible factory (checkPrerequisites
// probes it independently of this override), so callers set it to
// "fakesink" and rely on this override for the element actually used.
func useSinkElement(t *testing.T, el gst.Element) {
	t.Helper()
	orig := newSinkFactoryElement
	newSinkFactoryElement = func(Config) (gst.Element, error) { return el, nil }
	t.Cleanup(func() { newSinkFactoryElement = orig })
}

// waitForAvailability polls e.Available() until it matches want or
// timeout elapses, returning the last observed pair. A sink-side
// not-negotiated failure can surface asynchronously off watchBus, after
// New/buildPipeline has already returned successfully -- see
// engine_cgo.go's own doc comments on Available -- so a caller cannot
// assume the final state is visible the instant New returns.
func waitForAvailability(e *Engine, want bool, timeout time.Duration) (bool, string) {
	deadline := time.Now().Add(timeout)
	for {
		ok, reason := e.Available()
		if ok == want || time.Now().After(deadline) {
			return ok, reason
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// settleWindow is how long these tests wait, twice, after Available()
// first reports true before trusting it: a real not-negotiated failure
// on this exact class of restrictive sink was measured to surface
// roughly 30ms after New returns, once the pipeline actually starts
// pushing buffers rather than while it is still prerolling.
const settleWindow = 300 * time.Millisecond

// requireSustainedPlayback is the positive-path assertion every test in
// this file that expects success shares: Available() == true alone is
// not evidence, since it is also what a pipeline reports the instant
// before an asynchronous bus error kills it. This settles twice and
// requires the buffer count to have both stayed nonzero and gone up
// across the second window, so a pipeline that opened once and then
// silently stalled cannot pass either.
func requireSustainedPlayback(t *testing.T, e *Engine, buffers *atomic.Int64) {
	t.Helper()
	if ok, reason := waitForAvailability(e, true, 3*time.Second); !ok {
		t.Fatalf("Available() = false (%s), want true", reason)
	}
	time.Sleep(settleWindow)
	if ok, reason := e.Available(); !ok {
		t.Fatalf("Available() = false (%s) after a %s settle window: a bus error surfaced only after data started flowing", reason, settleWindow)
	}
	first := buffers.Load()
	if first == 0 {
		t.Fatalf("0 buffers reached the sink pad after a %s settle window: the pipeline never actually carried data", settleWindow)
	}
	time.Sleep(settleWindow)
	second := buffers.Load()
	if ok, reason := e.Available(); !ok {
		t.Fatalf("Available() = false (%s) after a second %s settle window", reason, settleWindow)
	}
	if second <= first {
		t.Fatalf("buffer count did not increase across a second %s window (%d -> %d): playback is not sustained", settleWindow, first, second)
	}
}

// TestEngineOpensAnIntegerOnlySink proves a route discovery blessed can
// actually be played, and keeps playing: interleave's internal mixing
// stays fixed at F32LE (interleaveSampleFormat), but the output pipeline
// must still sustain playback into a sink that only accepts an integer
// PCM format -- exactly what a class-compliant USB interface's raw hw:
// route negotiates, and exactly what [ProbeOutput]'s own
// audioconvert!audioresample-fronted probe pipeline already proved that
// route accepts.
func TestEngineOpensAnIntegerOnlySink(t *testing.T) {
	sink := newCapsRestrictedSink(t, "audio/x-raw,format=S32LE,rate=48000,channels=1")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1},
		ChannelCount:    1,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	requireSustainedPlayback(t, e, sink.buffers)
}

// TestEngineOpensAMultiChannelIntegerSink is
// TestEngineOpensAnIntegerOnlySink's multi-channel, second-format sibling
// -- S24LE across two program channels, no LTC channel -- so the fix is
// not just proven for the single-channel/single-format case.
func TestEngineOpensAMultiChannelIntegerSink(t *testing.T) {
	sink := newCapsRestrictedSink(t, "audio/x-raw,format=S24LE,rate=48000,channels=2")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		ChannelCount:    2,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	requireSustainedPlayback(t, e, sink.buffers)
}

// TestEngineResamplesToTheSinksRate proves the sink-side audioresample
// insertion is load-bearing on its own, not merely redundant with the
// format converter: the engine's configured 48000Hz interior meets a
// sink fixed at 44100Hz, a genuine rate mismatch that only a real
// resampler resolves. At matching rates audioresample measures as a
// passthrough (confirmed separately); this test is what actually
// exercises it doing work.
func TestEngineResamplesToTheSinksRate(t *testing.T) {
	sink := newCapsRestrictedSink(t, "audio/x-raw,format=S32LE,rate=44100,channels=1")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1},
		ChannelCount:    1,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	requireSustainedPlayback(t, e, sink.buffers)
}

// TestEngineRefusesToWidenAMonoChannelOntoAWiderFixedSink is the negative
// sibling proving the sink-side channels-only capsfilter's actual,
// narrow, measured purpose: a single unpositioned channel has no
// positional ambiguity, so audioconvert alone will cleanly upmix it onto
// a wider fixed-channel sink instead of refusing -- measured directly:
// with the capsfilter removed, this exact config (1 channel in,
// channels=2 fixed sink) reaches PLAYING and streams to EOS. The
// capsfilter must turn that into a loud refusal instead, because a show
// channel silently landing on the wrong physical output is worse than
// this route failing to open at all.
//
// This is NOT a general claim that every channel-count mismatch fails
// loudly on its own -- interleave's own channel-positions-from-input=false
// already emits an unpositioned channel-mask for channel counts above
// 1, and audioconvert already refuses to remix one unpositioned
// multi-channel layout onto another regardless of this capsfilter
// (measured separately, and unrelated to this PR).
func TestEngineRefusesToWidenAMonoChannelOntoAWiderFixedSink(t *testing.T) {
	sink := newCapsRestrictedSink(t, "audio/x-raw,format=S32LE,rate=48000,channels=2")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1},
		ChannelCount:    1,
		SampleRate:      48000,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	ok, reason := waitForAvailability(e, false, 3*time.Second)
	if ok {
		t.Fatalf("Available() = true, want false: a mono channel must not be silently upmixed onto a 2-channel fixed sink")
	}
	t.Logf("observed refusal reason: %s", reason)
	if got := sink.buffers.Load(); got != 0 {
		t.Errorf("buffers reached the sink pad = %d, want 0: no data should have flowed once the layout was refused", got)
	}
}
