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

// GET, PUT and revisions for show.weatherdelay, shaped like
// show.emergencystop. GET never 404s.

const maxWeatherDelayConfigRequestBodyBytes = 8192

func resolveWeatherDelayConfig(ctx context.Context, cs ConfigStore) (payload config.WeatherDelayPayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool, err error) {
	obj, err = cs.GetConfigObject(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.WeatherDelayDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	case err != nil:
		return config.WeatherDelayPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.weatherdelay config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.WeatherDelayDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	}

	rev, err = cs.GetConfigRevision(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.WeatherDelayPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.weatherdelay config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeWeatherDelayPayload(rev.PayloadJSON)
	if verr != nil {
		return config.WeatherDelayPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: decode show.weatherdelay payload: %s", verr.Error())
	}
	return payload, obj, rev, true, nil
}

// handleGetWeatherDelayConfig serves GET /api/v1/config/show.weatherdelay.
func (h *handlers) handleGetWeatherDelayConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, obj, rev, configured, err := resolveWeatherDelayConfig(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.weatherdelay", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.WeatherDelayConfigResponse{
			ServerTime: formatTime(now), Kind: config.ShowWeatherDelayConfigKind,
			Revision: 0, Payload: mapWeatherDelayPayload(payload),
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}
	jsonWrite(w, mapWeatherDelayConfigResponse(now, rev, obj, payload))
}

// handleGetWeatherDelayConfigRevisions serves
// GET /api/v1/config/show.weatherdelay/revisions.
func (h *handlers) handleGetWeatherDelayConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list show.weatherdelay config revisions", err)
		return
	}
	activeRevision := int64(0)
	if n := len(revs); n > 0 {
		activeRevision = revs[n-1].Revision
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.ShowWeatherDelayConfigKind, Revisions: out})
}

// handlePutWeatherDelayConfig serves PUT /api/v1/config/show.weatherdelay,
// activating the new revision in the same transaction as its audit entry.
func (h *handlers) handlePutWeatherDelayConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	// ADR-053 decision 13: no config value may bypass the hold, which
	// includes changing show.weatherdelay's own settings out from under
	// an active delay.
	if active, err := h.weatherDelayActive(ctx); err != nil {
		h.writeInternalError(w, now, "check weather delay state", err)
		return
	} else if active {
		writeProblem(w, h.logger, now, weatherDelayActiveProblem("A weather delay is active. Resume before changing its settings."))
		return
	}

	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWeatherDelayConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxWeatherDelayConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeWeatherDelayPayload(string(body))
	if verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Error()))
		return
	}
	payloadJSON, err := config.EncodeWeatherDelayPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.weatherdelay config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowWeatherDelayConfigKind, ObjectID: config.ShowWeatherDelayConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ShowWeatherDelaySourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ShowWeatherDelayConfigKind,
			Params: map[string]any{"revision": nextRevisionNo},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.weatherdelay config revision", writeErr)
		return
	}

	jsonWrite(w, mapWeatherDelayConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowWeatherDelayConfigKind, ID: config.ShowWeatherDelayConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func mapWeatherDelayPayload(p config.WeatherDelayPayload) v1.ConfigWeatherDelayPayload {
	groups := make([]v1.ConfigWeatherDelayPowerGroupPayload, 0, len(p.PowerGroups))
	for _, g := range p.PowerGroups {
		groups = append(groups, v1.ConfigWeatherDelayPowerGroupPayload{
			ID: g.ID, Label: g.Label, FPPInstanceIDs: nonNilStrings(g.FPPInstanceIDs),
			ResolumeInstanceIDs: nonNilStrings(g.ResolumeInstanceIDs), RenderNodeIDs: nonNilStrings(g.RenderNodeIDs),
			Heartbeat: v1.ConfigWeatherDelayHeartbeatPayload{Enabled: g.Heartbeat.Enabled, IntervalSeconds: g.Heartbeat.IntervalSeconds},
		})
	}
	return v1.ConfigWeatherDelayPayload{
		Alert: v1.ConfigWeatherDelayAlertPayload{
			DelayAssetID: p.Alert.DelayAssetID, CancelNightAssetID: p.Alert.CancelNightAssetID,
			RepeatCount: p.Alert.RepeatCount, NodeIDs: nonNilStrings(p.Alert.NodeIDs),
		},
		PowerGroups: groups,
		Triggers: v1.ConfigWeatherDelayTriggersPayload{
			AnswerWindowSeconds: p.Triggers.AnswerWindowSeconds, CancelAnswerWindowSeconds: p.Triggers.CancelAnswerWindowSeconds,
			RestartMinutes: p.Triggers.RestartMinutes,
		},
	}
}

func mapWeatherDelayConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.WeatherDelayPayload) v1.WeatherDelayConfigResponse {
	return v1.WeatherDelayConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowWeatherDelayConfigKind, Revision: rev.Revision,
		Payload:                mapWeatherDelayPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
