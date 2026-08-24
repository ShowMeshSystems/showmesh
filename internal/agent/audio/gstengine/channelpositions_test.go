//go:build cgo

package gstengine

import "testing"

// TestProbeSinkChannelPositionsRejectsAMismatchedFixedMask is the direct
// unit-level reproduction of the doc comment's own example: a sink whose
// accepted caps are fixed to channel-mask 0x0f at 4 channels accepts
// neither the unpositioned layout nor channelPositionBits' own 0x33
// candidate, so probeSinkChannelPositions must return nil. This guards
// that contract directly, at the probe level, rather than only through
// the engine-level TestEngineRefusesAMismatchedPositionedLayoutInsteadOfRemixing,
// which passes even when the probe itself falls back incorrectly because
// [linkInterleaveToSink]'s own mask pin refuses the link independently.
func TestProbeSinkChannelPositionsRejectsAMismatchedFixedMask(t *testing.T) {
	sink := newCapsRestrictedSink(t, "audio/x-raw,format=S32LE,rate=48000,channels=4,channel-mask=(bitmask)0x0f")

	got := probeSinkChannelPositions(sink.element, 4, 48000)
	if got != nil {
		t.Fatalf("probeSinkChannelPositions = %#x, want nil: a sink fixed to channel-mask 0x0f accepts neither the unpositioned layout nor the 0x33 positioned candidate", got)
	}
}
