package remoteoutput

import (
	"errors"
	"testing"
)

func TestProvisioningStateValidate(t *testing.T) {
	for _, s := range []ProvisioningState{
		ProvisioningNotAttempted, ProvisioningAttempted, ProvisioningAcknowledged,
		ProvisioningManuallyVerified, ProvisioningFailed, ProvisioningUnknown,
	} {
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%s): got %v, want nil", s, err)
		}
	}
	if err := ProvisioningState("ready").Validate(); !errors.Is(err, ErrUnknownProvisioningState) {
		t.Errorf("Validate(ready): got %v, want ErrUnknownProvisioningState (an upload attempt is never ready)", err)
	}
	if len(provisioningStates) != 6 {
		t.Errorf("provisioning vocabulary has %d members, want exactly 6", len(provisioningStates))
	}
}

func TestTriggerValidateHasNoStartMember(t *testing.T) {
	for _, tr := range []Trigger{TriggerAssetIngested, TriggerDestinationAssigned, TriggerConfigurationChanged, TriggerRetry} {
		if err := tr.Validate(); err != nil {
			t.Errorf("Validate(%s): got %v, want nil", tr, err)
		}
	}
	if err := Trigger("start").Validate(); !errors.Is(err, ErrUnknownTrigger) {
		t.Errorf("Validate(start): got %v, want ErrUnknownTrigger — start must never be a valid provisioning trigger", err)
	}
	if len(triggers) != 4 {
		t.Errorf("trigger vocabulary has %d members, want exactly 4", len(triggers))
	}
}

func TestDestinationValidate(t *testing.T) {
	if err := (Destination{ID: "d1", ConfigRevision: "r1"}).Validate(); err != nil {
		t.Errorf("Validate(complete): got %v, want nil", err)
	}
	if err := (Destination{ConfigRevision: "r1"}).Validate(); !errors.Is(err, ErrDestinationIncomplete) {
		t.Errorf("Validate(no id): got %v, want ErrDestinationIncomplete", err)
	}
	if err := (Destination{ID: "d1"}).Validate(); !errors.Is(err, ErrDestinationIncomplete) {
		t.Errorf("Validate(no config revision): got %v, want ErrDestinationIncomplete", err)
	}
}

func TestManualVerificationValidate(t *testing.T) {
	ok := ManualVerification{Destination: Destination{ID: "d1", ConfigRevision: "r1"}, ContentHash: "h1", Operator: "eric"}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate(complete): got %v, want nil", err)
	}
	missingOperator := ok
	missingOperator.Operator = ""
	if err := missingOperator.Validate(); !errors.Is(err, ErrManualVerificationIncomplete) {
		t.Errorf("Validate(no operator): got %v, want ErrManualVerificationIncomplete", err)
	}
	missingDest := ok
	missingDest.Destination = Destination{}
	if err := missingDest.Validate(); !errors.Is(err, ErrDestinationIncomplete) {
		t.Errorf("Validate(no destination): got %v, want ErrDestinationIncomplete", err)
	}
}
