package resolume

import "time"

// This file is Track D seam D-2/C's own implementation of
// TRACK-D-ADAPTER-SPEC.md §3.7's readiness conjunction: "computed in
// exactly one place... every caller uses it." [LayerReady] is that one
// place. Nothing else in this package, and nothing in collector.go, may
// re-derive "is this layer putting anything on the wall" from Layer/
// LayerGroup fields directly — nothing does, as of this file: collector.go
// builds a [ReadinessInputs] from its own by-id reads and calls this
// function, and that is the only path a readiness verdict can be produced
// through.
//
// # Kleene, not boolean
//
// A conjunction over three-valued terms (known-true, known-false, unknown)
// is not the same function as a conjunction over two-valued terms with an
// "unknown" bolted on afterwards. This file uses Kleene's K3 AND
// throughout: a single known-false term makes the whole conjunction false
// REGARDLESS of what any other term is, including a term this seam can
// NEVER read at all — composition.bypassed and composition.master are
// permanently unavailable (defect 2, 2026-08-15: no
// `GET /composition/{parameter}` path exists anywhere in Arena's own
// specification). Only when no term is known-false, and at least one term
// is unknown, does the result become unknown. This is deliberate and is
// what makes the two permanently-unavailable composition-level terms
// compatible with "a bypassed layer must always report not-ready" (adapter
// spec §3.7): their permanent unreadability never prevents a DEFINITE
// not-ready verdict from a term this seam CAN read.

// ReadinessTerm names one of the up-to-seven terms
// TRACK-D-ADAPTER-SPEC.md §3.7's conjunction is built from. The adapter
// spec is explicit that these seven are not claimed to be exhaustive — a
// crossfadergroup/crossfader-position term is a documented open question
// (§10) — so [Readiness] never states or implies "these are all the ways a
// layer can be silenced," only "these seven, as measured, all currently
// agree" or name which one(s) did not.
// # Known unevaluated terms
//
// Two readable fields are known NOT to be folded into the seven-term
// conjunction below, both for the identical reason: Resolume's own
// specification does not document their interaction with the rest of a
// composition's routing well enough to encode a rule this package could
// stand behind. `crossfadergroup`/the composition-level CrossFader's own
// `phase` is one (open question, unresearched further here). `solo` — on
// both Layer and LayerGroup — is the other, and unlike crossfader it IS
// evaluated by this package, just not as an eighth conjunction term: see
// [ApplySoloOverride], which downgrades a layer's already-computed verdict
// to Unknown when solo is active elsewhere and this layer is not part of
// it, applied by the caller AFTER [LayerReady] rather than folded into its
// own Kleene AND.
type ReadinessTerm string

const (
	ReadinessTermLayerBypassed       ReadinessTerm = "layer.bypassed"
	ReadinessTermLayerMaster         ReadinessTerm = "layer.master"
	ReadinessTermLayerVideoOpacity   ReadinessTerm = "layer.video.opacity"
	ReadinessTermGroupBypassed       ReadinessTerm = "layergroup.bypassed"
	ReadinessTermGroupMaster         ReadinessTerm = "layergroup.master"
	ReadinessTermCompositionBypassed ReadinessTerm = "composition.bypassed"
	ReadinessTermCompositionMaster   ReadinessTerm = "composition.master"

	// ReadinessTermSolo is [ApplySoloOverride]'s own term name — NOT a
	// member of readinessTermOrder or [ReadinessInputs], because it is
	// never one of [LayerReady]'s seven Kleene-AND inputs. It exists only
	// so a solo-caused Unknown verdict names itself the same way every
	// other unknown term does.
	ReadinessTermSolo ReadinessTerm = "solo"
)

// readinessTermOrder is the fixed evaluation order [LayerReady] walks in.
// It matches TRACK-D-ADAPTER-SPEC.md §3.7's own pseudocode top to bottom.
// Fixed order matters for exactly one thing: when more than one term is
// known-false, or more than one is unknown, the RESULT lists them in this
// same order every time, so two runs against identical evidence always
// produce an identically-worded observation — never a map-iteration-order
// flap that would make a stable input look like it changed.
var readinessTermOrder = []ReadinessTerm{
	ReadinessTermLayerBypassed,
	ReadinessTermLayerMaster,
	ReadinessTermLayerVideoOpacity,
	ReadinessTermGroupBypassed,
	ReadinessTermGroupMaster,
	ReadinessTermCompositionBypassed,
	ReadinessTermCompositionMaster,
}

