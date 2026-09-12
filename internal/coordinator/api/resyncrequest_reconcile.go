package api

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file closes the "the node never answers" half of noderesync.go's
// own acceptance criteria: a fresh report resolves an outstanding
// asset.inventory.request row through assetsync.Service.
// TriggerIfResyncIntentPrecedes (see that method's own confirming path),
// but a node that never sends one would otherwise leave its row
// "dispatched" forever, and GET /nodes/{nodeId}/assets would report
// "waiting for the node" long after any operator would call that honest.
// This periodic sweep is [ReconcileStrandedActionInvocations]'s own
// shape, one command family over: list unresolved commands, filter to
// this family's action, and resolve anything too old to still be a live
// in-flight request.
//
// Unlike that sweep, there is no one-shot startup call here: a coordinator
// restart mid-wait is not "stranded" the same way an action invocation is
// (nothing here was ever waiting on a live HTTP request to resolve it —
// see noderesync.go's own NOTHING WAITS contract), so the periodic loop
// alone is sufficient; a row that was already older than
// resyncRequestTimeout when this coordinator started is resolved on this
// loop's first tick same as any other.

// resyncRequestTimeout is how long an asset.inventory.request commands row
// may sit "dispatched" before [runResyncRequestReconciliationLoop]
// resolves it "failed": the node was asked for a fresh inventory and, by
// this deadline, still has not answered with one. Chosen well above the
// LAN round-trip an MQTT publish/report pair actually takes, so it never
// fires against a node that is merely slow, only one that is genuinely
// not answering.
const resyncRequestTimeout = 30 * time.Second

// resyncRequestReconcileInterval mirrors actionInvokeReconcileInterval's
// own 10s precedent.
const resyncRequestReconcileInterval = 10 * time.Second

// RunResyncRequestReconciliationLoop retries the resync-request timeout
// sweep on resyncRequestReconcileInterval until ctx is done. Errors are
// logged and never fatal, matching [RunActionInvokeReconciliationLoop]'s
// own posture.
func RunResyncRequestReconciliationLoop(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) {
	runResyncRequestReconciliationLoop(ctx, deps, now, logger, resyncRequestReconcileInterval)
}

// runResyncRequestReconciliationLoop is [RunResyncRequestReconciliationLoop]
// with its own poll interval injectable, so a test can observe more than
// one pass without waiting resyncRequestReconcileInterval in real time.
func runResyncRequestReconciliationLoop(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := reconcileTimedOutResyncRequests(ctx, deps, now, logger)
			switch {
			case err != nil:
				if logger != nil {
					logger.Warn("api: periodic resync-request reconciliation failed", "error", err)
				}
			case n > 0:
				if logger != nil {
					logger.Warn("api: periodic reconciliation timed out resync requests a node never answered", "count", n)
				}
			}
		}
	}
}

// reconcileTimedOutResyncRequests resolves every asset.inventory.request
// row still "dispatched" resyncRequestTimeout or longer after its own
// DispatchedAt as "failed", naming the timeout as the reason. A row still
// "pending" (its publish outcome not yet recorded) or already resolved
// (confirmed by a fresh report, or already failed by an earlier pass) is
// left untouched. The node's own outstanding re-sync intent
// (assetsync.Service.resyncIntents) is deliberately left in place: a
// report that arrives after this timeout still proves the node's current
// state and still runs the repair, per [assetsync.Service.
// TriggerIfResyncIntentPrecedes]'s own doc comment — timing this row out
// only stops claiming the node is still being asked, it does not withdraw
// the ask.
func reconcileTimedOutResyncRequests(ctx context.Context, deps Dependencies, now func() time.Time, logger *slog.Logger) (resolved int, err error) {
	deps = deps.withDefaults()

	unresolved, err := deps.Commands.ListUnresolvedCommands(ctx)
	if err != nil {
		return 0, fmt.Errorf("api: reconcile timed out resync requests: list unresolved commands: %w", err)
	}

	nowT := now()
	reason := fmt.Sprintf("the node did not report a fresh inventory within %s", resyncRequestTimeout)
	for _, rec := range unresolved {
		if rec.Action != assetsync.ResyncCommandAction || rec.State != "dispatched" {
			continue
		}
		dispatchedAt := rec.CreatedAt
		if rec.DispatchedAt != nil {
			dispatchedAt = *rec.DispatchedAt
		}
		if nowT.Sub(dispatchedAt) < resyncRequestTimeout {
			// Too young: a live, in-flight request the node may still
			// answer within the deadline.
			continue
		}

		if err := deps.Commands.UpdateCommandOutcome(ctx, rec.ID, store.CommandOutcomeUpdate{
			ResolvedAt: &nowT, State: strPtr("failed"),
			OutcomeState: strPtr(mqttproto.OutcomeFailed), OutcomeReason: &reason,
		}); err != nil {
			if logger != nil {
				logger.Warn("api: failed to time out a stranded resync request", "commandId", rec.ID, "nodeId", rec.TargetID, "error", err)
			}
			continue
		}
		resolved++
	}
	return resolved, nil
}
