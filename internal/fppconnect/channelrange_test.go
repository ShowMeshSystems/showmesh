package fppconnect

import (
	"errors"
	"testing"
)

func TestFormatChannelRangesSurfaceStartingAtChannelOne(t *testing.T) {
	got, err := FormatChannelRanges([]ChannelRange{{StartChannel: 1, ChannelCount: 150}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0-149" {
		t.Fatalf("got %q, want %q", got, "0-149")
	}
}

func TestFormatChannelRangesSurfaceStartingMidRange(t *testing.T) {
	got, err := FormatChannelRanges([]ChannelRange{{StartChannel: 301, ChannelCount: 150}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "300-449" {
		t.Fatalf("got %q, want %q", got, "300-449")
	}
}

func TestFormatChannelRangesTwoSurfacesOnOneNode(t *testing.T) {
	got, err := FormatChannelRanges([]ChannelRange{
		{StartChannel: 301, ChannelCount: 150},
		{StartChannel: 1, ChannelCount: 150},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sorted by start regardless of input order.
	if got != "0-149,300-449" {
		t.Fatalf("got %q, want %q", got, "0-149,300-449")
	}
}

func TestFormatChannelRangesRefusesEmpty(t *testing.T) {
	_, err := FormatChannelRanges(nil)
	if !errors.Is(err, ErrNoChannelRanges) {
		t.Fatalf("err = %v, want ErrNoChannelRanges", err)
	}
}

func TestFormatChannelRangesRefusesStartBelowOne(t *testing.T) {
	_, err := FormatChannelRanges([]ChannelRange{{StartChannel: 0, ChannelCount: 10}})
	if !errors.Is(err, ErrChannelRangeStartBelowOne) {
		t.Fatalf("err = %v, want ErrChannelRangeStartBelowOne", err)
	}
}

func TestFormatChannelRangesRefusesCountBelowOne(t *testing.T) {
	_, err := FormatChannelRanges([]ChannelRange{{StartChannel: 1, ChannelCount: 0}})
	if !errors.Is(err, ErrChannelRangeCountBelowOne) {
		t.Fatalf("err = %v, want ErrChannelRangeCountBelowOne", err)
	}
}

func TestFormatChannelRangesRefusesTooLong(t *testing.T) {
	// Each range formats to roughly 10 bytes ("1000-1149,"), 20 ranges push
	// well past the 120-byte ping field.
	ranges := make([]ChannelRange, 0, 20)
	for i := 0; i < 20; i++ {
		start := 1 + i*200
		ranges = append(ranges, ChannelRange{StartChannel: start, ChannelCount: 150})
	}
	_, err := FormatChannelRanges(ranges)
	if !errors.Is(err, ErrChannelRangesTooLong) {
		t.Fatalf("err = %v, want ErrChannelRangesTooLong", err)
	}
}

// TestFormatChannelRangesRefusesSingleChannelSurfaceAtChannelOne proves the
// formatter refuses to produce the literal string "0-0": a one-channel
// surface starting at channel 1 (1-based) is the one input that converts
// to it, and RES-003 section 10.1 records xLights silently discarding
// exactly that literal as an empty advertisement (falling back to a full,
// non-sparse FSEQ) rather than treating it as a genuine one-channel
// window.
func TestFormatChannelRangesRefusesSingleChannelSurfaceAtChannelOne(t *testing.T) {
	_, err := FormatChannelRanges([]ChannelRange{{StartChannel: 1, ChannelCount: 1}})
	if !errors.Is(err, ErrSingleChannelSurfaceAtChannelOne) {
		t.Fatalf("err = %v, want ErrSingleChannelSurfaceAtChannelOne", err)
	}
}
