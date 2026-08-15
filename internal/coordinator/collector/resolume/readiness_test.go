package resolume

import (
	"reflect"
	"testing"
	"time"
)

// allTrueInputs is every one of LayerReady's seven terms Known and
// HeldTrue, at t — the baseline every test below mutates one term of.
func allTrueInputs(t time.Time) ReadinessInputs {
	known := ReadinessTermInput{Known: true, HeldTrue: true, ObservedAt: t}
	return ReadinessInputs{
		LayerBypassed:       known,
		LayerMaster:         known,
		LayerVideoOpacity:   known,
		GroupBypassed:       known,
		GroupMaster:         known,
		CompositionBypassed: known,
		CompositionMaster:   known,
	}
}

func TestLayerReadyAllTermsTrueIsReady(t *testing.T) {
	now := time.Now()
	r := LayerReady(allTrueInputs(now))
	if r.State != ReadinessReady {
		t.Fatalf("State = %q, want %q", r.State, ReadinessReady)
	}
	if len(r.FailingTerms) != 0 || len(r.UnknownTerms) != 0 {
		t.Errorf("Ready result carries FailingTerms=%v UnknownTerms=%v, want both empty", r.FailingTerms, r.UnknownTerms)
	}
	if r.ObservedAt != now {
		t.Errorf("ObservedAt = %v, want %v (oldest of seven identical times)", r.ObservedAt, now)
	}
}

// TestLayerReadyBypassedLayerIsNotReadyDespiteEverythingElseTrue is
// TRACK-D-ADAPTER-SPEC.md §3.7's own named acceptance case: "a clip on a
// bypassed layer ... reports Connected with active_clip present" — the
// naive check a bare `connected == "Connected"` predicate would pass. The
// conjunction must still say not_ready.
func TestLayerReadyBypassedLayerIsNotReadyDespiteEverythingElseTrue(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerBypassed = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}

	r := LayerReady(in)
	if r.State != ReadinessNotReady {
		t.Fatalf("State = %q, want %q", r.State, ReadinessNotReady)
	}
	if !reflect.DeepEqual(r.FailingTerms, []ReadinessTerm{ReadinessTermLayerBypassed}) {
		t.Errorf("FailingTerms = %v, want exactly [%q]", r.FailingTerms, ReadinessTermLayerBypassed)
	}
}

// TestLayerReadyMasterZeroIsNotReadyDespiteEverythingElseTrue is
// acceptance criterion 2's second named case: "a layer at zero master."
func TestLayerReadyMasterZeroIsNotReadyDespiteEverythingElseTrue(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerMaster = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}

	r := LayerReady(in)
	if r.State != ReadinessNotReady {
		t.Fatalf("State = %q, want %q", r.State, ReadinessNotReady)
	}
	if !reflect.DeepEqual(r.FailingTerms, []ReadinessTerm{ReadinessTermLayerMaster}) {
		t.Errorf("FailingTerms = %v, want exactly [%q]", r.FailingTerms, ReadinessTermLayerMaster)
	}
}

// TestLayerReadyMultipleFailingTermsAreAllNamed proves FailingTerms is not
// truncated to the first failure: an operator fixing one problem must see
// that a second one remains, not discover it on the next survey.
func TestLayerReadyMultipleFailingTermsAreAllNamed(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerBypassed = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}
	in.GroupMaster = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}

	r := LayerReady(in)
	want := []ReadinessTerm{ReadinessTermLayerBypassed, ReadinessTermGroupMaster}
	if !reflect.DeepEqual(r.FailingTerms, want) {
		t.Errorf("FailingTerms = %v, want %v (readinessTermOrder order)", r.FailingTerms, want)
	}
}

// TestLayerReadyKnownFalseBeatsUnknown is this file's own proof of Kleene
// AND over "not_ready" vs "unknown": a term this seam can never read
// (composition-level, ladder off) must NEVER demote a definite not-ready
// verdict to merely unknown, and must never be silently treated as
// satisfied either. TRACK-D-D2-SPEC.md §4: "rung 2 must not report ready"
// — the sharper, unstated corollary is that an unread term must also never
// hide a definite failure.
func TestLayerReadyKnownFalseBeatsUnknown(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerBypassed = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}
	in.CompositionBypassed = ReadinessTermInput{UnknownReason: "ladder disabled"}
	in.CompositionMaster = ReadinessTermInput{UnknownReason: "ladder disabled"}

	r := LayerReady(in)
	if r.State != ReadinessNotReady {
		t.Fatalf("State = %q, want %q (a known-false term must win over unknown terms elsewhere)", r.State, ReadinessNotReady)
	}
	if !reflect.DeepEqual(r.FailingTerms, []ReadinessTerm{ReadinessTermLayerBypassed}) {
		t.Errorf("FailingTerms = %v, want exactly [%q]", r.FailingTerms, ReadinessTermLayerBypassed)
	}
}

