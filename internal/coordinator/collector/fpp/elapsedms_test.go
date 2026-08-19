package fpp

import (
	"encoding/json"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Track F seam F3: fpp.position.elapsed.ms (SignalPositionElapsedMS),
// reserved identifier docs/build/IDENTIFIER-REGISTER.md, on main at
// 3538439. F0's own finding: "milliseconds_elapsed" advances in exact 50ms
// quanta and is present only during playing/stopping states — absent, not
// present-as-zero, otherwise. This project's own oldest recurring defect
// is an absent optional field decoding as a plausible zero (ma, powerBad);
// these tests prove that has not happened again here.

func TestElapsedMSSignal_PresentValue(t *testing.T) {
	doc := rawDoc{"milliseconds_elapsed": json.RawMessage(`24496`)}
	got := elapsedMSSignal(doc)
	if got.Absence != "" {
		t.Fatalf("Absence = %q, want none (value present)", got.Absence)
	}
	if got.Value != int64(24496) {
		t.Fatalf("Value = %v (%T), want int64(24496)", got.Value, got.Value)
	}
	if got.Unit != "ms" {
		t.Fatalf("Unit = %q, want %q", got.Unit, "ms")
	}
}

// The absent-key case (idle within player mode, per F0 §3's own
// "90-idle-status-full.json" capture) must never decode as 0.
func TestElapsedMSSignal_AbsentKeyIsUnsupportedNeverZero(t *testing.T) {
	doc := rawDoc{"status_name": json.RawMessage(`"idle"`)}
	got := elapsedMSSignal(doc)
	if got.Absence != observation.StateUnsupported {
		t.Fatalf("Absence = %q, want %q", got.Absence, observation.StateUnsupported)
	}
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil (an absent field must never decode as a plausible zero)", got.Value)
	}
	if got.Reason == "" {
		t.Fatal("Reason is empty, want an explanation")
	}
}

// A JSON null is not an absent key — this project's own recurring lesson
// (CLAUDE.md), tested here directly for this signal.
func TestElapsedMSSignal_ExplicitNullIsNotAbsence(t *testing.T) {
	doc := rawDoc{"milliseconds_elapsed": json.RawMessage(`null`)}
	got := elapsedMSSignal(doc)
	if got.Value != nil {
		t.Fatalf("Value = %v, want nil", got.Value)
	}
	if got.Absence == observation.StateUnsupported {
		t.Fatalf("Absence = %q (the absent-key reason), want collection_failed: null is a decode failure, not a missing key", got.Absence)
	}
}
