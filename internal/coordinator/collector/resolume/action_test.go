package resolume

import (
	"context"
	"testing"
	"time"
)

// This file tests action.go's own declarative and mechanical pieces —
// the registry, the safety-class rule, the post-dispatch fence, and the
// poll loop's deadline handling — independent of a real (or fake) HTTP
// server. action_dispatch_test.go covers the seven actions end to end.

// --- The registry -----------------------------------------------------

func TestActionRegistryHasExactlySevenEntries(t *testing.T) {
	if got, want := len(actionRegistry), 7; got != want {
		t.Fatalf("len(actionRegistry) = %d, want %d — TRACK-D-D3-SPEC.md §2's table has exactly seven actions", got, want)
	}
}

func TestActionRegistryNamesMatchTheSpecTable(t *testing.T) {
	want := map[ActionName]bool{
		ActionLaunchClip: true, ActionClearLayer: true, ActionBlackout: true,
		ActionLaunchColumn: true, ActionSelectDeck: true,
		ActionSetLayerBypass: true, ActionSetLayerMaster: true,
	}
	seen := map[ActionName]bool{}
	for _, e := range actionRegistry {
		if seen[e.Name] {
			t.Errorf("action %q appears more than once in actionRegistry", e.Name)
		}
		seen[e.Name] = true
		if !want[e.Name] {
			t.Errorf("action %q is not one of TRACK-D-D3-SPEC.md §2's seven names", e.Name)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("action %q is missing from actionRegistry", name)
		}
	}
}

// TestEveryActionDeclaresASafetyClass is acceptance criterion 8: the build
// (well, this test) fails the moment a registry entry carries
// [ActionSafetyClassUndeclared], the zero value.
//
// Before trusting this test: temporarily changed the
// [ActionSetLayerBypass] entry in actionRegistry to omit its SafetyClass
// field (leaving it at the zero value) and reran — failed immediately,
// naming setLayerBypass. Restored afterward.
func TestEveryActionDeclaresASafetyClass(t *testing.T) {
	if len(actionRegistry) == 0 {
		t.Fatal("actionRegistry is empty; this test cannot prove anything")
	}
	for _, e := range actionRegistry {
		if e.SafetyClass == ActionSafetyClassUndeclared {
			t.Errorf("action %q has no explicit SafetyClass (ActionSafetyClassUndeclared, the zero value) — "+
				"every action registered in actionRegistry must explicitly set ActionSafetyClassExempt or "+
				"ActionSafetyClassNotExempt", e.Name)
		}
	}
}

// TestActionSafetyClassMembershipIsExactlyBlackoutAndClearLayer pins the
// membership decision (TRACK-D-D3-SPEC.md §5.2) against silent addition or
// removal: setLayerBypass and setLayerMaster are explicitly NOT exempt,
// stated here because they are "the reason this table is written out" —
// exempting them to protect the silencing direction would exempt the
// lighting direction with it (Step 8's own shipped defect).
func TestActionSafetyClassMembershipIsExactlyBlackoutAndClearLayer(t *testing.T) {
	wantExempt := map[ActionName]bool{
		ActionBlackout:   true,
		ActionClearLayer: true,
	}
	for _, e := range actionRegistry {
		got := e.SafetyClass == ActionSafetyClassExempt
		want := wantExempt[e.Name]
		if got != want {
			t.Errorf("action %q: SafetyClass exempt = %v, want %v", e.Name, got, want)
		}
	}
}

func TestEveryActionDeclaresCoordinatorRequiredFallback(t *testing.T) {
	for _, e := range actionRegistry {
		if e.LocalFallbackClass != localFallbackClassCoordinatorRequired {
			t.Errorf("action %q: LocalFallbackClass = %q, want %q", e.Name, e.LocalFallbackClass, localFallbackClassCoordinatorRequired)
		}
	}
}

func TestActionsReturnsASortedCopy(t *testing.T) {
	d := &ActionDispatcher{}
	got := d.Actions()
	if len(got) != len(actionRegistry) {
		t.Fatalf("len(Actions()) = %d, want %d", len(got), len(actionRegistry))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Name >= got[i].Name {
			t.Errorf("Actions() is not sorted: %q >= %q at index %d", got[i-1].Name, got[i].Name, i)
		}
	}
	// Mutating the returned slice must not affect the registry — Actions()
	// promises a copy.
	got[0].Name = "mutated"
	for _, e := range actionRegistry {
		if e.Name == "mutated" {
			t.Fatal("Actions() returned a slice that aliases actionRegistry's own backing array")
		}
	}
}

// --- The post-dispatch fence (§4.1, acceptance criterion 1) --------------