// TestLayerReadyUnknownTermNeverSatisfiesOrFails is acceptance criterion
// 3, applied per conjunction term rather than only against the two leaves
// D-1 already handled: a Known=false term must produce ReadinessUnknown
// naming it — NEVER ReadinessReady (silently satisfied) and NEVER
// ReadinessNotReady (silently treated as failing) — for every one of the
// seven terms in turn.
//
// Before trusting this test: LayerReady's own Known-check was temporarily
// changed from `t.input.Known && !t.input.HeldTrue` to just
// `!t.input.HeldTrue` in the not-ready branch (which makes a Known=false,
// HeldTrue=false zero-valued ReadinessTermInput read as a definite
// failure — exactly the "bypassed: null decoding to false reads as ready"
// defect class, mirrored onto readiness). Every subtest below failed as
// expected (each reported ReadinessNotReady instead of ReadinessUnknown),
// confirming this test actually exercises the null-vs-known distinction
// rather than passing regardless of it. Reverted afterward.
func TestLayerReadyUnknownTermNeverSatisfiesOrFails(t *testing.T) {
	now := time.Now()
	unknown := ReadinessTermInput{UnknownReason: "explicit null in Resolume's response"}

	cases := []struct {
		name string
		set  func(*ReadinessInputs)
		term ReadinessTerm
	}{
		{"LayerBypassed", func(in *ReadinessInputs) { in.LayerBypassed = unknown }, ReadinessTermLayerBypassed},
		{"LayerMaster", func(in *ReadinessInputs) { in.LayerMaster = unknown }, ReadinessTermLayerMaster},
		{"LayerVideoOpacity", func(in *ReadinessInputs) { in.LayerVideoOpacity = unknown }, ReadinessTermLayerVideoOpacity},
		{"GroupBypassed", func(in *ReadinessInputs) { in.GroupBypassed = unknown }, ReadinessTermGroupBypassed},
		{"GroupMaster", func(in *ReadinessInputs) { in.GroupMaster = unknown }, ReadinessTermGroupMaster},
		{"CompositionBypassed", func(in *ReadinessInputs) { in.CompositionBypassed = unknown }, ReadinessTermCompositionBypassed},
		{"CompositionMaster", func(in *ReadinessInputs) { in.CompositionMaster = unknown }, ReadinessTermCompositionMaster},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := allTrueInputs(now)
			tc.set(&in)

			r := LayerReady(in)
			if r.State != ReadinessUnknown {
				t.Fatalf("State = %q, want %q for a null/absent %s", r.State, ReadinessUnknown, tc.name)
			}
			if !reflect.DeepEqual(r.UnknownTerms, []ReadinessTerm{tc.term}) {
				t.Errorf("UnknownTerms = %v, want exactly [%q]", r.UnknownTerms, tc.term)
			}
			if len(r.UnknownReasons) != 1 || r.UnknownReasons[0] == "" {
				t.Errorf("UnknownReasons = %v, want exactly one non-empty reason", r.UnknownReasons)
			}
			if !r.ObservedAt.IsZero() {
				t.Errorf("ObservedAt = %v, want the zero time for an unknown verdict", r.ObservedAt)
			}
		})
	}
}

func TestLayerReadyMultipleUnknownTermsAllNamedInOrder(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.CompositionBypassed = ReadinessTermInput{UnknownReason: "ladder disabled"}
	in.CompositionMaster = ReadinessTermInput{UnknownReason: "ladder disabled"}
	in.LayerVideoOpacity = ReadinessTermInput{UnknownReason: "video was null"}

	r := LayerReady(in)
	if r.State != ReadinessUnknown {
		t.Fatalf("State = %q, want %q", r.State, ReadinessUnknown)
	}
	want := []ReadinessTerm{ReadinessTermLayerVideoOpacity, ReadinessTermCompositionBypassed, ReadinessTermCompositionMaster}
	if !reflect.DeepEqual(r.UnknownTerms, want) {
		t.Errorf("UnknownTerms = %v, want %v (readinessTermOrder order)", r.UnknownTerms, want)
	}
}

func TestLayerReadyObservedAtIsOldestFailingTermOnly(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	recent := time.Now()

	in := allTrueInputs(recent)
	// Two failing terms at different times; a third, unrelated term is
	// even older but Known=true/HeldTrue=true and must not affect
	// ObservedAt at all — only terms that actually determined the
	// not-ready verdict may.
	in.LayerBypassed = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: old}
	in.GroupMaster = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: recent}
	in.LayerMaster = ReadinessTermInput{Known: true, HeldTrue: true, ObservedAt: old.Add(-time.Hour)}

	r := LayerReady(in)
	if r.State != ReadinessNotReady {
		t.Fatalf("State = %q, want %q", r.State, ReadinessNotReady)
	}
	if r.ObservedAt != old {
		t.Errorf("ObservedAt = %v, want %v (the older of the two FAILING terms, ignoring the older passing term)", r.ObservedAt, old)
	}
}

