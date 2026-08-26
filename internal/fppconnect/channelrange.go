package fppconnect

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// ChannelRange is one show.surface's channel window, 1-based exactly as
// show.surface.channelRange stores it (startChannel >= 1, channelCount >=
// 1, internal/coordinator/config.ShowSurfaceChannelRange already refuses
// anything else at write time).
type ChannelRange struct {
	StartChannel int
	ChannelCount int
}

// ErrNoChannelRanges is refused by [FormatChannelRanges] when ranges is
// empty. A node with no configured surface never reaches this function:
// it advertises nothing per RES-003 section 10.1, so this refusal is for
// a caller that meant to pass at least one range and did not.
var ErrNoChannelRanges = errors.New("fppconnect: no channel ranges to format")

// ErrChannelRangeStartBelowOne is refused when a range's StartChannel is
// below 1: show.surface's channel numbering is 1-based, and a value below
// that is not a channel number this format can convert.
var ErrChannelRangeStartBelowOne = errors.New("fppconnect: channel range start must be at least 1")

// ErrChannelRangeCountBelowOne is refused when a range's ChannelCount is
// below 1: a zero-length range converts to a decreasing (start > end) or
// otherwise nonsensical pair, not a real window.
var ErrChannelRangeCountBelowOne = errors.New("fppconnect: channel range count must be at least 1")

// ErrChannelRangesTooLong is refused when the fully formatted, comma-joined
// string would exceed [multisync.MaxPingRangesLength], the ping's
// fixed-size ranges field cannot carry it.
var ErrChannelRangesTooLong = fmt.Errorf("fppconnect: formatted channel ranges string exceeds the %d-byte ping field", multisync.MaxPingRangesLength)

// ErrSingleChannelSurfaceAtChannelOne is refused when the formatted output
// would be the literal string "0-0", the one input (a single range,
// StartChannel: 1, ChannelCount: 1) that produces it. RES-003 section
// 10.1 records that xLights' ping parser discards exactly that literal
// and silently falls back to rendering a full, non-sparse FSEQ, so this
// formatter refuses to produce it rather than advertising a range xLights
// would treat as no range at all.
var ErrSingleChannelSurfaceAtChannelOne = errors.New("fppconnect: a single-channel surface at channel 1 cannot be advertised: xLights discards 0-0")

// FormatChannelRanges converts ranges (1-based, inclusive count, as
// show.surface stores them) into the comma-joined, 0-based, inclusive-end
// string xLights' FPP Connect dialog parses (RES-003 section 10.1): one
// range's start-1 becomes the emitted start, and start+count-2 becomes the
// emitted, inclusive end. Ranges are sorted by StartChannel before joining,
// regardless of the order ranges arrives in, so the emitted string is
// stable across calls with the same set in a different order. Never
// returns the literal "0-0", see [ErrSingleChannelSurfaceAtChannelOne].
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
	if len(joined) > multisync.MaxPingRangesLength {
		return "", fmt.Errorf("%w: %d bytes", ErrChannelRangesTooLong, len(joined))
	}
	if joined == "0-0" {
		return "", ErrSingleChannelSurfaceAtChannelOne
	}
	return joined, nil
}
