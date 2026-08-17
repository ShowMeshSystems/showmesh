package coordinator

// Track G seam G-3 (ADR-039): the SHOWMESH_FPP_MQTT_* -> store migration
// and disagreement rule, mirroring resolumeinstancessync.go. The broker
// password is migrated and compared separately from the non-secret
// fields, since it lives in a file rather than in a config_revisions row
// (internal/coordinator/config/fppmqttsecret.go).

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

var errFPPMQTTDisagree = errors.New("coordinator: SHOWMESH_FPP_MQTT_* disagree with the store's active fpp.mqtt configuration")

// syncFPPMQTTConfig is [syncResolumeInstancesConfig]'s mirror for the
// fpp.mqtt kind. See that function's own doc comment for the full case
// analysis; the logic is identical, with the password compared and
// migrated alongside the store lookup rather than inside the decoded
// payload.
func syncFPPMQTTConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, dataDir string, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateFPPMQTTFromEnv(ctx, identitySvc, dataDir, envCfg, envPassword, now, logger)
	case err != nil:
		return config.FPPMQTTConfig{}, "", false, fmt.Errorf("coordinator: read fpp.mqtt config object: %w", err)
	}

	// Mirrors configsync.go's identical defence against a store-integrity
	// condition turning into a boot refusal (constraint 13 forbids it).
	if obj.CurrentRevision == 0 {
		logger.Warn("fpp.mqtt config object exists but has no active revision (current_revision == 0); " +
			"treating this as no active fpp.mqtt configuration rather than refusing to start")
		return config.FPPMQTTConfig{}, "", false, nil
	}

	rev, err := st.GetConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		logger.Warn("fpp.mqtt config object's active revision pointer names a revision this store does not hold; "+
			"treating this as no active fpp.mqtt configuration rather than refusing to start",
			"current_revision", obj.CurrentRevision)
		return config.FPPMQTTConfig{}, "", false, nil
	}
	if err != nil {
		return config.FPPMQTTConfig{}, "", false, fmt.Errorf("coordinator: read active fpp.mqtt config revision %d: %w", obj.CurrentRevision, err)
	}
	storedCfg, _, err := config.DecodeFPPMQTTPayload(rev.PayloadJSON)
	if err != nil {
		return config.FPPMQTTConfig{}, "", false, fmt.Errorf("coordinator: decode active fpp.mqtt config payload: %w", err)
	}
	storedPassword, _, err := config.ReadFPPMQTTPassword(dataDir)
	if err != nil {
		return config.FPPMQTTConfig{}, "", false, fmt.Errorf("coordinator: read stored fpp.mqtt password: %w", err)
	}

	if !envCfg.Configured() {
		return storedCfg, storedPassword, false, nil
	}

	if config.FPPMQTTConfigEqual(storedCfg, envCfg) && storedPassword == envPassword {
		logger.Warn("SHOWMESH_FPP_MQTT_* are still set and match the store's active fpp.mqtt configuration exactly. " +
			"The store is now authoritative (ADR-039) — these variables are no longer read for anything and may be " +
			"removed from your environment.")
		return storedCfg, storedPassword, false, nil
	}

	// Never names the actual values in the error: brokerURL/username/hosts
	// are not secret, but the password is, and this message covers all of
	// them at once rather than growing a second, value-echoing branch.
	return config.FPPMQTTConfig{}, "", false, fmt.Errorf(
		"%w: remove SHOWMESH_FPP_MQTT_* from your environment to accept the store's configuration, or change them to match "+
			"(the non-secret fields, the password, or both differ)", errFPPMQTTDisagree)
}

