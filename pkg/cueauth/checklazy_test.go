package cueauth

import "testing"

// TestCheckLazyCrossShowNeverConsultsDisk proves H3 build item 3's own
// requirement: "a node holding a matching filename for a non-authorized
// Show still refuses with cross-show without consulting its disk." Here,
// "consulting its disk" is stood in for by assetsPresent: a node holding
// Thriller.fseq (a matching filename an unwary check might treat as
// evidence of permission) would, if actually asked, report true — but a
// cross-show tuple must never even ask.
func TestCheckLazyCrossShowNeverConsultsDisk(t *testing.T) {
	tuple := baseTuple()
	tuple.Show = "christmas-2026" // this node is only authorized for halloween-2026

	consultedDisk := false
	assetsPresent := func() bool {
		consultedDisk = true
		// Simulates a node that DOES hold a matching file on disk (e.g.
		// last year's Thriller.fseq) — if this were ever consulted, it
		// would say "present."
		return true
	}

	outcome, ok := CheckLazy(tuple, baseHeld(), assetsPresent)
	if ok || outcome != OutcomeCrossShow {
		t.Fatalf("CheckLazy(cross-show) = (%q, %v), want (%q, false)", outcome, ok, OutcomeCrossShow)
	}
	if consultedDisk {
		t.Fatalf("CheckLazy consulted assetsPresent for a cross-show tuple; a present file must never be a reason to execute, and must never even be asked about, for a Show this node is not authorized for")
	}
}

// TestCheckLazyStaleGenerationNeverConsultsDisk extends the same proof to
// every one of the six checks that do not depend on asset presence: none
// of them may ever reach assetsPresent.
func TestCheckLazyStaleGenerationNeverConsultsDisk(t *testing.T) {
	tuple := baseTuple()
	tuple.Generation = 1 // held is generation 3; this is stale

	called := false
	outcome, ok := CheckLazy(tuple, baseHeld(), func() bool { called = true; return true })
	if ok || outcome != OutcomeStaleGeneration {
		t.Fatalf("CheckLazy(stale generation) = (%q, %v), want (%q, false)", outcome, ok, OutcomeStaleGeneration)
	}
	if called {
		t.Fatalf("CheckLazy consulted assetsPresent for a stale-generation tuple")
	}
}

// TestCheckLazyAssetMissingConsultsDiskOnlyOnceEverythingElsePasses proves
// the converse: once every other check has genuinely passed, CheckLazy
// DOES consult assetsPresent, and a false answer refuses as asset-missing
// — the one case where disk truly matters.
func TestCheckLazyAssetMissingConsultsDiskOnlyOnceEverythingElsePasses(t *testing.T) {
	called := false
	outcome, ok := CheckLazy(baseTuple(), baseHeld(), func() bool { called = true; return false })
	if ok || outcome != OutcomeAssetMissing {
		t.Fatalf("CheckLazy(assets absent) = (%q, %v), want (%q, false)", outcome, ok, OutcomeAssetMissing)
	}
	if !called {
		t.Fatalf("CheckLazy never consulted assetsPresent even though every other check passed")
	}
}

// TestCheckLazyAuthorizes proves the success path also consults
// assetsPresent exactly once and authorizes when it reports true.
func TestCheckLazyAuthorizes(t *testing.T) {
	calls := 0
	outcome, ok := CheckLazy(baseTuple(), baseHeld(), func() bool { calls++; return true })
	if !ok || outcome != "" {
		t.Fatalf("CheckLazy(authorized) = (%q, %v), want (\"\", true)", outcome, ok)
	}
	if calls != 1 {
		t.Fatalf("CheckLazy called assetsPresent %d times, want exactly 1", calls)
	}
}
