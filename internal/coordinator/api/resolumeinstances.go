package api

import (
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file is Track D seam E: Resolume as a first-class observability
// resource. GET /resolume/instances and GET /resolume/instances/{instanceId}
// render exactly what D-2's collector already persisted plus D-2a's stored
// composition — no request to Arena is ever made serving either route.
// "instances" is an explicit path segment rather than a bare {id} because
// /resolume/actions already exists and /resolume/recovery is being added in
// parallel by another seam; see this route's registration comment in api.go.

// handleResolumeInstances serves GET /api/v1/resolume/instances. An
// unconfigured coordinator (h.deps.Resolume has nothing to list) answers 200
// with an empty array — a fact about the deployment, never a 404 or an
// error.
func (h *handlers) handleResolumeInstances(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	views, err := h.deps.Resolume.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list resolume instances", err)
		return
	}
	composition, err := resolumeInstanceComposition(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "get resolume composition", err)
		return
	}

	instances := make([]v1.ResolumeInstance, 0, len(views))
	for _, rv := range views {
		instances = append(instances, mapResolumeInstance(rv, composition, now))
	}
	jsonWrite(w, v1.ResolumeInstancesResponse{ServerTime: formatTime(now), Instances: instances})
}

// handleResolumeInstance serves GET /api/v1/resolume/instances/{instanceId}.
// Unlike the list route, no match here — including on an unconfigured
// coordinator — is the ordinary resource-not-found shape (Track D seam E
// spec section 2.2 rule 4), matching [handlers.handleFPPInstance]'s
// identical posture for a bare, single-resource GET.
func (h *handlers) handleResolumeInstance(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	instanceID := r.PathValue("instanceId")
	ctx := r.Context()

	views, err := h.deps.Resolume.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list resolume instances", err)
		return
	}
	for _, rv := range views {
		if rv.InstanceID != instanceID {
			continue
		}
		composition, err := resolumeInstanceComposition(ctx, h.deps.Config)
		if err != nil {
			h.writeInternalError(w, now, "get resolume composition", err)
			return
		}
		jsonWrite(w, v1.ResolumeInstanceResponse{ServerTime: formatTime(now), Instance: mapResolumeInstance(rv, composition, now)})
		return
	}
	writeProblem(w, h.logger, now, resourceNotFoundProblem("no Resolume instance with id "+strconv.Quote(instanceID)+" is configured"))
}
