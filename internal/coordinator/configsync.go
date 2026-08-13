package coordinator

// This file is Step 7 seam A's own startup sequencing: the
// SHOWMESH_FPP_ENDPOINTS -> store migration (RES-008 D1) and the owner's
// 2026-08-12 disagreement rule, both of which must run before this
// coordinator constructs a single FPP collector — see Run in
// coordinator.go, which calls syncFPPEndpointsConfig immediately after
// identitySvc exists and before apiDeps/fppRunner are built.

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

// errFPPEndpointsDisagree is returned by syncFPPEndpointsConfig when
// SHOWMESH_FPP_ENDPOINTS is set and disagrees with the store's active
// fpp.endpoints configuration — the owner's 2026-08-12 decision: "refuse
// to start, naming which endpoint differs and where to change it." Run
// treats this exactly like any other fatal config error (log and exit 1),
// never a warning: the reasoning belongs here, in code, per this seam's
// spec — "the refusal lands at the moment of the mistake rather than at
// upgrade time, and the dangerous case is precisely an operator editing
// .env and expecting it to take effect. A silently ignored variable is the
// second source of truth RES-008 warns about."
var errFPPEndpointsDisagree = errors.New("coordinator: SHOWMESH_FPP_ENDPOINTS disagrees with the store's active fpp.endpoints configuration")

// syncFPPEndpointsConfig implements RES-008 D1's env->store migration and
// the owner's 2026-08-12 disagreement rule, and returns the AUTHORITATIVE
// FPP endpoint list Run must use for everything downstream (the collector
// construction loop, api.Dependencies.FPP, api.Dependencies.Collectors,
// and the SHOWMESH_FPP_MQTT_HOSTS cross-check) — envEndpoints itself must
// never be used for any of that once this function returns, because once
// a store configuration exists it is authoritative BY DEFINITION, even
// when (the identical case) it is byte-for-byte equal to envEndpoints.
//
//   - No store configuration exists yet, envEndpoints empty: nothing to
//     migrate, nothing configured. Returns (nil, nil) — the collector does
//     not run, exactly as before this seam existed.
//   - No store configuration exists yet, envEndpoints non-empty: migrates
//     envEndpoints into the store as revision 1, source "env_migration",
//     audited with NO principal (a startup migration has no principal —
//     inventing one would misattribute it). Returns envEndpoints.
//   - A store configuration exists, envEndpoints empty: the store is
//     already authoritative and there is nothing in the environment to
//     compare it against. Returns the store's active endpoints, no
//     warning.
//   - A store configuration exists, envEndpoints non-empty, IDENTICAL
//     (order-insensitive set of id/url pairs — [config.FPPEndpointsEqual]):
//     starts, logging a warning that the store is authoritative and the
//     variable can be removed. Returns the store's active endpoints.
//   - A store configuration exists, envEndpoints non-empty, DIFFERENT:
//     refuses to start — returns [errFPPEndpointsDisagree] wrapped with a
//     message naming which endpoint differs and where to change it.
func syncFPPEndpointsConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, envEndpoints []config.FPPEndpoint, now func() time.Time, logger *slog.Logger) ([]config.FPPEndpoint, error) {
	obj, err := st.GetConfigObject(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateFPPEndpointsFromEnv(ctx, identitySvc, envEndpoints, now, logger)
	case err != nil:
		return nil, fmt.Errorf("coordinator: read fpp.endpoints config object: %w", err)
	}

	// Defect 6 (Step 7 seam A review): handleGetFPPEndpointsConfig
	// (internal/coordinator/api/config.go) already defends the read path
	// against obj.CurrentRevision == 0 ("declared, nothing active" — see
	// store.CreateConfigObject's own doc comment for how that state can
	// exist) and a dangling revision pointer (GetConfigRevision naming a
	// row this store does not hold). Before this fix the BOOT path ran the
	// identical GetConfigRevision call with NO such defence, so either
	// condition made this coordinator refuse to start — a store-integrity
	// question turned into an availability outage, which constraint 13
	// forbids ("the coordinator must start and stay up"). The boot path
	// takes the SAFER outcome the read path cannot: log loudly and
	// proceed as "no active configuration" (matching what a coordinator
	// with nothing configured has always done) rather than exiting.
	if obj.CurrentRevision == 0 {
		logger.Warn("fpp.endpoints config object exists but has no active revision (current_revision == 0); " +
			"treating this as no active fpp.endpoints configuration rather than refusing to start")
		return nil, nil
	}

	rev, err := st.GetConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		logger.Warn("fpp.endpoints config object's active revision pointer names a revision this store does not hold "+
			"(a store-integrity condition, not a normal startup state); treating this as no active fpp.endpoints "+
			"configuration rather than refusing to start",
			"current_revision", obj.CurrentRevision)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("coordinator: read active fpp.endpoints config revision %d: %w", obj.CurrentRevision, err)
	}
	storedEndpoints, err := config.DecodeFPPEndpointsPayload(rev.PayloadJSON)
	if err != nil {
		return nil, fmt.Errorf("coordinator: decode active fpp.endpoints config payload: %w", err)
	}

	if len(envEndpoints) == 0 {
		// Nothing in the environment to compare against; the store is
		// already authoritative from a prior migration or API/CLI write.
		return storedEndpoints, nil
	}

	if config.FPPEndpointsEqual(storedEndpoints, envEndpoints) {
		logger.Warn("SHOWMESH_FPP_ENDPOINTS is still set and matches the store's active fpp.endpoints configuration exactly. " +
			"The store is now authoritative (RES-008 D1) — this variable is no longer read for anything and may be removed " +
			"from your environment.")
		return storedEndpoints, nil
	}

	return nil, fmt.Errorf("%w: %s", errFPPEndpointsDisagree, diffFPPEndpoints(storedEndpoints, envEndpoints))
}

