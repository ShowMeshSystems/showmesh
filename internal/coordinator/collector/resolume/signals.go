package resolume

import (
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SignalReachable and SignalProduct are the ONLY two signals this seam
// (D-1) ever produces. Every other signal the adapter specification's
// observation table names — composition identity, deck/layer/column
// counts, layer readiness, active clip, per-clip connected and
// transporttype — belongs to seam D-2 and must NOT be emitted from this
// package's Collector. D-1 is transport-level reachability plus Resolume's
// own version identity; it carries no composition semantics at all (see
// this package's doc comment).
const (
	// SignalReachable is transport-level ONLY — see [Collector.Poll]'s
	// doc comment for what that does and does not mean.
	SignalReachable observation.SignalID = "resolume.reachable"

	// SignalProduct carries [Product.String]'s canonical form.
	SignalProduct observation.SignalID = "resolume.product"
)

// init runs [observation.ValidateSignalID] over both signal constants
// this package declares, so a malformed signal ID fails at package load
// rather than at the first poll — the same discipline
// internal/coordinator/collector/fpp's own init applies to its larger
// signal vocabulary.
func init() {
	for _, sig := range []observation.SignalID{SignalReachable, SignalProduct} {
		if err := observation.ValidateSignalID(sig); err != nil {
			panic(fmt.Sprintf("resolume: invalid signal ID declared by this package: %v", err))
		}
	}
}
