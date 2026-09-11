package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// POST /nodes/{nodeId}/assets/resync: asks nodeID for a fresh asset
// inventory report, stamped with the authenticated operator as issuer
// (InventoryRequester), and records that this node has an outstanding
// re-sync intent as of now (AssetSyncNudger.RecordResyncIntent). Neither
// call waits for the node: this route answers 202 immediately, with
// acceptance only, PROVIDED the asset.inventory.request publish itself
// reached the wire - a publish that never reached the node is not an
// accepted request at all, and this route answers 503
// (assetResyncPublishFailedProblem) instead, since silently returning 202
// for a request nothing was ever asked to act on is the same
// accepted-looking no-op this route's own acceptance criteria forbid. The
// repair itself (assetsync.Service.RequestNode) runs later, from
// internal/coordinator/inventory's handleAssetInventory, as soon as a live
// report proves fresher than this intent - see that package's
// ResyncIntentTrigger. The outcome is never claimed here; it surfaces
// later on GET /nodes/{nodeId}/assets.
//
// The asset.inventory.request dispatch itself IS recorded as a commands
// row, though, matching every other MQTT command this coordinator issues
// (see cuecatalogdeploy.go/cueactivationdispatch.go's identical
// InsertCommand-before-outcome shape): a caller-visible commandID that
// names nothing, or a publish failure that leaves no trace at all, is the
// same accepted-looking no-op this route's own acceptance criteria forbid,
// moved one step earlier than the manifest-driven repair itself. Nothing
// here waits for the node's own reply - see [InventoryRequester]'s doc
// comment for why AwaitResponse is never the right tool for this action.

// InventoryRequester asks a node to publish a fresh asset inventory report
// now, stamped with issuer, and returns the CommandID assigned to that
// request - populated even when err is non-nil, naming the command that
// was attempted (never delivered), so this route can still record what
// failed. Declared here, at the consumer, for the identical reason
// [RenderPublisher] is (renderdispatch.go): the real implementation is
// *broker.BrokerManager's own RequestNodeInventory method, which already
// satisfies this one-method interface with no adapter needed.
//
// AwaitResponse is never used to confirm this dispatch: the answer this
// request is asking for is the node's own inventory report, published
// RETAINED on its observed topic (mqttproto.ObservedDeliveryPolicy), and
// AwaitResponse deliberately discards a retained delivery (see broker's
// response.go), so it would discard the very report it is waiting for and
// present that as a timeout. This route only ever learns whether the
// PUBLISH itself reached the wire.
type InventoryRequester interface {
	RequestNodeInventory(ctx context.Context, nodeID string, issuer mqttproto.CmdIssuer) (string, error)
}

// ProblemTypeAssetResyncPublishFailed is POST /nodes/{nodeId}/assets/resync's
// own upstream failure: the request was valid and the node is declared,
// but the asset.inventory.request publish itself never reached the wire.
// Its own type, and a 503 rather than a 500, because the fault is the
// broker connection between this coordinator and the node, not a
// coordinator defect — the operator can retry once that connection is
// restored. A publish failure here still writes a "failed" commands row
// (see recordInventoryRequestCommand); this problem's detail names that
// row's id so an operator can find it on GET /nodes/{nodeId}/assets.
const ProblemTypeAssetResyncPublishFailed = problemBaseURI + "asset-resync-publish-failed"

func assetResyncPublishFailedProblem(nodeID, commandID string, err error) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeAssetResyncPublishFailed,
		Title:  "Re-sync request could not be sent",
		Status: http.StatusServiceUnavailable,
		Detail: fmt.Sprintf("node %q could not be asked for a fresh inventory: %v (command %s)", nodeID, err, commandID),
	}
}

