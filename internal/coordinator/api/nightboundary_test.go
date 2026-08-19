package api

import (
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Track F seam F3 tests: docs/private/seam-specs/TRACK-F-F3-fpp-timeline.md
// rules 1-7. Each test names which rule it defends in its own comment.

// Rule 2: "Every anchor carries the observation time it came from, and
// evidence that pre-dates the dispatch can never anchor it." Verified to
// fail when reverted: removing deriveNightBoundary's ObservedAt.Before
// check makes this test pass with a boundary armed from stale evidence.
func TestDeriveNightBoundary_Rule2_EvidencePredatingDispatchIsInvalid(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	stale := dispatched.Add(-1 * time.Second)
	a := nightContentAnchor{DispatchedAt: dispatched, ObservedAt: stale, DurationMS: 60000}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateInvalid {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateInvalid)
	}
}

// Rule 7: "Missing evidence is unknown. Not started, not failed, not
// probably fine." No ObservedAt at all (never dispatched, or dispatched
// with no evidence yet) must report unknown, not invalid and not a
// boundary at "now".
func TestDeriveNightBoundary_Rule7_NoEvidenceIsUnknown(t *testing.T) {
	a := nightContentAnchor{DurationMS: 60000}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateUnknown {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateUnknown)
	}
	if got.ExpectedAt != nil {
		t.Fatalf("ExpectedAt = %v, want nil", got.ExpectedAt)
	}
}

// Rule 1/F0 §1: a zero duration is a readiness failure, "never a boundary
// at time zero and never a guess" — deriveNightBoundary must refuse to
// arm even given otherwise-valid post-dispatch evidence.
func TestDeriveNightBoundary_Rule1_ZeroDurationIsInvalid(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	a := nightContentAnchor{DispatchedAt: dispatched, ObservedAt: dispatched.Add(time.Second), DurationMS: 0}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateInvalid {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateInvalid)
	}
}

// Ordinary arithmetic: E = ObservedAt + (duration - position). F0 §1's own
// worked example (300s FSEQ) is used directly.
func TestDeriveNightBoundary_ArmsExpectedTime(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	observed := dispatched.Add(2 * time.Second)
	a := nightContentAnchor{DispatchedAt: dispatched, ObservedAt: observed, DurationMS: 300000, PositionSeconds: 2}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateArmed {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateArmed)
	}
	want := observed.Add(298 * time.Second)
	if got.ExpectedAt == nil || !got.ExpectedAt.Equal(want) {
		t.Fatalf("ExpectedAt = %v, want %v", got.ExpectedAt, want)
	}
}

// Rule 1: position already past the resolved duration is contradictory
// evidence, not a negative-remaining boundary.
func TestDeriveNightBoundary_PositionPastDurationIsInvalid(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	a := nightContentAnchor{DispatchedAt: dispatched, ObservedAt: dispatched.Add(time.Second), DurationMS: 5000, PositionSeconds: 10}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateInvalid {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateInvalid)
	}
}

func oneShotAnchor() nightContentAnchor {
	return nightContentAnchor{
		Purpose: nightAnchorPurposeRestingOneShot, FPPInstanceID: "fpp-1",
		Playlist: "trackf-resting", Item: "trackf-resting.fseq",
		DurationMS: 60000, PositionSeconds: 5,
		DispatchedAt: time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC),
		ObservedAt:   time.Date(2026, 10, 31, 20, 0, 1, 0, time.UTC),
	}
}

// Rule 3: "A restart, seek, pause, ... item mismatch ... moves or
// invalidates the boundary." Item mismatch case.
func TestNightBoundaryContradicted_Rule3_ItemMismatch(t *testing.T) {
	anchor := oneShotAnchor()
	obs := nightPlaybackObservation{Current: true, Status: fppStatusValuePlaying, Playlist: anchor.Playlist, Item: "some-other.fseq"}
	bad, reason := nightBoundaryContradicted(anchor, obs)
	if !bad {
		t.Fatal("expected contradiction on item mismatch")
	}
	if reason == "" {
		t.Fatal("expected a non-empty reason")
	}
}

// Rule 3: pause is explicitly named and F0 records it as bench-reachable
// but not yet exercised — this coordinator must invalidate rather than
// assume the deadline arithmetic still holds while paused.
func TestNightBoundaryContradicted_Rule3_Pause(t *testing.T) {
	anchor := oneShotAnchor()
	obs := nightPlaybackObservation{Current: true, Status: fppStatusValuePaused, Playlist: anchor.Playlist, Item: anchor.Item}
	bad, _ := nightBoundaryContradicted(anchor, obs)
	if !bad {
		t.Fatal("expected contradiction on pause")
	}
}