// resolveAuthoritativeFPPEndpoints calls [syncFPPEndpointsConfig] and
// returns cfg with FPPEndpoints OVERWRITTEN by the result — the single
// assignment everything downstream (fppInstanceLister, the FPP collector
// construction loop, api.Dependencies.Collectors, and the
// SHOWMESH_FPP_MQTT_HOSTS cross-check) depends on to ever see the
// store-authoritative list rather than the raw environment-parsed one. It
// is pulled out of Run itself, which does not unit-test cleanly (it opens
// real network listeners), specifically so this assignment has a seam a
// test can call directly: TestResolveAuthoritativeFPPEndpointsUsesStoreOverEnv
// (configsync_test.go) proves that with the environment unset and a
// populated store, the returned cfg.FPPEndpoints is the store's list, not
// empty — the exact acceptance criterion ("the migrated deployment still
// collects from every host it collected from before") that deleting this
// assignment used to silently defeat while every other test in the repo
// stayed green.
func resolveAuthoritativeFPPEndpoints(ctx context.Context, st *store.Store, identitySvc identity.Service, cfg config.Config, now func() time.Time, logger *slog.Logger) (config.Config, error) {
	authoritativeFPPEndpoints, err := syncFPPEndpointsConfig(ctx, st, identitySvc, cfg.FPPEndpoints, now, logger)
	if err != nil {
		return cfg, err
	}
	cfg.FPPEndpoints = authoritativeFPPEndpoints
	// Defect 7 (Step 7 seam A review): once SHOWMESH_FPP_ENDPOINTS is
	// removed (exactly what the migration warning above tells the
	// operator to do), the store is the ONLY copy of the endpoint list —
	// a restored or freshly created data volume then resolves to zero
	// endpoints with what otherwise reads as a perfectly healthy startup
	// (every other log line green, /readyz OK) and a dashboard that only
	// says "not_configured" to someone already looking at it. INFO is the
	// wrong level for a fact that means "this coordinator will collect
	// from nothing" — raised to WARN in exactly that case; the non-zero
	// case is unchanged.
	if len(cfg.FPPEndpoints) == 0 {
		logger.Warn("resolved authoritative fpp.endpoints configuration (RES-008 D1): zero endpoints configured. " +
			"No FPP instances are configured and nothing will be collected. If this is unexpected, check that a " +
			"data volume was not restored from an empty or wrong backup, and that PUT /api/v1/config/fpp.endpoints " +
			"(or SHOWMESH_FPP_ENDPOINTS, on first boot) actually named your FPP hosts.")
	} else {
		logger.Info("resolved authoritative fpp.endpoints configuration (RES-008 D1)",
			"fpp_endpoint_count", len(cfg.FPPEndpoints))
	}
	return cfg, nil
}