// TestEvidenceIsPostDispatchFence is the direct reproduction, in this
// package's own vocabulary, of Step 7's 179-microsecond defect
// (CLAUDE.md): evidence collected AT OR BEFORE dispatch must never be
// accepted as confirming it, and evidence collected strictly after must be.
//
// Before trusting this test: temporarily changed evidenceIsPostDispatch to
// `return !readAt.Before(dispatchedAt)` (i.e. >=, accepting evidence
// collected in the SAME instant as dispatch) and reran — the "same instant"
// case below flipped from false to true, exactly the shape of Step 7's own
// defect reproduced here. Reverted afterward.
func TestEvidenceIsPostDispatchFence(t *testing.T) {
	dispatchedAt := time.Now()
	tests := []struct {
		name   string
		readAt time.Time
		want   bool
	}{
		{"before dispatch", dispatchedAt.Add(-1 * time.Millisecond), false},
		{"same instant as dispatch", dispatchedAt, false},
		{"179 microseconds after dispatch", dispatchedAt.Add(179 * time.Microsecond), true},
		{"well after dispatch", dispatchedAt.Add(2 * time.Second), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evidenceIsPostDispatch(tt.readAt, dispatchedAt); got != tt.want {
				t.Errorf("evidenceIsPostDispatch(%s, dispatchedAt) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// TestDispatchLaunchClipNeverConfirmsOnAlreadyPresentEvidence is an
// integration-level version of the same fence: a fake Arena that already
// shows the clip connected AND its layer's active_clip correct BEFORE
// dispatch is exactly TestDispatchLaunchClipAlreadyPlayingIsUnconfirmable's
// own scenario in action_dispatch_test.go — this test exists alongside it
// only to name the property explicitly: unconfirmable, never confirmed, is
// the fence operating at the dispatch level, not only the unit level above.
func TestDispatchLaunchClipNeverConfirmsOnAlreadyPresentEvidence(t *testing.T) {
	now := time.Now()
	arena := newFakeArena(&now)
	arena.layers[testLayerOne] = &faLayer{bypassedParamID: 9001, masterParamID: 9002, master: 1, activeClip: idPtr(testClipA)}
	arena.clips[testClipA] = &faClip{connected: "Connected", ownerLayer: testLayerOne}

	d := newTestActionDispatcher(t, arena, &now, identifiedSnapshot(now))
	out, err := d.Dispatch(context.Background(), ActionLaunchClip, ActionParams{ClipID: testClipA})
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if out.State == ActionConfirmed {
		t.Fatalf("State = %q, want anything but confirmed for evidence that already held before dispatch", out.State)
	}
}

// --- pollUntilConfirmedOrDeadline ------------------------------------------

func TestPollUntilConfirmedOrDeadlineConfirmsWhenCheckSucceeds(t *testing.T) {
	now := time.Now()
	d := &ActionDispatcher{now: fixedClock(&now), sleep: fakeSleep(&now), pollInterval: 10 * time.Millisecond}

	calls := 0
	out := d.pollUntilConfirmedOrDeadline(context.Background(), ActionBlackout, now, time.Second, func() (bool, time.Time, string) {
		calls++
		if calls < 3 {
			return false, time.Time{}, "not yet"
		}
		return true, now, "confirmed evidence"
	})
	if out.State != ActionConfirmed {
		t.Fatalf("State = %q, want %q", out.State, ActionConfirmed)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestPollUntilConfirmedOrDeadlineExpiresAsUnconfirmed(t *testing.T) {
	now := time.Now()
	dispatchedAt := now
	d := &ActionDispatcher{now: fixedClock(&now), sleep: fakeSleep(&now), pollInterval: 10 * time.Millisecond}

	out := d.pollUntilConfirmedOrDeadline(context.Background(), ActionLaunchClip, dispatchedAt, 100*time.Millisecond, func() (bool, time.Time, string) {
		return false, time.Time{}, "still not yet"
	})
	if out.State != ActionUnconfirmed {
		t.Fatalf("State = %q, want %q", out.State, ActionUnconfirmed)
	}
	if out.Reason == "" {
		t.Error("Reason is empty")
	}
}

func TestPollUntilConfirmedOrDeadlineRespectsContextCancellation(t *testing.T) {
	now := time.Now()
	d := &ActionDispatcher{now: fixedClock(&now), sleep: fakeSleep(&now), pollInterval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := d.pollUntilConfirmedOrDeadline(ctx, ActionLaunchClip, now, time.Hour, func() (bool, time.Time, string) {
		return false, time.Time{}, "not yet"
	})
	if out.State != ActionUnconfirmed {
		t.Fatalf("State = %q, want %q", out.State, ActionUnconfirmed)
	}
}

// --- identityGateRefusal / deckRefusal (unit level) -----------------------

func TestIdentityGateRefusalAllowsOnlyIdentityTrue(t *testing.T) {
	base := time.Now()
	tests := []struct {
		name   string
		snap   SurveySnapshot
		refuse bool
	}{
		{"no survey ever ran", SurveySnapshot{}, true},
		{"identity true", SurveySnapshot{SurveyRan: true, IdentityKnown: true, Identity: IdentityTrue, IdentityObservedAt: base}, false},
		{"identity false", SurveySnapshot{SurveyRan: true, IdentityKnown: true, Identity: IdentityFalse, IdentityObservedAt: base}, true},
		{"identity unknown", SurveySnapshot{SurveyRan: true, IdentityKnown: true, Identity: IdentityUnknown, IdentityObservedAt: base}, true},
		{"identity deck mismatch", SurveySnapshot{SurveyRan: true, IdentityKnown: true, Identity: IdentityDeckMismatch, IdentityObservedAt: base}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, refuse := identityGateRefusal(tt.snap)
			if refuse != tt.refuse {
				t.Errorf("refuse = %v, want %v", refuse, tt.refuse)
			}
			if refuse && reason == "" {
				t.Error("a refusal must carry a non-empty reason")
			}
			if !refuse && reason != "" {
				t.Errorf("reason = %q, want empty when not refusing", reason)
			}
		})
	}
}

// --- MaxActionConfirmDeadline / clampActionConfirmDeadline (task 2) -------

// TestClampActionConfirmDeadlineNeverExceedsTheMax is the "registry
// maximum" proof this task's own report requires: transition.duration is
// live, operator-set state with no upper bound this package's own registry
// can read in advance, so [MaxActionConfirmDeadline] has to be true by
// CONSTRUCTION (every deadline this package derives from it is clamped)
// rather than true by assertion. An input of one full hour is deliberately
// absurd — nothing in the bench capture ever measured a transition anywhere
// near it — to prove the clamp holds regardless of how large the live value
// gets, not only near the boundary.
//
// Before trusting this test: temporarily changed clampActionConfirmDeadline
// to `return d` (no clamp at all) and reran — this test failed, reporting
// the full one-hour-plus-margin value uncapped. Reverted afterward.
func TestClampActionConfirmDeadlineNeverExceedsTheMax(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"well under the max", 500 * time.Millisecond, 500 * time.Millisecond},
		{"exactly the max", MaxActionConfirmDeadline, MaxActionConfirmDeadline},
		{"just over the max", MaxActionConfirmDeadline + time.Millisecond, MaxActionConfirmDeadline},
		{"absurdly over the max", time.Hour, MaxActionConfirmDeadline},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampActionConfirmDeadline(tt.in); got != tt.want {
				t.Errorf("clampActionConfirmDeadline(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestDeriveClearDeadlineClampsAnArbitrarilyLongTransition proves the clamp
// is actually wired into deriveClearDeadline, not only into the standalone
// helper above: a layer reporting a transition.duration of 3600 seconds —
// the shape a real, operator-misconfigured (or simply very long) Arena
// layer would report — must still produce a deadline no HTTP write-deadline
// caller could ever be surprised by.
func TestDeriveClearDeadlineClampsAnArbitrarilyLongTransition(t *testing.T) {
	l := Layer{Transition: &layerTransition{
		Duration: ParamRangeField{Presence: PresencePresent, Param: &ParamRange{ID: 1, Value: 3600, ValuePresence: PresencePresent}},
	}}
	got := deriveClearDeadline(l)
	if got > MaxActionConfirmDeadline {
		t.Fatalf("deriveClearDeadline(3600s transition) = %s, want at most MaxActionConfirmDeadline (%s)", got, MaxActionConfirmDeadline)
	}
	if got != MaxActionConfirmDeadline {
		t.Errorf("deriveClearDeadline(3600s transition) = %s, want exactly MaxActionConfirmDeadline (%s) since 3600s+margin clearly exceeds it", got, MaxActionConfirmDeadline)
	}
}

func TestDeckRefusalNamesBothDecksAndComparesByID(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	now := time.Now()
	matching := SurveySnapshot{SelectedDeckKnown: true, SelectedDeckID: testDeckOne, SelectedDeckName: "Deck One", SelectedDeckObservedAt: now}
	if reason, refuse := deckRefusal(tc, testDeckOne, matching); refuse {
		t.Errorf("refuse = true (reason %q), want false when the clip's deck matches the selected deck", reason)
	}

	mismatched := SurveySnapshot{SelectedDeckKnown: true, SelectedDeckID: testDeckTwo, SelectedDeckName: "Deck Two", SelectedDeckObservedAt: now}
	reason, refuse := deckRefusal(tc, testDeckOne, mismatched)
	if !refuse {
		t.Fatal("refuse = false, want true for a mismatched deck")
	}
	if !contains(reason, "2000000000001") || !contains(reason, "2000000000002") {
		t.Errorf("reason = %q, want it to name both deck ids", reason)
	}

	unknown := SurveySnapshot{SelectedDeckKnown: false}
	if _, refuse := deckRefusal(tc, testDeckOne, unknown); !refuse {
		t.Error("refuse = false, want true when the selected deck is not known")
	}
}
