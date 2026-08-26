package fppconnect

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// maxChannelRangesBytes bounds the formatted string: the v3 ping's Ranges
// field is a fixed 121-byte, null-terminated buffer (120 bytes of content
// plus the terminator) — see pkg/multisync's own wire-layout doc comment,
// which cites ControlProtocol.txt's "Ping type 3" field directly.
const maxChannelRangesBytes = 120

// ChannelRange is one show.surface's channel window, 1-based exactly as
// show.surface.channelRange stores it (startChannel >= 1, channelCount >=
// 1 — internal/coordinator/config.ShowSurfaceChannelRange already refuses
// anything else at write time).
type ChannelRange struct {
	StartChannel int
	ChannelCount int
}

// ErrNoChannelRanges is refused by [FormatChannelRanges] when ranges is
// empty. A node with no configured surface never reaches this function —
// it advertises nothing per RES-003 section 10.1 — so this refusal is for
// a caller that meant to pass at least one range and did not.
var ErrNoChannelRanges = errors.New("fppconnect: no channel ranges to format")

// ErrChannelRangeStartBelowOne is refused when a range's StartChannel is
// below 1: show.surface's channel numbering is 1-based, and a value below
// that is not a channel number this format can convert.
var ErrChannelRangeStartBelowOne = errors.New("fppconnect: channel range start must be at least 1")

// ErrChannelRangeCountBelowOne is refused when a range's ChannelCount is
// below 1: a zero-length range converts to a decreasing (start > end) or
// otherwise nonsensical pair, not a real window. This is a distinct
// failure from RES-003 section 10.1's own "0-0" fact (xLights discards a
// zero-length ADVERTISED STRING and silently renders a full, non-sparse
// FSEQ) — this format can still legitimately EMIT the literal "0-0" for a
// genuinely valid one-channel surface at channel 1 (StartChannel: 1,
// ChannelCount: 1), which is not a defect here; see
// TestFormatChannelRangesSingleChannelSurfaceFormatsToZeroZero.
var ErrChannelRangeCountBelowOne = errors.New("fppconnect: channel range count must be at least 1")

// ErrChannelRangesTooLong is refused when the fully formatted, comma-joined
// string would exceed [maxChannelRangesBytes] — the ping's fixed-size
// ranges field cannot carry it.
var ErrChannelRangesTooLong = errors.New("fppconnect: formatted channel ranges string exceeds the 120-byte ping field")

// FormatChannelRanges converts ranges (1-based, inclusive count, as
// show.surface stores them) into the comma-joined, 0-based, inclusive-end
// string xLights' FPP Connect dialog parses (RES-003 section 10.1): one
// range's start-1 becomes the emitted start, and start+count-2 becomes the
// emitted, inclusive end. Ranges are sorted by StartChannel before joining,
// regardless of the order ranges arrives in, so the emitted string is
// stable across calls with the same set in a different order.
func FormatChannelRanges(ranges []ChannelRange) (string, error) {
	if len(ranges) == 0 {
		return "", ErrNoChannelRanges
	}

	sorted := make([]ChannelRange, len(ranges))
	copy(sorted, ranges)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].StartChannel < sorted[j].StartChannel })

	parts := make([]string, 0, len(sorted))
	for _, r := range sorted {
		if r.StartChannel < 1 {
			return "", fmt.Errorf("%w: got %d", ErrChannelRangeStartBelowOne, r.StartChannel)
		}
		if r.ChannelCount < 1 {
			return "", fmt.Errorf("%w: got %d", ErrChannelRangeCountBelowOne, r.ChannelCount)
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.StartChannel-1, r.StartChannel+r.ChannelCount-2))
	}

	joined := strings.Join(parts, ",")
	if len(joined) > maxChannelRangesBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrChannelRangesTooLong, len(joined))
	}
	return joined, nil
}
