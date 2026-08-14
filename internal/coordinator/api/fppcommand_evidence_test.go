package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Finding 10's own regression proof (Step 8 review):
// toInt64's doc comment claimed "never a silent zero standing in for
// 'not a number'", and nothing in the suite tested that claim — mutating
// its default branch to `return 0, true` left the ENTIRE
// internal/coordinator/api suite green. These tests exist so that
// mutation is caught, and so is the narrower defect the same function
// carried: silently truncating a fractional fpp.volume reading (55.9)
// into a false match for a requested 55.

// --- toInt64 itself. ---

func TestToInt64AcceptsInt64Int(t *testing.T) {
	if got, ok := toInt64(int64(42)); !ok || got != 42 {
		t.Errorf("toInt64(int64(42)) = (%d, %v), want (42, true)", got, ok)
	}
	if got, ok := toInt64(int(7)); !ok || got != 7 {
		t.Errorf("toInt64(int(7)) = (%d, %v), want (7, true)", got, ok)
	}
}

func TestToInt64AcceptsWholeFloat(t *testing.T) {
	if got, ok := toInt64(float64(55)); !ok || got != 55 {
		t.Errorf("toInt64(float64(55)) = (%d, %v), want (55, true)", got, ok)
	}
}

// TestToInt64RejectsFractionalFloat is Finding 10's exactness-vs-truncation
// decision made concrete: a fractional float64 (55.9) must return
// ok=false, never a truncated 55. FPP's own Volume Set clamps to a
// whole-number 0-100 (capture section 1.5) and this coordinator's own
// setVolume parameter is validated as an integer before dispatch, so a
// LEGITIMATE fpp.volume reading is always whole; a fractional one cannot
// be silently coerced into a coincidental match.
//
// Verified per this task's standing rule ("break the behavior, confirm
// the test fails, restore"): temporarily changing toInt64's float64 case
// to `return int64(n), true` (unconditional truncation, the pre-fix
// behavior) makes this test fail with got=55, ok=true instead of ok=false
// — confirmed by hand during this task, then reverted.
func TestToInt64RejectsFractionalFloat(t *testing.T) {
	if got, ok := toInt64(float64(55.9)); ok {
		t.Errorf("toInt64(55.9) = (%d, true), want ok=false — a fractional reading must never be silently truncated "+
			"into a coincidental integer match", got)
	}
}

func TestToInt64RejectsNonNumericTypes(t *testing.T) {
	for _, v := range []any{"55", nil, true, []byte("55")} {
		if got, ok := toInt64(v); ok {
			t.Errorf("toInt64(%#v) = (%d, true), want ok=false — never a silent zero standing in for \"not a number\"", v, got)
		}
	}
}

// --- evaluateSetVolumeEvidence: the fractional-value case end to end. ---

// TestEvaluateSetVolumeEvidenceUnconfirmedOnFractionalReading proves the
// exactness decision through the actual predicate, not just toInt64 in
// isolation: an observed fpp.volume of 55.9 must not confirm a request
// for 55 (nor for 56 — truncation and rounding disagree about which
// integer a fraction is "close to", which is exactly why this predicate
// treats a fractional reading as simply not a number it can compare).
func TestEvaluateSetVolumeEvidenceUnconfirmedOnFractionalReading(t *testing.T) {
	lister := &dynamicObservationLister{}
	lister.setObs([]observation.Observation{
		mustObs(observation.Measured(
			observation.ResourceRef{Kind: observation.ResourceFPP, ID: "bench-fpp"},
			observation.SignalID(fppVolumeSignal), float64(55.9), testNow,
			observation.WithValidFor(time.Hour), observation.WithCollectedAt(testNow), observation.WithSource("fpp-rest"),
		)),
	})
	confirmed, _, reason := evaluateSetVolumeEvidence(context.Background(), lister, "bench-fpp", 55, time.Time{}, testNow)
	if confirmed {
		t.Fatalf("confirmed = true, want false — 55.9 must never confirm a request for 55 via truncation")
	}
	if !strings.Contains(reason, "not a whole number") {
		t.Errorf("reason = %q, want it to say the observed value is not a whole number", reason)
	}
}
