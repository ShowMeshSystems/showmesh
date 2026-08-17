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
)

// This file is Track G seam G-4 (ADR-039): the assets.settings
// configuration write surface, mirroring resolumeinstancesconfig.go's
// singleton/env-migration shape (same object id, same absent/null/present
// payload discipline, same still-set-env-var 409, same fail-closed-on-audit
// write) narrowed to a scalar object of four named fields instead of a
// list.
//
// The PUT body supports PARTIAL updates, unlike fpp.endpoints/
// resolume.instances' whole-list replace: each of the four fields is
// independently optional, and an absent field means "leave the currently
// stored (or default) value alone" — ADR-039 decision 5's rule applied to
// a scalar object rather than an array. This is deliberately more useful
// here than a forced full-replace would be: an operator changing only the
// sync interval should not have to already know (and risk mistyping) the
// other three values just to send them back unchanged.
//
// Reads here are gated identically to every other configuration kind in
// this package: config:write, never opened by [Options.CloseReads] — see
// config.go's own top comment for the full reasoning, which applies
// unchanged to this kind.

const maxAssetsSettingsConfigRequestBodyBytes = 4 * 1024

// handleGetAssetsSettingsConfig serves GET /api/v1/config/assets.settings:
// the active revision and its decoded payload. 404 when no revision has
// ever been activated, mirroring handleGetResolumeInstancesConfig's
// identical "not configured yet" vs. "configured, migration deferred"
// distinction — unlike resolume.recovery, this kind has no single boolean
// default an unconfigured GET could honestly report, because a migration
// deferral means real values (from the environment) are in effect that a
// blanket "assume config.DefaultAssetSettings" response would misstate.
func (h *handlers) handleGetAssetsSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		if h.deps.AssetSettingsMigrationDeferred {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no assets.settings configuration is stored, but this coordinator IS using one or more of the "+
					"SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/SHOWMESH_ASSET_SYNC_INTERVAL/"+
					"SHOWMESH_ASSET_INVENTORY_INTERVAL settings: the startup migration of those variables into this "+
					"store could not be persisted on this boot, and was deferred rather than refusing to start. "+
					"Nothing was written, so nothing here is stale or half-applied. Check this coordinator's startup "+
					"log for the failure, fix the data volume (usually full, read-only, or a damaged database), and "+
					"restart: the migration is retried on every start. Do NOT remove any of those variables until it "+
					"has succeeded — while the migration is deferred they are the only copy of this configuration."))
			return
		}
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no assets.settings configuration has been created yet; PUT one to create it"))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get assets.settings config object", err)
		return
	}
	if obj.CurrentRevision == 0 {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no assets.settings configuration has been created yet; PUT one to create it"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active assets.settings config revision", err)
		return
	}

	settings, err := config.DecodeAssetSettingsPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode assets.settings config payload", err)
		return
	}

	jsonWrite(w, mapAssetsSettingsConfigResponse(now, rev, obj, settings))
}

// handleGetAssetsSettingsConfigRevisions serves GET
// /api/v1/config/assets.settings/revisions: every revision's metadata,
// newest first — a 200 with an empty list when nothing has ever been
// created, mirroring handleGetResolumeInstancesConfigRevisions.
func (h *handlers) handleGetAssetsSettingsConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get assets.settings config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list assets.settings config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}

	jsonWrite(w, v1.ConfigRevisionsResponse{
		ServerTime: formatTime(now),
		Kind:       config.AssetSettingsConfigKind,
		Revisions:  out,
	})
}

// assetsSettingsPutFields is [decodeAssetsSettingsConfigPutBody]'s decoded
// result: a pointer per field, nil meaning ABSENT ("leave the stored value
// alone" — ADR-039 decision 5). A present-but-null field is rejected by
// the decoder itself (see that function), so by the time a caller holds
// one of these, every non-nil pointer names a value that was genuinely
// present on the wire.
type assetsSettingsPutFields struct {
	ContentBaseURL           *string
	MaxUploadBytes           *int64
	SyncIntervalSeconds      *float64
	InventoryIntervalSeconds *float64
}

