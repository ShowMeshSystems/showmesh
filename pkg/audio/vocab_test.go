package audio

import "testing"

func TestSourceRoleValidate(t *testing.T) {
	for _, ok := range []SourceRole{SourceRoleShow, SourceRoleBackground, SourceRoleAnnouncement, SourceRoleManual} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := SourceRole("unknown-role").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestStateValidate(t *testing.T) {
	for _, ok := range []State{StatePreparing, StateReady, StatePlaying, StatePaused, StateStopping, StateStopped, StateCompleted, StateFailed, StateUnknown} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := State("bogus").Validate(); err == nil {
		t.Error("Validate(bogus) = nil, want error")
	}
}

func TestRepeatModeValidate(t *testing.T) {
	for _, ok := range []RepeatMode{RepeatNone, RepeatItem, RepeatPlaylist} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := RepeatMode("loop-forever").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestItemTransitionValidate(t *testing.T) {
	for _, ok := range []ItemTransition{ItemTransitionSequential, ItemTransitionGapless, ItemTransitionCrossfade} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := ItemTransition("smash-cut").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestMixPolicyValidate(t *testing.T) {
	for _, ok := range []MixPolicy{MixPolicyMix, MixPolicyDuck, MixPolicyInterrupt, MixPolicyUnsupported} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := MixPolicy("blend").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestResumePolicyValidate(t *testing.T) {
	for _, ok := range []ResumePolicy{ResumePolicyResume, ResumePolicyRestart} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := ResumePolicy("rewind").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestFadeCurveValidate(t *testing.T) {
	if err := FadeCurveLinear.Validate(); err != nil {
		t.Errorf("Validate(linear) = %v, want nil", err)
	}
	if err := FadeCurve("exponential").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestValidateItemTransitionSupport(t *testing.T) {
	if err := ValidateItemTransitionSupport(ItemTransitionSequential, false); err != nil {
		t.Errorf("sequential with no confirmation: got %v, want nil", err)
	}
	if err := ValidateItemTransitionSupport(ItemTransitionGapless, true); err != nil {
		t.Errorf("gapless with confirmation: got %v, want nil", err)
	}
	if err := ValidateItemTransitionSupport(ItemTransitionCrossfade, true); err != nil {
		t.Errorf("crossfade with confirmation: got %v, want nil", err)
	}
	if err := ValidateItemTransitionSupport(ItemTransitionGapless, false); err == nil {
		t.Error("gapless with no confirmation: got nil, want error")
	}
	if err := ValidateItemTransitionSupport(ItemTransitionCrossfade, false); err == nil {
		t.Error("crossfade with no confirmation: got nil, want error")
	}
}

func TestValidateItemTransitionSupportRejectsUnknownTransition(t *testing.T) {
	if err := ValidateItemTransitionSupport(ItemTransition("smash-cut"), true); err == nil {
		t.Error("unknown transition with confirmation: got nil, want error")
	}
}
