package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const maxEventsMQTTConfigRequestBodyBytes = 4 * 1024

// storedEventsMQTT reads the active events.mqtt revision; ok is false when nothing is stored.
func storedEventsMQTT(ctx context.Context, cs ConfigStore) (settings config.EventsMQTT, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, ok bool, err error) {
	obj, err = cs.GetConfigObject(ctx, config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return config.DefaultEventsMQTT(), rev, obj, false, nil
	}
	if err != nil {
		return config.EventsMQTT{}, rev, obj, false, err
	}
	rev, err = cs.GetConfigRevision(ctx, config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.EventsMQTT{}, rev, obj, false, err
	}
	settings, err = config.DecodeEventsMQTTPayload(rev.PayloadJSON)
	return settings, rev, obj, err == nil, err
}

// handleGetEventsMQTTConfig serves GET /api/v1/config/events.mqtt. It never answers 404.
func (h *handlers) handleGetEventsMQTTConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	settings, rev, obj, ok, err := storedEventsMQTT(r.Context(), h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "read events.mqtt", err)
		return
	}
	if !ok {
		jsonWrite(w, v1.EventsMQTTConfigResponse{
			ServerTime: formatTime(now), Kind: config.EventsMQTTConfigKind,
			Payload: mapEventsMQTTPayload(settings), UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}
	jsonWrite(w, mapEventsMQTTConfigResponse(now, rev, obj.UpdatedAt, settings))
}

// handleGetEventsMQTTConfigRevisions serves GET /api/v1/config/events.mqtt/revisions, newest first.
func (h *handlers) handleGetEventsMQTTConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get events.mqtt config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}
	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list events.mqtt config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.EventsMQTTConfigKind, Revisions: out})
}

// decodeEventsMQTTPutBody reads a partial update: an absent field keeps the
// stored value, and a null or unknown field is refused. problem is the
// operator's sentence, or "" on success.
func decodeEventsMQTTPutBody(body io.Reader, base config.EventsMQTT) (settings config.EventsMQTT, problem string) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return base, "The body must be a JSON object with enabled, broker or topicPrefix."
	}
	if top == nil {
		return base, "The body must be a JSON object. Send {} to change nothing."
	}
	for key, raw := range top {
		var name string
		var target any
		switch key {
		case "enabled":
			name, target = "Publishing", &base.Enabled
		case "broker":
			name, target = "The broker", &base.Broker
		case "topicPrefix":
			name, target = "The topic prefix", &base.TopicPrefix
		default:
			return base, "That setting is not recognised. Use only enabled, broker and topicPrefix."
		}
		if isJSONNull(raw) {
			return base, name + " cannot be null. Leave it out to keep the saved value."
		}
		if err := json.Unmarshal(raw, target); err != nil {
			if key == "enabled" {
				return base, "Publishing must be true or false."
			}
			return base, name + " must be text."
		}
	}
	return base, ""
}

// handlePutEventsMQTTConfig serves PUT /api/v1/config/events.mqtt: it stores a new revision with its audit entry.
func (h *handlers) handlePutEventsMQTTConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}
	base, _, _, _, err := storedEventsMQTT(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "read events.mqtt for merge", err)
		return
	}
	settings, problem := decodeEventsMQTTPutBody(io.LimitReader(r.Body, maxEventsMQTTConfigRequestBodyBytes+1), base)
	if problem != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(problem))
		return
	}
	brokers, err := h.deps.IntegrationBrokers.IntegrationBrokers(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list integration brokers for events.mqtt", err)
		return
	}
	if problem = config.EventsMQTTProblem(settings, brokers); problem != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(problem))
		return
	}
	payloadJSON, err := config.EncodeEventsMQTTPayload(settings)
	if err != nil {
		h.writeInternalError(w, now, "encode events.mqtt payload", err)
		return
	}

	var activated store.ConfigRevisionRecord
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		if obj, gerr := tx.GetConfigObject(ctx, config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}
		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.EventsMQTTConfigKind, ObjectID: config.EventsMQTTConfigObjectID,
			Revision: currentRevision + 1, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.EventsMQTTSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.EventsMQTTConfigKind, config.EventsMQTTConfigObjectID, rec.Revision); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.EventsMQTTConfigKind,
			Params: map[string]any{"revision": rec.Revision, "enabled": settings.Enabled, "broker": settings.Broker},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write events.mqtt config revision", writeErr)
		return
	}
	jsonWrite(w, mapEventsMQTTConfigResponse(now, activated, now, settings))
}

func mapEventsMQTTPayload(s config.EventsMQTT) v1.ConfigEventsMQTTPayload {
	return v1.ConfigEventsMQTTPayload{Enabled: s.Enabled, Broker: s.Broker, TopicPrefix: s.TopicPrefix}
}

func mapEventsMQTTConfigResponse(now time.Time, rev store.ConfigRevisionRecord, updatedAt time.Time, s config.EventsMQTT) v1.EventsMQTTConfigResponse {
	return v1.EventsMQTTConfigResponse{
		ServerTime: formatTime(now), Kind: config.EventsMQTTConfigKind, Revision: rev.Revision,
		Payload: mapEventsMQTTPayload(s), UpdatedAt: formatTime(updatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