// decodeAssetsSettingsConfigPutBody implements PUT
// /api/v1/config/assets.settings' request-body contract: every one of the
// four fields is independently OPTIONAL (absent means "leave the stored
// value alone"), but a field that IS present must not be JSON `null` — the
// identical "a JSON null is not an absent key" rule
// decodeFPPEndpointsConfigPutBody enforces for its own "endpoints" key,
// applied per-field here because this body has four independent fields
// instead of one array. Any other top-level key is refused.
func decodeAssetsSettingsConfigPutBody(body io.Reader) (assetsSettingsPutFields, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return assetsSettingsPutFields{}, fmt.Errorf(
			`request body must be a JSON object with any of "contentBaseUrl","maxUploadBytes","syncIntervalSeconds","inventoryIntervalSeconds": %w`, err)
	}

	allowed := map[string]bool{
		"contentBaseUrl": true, "maxUploadBytes": true,
		"syncIntervalSeconds": true, "inventoryIntervalSeconds": true,
	}
	for key := range top {
		if !allowed[key] {
			return assetsSettingsPutFields{}, fmt.Errorf(
				`unknown field %q; the accepted fields are "contentBaseUrl","maxUploadBytes","syncIntervalSeconds","inventoryIntervalSeconds"`, key)
		}
	}

	var out assetsSettingsPutFields

	if raw, present := top["contentBaseUrl"]; present {
		if isJSONNull(raw) {
			return assetsSettingsPutFields{}, errors.New(
				`"contentBaseUrl" must not be null; omit it to leave the stored value unchanged, or pass "" to deliberately disable asset sync`)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return assetsSettingsPutFields{}, fmt.Errorf(`"contentBaseUrl" must be a string: %w`, err)
		}
		out.ContentBaseURL = &s
	}

	if raw, present := top["maxUploadBytes"]; present {
		if isJSONNull(raw) {
			return assetsSettingsPutFields{}, errors.New(`"maxUploadBytes" must not be null; omit it to leave the stored value unchanged`)
		}
		var n int64
		if err := json.Unmarshal(raw, &n); err != nil {
			return assetsSettingsPutFields{}, fmt.Errorf(`"maxUploadBytes" must be an integer: %w`, err)
		}
		out.MaxUploadBytes = &n
	}

	if raw, present := top["syncIntervalSeconds"]; present {
		if isJSONNull(raw) {
			return assetsSettingsPutFields{}, errors.New(`"syncIntervalSeconds" must not be null; omit it to leave the stored value unchanged`)
		}
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return assetsSettingsPutFields{}, fmt.Errorf(`"syncIntervalSeconds" must be a number: %w`, err)
		}
		out.SyncIntervalSeconds = &n
	}

	if raw, present := top["inventoryIntervalSeconds"]; present {
		if isJSONNull(raw) {
			return assetsSettingsPutFields{}, errors.New(`"inventoryIntervalSeconds" must not be null; omit it to leave the stored value unchanged`)
		}
		var n float64
		if err := json.Unmarshal(raw, &n); err != nil {
			return assetsSettingsPutFields{}, fmt.Errorf(`"inventoryIntervalSeconds" must be a number: %w`, err)
		}
		out.InventoryIntervalSeconds = &n
	}

	return out, nil
}

// isJSONNull (fppcommand_primitives.go) reports whether raw is the literal
// JSON `null` — reused here rather than a second copy, mirroring this
// package's own "one shared rule, one place it is called" lesson.

// applyAssetsSettingsPutFields merges fields over base: a nil pointer
// leaves base's own value in place, a non-nil pointer overwrites it. base
// is the current stored settings (or [config.DefaultAssetSettings] when
// nothing has ever been written), so a partial PUT against a fresh
// coordinator still produces a complete, valid [config.AssetSettings].
func applyAssetsSettingsPutFields(base config.AssetSettings, fields assetsSettingsPutFields) config.AssetSettings {
	out := base
	if fields.ContentBaseURL != nil {
		out.ContentBaseURL = *fields.ContentBaseURL
	}
	if fields.MaxUploadBytes != nil {
		out.MaxUploadBytes = *fields.MaxUploadBytes
	}
	if fields.SyncIntervalSeconds != nil {
		out.SyncInterval = time.Duration(*fields.SyncIntervalSeconds * float64(time.Second))
	}
	if fields.InventoryIntervalSeconds != nil {
		out.InventoryInterval = time.Duration(*fields.InventoryIntervalSeconds * float64(time.Second))
	}
	return out
}

