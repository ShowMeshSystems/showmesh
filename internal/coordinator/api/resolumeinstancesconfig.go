package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track G seam G-2 (ADR-039): the resolume.instances
// configuration write surface, mirroring config.go's fpp.endpoints shape
// exactly — same singleton object id, same absent/null/empty payload
// discipline, same still-set-env-var 409, same fail-closed-on-audit write.
// The one structural difference is the schema's own limit: at most one
// instance, enforced by [config.ValidateResolumeInstances], never by the
// wire shape — see that function's own doc comment.
//
// Reads here are gated identically to fpp.endpoints: config:write, never
// opened by [Options.CloseReads] — see config.go's own top comment for the
// full reasoning, which applies unchanged to this kind.

const maxResolumeInstancesConfigRequestBodyBytes = 16 * 1024

// currentResolumeInstanceIDs reads whatever Resolume instance id(s) this
// coordinator currently has configured, LIVE at call time, via
// [Dependencies.Resolume] rather than by re-reading the config store here —
// mirroring showconfig.go's [currentFPPEndpoints], which derives its own
// live FPP endpoint list from [Dependencies.FPP] for the identical reason:
// the wiring layer's resolumeManager (internal/coordinator) is already the
// single source of truth for "what is configured right now", including a
// resolume.instances change that landed after this coordinator started
// (ADR-036 applies to this kind from the start — Track G seam G-2).
func currentResolumeInstanceIDs(ctx context.Context, rl ResolumeLister) ([]string, error) {
	views, err := rl.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.InstanceID)
	}
	return out, nil
}

// handleGetResolumeInstancesConfig serves GET
// /api/v1/config/resolume.instances: the active revision and its decoded
// payload. 404 when no revision has ever been activated, mirroring
// handleGetFPPEndpointsConfig's identical "not configured yet" vs.
// "configured with zero instances" distinction.
func (h *handlers) handleGetResolumeInstancesConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		if h.deps.ResolumeInstancesMigrationDeferred {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no resolume.instances configuration is stored, but this coordinator IS using the instance named by "+
					"SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID: the startup migration of those variables into this store "+
					"could not be persisted on this boot, and was deferred rather than refusing to start. GET "+
					"/api/v1/resolume/instances lists the instance actually in effect. Nothing was written, so nothing "+
					"here is stale or half-applied. Check this coordinator's startup log for the failure, fix the data "+
					"volume (usually full, read-only, or a damaged database), and restart: the migration is retried on "+
					"every start. Do NOT remove SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID until it has succeeded — while "+
					"the migration is deferred those variables are the only copy of this configuration."))
			return
		}
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no resolume.instances configuration has been created yet; PUT one to create it"))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get resolume.instances config object", err)
		return
	}
	if obj.CurrentRevision == 0 {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no resolume.instances configuration has been created yet; PUT one to create it"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active resolume.instances config revision", err)
		return
	}

	instances, err := config.DecodeResolumeInstancesPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode resolume.instances config payload", err)
		return
	}

	jsonWrite(w, mapResolumeInstancesConfigResponse(now, rev, obj, instances))
}

// handleGetResolumeInstancesConfigRevisions serves GET
// /api/v1/config/resolume.instances/revisions: every revision's metadata,
// newest first — a 200 with an empty list when nothing has ever been
// created, mirroring handleGetFPPEndpointsConfigRevisions.
func (h *handlers) handleGetResolumeInstancesConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get resolume.instances config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list resolume.instances config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}

	jsonWrite(w, v1.ConfigRevisionsResponse{
		ServerTime: formatTime(now),
		Kind:       config.ResolumeInstancesConfigKind,
		Revisions:  out,
	})
}

// decodeResolumeInstancesConfigPutBody implements PUT
// /api/v1/config/resolume.instances' request-body contract, mirroring
// decodeFPPEndpointsConfigPutBody's identical absent/null/present decode —
// see that function's own doc comment for why a bare struct field cannot
// tell those three cases apart.
func decodeResolumeInstancesConfigPutBody(body io.Reader) ([]config.ResolumeInstance, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return nil, fmt.Errorf(`request body must be a JSON object matching {"instances":[{"id":string,"url":string},...]}: %w`, err)
	}

	for key := range top {
		if key != "instances" {
			return nil, fmt.Errorf(`unknown field %q; the only accepted top-level field is "instances"`, key)
		}
	}

	instancesRaw, present := top["instances"]
	if !present {
		return nil, errors.New(`"instances" is required and was absent; pass an empty array ("instances": []) to deliberately configure zero instances`)
	}
	if bytes.Equal(bytes.TrimSpace(instancesRaw), []byte("null")) {
		return nil, errors.New(`"instances" must not be null; pass an empty array ("instances": []) to deliberately configure zero instances`)
	}

	var wire []v1.ConfigResolumeInstance
	if err := json.Unmarshal(instancesRaw, &wire); err != nil {
		return nil, fmt.Errorf(`"instances" must be an array of {"id":string,"url":string}: %w`, err)
	}

	instances := make([]config.ResolumeInstance, 0, len(wire))
	for _, e := range wire {
		instances = append(instances, config.ResolumeInstance{ID: e.ID, URL: e.URL})
	}
	return instances, nil
}

