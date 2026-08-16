package resolume

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam B's own test suite for references.go: the
// label vocabulary (ADR-037 decision 4) and the resolve functions (decisions
// 3, 5, 6) against [TrackedComposition] fixtures. parseTestComposition
// (idmap_test.go) supplies the real, named fixture (pkg/resolumecomp's
// testdata/complete.avc); a handful of tests below build a small literal
// resolumecomp.Composition instead, when the scenario (an ambiguous name, a
// persistent clip disambiguated by layer) is not already present in that
// fixture.

// --- Label functions ---------------------------------------------------

func TestLayerLabelAuthoredVsGenerated(t *testing.T) {
	if label, generated := LayerLabel(0, "Peak Only"); label != "Peak Only" || generated {
		t.Errorf("LayerLabel(0, %q) = (%q, %v), want (%q, false)", "Peak Only", label, generated, "Peak Only")
	}
	if label, generated := LayerLabel(4, ""); label != "Layer 5" || !generated {
		t.Errorf("LayerLabel(4, \"\") = (%q, %v), want (%q, true)", label, generated, "Layer 5")
	}
}

func TestColumnLabelIsAlwaysGenerated(t *testing.T) {
	if got := ColumnLabel(0); got != "Column 1" {
		t.Errorf("ColumnLabel(0) = %q, want %q", got, "Column 1")
	}
	if got := ColumnLabel(3); got != "Column 4" {
		t.Errorf("ColumnLabel(3) = %q, want %q", got, "Column 4")
	}
}

func TestDeckLabelAuthoredVsGenerated(t *testing.T) {
	if label, generated := DeckLabel(1, "Main"); label != "Main" || generated {
		t.Errorf("DeckLabel(1, %q) = (%q, %v), want (%q, false)", "Main", label, generated, "Main")
	}
	if label, generated := DeckLabel(2, ""); label != "Deck 2" || !generated {
		t.Errorf("DeckLabel(2, \"\") = (%q, %v), want (%q, true)", label, generated, "Deck 2")
	}
}

func TestClipLabelAuthoredVsGenerated(t *testing.T) {
	if label, generated := ClipLabel(0, 0, "Snowflakes"); label != "Snowflakes" || generated {
		t.Errorf("ClipLabel(0,0,%q) = (%q, %v), want (%q, false)", "Snowflakes", label, generated, "Snowflakes")
	}
	if label, generated := ClipLabel(1, 2, ""); label != "Clip L2C3" || !generated {
		t.Errorf("ClipLabel(1,2,\"\") = (%q, %v), want (%q, true)", label, generated, "Clip L2C3")
	}
}

func TestPersistentClipLabelAuthoredVsGenerated(t *testing.T) {
	if label, generated := PersistentClipLabel(1, "Persistent A"); label != "Persistent A" || generated {
		t.Errorf("PersistentClipLabel(1,%q) = (%q, %v), want (%q, false)", "Persistent A", label, generated, "Persistent A")
	}
	if label, generated := PersistentClipLabel(3, ""); label != "Persistent clip 3" || !generated {
		t.Errorf("PersistentClipLabel(3,\"\") = (%q, %v), want (%q, true)", label, generated, "Persistent clip 3")
	}
}

// --- ResolveDeck / ResolveLayer / ResolveColumn against the real fixture --

func TestResolveDeckByAuthoredName(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	id, err := ResolveDeck(tc, "Deck Two")
	if err != nil {
		t.Fatalf("ResolveDeck: %v", err)
	}
	if want := ObjectID(2000000000002); id != want {
		t.Errorf("ResolveDeck(\"Deck Two\") = %v, want %v", id, want)
	}
}

func TestResolveDeckNotFoundNamesTheReference(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	_, err := ResolveDeck(tc, "Deck Three")
	if err == nil {
		t.Fatal("ResolveDeck(\"Deck Three\"): err = nil, want a not-found refusal")
	}
	if !strings.Contains(err.Error(), "Deck Three") {
		t.Errorf("refusal = %q, want it to name %q", err.Error(), "Deck Three")
	}
}