// currentOrDefaultAssetSettings reads this coordinator's current
// assets.settings, or [config.DefaultAssetSettings] when nothing has ever
// been written — the baseline handlePutAssetsSettingsConfig merges a
// partial PUT over.
func currentOrDefaultAssetSettings(ctx context.Context, cs ConfigStore) (config.AssetSettings, error) {
	obj, err := cs.GetConfigObject(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return config.DefaultAssetSettings(), nil
	}
	if err != nil {
		return config.AssetSettings{}, err
	}
	if obj.CurrentRevision == 0 {
		return config.DefaultAssetSettings(), nil
	}
	rev, err := cs.GetConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.AssetSettings{}, err
	}
	return config.DecodeAssetSettingsPayload(rev.PayloadJSON)
}

// handlePutAssetsSettingsConfig serves PUT /api/v1/config/assets.settings:
// merges the request's present fields over the current (or default)
// settings, validates the RESULT, appends an immutable revision, activates
// it, and returns the new active revision — mirroring
// handlePutResolumeInstancesConfig's structure and its still-set-env-var
// 409, narrowed to a merge instead of a whole-object replace (see this
// file's own top comment for why).
//
// ADR-009 requires invalid configuration be rejected before activation:
// decoding, merging, and validation all run and can all fail BEFORE
// [identity.Service.AuditedWrite] is ever called, so a rejected write never
// opens a transaction and never leaves a config_revisions row behind.
func (h *handlers) handlePutAssetsSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	if h.deps.AssetSettingsEnvVarsSet {
		if h.deps.AssetSettingsMigrationDeferred {
			writeProblem(w, h.logger, now, assetsSettingsMigrationDeferredProblem())
			return
		}
		writeProblem(w, h.logger, now, assetsSettingsEnvVarSetProblem())
		return
	}

	fields, err := decodeAssetsSettingsConfigPutBody(io.LimitReader(r.Body, maxAssetsSettingsConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	base, err := currentOrDefaultAssetSettings(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "get current assets.settings for merge", err)
		return
	}
	settings := applyAssetsSettingsPutFields(base, fields)

	if err := config.ValidateAssetSettings(settings); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	payloadJSON, err := config.EncodeAssetSettingsPayload(settings)
	if err != nil {
		h.writeInternalError(w, now, "encode assets.settings config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:                   config.AssetSettingsConfigKind,
			ObjectID:               config.AssetSettingsConfigObjectID,
			Revision:               nextRevisionNo,
			PayloadJSON:            payloadJSON,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
			Source:                 config.AssetSettingsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}

		if _, aerr := tx.ActivateConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, nextRevisionNo); aerr != nil {
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
			Target:        config.AssetSettingsConfigKind,
			Params: map[string]any{
				"revision":          nextRevisionNo,
				"contentBaseUrlSet": settings.ContentBaseURL != "",
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write assets.settings config revision", writeErr)
		return
	}

	jsonWrite(w, mapAssetsSettingsConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.AssetSettingsConfigKind, ID: config.AssetSettingsConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, settings))
}

// assetsSettingsRestartRequiredReason mirrors resolumeInstancesRestartRequiredReason:
// the live asset sync service follows this configuration on its very next
// loop iteration (or promptly via a nudge on change — see
// assetsync.Service.SetSettings), no restart needed.
const assetsSettingsRestartRequiredReason = "this change is already in effect: the asset sync service follows " +
	"this configuration promptly (within about ten seconds). No restart is needed."

func mapAssetsSettingsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, settings config.AssetSettings) v1.AssetsSettingsConfigResponse {
	return v1.AssetsSettingsConfigResponse{
		ServerTime: formatTime(now),
		Kind:       config.AssetSettingsConfigKind,
		Revision:   rev.Revision,
		Payload: v1.ConfigAssetsSettingsPayload{
			ContentBaseURL:           settings.ContentBaseURL,
			MaxUploadBytes:           settings.MaxUploadBytes,
			SyncIntervalSeconds:      settings.SyncInterval.Seconds(),
			InventoryIntervalSeconds: settings.InventoryInterval.Seconds(),
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
		RestartRequired:        false,
		RestartRequiredReason:  assetsSettingsRestartRequiredReason,
	}
}
