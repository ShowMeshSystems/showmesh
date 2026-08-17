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
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HTTP surface for the show, show.surface, and show.active config kinds
// (internal/coordinator/config's show.go/showsurface.go/showactive.go).
// Reuses showconfig.go's shared helpers (getActiveShowConfigRevision,
// handleGetShowConfigRevisions, writeShowConfigRevision, mapValidationError,
// showConfigObjectNotFoundProblem).
//
// listConfigObjectSummaries (showconfig.go) is NOT reused for "show" or
// "show.surface": it decodes a "label"/"show" JSON key pair that fits
// show.action/show.macro but not these two kinds (ShowPayload has "name",
// no "show"; ShowSurfacePayload has "name", not "label"), which would
// otherwise render every list entry's Label permanently blank. This
// file's own listShowSummaries/listShowSurfaceSummaries decode the
// correct field per kind instead.

// showExists reports whether id names a "show" object with an active
// revision — the showExists callback DecodeShowSurfacePayload/
// DecodeShowActivePayload take.
func (h *handlers) showExists(ctx context.Context) func(id string) bool {
	return func(id string) bool {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowConfigKind, id)
		if err != nil {
			return false
		}
		return obj.CurrentRevision > 0
	}
}

// nodeDeclared reports whether nodeID names a declared node — the
// nodeDeclared callback DecodeShowSurfacePayload takes. Reads the full
// declaration list once per call; DeclarationStore has no single-node
// lookup.
func (h *handlers) nodeDeclared(ctx context.Context) func(nodeID string) bool {
	return func(nodeID string) bool {
		decls, err := h.deps.Discovery.ListNodeDeclarations(ctx)
		if err != nil {
			return false
		}
		for _, d := range decls {
			if d.NodeID == nodeID {
				return true
			}
		}
		return false
	}
}

// --- kind "show" ---