// Rule 3 / F0 §5: "for a one-shot item, decreasing elapsed time between
// two polls is the only signal a loop restarted." Position moving
// backward must invalidate a one-shot boundary.
func TestNightBoundaryContradicted_Rule3_PositionMovedBackward(t *testing.T) {
	anchor := oneShotAnchor()
	obs := nightPlaybackObservation{
		Current: true, Status: fppStatusValuePlaying, Playlist: anchor.Playlist, Item: anchor.Item,
		PositionSeconds: anchor.PositionSeconds - 1, PositionCurrent: true,
	}
	bad, _ := nightBoundaryContradicted(anchor, obs)
	if !bad {
		t.Fatal("expected contradiction when position moves backward")
	}
}

// Ordinary agreement: matching item/playlist, still playing, position
// advancing must NOT be reported as a contradiction.
func TestNightBoundaryContradicted_AgreeingEvidenceIsNotContradicted(t *testing.T) {
	anchor := oneShotAnchor()
	obs := nightPlaybackObservation{
		Current: true, Status: fppStatusValuePlaying, Playlist: anchor.Playlist, Item: anchor.Item,
		PositionSeconds: anchor.PositionSeconds + 1, PositionCurrent: true,
	}
	if bad, reason := nightBoundaryContradicted(anchor, obs); bad {
		t.Fatalf("expected no contradiction, got one: %s", reason)
	}
}

// Rule 7: missing evidence (obs.Current == false) is unknown, not a
// contradiction — nightBoundaryContradicted must not manufacture a
// negative finding out of absence.
func TestNightBoundaryContradicted_MissingEvidenceIsNotAContradiction(t *testing.T) {
	anchor := oneShotAnchor()
	obs := nightPlaybackObservation{Current: false}
	if bad, reason := nightBoundaryContradicted(anchor, obs); bad {
		t.Fatalf("expected no contradiction from missing evidence, got one: %s", reason)
	}
}

// Encode/decode round trip: BoundaryJSON/ContentAnchorJSON must survive a
// store round trip losslessly for every field the loop reads back.
func TestNightContentAnchorEncodeDecodeRoundTrip(t *testing.T) {
	a := oneShotAnchor()
	raw := encodeNightContentAnchor(a)
	got, ok := decodeNightContentAnchor(raw)
	if !ok {
		t.Fatal("decode reported not ok")
	}
	if got != a {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, a)
	}
}

func TestDecodeNightContentAnchor_EmptyIsAbsent(t *testing.T) {
	if _, ok := decodeNightContentAnchor(""); ok {
		t.Fatal("expected ok=false for an empty string")
	}
}

// Boundary derivation prefers
// millisecond-precision position when the anchoring observation had it,
// and states which source it used.
func TestDeriveNightBoundary_PrefersMillisecondPositionWhenKnown(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	observed := dispatched.Add(2 * time.Second)
	a := nightContentAnchor{
		DispatchedAt: dispatched, ObservedAt: observed, DurationMS: 300000,
		PositionSeconds: 2, PositionMS: 2100, PositionMSKnown: true,
	}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateArmed {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateArmed)
	}
	want := observed.Add(297900 * time.Millisecond)
	if got.ExpectedAt == nil || !got.ExpectedAt.Equal(want) {
		t.Fatalf("ExpectedAt = %v, want %v (ms-precision remainder, not the whole-second one)", got.ExpectedAt, want)
	}
	if got.Reason == "" {
		t.Fatal("expected a stated reason naming the millisecond source")
	}
}

// The fallback case: no ms known, whole-second arithmetic used, and the
// fallback is stated rather than silent.
func TestDeriveNightBoundary_FallsBackToWholeSecondWithStatedReason(t *testing.T) {
	dispatched := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	observed := dispatched.Add(2 * time.Second)
	a := nightContentAnchor{DispatchedAt: dispatched, ObservedAt: observed, DurationMS: 300000, PositionSeconds: 2}
	got := deriveNightBoundary(a)
	if got.State != nightBoundaryStateArmed {
		t.Fatalf("state = %q, want %q", got.State, nightBoundaryStateArmed)
	}
	want := observed.Add(298 * time.Second)
	if got.ExpectedAt == nil || !got.ExpectedAt.Equal(want) {
		t.Fatalf("ExpectedAt = %v, want %v", got.ExpectedAt, want)
	}
	if got.Reason == "" {
		t.Fatal("expected a stated fallback reason")
	}
}

