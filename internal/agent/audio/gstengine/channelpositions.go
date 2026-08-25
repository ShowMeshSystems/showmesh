//go:build cgo

package gstengine

import (
	"fmt"

	"github.com/go-gst/go-gst/pkg/gst"
	"github.com/go-gst/go-gst/pkg/gstaudio"
)

// channelPositionBits reports the per-channel GstAudioChannelPosition
// bitmask a positioned layout would assign each of channelCount output
// channels, in ascending 1-based channel order, or nil if channelCount
// has no standard positioned layout at all. It is a candidate, not a
// decision: [probeSinkChannelPositions] is what decides whether the
// bound sink actually wants these positions or the pre-existing
// unpositioned layout, by querying the sink itself rather than assuming
// from channel count alone.
//
// The source of truth is GStreamer's own gst_audio_channel_get_fallback_mask
// (wrapped here as [gstaudio.AudioChannelGetFallbackMask]): the same
// layout GStreamer itself assigns a positionless N-channel stream when one
// is needed, defined for channel counts 1 through 8 and returning a 0
// mask (meaning "no defined fallback") outside that range.
//
// A single-channel stream's fallback mask is 0 (mono is unpositioned by
// definition), which this function folds into the same "no layout" nil
// return as an unsupported wide channel count.
//
// Measured against a real MOTU M4 (4 channels): this returns
// [FL, FR, RL, RR] (0x1, 0x2, 0x10, 0x20), which sums to the 0x33
// channel-mask alsasink itself negotiated for that device's hw: route.
func channelPositionBits(channelCount int) []uint64 {
	mask := gstaudio.AudioChannelGetFallbackMask(int32(channelCount))
	if mask == 0 {
		return nil
	}
	bits := make([]uint64, 0, channelCount)
	for bit := uint(0); bit < 64 && len(bits) < channelCount; bit++ {
		if mask&(1<<bit) != 0 {
			bits = append(bits, uint64(1)<<bit)
		}
	}
	return bits
}

// probeSinkChannelPositions decides whether sinkEl's own accepted caps,
// at channelCount channels and sampleRate, will take a positioned
// layout, and if so returns [channelPositionBits]'s candidate for
// channelCount; otherwise it returns nil, leaving interleave's
// pre-existing unpositioned output in place.
//
// A real ALSA sink reports its actual device-restricted caps only once
// it has opened the device, which happens on the NULL->READY state
// transition -- sinkEl is brought to READY here so this query reflects
// what the bound route genuinely accepts rather than an unconstrained
// template. It is returned to NULL before this function returns: READY
// on a raw hw: route is an open ALSA device (it reads as state OPEN in
// /proc/asound), and a build that fails after this point would otherwise
// leave that device held by a process reporting itself unavailable. The
// caller's own later pipeline-wide SetState(PLAYING) reopens it.
//
// The candidate goes to the pad as a CAPS query filter, not as an
// ACCEPT_CAPS question. ACCEPT_CAPS is a yes/no on caps the caller must
// already have fixed: a GstBaseSink -- which alsasink is -- answers it
// with gst_caps_is_subset against its own allowed caps, so a candidate
// that leaves format or layout unspecified, as one assembled from rate,
// channel count and mask alone must, is refused outright however well
// the layout itself matches. MEASURED: a base sink fixed to the MOTU
// M4's own S32LE/interleaved/44100/4ch/0x33 caps refuses that question
// and this function reported "no positioned layout" for a device that
// demands exactly one, which is what left the engine emitting an
// unpositioned layout the M4 then refused once data flowed. A
// capsfilter-fronted test sink hides it entirely, because
// GstBaseTransform answers ACCEPT_CAPS by intersection and says yes.
// A filtered CAPS query asks what this function actually wants to know
// -- what the pad can still do under this constraint -- and every pad
// answers it the same way.
//
// The decision prefers positioned over unpositioned, falling back to
// unpositioned only when the sink's accepted caps are not compatible
// with channelPositionBits(channelCount)'s combined mask -- MEASURED
// against a real MOTU M4 on hw:0,0 to be the only order that works: a
// capsfilter explicitly fixed to channel-mask=0x0 links to alsasink
// fine there, but fails at streaming with not-negotiated once data
// actually flows, while 0x33 (channelPositionBits' own candidate for 4
// channels) plays to completion. alsasink's own queried caps intersect
// BOTH an unpositioned and a positioned candidate on that device, so a
// sink accepting either is telling this probe only what caps it can
// parse, not what the underlying device driver will actually take once
// opened -- the more specific, positioned layout is the safer choice
// whenever the sink can express it, and unpositioned is the fallback
// reserved for a sink whose caps genuinely rule positioned out. A sink
// fixed to some other, mismatched positioned mask (e.g. 0x0f) leaves the
// query empty, so this still returns nil for that case; on a real
// device that mismatch was also measured to fail only once data starts
// streaming, not at link time, so [linkInterleaveToSink]'s own mask pin
// is a runtime refusal there, not the link-time one a fixed-caps test
// sink shows.
func probeSinkChannelPositions(sinkEl gst.Element, channelCount, sampleRate int) []uint64 {
	fallback := channelPositionBits(channelCount)
	if len(fallback) == 0 {
		return nil
	}
	pad := sinkEl.GetStaticPad("sink")
	if pad == nil {
		return nil
	}
	if sinkEl.SetState(gst.StateReady) == gst.StateChangeFailure {
		return nil
	}
	defer sinkEl.SetState(gst.StateNull)

	var fallbackMask uint64
	for _, bit := range fallback {
		fallbackMask |= bit
	}

	filter := gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d", sampleRate, channelCount))
	accepted := pad.QueryCaps(filter)
	if accepted == nil || accepted.IsEmpty() {
		return nil
	}

	positioned := gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d,channel-mask=(bitmask)0x%x", sampleRate, channelCount, fallbackMask))
	remaining := pad.QueryCaps(positioned)
	if remaining == nil || remaining.IsEmpty() {
		return nil
	}
	return fallback
}
