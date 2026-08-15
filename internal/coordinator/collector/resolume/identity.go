package resolume

// This file is TRACK-D-D2-SPEC.md §6's composition-identity check:
// resolving [IdentitySample] (idmap.go) against by-id reads collector.go
// already performed, and classifying the result per §6 and adapter spec
// §6.4/decision 6. It performs no network I/O of its own — every fact it
// needs is handed in, already fetched, by [IdentityCheck] — which is what
// makes [CheckIdentity] a pure function this package's tests can drive
// without an HTTP server.

// IdentityOutcome is the four-way result §6 defines. Three of the four
// (True, False, Unknown) map onto the Resolume-observation vocabulary
// directly. The fourth, DeckMismatch, is explicitly "not an identity
// result at all" (§6's own wording) — collector.go's own doc comment on how
// it uses this says why it is handled differently from the other three
// rather than folded into False or Unknown.
type IdentityOutcome string

const (
	IdentityTrue         IdentityOutcome = "identified"
	IdentityFalse        IdentityOutcome = "not_identified"
	IdentityUnknown      IdentityOutcome = "unknown"
	IdentityDeckMismatch IdentityOutcome = "deck_mismatch"
)

// DeckRecheck is collector.go's answer to "is the deck this identity
// sample was drawn from still the selected deck", read AFTER the sample's
// clip ids were resolved — never the same reading used to draw the sample
// in the first place. §6.4's own "the deck reading is itself evidence and
// is fenced" rule applies here exactly as it does to an action's
// confirmation: deciding "deck mismatch" off a reading of `selected` the
// operator has since changed would be the identical defect in a new place.
//
// Populated by collector.go ONLY when at least one DeckClips entry failed
// to resolve — see [IdentityCheck.DeckRecheck]'s own doc comment for why a
// recheck is skipped otherwise.
type DeckRecheck struct {
	// StillSelected is true when Sample.SelectedDeck is still the
	// currently selected deck as of this recheck.
	StillSelected bool

	// CurrentSelectedKnown is false when the recheck could not determine
	// ANY currently selected deck at all (e.g. every re-read deck object
	// itself failed, or none reports selected=true) — Resolume becoming
	// unreachable mid-survey is the expected cause. CurrentSelectedID/Name
	// are meaningful only when this is true.
	CurrentSelectedKnown bool
	CurrentSelectedID    ObjectID
	CurrentSelectedName  string
}

// IdentityCheck is every already-fetched fact [CheckIdentity] needs.
// collector.go builds one per survey from its own by-id reads; nothing in
// this type performs a read of its own.
type IdentityCheck struct {
	Sample IdentitySample

	// Resolved maps every id named in Sample.DeckClips and
	// Sample.PersistentClips to whether a by-id read of it resolved (true)
	// or 404'd (false). An id from Sample with no entry here is treated
	// identically to false (did not resolve) — collector.go's own survey
	// logic always populates every sample id, so this is a defensive
	// fallback, not a documented input shape a caller should rely on.
	Resolved map[ObjectID]bool

	// DeckRecheck is non-nil only when collector.go performed one — see
	// this type's own doc comment on DeckRecheck for when that happens.
	// nil here with at least one DeckClips id unresolved means "collector.go
	// judged a recheck unnecessary or could not perform one"; CheckIdentity
	// treats that conservatively (IdentityUnknown, never a manufactured
	// False) rather than guessing.
	DeckRecheck *DeckRecheck
}

// IdentityResult is [CheckIdentity]'s output.
type IdentityResult struct {
	Outcome IdentityOutcome

	// MissingIDs names every sample id that failed to resolve and
	// contributed to Outcome (empty for IdentityTrue). Carried separately
	// from Reason so a caller building an operator-facing message can
	// choose its own formatting; Reason is already a complete sentence.
	MissingIDs []IdentitySampleClip

	// Reason is operator-facing: it must read as though no internal
	// document, ADR, or research record existed. Mechanically enforced by
	// TestReadinessAndIdentityStringsCarryNoInternalCitation
	// (collector_test.go).
	Reason string

	// ExpectedDeck/ActualDeck are populated only for IdentityDeckMismatch,
	// naming both decks per §6.4's own requirement ("naming both decks").
	// ActualDeckKnown is false when the recheck could not identify any
	// currently selected deck at all (DeckRecheck.CurrentSelectedKnown was
	// false) — ActualDeck is then the zero value and Reason says so rather
	// than naming a deck that was never actually determined.
	ExpectedDeck    IdentitySampleClip // Name is not populated (idmap.go's IdentitySample carries no deck names)
	ActualDeckKnown bool
	ActualDeck      ObjectID
	ActualDeckName  string
}

