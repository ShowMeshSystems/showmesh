package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/clockconfigpush"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the HTTP surface for the "node.clock" collection
// (ADR-039, Track I seam I1): list/get/put/revisions over
// /api/v1/config/node.clock[/{id}[/revisions]], reusing showconfig.go's
// shared helpers exactly as audionode.go does one kind over. Gated by
// config:write only, matching audio.node's own posture (nearer
// principal/physical-interface management than show-programming state) —
// see [config.NodeClockConfigKind]'s own doc comment for why this is a
// separate kind from audio.node.

const maxNodeClockConfigRequestBodyBytes = 4 * 1024

// listNodeClockSummaries lists every "node.clock" config object with an
// active revision, matching listAudioNodeSummaries's identical shape one
// kind over. Label carries the configured provider — the field most
// useful to skim in a list without fetching each object's full body.
func (h *handlers) listNodeClockSummaries(ctx context.Context) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.NodeClockConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list node.clock config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.NodeClockConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active node.clock config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Provider string `json:"provider"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode node.clock config payload head for %q: %w", obj.ID, err)
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.Provider, Show: "",
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

func (h *handlers) handleListNodeClocks(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	objs, err := h.listNodeClockSummaries(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list node.clock config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.NodeClockConfigKind, Objects: objs})
}

func (h *handlers) handleGetNodeClock(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.NodeClockConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active node.clock config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.NodeClockPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode node.clock config payload", err)
		return
	}
	jsonWrite(w, mapNodeClockConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutNodeClock(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateNodeClockObjectID(id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxNodeClockConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read node.clock request body", err)
		return
	}
	if len(raw) > maxNodeClockConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeNodeClockPayload(string(raw))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeNodeClockPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode node.clock config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.NodeClockConfigKind, id, payloadJSON,
		map[string]any{"provider": payload.Provider, "interface": payload.Interface})
	if writeErr != nil {
		h.writeInternalError(w, now, "write node.clock config revision", writeErr)
		return
	}

	// ADR-039/ADR-036: push the newly-written binding to id without
	// waiting for its next hello — matching handlePutAudioNode's
	// identical push, one configuration kind over.
	go func() {
		pushCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), audioConfigPushTimeout)
		defer cancel()
		clockconfigpush.BestEffort(pushCtx, h.deps.Config, h.deps.RenderPublisher, h.now, id, h.logger)
	}()

	jsonWrite(w, mapNodeClockConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.NodeClockConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetNodeClockRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.NodeClockConfigKind)
}

func mapNodeClockConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.NodeClockPayload) v1.NodeClockConfigResponse {
	return v1.NodeClockConfigResponse{
		ServerTime: formatTime(now), Kind: config.NodeClockConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload: v1.ConfigNodeClock{
			Provider: p.Provider, Interface: p.Interface, Domain: p.Domain,
			ClientOnly: p.ClientOnly, HoldoverLimitSeconds: p.HoldoverLimitSeconds,
			Priority1: p.Priority1, HardwareTimestamping: p.HardwareTimestamping,
			ExternalUDSAddress: p.ExternalUDSAddress, FPPBaseURL: p.FPPBaseURL,
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