func TestResolveColumnScopedToDeck(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	// Deck One's columnIndex 0 keeps the FIRST Column element seen
	// (5000000000001, over the duplicate 5000000000009 — pkg/resolumecomp's
	// own dedup rule), and Deck Two's own columnIndex 0 is a DIFFERENT
	// object (5000000000101): the same generated label "Column 1" must
	// resolve to two different ids depending which deck it is scoped to.
	id, err := ResolveColumn(tc, "Deck One", "Column 1")
	if err != nil {
		t.Fatalf("ResolveColumn(Deck One, Column 1): %v", err)
	}
	if want := ObjectID(5000000000001); id != want {
		t.Errorf("ResolveColumn(Deck One, Column 1) = %v, want %v", id, want)
	}

	id2, err := ResolveColumn(tc, "Deck Two", "Column 1")
	if err != nil {
		t.Fatalf("ResolveColumn(Deck Two, Column 1): %v", err)
	}
	if want := ObjectID(5000000000101); id2 != want {
		t.Errorf("ResolveColumn(Deck Two, Column 1) = %v, want %v", id2, want)
	}
}

func TestResolveColumnUnknownDeckIsReportedAsADeckFailure(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	_, err := ResolveColumn(tc, "Nonexistent Deck", "Column 1")
	if err == nil {
		t.Fatal("ResolveColumn with an unknown deck: err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "deck") {
		t.Errorf("refusal = %q, want it to be reported as a deck failure, not collapsed into \"column not found\"", err.Error())
	}
}

// --- Acceptance criterion 5: an unnamed layer is addressable by its
// generated label, and a collision with an authored label is ambiguous. --

func compWithAmbiguousGeneratedLayerLabel() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Layers: []resolumecomp.Layer{
			{ID: "301", Index: 2, Name: ""},        // unnamed at index 2 -> generates "Layer 3"
			{ID: "302", Index: 9, Name: "Layer 3"}, // a DIFFERENT layer, authored with that exact string
			{ID: "303", Index: 0, Name: ""},        // unnamed at index 0 -> generates "Layer 1", no collision
		},
	}
}

func TestResolveLayerUnnamedIsAddressableByGeneratedLabel(t *testing.T) {
	tc := buildTrackedComposition(t, compWithAmbiguousGeneratedLayerLabel())
	id, err := ResolveLayer(tc, "Layer 1")
	if err != nil {
		t.Fatalf("ResolveLayer(\"Layer 1\"): %v", err)
	}
	if want := ObjectID(303); id != want {
		t.Errorf("ResolveLayer(\"Layer 1\") = %v, want %v", id, want)
	}
}

func TestResolveLayerGeneratedLabelCollidesWithAuthoredNameIsAmbiguous(t *testing.T) {
	tc := buildTrackedComposition(t, compWithAmbiguousGeneratedLayerLabel())
	_, err := ResolveLayer(tc, "Layer 3")
	if err == nil {
		t.Fatal("ResolveLayer(\"Layer 3\"): err = nil, want an ambiguity refusal — layer 301 (unnamed, at index 2) " +
			"generates this exact label and layer 302 is authored with the identical string")
	}
}

// --- Acceptance criterion 7: a trailing-space label matches only exactly. -

func TestResolveLayerTrailingSpaceMustMatchExactly(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	// complete.avc's third layer is authored "Peak + Under " (trailing space).
	if _, err := ResolveLayer(tc, "Peak + Under "); err != nil {
		t.Errorf("ResolveLayer with the exact trailing-space label failed: %v", err)
	}
	if _, err := ResolveLayer(tc, "Peak + Under"); err == nil {
		t.Error("ResolveLayer without the trailing space: err = nil, want a refusal — trimming would silently " +
			"address a reference the operator did not type")
	}
}

// --- Acceptance criteria 3 and 4: clip resolution, ambiguity, and the
// deck/persistent conditional-required rule. ---

