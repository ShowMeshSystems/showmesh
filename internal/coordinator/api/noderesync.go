package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the operator-invoked resync route's own HTTP surface: POST
// /nodes/{nodeId}/assets/resync asks the existing asset-sync service
// (internal/coordinator/assetsync.Service) to run its own gap-driven tick
// now, instead of waiting out its own sync interval, for this node among
// every declared node. It dispatches no MQTT command of its own: [Service.
// Nudge], reached here through [Dependencies.AssetSyncNudger], is the SAME
// hook the asset-upload handler already uses to run the identical tick
// early. See assetsync/doc.go's own doc comment: this package already
// reserves this exact shape for "any future manifest API handler".
//
// Answers 202, never 200: accepted, never confirmed by anything downstream
// at this layer, mirroring dispatchNightCommand's identical posture
// (nightsessioncontrol.go). An outcome comes only from the node's own next
// asset report and the existing FetchConfirmed rule (assetsync/sync.go),
// surfaced later on GET /nodes/{nodeId}/assets, never from this response.

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

	h.deps.AssetSyncNudger.Nudge()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v1.ResyncNodeAssetsResponse{
		ServerTime: formatTime(now),
		Resync:     v1.ResyncNodeAssetsResult{Node: nodeID, AcceptedAt: formatTime(now)},
	})
}
