package resolume

import "testing"

var (
	deckA = ObjectID(2000000000001)
	deckB = ObjectID(2000000000002)

	deckClip1 = IdentitySampleClip{ID: 6000000000001, Name: "Snowflakes"}
	deckClip2 = IdentitySampleClip{ID: 6000000000003, Name: "Clip B"}
	persist1  = IdentitySampleClip{ID: 7000000000001, Name: "Persistent A"}
	persist2  = IdentitySampleClip{ID: 7000000000002, Name: "Persistent B"}
)

func baseSample() IdentitySample {
	return IdentitySample{
		SelectedDeck:    deckA,
		DeckClips:       []IdentitySampleClip{deckClip1, deckClip2},
		PersistentClips: []IdentitySampleClip{persist1, persist2},
	}
}

func TestCheckIdentityEverythingResolvesIsTrue(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: true, deckClip2.ID: true, persist1.ID: true, persist2.ID: true,
		},
	})
	if r.Outcome != IdentityTrue {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityTrue)
	}
}

// TestCheckIdentityNothingResolvesIsUnknownNotFalse is §6's own named
// case: "nothing resolves and Resolume is reachable -> unknown, not
// false. This is the restart load window."
func TestCheckIdentityNothingResolvesIsUnknownNotFalse(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample:   baseSample(),
		Resolved: map[ObjectID]bool{}, // every id absent from the map == unresolved
	})
	if r.Outcome != IdentityUnknown {
		t.Fatalf("Outcome = %q, want %q (never IdentityFalse when NOTHING resolved)", r.Outcome, IdentityUnknown)
	}
}

// TestCheckIdentityNothingResolvesWinsOverPersistentMissing proves the
// "nothing resolved" branch is checked FIRST, ahead of even a persistent
// clip's unconditional-stale-reference rule: with literally zero
// resolutions, §6 says unknown regardless of which specific ids would
// otherwise look like a definite stale reference.
func TestCheckIdentityNothingResolvesWinsOverPersistentMissing(t *testing.T) {
	sample := IdentitySample{SelectedDeck: deckA, PersistentClips: []IdentitySampleClip{persist1}}
	r := CheckIdentity(IdentityCheck{Sample: sample, Resolved: map[ObjectID]bool{}})
	if r.Outcome != IdentityUnknown {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityUnknown)
	}
}

func TestCheckIdentityMissingPersistentClipIsUnconditionallyFalse(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: true, deckClip2.ID: true, persist1.ID: true, persist2.ID: false,
		},
	})
	if r.Outcome != IdentityFalse {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityFalse)
	}
	if len(r.MissingIDs) != 1 || r.MissingIDs[0].ID != persist2.ID {
		t.Errorf("MissingIDs = %+v, want exactly [persist2]", r.MissingIDs)
	}
}

func TestCheckIdentityMissingDeckClipNoRecheckIsUnknown(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: false, deckClip2.ID: true, persist1.ID: true, persist2.ID: true,
		},
		DeckRecheck: nil,
	})
	if r.Outcome != IdentityUnknown {
		t.Fatalf("Outcome = %q, want %q (no recheck available -> conservative unknown, never a manufactured false)", r.Outcome, IdentityUnknown)
	}
}

func TestCheckIdentityMissingDeckClipStillSelectedIsFalse(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: false, deckClip2.ID: true, persist1.ID: true, persist2.ID: true,
		},
		DeckRecheck: &DeckRecheck{StillSelected: true, CurrentSelectedKnown: true, CurrentSelectedID: deckA},
	})
	if r.Outcome != IdentityFalse {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityFalse)
	}
	if len(r.MissingIDs) != 1 || r.MissingIDs[0].ID != deckClip1.ID {
		t.Errorf("MissingIDs = %+v, want exactly [deckClip1]", r.MissingIDs)
	}
}

// TestCheckIdentityMissingDeckClipDeckChangedIsDeckMismatch is
// TRACK-D-D2-SPEC.md §6 / adapter spec §6.4's own headline case: a 404 on
// a stored clip id whose deck is no longer selected must be reported as a
// deck mismatch, never a stale reference and never IdentityFalse.
func TestCheckIdentityMissingDeckClipDeckChangedIsDeckMismatch(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: false, deckClip2.ID: false, persist1.ID: true, persist2.ID: true,
		},
		DeckRecheck: &DeckRecheck{StillSelected: false, CurrentSelectedKnown: true, CurrentSelectedID: deckB, CurrentSelectedName: "Deck Two"},
	})
	if r.Outcome != IdentityDeckMismatch {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityDeckMismatch)
	}
	if r.ExpectedDeck.ID != deckA {
		t.Errorf("ExpectedDeck.ID = %v, want %v", r.ExpectedDeck.ID, deckA)
	}
	if !r.ActualDeckKnown || r.ActualDeck != deckB || r.ActualDeckName != "Deck Two" {
		t.Errorf("Actual deck = (known=%v id=%v name=%q), want (true, %v, %q)", r.ActualDeckKnown, r.ActualDeck, r.ActualDeckName, deckB, "Deck Two")
	}
}

func TestCheckIdentityMissingDeckClipDeckChangedButUnknownDestination(t *testing.T) {
	r := CheckIdentity(IdentityCheck{
		Sample: baseSample(),
		Resolved: map[ObjectID]bool{
			deckClip1.ID: false, deckClip2.ID: true, persist1.ID: true, persist2.ID: true,
		},
		DeckRecheck: &DeckRecheck{StillSelected: false, CurrentSelectedKnown: false},
	})
	if r.Outcome != IdentityDeckMismatch {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityDeckMismatch)
	}
	if r.ActualDeckKnown {
		t.Errorf("ActualDeckKnown = true, want false when the recheck could not identify any selected deck")
	}
}

func TestCheckIdentityEmptySampleIsUnknown(t *testing.T) {
	r := CheckIdentity(IdentityCheck{Sample: IdentitySample{SelectedDeck: deckA}, Resolved: map[ObjectID]bool{}})
	if r.Outcome != IdentityUnknown {
		t.Fatalf("Outcome = %q, want %q", r.Outcome, IdentityUnknown)
	}
}
