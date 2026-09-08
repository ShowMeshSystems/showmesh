package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Part 1/Part 3's own coverage for the night session's
// per-participating-node catalog-currency readiness check
// (nightcatalogreadiness.go): a node's stale or never-acknowledged
// catalog acknowledgement fails readiness naming both revisions, unless a
// deploy is currently held safely pending for it, in which case it warns
// instead. See that file's own doc comment.

const nightCatalogReadinessCueBody = `{
	"show": "halloween-2026",
	"name": "Thriller",
	"outputs": {
		"render": {"sequence": "thriller"}
	}
}`

// newNightCatalogReadinessFixture wires an admin API with one declared,
// participating node (render-01: a show.surface assignment plus a
// referenced show.cue give it a real, non-empty resolved catalog), the
// active show already set, and [Dependencies.CurrentRuns] fixed to runs,
// so cueCatalogAutoDeployHold's own verdict is controlled directly by the
// test rather than by faking FPP/audio observation evidence underneath it.
func newNightCatalogReadinessFixture(t *testing.T, runs []currentrun.Run) (*API, *store.Store, string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.CurrentRuns = currentRunsReaderFake{snapshot: currentrun.Snapshot{Runs: runs}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	putSurfaceReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", validSurfaceBodyNDI, auth)
	if resp, body := doRawRequest(t, api.Handler, putSurfaceReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	putCueReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", nightCatalogReadinessCueBody, auth)
	if resp, body := doRawRequest(t, api.Handler, putCueReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue/thriller: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	playlistBody := `{
		"show": "halloween-2026",
		"name": "Main show",
		"runner": "fpp",
		"mismatchPolicy": "hold",
		"fpp": {
			"instanceUuid": "11111111-1111-1111-1111-111111111111",
			"playlistName": "Halloween Main",
			"playlistHash": "` + playlistHash64 + `"
		},
		"entries": [
			{"id": "e1", "cue": "thriller", "fpp": {"section": "mainPlaylist", "position": 0}}
		]
	}`
	putPlaylistReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", playlistBody, auth)
	if resp, body := doRawRequest(t, api.Handler, putPlaylistReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	mustPutShowActive(t, api, token, "halloween-2026")
	return api, st, token
}

// resolveNightCatalogReadinessRevision returns render-01's currently
// required catalog revision, the same resolver
// nightCheckNodeCatalogCurrent itself calls.
func resolveNightCatalogReadinessRevision(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		t.Fatalf("resolve active show: configured=%v err=%v", active.Configured, err)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("resolve cue catalog: %v", err)
	}
	return catalog.Revision
}

func nightCatalogCurrentCheck(t *testing.T, api *API) nightReadinessCheck {
	t.Helper()
	checks := api.h.nightCheckCatalogCurrent(context.Background(), testNow, "halloween-2026")
	for _, c := range checks {
		if c.name == nightCatalogCurrentCheckPrefix+":render-01" {
			return c
		}
	}
	t.Fatalf("no %s check found among %d checks", nightCatalogCurrentCheckPrefix+":render-01", len(checks))
	return nightReadinessCheck{}
}

// TestNightCheckCatalogCurrent_FailsNamingBothRevisions is acceptance
// criterion 3: a stale node with nothing running (and so nothing holding
// a deploy for it) fails readiness, and the reason names both the
// required and the acknowledged revision.
func TestNightCheckCatalogCurrent_FailsNamingBothRevisions(t *testing.T) {
	api, st, _ := newNightCatalogReadinessFixture(t, nil)
	required := resolveNightCatalogReadinessRevision(t, st)
	const staleRevision = "47da18900000000000000000000000000000000000000000000000000000"
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: staleRevision, ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}

	check := nightCatalogCurrentCheck(t, api)
	if check.health != nightHealthFailed() {
		t.Fatalf("health = %q, want failed; reason: %s", check.health, check.reason)
	}
	if !strings.Contains(check.reason, required) {
		t.Fatalf("reason does not name the required revision %q: %s", required, check.reason)
	}
	if !strings.Contains(check.reason, staleRevision) {
		t.Fatalf("reason does not name the acknowledged revision %q: %s", staleRevision, check.reason)
	}
}

// TestNightCheckCatalogCurrent_HealthyWhenAcknowledged proves the
// counterpart: a node that has acknowledged the exact current revision
// reads healthy, with no hold or warning language at all.
func TestNightCheckCatalogCurrent_HealthyWhenAcknowledged(t *testing.T) {
	api, st, _ := newNightCatalogReadinessFixture(t, nil)
	required := resolveNightCatalogReadinessRevision(t, st)
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: required, ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}

	check := nightCatalogCurrentCheck(t, api)
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %q, want healthy; reason: %s", check.health, check.reason)
	}
}

// TestNightCheckCatalogCurrent_WarnsWhileDeployHeldForRunningCue is
// acceptance criterion 2's own readiness half: a stale node with a
// currently playing FPP run warns rather than fails, and the aggregate
// outcome stays ready: a held deploy is expected to apply on its own,
// not to block the night from starting.
func TestNightCheckCatalogCurrent_WarnsWhileDeployHeldForRunningCue(t *testing.T) {
	api, st, _ := newNightCatalogReadinessFixture(t, []currentrun.Run{
		{ID: "fpp:player-01", Runner: currentrun.RunnerFPP, Playback: currentrun.Playback{State: "playing"}},
	})
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: "stale-revision", ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}

	check := nightCatalogCurrentCheck(t, api)
	// health stays [nightHealthHealthy] deliberately (see
	// nightCheckNodeCatalogCurrent's own doc comment): nightReadinessCheck
	// has no separate "warning, still ready" outcome bucket, so a held,
	// pending-safe deploy is reported healthy with an explanatory reason
	// rather than any state that would flip the aggregate outcome away
	// from ready. [nightHealthSeverity]/nightComputeReadinessChecks'
	// aggregation is pre-existing, unchanged code this test does not
	// re-verify.
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %q, want healthy (warned, not failed); reason: %s", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "held") {
		t.Fatalf("reason does not explain the hold: %s", check.reason)
	}
	if strings.Contains(check.reason, "stale or unreadable") {
		t.Fatalf("a genuinely playing run must not be reported as uncertain evidence: %s", check.reason)
	}
}

// TestNightCheckCatalogCurrent_WarnsWithUncertainEvidenceNamedSeparately
// proves the owner's own distinction: a hold caused by evidence this
// coordinator cannot currently confirm is worded differently from a hold
// caused by a genuinely running Cue, so an operator knows whether there
// is anything to go look at.
func TestNightCheckCatalogCurrent_WarnsWithUncertainEvidenceNamedSeparately(t *testing.T) {
	api, st, _ := newNightCatalogReadinessFixture(t, []currentrun.Run{
		{ID: "fpp:player-01", Runner: currentrun.RunnerFPP, Playback: currentrun.Playback{State: "unavailable"}},
	})
	if err := st.PutNodeCueCatalogAck(context.Background(), store.NodeCueCatalogAckRecord{
		NodeID: "render-01", Revision: "stale-revision", ShowID: "halloween-2026", Generation: 1, AcknowledgedAt: testNow,
	}); err != nil {
		t.Fatalf("PutNodeCueCatalogAck: %v", err)
	}

	check := nightCatalogCurrentCheck(t, api)
	if check.health != nightHealthHealthy() {
		t.Fatalf("health = %q, want healthy (warned, not failed); reason: %s", check.health, check.reason)
	}
	if !strings.Contains(check.reason, "stale or unreadable") {
		t.Fatalf("reason does not name uncertain evidence as the reason for the hold: %s", check.reason)
	}
}