// ReadinessTermInput is the caller's own evidence for exactly one
// conjunction term, already reduced to the three-valued shape this
// function needs: Known and (only when Known) HeldTrue/ObservedAt, or (only
// when !Known) UnknownReason.
//
// Building one of these from a raw *Field (composition.go), or the fixed
// permanently-unavailable answer for composition.bypassed/master
// ([compositionLevelReadinessTerm], collector.go), is collector.go's job —
// see boolTermHoldsWhenFalse and rangeTermHoldsWhenPositive below, which do
// that translation once so every term uses the identical null-vs-absent
// handling rather than seven hand-rolled copies of it.
type ReadinessTermInput struct {
	// Known is false when this term's evidence was unavailable: the field
	// was explicit JSON null, absent from the response entirely, its own
	// "value" key was absent from an otherwise-present envelope, the term
	// is one of the two permanently-unavailable composition-level terms, or
	// the by-id read that would have produced it failed outright.
	// Known=false NEVER means "false" — see this file's own top comment.
	Known bool

	// HeldTrue is meaningful only when Known is true: whether this term's
	// own condition holds (e.g., for ReadinessTermLayerBypassed, HeldTrue
	// means bypassed == false — the term is phrased so HeldTrue is always
	// "this term does not block readiness").
	HeldTrue bool

	// ObservedAt is meaningful only when Known is true: when the evidence
	// backing HeldTrue was actually read. [LayerReady] uses this to derive
	// the overall verdict's own freshness — see [Readiness.ObservedAt]'s
	// doc comment.
	ObservedAt time.Time

	// UnknownReason is required, and operator-facing, whenever Known is
	// false. It must read as though no internal document, ADR, or research
	// record existed (CLAUDE.md's own rule, mechanically enforced for this
	// package by TestReadinessAndIdentityStringsCarryNoInternalCitation in
	// collector_test.go).
	UnknownReason string
}

// ReadinessInputs is every term [LayerReady] needs, named per
// TRACK-D-ADAPTER-SPEC.md §3.7's own pseudocode. Every field is required —
// there is no "some terms omitted" shape, because a term this package
// cannot currently read still has to be represented, as Known=false with a
// reason, never left as a Go zero value that would silently read as
// Known=false/HeldTrue=false ("bypassed") when the caller simply forgot to
// set it.
type ReadinessInputs struct {
	LayerBypassed       ReadinessTermInput
	LayerMaster         ReadinessTermInput
	LayerVideoOpacity   ReadinessTermInput
	GroupBypassed       ReadinessTermInput
	GroupMaster         ReadinessTermInput
	CompositionBypassed ReadinessTermInput
	CompositionMaster   ReadinessTermInput
}

// byTerm returns in's seven inputs as a term-keyed slice, in
// readinessTermOrder, pairing each with its ReadinessTerm name. Kept as an
// unexported helper rather than inlined into LayerReady so the fixed
// ordering lives in exactly one place (readinessTermOrder) rather than
// being re-typed as a literal struct-field walk.
func (in ReadinessInputs) byTerm() [7]struct {
	term  ReadinessTerm
	input ReadinessTermInput
} {
	return [7]struct {
		term  ReadinessTerm
		input ReadinessTermInput
	}{
		{ReadinessTermLayerBypassed, in.LayerBypassed},
		{ReadinessTermLayerMaster, in.LayerMaster},
		{ReadinessTermLayerVideoOpacity, in.LayerVideoOpacity},
		{ReadinessTermGroupBypassed, in.GroupBypassed},
		{ReadinessTermGroupMaster, in.GroupMaster},
		{ReadinessTermCompositionBypassed, in.CompositionBypassed},
		{ReadinessTermCompositionMaster, in.CompositionMaster},
	}
}

// ReadinessState is the three-valued outcome of [LayerReady]. Never
// rendered as a bool anywhere downstream — see this package's signals.go
// for how it becomes an [observation.Observation].
type ReadinessState string

const (
	ReadinessReady    ReadinessState = "ready"
	ReadinessNotReady ReadinessState = "not_ready"
	ReadinessUnknown  ReadinessState = "unknown"
)

