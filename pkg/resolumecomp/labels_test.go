package resolumecomp

import "testing"

// This file is labels.go's own test suite. It moved here from
// internal/coordinator/collector/resolume/references_test.go along with the
// functions it tests: these are pure over this package's own parsed types
// and have no dependency on a *TrackedComposition, so the coverage belongs
// with the implementation.

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

func TestLayerLabelByIndexUnknownIndexReportsNotKnown(t *testing.T) {
	layers := []Layer{{ID: "101", Index: 0, Name: "First"}}
	if _, known := LayerLabelByIndex(layers, 99); known {
		t.Error("LayerLabelByIndex(...,99) known = true, want false — no layer has that index")
	}
}

// TestLayerLabelByIndexDuplicateIndexKeepsLastMatch pins the same
// last-match-wins rule internal/coordinator/collector/resolume's
// BuildTrackedComposition relies on for its own layerIndexToID (a plain map
// write, which also keeps the last one seen). See that package's own
// TestLayerLabelByIndexAgreesWithBuildTrackedCompositionOnDuplicateIndex for
// the cross-package proof that the two agree.
func TestLayerLabelByIndexDuplicateIndexKeepsLastMatch(t *testing.T) {
	layers := []Layer{
		{ID: "101", Index: 0, Name: "First"},
		{ID: "102", Index: 0, Name: "Second"},
	}
	label, known := LayerLabelByIndex(layers, 0)
	if !known {
		t.Fatal("LayerLabelByIndex(...,0) known = false, want true")
	}
	if label != "Second" {
		t.Errorf("LayerLabelByIndex(...,0) = %q, want %q (duplicate index keeps the LAST layer)", label, "Second")
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
