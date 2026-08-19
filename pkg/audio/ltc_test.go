package audio

import "testing"

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
