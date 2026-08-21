package audio

import (
	"testing"
	"time"
)

func TestLTCFrameRateValidateAcceptsEveryReservedRate(t *testing.T) {
	for _, r := range []LTCFrameRate{LTCFrameRate24, LTCFrameRate25, LTCFrameRate2997, LTCFrameRate30} {
		if err := r.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", r, err)
		}
	}
}

func TestLTCFrameRateValidateRejectsOutsideClosedVocabulary(t *testing.T) {
	for _, r := range []LTCFrameRate{"", "60", "23.976", "029.97", "30fps"} {
		if err := r.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want an error (not in the closed vocabulary)", r)
		}
	}
}

func TestLTCTimecodeValidateAcceptsWellFormed(t *testing.T) {
	for _, tc := range []LTCTimecode{"00:00:00:00", "23:59:59:99", "01:00:00:00"} {
		if err := tc.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", tc, err)
		}
	}
}

func TestLTCTimecodeValidateRejectsMalformedShape(t *testing.T) {
	for _, tc := range []LTCTimecode{"", "1:00:00:00", "00-00-00-00", "00:00:00", "00:00:00:00:00", "not a timecode"} {
		if err := tc.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want an error (malformed shape)", tc)
		}
	}
}

func TestLTCTimecodeValidateRejectsOutOfRangeFields(t *testing.T) {
	for _, tc := range []LTCTimecode{"24:00:00:00", "00:60:00:00", "00:00:60:00"} {
		if err := tc.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want an error (field out of range)", tc)
		}
	}
}

// TestSessionDesiredStateValidatePropagatesLTCStartOffsetError proves the
// wiring in [SessionDesiredState.Validate], not just [LTCTimecode.
// Validate] in isolation — a session carrying a malformed offset must
// fail validation the same way a malformed Gain or Ceiling does.
func TestSessionDesiredStateValidatePropagatesLTCStartOffsetError(t *testing.T) {
	bad := LTCTimecode("garbage")
	s := SessionDesiredState{LTCStartOffset: &bad}
	if err := s.Validate(); err == nil {
		t.Error("Validate() = nil, want an error for a malformed LTCStartOffset")
	}
}

func TestSessionDesiredStateValidateAcceptsNilLTCStartOffset(t *testing.T) {
	s := SessionDesiredState{}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil (LTCStartOffset unset is valid)", err)
	}
}

// TestApplyRequestMergeSetsLTCStartOffset proves the [Field]-mediated
// merge actually reaches LTCStartOffset: unset leaves it alone, null
// clears it, set replaces it — the same three-state contract every other
// ApplyRequest field already has a test for one field over.
func TestApplyRequestMergeSetsLTCStartOffset(t *testing.T) {
	tc := LTCTimecode("01:00:00:00")
	r := ApplyRequest{LTCStartOffset: SetField(tc)}
	got, _, err := r.Merge(SessionDesiredState{})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got.LTCStartOffset == nil || *got.LTCStartOffset != tc {
		t.Errorf("LTCStartOffset = %v, want %q", got.LTCStartOffset, tc)
	}
}

func TestApplyRequestMergeUnsetLeavesLTCStartOffsetUnchanged(t *testing.T) {
	tc := LTCTimecode("01:00:00:00")
	current := SessionDesiredState{LTCStartOffset: &tc}
	got, _, err := ApplyRequest{}.Merge(current)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got.LTCStartOffset == nil || *got.LTCStartOffset != tc {
		t.Errorf("LTCStartOffset = %v, want unchanged %q", got.LTCStartOffset, tc)
	}
}

func TestApplyRequestMergeNullClearsLTCStartOffset(t *testing.T) {
	tc := LTCTimecode("01:00:00:00")
	current := SessionDesiredState{LTCStartOffset: &tc}
	r := ApplyRequest{LTCStartOffset: NullField[LTCTimecode]()}
	got, _, err := r.Merge(current)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got.LTCStartOffset != nil {
		t.Errorf("LTCStartOffset = %v, want nil after an explicit null", got.LTCStartOffset)
	}
}

func TestLTCTimecodeFrameCountRoundTrip(t *testing.T) {
	cases := []struct {
		tc    LTCTimecode
		rate  LTCFrameRate
		count int64
	}{
		{"00:00:00:00", LTCFrameRate30, 0},
		{"00:00:01:00", LTCFrameRate25, 25},
		{"01:00:00:00", LTCFrameRate24, 86400},
		{"00:10:30:17", LTCFrameRate2997, (10*60+30)*30 + 17},
	}
	for _, c := range cases {
		got, err := c.tc.FrameCount(c.rate)
		if err != nil {
			t.Fatalf("%s at %s: %v", c.tc, c.rate, err)
		}
		if got != c.count {
			t.Errorf("%s at %s: FrameCount = %d, want %d", c.tc, c.rate, got, c.count)
		}
		if back := LTCTimecodeFromFrameCount(got, c.rate); back != c.tc {
			t.Errorf("%s at %s: round trip = %s", c.tc, c.rate, back)
		}
	}
}

// TestLTCTimecodeFrameCountRejectsFrameAboveRate proves the frame field is
// bounded by the rate it is read at, which [LTCTimecode.Validate]
// deliberately does not check.
func TestLTCTimecodeFrameCountRejectsFrameAboveRate(t *testing.T) {
	if _, err := LTCTimecode("00:00:00:27").FrameCount(LTCFrameRate25); err == nil {
		t.Error("frame 27 at 25 fps was accepted, want an error")
	}
}

// TestLTCTimecodeAdvanceAtNonIntegerRate proves 29.97 non-drop timecode
// runs slower than wall time: one wall-clock hour advances the timecode by
// 107892 frames, 108 short of the 108000 a 30 fps hour would count.
func TestLTCTimecodeAdvanceAtNonIntegerRate(t *testing.T) {
	got, err := LTCTimecode("00:00:00:00").Advance(time.Hour, LTCFrameRate2997)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	count, err := got.FrameCount(LTCFrameRate2997)
	if err != nil {
		t.Fatalf("FrameCount: %v", err)
	}
	if count != 107892 {
		t.Errorf("one hour at 29.97 advanced %d frames (%s), want 107892", count, got)
	}
}

// TestLTCTimecodeFromFrameCountWrapsAtOneDay proves the count wraps the
// way SMPTE timecode itself does rather than producing an hour field
// above 23.
func TestLTCTimecodeFromFrameCountWrapsAtOneDay(t *testing.T) {
	perDay := int64(30 * 60 * 60 * 24)
	if got := LTCTimecodeFromFrameCount(perDay+30, LTCFrameRate30); got != "00:00:01:00" {
		t.Errorf("one day plus one second = %s, want 00:00:01:00", got)
	}
}
