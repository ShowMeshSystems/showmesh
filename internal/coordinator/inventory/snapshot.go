package inventory

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// NodeView is one node's current inventory: its stored evidence, plus the
// liveness verdict [deriveLiveness] computes from that evidence and the
// time Snapshot was called. ARCHITECTURE section 7.2's reconciliation
// vocabulary (converged/progressing/degraded/unknown/conflicted) is a
// different concept — desired-vs-observed convergence, which this task is
// explicitly out of scope for (see the Step 2 round 2 store task spec) —
// so NodeView deliberately does not reuse or alias it; Liveness has its
// own three-value vocabulary (online/offline/unknown) sized for exactly
// what evidence exists at this step.
type NodeView struct {
	NodeID string

	Hello  *store.HelloRecord
	LWT    *store.LWTRecord
	Health *store.HealthRecord

	FirstSeenAt time.Time
	UpdatedAt   time.Time

	Liveness Liveness
	// LivenessReason is a short, human-readable explanation of Liveness,
	// e.g. for an operator-facing log line or (in a later step) a status
	// API field. It is not machine-parsed anywhere in this codebase today.
	LivenessReason string
}

// Snapshot returns every node the store currently knows about, each with
// its liveness verdict computed against now.
func (m *Manager) Snapshot(ctx context.Context, now time.Time) ([]NodeView, error) {
	records, err := m.store.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("inventory: snapshot: %w", err)
	}

	views := make([]NodeView, len(records))
	for i, rec := range records {
		liveness, reason := deriveLiveness(rec, now)
		views[i] = NodeView{
			NodeID: rec.NodeID,
			Hello:  rec.Hello, LWT: rec.LWT, Health: rec.Health,
			FirstSeenAt: rec.FirstSeenAt, UpdatedAt: rec.UpdatedAt,
			Liveness: liveness, LivenessReason: reason,
		}
	}
	return views, nil
}