// resolveAuthoritativeFPPMQTT calls [syncFPPMQTTConfig] and returns the
// AUTHORITATIVE fpp.mqtt configuration and password. Everything downstream
// must use this result, never envCfg/envPassword directly, once this
// function returns.
func resolveAuthoritativeFPPMQTT(ctx context.Context, st *store.Store, identitySvc identity.Service, dataDir string, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	cfg, password, migrationDeferred, err = syncFPPMQTTConfig(ctx, st, identitySvc, dataDir, envCfg, envPassword, now, logger)
	if err != nil {
		return config.FPPMQTTConfig{}, "", false, err
	}
	if cfg.Configured() {
		logger.Info("resolved authoritative fpp.mqtt configuration (ADR-039)", "fpp_mqtt_host_count", len(cfg.Hosts))
	} else {
		logger.Info("resolved authoritative fpp.mqtt configuration (ADR-039): not configured")
	}
	return cfg, password, migrationDeferred, nil
}

// migrateFPPMQTTFromEnv is [migrateResolumeInstancesFromEnv]'s mirror. A
// failed write is logged and NEVER refuses to start (ADR-039 decision 3):
// a startup migration has no principal to hold accountable for refusing
// to boot. The password file is written FIRST, before the config
// revision: if it fails, the migration is deferred and nothing is written
// to the store either, so the store and the file never disagree about
// whether this migration landed.
func migrateFPPMQTTFromEnv(ctx context.Context, identitySvc identity.Service, dataDir string, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	if !envCfg.Configured() {
		return config.FPPMQTTConfig{}, "", false, nil
	}

	if envPassword != "" {
		if werr := config.WriteFPPMQTTPassword(dataDir, envPassword); werr != nil {
			reportDeferredFPPMQTTMigration(logger, "the fpp.mqtt secret file could not be written", werr)
			return envCfg, envPassword, true, nil
		}
	}

	payloadJSON, err := config.EncodeFPPMQTTPayload(envCfg, envPassword != "")
	if err != nil {
		return config.FPPMQTTConfig{}, "", false, fmt.Errorf("coordinator: encode fpp.mqtt migration payload: %w", err)
	}

	writeErr := identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:        config.FPPMQTTConfigKind,
			ObjectID:    config.FPPMQTTConfigObjectID,
			Revision:    1,
			PayloadJSON: payloadJSON,
			// CreatedByPrincipalID/CreatedByPrincipalName deliberately left
			// empty: a startup migration has no principal.
			Source: config.FPPMQTTSourceEnvMigration,
			Note:   "migrated from SHOWMESH_FPP_MQTT_* at coordinator startup",
		}); cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, 1); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		return identity.AuditEntry{
			Timestamp: now(),
			Action:    "config.migrate",
			Target:    config.FPPMQTTConfigKind,
			Params: map[string]any{
				"source":    config.FPPMQTTSourceEnvMigration,
				"hostCount": len(envCfg.Hosts),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		reportDeferredFPPMQTTMigration(logger, "the fpp.mqtt config revision itself could not be written", writeErr)
		return envCfg, envPassword, true, nil
	}

	logger.Warn("migrated SHOWMESH_FPP_MQTT_* into the coordinator's store as fpp.mqtt revision 1 (ADR-039). The store is " +
		"now authoritative — these variables are no longer read for anything and may be removed from your environment.")

	return envCfg, envPassword, false, nil
}

// reportDeferredFPPMQTTMigration is
// [reportDeferredResolumeInstancesMigration]'s mirror.
func reportDeferredFPPMQTTMigration(logger *slog.Logger, cause string, writeErr error) {
	logger.Error("could not migrate SHOWMESH_FPP_MQTT_* into the coordinator's store (ADR-039): "+cause+". "+
		"This coordinator's store does not hold a confirmed fpp.mqtt configuration yet; it is starting anyway, using "+
		"SHOWMESH_FPP_MQTT_* exactly as it did before this migration existed, and the migration is retried on every "+
		"start. Do NOT remove SHOWMESH_FPP_MQTT_* until it has succeeded — while the migration is deferred those "+
		"variables are the only copy of this configuration. PUT /api/v1/config/fpp.mqtt is refused with 409 for as "+
		"long as they are set, whether or not this migration succeeded.",
		"error", writeErr)
}
