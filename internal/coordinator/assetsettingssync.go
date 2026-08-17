package coordinator

// This file is Track G seam G-4's own startup sequencing (ADR-039),
// mirroring resolumeinstancessync.go's env->store migration exactly for the
// four SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/
// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL variables,
// treated as one group (config.AssetSettings): the env->store migration and
// the owner's identical disagreement rule, both of which must run before
// this coordinator's asset sync service is constructed — see Run in
// coordinator.go. SHOWMESH_ASSET_DIR is never part of this migration — it
// stays environment-only (ADR-039 decision 2).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// errAssetSettingsDisagree is [errResolumeInstancesDisagree]'s mirror: the
// owner's disagreement rule applied to assets.settings. Run treats this as
// fatal, exactly like the FPP and Resolume cases.
var errAssetSettingsDisagree = errors.New("coordinator: the SHOWMESH_ASSET_* settings disagree with the store's active assets.settings configuration")

// syncAssetSettingsConfig is [syncResolumeInstancesConfig]'s mirror for the
// assets.settings kind. envVarsSet is [config.Config.AssetSettingsEnvVarsSet]
// — whether ANY of the four variables is currently set — and envSettings is
// their PARSED value (which already carries config.Load's own defaults for
// whichever of the four were left unset). See that function's own doc
// comment for the full case analysis; the logic is identical, narrowed from
// a list to one scalar settings object.
func syncAssetSettingsConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, envVarsSet bool, envSettings config.AssetSettings, now func() time.Time, logger *slog.Logger) (settings config.AssetSettings, migrationDeferred bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateAssetSettingsFromEnv(ctx, identitySvc, envVarsSet, envSettings, now, logger)
	case err != nil:
		return config.AssetSettings{}, false, fmt.Errorf("coordinator: read assets.settings config object: %w", err)
	}

	// Mirrors resolumeinstancessync.go's identical defence: a
	// store-integrity condition (a declared-but-inactive object, or a
	// dangling revision pointer) must not turn into a boot refusal —
	// constraint 13 forbids it. Log loudly and proceed with defaults.
	if obj.CurrentRevision == 0 {
		logger.Warn("assets.settings config object exists but has no active revision (current_revision == 0); " +
			"treating this as no active assets.settings configuration rather than refusing to start")
		return config.DefaultAssetSettings(), false, nil
	}

	rev, err := st.GetConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		logger.Warn("assets.settings config object's active revision pointer names a revision this store does not hold "+
			"(a store-integrity condition, not a normal startup state); treating this as no active assets.settings "+
			"configuration rather than refusing to start",
			"current_revision", obj.CurrentRevision)
		return config.DefaultAssetSettings(), false, nil
	}
	if err != nil {
		return config.AssetSettings{}, false, fmt.Errorf("coordinator: read active assets.settings config revision %d: %w", obj.CurrentRevision, err)
	}
	storedSettings, err := config.DecodeAssetSettingsPayload(rev.PayloadJSON)
	if err != nil {
		return config.AssetSettings{}, false, fmt.Errorf("coordinator: decode active assets.settings config payload: %w", err)
	}

	if !envVarsSet {
		return storedSettings, false, nil
	}

	if config.AssetSettingsEqual(storedSettings, envSettings) {
		logger.Warn("one or more SHOWMESH_ASSET_* settings variables are still set and match the store's active " +
			"assets.settings configuration exactly. The store is now authoritative (ADR-039) — these variables " +
			"are no longer read for anything and may be removed from your environment (SHOWMESH_ASSET_DIR excepted; " +
			"it stays environment-only).")
		return storedSettings, false, nil
	}

	return config.AssetSettings{}, false, fmt.Errorf("%w: %s", errAssetSettingsDisagree, diffAssetSettings(storedSettings, envSettings))
}

// resolveAuthoritativeAssetSettings calls [syncAssetSettingsConfig] and
// returns the AUTHORITATIVE assets.settings — mirroring
// [resolveAuthoritativeResolumeInstances]'s identical role. Everything
// downstream (the asset sync service) must use this result, never
// envSettings directly, once this function returns.
func resolveAuthoritativeAssetSettings(ctx context.Context, st *store.Store, identitySvc identity.Service, envVarsSet bool, envSettings config.AssetSettings, now func() time.Time, logger *slog.Logger) (settings config.AssetSettings, migrationDeferred bool, err error) {
	settings, migrationDeferred, err = syncAssetSettingsConfig(ctx, st, identitySvc, envVarsSet, envSettings, now, logger)
	if err != nil {
		return config.AssetSettings{}, false, err
	}
	logger.Info("resolved authoritative assets.settings configuration (ADR-039)",
		"content_base_url_set", settings.ContentBaseURL != "",
		"max_upload_bytes", settings.MaxUploadBytes,
		"sync_interval", settings.SyncInterval,
		"inventory_interval", settings.InventoryInterval,
	)
	return settings, migrationDeferred, nil
}

