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
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppconnectpush"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the HTTP surface for the "fppconnect.settings" singleton
// (ADR-039, ADR-044 decision 5): GET/PUT /api/v1/config/fppconnect.settings
// and its revisions list. Mirrors audiosettings.go's GET/PUT/revisions
// handlers exactly, narrowed to a payload with a well-defined default so
// GET never 404s. Reads and writes are both gated by config:write, matching
// every other always-sensitive configuration kind in this package.

const maxFPPConnectSettingsConfigRequestBodyBytes = 4 * 1024

// fppConnectConfigPushTimeout bounds a single node's best-effort
// fppconnect.configure push, matching audioConfigPushTimeout's identical
// reasoning one kind over.
const fppConnectConfigPushTimeout = 5 * time.Second

// resolveFPPConnectSettings reads the fppconnect.settings configuration
// kind's current value, mirroring resolveAudioSettings exactly.
func resolveFPPConnectSettings(ctx context.Context, cs ConfigStore) (payload config.FPPConnectSettingsPayload, configured bool, err error) {
	obj, err := cs.GetConfigObject(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.FPPConnectSettingsDefaultPayload, false, nil
	case err != nil:
		return config.FPPConnectSettingsPayload{}, false, fmt.Errorf("api: get fppconnect.settings config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.FPPConnectSettingsDefaultPayload, false, nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.FPPConnectSettingsPayload{}, false, fmt.Errorf("api: get fppconnect.settings config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeFPPConnectSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return config.FPPConnectSettingsPayload{}, false, fmt.Errorf("api: decode fppconnect.settings payload: %s", verr.Error())
	}
	return payload, true, nil
}

// handleGetFPPConnectSettingsConfig serves GET
// /api/v1/config/fppconnect.settings. "Nothing has ever been written" is
// never a 404 here, the payload has a well-defined default, so this
// always answers 200.
func (h *handlers) handleGetFPPConnectSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, configured, err := resolveFPPConnectSettings(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve fppconnect.settings", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.FPPConnectSettingsConfigResponse{
			ServerTime: formatTime(now), Kind: config.FPPConnectSettingsConfigKind,
			Revision: 0, Payload: mapFPPConnectSettingsPayload(payload),
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get fppconnect.settings config object", err)
		return
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active fppconnect.settings config revision", err)
		return
	}
	jsonWrite(w, mapFPPConnectSettingsConfigResponse(now, rev, obj, payload))
}

// handleGetFPPConnectSettingsConfigRevisions serves
// GET /api/v1/config/fppconnect.settings/revisions: every revision's
// metadata, newest first.
func (h *handlers) handleGetFPPConnectSettingsConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get fppconnect.settings config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list fppconnect.settings config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.FPPConnectSettingsConfigKind, Revisions: out})
}

// handlePutFPPConnectSettingsConfig serves PUT
// /api/v1/config/fppconnect.settings: a full replacement, appends an
// immutable revision, and activates it in the SAME transaction as its
// audit log entry (ADR-024 decision 11).
func (h *handlers) handlePutFPPConnectSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxFPPConnectSettingsConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxFPPConnectSettingsConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeFPPConnectSettingsPayload(string(body))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeFPPConnectSettingsPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode fppconnect.settings config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.FPPConnectSettingsConfigKind, ObjectID: config.FPPConnectSettingsConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.FPPConnectSettingsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.FPPConnectSettingsConfigKind, config.FPPConnectSettingsConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.FPPConnectSettingsConfigKind,
			Params: map[string]any{"revision": nextRevisionNo},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write fppconnect.settings config revision", writeErr)
		return
	}

	// ADR-039/ADR-036: fppconnect.settings applies to every node's
	// listener, so every node in inventory is pushed the new revision
	// without waiting for its next hello, best-effort per node, matching
	// pushAudioSettingsToAllNodes.
	h.pushFPPConnectToAllNodes(ctx, now)

	jsonWrite(w, mapFPPConnectSettingsConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.FPPConnectSettingsConfigKind, ID: config.FPPConnectSettingsConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// pushFPPConnectToAllNodes best-effort pushes the current
// fppconnect.configure state to every node currently in inventory,
// mirroring pushAudioSettingsToAllNodes's identical fan-out (one goroutine
// per node, detached context, individually bounded) one kind over. Shared
// by every write hook in this package whose kind affects every node
// (fppconnect.settings, show, show.active) rather than one node in
// particular (show.surface's own write pushes only the affected node(s);
// see showobjects.go).
func (h *handlers) pushFPPConnectToAllNodes(ctx context.Context, now time.Time) {
	if h.deps.Nodes == nil {
		return
	}
	views, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.logWarn("failed to list nodes for fppconnect config push", "error", err)
		return
	}
	detached := context.WithoutCancel(ctx)
	for _, nv := range views {
		nodeID := nv.NodeID
		go func() {
			pushCtx, cancel := context.WithTimeout(detached, fppConnectConfigPushTimeout)
			defer cancel()
			fppconnectpush.BestEffort(pushCtx, h.deps.Config, h.deps.RenderPublisher, h.now, nodeID, h.logger, h.deps.FPPConnectStatus)
		}()
	}
}

// pushFPPConnectToNode best-effort pushes the current fppconnect.configure
// state to a single node, detached from the request context and on its
// own bounded timeout, matching handlePutAudioNode's identical
// single-node push one kind over. Used by show.surface's write hook
// (showobjects.go) for the one or two nodes a surface write actually
// affects.
func (h *handlers) pushFPPConnectToNode(ctx context.Context, nodeID string) {
	go func() {
		pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fppConnectConfigPushTimeout)
		defer cancel()
		fppconnectpush.BestEffort(pushCtx, h.deps.Config, h.deps.RenderPublisher, h.now, nodeID, h.logger, h.deps.FPPConnectStatus)
	}()
}

func mapFPPConnectSettingsPayload(p config.FPPConnectSettingsPayload) v1.ConfigFPPConnectSettingsPayload {
	return v1.ConfigFPPConnectSettingsPayload{
		Enabled:          p.Enabled,
		MaxFileBytes:     p.MaxFileBytes,
		MaxAssetDirBytes: p.MaxAssetDirBytes,
	}
}

func mapFPPConnectSettingsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.FPPConnectSettingsPayload) v1.FPPConnectSettingsConfigResponse {
	return v1.FPPConnectSettingsConfigResponse{
		ServerTime: formatTime(now), Kind: config.FPPConnectSettingsConfigKind, Revision: rev.Revision,
		Payload:                mapFPPConnectSettingsPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
