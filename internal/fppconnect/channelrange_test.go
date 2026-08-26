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
	// Each range formats to roughly 10 bytes ("1000-1149,") — 20 ranges push
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

// TestFormatChannelRangesSingleChannelSurfaceFormatsToZeroZero documents a
// genuinely legal edge case: a one-channel surface starting at channel 1
// (1-based) converts to the literal string "0-0" — the same string RES-003
// section 10.1 records xLights silently discarding as an empty
// advertisement. That collision is real and is not this formatter's to
// resolve (it has no narrower way to say "exactly channel 0"); refusing a
// count of exactly 1 would be refusing a valid surface for a problem that
// belongs to xLights' own parser.
func TestFormatChannelRangesSingleChannelSurfaceFormatsToZeroZero(t *testing.T) {
	got, err := FormatChannelRanges([]ChannelRange{{StartChannel: 1, ChannelCount: 1}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0-0" {
		t.Fatalf("got %q, want %q", got, "0-0")
	}
}
