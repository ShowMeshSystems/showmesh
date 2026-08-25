//go:build cgo

package gstengine

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// newPositionedBaseSink builds a real GstBaseSink (appsink) fixed to
// caps, standing in for alsasink on a raw hw: route. The distinction
// from [newCapsRestrictedSink] is load-bearing rather than cosmetic: a
// capsfilter answers ACCEPT_CAPS through GstBaseTransform, which allows
// a partially specified query, while a GstBaseSink answers it with
// gst_caps_is_subset against its own allowed caps, so a query that
// leaves format or layout unspecified is refused outright. alsasink is a
// GstBaseSink, so only this shape reproduces what a real device does to
// [probeSinkChannelPositions].
func newPositionedBaseSink(t *testing.T, caps string) capsRestrictedSink {
	t.Helper()
	gst.Init()
	sink := gst.ElementFactoryMake("appsink", "sink")
	if sink == nil {
		t.Fatalf("could not create appsink")
	}
	sink.SetObjectProperty("caps", gst.CapsFromString(caps))
	sink.SetObjectProperty("sync", false)
	sink.SetObjectProperty("drop", true)
	sink.SetObjectProperty("max-buffers", uint32(1))
	var count atomic.Int64
	sink.GetStaticPad("sink").AddProbe(gst.PadProbeTypeBuffer, func(_ gst.Pad, _ *gst.PadProbeInfo) gst.PadProbeReturn {
		count.Add(1)
		return gst.PadProbeOK
	})
	return capsRestrictedSink{element: sink, buffers: &count}
}

// motuLikeSinkCaps is the caps a real alsasink reports for the MOTU M4's
// hw:CARD=M4,DEV=0 route: an integer format, interleaved layout, four
// channels and the positioned channel-mask 0x33 that device demands.
const motuLikeSinkCaps = "audio/x-raw,format=(string)S32LE,layout=(string)interleaved,rate=(int)44100,channels=(int)4,channel-mask=(bitmask)0x33"

// TestProbeSinkChannelPositionsAsksAQuestionABaseSinkCanAnswer pins the
// negotiation defect at its own level: the positioned candidate must be
// put to the sink as caps it can actually judge. Asking with format and
// layout left open makes gst_pad_query_accept_caps's subset check say no
// on every real GstBaseSink, so the engine silently fell back to an
// unpositioned layout the device then refused once data flowed.
func TestProbeSinkChannelPositionsAsksAQuestionABaseSinkCanAnswer(t *testing.T) {
	sink := newPositionedBaseSink(t, motuLikeSinkCaps)
	got := probeSinkChannelPositions(sink.element, 4, 44100)
	if len(got) == 0 {
		t.Fatalf("probeSinkChannelPositions = nil, want the positioned layout a base sink fixed to channel-mask 0x33 demands")
	}
	var mask uint64
	for _, bit := range got {
		mask |= bit
	}
	if mask != 0x33 {
		t.Errorf("combined mask = %#x, want 0x33", mask)
	}
}

// TestProbeSinkChannelPositionsReleasesTheSinkItOpened pins the probe's
// own device handling: it brings the sink to READY to make its caps
// query meaningful, and READY on a raw hw: route is an open ALSA device.
// A build that fails after this point would otherwise leave the device
// held with nothing left to close it.
func TestProbeSinkChannelPositionsReleasesTheSinkItOpened(t *testing.T) {
	sink := newPositionedBaseSink(t, motuLikeSinkCaps)
	probeSinkChannelPositions(sink.element, 4, 44100)
	if got := sinkElementState(t, sink.element); got != gst.StateNull {
		t.Errorf("sink element state after the probe = %v, want %v", got, gst.StateNull)
	}
}

// TestEngineOpensAPositionedBaseSink is the whole-engine reproduction of
// the MOTU M4 failure: program on channels 1 and 2, LTC on 3, channel 4
// silent, 44100Hz, into a real GstBaseSink demanding channel-mask 0x33.
// Before the probe asked a question a base sink could answer, this
// reported Available()==true and then failed asynchronously with
// not-negotiated named at a keep-alive audiotestsrc.
func TestEngineOpensAPositionedBaseSink(t *testing.T) {
	sink := newPositionedBaseSink(t, motuLikeSinkCaps)
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    4,
		SampleRate:      44100,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if ok, reason := e.Available(); !ok {
		t.Fatalf("New reported the engine unavailable: %s", reason)
	}
	requireSustainedPlayback(t, e, sink.buffers)
}