// listShowSummaries lists every "show" config object with an active
// revision. Show is set to the object's own id (a show belongs to no
// other show; leaving the field blank would read as "unassigned" rather
// than "not applicable" — CLAUDE.md's own "absent evidence is stated,
// never omitted" rule, applied to a summary field rather than an
// observation).
func (h *handlers) listShowSummaries(ctx context.Context) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Name string `json:"name"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode show config payload head for %q: %w", obj.ID, err)
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.Name, Show: obj.ID,
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

func (h *handlers) handleListShows(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.ShowConfigKind))
		return
	}
	objs, err := h.listShowSummaries(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list show config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowConfigKind, Objects: objs})
}

func (h *handlers) handleGetShow(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show config payload", err)
		return
	}
	jsonWrite(w, mapShowConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutShow(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("show id", id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowPayload(string(raw))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeShowPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowConfigKind, id, payloadJSON,
		map[string]any{"name": payload.Name})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetShowRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowConfigKind)
}

func mapShowConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowPayload) v1.ShowConfigResponse {
	return v1.ShowConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                v1.ConfigShow{Name: p.Name, Notes: p.Notes},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- kind "show.surface" ---

// listShowSurfaceSummaries lists every "show.surface" config object with
// an active revision, optionally narrowed to those whose "show" field
// equals showFilter and/or whose "node" field equals nodeFilter (empty
// means no filter on that axis; both may be set at once).
//
// The node filter exists so a client that only wants "which surfaces are
// assigned to this node" (RenderSurfacePanel.tsx) never has to fetch every
// candidate's full payload just to read payload.node — the review of PR
// #14 found exactly that fan-out costing one HTTP call per configured
// surface. Filtering here, against the same per-object decode the show
// filter already does, keeps that a single list call regardless of how
// many surfaces are configured.
func (h *handlers) listShowSurfaceSummaries(ctx context.Context, showFilter, nodeFilter string) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.surface config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show.surface config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Show string `json:"show"`
			Name string `json:"name"`
			Node string `json:"node"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode show.surface config payload head for %q: %w", obj.ID, err)
		}
		if showFilter != "" && head.Show != showFilter {
			continue
		}
		if nodeFilter != "" && head.Node != nodeFilter {
			continue
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.Name, Show: head.Show,
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

func (h *handlers) handleListShowSurfaces(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	showFilter := r.URL.Query().Get("show")
	nodeFilter := r.URL.Query().Get("node")
	objs, err := h.listShowSurfaceSummaries(r.Context(), showFilter, nodeFilter)
	if err != nil {
		h.writeInternalError(w, now, "list show.surface config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowSurfaceConfigKind, Objects: objs})
}

func (h *handlers) handleGetShowSurface(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowSurfaceConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.surface config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowSurfacePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.surface config payload", err)
		return
	}
	jsonWrite(w, mapShowSurfaceConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutShowSurface(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("surface id", id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.surface request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowSurfacePayload(string(raw), h.showExists(r.Context()), h.nodeDeclared(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeShowSurfacePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.surface config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowSurfaceConfigKind, id, payloadJSON,
		map[string]any{"show": payload.Show, "node": payload.Node})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show.surface config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowSurfaceConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowSurfaceConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetShowSurfaceRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowSurfaceConfigKind)
}

func mapConfigShowSurfaceOutput(o config.ShowSurfaceOutput) v1.ConfigShowSurfaceOutput {
	out := v1.ConfigShowSurfaceOutput{Transport: o.Transport}
	if o.NDI != nil {
		out.NDI = &v1.ConfigShowSurfaceNDIOutput{SourceName: o.NDI.SourceName}
	}
	if o.HDMI != nil {
		out.HDMI = &v1.ConfigShowSurfaceHDMI{Display: o.HDMI.Display}
	}
	return out
}

func mapShowSurfaceConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowSurfacePayload) v1.ShowSurfaceConfigResponse {
	return v1.ShowSurfaceConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowSurfaceConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload: v1.ConfigShowSurface{
			Show: p.Show, Name: p.Name, Node: p.Node,
			ChannelRange: v1.ConfigShowSurfaceChannelRange{StartChannel: p.ChannelRange.StartChannel, ChannelCount: p.ChannelRange.ChannelCount},
			Geometry:     v1.ConfigShowSurfaceGeometry{Width: p.Geometry.Width, Height: p.Geometry.Height, PixelFormat: p.Geometry.PixelFormat},
			FrameRate:    p.FrameRate,
			Output:       mapConfigShowSurfaceOutput(p.Output),
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- kind "show.active" (singleton) ---

// handleGetShowActive serves GET /config/show.active: 404
// resourceNotFoundProblem when nothing has ever been activated, matching
// what fpp.endpoints and resolume.composition already answer for "nothing
// configured yet" (TRACK-E-SESSION-SPEC.md section 2.4) — reuses
// getActiveShowConfigRevision (showconfig.go), which already produces that
// 404 for both "no object at all" and "object exists with CurrentRevision
// 0" (store/config.go's documented "declared, nothing active yet" state).
func (h *handlers) handleGetShowActive(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowActiveConfigKind, config.ShowActiveObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get active show.active config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowActivePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.active config payload", err)
		return
	}
	jsonWrite(w, mapShowActiveConfigResponse(now, rev, obj, payload))
}

// handlePutShowActive serves PUT /config/show.active. The object id is
// always [config.ShowActiveObjectID] — a fixed constant, never taken from
// the request (there is no {id} path segment on this route at all) or from
// the payload's own "show" value, mirroring
// resolumecomposition.go's resolumeCompositionObjectIDConst reasoning:
// deriving a singleton's id from any value an operator can change would
// orphan every stored revision the moment that value changed.
func (h *handlers) handlePutShowActive(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := config.ShowActiveObjectID

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.active request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowActivePayload(string(raw), h.showExists(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeShowActivePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.active config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowActiveConfigKind, id, payloadJSON,
		map[string]any{"show": payload.Show})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show.active config revision", writeErr)
		return
	}

	// Every node's expected asset set just changed. Without this the fleet
	// waits out a whole sync interval before anything starts fetching the
	// new show's assets.
	h.deps.AssetSyncNudger.Nudge()

	jsonWrite(w, mapShowActiveConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowActiveConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// handleGetShowActiveRevisions serves GET /config/show.active/revisions.
// show.active has no {id} path segment (it is a singleton route), so this
// cannot call handleGetShowConfigRevisions (showconfig.go), which reads
// its id from r.PathValue("id") — that would read "" here. This mirrors
// that function's own logic with [config.ShowActiveObjectID] standing in
// for the path value it does not have.
func (h *handlers) handleGetShowActiveRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	kind := config.ShowActiveConfigKind
	id := config.ShowActiveObjectID

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), kind, id)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// No object at all yet: activeRevision stays 0.
	case err != nil:
		h.writeInternalError(w, now, "get "+kind+" config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), kind, id)
	if err != nil {
		h.writeInternalError(w, now, "list "+kind+" config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: kind, Revisions: out})
}

func mapShowActiveConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowActivePayload) v1.ShowActiveConfigResponse {
	return v1.ShowActiveConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowActiveConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                v1.ConfigShowActive{Show: p.Show},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
