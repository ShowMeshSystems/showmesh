package audio

import "testing"

func TestOutcomeValidate(t *testing.T) {
	for _, ok := range []Outcome{OutcomeStarted, OutcomePosition, OutcomeGain, OutcomeFadeComplete, OutcomeStopped, OutcomeCompleted, OutcomeRefused, OutcomeFailed, OutcomeUnconfirmable} {
		if err := ok.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	if err := Outcome("succeeded").Validate(); err == nil {
		t.Error("Validate(unknown) = nil, want error")
	}
}

func TestOutcomeResultRequiresReason(t *testing.T) {
	requiresReason := []Outcome{OutcomeRefused, OutcomeFailed, OutcomeUnconfirmable}
	for _, o := range requiresReason {
		if err := (OutcomeResult{Outcome: o}).Validate(); err == nil {
			t.Errorf("%q with empty reason: got nil, want error", o)
		}
		if err := (OutcomeResult{Outcome: o, Reason: "some reason"}).Validate(); err != nil {
			t.Errorf("%q with reason: got %v, want nil", o, err)
		}
	}

	reasonOptional := []Outcome{OutcomeStarted, OutcomePosition, OutcomeGain, OutcomeFadeComplete, OutcomeStopped, OutcomeCompleted}
	for _, o := range reasonOptional {
		if err := (OutcomeResult{Outcome: o}).Validate(); err != nil {
			t.Errorf("%q with no reason: got %v, want nil", o, err)
		}
		if err := (OutcomeResult{Outcome: o, Reason: "observed"}).Validate(); err != nil {
			t.Errorf("%q with reason: got %v, want nil", o, err)
		}
	}
}
