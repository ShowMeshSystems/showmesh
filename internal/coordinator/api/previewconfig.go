package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The three preview configuration kinds. Writes need config:write like every
// other kind, and are allowed in show mode because previews are not show
// configuration. Secrets (gateway token, camera stream URL) are written in the
// same transaction as the revision and are never returned.

const maxPreviewConfigRequestBodyBytes = 16 * 1024

// operatorError is a message written for the operator, so it keeps sentence case and a final period.
type operatorError string

func (e operatorError) Error() string { return string(e) }

func operatorErrorf(format string, args ...any) error {
	return operatorError(fmt.Sprintf(format, args...))
}

type previewConfigRead struct {
	rev store.ConfigRevisionRecord
	obj store.ConfigObjectRecord
	ok  bool
}

// readActivePreviewConfig returns the active revision of kind/id; ok is false when none exists.
func (h *handlers) readActivePreviewConfig(ctx context.Context, kind, id string) (previewConfigRead, error) {
	obj, err := h.deps.Config.GetConfigObject(ctx, kind, id)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return previewConfigRead{}, nil
	}
	if err != nil {
		return previewConfigRead{}, err
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, kind, id, obj.CurrentRevision)
	if err != nil {
		return previewConfigRead{}, err
	}
	return previewConfigRead{rev: rev, obj: obj, ok: true}, nil
}

func (h *handlers) handlePreviewConfigRevisions(w http.ResponseWriter, r *http.Request, kind, id string) {
	now := h.now()
	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), kind, id)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
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

// decodePreviewPutBody reads a JSON object body and returns its fields. null and
// non-object bodies are refused, and so is any key outside known.
func decodePreviewPutBody(body io.Reader, known ...string) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return nil, operatorErrorf("The request body must be a JSON object. Fix it and send it again.")
	}
	if top == nil {
		return nil, operatorErrorf("The request body must be a JSON object, not null. Send {} to change nothing.")
	}
	allowed := make(map[string]bool, len(known))
	for _, k := range known {
		allowed[k] = true
	}
	for key := range top {
		if !allowed[key] {
			return nil, operatorErrorf("The request has a field that is not accepted. Use only %s.", strings.Join(known, ", "))
		}
	}
	return top, nil
}

func decodePreviewString(raw json.RawMessage, field string) (string, error) {
	if isJSONNull(raw) {
		return "", operatorErrorf("The %s cannot be blank. Leave it out to keep the stored value.", field)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", operatorErrorf("The %s must be text.", field)
	}
	return s, nil
}

func decodePreviewStringList(raw json.RawMessage, field string) ([]string, error) {
	if isJSONNull(raw) {
		return nil, operatorErrorf("The %s cannot be blank. Leave it out to keep the stored value.", field)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, operatorErrorf("The %s must be a list of tile keys.", field)
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}

// credentialChange is a secret write applied inside the revision's own transaction.
type credentialChange struct {
	kind, id, field string
	set             bool
	value           string
	apply           bool
}

func (c credentialChange) run(ctx context.Context, tx *store.Tx) error {
	if !c.apply {
		return nil
	}
	if c.set {
		return tx.SetCredential(ctx, c.kind, c.id, c.field, c.value)
	}
	return tx.ClearCredential(ctx, c.kind, c.id, c.field)
}

// writePreviewRevision appends and activates a revision, applying cred in the same transaction,
// and answers with a problem on a precondition conflict. It reports whether the write succeeded.
func (h *handlers) writePreviewRevision(w http.ResponseWriter, r *http.Request, now time.Time, kind, id, payloadJSON string, precondition revisionPrecondition, cred credentialChange) (store.ConfigRevisionRecord, int64, bool) {
	ac := authFromContext(r.Context())
	rec, next, err := h.writeShowConfigRevisionChecked(r, now, ac, kind, id, payloadJSON, precondition, map[string]any{},
		func(ctx context.Context, tx *store.Tx) error { return cred.run(ctx, tx) })
	if err != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(err, &conflict) {
			writeProblem(w, h.logger, now, previewConflictProblem(conflict))
			return rec, 0, false
		}
		h.writeInternalError(w, now, "write "+kind+" config revision", err)
		return rec, 0, false
	}
	h.deps.Previews.Notify()
	return rec, next, true
}