// --- boolTermHoldsWhenFalse / rangeTermHoldsWhenPositive --------------------

func TestBoolTermHoldsWhenFalseThreeWayPresence(t *testing.T) {
	now := time.Now()

	present := ParamBooleanField{Presence: PresencePresent, Param: &ParamBoolean{Value: false, ValuePresence: PresencePresent}}
	in := boolTermHoldsWhenFalse(present, now, "layer.bypassed")
	if !in.Known || !in.HeldTrue {
		t.Errorf("present/false = %+v, want Known=true HeldTrue=true (bypassed==false holds)", in)
	}

	presentTrue := ParamBooleanField{Presence: PresencePresent, Param: &ParamBoolean{Value: true, ValuePresence: PresencePresent}}
	in = boolTermHoldsWhenFalse(presentTrue, now, "layer.bypassed")
	if !in.Known || in.HeldTrue {
		t.Errorf("present/true = %+v, want Known=true HeldTrue=false (bypassed==true fails)", in)
	}

	null := ParamBooleanField{Presence: PresenceNull}
	in = boolTermHoldsWhenFalse(null, now, "layer.bypassed")
	if in.Known {
		t.Errorf("null = %+v, want Known=false — a null bypassed must never read as HeldTrue", in)
	}
	if in.UnknownReason == "" {
		t.Errorf("null UnknownReason is empty, want a reason")
	}

	absent := ParamBooleanField{Presence: PresenceAbsent}
	in = boolTermHoldsWhenFalse(absent, now, "layer.bypassed")
	if in.Known {
		t.Errorf("absent = %+v, want Known=false", in)
	}

	// Defect 1 (2026-08-15): the envelope itself is present, but its own
	// "value" key is not — capture §17.3's own headline finding, since no
	// schema in Arena's specification carries a `required` list. Must read
	// EXACTLY like null/absent: Known=false, never HeldTrue=true off the
	// bare Go zero value `false`.
	presentNoValue := ParamBooleanField{Presence: PresencePresent, Param: &ParamBoolean{ValuePresence: PresenceAbsent}}
	in = boolTermHoldsWhenFalse(presentNoValue, now, "layer.bypassed")
	if in.Known {
		t.Errorf("present envelope with no value = %+v, want Known=false — this is the darkening-direction false confirmation defect", in)
	}
	if in.UnknownReason == "" {
		t.Errorf("present envelope with no value: UnknownReason is empty, want a reason")
	}
}

func TestRangeTermHoldsWhenPositiveThreeWayPresence(t *testing.T) {
	now := time.Now()

	present := ParamRangeField{Presence: PresencePresent, Param: &ParamRange{Value: 0.5, ValuePresence: PresencePresent}}
	in := rangeTermHoldsWhenPositive(present, now, "layer.master")
	if !in.Known || !in.HeldTrue {
		t.Errorf("present/0.5 = %+v, want Known=true HeldTrue=true", in)
	}

	zero := ParamRangeField{Presence: PresencePresent, Param: &ParamRange{Value: 0, ValuePresence: PresencePresent}}
	in = rangeTermHoldsWhenPositive(zero, now, "layer.master")
	if !in.Known || in.HeldTrue {
		t.Errorf("present/0 = %+v, want Known=true HeldTrue=false", in)
	}

	null := ParamRangeField{Presence: PresenceNull}
	in = rangeTermHoldsWhenPositive(null, now, "layer.master")
	if in.Known {
		t.Errorf("null = %+v, want Known=false", in)
	}

	// Defect 1 (2026-08-15): envelope present, "value" key absent — the
	// setLayerMaster darkening-direction case CLAUDE.md names by name (a
	// value-less "master" envelope's Go zero value is 0.0).
	presentNoValue := ParamRangeField{Presence: PresencePresent, Param: &ParamRange{ValuePresence: PresenceAbsent}}
	in = rangeTermHoldsWhenPositive(presentNoValue, now, "layer.master")
	if in.Known {
		t.Errorf("present envelope with no value = %+v, want Known=false", in)
	}
	if in.UnknownReason == "" {
		t.Errorf("present envelope with no value: UnknownReason is empty, want a reason")
	}
}

// --- Defect 5 (2026-08-15): solo, applied as an override on top of
// LayerReady's own verdict, never folded into the seven-term conjunction ---

// TestApplySoloOverrideNoOpWhenSoloInactive proves the ordinary case —
// nothing soloed anywhere in the composition — leaves LayerReady's own
// verdict completely untouched, for all three states.
func TestApplySoloOverrideNoOpWhenSoloInactive(t *testing.T) {
	now := time.Now()
	ready := LayerReady(allTrueInputs(now))
	if got := ApplySoloOverride(ready, false, false); !reflect.DeepEqual(got, ready) {
		t.Errorf("ApplySoloOverride(ready, soloActiveElsewhere=false, _) = %+v, want the verdict unchanged: %+v", got, ready)
	}
}