func (h *handlers) handlePostResyncNodeAssets(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	nodeID := r.PathValue("nodeId")
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("nodeId is not a syntactically valid node ID: "+err.Error()))
		return
	}
	if !h.nodeDeclared(ctx)(nodeID) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no declared node with id "+strconv.Quote(nodeID)))
		return
	}
	if h.deps.AssetSettings.ContentBaseURL() == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(assetSyncDisabledNote))
		return
	}

	ac := authFromContext(ctx)
	issuerID := ac.result.Principal.ID
	issuerName := ac.result.Principal.Name
	if issuerID == "" {
		issuerID = renderIssuerPrincipalIDMissing
	}

	// The intent is recorded regardless of whether the inventory request
	// itself is actually delivered: a node that never sees this specific
	// push still answers on its own next ordinary report, which is live
	// evidence too and still postdates now, so the repair still runs from
	// it - only later than a delivered push would have.
	h.deps.AssetSyncNudger.RecordResyncIntent(nodeID, now)

	commandID, pubErr := h.deps.InventoryRequester.RequestNodeInventory(ctx, nodeID, mqttproto.CmdIssuer{
		PrincipalID: issuerID, PrincipalName: issuerName,
	})
	if commandID == "" {
		// Defensive: this route still needs an id to name the row it is
		// about to record even against an [InventoryRequester]
		// implementation that reports a publish failure with no id of its
		// own (the unwired-broker default, [noInventoryRequester], does
		// exactly this).
		commandID = uuid.NewString()
	}
	h.recordInventoryRequestCommand(ctx, now, nodeID, commandID, issuerID, issuerName, pubErr)
	if pubErr != nil {
		h.logger.Warn("resync: failed to publish asset.inventory.request", "node_id", nodeID, "command_id", commandID, "error", pubErr)
		writeProblem(w, h.logger, now, assetResyncPublishFailedProblem(nodeID, commandID, pubErr))
		return
	}

	result := v1.ResyncNodeAssetsResult{Node: nodeID, AcceptedAt: formatTime(now), InventoryRequestCommandID: commandID}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.ResyncNodeAssetsResponse{
		ServerTime: formatTime(now),
		Resync:     result,
	})
}

// recordInventoryRequestCommand inserts the commands row for the
// asset.inventory.request identified by commandID, then resolves it
// according to whether the publish itself reached the wire - matching
// cuecatalogdeploy.go/cueactivationdispatch.go's own insert-then-resolve
// shape, narrowed to this action's own two possible outcomes: a publish
// failure resolves the row immediately (state "failed",
// outcome_state "collection_failed", outcome_reason the publish error -
// [observation.StateCollectionFailed] is this codebase's own vocabulary
// for exactly "nothing reached the wire"), never left silently pending; a
// successful publish only marks the row dispatched, since - per this
// route's own NOTHING WAITS contract - nothing here ever learns whether
// the node answered.
//
// Best-effort: a store failure here is logged and otherwise swallowed,
// matching this route's own pre-existing posture toward
// AssetSyncNudger.RecordResyncIntent above it. The repair this whole
// route exists to trigger runs off the node's own next report regardless
// of whether this bookkeeping succeeds.
func (h *handlers) recordInventoryRequestCommand(ctx context.Context, now time.Time, nodeID, commandID, issuerID, issuerName string, pubErr error) {
	rec := store.CommandRecord{
		ID: commandID, IdempotencyKey: commandID, Action: assetsync.ResyncCommandAction,
		TargetKind: assetsync.ResyncCommandTargetKind, TargetID: nodeID,
		IssuerPrincipalID: issuerID, IssuerPrincipalName: issuerName,
		ConfirmationMethod: "evidence", State: "pending",
	}
	if _, err := h.deps.Commands.InsertCommand(ctx, rec); err != nil {
		h.logger.Warn("resync: failed to record asset.inventory.request command", "node_id", nodeID, "command_id", commandID, "error", err)
		return
	}

	if pubErr != nil {
		reason := pubErr.Error()
		if err := h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
			ResolvedAt: &now, State: strPtr("failed"),
			OutcomeState: strPtr(string(observation.StateCollectionFailed)), OutcomeReason: &reason,
		}); err != nil {
			h.logger.Warn("resync: failed to resolve a failed asset.inventory.request command", "node_id", nodeID, "command_id", commandID, "error", err)
		}
		return
	}

	if err := h.deps.Commands.UpdateCommandOutcome(ctx, commandID, store.CommandOutcomeUpdate{
		DispatchedAt: &now, State: strPtr("dispatched"),
	}); err != nil {
		h.logger.Warn("resync: failed to mark an asset.inventory.request command dispatched", "node_id", nodeID, "command_id", commandID, "error", err)
	}
}
