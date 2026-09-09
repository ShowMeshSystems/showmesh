package fpp

import (
	"encoding/json"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fpp.position.duration.seconds is a coordinator-computed fact ("seconds_played"
// plus "seconds_remaining"), not a raw field FPP reports. These tests cover
// positionDurationSignal directly, the same way elapsedms_test.go covers
// elapsedMSSignal: a missing or malformed input must never decode as a
// plausible zero, which is this project's own oldest recurring defect (ma,
// powerBad, warnings).

func TestPositionDurationSignal_PresentValues(t *testing.T) {
	doc := rawDoc{
		"seconds_played":    json.RawMessage(`"120"`),
		"seconds_remaining": json.RawMessage(`"45"`),
	}
	got := positionDurationSignal(doc)
	if got.Absence != "" {
		t.Fatalf("Absence = %q, want none (both inputs present)", got.Absence)
	}
	if got.Value != float64(165) {
		t.Fatalf("Value = %v, want 165", got.Value)
	}
	if got.Unit != "seconds" {
		t.Fatalf("Unit = %q, want %q", got.Unit, "seconds")
	}
}

func TestPositionDurationSignal_MissingPlayedIsUnsupported(t *testing.T) {
	doc := rawDoc{"seconds_remaining": json.RawMessage(`"45"`)}
	got := positionDurationSignal(doc)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("Absence = %q, want %q", got.Absence, observation.StateUnsupported)
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil (a missing input must never decode as a plausible zero)", got.Value)
	}
	if got.Reason == "" || !contains(got.Reason, "seconds_played") {
		t.Errorf("Reason = %q, want it to name seconds_played", got.Reason)
	}
}

func TestPositionDurationSignal_MissingRemainingIsUnsupported(t *testing.T) {
	doc := rawDoc{"seconds_played": json.RawMessage(`"120"`)}
	got := positionDurationSignal(doc)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("Absence = %q, want %q", got.Absence, observation.StateUnsupported)
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil (a missing input must never decode as a plausible zero)", got.Value)
	}
	if got.Reason == "" || !contains(got.Reason, "seconds_remaining") {
		t.Errorf("Reason = %q, want it to name seconds_remaining", got.Reason)
	}
}

func TestPositionDurationSignal_MissingBothIsUnsupported(t *testing.T) {
	doc := rawDoc{}
	got := positionDurationSignal(doc)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("Absence = %q, want %q", got.Absence, observation.StateUnsupported)
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil", got.Value)
	}
	if got.Reason == "" || !contains(got.Reason, "seconds_played") || !contains(got.Reason, "seconds_remaining") {
		t.Errorf("Reason = %q, want it to name both seconds_played and seconds_remaining", got.Reason)
	}
}

// A present-but-malformed input (wrong JSON shape) must degrade the same
// way as a missing one: unsupported, never a fabricated zero. Contract
// section 3.2's "ma" rule applies here too, even though this signal is not
// mode-governed.
func TestPositionDurationSignal_MalformedInputIsUnsupportedNeverZero(t *testing.T) {
	doc := rawDoc{
		"seconds_played":    json.RawMessage(`"not a number"`),
		"seconds_remaining": json.RawMessage(`"45"`),
	}
	got := positionDurationSignal(doc)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("Absence = %q, want %q", got.Absence, observation.StateUnsupported)
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil", got.Value)
	}
}
