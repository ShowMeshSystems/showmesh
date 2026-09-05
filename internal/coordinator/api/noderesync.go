package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// POST /nodes/{nodeId}/assets/resync: queues this one node for the
// existing asset-sync service's next pass (assetsync.Service.RequestNode)
// and answers 202 with acceptance only. The outcome is never claimed here;
// it surfaces later on GET /nodes/{nodeId}/assets.

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

	h.deps.AssetSyncNudger.RequestNode(nodeID)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.ResyncNodeAssetsResponse{
		ServerTime: formatTime(now),
		Resync:     v1.ResyncNodeAssetsResult{Node: nodeID, AcceptedAt: formatTime(now)},
	})
}