func compWithDuplicateClipNames() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks: []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{
			{ID: "101", Index: 0, Name: "Layer A"},
			{ID: "102", Index: 1, Name: "Layer B"},
		},
		Clips: []resolumecomp.Clip{
			{ID: "201", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Fire"},
			{ID: "202", DeckID: "1", LayerIndex: 1, ColumnIndex: 0, Name: "Fire"},
		},
		// A persistent clip sharing the SAME name as the deck clips above,
		// specifically so TestResolveClipRequiresExactlyOneOfDeckOrPersistent
		// can prove the "both deck and persistent" refusal is a real guard
		// and not an accident of an empty candidate set: without the guard,
		// "Fire" + Deck "Main" + Persistent true would resolve cleanly
		// against THIS clip.
		PersistentClips: []resolumecomp.Clip{
			{ID: "301", LayerIndex: 0, ColumnIndex: 0, Name: "Fire"},
		},
	}
}

// TestResolveClipAmbiguousNamesBothCandidatesThenLayerDisambiguates is
// acceptance criterion 3.
func TestResolveClipAmbiguousNamesBothCandidatesThenLayerDisambiguates(t *testing.T) {
	tc := buildTrackedComposition(t, compWithDuplicateClipNames())

	_, err := ResolveClip(tc, ClipReference{Clip: "Fire", Deck: "Main"})
	if err == nil {
		t.Fatal("ResolveClip(\"Fire\" on \"Main\"): err = nil, want an ambiguity refusal — two clips share this name")
	}
	for _, want := range []string{"Layer A", "Layer B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity refusal = %q, want it to name both candidates' layer labels, including %q", err.Error(), want)
		}
	}

	id, err := ResolveClip(tc, ClipReference{Clip: "Fire", Deck: "Main", Layer: "Layer A"})
	if err != nil {
		t.Fatalf("ResolveClip with the disambiguating layer: %v", err)
	}
	if want := ObjectID(201); id != want {
		t.Errorf("ResolveClip(\"Fire\", layer \"Layer A\") = %v, want %v", id, want)
	}
}

// TestResolveClipLayerDisambiguatorThatMatchesNothingNarrowsToZero proves
// §2.1's own rule: a "layer" that does not match any candidate narrows the
// set to zero (a not-found refusal), never a fallback to the unfiltered
// set.
func TestResolveClipLayerDisambiguatorThatMatchesNothingNarrowsToZero(t *testing.T) {
	tc := buildTrackedComposition(t, compWithDuplicateClipNames())
	_, err := ResolveClip(tc, ClipReference{Clip: "Fire", Deck: "Main", Layer: "Layer Z"})
	if err == nil {
		t.Fatal("ResolveClip with a non-matching layer: err = nil, want a not-found refusal, not a fallback to the unfiltered set")
	}
}

// TestResolveClipRequiresExactlyOneOfDeckOrPersistent is acceptance
// criterion 4.
func TestResolveClipRequiresExactlyOneOfDeckOrPersistent(t *testing.T) {
	tc := buildTrackedComposition(t, compWithDuplicateClipNames())

	_, err := ResolveClip(tc, ClipReference{Clip: "Fire"})
	if err == nil {
		t.Fatal("ResolveClip with neither deck nor persistent: err = nil, want a refusal naming the deck as required")
	}
	if !strings.Contains(err.Error(), "must name the deck") {
		t.Errorf("refusal = %q, want it to state the §2.1 rule (\"must name the deck\"), not a generic not-found", err.Error())
	}

	// Without the guard this would resolve cleanly against the PERSISTENT
	// "Fire" clip added to the fixture above (Persistent wins, deck is
	// simply ignored) — proving this assertion actually depends on the
	// guard, not on an empty candidate set.
	if _, err := ResolveClip(tc, ClipReference{Clip: "Fire", Deck: "Main", Persistent: true}); err == nil {
		t.Error("ResolveClip with both deck and persistent: err = nil, want a refusal")
	}
}

// compWithSharedLayerClipCollision reproduces the shape measured against
// the operator's real composition (ADR-037 amendment, 2026-08-16): four
// clips sharing one name on the SAME layer, so a "layer" disambiguator can
// never resolve them — only renaming can. (The real composition's own
// deck="Main" layer="Peak Only" clip="Text Block" collision, x4.)
func compWithSharedLayerClipCollision() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Peak Only"}},
		Clips: []resolumecomp.Clip{
			{ID: "401", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Text Block"},
			{ID: "402", DeckID: "1", LayerIndex: 0, ColumnIndex: 1, Name: "Text Block"},
			{ID: "403", DeckID: "1", LayerIndex: 0, ColumnIndex: 2, Name: "Text Block"},
			{ID: "404", DeckID: "1", LayerIndex: 0, ColumnIndex: 3, Name: "Text Block"},
		},
	}
}