// Readiness is [LayerReady]'s result.
type Readiness struct {
	State ReadinessState

	// FailingTerms is non-empty only when State == ReadinessNotReady: every
	// term that was Known and NOT HeldTrue, in readinessTermOrder. Kleene
	// AND means a not-ready verdict can be reached by ANY ONE known-false
	// term regardless of what the others are — FailingTerms names every one
	// that was actually false, not only the first, because an operator
	// fixing "layer 7 is bypassed" only to find it still not-ready because
	// master is also 0 is the exact "which term failed" ambiguity
	// TRACK-D-ADAPTER-SPEC.md §3.7 exists to remove.
	FailingTerms []ReadinessTerm

	// UnknownTerms is non-empty only when State == ReadinessUnknown: every
	// term that was !Known, in readinessTermOrder, paired 1:1 with
	// UnknownReasons.
	UnknownTerms   []ReadinessTerm
	UnknownReasons []string

	// ObservedAt is the freshness of THIS VERDICT, derived from only the
	// term(s) that actually determined it — never from every term supplied,
	// because a term Kleene AND never needed to consult contributes nothing
	// to how fresh the verdict is:
	//   - ReadinessNotReady: the OLDEST ObservedAt among FailingTerms only.
	//     One known-false term is sufficient proof of not-ready on its own;
	//     the freshness of terms that were not what made the verdict false
	//     is irrelevant to it.
	//   - ReadinessReady: the OLDEST ObservedAt among all seven terms. Every
	//     term had to be Known and HeldTrue, so the verdict is only as
	//     fresh as the least-recently-read piece of confirming evidence.
	//   - ReadinessUnknown: the zero time. There is no confirming or
	//     disconfirming evidence this verdict is built from — only absence
	//     — so there is nothing for a caller to treat as "when this was
	//     true." Callers must not default this to time.Now(); see
	//     pkg/observation's own doc comment on exactly this mistake.
	ObservedAt time.Time
}

// LayerReady is TRACK-D-ADAPTER-SPEC.md §3.7's conjunction, and the ONLY
// function in this codebase permitted to compute it (this file's own doc
// comment). It implements Kleene K3 AND over the seven terms in in:
//
//  1. Any Known term that is NOT HeldTrue -> ReadinessNotReady, naming
//     every such term. This branch is checked FIRST and wins regardless of
//     how many terms are Known=false elsewhere in the input — a definite
//     failure is never demoted to "unknown" by an unrelated term this seam
//     could not read (TRACK-D-D2-SPEC.md §4: "rung 2 must not report
//     ready" — it must also never turn a definite not-ready into unknown).
//  2. Otherwise, any !Known term -> ReadinessUnknown, naming every such
//     term.
//  3. Otherwise (all seven Known and HeldTrue) -> ReadinessReady.
func LayerReady(in ReadinessInputs) Readiness {
	terms := in.byTerm()

	var failing []ReadinessTerm
	var failingOldest time.Time
	for _, t := range terms {
		if t.input.Known && !t.input.HeldTrue {
			failing = append(failing, t.term)
			if failingOldest.IsZero() || t.input.ObservedAt.Before(failingOldest) {
				failingOldest = t.input.ObservedAt
			}
		}
	}
	if len(failing) > 0 {
		return Readiness{State: ReadinessNotReady, FailingTerms: failing, ObservedAt: failingOldest}
	}

	var unknownTerms []ReadinessTerm
	var unknownReasons []string
	for _, t := range terms {
		if !t.input.Known {
			unknownTerms = append(unknownTerms, t.term)
			unknownReasons = append(unknownReasons, t.input.UnknownReason)
		}
	}
	if len(unknownTerms) > 0 {
		return Readiness{State: ReadinessUnknown, UnknownTerms: unknownTerms, UnknownReasons: unknownReasons}
	}

	var readyOldest time.Time
	for _, t := range terms {
		if readyOldest.IsZero() || t.input.ObservedAt.Before(readyOldest) {
			readyOldest = t.input.ObservedAt
		}
	}
	return Readiness{State: ReadinessReady, ObservedAt: readyOldest}
}

// --- Bridging composition.go's *Field types into ReadinessTermInput -------
//
// These two helpers are plumbing, not a second copy of the conjunction
// rule: they translate ONE leaf's [Presence] into the Known/HeldTrue/
// UnknownReason shape [LayerReady] consumes, for a "this term holds when
// the boolean is false" term (bypassed) or a "this term holds when the
// number is positive" term (master, opacity). Sharing them across all five
// layer/group-level terms is what stops seven hand-rolled null checks from
// quietly drifting apart, the identical reasoning composition.go's own
// presenceFieldValue doc comment gives for itself.

