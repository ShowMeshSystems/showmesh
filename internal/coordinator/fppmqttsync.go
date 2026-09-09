package coordinator

// Track G seam G-3 (ADR-039): the SHOWMESH_FPP_MQTT_* -> store migration
// and disagreement rule, mirroring resolumeinstancessync.go. The broker
// password is migrated and compared separately from the non-secret fields,
// since it lives in the credentials table rather than in a config_revisions
// row (internal/coordinator/store/credentials.go).
//
// This file also holds migrateFPPMQTTSecretFileToStore, the unrelated
// one-time move of the password OUT OF its legacy pre-credentials-table file
// (owner ruling 2026-09-08, "credentials into SQLite"): see that function's
// own doc comment. It must run before every function below it in this file,
// so a deployment upgrading from that file-based version sees its password
// already in the credentials table by the time syncFPPMQTTConfig reads it.

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
func syncFPPMQTTConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateFPPMQTTFromEnv(ctx, st, identitySvc, envCfg, envPassword, now, logger)
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
	storedPassword, _, perr := st.GetCredential(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, config.FPPMQTTPasswordCredentialField)
	if perr != nil {
		// An unreadable credential must not refuse boot: a startup path has
		// no principal to hold accountable, and exiting here is a restart
		// loop with no API and no dashboard (ADR-039 decision 3's posture).
		// The failure is scoped to the FPP MQTT collector's credential:
		// continue with the env password where the environment still
		// carries a matching configuration, otherwise with none, until the
		// volume is fixed or the password is rotated via
		// PUT /api/v1/config/fpp.mqtt.
		logger.Error("failed to read the stored fpp.mqtt password; starting anyway with the FPP MQTT collector's "+
			"credential degraded rather than refusing to boot. Fix the data volume and restart, or rotate the "+
			"password via PUT /api/v1/config/fpp.mqtt.",
			"error", perr)
		if envCfg.Configured() && config.FPPMQTTConfigEqual(storedCfg, envCfg) {
			return storedCfg, envPassword, false, nil
		}
		if !envCfg.Configured() {
			return storedCfg, "", false, nil
		}
		// Non-secret fields disagree regardless of the unreadable file:
		// fall through to the owner's disagreement rule below.
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
func resolveAuthoritativeFPPMQTT(ctx context.Context, st *store.Store, identitySvc identity.Service, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	cfg, password, migrationDeferred, err = syncFPPMQTTConfig(ctx, st, identitySvc, envCfg, envPassword, now, logger)
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
// to boot. The credential is written FIRST, before the config revision: if
// it fails, the migration is deferred and nothing is written to the config
// revision either, so the two never disagree about whether this migration
// landed.
func migrateFPPMQTTFromEnv(ctx context.Context, st *store.Store, identitySvc identity.Service, envCfg config.FPPMQTTConfig, envPassword string, now func() time.Time, logger *slog.Logger) (cfg config.FPPMQTTConfig, password string, migrationDeferred bool, err error) {
	if !envCfg.Configured() {
		return config.FPPMQTTConfig{}, "", false, nil
	}

	if envPassword != "" {
		if werr := st.SetCredential(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, config.FPPMQTTPasswordCredentialField, envPassword); werr != nil {
			reportDeferredFPPMQTTMigration(logger, "the fpp.mqtt credential could not be written", werr)
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

// migrateFPPMQTTSecretFileToStore is the one-time move of the fpp.mqtt
// broker password out of its legacy 0600 file (config.ReadFPPMQTTPassword)
// and into the credentials table (owner ruling 2026-09-08, "credentials
// into SQLite"): a database an operator can back up as a single unit, not a
// database plus a file somebody forgets. Called once at startup, before
// [resolveAuthoritativeFPPMQTT]: everything below this function in the
// package reads the credential from the store only, never from this file,
// so it must land there first.
//
// Retried on every start until it succeeds, and, like
// [migrateFPPMQTTFromEnv], NEVER refuses to start on failure (ADR-039
// decision 3): a startup migration has no principal to hold accountable for
// an audit-append failure, and under the deployment bundle's restart policy
// a refusal is a restart loop with no API and no dashboard. That mistake
// has already been made once in this project (Step 7's SHOWMESH_FPP_ENDPOINTS
// migration).
//
// Three cases beyond the ordinary move: the file absent is the steady state
// once this has succeeded once (or on a deployment that never used the
// file), and is not an error. The file present but empty means there is
// nothing to move: write nothing to the store, only remove the file. A
// re-run after a partial move (the credential already landed in a previous
// boot but that boot died before removing the file) is safe: the store
// write is an upsert, and the file is removed unconditionally once whatever
// value it held, if any, is confirmed in the store.
func migrateFPPMQTTSecretFileToStore(ctx context.Context, st *store.Store, identitySvc identity.Service, dataDir string, now func() time.Time, logger *slog.Logger) {
	password, present, err := config.ReadFPPMQTTPassword(dataDir)
	if err != nil {
		logger.Error("failed to read the legacy fpp.mqtt secret file during startup migration; leaving it in place "+
			"and retrying on every start until the data volume is fixed", "error", err)
		return
	}
	if !present {
		return
	}

	if password != "" {
		writeErr := identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
			if err := tx.SetCredential(ctx, config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, config.FPPMQTTPasswordCredentialField, password); err != nil {
				return identity.AuditEntry{}, err
			}
			return identity.AuditEntry{
				Timestamp: now(),
				Action:    "credential.migrate",
				Target:    config.FPPMQTTConfigKind + "." + config.FPPMQTTPasswordCredentialField,
				Params:    map[string]any{"source": "legacy_file"},
				Kind:      identity.AuditAdmin,
			}, nil
		})
		if writeErr != nil {
			logger.Error("could not migrate the legacy fpp.mqtt secret file into the credentials table; leaving the "+
				"file in place, the FPP MQTT collector's password stays unavailable until this succeeds, and this "+
				"migration retries on every start", "error", writeErr)
			return
		}
		logger.Warn("migrated the fpp.mqtt broker password out of its legacy data-directory file and into the " +
			"credentials table; removing the now-obsolete file")
	}

	if err := config.ClearFPPMQTTPassword(dataDir); err != nil {
		logger.Error("moved the fpp.mqtt password into the credentials table but could not remove the now-obsolete "+
			"legacy secret file; remove it by hand, it is no longer read", "error", err)
	}
}