// TestResolveClipAmbiguousOnTheSameLayerRefusesWithRenamingNotLayerAdvice
// is acceptance criterion 14's second half: when the candidates ALREADY
// share a layer, "add a layer" is never suggested — renaming is the
// stated remedy, both with and without a layer named in the reference.
func TestResolveClipAmbiguousOnTheSameLayerRefusesWithRenamingNotLayerAdvice(t *testing.T) {
	tc := buildTrackedComposition(t, compWithSharedLayerClipCollision())

	_, err := ResolveClip(tc, ClipReference{Clip: "Text Block", Deck: "Main"})
	if err == nil {
		t.Fatal("ResolveClip: err = nil, want an ambiguity refusal")
	}
	if strings.Contains(err.Error(), `add a "layer"`) {
		t.Errorf("refusal = %q, must not suggest adding a layer — these candidates already share one", err.Error())
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("refusal = %q, want it to say renaming is the remedy", err.Error())
	}

	// Even naming the (shared) layer explicitly does not help — still
	// refused, still no "add a layer" advice, since one was already given.
	_, err = ResolveClip(tc, ClipReference{Clip: "Text Block", Deck: "Main", Layer: "Peak Only"})
	if err == nil {
		t.Fatal("ResolveClip with the shared layer already named: err = nil, want a refusal")
	}
	if strings.Contains(err.Error(), `add a "layer"`) {
		t.Errorf("refusal = %q, must not suggest adding a layer a second time", err.Error())
	}
	if !strings.Contains(err.Error(), "rename") {
		t.Errorf("refusal = %q, want it to say renaming is the remedy", err.Error())
	}
}

// TestAmbiguousClipIDsMarksSharedTriplesOnly is acceptance criterion 13's
// core logic, independent of either caller's own data model.
func TestAmbiguousClipIDsMarksSharedTriplesOnly(t *testing.T) {
	entries := map[string]ClipTripleKey{
		"401": {Deck: "1", Layer: "Peak Only", Label: "Text Block"},
		"402": {Deck: "1", Layer: "Peak Only", Label: "Text Block"},
		"501": {Deck: "1", Layer: "Other Layer", Label: "Unique Clip"},
	}
	got := AmbiguousClipIDs(entries)
	if !got["401"] || !got["402"] {
		t.Errorf("got = %+v, want \"401\" and \"402\" both true (shared triple)", got)
	}
	if got["501"] {
		t.Errorf("got = %+v, want \"501\" false (unique triple)", got)
	}
}

// TestResolveClipPersistentResolvesWithoutADeck proves a persistent
// reference needs no deck term at all (ADR-032 decision 6), against the
// real fixture's own two persistent clips.
func TestResolveClipPersistentResolvesWithoutADeck(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	id, err := ResolveClip(tc, ClipReference{Clip: "Persistent A", Persistent: true})
	if err != nil {
		t.Fatalf("ResolveClip(persistent \"Persistent A\"): %v", err)
	}
	if want := ObjectID(7000000000001); id != want {
		t.Errorf("ResolveClip(persistent \"Persistent A\") = %v, want %v", id, want)
	}
}

func TestResolveClipUnknownNameOnADeckIsRefused(t *testing.T) {
	tc := buildTrackedComposition(t, parseTestComposition(t))
	_, err := ResolveClip(tc, ClipReference{Clip: "Does Not Exist", Deck: "Deck One"})
	if err == nil {
		t.Fatal("ResolveClip with an unknown clip name: err = nil, want a not-found refusal")
	}
	if !strings.Contains(err.Error(), "Does Not Exist") {
		t.Errorf("refusal = %q, want it to name the unresolved clip", err.Error())
	}
}

// buildTrackedComposition is this file's own small helper: build, or fail
// the test immediately — every test above wants the built value, not the
// error.
func buildTrackedComposition(t *testing.T, comp *resolumecomp.Composition) *TrackedComposition {
	t.Helper()
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}
	return tc
}
