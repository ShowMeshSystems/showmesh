package audio

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestGainValidate(t *testing.T) {
	if err := Gain(0).Validate(); err != nil {
		t.Errorf("Validate(0): got %v, want nil", err)
	}
	if err := Gain(2.5).Validate(); err != nil {
		t.Errorf("Validate(2.5): got %v, want nil", err)
	}
	if err := Gain(-0.1).Validate(); !errors.Is(err, ErrGainInvalid) {
		t.Errorf("Validate(-0.1): got %v, want ErrGainInvalid", err)
	}
	if err := Gain(math.NaN()).Validate(); !errors.Is(err, ErrGainInvalid) {
		t.Errorf("Validate(NaN): got %v, want ErrGainInvalid", err)
	}
	if err := Gain(math.Inf(1)).Validate(); !errors.Is(err, ErrGainInvalid) {
		t.Errorf("Validate(+Inf): got %v, want ErrGainInvalid", err)
	}
}

func TestCeilingValidate(t *testing.T) {
	if err := Ceiling(1).Validate(); err != nil {
		t.Errorf("Validate(1): got %v, want nil", err)
	}
	if err := Ceiling(0).Validate(); !errors.Is(err, ErrCeilingInvalid) {
		t.Errorf("Validate(0): got %v, want ErrCeilingInvalid", err)
	}
	if err := Ceiling(-1).Validate(); !errors.Is(err, ErrCeilingInvalid) {
		t.Errorf("Validate(-1): got %v, want ErrCeilingInvalid", err)
	}
	if err := Ceiling(math.NaN()).Validate(); !errors.Is(err, ErrCeilingInvalid) {
		t.Errorf("Validate(NaN): got %v, want ErrCeilingInvalid", err)
	}
}

func TestApplyCeilingClampsAndReports(t *testing.T) {
	r, err := ApplyCeiling(2.0, Ceiling(1.0))
	if err != nil {
		t.Fatalf("ApplyCeiling(2.0, 1.0): got err %v, want nil", err)
	}
	if !r.Clamped {
		t.Fatal("ApplyCeiling(2.0, 1.0): got Clamped=false, want true")
	}
	if r.Effective != 1.0 {
		t.Fatalf("ApplyCeiling(2.0, 1.0): got Effective=%v, want 1.0", r.Effective)
	}
	if r.Requested != 2.0 {
		t.Fatalf("ApplyCeiling(2.0, 1.0): got Requested=%v, want 2.0 (original request preserved)", r.Requested)
	}
}

func TestApplyCeilingPassesThroughWithinLimit(t *testing.T) {
	r, err := ApplyCeiling(0.5, Ceiling(1.0))
	if err != nil {
		t.Fatalf("ApplyCeiling(0.5, 1.0): got err %v, want nil", err)
	}
	if r.Clamped {
		t.Fatal("ApplyCeiling(0.5, 1.0): got Clamped=true, want false")
	}
	if r.Effective != 0.5 {
		t.Fatalf("ApplyCeiling(0.5, 1.0): got Effective=%v, want 0.5", r.Effective)
	}
}

func TestApplyCeilingAtExactCeilingNotClamped(t *testing.T) {
	r, err := ApplyCeiling(1.0, Ceiling(1.0))
	if err != nil {
		t.Fatalf("ApplyCeiling(1.0, 1.0): got err %v, want nil", err)
	}
	if r.Clamped {
		t.Fatal("ApplyCeiling(1.0, 1.0): got Clamped=true, want false (equal to ceiling is not over it)")
	}
}

func TestApplyCeilingRejectsInvalidGain(t *testing.T) {
	if _, err := ApplyCeiling(-1, Ceiling(1.0)); !errors.Is(err, ErrGainInvalid) {
		t.Errorf("ApplyCeiling(-1, 1.0): got %v, want ErrGainInvalid", err)
	}
}

func TestApplyCeilingRejectsInvalidCeiling(t *testing.T) {
	if _, err := ApplyCeiling(0.5, Ceiling(0)); !errors.Is(err, ErrCeilingInvalid) {
		t.Errorf("ApplyCeiling(0.5, 0): got %v, want ErrCeilingInvalid", err)
	}
	if _, err := ApplyCeiling(0.5, Ceiling(-1)); !errors.Is(err, ErrCeilingInvalid) {
		t.Errorf("ApplyCeiling(0.5, -1): got %v, want ErrCeilingInvalid", err)
	}
}

func TestFadeValidate(t *testing.T) {
	ok := Fade{Curve: FadeCurveLinear, Duration: time.Second, TargetGain: 0.8}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate(valid fade): got %v, want nil", err)
	}

	badCurve := ok
	badCurve.Curve = "sine"
	if err := badCurve.Validate(); err == nil {
		t.Error("Validate(bad curve): got nil, want error")
	}

	badDuration := ok
	badDuration.Duration = 0
	if err := badDuration.Validate(); !errors.Is(err, ErrFadeDurationNonPositive) {
		t.Errorf("Validate(zero duration): got %v, want ErrFadeDurationNonPositive", err)
	}

	negGain := ok
	negGain.TargetGain = -1
	if err := negGain.Validate(); !errors.Is(err, ErrGainInvalid) {
		t.Errorf("Validate(negative target gain): got %v, want ErrGainInvalid", err)
	}
}
