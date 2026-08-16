package assetsync

import (
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
)

// maxInFlightPerNode and maxInFlightTotal bound how many dispatched-but-
// unconfirmed asset.fetch commands [Service] tracks at once, so a newly
// activated show's upload does not become a fleet-wide stampede (§5.1).
const (
	maxInFlightPerNode = 2
	maxInFlightTotal   = 8
)

// inFlightExpiry is how long [Service] keeps counting a dispatched
// asset.fetch against the concurrency budget before treating it as
// abandoned and letting a later tick redispatch it. Derived from
// [assetstore.UploadBudget] — the SAME size-derived transfer-time budget
// the HTTP upload/fetch path uses — rather than a second, independently
// chosen timeout for the identical kind of transfer (§6's "two timeouts on
// opposite sides of one contract are a single decision", applied here to
// two SIDES of the same fetch rather than two ends of one HTTP request).
func inFlightExpiry(sizeBytes int64) time.Duration {
	return assetstore.UploadBudget(sizeBytes)
}
