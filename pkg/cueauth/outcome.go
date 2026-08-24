// Package cueauth is TRACK-H-H3-SPEC.md section 6's closed refusal
// vocabulary for checking a Cue authorization tuple (section 5) against
// what a checking side currently holds. It has no HTTP, no store access,
// and dispatches nothing: H3 ships the vocabulary and the pure check both
// the coordinator's dispatch path and a node's execution path reuse (H4
// owns actually wiring either); this seam's own job is making sure
// neither of them can invent a different answer for the same evidence,
// the identical role internal/coordinator/fppreconcile plays for FPP
// observation reconciliation.
//
// This package lives under pkg/, not internal/coordinator, SPECIFICALLY
// so both internal/coordinator and internal/agent can import the one copy
// of the vocabulary and the one copy of [Check] — internal/agent must
// never import internal/coordinator (or vice versa — see pkg/cuecatalog's
// own doc comment for the identical boundary), and pkg/ is this
// codebase's established third place for a type or function two otherwise
// disjoint binaries both need (pkg/fppidentity and pkg/observation play
// the identical role for other cross-boundary contracts). It started as
// internal/coordinator/cueauth before the node-agent half of H3 needed it
// too; moving it here, rather than reimplementing it a second time in
// internal/agent, is what makes "both refuse the same set" a fact about
// shared Go code rather than merely a contract on seven string spellings
// kept in sync by convention.
package cueauth

// Outcome is one of TRACK-H-H3-SPEC.md section 6's seven refusal reasons,
// checked in the exact order [Check] evaluates them (first match wins) —
// the same "state with evidence, never a silent no-op" posture
// fppreconcile.Outcome already established one seam earlier.
type Outcome string

const (
	// OutcomeCrossShow: the tuple's Show is not the checking side's
	// authorized Show.
	OutcomeCrossShow Outcome = "cross-show"

	// OutcomeStaleGeneration: the tuple's Generation is older than the
	// checking side's.
	OutcomeStaleGeneration Outcome = "stale-generation"

	// OutcomeUnknownGeneration: the tuple's Generation is newer than the
	// checking side's — the checking side re-fetches rather than trusting
	// a generation it has not itself resolved.
	OutcomeUnknownGeneration Outcome = "unknown-generation"

	// OutcomeStaleCatalog: the tuple's CatalogRevision does not match the
	// checking side's held catalog revision.
	OutcomeStaleCatalog Outcome = "stale-catalog"

	// OutcomeUnknownCue: the tuple's CueID is not in the checking side's
	// held catalog at all.
	OutcomeUnknownCue Outcome = "unknown-cue"

	// OutcomeStaleCue: the tuple's CueID IS in the held catalog, but at a
	// different CueRevision.
	OutcomeStaleCue Outcome = "stale-cue"

	// OutcomeAssetMissing: every other check passed, but an asset the Cue
	// names is not present locally. A present file is never, by itself, a
	// reason TO execute (H3 spec section 6) — this outcome is checked
	// LAST, and only ever refuses, never grants.
	OutcomeAssetMissing Outcome = "asset-missing"
)

// AuthorizationTuple is what an activation or a dispatch derived from one
// carries end to end (H3 spec section 5) — the fields [Check] compares
// against a [HeldState].
type AuthorizationTuple struct {
	Show            string
	Generation      int64
	CatalogRevision string
	CueID           string
	CueRevision     int64
}

// HeldState is what the checking side (a future coordinator dispatch path,
// or a node's execution path) currently holds, to check a tuple against.
type HeldState struct {
	Show            string
	Generation      int64
	CatalogRevision string

	// KnownCueRevisions maps a held catalog's Cue id to its Cue revision,
	// so [OutcomeUnknownCue] and [OutcomeStaleCue] are distinguishable
	// rather than collapsed into one "does not match" outcome.
	KnownCueRevisions map[string]int64

	// AssetsPresent reports whether every asset the tuple's Cue needs is
	// present locally. Checked last, and only by [Check] itself never
	// short-circuiting the six checks before it — see [OutcomeAssetMissing]'s
	// own doc comment for why a present file must never grant anything.
	AssetsPresent bool
}

// Check evaluates tuple against held in TRACK-H-H3-SPEC.md section 6's
// exact order, stopping at the first refusal. ok is true, and outcome is
// the zero value, only when every check passes — never both a non-zero
// outcome and ok == true, and never a silent success from an unset field:
// a caller supplying a zero-value HeldState (e.g. never having resolved
// anything yet) gets OutcomeCrossShow or OutcomeUnknownGeneration from the
// very first checks, never an accidental "authorized".
func Check(tuple AuthorizationTuple, held HeldState) (outcome Outcome, ok bool) {
	if tuple.Show != held.Show {
		return OutcomeCrossShow, false
	}
	if tuple.Generation < held.Generation {
		return OutcomeStaleGeneration, false
	}
	if tuple.Generation > held.Generation {
		return OutcomeUnknownGeneration, false
	}
	if tuple.CatalogRevision != held.CatalogRevision {
		return OutcomeStaleCatalog, false
	}
	heldRevision, known := held.KnownCueRevisions[tuple.CueID]
	if !known {
		return OutcomeUnknownCue, false
	}
	if heldRevision != tuple.CueRevision {
		return OutcomeStaleCue, false
	}
	if !held.AssetsPresent {
		return OutcomeAssetMissing, false
	}
	return "", true
}

// CheckLazy is [Check] with [HeldState.AssetsPresent] resolved lazily, by
// calling assetsPresent, ONLY when every earlier check has already passed
// — never before. TRACK-H-H3-SPEC.md section 6's own rule ("a present
// file is never a reason to execute... a node holding Thriller.fseq from
// last year's Show refuses a Halloween Cue under cross-show without ever
// looking at its disk") is a statement about behaviour, not merely about
// which string comes back: a caller whose assetsPresent stats the local
// filesystem must not touch disk at all for a cross-show, stale, or
// unknown/stale-Cue tuple. Check itself cannot express that on its own,
// because [HeldState.AssetsPresent] is a plain bool its caller must
// already have resolved before calling it; CheckLazy is the one place
// that defers resolving it, by probing Check with AssetsPresent forced
// true first (which can only ever produce one of the six checks that
// don't depend on it, or an outright pass) and consulting assetsPresent
// only in that second, pass-so-far case — never in the first.
func CheckLazy(tuple AuthorizationTuple, held HeldState, assetsPresent func() bool) (outcome Outcome, ok bool) {
	probe := held
	probe.AssetsPresent = true
	outcome, ok = Check(tuple, probe)
	if !ok {
		// One of the first six checks already refused; probe's forced-true
		// AssetsPresent could not have caused that, so this is a genuine
		// refusal reached without ever calling assetsPresent.
		return outcome, false
	}
	if assetsPresent() {
		return "", true
	}
	return OutcomeAssetMissing, false
}
