package api

import (
	"context"
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

// This file is Track B seam B2c's HTTP surface (ADR-039,
// TRACK-B-BUILD-CONTRACT.md §"render.settings"): GET/PUT
// /api/v1/config/render.settings and its revisions list. Mirrors
// resolumerecovery.go's config.go-shaped GET/PUT/revisions handlers
// exactly, narrowed to a payload with a well-defined default so GET never
// 404s. Reads and writes are both gated by config:write, matching
// fpp.endpoints' and resolume.recovery's own always-sensitive posture
// (config.go's own doc comment: this is nearer principal management than
// node/FPP telemetry).

const maxRenderSettingsConfigRequestBodyBytes = 4 * 1024

// resolveRenderSettings reads the render.settings configuration kind's
// current value: the stored payload, and whether a revision has ever
// actually been written ("configured"). The default when nothing has ever
// been written is [config.RenderSettingsDefaultPayload], reported with
// configured=false — mirrors ResolveResolumeRecoveryToggle's identical
// shape and its "one function, called by every reader" reasoning.
func resolveRenderSettings(ctx context.Context, cs ConfigStore) (payload config.RenderSettingsPayload, configured bool, err error) {
	obj, err := cs.GetConfigObject(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.RenderSettingsDefaultPayload, false, nil
	case err != nil:
		return config.RenderSettingsPayload{}, false, fmt.Errorf("api: get render.settings config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.RenderSettingsDefaultPayload, false, nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.RenderSettingsPayload{}, false, fmt.Errorf("api: get render.settings config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeRenderSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		// A stored row this package never wrote in this shape is a
		// store-integrity error, not a validation outcome to recover from —
		// every writer of this kind goes through EncodeRenderSettingsPayload
		// after DecodeRenderSettingsPayload already accepted it.
		return config.RenderSettingsPayload{}, false, fmt.Errorf("api: decode render.settings payload: %s", verr.Error())
	}
	return payload, true, nil
}

// handleGetRenderSettingsConfig serves GET /api/v1/config/render.settings:
// revision metadata behind config:write. "Nothing has ever been written" is
// never a 404 here — the payload has a well-defined default — so this
// always answers 200, reporting whether the current value is the default or
// a stored choice via Source/Revision.
func (h *handlers) handleGetRenderSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, configured, err := resolveRenderSettings(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve render.settings", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.RenderSettingsConfigResponse{
			ServerTime: formatTime(now), Kind: config.RenderSettingsConfigKind,
			Revision: 0, Payload: mapRenderSettingsPayload(payload),
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get render.settings config object", err)
		return
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active render.settings config revision", err)
		return
	}
	jsonWrite(w, mapRenderSettingsConfigResponse(now, rev, obj, payload))
}

// handleGetRenderSettingsConfigRevisions serves
// GET /api/v1/config/render.settings/revisions: every revision's metadata,
// newest first. Mirrors handleGetResolumeRecoveryConfigRevisions exactly.
func (h *handlers) handleGetRenderSettingsConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get render.settings config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list render.settings config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.RenderSettingsConfigKind, Revisions: out})
}

// handlePutRenderSettingsConfig serves PUT /api/v1/config/render.settings:
// validates (a full replacement — every field required, per
// config.DecodeRenderSettingsPayload's own doc comment), appends an
// immutable revision, and activates it in the SAME transaction as its audit
// log entry (ADR-024 decision 11). ADR-009's "rejected before activation"
// rule holds structurally: decoding/validation can fail before
// [identity.Service.AuditedWrite] is ever called, so a rejected write opens
// no transaction and leaves no revision behind.
func (h *handlers) handlePutRenderSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRenderSettingsConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxRenderSettingsConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeRenderSettingsPayload(string(body))
	if verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Error()))
		return
	}

	payloadJSON, err := config.EncodeRenderSettingsPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode render.settings config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.RenderSettingsConfigKind, ObjectID: config.RenderSettingsConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.RenderSettingsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.RenderSettingsConfigKind, config.RenderSettingsConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.RenderSettingsConfigKind,
			Params: map[string]any{
				"revision":   nextRevisionNo,
				"idleOutput": payload.IdleOutput,
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write render.settings config revision", writeErr)
		return
	}

	jsonWrite(w, mapRenderSettingsConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.RenderSettingsConfigKind, ID: config.RenderSettingsConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func mapRenderSettingsPayload(p config.RenderSettingsPayload) v1.ConfigRenderSettingsPayload {
	return v1.ConfigRenderSettingsPayload{
		IdleOutput: p.IdleOutput,
		RestartPolicy: v1.ConfigRenderRestartPolicy{
			InitialDelaySeconds:        p.RestartPolicy.InitialDelaySeconds,
			MaxDelaySeconds:            p.RestartPolicy.MaxDelaySeconds,
			MaxConsecutiveFastFailures: p.RestartPolicy.MaxConsecutiveFastFailures,
		},
	}
}

func mapRenderSettingsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.RenderSettingsPayload) v1.RenderSettingsConfigResponse {
	return v1.RenderSettingsConfigResponse{
		ServerTime: formatTime(now), Kind: config.RenderSettingsConfigKind, Revision: rev.Revision,
		Payload:                mapRenderSettingsPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