// CheckIdentity classifies an already-fetched [IdentityCheck] per §6.
//
// Order of classification, matching §6's own bullet order:
//  1. Nothing in the sample resolved at all (and the sample was non-empty)
//     -> IdentityUnknown. This is checked FIRST and wins over every other
//     branch below: §6 is explicit that this is "the restart load window
//     and it resolves itself", never a false or a deck mismatch, however
//     the individual ids would otherwise classify.
//  2. Any PersistentClips id failed to resolve -> IdentityFalse.
//     Persistent clips resolve regardless of deck selection (ADR-032
//     decision 6), so a 404 on one is unconditionally a stale reference —
//     no deck question to ask.
//  3. Any DeckClips id failed to resolve:
//     - no DeckRecheck available -> IdentityUnknown (conservative: cannot
//     tell a stale reference from a deck-selection race, so this must
//     not manufacture a False).
//     - DeckRecheck.StillSelected -> IdentityFalse (genuine stale
//     reference: the id's own deck was selected both when the sample was
//     drawn and when it was re-verified).
//     - otherwise -> IdentityDeckMismatch, naming both decks.
//  4. Everything resolved -> IdentityTrue.
func CheckIdentity(ck IdentityCheck) IdentityResult {
	total := len(ck.Sample.DeckClips) + len(ck.Sample.PersistentClips)
	if total == 0 {
		return IdentityResult{Outcome: IdentityUnknown, Reason: "the uploaded composition has no clip ids to sample against, so identity cannot be checked"}
	}

	resolvedCount := 0
	for _, c := range ck.Sample.DeckClips {
		if ck.Resolved[c.ID] {
			resolvedCount++
		}
	}
	for _, c := range ck.Sample.PersistentClips {
		if ck.Resolved[c.ID] {
			resolvedCount++
		}
	}
	if resolvedCount == 0 {
		return IdentityResult{
			Outcome: IdentityUnknown,
			Reason:  "no sampled clip id resolved yet; if Resolume just restarted, this typically clears within the first few seconds as it finishes loading the composition",
		}
	}

	var missingPersistent []IdentitySampleClip
	for _, c := range ck.Sample.PersistentClips {
		if !ck.Resolved[c.ID] {
			missingPersistent = append(missingPersistent, c)
		}
	}
	if len(missingPersistent) > 0 {
		return IdentityResult{
			Outcome:    IdentityFalse,
			MissingIDs: missingPersistent,
			Reason:     "one or more persistent clips from the uploaded composition no longer exist in the running show",
		}
	}

	var missingDeck []IdentitySampleClip
	for _, c := range ck.Sample.DeckClips {
		if !ck.Resolved[c.ID] {
			missingDeck = append(missingDeck, c)
		}
	}
	if len(missingDeck) == 0 {
		return IdentityResult{Outcome: IdentityTrue}
	}

	if ck.DeckRecheck == nil {
		return IdentityResult{
			Outcome:    IdentityUnknown,
			MissingIDs: missingDeck,
			Reason:     "some sampled clips did not resolve and the selected deck could not be re-verified, so a stale reference cannot be told apart from a deck change",
		}
	}
	if ck.DeckRecheck.StillSelected {
		return IdentityResult{
			Outcome:    IdentityFalse,
			MissingIDs: missingDeck,
			Reason:     "one or more clips from the uploaded composition no longer exist on the deck that is still selected",
		}
	}

	result := IdentityResult{
		Outcome:      IdentityDeckMismatch,
		MissingIDs:   missingDeck,
		ExpectedDeck: IdentitySampleClip{ID: ck.Sample.SelectedDeck},
	}
	if ck.DeckRecheck.CurrentSelectedKnown {
		result.ActualDeckKnown = true
		result.ActualDeck = ck.DeckRecheck.CurrentSelectedID
		result.ActualDeckName = ck.DeckRecheck.CurrentSelectedName
		result.Reason = "the selected deck changed while this identity check was running, so the missing clips are not evidence of a stale composition"
	} else {
		result.Reason = "the selected deck changed while this identity check was running and could not be re-identified, so the missing clips are not evidence of a stale composition"
	}
	return result
}
