package api

import (
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
	"github.com/showmeshsystems/showmesh/internal/coordinator/versions"
)

const maxReleaseSettingsConfigRequestBodyBytes = 4 * 1024

// storedReleaseSettings reads the active release.settings revision; ok is false when nothing is stored.
func storedReleaseSettings(ctx context.Context, cs ConfigStore) (settings config.ReleaseSettings, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, ok bool, err error) {
	obj, err = cs.GetConfigObject(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0) {
		return config.ReleaseSettings{}, rev, obj, false, nil
	}
	if err != nil {
		return config.ReleaseSettings{}, rev, obj, false, err
	}
	rev, err = cs.GetConfigRevision(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.ReleaseSettings{}, rev, obj, false, err
	}
	settings, err = config.DecodeReleaseSettingsPayload(rev.PayloadJSON)
	return settings, rev, obj, err == nil, err
}

// runningReleaseSettings is what the checker runs with now, used while nothing is stored.
func (h *handlers) runningReleaseSettings() config.ReleaseSettings {
	if h.deps.Releases == nil {
		return config.DefaultReleaseSettings()
	}
	s := h.deps.Releases.Status()
	return config.ReleaseSettings{Enabled: s.Enabled, CheckInterval: s.Interval}
}

// handleGetReleaseSettingsConfig serves GET /api/v1/config/release.settings. It never answers 404.
func (h *handlers) handleGetReleaseSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	settings, rev, obj, ok, err := storedReleaseSettings(r.Context(), h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "read release.settings", err)
		return
	}
	if !ok {
		jsonWrite(w, v1.ReleaseSettingsConfigResponse{
			ServerTime: formatTime(now), Kind: config.ReleaseSettingsConfigKind,
			Payload: mapReleaseSettingsPayload(h.runningReleaseSettings()), UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}
	jsonWrite(w, mapReleaseSettingsConfigResponse(now, rev, obj.UpdatedAt, settings))
}

// decodeReleaseSettingsPutBody reads a partial update: an absent field keeps
// the stored value, and a null or unknown field is refused.
func decodeReleaseSettingsPutBody(body io.Reader, base config.ReleaseSettings) (config.ReleaseSettings, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return base, fmt.Errorf(`request body must be a JSON object with any of "enabled","checkIntervalSeconds": %w`, err)
	}
	if top == nil {
		return base, errors.New(`request body must be a JSON object, not null; send {} to change nothing`)
	}
	for key, raw := range top {
		if isJSONNull(raw) {
			return base, fmt.Errorf("%q must not be null; omit it to leave the stored value unchanged", key)
		}
		switch key {
		case "enabled":
			if err := json.Unmarshal(raw, &base.Enabled); err != nil {
				return base, fmt.Errorf(`"enabled" must be true or false: %w`, err)
			}
		case "checkIntervalSeconds":
			var seconds int64
			if err := json.Unmarshal(raw, &seconds); err != nil {
				return base, fmt.Errorf(`"checkIntervalSeconds" must be a whole number: %w`, err)
			}
			base.CheckInterval = time.Duration(seconds) * time.Second
		default:
			return base, fmt.Errorf(`unknown field %q; the accepted fields are "enabled","checkIntervalSeconds"`, key)
		}
	}
	return base, nil
}

// handlePutReleaseSettingsConfig serves PUT /api/v1/config/release.settings:
// it stores a new revision with its audit entry, then applies it to the running check.
func (h *handlers) handlePutReleaseSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	if h.deps.ReleaseSettingsEnvVarsSet {
		if h.deps.ReleaseSettingsMigrationDeferred {
			writeProblem(w, h.logger, now, releaseSettingsMigrationDeferredProblem())
			return
		}
		writeProblem(w, h.logger, now, releaseSettingsEnvVarSetProblem())
		return
	}
	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}
	base, _, _, ok, err := storedReleaseSettings(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "read release.settings for merge", err)
		return
	}
	if !ok {
		base = config.DefaultReleaseSettings()
	}
	settings, err := decodeReleaseSettingsPutBody(io.LimitReader(r.Body, maxReleaseSettingsConfigRequestBodyBytes+1), base)
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}
	if problem := config.ReleaseSettingsProblem(settings); problem != "" {
		writeProblem(w, h.logger, now, invalidParameterProblem(problem))
		return
	}
	payloadJSON, err := config.EncodeReleaseSettingsPayload(settings)
	if err != nil {
		h.writeInternalError(w, now, "encode release.settings payload", err)
		return
	}

	var activated store.ConfigRevisionRecord
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		if obj, gerr := tx.GetConfigObject(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}
		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ReleaseSettingsConfigKind, ObjectID: config.ReleaseSettingsConfigObjectID,
			Revision: currentRevision + 1, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ReleaseSettingsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, rec.Revision); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ReleaseSettingsConfigKind,
			Params: map[string]any{"revision": rec.Revision, "enabled": settings.Enabled},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write release.settings config revision", writeErr)
		return
	}

	if h.deps.Releases != nil {
		h.deps.Releases.SetSettings(versions.Settings{Enabled: settings.Enabled, Interval: settings.CheckInterval})
	}
	jsonWrite(w, mapReleaseSettingsConfigResponse(now, activated, now, settings))
}

func mapReleaseSettingsPayload(s config.ReleaseSettings) v1.ConfigReleaseSettingsPayload {
	return v1.ConfigReleaseSettingsPayload{Enabled: s.Enabled, CheckIntervalSeconds: int64(s.CheckInterval / time.Second)}
}

func mapReleaseSettingsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, updatedAt time.Time, s config.ReleaseSettings) v1.ReleaseSettingsConfigResponse {
	return v1.ReleaseSettingsConfigResponse{
		ServerTime: formatTime(now), Kind: config.ReleaseSettingsConfigKind, Revision: rev.Revision,
		Payload: mapReleaseSettingsPayload(s), UpdatedAt: formatTime(updatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// releaseSettingsEnvVarSetProblem refuses a write while a retired variable is still set (ADR-039 decision 4).
func releaseSettingsEnvVarSetProblem() v1.Problem {
	return v1.Problem{
		Type: ProblemTypeConflict, Status: http.StatusConflict,
		Title: "The release check cannot be changed yet",
		Detail: "The release check is still set in the coordinator's environment. Remove SHOWMESH_RELEASE_CHECK " +
			"and SHOWMESH_RELEASE_CHECK_INTERVAL, then restart the coordinator.",
	}
}

// releaseSettingsMigrationDeferredProblem refuses a write while the variables are the only copy of the setting.
func releaseSettingsMigrationDeferredProblem() v1.Problem {
	return v1.Problem{
		Type: ProblemTypeConflict, Status: http.StatusConflict,
		Title: "The release check cannot be changed yet",
		Detail: "The coordinator's data volume cannot be written, so nothing was saved. Fix the volume and " +
			"restart; keep the SHOWMESH_RELEASE_CHECK variables until then.",
	}
}