// TestApplySoloOverrideExemptsTheSoloedLayerItself proves the other no-op
// case: solo IS active somewhere, but the layer this verdict belongs to is
// the soloed one (or in the soloed group) — its own verdict must not be
// downgraded.
func TestApplySoloOverrideExemptsTheSoloedLayerItself(t *testing.T) {
	now := time.Now()
	ready := LayerReady(allTrueInputs(now))
	if got := ApplySoloOverride(ready, true, true); !reflect.DeepEqual(got, ready) {
		t.Errorf("ApplySoloOverride(ready, soloActiveElsewhere=true, thisLayerSoloed=true) = %+v, want the verdict unchanged: %+v", got, ready)
	}
}

// TestApplySoloOverrideDowngradesReadyToUnknown is defect 5's own headline
// case: solo is active on ANOTHER layer, and this layer would otherwise
// report ready — that must become unknown, naming solo, never ready
// (which would claim a safety this seam has no evidence for) and never
// not_ready (which would claim a specific fault that is not what happened).
func TestApplySoloOverrideDowngradesReadyToUnknown(t *testing.T) {
	now := time.Now()
	ready := LayerReady(allTrueInputs(now))
	if ready.State != ReadinessReady {
		t.Fatalf("test setup: LayerReady(allTrueInputs) = %q, want %q", ready.State, ReadinessReady)
	}

	got := ApplySoloOverride(ready, true, false)
	if got.State != ReadinessUnknown {
		t.Fatalf("State = %q, want %q", got.State, ReadinessUnknown)
	}
	if !reflect.DeepEqual(got.UnknownTerms, []ReadinessTerm{ReadinessTermSolo}) {
		t.Errorf("UnknownTerms = %v, want exactly [%q]", got.UnknownTerms, ReadinessTermSolo)
	}
	if len(got.UnknownReasons) != 1 || got.UnknownReasons[0] == "" {
		t.Errorf("UnknownReasons = %v, want exactly one non-empty reason", got.UnknownReasons)
	}
}

// TestApplySoloOverrideDowngradesNotReadyToUnknown proves the override is
// UNCONDITIONAL, per this task's own instruction: even a definite,
// known-false not_ready verdict is withdrawn to unknown while solo is
// active elsewhere on this layer — never left standing as if solo made no
// difference to what this seam can vouch for.
func TestApplySoloOverrideDowngradesNotReadyToUnknown(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerBypassed = ReadinessTermInput{Known: true, HeldTrue: false, ObservedAt: now}
	notReady := LayerReady(in)
	if notReady.State != ReadinessNotReady {
		t.Fatalf("test setup: LayerReady = %q, want %q", notReady.State, ReadinessNotReady)
	}

	got := ApplySoloOverride(notReady, true, false)
	if got.State != ReadinessUnknown {
		t.Fatalf("State = %q, want %q — solo must override even a definite not_ready", got.State, ReadinessUnknown)
	}
	if len(got.FailingTerms) != 0 {
		t.Errorf("FailingTerms = %v, want empty — a downgraded verdict is Unknown, not NotReady-with-extra-terms", got.FailingTerms)
	}
}

// TestApplySoloOverridePreservesExistingUnknownReasons proves an
// already-unknown verdict keeps its own terms and reasons when solo also
// applies — solo is APPENDED, never discarded in favor of a single generic
// reason that would erase a real, separately-diagnosable problem (e.g. the
// composition-level terms being permanently unavailable).
func TestApplySoloOverridePreservesExistingUnknownReasons(t *testing.T) {
	now := time.Now()
	in := allTrueInputs(now)
	in.LayerVideoOpacity = ReadinessTermInput{UnknownReason: "video was null"}
	unknown := LayerReady(in)
	if unknown.State != ReadinessUnknown {
		t.Fatalf("test setup: LayerReady = %q, want %q", unknown.State, ReadinessUnknown)
	}

	got := ApplySoloOverride(unknown, true, false)
	if got.State != ReadinessUnknown {
		t.Fatalf("State = %q, want %q", got.State, ReadinessUnknown)
	}
	want := []ReadinessTerm{ReadinessTermLayerVideoOpacity, ReadinessTermSolo}
	if !reflect.DeepEqual(got.UnknownTerms, want) {
		t.Errorf("UnknownTerms = %v, want %v (existing reason preserved, solo appended)", got.UnknownTerms, want)
	}
	if len(got.UnknownReasons) != 2 {
		t.Errorf("UnknownReasons = %v, want exactly 2", got.UnknownReasons)
	}
}
