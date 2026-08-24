package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fppInstanceUUIDNoUnacknowledgedChangeProblem is the 409 returned when an
// operator asks to acknowledge a uuid change that either never existed or
// was already cleared. Shares [ProblemTypeConflict] with every other
// "the request is syntactically fine but this coordinator's current state
// makes it meaningless right now" case in this package (see
// fppEndpointsEnvVarSetProblem's own doc comment for that convention).
func fppInstanceUUIDNoUnacknowledgedChangeProblem(instanceID string) v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "No pending FPP instance uuid change to acknowledge",
		Status: http.StatusConflict,
		Detail: "FPP instance " + strconv.Quote(instanceID) + " has no unacknowledged instance uuid change. " +
			"Either it has never reported a different uuid than its current one, or a prior acknowledgment " +
			"already cleared it.",
	}
}

// --- POST /api/v1/fpp/{instanceId}/instance-uuid/acknowledge ---

// handleAcknowledgeFPPInstanceUUIDChange serves
// POST /api/v1/fpp/{instanceId}/instance-uuid/acknowledge, behind
// [handlers.writeGuard](&scopeConfigWrite, ...), the same scope
// POST/DELETE /nodes/{nodeId}/declaration use, because clearing this
// conflict is the identical class of decision: an operator-authored
// statement about this installation's inventory, not a command sent to
// any device. This is the changed-uuid rule's ONLY way to clear a pending
// unacknowledged uuid change (store/fppinstanceuuid.go's own doc comment)
// , it never happens automatically, and it never changes the recorded
// uuid itself, only the conflict marker.
//
// A coordinator-local state change, so ADR-024 decision 11's
// same-transaction rule applies: the acknowledgment and its audit entry
// land in one transaction via [identity.Service.AuditedWrite], or neither
// does, mirroring [handlers.handlePromoteNode]'s identical posture.
func (h *handlers) handleAcknowledgeFPPInstanceUUIDChange(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	instanceID := r.PathValue("instanceId")
	if err := mqttproto.ValidateNodeID(instanceID); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("instanceId is not a syntactically valid instance ID: "+err.Error()))
		return
	}

	err := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		out, acknowledgedPreviousUUID, err := tx.AcknowledgeFPPInstanceUUIDChange(ctx, instanceID, ac.result.Principal.ID, ac.result.Principal.Name)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "fpp.instance_uuid.acknowledge", Target: instanceID,
			Params: map[string]any{"uuid": out.UUID, "previousUuid": acknowledgedPreviousUUID},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if err != nil {
		if errors.Is(err, store.ErrFPPInstanceUUIDNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem("no FPP instance with id "+strconv.Quote(instanceID)+" has ever reported an instance uuid"))
			return
		}
		if errors.Is(err, store.ErrFPPInstanceUUIDNoUnacknowledgedChange) {
			writeProblem(w, h.logger, now, fppInstanceUUIDNoUnacknowledgedChangeProblem(instanceID))
			return
		}
		h.writeInternalError(w, now, "acknowledge fpp instance uuid change", err)
		return
	}

	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances after acknowledge", err)
		return
	}
	for _, fv := range views {
		if fv.InstanceID == instanceID {
			instance := mapFPPInstance(fv, now)
			jsonWrite(w, v1.AcknowledgeFPPInstanceUUIDChangeResponse{ServerTime: formatTime(now), Instance: &instance})
			return
		}
	}
	// The acknowledgment itself succeeded and committed (instanceID had a
	// row in fpp_instance_uuid_observations, the write and its audit
	// entry are already durable), but instanceID is no longer among the
	// currently configured fpp.endpoints, an operator removed the
	// endpoint between the write above and this read. Reporting a 404
	// here would contradict the commit that already happened, so this
	// still reports 200 with Instance null: there is no current
	// FPPInstance view left to render, but the acknowledgment is not
	// undone.
	jsonWrite(w, v1.AcknowledgeFPPInstanceUUIDChangeResponse{ServerTime: formatTime(now), Instance: nil})
}