// boolTermHoldsWhenFalse builds a [ReadinessTermInput] for a bypassed-shaped
// term: the term holds (blocks nothing) exactly when the field's boolean
// value is false. fieldLabel names the field for UnknownReason, e.g.
// "layer.bypassed" — operator-facing, so it must never carry a repo path,
// ADR number, or doc section reference.
func boolTermHoldsWhenFalse(f ParamBooleanField, readAt time.Time, fieldLabel string) ReadinessTermInput {
	if v, ok := f.Bool(); ok {
		return ReadinessTermInput{Known: true, HeldTrue: !v, ObservedAt: readAt}
	}
	switch f.Presence {
	case PresenceNull:
		return ReadinessTermInput{UnknownReason: fieldLabel + " was explicitly null in Resolume's response"}
	case PresencePresent:
		// The envelope itself arrived, but its own "value" key did not —
		// capture §17.3's own headline finding: no schema in Arena's
		// specification carries a `required` list, so a value-less
		// envelope is contract-legal. Reported exactly like an unreadable
		// field, never treated as HeldTrue=false (which "bypassed": {} 's
		// bare Go zero value would silently be — see [ParamBooleanField.Bool]'s
		// own doc comment).
		return ReadinessTermInput{UnknownReason: fieldLabel + " answered but its value was absent from Resolume's response"}
	default: // PresenceAbsent
		return ReadinessTermInput{UnknownReason: fieldLabel + " was absent from Resolume's response"}
	}
}

// rangeTermHoldsWhenPositive builds a [ReadinessTermInput] for a
// master/opacity-shaped term: the term holds exactly when the field's
// numeric value is greater than zero. See boolTermHoldsWhenFalse's doc
// comment for fieldLabel's rule.
func rangeTermHoldsWhenPositive(f ParamRangeField, readAt time.Time, fieldLabel string) ReadinessTermInput {
	if v, ok := f.Float(); ok {
		return ReadinessTermInput{Known: true, HeldTrue: v > 0, ObservedAt: readAt}
	}
	switch f.Presence {
	case PresenceNull:
		return ReadinessTermInput{UnknownReason: fieldLabel + " was explicitly null in Resolume's response"}
	case PresencePresent:
		// See boolTermHoldsWhenFalse's identical branch: the envelope
		// answered, but its own "value" key did not.
		return ReadinessTermInput{UnknownReason: fieldLabel + " answered but its value was absent from Resolume's response"}
	default: // PresenceAbsent
		return ReadinessTermInput{UnknownReason: fieldLabel + " was absent from Resolume's response"}
	}
}

// --- Solo (§17.3's own open item) -------------------------------------------

// soloUnknownReason is ApplySoloOverride's fixed, operator-facing text —
// named once so every caller states the identical wording rather than each
// composing a slightly different sentence.
const soloUnknownReason = "another layer or group in this composition has solo enabled; this project has not confirmed how Resolume's own solo interacts with an individual layer's readiness, so this layer's readiness cannot be verified while solo is active elsewhere"

// ApplySoloOverride is this package's honest answer to `solo` (readable on
// both Layer and LayerGroup, per this file's own ReadinessTerm doc
// comment): standard mixer semantics would have a solo ANYWHERE silence
// every non-soloed layer, which would make an otherwise-ready dark layer
// report ready — but Arena's own specification says nothing about the
// mechanism, so this is deliberately NOT folded into [LayerReady]'s seven-
// term Kleene AND as an unproven eighth term. Instead, it is applied to
// [LayerReady]'s ALREADY-COMPUTED result: whenever soloActiveElsewhere is
// true (some layer or group in the composition reports solo=true) and
// thisLayerSoloed is false (neither this layer's own solo nor its
// containing group's is), the verdict is downgraded to ReadinessUnknown —
// UNCONDITIONALLY, even overriding an otherwise-definite ReadinessReady or
// ReadinessNotReady, because this package has no evidence either of those
// claims still holds while an unmodeled solo is active. A verdict that was
// already Unknown keeps its own terms and reasons, with "solo" appended
// rather than discarded, so an operator does not lose a real reason (the
// two composition-level terms being permanently unavailable, a null
// field) to a new one. Never
// promotes a verdict, and never applies when soloActiveElsewhere is false
// — the ordinary, no-solo-anywhere case this composition is in almost all
// of the time.
func ApplySoloOverride(r Readiness, soloActiveElsewhere bool, thisLayerSoloed bool) Readiness {
	if !soloActiveElsewhere || thisLayerSoloed {
		return r
	}
	if r.State == ReadinessUnknown {
		return Readiness{
			State:          ReadinessUnknown,
			UnknownTerms:   append(append([]ReadinessTerm{}, r.UnknownTerms...), ReadinessTermSolo),
			UnknownReasons: append(append([]string{}, r.UnknownReasons...), soloUnknownReason),
		}
	}
	return Readiness{
		State:          ReadinessUnknown,
		UnknownTerms:   []ReadinessTerm{ReadinessTermSolo},
		UnknownReasons: []string{soloUnknownReason},
	}
}
