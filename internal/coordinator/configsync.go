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
//   - No store configuration exists yet, envEndpoints non-empty, and the
//     migration write FAILS (either its audit entry or the config revision
//     itself): logs at ERROR, persists nothing, and returns envEndpoints
//     so this boot behaves exactly as it did before this seam existed.
//     The migration is retried on the next start. See
//     [migrateFPPEndpointsFromEnv] for why this does not refuse to start.
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
//
// The second result, migrationDeferred, is true ONLY in the failed-write
// case above. It is not a detail of this function: it is a fact about this
// running coordinator that nothing else can derive, because "the store
// holds no fpp.endpoints configuration" and "the store holds no
// fpp.endpoints configuration WHILE this coordinator is collecting from
// endpoints the environment named" are indistinguishable from the store
// alone, and the second one is new — before the failed write could defer,
// the migration either created the object or the process exited. It is
// threaded to [api.Dependencies.FPPEndpointsMigrationDeferred] so the read
// and write handlers can state it rather than reporting a coordinator that
// nothing has ever configured. A review of the first version of this fix
// caught that omission: GET /api/v1/config/fpp.endpoints answered 404 with
// "no fpp.endpoints configuration has been created yet" and the Operator UI
// rendered an empty table reading "No fpp.endpoints configuration exists
// yet", both false while three hosts were being polled from the very list
// that failed to persist. A missing field renders as blank and blank reads
// as fine (ADR-020, ADR-011).
func syncFPPEndpointsConfig(ctx context.Context, st *store.Store, identitySvc identity.Service, envEndpoints []config.FPPEndpoint, now func() time.Time, logger *slog.Logger) (endpoints []config.FPPEndpoint, migrationDeferred bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return migrateFPPEndpointsFromEnv(ctx, identitySvc, envEndpoints, now, logger)
	case err != nil:
		return nil, false, fmt.Errorf("coordinator: read fpp.endpoints config object: %w", err)
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
		return nil, false, nil
	}

	rev, err := st.GetConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		logger.Warn("fpp.endpoints config object's active revision pointer names a revision this store does not hold "+
			"(a store-integrity condition, not a normal startup state); treating this as no active fpp.endpoints "+
			"configuration rather than refusing to start",
			"current_revision", obj.CurrentRevision)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: read active fpp.endpoints config revision %d: %w", obj.CurrentRevision, err)
	}
	storedEndpoints, err := config.DecodeFPPEndpointsPayload(rev.PayloadJSON)
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: decode active fpp.endpoints config payload: %w", err)
	}

	if len(envEndpoints) == 0 {
		// Nothing in the environment to compare against; the store is
		// already authoritative from a prior migration or API/CLI write.
		return storedEndpoints, false, nil
	}

	if config.FPPEndpointsEqual(storedEndpoints, envEndpoints) {
		logger.Warn("SHOWMESH_FPP_ENDPOINTS is still set and matches the store's active fpp.endpoints configuration exactly. " +
			"The store is now authoritative (RES-008 D1) — this variable is no longer read for anything and may be removed " +
			"from your environment.")
		return storedEndpoints, false, nil
	}

	return nil, false, fmt.Errorf("%w: %s", errFPPEndpointsDisagree, diffFPPEndpoints(storedEndpoints, envEndpoints))
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
func resolveAuthoritativeFPPEndpoints(ctx context.Context, st *store.Store, identitySvc identity.Service, cfg config.Config, now func() time.Time, logger *slog.Logger) (config.Config, bool, error) {
	authoritativeFPPEndpoints, migrationDeferred, err := syncFPPEndpointsConfig(ctx, st, identitySvc, cfg.FPPEndpoints, now, logger)
	if err != nil {
		return cfg, false, err
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
	return cfg, migrationDeferred, nil
}

// migrateFPPEndpointsFromEnv performs the one-time SHOWMESH_FPP_ENDPOINTS
// -> store migration (RES-008 D1) when no fpp.endpoints configuration
// object exists yet. See [syncFPPEndpointsConfig]'s doc comment for the
// three cases this covers (envEndpoints empty, non-empty, and non-empty
// with a failed write).
//
// A FAILED write does not refuse to start, and that is a deliberate
// reversal of what this function shipped in Step 7. It used to return the
// error, which Run treats as fatal, so an unwritable audit_log — or an
// unwritable config_revisions — turned this exact branch into a
// restart-looping coordinator with no API, no change stream, and no
// dashboard. Three reasons that is the wrong failure direction:
//
//   - It is the same class as the two conditions immediately above in
//     [syncFPPEndpointsConfig] (a zero current_revision, a dangling
//     revision pointer), which already log and proceed for the reason
//     stated there: a store-integrity question must not become an
//     availability outage, which constraint 13 forbids.
//   - ADR-024 decision 11's fail-closed rule ("a write that cannot be
//     attributed does not proceed") exists so an operator cannot act
//     without a trace. This migration has NO principal to hold
//     accountable, so refusing it protects nobody.
//   - ADR-024 decision 7 and constraint 23 scope an identity or audit
//     failure to "you cannot act", never "you cannot see" — reads stay
//     open precisely so a credential problem never costs the operator
//     sight of the show. Exiting here costs the reads, the writes, the
//     API, the stream, and the process at once. And because this branch
//     is only reachable on the first boot after an existing deployment
//     upgrades into Step 7, the operator would read it as the upgrade
//     having broken their coordinator.
//
// This exempts NOTHING from decision 11, which is why it needs no ADR
// change: [identity.Service.AuditedWrite] rolls the whole transaction
// back, so the unattributable write genuinely does not proceed. Only the
// process-level response changes, and that belongs to constraint 13, not
// to decision 11. Writing the revision anyway with degraded attribution —
// the blackout/stop/power-off exemption seam C takes in
// api/fppcommand_handler.go — would be a second exemption to decision 11
// and would need one; a configuration migration is not in that safety
// class, and it costs nothing to simply retry next boot.
//
// Nothing durable is left behind either way, so RES-008 D1's own concern
// is untouched: the environment variable remains the single source of
// truth exactly as it was pre-D1, and the disagreement rule cannot fire
// because no store configuration exists to disagree with.
//
// It is NOT, however, simply "the pre-migration posture" — an earlier
// version of this comment claimed that and a review disproved it. The
// deferred state is genuinely new, and two surfaces had answers written
// under the assumption that it could not exist: the configuration read
// handler reported that nothing had ever been configured, and the write
// handler's 409 told the operator to remove SHOWMESH_FPP_ENDPOINTS and
// restart, which in this state discards the only copy of the endpoint
// list. Both now state the deferral, via
// [api.Dependencies.FPPEndpointsMigrationDeferred]. The retry is still
// boot-only and deliberately so: nothing re-attempts the migration while
// this process runs, so an operator who frees the disk must restart. That
// is a documented limitation rather than an oversight, and it is
// tolerable only because the state is now visible on the API instead of
// living in one startup log line.
func migrateFPPEndpointsFromEnv(ctx context.Context, identitySvc identity.Service, envEndpoints []config.FPPEndpoint, now func() time.Time, logger *slog.Logger) (endpoints []config.FPPEndpoint, migrationDeferred bool, err error) {
	if len(envEndpoints) == 0 {
		// Nothing configured anywhere: matches today's pre-migration
		// behavior exactly (no FPP collector runs).
		return nil, false, nil
	}

	payloadJSON, err := config.EncodeFPPEndpointsPayload(envEndpoints)
	if err != nil {
		return nil, false, fmt.Errorf("coordinator: encode fpp.endpoints migration payload: %w", err)
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
		reportDeferredFPPEndpointsMigration(logger, envEndpoints, writeErr)
		// envEndpoints, not nil: this boot must collect from every host it
		// collected from before the upgrade. Returning nil here would be
		// the silent case defect 7 already warned about — a coordinator
		// that starts looking entirely healthy and collects from nothing.
		return envEndpoints, true, nil
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

	return envEndpoints, false, nil
}

// reportDeferredFPPEndpointsMigration logs the one channel an operator has
// for a migration that did not happen, at ERROR because nothing else on
// this startup path will look wrong: the coordinator comes up, the
// collector runs against the environment's endpoint list, and /readyz is
// OK. The two failure modes are named apart because their remedies differ;
// see [identity.ErrAuditWrite] for the distinction AuditedWrite preserves.
//
// Unlike seam C's degraded-attribution report (api/fppcommand_handler.go),
// this deliberately does NOT also write to stderr. That one exists because
// an unattributed state change really happened and the audit row that
// would have recorded it is gone, so a plain-text line on a second stream
// is the substitute record. Here the transaction rolled back and no state
// change exists to attribute, so this is a diagnostic, not a record, and
// the coordinator's own JSON logger is the right and only place for it.
func reportDeferredFPPEndpointsMigration(logger *slog.Logger, envEndpoints []config.FPPEndpoint, writeErr error) {
	cause := "the fpp.endpoints config revision itself could not be written"
	if errors.Is(writeErr, identity.ErrAuditWrite) {
		cause = "the fpp.endpoints config revision was written but its audit entry could not be recorded alongside it, so the whole transaction rolled back"
	}
	logger.Error("could not migrate SHOWMESH_FPP_ENDPOINTS into the coordinator's store (RES-008 D1): "+cause+". "+
		"Nothing was persisted and the coordinator is starting anyway, collecting from SHOWMESH_FPP_ENDPOINTS exactly as it "+
		"did before this migration existed; the migration is retried on every start. This usually means the data volume is "+
		"full, read-only, or the database is damaged: fix that and restart. Until the migration succeeds, DO NOT remove "+
		"SHOWMESH_FPP_ENDPOINTS: it is the only copy of this coordinator's endpoint list, and removing it would resolve this "+
		"coordinator to zero endpoints on its next restart. PUT /api/v1/config/fpp.endpoints is refused with 409 for as long "+
		"as that variable is set, which is true whether or not this migration succeeded.",
		"error", writeErr, "fpp_endpoint_count", len(envEndpoints))
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
