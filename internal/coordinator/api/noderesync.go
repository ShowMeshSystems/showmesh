package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// POST /nodes/{nodeId}/assets/resync: asks nodeID for a fresh asset
// inventory report, stamped with the authenticated operator as issuer
// (InventoryRequester), and records that this node has an outstanding
// re-sync intent as of now (AssetSyncNudger.RecordResyncIntent). Neither
// call waits for the node: this route answers 202 immediately, with
// acceptance only. The repair itself (assetsync.Service.RequestNode) runs
// later, from internal/coordinator/inventory's handleAssetInventory, as
// soon as a live report proves fresher than this intent - see that
// package's ResyncIntentTrigger. The outcome is never claimed here; it
// surfaces later on GET /nodes/{nodeId}/assets.

// InventoryRequester asks a node to publish a fresh asset inventory report
// now, stamped with issuer, and returns the CommandID assigned to that
// request. Declared here, at the consumer, for the identical reason
// [RenderPublisher] is (renderdispatch.go): the real implementation is
// *broker.BrokerManager's own RequestNodeInventory method, which already
// satisfies this one-method interface with no adapter needed.
type InventoryRequester interface {
	RequestNodeInventory(ctx context.Context, nodeID string, issuer mqttproto.CmdIssuer) (string, error)
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

	commandID, err := h.deps.InventoryRequester.RequestNodeInventory(ctx, nodeID, mqttproto.CmdIssuer{
		PrincipalID: issuerID, PrincipalName: issuerName,
	})
	if err != nil {
		h.logger.Warn("resync: failed to publish asset.inventory.request", "node_id", nodeID, "error", err)
	}

	result := v1.ResyncNodeAssetsResult{Node: nodeID, AcceptedAt: formatTime(now)}
	if err == nil {
		result.InventoryRequestCommandID = commandID
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.ResyncNodeAssetsResponse{
		ServerTime: formatTime(now),
		Resync:     result,
	})
}