// migrateAssetSettingsFromEnv is [migrateResolumeInstancesFromEnv]'s
// mirror. See that function's own doc comment for the full reasoning —
// unchanged here: a failed write is logged and NEVER refuses to start
// (ADR-039 decision 3), because a startup migration has no principal to
// hold accountable for refusing to boot. Runs (and writes revision 1) even
// when envVarsSet is false, so that a coordinator with nothing configured
// still gets a resolvable revision-0 state via [config.DefaultAssetSettings]
// rather than a special case downstream — mirrored from
// migrateResolumeInstancesFromEnv's own "nothing configured" short-circuit,
// narrowed: unlike Resolume, zero configured asset settings is not "no
// migration needed", because config.DefaultAssetSettings() must be the value
// used in BOTH cases and no PUT is required for env vars that were never
// set in the first place.
func migrateAssetSettingsFromEnv(ctx context.Context, identitySvc identity.Service, envVarsSet bool, envSettings config.AssetSettings, now func() time.Time, logger *slog.Logger) (settings config.AssetSettings, migrationDeferred bool, err error) {
	if !envVarsSet {
		return config.DefaultAssetSettings(), false, nil
	}

	payloadJSON, err := config.EncodeAssetSettingsPayload(envSettings)
	if err != nil {
		return config.AssetSettings{}, false, fmt.Errorf("coordinator: encode assets.settings migration payload: %w", err)
	}

	writeErr := identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:        config.AssetSettingsConfigKind,
			ObjectID:    config.AssetSettingsConfigObjectID,
			Revision:    1,
			PayloadJSON: payloadJSON,
			// CreatedByPrincipalID/CreatedByPrincipalName deliberately left
			// empty: a startup migration has no principal.
			Source: config.AssetSettingsSourceEnvMigration,
			Note:   "migrated from SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL at coordinator startup",
		}); cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, 1); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		return identity.AuditEntry{
			Timestamp: now(),
			Action:    "config.migrate",
			Target:    config.AssetSettingsConfigKind,
			Params: map[string]any{
				"source": config.AssetSettingsSourceEnvMigration,
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		reportDeferredAssetSettingsMigration(logger, writeErr)
		return envSettings, true, nil
	}

	logger.Warn("migrated the SHOWMESH_ASSET_* settings variables into the coordinator's store as assets.settings " +
		"revision 1 (ADR-039). The store is now authoritative — these variables are no longer read for anything and " +
		"may be removed from your environment (SHOWMESH_ASSET_DIR excepted). Leaving them set is safe as long as they " +
		"continue to match the store's active configuration; a later restart with a DIFFERENT value refuses to start " +
		"rather than silently overriding the store.")

	return envSettings, false, nil
}

// reportDeferredAssetSettingsMigration is
// [reportDeferredResolumeInstancesMigration]'s mirror.
func reportDeferredAssetSettingsMigration(logger *slog.Logger, writeErr error) {
	cause := "the assets.settings config revision itself could not be written"
	if errors.Is(writeErr, identity.ErrAuditWrite) {
		cause = "the assets.settings config revision was written but its audit entry could not be recorded alongside it, so the whole transaction rolled back"
	}
	logger.Error("could not migrate the SHOWMESH_ASSET_* settings variables into the coordinator's store (ADR-039): "+cause+". "+
		"Nothing was persisted and the coordinator is starting anyway, using those variables exactly as it did before "+
		"this migration existed; the migration is retried on every start. This usually means the data volume is full, "+
		"read-only, or the database is damaged: fix that and restart. Until the migration succeeds, DO NOT remove any "+
		"of the four SHOWMESH_ASSET_* settings variables: they are the only copy of this coordinator's asset store "+
		"settings. PUT /api/v1/config/assets.settings is refused with 409 for as long as any of them is set, which is "+
		"true whether or not this migration succeeded.",
		"error", writeErr)
}

// diffAssetSettings is [diffResolumeInstances]'s mirror, for
// [errAssetSettingsDisagree]'s error message.
func diffAssetSettings(stored, env config.AssetSettings) string {
	msg := "the store's active assets.settings configuration is authoritative (ADR-039); " +
		"either remove the SHOWMESH_ASSET_* settings variables from your environment to accept it, or change them to match. Differences: "

	first := true
	appendDiff := func(s string) {
		if !first {
			msg += "; "
		}
		msg += s
		first = false
	}

	if stored.ContentBaseURL != env.ContentBaseURL {
		appendDiff(fmt.Sprintf("contentBaseUrl: env has %q, the store has %q", env.ContentBaseURL, stored.ContentBaseURL))
	}
	if stored.MaxUploadBytes != env.MaxUploadBytes {
		appendDiff(fmt.Sprintf("maxUploadBytes: env has %d, the store has %d", env.MaxUploadBytes, stored.MaxUploadBytes))
	}
	if stored.SyncInterval != env.SyncInterval {
		appendDiff(fmt.Sprintf("syncInterval: env has %s, the store has %s", env.SyncInterval, stored.SyncInterval))
	}
	if stored.InventoryInterval != env.InventoryInterval {
		appendDiff(fmt.Sprintf("inventoryInterval: env has %s, the store has %s", env.InventoryInterval, stored.InventoryInterval))
	}
	return msg
}
