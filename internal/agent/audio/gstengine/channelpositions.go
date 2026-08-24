//go:build cgo

package gstengine

import "github.com/go-gst/go-gst/pkg/gstaudio"

// channelPositionBits reports the per-channel GstAudioChannelPosition
// bitmask this package assigns each of channelCount output channels, in
// ascending 1-based channel order, or nil if channelCount has no standard
// positioned layout to fall back to.
//
// The source of truth is GStreamer's own gst_audio_channel_get_fallback_mask
// (wrapped here as [gstaudio.AudioChannelGetFallbackMask]): the same
// layout GStreamer itself assigns a positionless N-channel stream when one
// is needed, defined for channel counts 1 through 8 and returning a 0
// mask (meaning "no defined fallback") outside that range. Using it rather
// than deriving a layout from a specific device's own driver-reported
// channel map keeps this package's output layout decision independent of
// probe evidence [audio.ProbeResult] does not currently carry, and
// reversible: a later change to probe evidence-derived positions replaces
// this one function, not the pipeline wiring around it.
//
// A single-channel stream's fallback mask is 0 (mono is unpositioned by
// definition), which this function folds into the same "no layout" nil
// return as an unsupported wide channel count -- both leave the existing
// unpositioned interleave behavior in place rather than fabricate a
// position no one asked for.
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
	if len(bits) != channelCount {
		// The fallback table never sets fewer bits than channelCount for
		// any count it actually covers (verified for 1-8 above); this is
		// a defensive fallback to the pre-existing unpositioned behavior
		// rather than handing back a short, mismatched slice.
		return nil
	}
	return bits
}
