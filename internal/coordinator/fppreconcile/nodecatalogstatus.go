package fppreconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the ONE place that resolves a node's cue-catalog
// acknowledgement status against a currently-required revision — the
// resolution GET /nodes/{nodeId}/cue-catalog already used
// (internal/coordinator/api/cuecatalog.go's
// resolveCueCatalogAcknowledgedFields), extracted here so this package's
// own node-catalog-stale readiness condition and that HTTP route compute
// the identical answer rather than each resolving it a second way. Any
// later per-node readiness resolution reuses it too (see
// docs/build/IDENTIFIER-REGISTER.md's "Playlist readiness conditions"
// section).

// NodeCatalogAckStatus resolves nodeID's persisted cue-catalog
// acknowledgement ([store.Store.GetNodeCueCatalogAck]) against
// currentRevision without performing any write: [v1.CueCatalogStatusNeverAcknowledged]
// when nodeID has never acknowledged anything, [v1.CueCatalogStatusCurrent]
// when the acknowledged revision equals currentRevision, and
// [v1.CueCatalogStatusStale] otherwise — including when currentRevision is
// "" (no active show resolved, or this node holds no catalog obligation),
// since there is then no "current" value for any acknowledgement to
// match. ackRevision and ackAt are both the zero value exactly when
// status is CueCatalogStatusNeverAcknowledged. A non-nil error means a
// genuine store failure, which the caller must not treat as
// never-acknowledged.
func NodeCatalogAckStatus(ctx context.Context, st *store.Store, nodeID, currentRevision string) (status string, ackRevision string, ackAt time.Time, err error) {
	ack, err := st.GetNodeCueCatalogAck(ctx, nodeID)
	if errors.Is(err, store.ErrNodeCueCatalogAckNotFound) {
		return v1.CueCatalogStatusNeverAcknowledged, "", time.Time{}, nil
	}
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("fppreconcile: get node cue-catalog ack %q: %w", nodeID, err)
	}
	status = v1.CueCatalogStatusStale
	if currentRevision != "" && ack.Revision == currentRevision {
		status = v1.CueCatalogStatusCurrent
	}
	return status, ack.Revision, ack.AcknowledgedAt, nil
}
