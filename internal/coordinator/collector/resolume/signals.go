package resolume

import (
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SignalReachable and SignalProduct are the only two signals the steady-state
// liveness timer ever produces (the "resolume-rest" source) — see
// [Collector.Poll]. Everything below them is survey-only.
const (
	// SignalReachable is transport-level ONLY — see [Collector.Poll]'s
	// doc comment for what that does and does not mean.
	SignalReachable observation.SignalID = "resolume.reachable"

	// SignalProduct carries [Product.String]'s canonical form.
	SignalProduct observation.SignalID = "resolume.product"
)

// The survey-only signals below carry a DIFFERENT [observation.Observation].Source
// than the two above — see collector.go's surveySourceName: that separation is
// what stops an ordinary liveness poll's complete=true delivery from pruning
// every survey-derived row out of the store on its next tick.
const (
	// SignalCompositionName is display-only and explicitly not an identity.
	// Permanently StateUnsupported: no path this package may use can read
	// it, and it is never backfilled from the uploaded composition file.
	SignalCompositionName observation.SignalID = "resolume.composition.name"

	// SignalCompositionIdentified is §6's identity check result, a
	// descriptive string rather than a bool — a bare bool cannot carry §6's
	// required detail through pkg/observation's Value/Absence shape.
	SignalCompositionIdentified observation.SignalID = "resolume.composition.identified"

	// SignalCompositionDecks is the COUNT of tracked decks that resolved on
	// the most recent survey — a lower bound on decks actually present, since
	// D-2 only ever re-reads the ones the uploaded file named.
	SignalCompositionDecks observation.SignalID = "resolume.composition.decks"

	// SignalCompositionSelectedDeck carries the second half of §5's
	// resolume.composition.decks row ("plus the selected deck's name and
	// id"), which cannot share one [observation.Observation].Value with an
	// int64 count.
	SignalCompositionSelectedDeck observation.SignalID = "resolume.composition.selected_deck"
)

// These four build §5's per-object signal ids ("resolume.layer.<id>.ready"
// and siblings). Functions rather than a naked fmt.Sprintf at every call site
// keep the exact id shape in one place.
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

// staticSignals is every signal ID this package declares that does NOT carry
// a variable object id, validated once at init. The four per-object functions
// above are validated per-call instead, in collector.go.
var staticSignals = []observation.SignalID{
	SignalReachable,
	SignalProduct,
	SignalCompositionName,
	SignalCompositionIdentified,
	SignalCompositionDecks,
	SignalCompositionSelectedDeck,
}

// init validates every static signal constant so a malformed signal ID fails
// at package load rather than at the first poll.
func init() {
	for _, sig := range staticSignals {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic(fmt.Sprintf("resolume: invalid signal ID declared by this package: %v", err))
		}
	}
}