// TestChannelPositionIsPinnedDownstreamOfTheMixer pins the topology
// rule the positioned layout depends on: audiomixer does not carry a
// channel-mask through to its src pad, so a position pinned on the
// chains FEEDING a mixer is lost across it and interleave sees an
// unpositioned mono input. MEASURED by hand against a MOTU M4: mono
// capsfilters with masks, then audiomixer, then interleave goes
// not-negotiated, while the same chain with the mask pinned in a
// capsfilter AFTER the mixer runs to completion. This asserts the caps
// each program channel actually hands interleave, not merely that the
// pipeline negotiated.
func TestChannelPositionIsPinnedDownstreamOfTheMixer(t *testing.T) {
	sink := newPositionedBaseSink(t, motuLikeSinkCaps)
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    4,
		SampleRate:      44100,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	requireSustainedPlayback(t, e, sink.buffers)

	for _, tc := range []struct {
		element string
		mask    string
	}{
		{"mixer-caps-ch1", "channel-mask=(bitmask)0x0000000000000001"},
		{"mixer-caps-ch2", "channel-mask=(bitmask)0x0000000000000002"},
	} {
		el := e.pipeline.(gst.Bin).GetByName(tc.element)
		if el == nil {
			t.Fatalf("no element named %q: every program channel must pin its position AFTER its mixer", tc.element)
		}
		caps := el.GetStaticPad("src").GetCurrentCaps()
		if caps == nil {
			t.Fatalf("%s negotiated no caps", tc.element)
		}
		if got := caps.String(); !strings.Contains(got, tc.mask) {
			t.Errorf("%s src caps = %q, want %s: the position must survive the mixer", tc.element, got, tc.mask)
		}
	}
}

// TestEngineReportsAFailedBuildRatherThanAnAsyncFailure pins the
// reporting half: a build that cannot sustain PLAYING must come back
// from New already unavailable, not report itself available and then go
// unavailable off the bus a moment later. The sink here is fixed to a
// positioned layout this engine is not assigned, which no negotiation
// can satisfy.
func TestEngineReportsAFailedBuildRatherThanAnAsyncFailure(t *testing.T) {
	sink := newPositionedBaseSink(t, "audio/x-raw,format=(string)S32LE,layout=(string)interleaved,rate=(int)44100,channels=(int)4,channel-mask=(bitmask)0x0f")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    4,
		SampleRate:      44100,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	ok, reason := e.Available()
	if ok {
		t.Fatalf("Available() = true immediately after New, want false: a build that cannot sustain PLAYING must be reported as a failed build")
	}
	t.Logf("observed build failure reason: %s", reason)
}

// TestFailedBuildReleasesTheSinkDevice pins the device-holding half: an
// engine that reports itself unavailable from a failed build must not
// still hold its output element open. A real ALSA device left in READY
// reads as state OPEN in /proc/asound and nothing else can claim it, so
// the sink element must be back at NULL.
func TestFailedBuildReleasesTheSinkDevice(t *testing.T) {
	sink := newPositionedBaseSink(t, "audio/x-raw,format=(string)S32LE,layout=(string)interleaved,rate=(int)44100,channels=(int)4,channel-mask=(bitmask)0x0f")
	useSinkElement(t, sink.element)

	cfg := Config{
		SinkFactory:     "fakesink",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		ChannelCount:    4,
		SampleRate:      44100,
		Resolve:         resolveByRuntimeFilename,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	if ok, _ := e.Available(); ok {
		t.Fatalf("Available() = true, want false for this sink")
	}
	if got := sinkElementState(t, sink.element); got != gst.StateNull {
		t.Errorf("sink element state = %v, want %v: an engine reporting itself unavailable must not still hold its output device", got, gst.StateNull)
	}
}

// sinkElementState reports el's own current GStreamer state, waiting out
// any in-progress asynchronous transition.
func sinkElementState(t *testing.T, el gst.Element) gst.State {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		state, _, _ := el.GetState(gst.ClockTime(100 * time.Millisecond))
		if state == gst.StateNull || time.Now().After(deadline) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
}
