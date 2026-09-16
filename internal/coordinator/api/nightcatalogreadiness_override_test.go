package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the override feature's own night-readiness coverage: a
// node holding a revision that carries an H0.5 conflict downgrades from
// a hard failure to a named warning only when a recorded override covers
// exactly that revision's conflict.

// newNightCatalogReadinessOverrideFixture reuses
// TestCueCatalogDeployRefusesOnClaimConflict's own conflict scenario.
func newNightCatalogReadinessOverrideFixture(t *testing.T) (*API, *store.Store, string) {
	t.Helper()
	api, st, _, token := newCueCatalogClaimConflictFixture(t)
	return api, st, token
}

func nightCatalogCurrentCheckFor(t *testing.T, api *API, nodeID string) nightReadinessCheck {
	t.Helper()
	checks := api.h.nightCheckCatalogCurrent(context.Background(), testNow, "halloween-2026")
	name := nightCatalogCurrentCheckPrefix + ":" + nodeID
	for _, c := range checks {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no %s check found among %d checks", name, len(checks))
	return nightReadinessCheck{}
}

// TestNightCheckCatalogCurrent_ConflictedRevisionWithNoOverrideFails: a
// conflicted revision with no recorded override fails readiness rather
// than reading healthy merely because the acknowledged revision matches.
func TestNightCheckCatalogCurrent_ConflictedRevisionWithNoOverrideFails(t *testing.T) {
	api, st, _ := newNightCatalogReadinessOverrideFixture(t)
	required := resolveNightCatalogReadinessRevisionFor(t, st, "audio-01")

	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "audio-01", Revision: required, ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}

	check := nightCatalogCurrentCheckFor(t, api, "audio-01")
	if check.health != nightHealthFailed() {
		t.Fatalf("health = %q, want failed; reason: %s", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "no recorded operator override covers") {
		t.Fatalf("reason does not explain the uncovered conflict: %s", check.reason)
	}
}

// TestNightCheckCatalogCurrent_OverriddenConflictDegradesInsteadOfFailing:
// an override recorded for exactly the acknowledged revision downgrades
// to a named warning.
func TestNightCheckCatalogCurrent_OverriddenConflictDegradesInsteadOfFailing(t *testing.T) {
	api, st, _ := newNightCatalogReadinessOverrideFixture(t)
	required := resolveNightCatalogReadinessRevisionFor(t, st, "audio-01")

	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "audio-01", Revision: required, ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}
	if err := st.PutNodeCueCatalogOverride(context.Background(), store.NodeCueCatalogOverrideRecord{
		NodeID: "audio-01", Revision: required, ShowID: "halloween-2026", Generation: 1,
		Conflicts:                 []store.StoredCatalogConflict{{CueA: "cue-a", CueB: "cue-b", Claim: "program-audio-route:audio-01:usb-interface"}},
		OverriddenByPrincipalID:   "admin-1",
		OverriddenByPrincipalName: "Admin One",
		OverriddenAt:              testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogOverride: %v", err)
	}

	check := nightCatalogCurrentCheckFor(t, api, "audio-01")
	if check.health != nightHealthDegraded() {
		t.Fatalf("health = %q, want degraded (warned, not failed); reason: %s", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "operator override") {
		t.Fatalf("reason does not name the operator override: %s", check.reason)
	}
	if !strings.Contains(check.reason, "cue-a") || !strings.Contains(check.reason, "cue-b") {
		t.Fatalf("reason does not name both overridden cues: %s", check.reason)
	}
}

// TestNightCheckCatalogCurrent_OverrideForDifferentRevisionStillFails: an
// override naming a stale revision does not cover the current one, even
// though the acknowledgement matches.
func TestNightCheckCatalogCurrent_OverrideForDifferentRevisionStillFails(t *testing.T) {
	api, st, _ := newNightCatalogReadinessOverrideFixture(t)
	required := resolveNightCatalogReadinessRevisionFor(t, st, "audio-01")

	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "audio-01", Revision: required, ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}
	if err := st.PutNodeCueCatalogOverride(context.Background(), store.NodeCueCatalogOverrideRecord{
		NodeID: "audio-01", Revision: "a-prior-unrelated-revision", ShowID: "halloween-2026", Generation: 1,
		Conflicts:                 []store.StoredCatalogConflict{{CueA: "cue-a", CueB: "cue-b", Claim: "program-audio-route:audio-01:usb-interface"}},
		OverriddenByPrincipalID:   "admin-1",
		OverriddenByPrincipalName: "Admin One",
		OverriddenAt:              testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogOverride: %v", err)
	}

	check := nightCatalogCurrentCheckFor(t, api, "audio-01")
	if check.health != nightHealthFailed() {
		t.Fatalf("health = %q, want failed (the recorded override names a different revision); reason: %s", check.health, check.reason)
	}
}

// resolveNightCatalogReadinessRevisionFor is
// resolveNightCatalogReadinessRevision's own sibling for an arbitrary
// node, reused here for audio-01 rather than render-01.
func resolveNightCatalogReadinessRevisionFor(t *testing.T, st *store.Store, nodeID string) string {
	t.Helper()
	ctx := context.Background()
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		t.Fatalf("resolve active show: configured=%v err=%v", active.Configured, err)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("resolve cue catalog: %v", err)
	}
	return catalog.Revision
}
