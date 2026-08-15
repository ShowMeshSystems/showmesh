package resolume

import (
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SignalReachable and SignalProduct were D-1's only two signals. Track D
// seam D-2/C adds the rest of TRACK-D-D2-SPEC.md §5's table below —
// composition identity, deck count/selected deck, layer readiness, active
// clip, per-clip connected/transporttype, and composition name (defect 2,
// 2026-08-15: now permanently StateUnsupported — see
// [compositionLevelUnavailableReason]). Poll's own doc comment in collector.go still governs
// which of these ever appear on the steady-state liveness timer: these two
// (the "resolume-rest" source), and only these two.
const (
	// SignalReachable is transport-level ONLY — see [Collector.Poll]'s
	// doc comment for what that does and does not mean.
	SignalReachable observation.SignalID = "resolume.reachable"

	// SignalProduct carries [Product.String]'s canonical form.
	SignalProduct observation.SignalID = "resolume.product"
)

// The survey-only signals below (TRACK-D-D2-SPEC.md §3.1: produced only by
// an explicit refresh or a confirmed reconnect, never by the liveness
// timer) are delivered under a DIFFERENT [observation.Observation].Source
// than SignalReachable/SignalProduct — see collector.go's surveySourceName
// doc comment for why that separation is load-bearing, not cosmetic: it is
// what stops an ordinary liveness poll's complete=true delivery from
// pruning every survey-derived row out of the store on its very next tick.
const (
	// SignalCompositionName is display-only and explicitly not an identity
	// (TRACK-D-D2-SPEC.md §3.8). Permanently StateUnsupported (defect 2,
	// 2026-08-15): no path this package may use can read it, and it is
	// never backfilled from the uploaded composition file — see
	// [Collector.compositionNameObservation].
	SignalCompositionName observation.SignalID = "resolume.composition.name"

	// SignalCompositionIdentified is §6's identity check result, rendered
	// as a descriptive string ("identified", "not_identified: ...",
	// "unknown: ...") rather than a bool — see collector.go's own
	// formatIdentity for why a bare bool cannot carry §6's required detail
	// through pkg/observation's existing Value/Absence shape.
	SignalCompositionIdentified observation.SignalID = "resolume.composition.identified"

	// SignalCompositionDecks is the COUNT of tracked decks that resolved
	// on the most recent survey — see collector.go's own doc comment on
	// why this can only ever be a lower bound on decks actually present in
	// Resolume (D-2 never enumerates decks; it only re-reads the ones the
	// uploaded composition file already named).
	SignalCompositionDecks observation.SignalID = "resolume.composition.decks"

	// SignalCompositionSelectedDeck is NOT in TRACK-D-D2-SPEC.md §5's
	// table as its own row — that table's resolume.composition.decks row
	// says "int... plus the selected deck's name and id", which cannot fit
	// alongside an int64 count in one [observation.Observation].Value (see
	// pkg/observation.Observation's own doc comment: Value is exactly one
	// of bool/string/int64/float64). This signal is where that second half
	// of the same table row actually lives; see this task's own report for
	// why it was split out rather than silently dropped.
	SignalCompositionSelectedDeck observation.SignalID = "resolume.composition.selected_deck"
)

// LayerReadySignal, LayerActiveClipSignal, ClipConnectedSignal, and
// ClipTransportTypeSignal build the per-object signal ids §5's table
// describes as "resolume.layer.<id>.ready" and its siblings. A function
// rather than a naked fmt.Sprintf at every call site is what keeps the
// exact id shape in one place — collector.go never formats one of these by
// hand.
func LayerReadySignal(id ObjectID) observation.SignalID {
	return observation.SignalID(fmt.Sprintf("resolume.layer.%s.ready", id))
}

func LayerActiveClipSignal(id ObjectID) observation.SignalID {
	return observation.SignalID(fmt.Sprintf("resolume.layer.%s.active_clip", id))
}

func ClipConnectedSignal(id ObjectID) observation.SignalID {
	return observation.SignalID(fmt.Sprintf("resolume.clip.%s.connected", id))
}

func ClipTransportTypeSignal(id ObjectID) observation.SignalID {
	return observation.SignalID(fmt.Sprintf("resolume.clip.%s.transporttype", id))
}

// staticSignals is every signal ID this package declares that does NOT
// carry a variable object id — validated once at init, mirroring D-1's own
// original list. The four per-object signal functions above are validated
// per-call instead (collector.go does so once per survey, on first use of
// each), since [observation.ValidateSignalID]'s rule (lowercase ASCII
// letters/digits/underscores per dot-segment) accepts any base-10
// [ObjectID] and this package has no fixed set of object ids to enumerate
// here.
var staticSignals = []observation.SignalID{
	SignalReachable,
	SignalProduct,
	SignalCompositionName,
	SignalCompositionIdentified,
	SignalCompositionDecks,
	SignalCompositionSelectedDeck,
}

// init runs [observation.ValidateSignalID] over every static signal
// constant this package declares, so a malformed signal ID fails at
// package load rather than at the first poll — the same discipline
// internal/coordinator/collector/fpp's own init applies to its larger
// signal vocabulary.
func init() {
	for _, sig := range staticSignals {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic(fmt.Sprintf("resolume: invalid signal ID declared by this package: %v", err))
		}
	}
}