func previewObjectRecord(kind, id string, revision int64, now time.Time) store.ConfigObjectRecord {
	return store.ConfigObjectRecord{Kind: kind, ID: id, CurrentRevision: revision, UpdatedAt: now}
}

// --- preview.gateway ---

func (h *handlers) handleGetPreviewGatewayConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	cur, err := h.readActivePreviewConfig(r.Context(), config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "read preview.gateway config", err)
		return
	}
	if !cur.ok {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("No preview gateway has been set up yet. Save its address to turn previews on."))
		return
	}
	cfg, tokenSet, err := config.DecodePreviewGatewayPayload(cur.rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode preview.gateway config payload", err)
		return
	}
	jsonWrite(w, mapPreviewGatewayResponse(now, cur.rev, cur.obj, cfg, tokenSet))
}

func (h *handlers) handleGetPreviewGatewayConfigRevisions(w http.ResponseWriter, r *http.Request) {
	h.handlePreviewConfigRevisions(w, r, config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID)
}

func (h *handlers) handlePutPreviewGatewayConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	top, err := decodePreviewPutBody(io.LimitReader(r.Body, maxPreviewConfigRequestBodyBytes+1), "url", "token")
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	cur, err := h.readActivePreviewConfig(r.Context(), config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "read preview.gateway config", err)
		return
	}
	next, tokenSet := config.PreviewGatewayConfig{}, false
	if cur.ok {
		if next, tokenSet, err = config.DecodePreviewGatewayPayload(cur.rev.PayloadJSON); err != nil {
			h.writeInternalError(w, now, "decode preview.gateway config payload", err)
			return
		}
	}
	cred := credentialChange{kind: config.PreviewGatewayConfigKind, id: config.PreviewGatewayConfigObjectID, field: config.PreviewGatewayTokenCredentialField}
	if raw, ok := top["url"]; ok {
		s, err := decodePreviewString(raw, "address")
		if err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
			return
		}
		next.URL = strings.TrimSpace(s)
	}
	if raw, ok := top["token"]; ok {
		cred.apply = true
		if !isJSONNull(raw) {
			s, err := decodePreviewString(raw, "token")
			if err != nil {
				writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
				return
			}
			cred.value, cred.set = s, s != ""
		}
		tokenSet = cred.set
	}
	if msg := config.PreviewGatewayProblem(next); msg != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(msg))
		return
	}
	payloadJSON, err := config.EncodePreviewGatewayPayload(next, tokenSet)
	if err != nil {
		h.writeInternalError(w, now, "encode preview.gateway config payload", err)
		return
	}
	rec, revNo, ok := h.writePreviewRevision(w, r, now, config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID, payloadJSON, precondition, cred)
	if !ok {
		return
	}
	jsonWrite(w, mapPreviewGatewayResponse(now, rec, previewObjectRecord(config.PreviewGatewayConfigKind, config.PreviewGatewayConfigObjectID, revNo, now), next, tokenSet))
}

func mapPreviewGatewayResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, cfg config.PreviewGatewayConfig, tokenSet bool) v1.PreviewGatewayConfigResponse {
	return v1.PreviewGatewayConfigResponse{
		ServerTime: formatTime(now), Kind: config.PreviewGatewayConfigKind, Revision: rev.Revision,
		Payload:                v1.ConfigPreviewGatewayPayload{URL: cfg.URL, TokenSet: tokenSet},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- preview.camera ---

func (h *handlers) handleListPreviewCameras(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	objs, err := h.deps.Config.ListConfigObjects(r.Context(), config.PreviewCameraConfigKind)
	if err != nil {
		h.writeInternalError(w, now, "list preview.camera config objects", err)
		return
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.PreviewCameraConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			h.writeInternalError(w, now, "get active preview.camera config revision", err)
			return
		}
		p, err := config.DecodePreviewCameraPayload(rev.PayloadJSON)
		if err != nil {
			h.writeInternalError(w, now, "decode preview.camera config payload", err)
			return
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: p.Name, Show: "", CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.PreviewCameraConfigKind, Objects: out})
}

func (h *handlers) handleGetPreviewCamera(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	cur, err := h.readActivePreviewConfig(r.Context(), config.PreviewCameraConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active preview.camera config revision", err)
		return
	}
	if !cur.ok {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("No camera has this id. Add it first or check the id."))
		return
	}
	rev, obj := cur.rev, cur.obj
	p, err := config.DecodePreviewCameraPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode preview.camera config payload", err)
		return
	}
	jsonWrite(w, mapPreviewCameraResponse(now, rev, obj, p))
}

