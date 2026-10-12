package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// resolveReleaseSettings returns the release.settings the checker starts with.
// The stored configuration wins. With nothing stored, the two startup variables
// are migrated once when set (ADR-039 decision 3). It never refuses to start:
// every failure is logged and answered with the best settings still known.
func resolveReleaseSettings(ctx context.Context, st *store.Store, identitySvc identity.Service, envVarsSet bool, env config.ReleaseSettings, now func() time.Time, logger *slog.Logger) (settings config.ReleaseSettings, migrationDeferred bool) {
	fallback := config.DefaultReleaseSettings()
	if envVarsSet {
		fallback = env
	}

	obj, err := st.GetConfigObject(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound) || (err == nil && obj.CurrentRevision == 0):
		if !envVarsSet {
			return fallback, false
		}
		return env, !migrateReleaseSettingsFromEnv(ctx, identitySvc, env, now, logger)
	case err != nil:
		logger.Error("could not read the stored release.settings; starting with the fallback settings", "error", err)
		return fallback, false
	}

	rev, err := st.GetConfigRevision(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		logger.Error("could not read the active release.settings revision; starting with the fallback settings", "error", err)
		return fallback, false
	}
	stored, err := config.DecodeReleaseSettingsPayload(rev.PayloadJSON)
	if err != nil {
		logger.Error("could not decode the active release.settings revision; starting with the fallback settings", "error", err)
		return fallback, false
	}
	if envVarsSet {
		logger.Warn("SHOWMESH_RELEASE_CHECK or SHOWMESH_RELEASE_CHECK_INTERVAL is still set. The stored release.settings "+
			"is used and these variables are ignored; remove them and restart to allow edits.",
			"stored_enabled", stored.Enabled, "stored_interval", stored.CheckInterval,
			"env_enabled", env.Enabled, "env_interval", env.CheckInterval)
	}
	return stored, false
}

// migrateReleaseSettingsFromEnv writes env as release.settings revision 1 with
// its audit entry in one transaction, and reports whether it was stored.
func migrateReleaseSettingsFromEnv(ctx context.Context, identitySvc identity.Service, env config.ReleaseSettings, now func() time.Time, logger *slog.Logger) bool {
	payloadJSON, err := config.EncodeReleaseSettingsPayload(env)
	if err == nil {
		err = identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
			if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
				Kind: config.ReleaseSettingsConfigKind, ObjectID: config.ReleaseSettingsConfigObjectID,
				Revision: 1, PayloadJSON: payloadJSON, Source: config.ReleaseSettingsSourceEnvMigration,
				Note: "migrated from SHOWMESH_RELEASE_CHECK/SHOWMESH_RELEASE_CHECK_INTERVAL at coordinator startup",
			}); cerr != nil {
				return identity.AuditEntry{}, cerr
			}
			if _, aerr := tx.ActivateConfigRevision(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, 1); aerr != nil {
				return identity.AuditEntry{}, aerr
			}
			return identity.AuditEntry{
				Timestamp: now(), Action: "config.migrate", Target: config.ReleaseSettingsConfigKind,
				Params: map[string]any{"source": config.ReleaseSettingsSourceEnvMigration},
				Kind:   identity.AuditAdmin,
			}, nil
		})
	}
	if err != nil {
		logger.Error("could not store SHOWMESH_RELEASE_CHECK and SHOWMESH_RELEASE_CHECK_INTERVAL as release.settings. "+
			"The coordinator is starting with those variables and retries on the next start; do not remove them yet.",
			"error", err)
		return false
	}
	logger.Warn("stored SHOWMESH_RELEASE_CHECK and SHOWMESH_RELEASE_CHECK_INTERVAL as release.settings revision 1. " +
		"Remove both variables and restart to allow edits from the API.")
	return true
}
