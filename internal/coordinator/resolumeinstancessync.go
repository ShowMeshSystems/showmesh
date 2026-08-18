package coordinator

// This file is Track G seam G-2's own startup sequencing (ADR-039),
// mirroring configsync.go's SHOWMESH_FPP_ENDPOINTS migration exactly for
// SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID: the env->store migration and
// the owner's identical disagreement rule, both of which must run before
// this coordinator's resolumeManager builds its first collector — see Run
// in coordinator.go.

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

// errResolumeInstancesDisagree is [errFPPEndpointsDisagree]'s mirror: the
// owner's disagreement rule applied to resolume.instances. Run treats this
// as fatal, exactly like the FPP case — see that error's own doc comment
// for the reasoning, which applies unchanged here.
var errResolumeInstancesDisagree = errors.New("coordinator: SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID disagree with the store's active resolume.instances configuration")

// syncResolumeInstancesConfig is [syncFPPEndpointsConfig]'s mirror for the
// resolume.instances kind. See that function's own doc comment for the
// full case analysis (empty/non-empty env, existing/missing store
// configuration, agreement/disagreement, a deferred migration write) — the
// logic is identical, narrowed from a list to at most one instance.
func syncResolumeInstancesConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, envInstances []config.ResolumeInstance, now func() time.Time, logger *slog.Logger) (instances []config.ResolumeInstance, migrationDeferred bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateResolumeInstancesFromEnv(ctx, identitySvc, envInstances, now, logger)
	case err != nil:
		return nil, false, fmt.Errorf("coordinator: read resolume.instances config object: %w", err)
	}

	// Mirrors configsync.go's identical defence: a store-integrity
	// condition (a declared-but-inactive object, or a dangling revision
	// pointer) must not turn into a boot refusal — constraint 13 forbids
	// it. Log loudly and proceed as "no active configuration".
	if obj.CurrentRevision == 0 {
		logger.Warn("resolume.instances config object exists but has no active revision (current_revision == 0); " +
			"treating this as no active resolume.instances configuration rather than refusing to start")
		return nil, false, nil
	}

	rev, err := st.GetConfigRevision(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		logger.Warn("resolume.instances config object's active revision pointer names a revision this store does not hold "+
			"(a store-integrity condition, not a normal startup state); treating this as no active resolume.instances "+
			"configuration rather than refusing to start",
			"current_revision", obj.CurrentRevision)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: read active resolume.instances config revision %d: %w", obj.CurrentRevision, err)
	}
	storedInstances, err := config.DecodeResolumeInstancesPayload(rev.PayloadJSON)
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: decode active resolume.instances config payload: %w", err)
	}

	if len(envInstances) == 0 {
		return storedInstances, false, nil
	}

	if config.ResolumeInstancesEqual(storedInstances, envInstances) {
		logger.Warn("SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID are still set and match the store's active " +
			"resolume.instances configuration exactly. The store is now authoritative (ADR-039) — these variables " +
			"are no longer read for anything and may be removed from your environment.")
		return storedInstances, false, nil
	}

	return nil, false, fmt.Errorf("%w: %s", errResolumeInstancesDisagree, diffResolumeInstances(storedInstances, envInstances))
}

// resolveAuthoritativeResolumeInstances calls [syncResolumeInstancesConfig]
// and returns the AUTHORITATIVE Resolume instance list — mirroring
// [resolveAuthoritativeFPPEndpoints]'s identical role. Everything
// downstream (the resolumeManager, the boot-time fpp.endpoints/
// resolume.instances collision re-check) must use this result, never
// envInstances directly, once this function returns.
func resolveAuthoritativeResolumeInstances(ctx context.Context, st *store.Store, identitySvc identity.Service, envInstances []config.ResolumeInstance, now func() time.Time, logger *slog.Logger) (instances []config.ResolumeInstance, migrationDeferred bool, err error) {
	instances, migrationDeferred, err = syncResolumeInstancesConfig(ctx, st, identitySvc, envInstances, now, logger)
	if err != nil {
		return nil, false, err
	}
	if len(instances) == 0 {
		logger.Info("resolved authoritative resolume.instances configuration (ADR-039): zero instances configured. " +
			"No Resolume Arena instance is configured.")
	} else {
		logger.Info("resolved authoritative resolume.instances configuration (ADR-039)",
			"resolume_instance_id", instances[0].ID)
	}
	return instances, migrationDeferred, nil
}