// handlePutResolumeInstancesConfig serves PUT
// /api/v1/config/resolume.instances: validates, appends an immutable
// revision, activates it, and returns the new active revision — mirroring
// handlePutFPPEndpointsConfig's structure and its three pre-transaction
// refusals:
//
//   - While SHOWMESH_RESOLUME_URL is still set in this process's
//     environment, refused with 409 (ADR-039 decision 4): a write accepted
//     now cannot survive this coordinator's own next restart, so the
//     refusal belongs at the moment of the mistake.
//   - The proposed list is validated ([config.ValidateResolumeInstances]):
//     shape, the at-most-one-instance limit, and a collision against the
//     CURRENT fpp.endpoints configuration, read live via
//     [currentFPPEndpoints] rather than a value cached at startup — the
//     identical "must not silently stop working once the other side of the
//     comparison is itself store-backed" requirement ADR-039 decision 6
//     states for this kind's own no-restart apply, applied to the
//     cross-check rather than to dispatch.
//
// ADR-009 requires invalid configuration be rejected before activation, and
// as with handlePutFPPEndpointsConfig, nothing below this point runs until
// every refusal above has already passed.
func (h *handlers) handlePutResolumeInstancesConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	if h.deps.ResolumeInstancesEnvVarSet {
		if h.deps.ResolumeInstancesMigrationDeferred {
			writeProblem(w, h.logger, now, resolumeInstancesMigrationDeferredProblem())
			return
		}
		writeProblem(w, h.logger, now, resolumeInstancesEnvVarSetProblem())
		return
	}

	instances, err := decodeResolumeInstancesConfigPutBody(io.LimitReader(r.Body, maxResolumeInstancesConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	fppEndpoints, err := currentFPPEndpoints(ctx, h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "get current fpp.endpoints for resolume.instances collision check", err)
		return
	}
	if err := config.ValidateResolumeInstances(instances, fppEndpoints); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	payloadJSON, err := config.EncodeResolumeInstancesPayload(instances)
	if err != nil {
		h.writeInternalError(w, now, "encode resolume.instances config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:                   config.ResolumeInstancesConfigKind,
			ObjectID:               config.ResolumeInstancesConfigObjectID,
			Revision:               nextRevisionNo,
			PayloadJSON:            payloadJSON,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
			Source:                 config.ResolumeInstancesSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}

		if _, aerr := tx.ActivateConfigRevision(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}

		activated = rec

		return identity.AuditEntry{
			Timestamp:     now,
			PrincipalID:   ac.result.Principal.ID,
			PrincipalName: ac.result.Principal.Name,
			Form:          ac.result.Form,
			CredentialID:  ac.result.CredentialID,
			ClientAddr:    h.clientAddr(r),
			Action:        "config.write",
			Target:        config.ResolumeInstancesConfigKind,
			Params: map[string]any{
				"revision":      nextRevisionNo,
				"instanceCount": len(instances),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write resolume.instances config revision", writeErr)
		return
	}

	jsonWrite(w, mapResolumeInstancesConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ResolumeInstancesConfigKind, ID: config.ResolumeInstancesConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, instances))
}

// resolumeInstancesRestartRequiredReason mirrors restartRequiredReason
// (config.go): the collector set follows this configuration within about
// ten seconds, no restart needed — Track G seam G-2 applies ADR-036 to this
// kind from the start rather than shipping the restart-snapshot defect
// fpp.endpoints had to be corrected out of.
const resolumeInstancesRestartRequiredReason = "this change is already in effect: the Resolume collector set follows " +
	"this configuration within about ten seconds. No restart is needed."

func mapResolumeInstancesConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, instances []config.ResolumeInstance) v1.ResolumeInstancesConfigResponse {
	payload := v1.ConfigResolumeInstancesPayload{Instances: make([]v1.ConfigResolumeInstance, 0, len(instances))}
	for _, inst := range instances {
		payload.Instances = append(payload.Instances, v1.ConfigResolumeInstance{ID: inst.ID, URL: inst.URL})
	}
	return v1.ResolumeInstancesConfigResponse{
		ServerTime:             formatTime(now),
		Kind:                   config.ResolumeInstancesConfigKind,
		Revision:               rev.Revision,
		Payload:                payload,
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
		RestartRequired:        false,
		RestartRequiredReason:  resolumeInstancesRestartRequiredReason,
	}
}
