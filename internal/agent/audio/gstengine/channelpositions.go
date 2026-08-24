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
// at channelCount channels and sampleRate, require a positioned layout,
// and if so returns [channelPositionBits]'s candidate for channelCount;
// otherwise it returns nil, leaving interleave's pre-existing unpositioned
// output in place.
//
// A real ALSA sink reports its actual device-restricted caps only once
// it has opened the device, which happens on the NULL->READY state
// transition -- sinkEl is brought to READY here so this query reflects
// what the bound route genuinely accepts rather than an unconstrained
// template. (The caller's own later pipeline-wide SetState(PLAYING)
// still governs the element's final state; READY here is only to make
// the query meaningful, and a real device that later fails to reach
// PLAYING is reported the same way it always was, through the bus and
// Available().)
//
// The decision is made by intersecting the sink's accepted caps against
// two candidates -- an explicitly unpositioned one (channel-mask=0x0)
// and channelPositionBits(channelCount)'s combined mask -- rather than
// by inspecting the accepted caps' own channel-mask field, because a
// sink with no positional opinion (most test sinks, and any device that
// does not itself constrain the mask) accepts both, and the existing
// unpositioned behavior must still be the answer in that case: it is
// what today's callers, including several already-passing tests fixed
// to a bare "channels=N" with no mask field, depend on. Measured on this
// exact query shape: a 2-channel sink with no mask field accepts both
// candidates (unpositioned wins); a sink fixed to channelPositionBits'
// own mask (e.g. the MOTU M4's negotiated 0x33 at 4 channels) accepts
// only the positioned candidate; a sink fixed to some other mask (e.g.
// 0x0f) accepts neither, so this returns nil and the mismatch fails
// loudly at link time via [linkInterleaveToSink]'s own mask pin rather
// than silently remixing.
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

	var fallbackMask uint64
	for _, bit := range fallback {
		fallbackMask |= bit
	}

	filter := gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d", sampleRate, channelCount))
	accepted := pad.QueryCaps(filter)
	if accepted == nil || accepted.IsEmpty() {
		return nil
	}

	unpositioned := gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d,channel-mask=(bitmask)0x0", sampleRate, channelCount))
	if accepted.CanIntersect(unpositioned) {
		return nil
	}
	positioned := gst.CapsFromString(fmt.Sprintf("audio/x-raw,rate=%d,channels=%d,channel-mask=(bitmask)0x%x", sampleRate, channelCount, fallbackMask))
	if accepted.CanIntersect(positioned) {
		return fallback
	}
	return nil
}
