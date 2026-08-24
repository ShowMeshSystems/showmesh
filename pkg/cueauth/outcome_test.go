package cueauth

import "testing"

func baseTuple() AuthorizationTuple {
	return AuthorizationTuple{
		Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a",
		CueID: "thriller", CueRevision: 2,
	}
}

func baseHeld() HeldState {
	return HeldState{
		Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a",
		KnownCueRevisions: map[string]int64{"thriller": 2},
		AssetsPresent:     true,
	}
}

func TestCheckAuthorizes(t *testing.T) {
	outcome, ok := Check(baseTuple(), baseHeld())
	if !ok {
		t.Fatalf("Check(baseTuple, baseHeld) = (%q, false), want ok", outcome)
	}
	if outcome != "" {
		t.Fatalf("Check ok=true carried non-empty outcome %q", outcome)
	}
}

func TestCheckCrossShow(t *testing.T) {
	tuple := baseTuple()
	tuple.Show = "christmas-2026"
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeCrossShow {
		t.Fatalf("Check(mismatched show) = (%q, %v), want (%q, false)", outcome, ok, OutcomeCrossShow)
	}
}

func TestCheckStaleGeneration(t *testing.T) {
	tuple := baseTuple()
	tuple.Generation = 2
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeStaleGeneration {
		t.Fatalf("Check(older generation) = (%q, %v), want (%q, false)", outcome, ok, OutcomeStaleGeneration)
	}
}

func TestCheckUnknownGeneration(t *testing.T) {
	tuple := baseTuple()
	tuple.Generation = 4
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeUnknownGeneration {
		t.Fatalf("Check(newer generation) = (%q, %v), want (%q, false)", outcome, ok, OutcomeUnknownGeneration)
	}
}

func TestCheckStaleCatalog(t *testing.T) {
	tuple := baseTuple()
	tuple.CatalogRevision = "rev-b"
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeStaleCatalog {
		t.Fatalf("Check(mismatched catalog revision) = (%q, %v), want (%q, false)", outcome, ok, OutcomeStaleCatalog)
	}
}

func TestCheckUnknownCue(t *testing.T) {
	tuple := baseTuple()
	tuple.CueID = "not-in-catalog"
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeUnknownCue {
		t.Fatalf("Check(unknown cue) = (%q, %v), want (%q, false)", outcome, ok, OutcomeUnknownCue)
	}
}

func TestCheckStaleCue(t *testing.T) {
	tuple := baseTuple()
	tuple.CueRevision = 99
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeStaleCue {
		t.Fatalf("Check(stale cue revision) = (%q, %v), want (%q, false)", outcome, ok, OutcomeStaleCue)
	}
}

func TestCheckAssetMissing(t *testing.T) {
	held := baseHeld()
	held.AssetsPresent = false
	outcome, ok := Check(baseTuple(), held)
	if ok || outcome != OutcomeAssetMissing {
		t.Fatalf("Check(assets not present) = (%q, %v), want (%q, false)", outcome, ok, OutcomeAssetMissing)
	}
}

// TestCheckOrderStopsAtFirstRefusal proves a tuple wrong in more than one
// way is refused for the FIRST cause TRACK-H-H3-SPEC.md section 6's table
// orders, not the last: a cross-show tuple with an also-stale generation
// must report cross-show, never stale-generation.
func TestCheckOrderStopsAtFirstRefusal(t *testing.T) {
	tuple := baseTuple()
	tuple.Show = "christmas-2026"
	tuple.Generation = 1 // also stale, if generation were checked first
	outcome, ok := Check(tuple, baseHeld())
	if ok || outcome != OutcomeCrossShow {
		t.Fatalf("Check(cross-show AND stale generation) = (%q, %v), want (%q, false)", outcome, ok, OutcomeCrossShow)
	}
}

// TestCheckPresentFileNeverGrants proves the H3 spec's own headline rule:
// an asset present locally does not, by itself, make an otherwise-invalid
// tuple pass. AssetsPresent true alone, with every other field wrong,
// still refuses on the first mismatched field.
func TestCheckPresentFileNeverGrants(t *testing.T) {
	tuple := baseTuple()
	tuple.Show = "christmas-2026"
	held := baseHeld()
	held.AssetsPresent = true
	outcome, ok := Check(tuple, held)
	if ok || outcome != OutcomeCrossShow {
		t.Fatalf("Check(cross-show, assets present) = (%q, %v), want (%q, false): a present file must never grant authority", outcome, ok, OutcomeCrossShow)
	}
}