// migrateResolumeInstancesFromEnv is [migrateFPPEndpointsFromEnv]'s mirror.
// See that function's own doc comment for the full reasoning — unchanged
// here: a failed write is logged and NEVER refuses to start (ADR-039
// decision 3), because a startup migration has no principal to hold
// accountable for refusing to boot.
func migrateResolumeInstancesFromEnv(ctx context.Context, identitySvc identity.Service, envInstances []config.ResolumeInstance, now func() time.Time, logger *slog.Logger) (instances []config.ResolumeInstance, migrationDeferred bool, err error) {
	if len(envInstances) == 0 {
		return nil, false, nil
	}

	payloadJSON, err := config.EncodeResolumeInstancesPayload(envInstances)
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: encode resolume.instances migration payload: %w", err)
	}

	writeErr := identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:        config.ResolumeInstancesConfigKind,
			ObjectID:    config.ResolumeInstancesConfigObjectID,
			Revision:    1,
			PayloadJSON: payloadJSON,
			// CreatedByPrincipalID/CreatedByPrincipalName deliberately left
			// empty: a startup migration has no principal — see
			// migrateFPPEndpointsFromEnv's identical reasoning.
			Source: config.ResolumeInstancesSourceEnvMigration,
			Note:   "migrated from SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID at coordinator startup",
		}); cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, 1); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		return identity.AuditEntry{
			Timestamp: now(),
			Action:    "config.migrate",
			Target:    config.ResolumeInstancesConfigKind,
			Params: map[string]any{
				"source":        config.ResolumeInstancesSourceEnvMigration,
				"instanceCount": len(envInstances),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		reportDeferredResolumeInstancesMigration(logger, envInstances, writeErr)
		return envInstances, true, nil
	}

	logger.Warn("migrated SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID into the coordinator's store as resolume.instances " +
		"revision 1 (ADR-039). The store is now authoritative — these variables are no longer read for anything and " +
		"may be removed from your environment. Leaving them set is safe as long as they continue to match the store's " +
		"active configuration; a later restart with a DIFFERENT value refuses to start rather than silently overriding " +
		"the store.")

	return envInstances, false, nil
}

// reportDeferredResolumeInstancesMigration is
// [reportDeferredFPPEndpointsMigration]'s mirror.
func reportDeferredResolumeInstancesMigration(logger *slog.Logger, envInstances []config.ResolumeInstance, writeErr error) {
	cause := "the resolume.instances config revision itself could not be written"
	if errors.Is(writeErr, identity.ErrAuditWrite) {
		cause = "the resolume.instances config revision was written but its audit entry could not be recorded alongside it, so the whole transaction rolled back"
	}
	logger.Error("could not migrate SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID into the coordinator's store (ADR-039): "+cause+". "+
		"Nothing was persisted and the coordinator is starting anyway, using SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID "+
		"exactly as it did before this migration existed; the migration is retried on every start. This usually means "+
		"the data volume is full, read-only, or the database is damaged: fix that and restart. Until the migration "+
		"succeeds, DO NOT remove SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID: they are the only copy of this "+
		"coordinator's Resolume instance. PUT /api/v1/config/resolume.instances is refused with 409 for as long as "+
		"those variables are set, which is true whether or not this migration succeeded.",
		"error", writeErr, "resolume_instance_count", len(envInstances))
}

// diffResolumeInstances is [diffFPPEndpoints]'s mirror, for
// [errResolumeInstancesDisagree]'s error message.
func diffResolumeInstances(stored, env []config.ResolumeInstance) string {
	storedByID := make(map[string]string, len(stored))
	for _, e := range stored {
		storedByID[e.ID] = e.URL
	}
	envByID := make(map[string]string, len(env))
	for _, e := range env {
		envByID[e.ID] = e.URL
	}

	msg := "the store's active resolume.instances configuration is authoritative (ADR-039); " +
		"either remove SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID from your environment to accept it, or change them to match. Differences: "

	first := true
	appendDiff := func(s string) {
		if !first {
			msg += "; "
		}
		msg += s
		first = false
	}

	for id, envURL := range envByID {
		storedURL, ok := storedByID[id]
		switch {
		case !ok:
			appendDiff(fmt.Sprintf("id %q is in SHOWMESH_RESOLUME_ID/SHOWMESH_RESOLUME_URL (url %q) but not in the store", id, envURL))
		case storedURL != envURL:
			appendDiff(fmt.Sprintf("id %q: SHOWMESH_RESOLUME_URL has url %q, the store has %q", id, envURL, storedURL))
		}
	}
	for id, storedURL := range storedByID {
		if _, ok := envByID[id]; !ok {
			appendDiff(fmt.Sprintf("id %q is in the store (url %q) but not in SHOWMESH_RESOLUME_ID/SHOWMESH_RESOLUME_URL", id, storedURL))
		}
	}
	return msg
}