// Rule 3 extended: a millisecond-precision position moving backward
// invalidates just as the whole-second one does.
func TestNightBoundaryContradicted_PositionMSMovedBackward(t *testing.T) {
	anchor := oneShotAnchor()
	anchor.PositionMS, anchor.PositionMSKnown = 5000, true
	obs := nightPlaybackObservation{
		Current: true, Status: fppStatusValuePlaying, Playlist: anchor.Playlist, Item: anchor.Item,
		PositionMS: 4900, PositionMSCurrent: true,
	}
	bad, _ := nightBoundaryContradicted(anchor, obs)
	if !bad {
		t.Fatal("expected contradiction when millisecond position moves backward")
	}
}

// TestMapNightTransition_HeldReasonReachesTheSurface: a held-transition
// reason in BoundaryJSON.Reason must reach the wire even with no content
// anchor recorded (transition-to-show on the first show of the night).
func TestMapNightTransition_HeldReasonReachesTheSurface(t *testing.T) {
	const blockedReason = `barrier cue "lighting-fade" is dispatched, not resolved`
	expected := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		ContentAnchorJSON: "", // cleared on entering transition-to-show
		BoundaryJSON:      encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &expected, Reason: blockedReason}),
	}
	got := mapNightTransition(rec)
	if got.Reason != blockedReason {
		t.Fatalf("Reason = %q, want the held-transition reason %q", got.Reason, blockedReason)
	}
	if got.State != v1.NightEvidenceRecorded {
		t.Fatalf("State = %q, want %q (a boundary is armed, evidence exists)", got.State, v1.NightEvidenceRecorded)
	}
}

// TestMapNightTransition_HeldReasonReachesTheSurfaceWithAnchorPresent: the
// same held-transition reason must also surface on the second night's
// cycle onward, where nightAdvanceRestingIntershow deliberately keeps the
// content anchor through the lead window (F4 review) and hasAnchor is
// true. The armed-boundary case must not discard boundary.Reason for its
// own "boundary armed for <time>" text.
func TestMapNightTransition_HeldReasonReachesTheSurfaceWithAnchorPresent(t *testing.T) {
	const blockedReason = `barrier cue "lighting-fade" is dispatched, not resolved`
	expected := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	observed := expected.Add(-time.Minute)
	rec := store.NightSessionRecord{
		ContentAnchorJSON: encodeNightContentAnchor(nightContentAnchor{
			Purpose: nightAnchorPurposeRestingOneShot, DispatchedAt: observed.Add(-time.Second), ObservedAt: observed,
		}),
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &expected, Reason: blockedReason}),
	}
	got := mapNightTransition(rec)
	if got.Reason != blockedReason {
		t.Fatalf("Reason = %q, want the held-transition reason %q", got.Reason, blockedReason)
	}
}

// TestMapNightTransition_NoAnchorNoBoundaryStillUnknown: the fix must not
// remove the original fallback for the case that motivated it (no
// content anchor recorded at all, e.g. before start-night).
func TestMapNightTransition_NoAnchorNoBoundaryStillUnknown(t *testing.T) {
	got := mapNightTransition(store.NightSessionRecord{})
	if got.State != v1.NightEvidenceUnknown {
		t.Fatalf("State = %q, want %q", got.State, v1.NightEvidenceUnknown)
	}
	if got.Reason != "no content anchor is armed for the current state" {
		t.Fatalf("Reason = %q, want the original fallback text", got.Reason)
	}
}

// TestMapNightTransition_BlankBoundaryReasonNeverRendersBlank: no writer
// leaves nightBoundary.Reason empty today, but a blank Reason must not
// reach the wire as an empty string, which renders as fine.
func TestMapNightTransition_BlankBoundaryReasonNeverRendersBlank(t *testing.T) {
	expected := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	rec := store.NightSessionRecord{
		BoundaryJSON: encodeNightBoundary(nightBoundary{State: nightBoundaryStateArmed, ExpectedAt: &expected}),
	}
	got := mapNightTransition(rec)
	if got.Reason == "" {
		t.Fatal("Reason is empty; a blank boundary reason must fall back to a stated reason")
	}
}