func (h *handlers) handleGetPreviewCameraRevisions(w http.ResponseWriter, r *http.Request) {
	h.handlePreviewConfigRevisions(w, r, config.PreviewCameraConfigKind, r.PathValue("id"))
}

func (h *handlers) handlePutPreviewCamera(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	if verr := config.ValidatePreviewCameraObjectID(id); verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem("The camera id must be 1 to 64 lowercase letters, digits or hyphens, and cannot start or end with a hyphen."))
		return
	}
	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	top, err := decodePreviewPutBody(io.LimitReader(r.Body, maxPreviewConfigRequestBodyBytes+1), "name", "enabled", "url")
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	cur, err := h.readActivePreviewConfig(r.Context(), config.PreviewCameraConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "read preview.camera config", err)
		return
	}
	next := config.PreviewCameraPayload{Enabled: true}
	if cur.ok {
		if next, err = config.DecodePreviewCameraPayload(cur.rev.PayloadJSON); err != nil {
			h.writeInternalError(w, now, "decode preview.camera config payload", err)
			return
		}
	}
	bad := func(msg string) { writeProblem(w, h.logger, now, invalidParameterProblem(msg)) }
	if raw, ok := top["name"]; ok {
		s, err := decodePreviewString(raw, "name")
		if err != nil {
			bad(err.Error())
			return
		}
		next.Name = strings.TrimSpace(s)
	}
	if raw, ok := top["enabled"]; ok {
		if isJSONNull(raw) || json.Unmarshal(raw, &next.Enabled) != nil {
			bad("The enabled setting must be true or false.")
			return
		}
	}
	cred := credentialChange{kind: config.PreviewCameraConfigKind, id: id, field: config.PreviewCameraURLCredentialField}
	if raw, ok := top["url"]; ok {
		s, err := decodePreviewString(raw, "address")
		if err != nil {
			bad(err.Error())
			return
		}
		s = strings.TrimSpace(s)
		if msg := config.PreviewCameraURLProblem(s); msg != "" {
			bad(msg)
			return
		}
		cred.apply, cred.set, cred.value = true, true, s
		next.URLSet, next.URLDisplay = true, config.PreviewCameraURLDisplay(s)
	}
	if msg := config.PreviewCameraNameProblem(next.Name); msg != "" {
		bad(msg)
		return
	}
	if !next.URLSet {
		bad("A camera needs an address. Add its rtsp:// address and save again.")
		return
	}
	payloadJSON, err := config.EncodePreviewCameraPayload(next)
	if err != nil {
		h.writeInternalError(w, now, "encode preview.camera config payload", err)
		return
	}
	rec, revNo, ok := h.writePreviewRevision(w, r, now, config.PreviewCameraConfigKind, id, payloadJSON, precondition, cred)
	if !ok {
		return
	}
	jsonWrite(w, mapPreviewCameraResponse(now, rec, previewObjectRecord(config.PreviewCameraConfigKind, id, revNo, now), next))
}

// handleDeletePreviewCamera tombstones the camera and removes its stored stream URL in the same transaction.
func (h *handlers) handleDeletePreviewCamera(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clearURL := func(ctx context.Context, tx *store.Tx) error {
		return tx.ClearCredential(ctx, config.PreviewCameraConfigKind, id, config.PreviewCameraURLCredentialField)
	}
	h.handleDeleteShowConfigObject(w, r, config.PreviewCameraConfigKind, clearURL)
	h.deps.Previews.Notify()
}

func mapPreviewCameraResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.PreviewCameraPayload) v1.PreviewCameraConfigResponse {
	return v1.PreviewCameraConfigResponse{
		ServerTime: formatTime(now), Kind: config.PreviewCameraConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                v1.ConfigPreviewCameraPayload{Name: p.Name, Enabled: p.Enabled, URLSet: p.URLSet, URLDisplay: p.URLDisplay},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- dashboard.layout ---

func (h *handlers) handleGetDashboardLayoutConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	cur, err := h.readActivePreviewConfig(r.Context(), config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "read dashboard.layout config", err)
		return
	}
	if !cur.ok {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("No dashboard layout has been saved yet. Tiles show in their default order."))
		return
	}
	p, err := config.DecodeDashboardLayoutPayload(cur.rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode dashboard.layout config payload", err)
		return
	}
	jsonWrite(w, mapDashboardLayoutResponse(now, cur.rev, cur.obj, p))
}

func (h *handlers) handleGetDashboardLayoutConfigRevisions(w http.ResponseWriter, r *http.Request) {
	h.handlePreviewConfigRevisions(w, r, config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID)
}

func (h *handlers) handlePutDashboardLayoutConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	precondition, problem := parseRevisionPrecondition(r)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	top, err := decodePreviewPutBody(io.LimitReader(r.Body, maxPreviewConfigRequestBodyBytes+1), "previews")
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	cur, err := h.readActivePreviewConfig(r.Context(), config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "read dashboard.layout config", err)
		return
	}
	next := config.DashboardLayoutPayload{}.Normalized()
	if cur.ok {
		if next, err = config.DecodeDashboardLayoutPayload(cur.rev.PayloadJSON); err != nil {
			h.writeInternalError(w, now, "decode dashboard.layout config payload", err)
			return
		}
	}
	if raw, ok := top["previews"]; ok {
		if isJSONNull(raw) || bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
			writeProblem(w, h.logger, now, invalidParameterProblem("The layout must be an object with an order list and a hidden list."))
			return
		}
		inner, err := decodePreviewPutBody(bytes.NewReader(raw), "order", "hidden")
		if err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
			return
		}
		if rawList, ok := inner["order"]; ok {
			if next.Previews.Order, err = decodePreviewStringList(rawList, "order list"); err != nil {
				writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
				return
			}
		}
		if rawList, ok := inner["hidden"]; ok {
			if next.Previews.Hidden, err = decodePreviewStringList(rawList, "hidden list"); err != nil {
				writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
				return
			}
		}
	}
	if msg := config.DashboardLayoutProblem(next); msg != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(msg))
		return
	}
	payloadJSON, err := config.EncodeDashboardLayoutPayload(next)
	if err != nil {
		h.writeInternalError(w, now, "encode dashboard.layout config payload", err)
		return
	}
	rec, revNo, ok := h.writePreviewRevision(w, r, now, config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID, payloadJSON, precondition, credentialChange{})
	if !ok {
		return
	}
	jsonWrite(w, mapDashboardLayoutResponse(now, rec, previewObjectRecord(config.DashboardLayoutConfigKind, config.DashboardLayoutConfigObjectID, revNo, now), next))
}

func mapDashboardLayoutResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.DashboardLayoutPayload) v1.DashboardLayoutConfigResponse {
	p = p.Normalized()
	return v1.DashboardLayoutConfigResponse{
		ServerTime: formatTime(now), Kind: config.DashboardLayoutConfigKind, Revision: rev.Revision,
		Payload:                v1.ConfigDashboardLayoutPayload{Previews: v1.ConfigDashboardPreviewLayout{Order: p.Previews.Order, Hidden: p.Previews.Hidden}},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// previewConflictProblem is the 409 for a refused If-Match or If-None-Match on a preview kind.
func previewConflictProblem(e *errConfigRevisionPreconditionFailed) v1.Problem {
	detail := "A newer version was saved first. Read the current settings again and retry."
	if e.precondition.Mode == revisionPreconditionIfNoneMatchCreate {
		detail = "These settings already exist. Read the current settings and save again to change them."
	}
	return v1.Problem{
		Type:   ProblemTypeConfigRevisionPreconditionFailed,
		Title:  "Config write refused: a newer version was saved first",
		Status: http.StatusConflict,
		Detail: detail,
	}
}
