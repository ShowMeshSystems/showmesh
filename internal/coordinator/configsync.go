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
		return migrateFPPEndpointsFromEnv(ctx, identitySvc, envEndpoints, now)
	case err != nil:
		return nil, fmt.Errorf("coordinator: read fpp.endpoints config object: %w", err)
	}

	rev, err := st.GetConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, obj.CurrentRevision)
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

// migrateFPPEndpointsFromEnv performs the one-time SHOWMESH_FPP_ENDPOINTS
// -> store migration (RES-008 D1) when no fpp.endpoints configuration
// object exists yet. See [syncFPPEndpointsConfig]'s doc comment for the
// two cases this covers (envEndpoints empty vs. non-empty).
func migrateFPPEndpointsFromEnv(ctx context.Context, identitySvc identity.Service, envEndpoints []config.FPPEndpoint, now func() time.Time) ([]config.FPPEndpoint, error) {
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