// migrateFPPEndpointsFromEnv performs the one-time SHOWMESH_FPP_ENDPOINTS
// -> store migration (RES-008 D1) when no fpp.endpoints configuration
// object exists yet. See [syncFPPEndpointsConfig]'s doc comment for the
// two cases this covers (envEndpoints empty vs. non-empty).
func migrateFPPEndpointsFromEnv(ctx context.Context, identitySvc identity.Service, envEndpoints []config.FPPEndpoint, now func() time.Time, logger *slog.Logger) ([]config.FPPEndpoint, error) {
	if len(envEndpoints) == 0 {
		// Nothing configured anywhere: matches today's pre-migration
		// behavior exactly (no FPP collector runs).
		return nil, nil
	}

	payloadJSON, err := config.EncodeFPPEndpointsPayload(envEndpoints)
	if err != nil {
		return nil, fmt.Errorf("coordinator: encode fpp.endpoints migration payload: %w", err)
	}

	writeErr := identitySvc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if _, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:        config.FPPEndpointsConfigKind,
			ObjectID:    config.FPPEndpointsConfigObjectID,
			Revision:    1,
			PayloadJSON: payloadJSON,
			// CreatedByPrincipalID/CreatedByPrincipalName deliberately left
			// empty: a startup migration has no principal, and inventing
			// one (e.g. "system") would misattribute this row to a
			// principal that never authenticated — see
			// v1.FPPEndpointsConfigResponse's doc comment for how the API
			// renders that absence (null, not a fabricated name).
			Source: config.FPPEndpointsSourceEnvMigration,
			Note:   "migrated from SHOWMESH_FPP_ENDPOINTS at coordinator startup",
		}); cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, 1); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		return identity.AuditEntry{
			// Timestamp is the only field this entry carries beyond the
			// action/target/params triple: PrincipalID/PrincipalName/Form/
			// CredentialID all stay their zero value ("no principal"),
			// which is the honest attribution for a migration nothing
			// authenticated to trigger — see this function's doc comment.
			Timestamp: now(),
			Action:    "config.migrate",
			Target:    config.FPPEndpointsConfigKind,
			Params: map[string]any{
				"source":        config.FPPEndpointsSourceEnvMigration,
				"endpointCount": len(envEndpoints),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		return nil, fmt.Errorf("coordinator: migrate SHOWMESH_FPP_ENDPOINTS into the store: %w", writeErr)
	}

	// Defect 3b (Step 7 seam A review): this warning previously existed
	// ONLY in syncFPPEndpointsConfig's identical-values branch, which by
	// construction can never run on the boot that performs the migration
	// (that branch only runs once a store configuration ALREADY exists).
	// So an operator following this exact migration path — the one
	// RES-008 D1 exists for — never saw the "you may remove this
	// variable" guidance on the one boot where it first became true. It
	// is logged here, once, on the migrating boot itself.
	logger.Warn("migrated SHOWMESH_FPP_ENDPOINTS into the coordinator's store as fpp.endpoints revision 1 (RES-008 D1). " +
		"The store is now authoritative — this variable is no longer read for anything and may be removed from your " +
		"environment. Leaving it set is safe as long as it continues to match the store's active configuration; a later " +
		"restart with a DIFFERENT value refuses to start rather than silently overriding the store.")

	return envEndpoints, nil
}

// diffFPPEndpoints renders a human-readable description of exactly which
// endpoints differ between the store's active configuration and
// SHOWMESH_FPP_ENDPOINTS, for [errFPPEndpointsDisagree]'s error message —
// the owner's 2026-08-12 decision requires "naming which endpoint differs
// and where to change it," not merely stating that the two disagree.
func diffFPPEndpoints(stored, env []config.FPPEndpoint) string {
	storedByID := make(map[string]string, len(stored))
	for _, e := range stored {
		storedByID[e.ID] = e.URL
	}
	envByID := make(map[string]string, len(env))
	for _, e := range env {
		envByID[e.ID] = e.URL
	}

	msg := "the store's active fpp.endpoints configuration is authoritative (RES-008 D1); " +
		"either remove SHOWMESH_FPP_ENDPOINTS from your environment to accept it, or change it to match. Differences: "

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
			appendDiff(fmt.Sprintf("id %q is in SHOWMESH_FPP_ENDPOINTS (url %q) but not in the store", id, envURL))
		case storedURL != envURL:
			appendDiff(fmt.Sprintf("id %q: SHOWMESH_FPP_ENDPOINTS has url %q, the store has %q", id, envURL, storedURL))
		}
	}
	for id, storedURL := range storedByID {
		if _, ok := envByID[id]; !ok {
			appendDiff(fmt.Sprintf("id %q is in the store (url %q) but not in SHOWMESH_FPP_ENDPOINTS", id, storedURL))
		}
	}
	return msg
}
