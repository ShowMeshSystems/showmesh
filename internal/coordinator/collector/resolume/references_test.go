package resolume

import (
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is references.go's own test suite: the resolve functions
// against [TrackedComposition] fixtures. The label vocabulary itself
// (LayerLabel and friends) is pkg/resolumecomp's own responsibility and is
// tested there; parseTestComposition (idmap_test.go) supplies the real,
// named fixture (pkg/resolumecomp's testdata/complete.avc), and a handful
// of tests below build a small literal resolumecomp.Composition instead,
// when the scenario is not already present in that fixture.

// TestLayerLabelByIndexAgreesWithBuildTrackedCompositionOnDuplicateIndex is
// review finding 9: BuildTrackedComposition's own layerIndexToID is a plain
// map write and so keeps the LAST layer for a duplicate index; pkg/resolumecomp's
// LayerLabelByIndex must agree, not silently pick the first.
func TestLayerLabelByIndexAgreesWithBuildTrackedCompositionOnDuplicateIndex(t *testing.T) {
	comp := &resolumecomp.Composition{
		Layers: []resolumecomp.Layer{
			{ID: "101", Index: 0, Name: "First"},
			{ID: "102", Index: 0, Name: "Second"},
		},
		Decks: []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Clips: []resolumecomp.Clip{
			{ID: "201", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Clip"},
		},
	}
	tc := buildTrackedComposition(t, comp)

	clip, ok := tc.ClipByID(201)
	if !ok || clip.LayerID == nil {
		t.Fatalf("clip 201 LayerID = %v, want a resolved layer", clip.LayerID)
	}
	resolvedLayer, ok := tc.LayerByID(*clip.LayerID)
	if !ok {
		t.Fatalf("LayerByID(%v): not found", *clip.LayerID)
	}
	wantLabel, _ := resolumecomp.LayerLabel(resolvedLayer.Index, resolvedLayer.Name)

	gotLabel, known := resolumecomp.LayerLabelByIndex(comp.Layers, 0)
	if !known {
		t.Fatalf("LayerLabelByIndex(...,0) known = false, want true")
	}
	if gotLabel != wantLabel {
		t.Errorf("LayerLabelByIndex(...,0) = %q, want %q (must agree with BuildTrackedComposition's own last-match-wins layerIndexToID)", gotLabel, wantLabel)
	}
	if gotLabel != "Second" {
		t.Errorf("LayerLabelByIndex(...,0) = %q, want %q (BuildTrackedComposition keeps the LAST layer for a duplicate index)", gotLabel, "Second")
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
	// The collapsed message this test exists to catch is "no column named
	// %q was found on deck %q", which also contains "deck" — so the check
	// must be on the actual deck-resolution wrapping, not a bare substring
	// "deck" that would pass either way.
	if !strings.Contains(err.Error(), "resolving this column's deck:") {
		t.Errorf("refusal = %q, want it wrapped as a deck-resolution failure (\"resolving this column's deck: ...\"), not collapsed into \"column not found\"", err.Error())
	}
	if !strings.Contains(err.Error(), "Nonexistent Deck") {
		t.Errorf("refusal = %q, want it to name the deck that did not resolve", err.Error())
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

// compWithSingleClipOnOneLayer has exactly one clip named "Solo", on Layer
// A only — specifically so TestResolveClipLayerDisambiguatorThatMatchesNothingNarrowsToZero
// can tell a correct refusal apart from a fallback bug: the unfiltered
// candidate set has exactly one member, so a fallback to it would return a
// SUCCESSFUL id (the wrong one — on a different layer than asked for),
// while narrowing to zero (correct) refuses.
func compWithSingleClipOnOneLayer() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks: []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{
			{ID: "101", Index: 0, Name: "Layer A"},
			{ID: "102", Index: 1, Name: "Layer B"},
		},
		Clips: []resolumecomp.Clip{
			{ID: "501", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Solo"},
		},
	}
}

// TestResolveClipLayerDisambiguatorThatMatchesNothingNarrowsToZero proves a
// "layer" that does not match any candidate narrows the set to zero (a
// not-found refusal naming the layer that was asked for), never a fallback
// to the unfiltered set — see compWithSingleClipOnOneLayer's own doc
// comment for why this fixture, and not a decorative one where every
// outcome is an error either way, actually discriminates the two.
func TestResolveClipLayerDisambiguatorThatMatchesNothingNarrowsToZero(t *testing.T) {
	tc := buildTrackedComposition(t, compWithSingleClipOnOneLayer())
	_, err := ResolveClip(tc, ClipReference{Clip: "Solo", Deck: "Main", Layer: "Layer B"})
	if err == nil {
		t.Fatal("ResolveClip with a non-matching layer: err = nil, want a not-found refusal, not a fallback to the unfiltered set")
	}
	if !strings.Contains(err.Error(), "Solo") || !strings.Contains(err.Error(), "Layer B") {
		t.Errorf("refusal = %q, want it to name both the clip and the layer that did not match", err.Error())
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
		t.Errorf("refusal = %q, want it to state the specific rule (\"must name the deck\"), not a generic not-found", err.Error())
	}

	// Without the guard this would resolve cleanly against the PERSISTENT
	// "Fire" clip added to the fixture above (Persistent wins, deck is
	// simply ignored) — proving this assertion actually depends on the
	// guard, not on an empty candidate set.
	if _, err := ResolveClip(tc, ClipReference{Clip: "Fire", Deck: "Main", Persistent: true}); err == nil {
		t.Error("ResolveClip with both deck and persistent: err = nil, want a refusal")
	}
}

// compWithMixedClipCollision is neither all-distinct
// (compWithDuplicateClipNames) nor all-shared
// (compWithSharedLayerClipCollision): three clips named "Snow" on one
// deck, one on Layer A and TWO on Layer B — review finding 1's own
// reproduction. A candidate set like this must still advise adding a
// layer, since Layer A alone resolves it right now; only a bug that
// refuses whenever ANY pair in the matched set shares a layer (rather than
// when EVERY candidate does) gets this one wrong.
func compWithMixedClipCollision() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks: []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{
			{ID: "101", Index: 0, Name: "Layer A"},
			{ID: "102", Index: 1, Name: "Layer B"},
		},
		Clips: []resolumecomp.Clip{
			{ID: "601", DeckID: "1", LayerIndex: 0, ColumnIndex: 0, Name: "Snow"},
			{ID: "602", DeckID: "1", LayerIndex: 1, ColumnIndex: 0, Name: "Snow"},
			{ID: "603", DeckID: "1", LayerIndex: 1, ColumnIndex: 1, Name: "Snow"},
		},
	}
}

// TestResolveClipMixedCollisionStillAdvisesAddingALayer is review finding
// 1: the rename branch must fire only when EVERY matched candidate shares
// one layer, not when any pair among them does — one colliding pair must
// not poison the advice for a candidate a layer would actually
// disambiguate.
func TestResolveClipMixedCollisionStillAdvisesAddingALayer(t *testing.T) {
	tc := buildTrackedComposition(t, compWithMixedClipCollision())

	_, err := ResolveClip(tc, ClipReference{Clip: "Snow", Deck: "Main"})
	if err == nil {
		t.Fatal("ResolveClip: err = nil, want an ambiguity refusal — three clips share this name")
	}
	if strings.Contains(err.Error(), "rename") {
		t.Errorf("refusal = %q, must not say renaming is the remedy — Layer A alone would disambiguate right now", err.Error())
	}
	if !strings.Contains(err.Error(), `add a "layer"`) {
		t.Errorf("refusal = %q, want it to suggest adding a layer", err.Error())
	}

	id, err := ResolveClip(tc, ClipReference{Clip: "Snow", Deck: "Main", Layer: "Layer A"})
	if err != nil {
		t.Fatalf("ResolveClip with Layer A named: %v", err)
	}
	if want := ObjectID(601); id != want {
		t.Errorf("ResolveClip(\"Snow\", layer \"Layer A\") = %v, want %v", id, want)
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

// compWithOneUnresolvableClipLayer has exactly one "Ghost" clip whose own
// layerIndex does not resolve — specifically so a match against
// --layer "an unknown layer" would return a SUCCESSFUL (wrong) id if the
// historical bug were present, rather than merely a different flavor of
// error the way two colliding unresolved clips would.
func compWithOneUnresolvableClipLayer() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips: []resolumecomp.Clip{
			{ID: "701", DeckID: "1", LayerIndex: 99, ColumnIndex: 0, Name: "Ghost"},
		},
	}
}

// compWithUnresolvableClipLayers has two "Ghost" clips whose own layerIndex
// does not match any tracked layer's index — layerIndex 99 and 98 against
// one layer at index 0 — so neither clip's LayerID resolves at all.
func compWithUnresolvableClipLayers() *resolumecomp.Composition {
	return &resolumecomp.Composition{
		Decks:  []resolumecomp.Deck{{ID: "1", Name: "Main"}},
		Layers: []resolumecomp.Layer{{ID: "101", Index: 0, Name: "Layer A"}},
		Clips: []resolumecomp.Clip{
			{ID: "701", DeckID: "1", LayerIndex: 99, ColumnIndex: 0, Name: "Ghost"},
			{ID: "702", DeckID: "1", LayerIndex: 98, ColumnIndex: 0, Name: "Ghost"},
		},
	}
}

// TestResolveClipUnresolvedLayerIsNeverAMatchableReference is review
// finding 2: a clip whose layer could not be resolved has no label a
// "layer" reference could ever equal — not even the literal prose a
// refusal renders for it — and two such clips are never thereby treated as
// sharing a layer, since neither's actual layer is known.
func TestResolveClipUnresolvedLayerIsNeverAMatchableReference(t *testing.T) {
	// Half A: on a composition with exactly one unresolved-layer clip, a
	// reference naming the exact prose a refusal would render for it must
	// still refuse — not resolve successfully.
	single := buildTrackedComposition(t, compWithOneUnresolvableClipLayer())
	if _, err := ResolveClip(single, ClipReference{Clip: "Ghost", Deck: "Main", Layer: "an unknown layer"}); err == nil {
		t.Fatal(`ResolveClip with --layer "an unknown layer": err = nil, want a refusal — an unresolved layer must never match any reference string`)
	}

	// Half B: two clips whose layer could not be resolved are NOT thereby
	// known to share one.
	tc := buildTrackedComposition(t, compWithUnresolvableClipLayers())
	_, err := ResolveClip(tc, ClipReference{Clip: "Ghost", Deck: "Main"})
	if err == nil {
		t.Fatal(`ResolveClip("Ghost"): err = nil, want an ambiguity refusal`)
	}
	if strings.Contains(err.Error(), "share the same layer") {
		t.Errorf("refusal = %q, must not claim two unresolved-layer clips share a layer — that was never established", err.Error())
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
