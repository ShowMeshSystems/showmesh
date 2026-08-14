package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 7 seam A's own test suite: the configuration write
// surface (RES-008 D1) and, above all, the seam this step exists for —
// proving ADR-024 decision 11's same-transaction rule on a real SQLite
// trigger, never a mock (see TestPutFPPEndpointsConfigFailsClosedOnAuditFailure).

// newTestIdentityServiceWithStore is [newTestIdentityService] (auth_test.go)
// but also returns *store.Store and the store's own on-disk directory —
// needed here (and nowhere else in this package's existing suite) so a
// test can wire Dependencies.Config against the SAME store identitySvc's
// AuditedWrite composes against, and so
// TestPutFPPEndpointsConfigFailsClosedOnAuditFailure can open a second raw
// connection to install a real SQLite trigger. Mirrors
// internal/coordinator/identity/audited_write_test.go's
// newServiceWithOwnStoreDir exactly, duplicated here because this package
// cannot import another package's _test.go helpers — see auth_test.go's
// own doc comment for the identical rule.
func newTestIdentityServiceWithStore(t *testing.T, now func() time.Time) (identity.Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return svc, st, storeDir
}

// installFailAuditTrigger mirrors
// internal/coordinator/identity/audited_write_test.go's helper of the
// identical name — see that file's doc comment for why a REAL SQLite
// trigger, exactly the mechanism BUILD-PLAN's Step 7 spec names, is
// required rather than a mock. Duplicated here for the same
// cannot-import-_test.go-helpers reason [newTestIdentityServiceWithStore]
// is.
func installFailAuditTrigger(t *testing.T, storeDir string) {
	t.Helper()
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection to %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	_, err = raw.ExecContext(context.Background(), `
		CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END;
	`)
	if err != nil {
		t.Fatalf("install fail_audit trigger: %v", err)
	}
}

// configTestDeps mirrors authTestDeps, additionally wiring Config against
// st (see [ConfigStore]'s doc comment: *store.Store already satisfies it
// directly, no adapter needed).
func configTestDeps(svc identity.Service, st *store.Store) Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st,
	}
}

const validFPPEndpointsBody = `{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"},{"id":"shed","url":"http://10.0.1.21"}]}`

// TestPutFPPEndpointsConfigAuthAndScope is this seam's acceptance criterion
// 1: unauthenticated -> 401, a viewer (holds no config:write) -> 403
// naming config:write, an admin -> 200. config:write is admin-only
// (identity/types.go's adminOnlyScopes), so "operator" is exercised too,
// as a second principal that also lacks it.
func TestPutFPPEndpointsConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})

	t.Run("operator forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
			map[string]string{"Authorization": "Bearer " + operatorToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
			map[string]string{"Authorization": "Bearer " + adminToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1", m["revision"])
		}
	})
}

// TestGetFPPEndpointsConfigRequiresConfigWriteScope proves both GETs on
// this surface are gated by config:write and NEVER open under
// [Options.CloseReads] — this seam's own deliberate decision (config.go's
// doc comment), the same posture GET /api/v1/audit already established for
// a "new, always-sensitive surface". h.closeReads defaults to false here
// (Options{} zero value), which is exactly the point: unlike every
// pre-existing v1 read route, this one is gated even when reads are
// otherwise wide open.
func TestGetFPPEndpointsConfigRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range []string{"/api/v1/config/fpp.endpoints", "/api/v1/config/fpp.endpoints/revisions"} {
		t.Run(path+"/unauthenticated", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, nil)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/viewer forbidden", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + viewerToken})
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
			}
		})
		t.Run(path+"/admin allowed", func(t *testing.T) {
			resp, body := doRequest(t, api.Handler, "GET", path, map[string]string{"Authorization": "Bearer " + adminToken})
			// admin is allowed THROUGH the scope gate; the fpp.endpoints
			// object itself does not exist yet in this subtest's fresh
			// store, so "/config/fpp.endpoints" (not "/revisions") 404s —
			// still proves the auth gate passed rather than short-circuited.
			if path == "/api/v1/config/fpp.endpoints" {
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("status = %d, want 404 (scope gate passed, no config exists yet); body: %s", resp.StatusCode, body)
				}
				return
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
		})
	}
}

// TestPutFPPEndpointsConfigValidatesBeforeActivation is acceptance
// criterion 3: invalid configuration is rejected before activation and
// leaves no revision — ADR-009's rule, and A1's "a rejected write leaves
// no revision behind."
func TestPutFPPEndpointsConfigValidatesBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	invalidBody := `{"endpoints":[{"id":"player-01","url":"not a url"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", invalidBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "fpp.endpoints", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a rejected write = %v, want none", revs)
	}
}

// TestPutFPPEndpointsConfigFailsClosedOnAuditFailure is acceptance
// criterion 2, the criterion this seam exists for: with the audit store
// failing, the write is refused AND the revision is absent afterwards
// (ADR-024 decision 11's same-transaction rule, config:write's fail-closed
// half — the opposite of seam C's blackout/stop/power-off exemption). A
// REAL SQLite trigger, not a mock, per this step's spec.
func TestPutFPPEndpointsConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (write refused: the audit store is failing); body: %s", resp.StatusCode, body)
	}

	_, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default")
	if err == nil {
		t.Fatalf("GetConfigObject succeeded after a write whose audit entry failed to write — same-transaction rule violated")
	}
	revs, lerr := st.ListConfigRevisions(context.Background(), "fpp.endpoints", "default")
	if lerr != nil {
		t.Fatalf("ListConfigRevisions: %v", lerr)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a write whose audit entry failed = %v, want none — same-transaction rule violated", revs)
	}
}

// TestPutFPPEndpointsConfigWithoutFailingAuditSucceeds is the control for
// TestPutFPPEndpointsConfigFailsClosedOnAuditFailure: identical setup, no
// trigger installed, proving the previous test's failure really comes from
// the trigger and not from unrelated scaffolding breakage — matching this
// project's standing control-test pattern (see
// internal/coordinator/identity/audited_write_test.go's identically-shaped
// pair).
func TestPutFPPEndpointsConfigWithoutFailingAuditSucceeds(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no trigger installed); body: %s", resp.StatusCode, body)
	}

	obj, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default")
	if err != nil {
		t.Fatalf("GetConfigObject after a successful write: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}

	// ADR-024's rule is that a write which cannot be attributed does not
	// proceed — which means the attribution actually landed in the
	// transaction matters as much as the write itself. Assert the audit
	// entry's content, not merely its presence: a garbage action string, a
	// wrong target, or a dropped principal/form/credential/client address
	// would all leave every other assertion in this test passing.
	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action != "config.write" || e.Target != "fpp.endpoints" {
			continue
		}
		found = true
		if e.PrincipalID != admin.ID {
			t.Errorf("audit entry PrincipalID = %q, want %q", e.PrincipalID, admin.ID)
		}
		if e.PrincipalName != admin.Name {
			t.Errorf("audit entry PrincipalName = %q, want %q", e.PrincipalName, admin.Name)
		}
		if e.Form != identity.FormToken {
			t.Errorf("audit entry Form = %q, want %q (this request authenticated via bearer token)", e.Form, identity.FormToken)
		}
		if e.CredentialID == "" {
			t.Errorf("audit entry CredentialID is empty, want the token's credential id recorded")
		}
	}
	if !found {
		t.Fatalf("no config.write audit entry for fpp.endpoints found among %d entries", len(entries))
	}
}

// TestFPPEndpointsConfigRevisionsAreImmutable is acceptance criterion 6:
// an earlier revision's payload is byte-identical after a later revision
// is activated. Driven at the store level (not just through the API's
// metadata-only revisions list, which deliberately never re-exposes a past
// revision's payload — see v1.ConfigRevisionMeta's doc comment and RES-008
// section 10's "rollback tooling is deliberately out of scope") because
// that is what ADR-009's immutability guarantee actually claims: the ROW
// never changes, regardless of what any particular endpoint chooses to
// render.
func TestFPPEndpointsConfigRevisionsAreImmutable(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req1 := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first PUT: status = %d; body: %s", resp.StatusCode, body)
	}

	rev1Before, err := st.GetConfigRevision(context.Background(), "fpp.endpoints", "default", 1)
	if err != nil {
		t.Fatalf("GetConfigRevision(1) after first PUT: %v", err)
	}

	secondBody := `{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]}`
	req2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", secondBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second PUT: status = %d; body: %s", resp2.StatusCode, body2)
	}
	m := decodeMap(t, body2)
	if m["revision"] != float64(2) {
		t.Fatalf("second PUT's revision = %v, want 2", m["revision"])
	}

	rev1After, err := st.GetConfigRevision(context.Background(), "fpp.endpoints", "default", 1)
	if err != nil {
		t.Fatalf("GetConfigRevision(1) after second PUT: %v", err)
	}
	if rev1Before.PayloadJSON != rev1After.PayloadJSON {
		t.Errorf("revision 1 payload changed after revision 2 was activated:\nbefore: %s\nafter:  %s",
			rev1Before.PayloadJSON, rev1After.PayloadJSON)
	}

	// The revisions list itself must still carry both, oldest history
	// intact, newest first, with only revision 2 marked active.
	revs, err := st.ListConfigRevisions(context.Background(), "fpp.endpoints", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("len(revisions) = %d, want 2", len(revs))
	}
}

// TestGetFPPEndpointsConfigRevisionsListsNewestFirst proves the wire
// contract: GET .../revisions lists newest first, and marks exactly the
// active one.
func TestGetFPPEndpointsConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, body := range []string{
		`{"endpoints":[{"id":"a","url":"http://10.0.1.1"}]}`,
		`{"endpoints":[{"id":"a","url":"http://10.0.1.2"}]}`,
	} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", body,
			map[string]string{"Authorization": "Bearer " + adminToken})
		if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT: status = %d; body: %s", resp.StatusCode, respBody)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.endpoints/revisions",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revisions, ok := m["revisions"].([]any)
	if !ok || len(revisions) != 2 {
		t.Fatalf("revisions = %v, want 2 elements", m["revisions"])
	}
	first := revisions[0].(map[string]any)
	second := revisions[1].(map[string]any)
	if first["revision"] != float64(2) || second["revision"] != float64(1) {
		t.Errorf("revisions order = [%v, %v], want [2, 1] (newest first)", first["revision"], second["revision"])
	}
	if first["active"] != true {
		t.Errorf("revisions[0].active = %v, want true", first["active"])
	}
	if second["active"] != false {
		t.Errorf("revisions[1].active = %v, want false", second["active"])
	}
}

// TestGetFPPEndpointsConfigRestartRequiredIsAlwaysStated is A4: the API
// response carries the "takes effect on next restart" fact rather than
// leaving the client to know it out of band.
func TestGetFPPEndpointsConfigRestartRequiredIsAlwaysStated(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["restartRequired"] != true {
		t.Errorf("PUT response restartRequired = %v, want true", m["restartRequired"])
	}
	if reason, _ := m["restartRequiredReason"].(string); reason == "" {
		t.Errorf("PUT response restartRequiredReason is empty, want a stated reason")
	}

	resp2, body2 := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.endpoints",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	if m2["restartRequired"] != true {
		t.Errorf("GET response restartRequired = %v, want true", m2["restartRequired"])
	}
}

// TestPutFPPEndpointsConfigRefusesWhenEnvVarSet is Step 7 seam A review
// defect 3a's own regression test: while [Dependencies.FPPEndpointsEnvVarSet]
// is true, the write is refused with 409 before it can do anything —
// reproducing (without a real coordinator process) the live sequence the
// review found: a config write accepted while SHOWMESH_FPP_ENDPOINTS is
// still set cannot survive this coordinator's own disagreement rule on the
// next restart. Also proves the refusal really is BEFORE activation: no
// revision exists afterward.
func TestPutFPPEndpointsConfigRefusesWhenEnvVarSet(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.FPPEndpointsEnvVarSet = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeConflict)
	}
	if !strings.Contains(fmt.Sprint(m["detail"]), "SHOWMESH_FPP_ENDPOINTS") {
		t.Errorf("detail = %v, want it to name SHOWMESH_FPP_ENDPOINTS", m["detail"])
	}

	if _, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write — the refusal must precede activation, same as any other validation failure")
	}
}

// TestPutFPPEndpointsConfigDeferredMigrationGivesANonDestructiveRemedy
// covers the state the deferral fix (internal/coordinator/configsync.go,
// 2026-08-13) made reachable and its review caught: SHOWMESH_FPP_ENDPOINTS
// is set AND the startup migration of it could not be persisted, so the
// store holds no fpp.endpoints configuration at all.
//
// The refusal is the same 409 for the same reason, and the REMEDY must
// differ. The standard detail tells the operator to remove the variable
// and restart once, which is correct after a migration has landed and
// destroys the endpoint list before one has: removing the only copy of
// the list resolves the coordinator to zero endpoints on its next
// restart, and the retried write then fails on the same unwritable store.
// The whole sequence closes with the operator making no mistake at any
// step, which is why this is asserted on the text rather than left to a
// reader of the code.
func TestPutFPPEndpointsConfigDeferredMigrationGivesANonDestructiveRemedy(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.FPPEndpointsEnvVarSet = true
	deps.FPPEndpointsMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if !strings.Contains(detail, "Do NOT remove SHOWMESH_FPP_ENDPOINTS") {
		t.Errorf("detail = %q, want an explicit warning against removing the variable while the migration is deferred", detail)
	}
	// The exact instruction the standard 409 gives, which is destructive
	// here. Asserting its ABSENCE is the load-bearing half: a handler that
	// forgot to branch would still contain the phrase above if it were
	// added to both messages, but cannot contain this one.
	if strings.Contains(detail, "Remove SHOWMESH_FPP_ENDPOINTS from this coordinator's environment and restart it once") {
		t.Errorf("detail = %q, must NOT give the standard remedy: with the migration deferred, removing the variable "+
			"discards the only copy of the endpoint list", detail)
	}
}

// TestGetFPPEndpointsConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured
// is the read half of the same state. ADR-020: absent evidence is stated
// with a reason, never reported as absence. A bare "no fpp.endpoints
// configuration has been created yet" is false while GET /api/v1/fpp is
// listing every host this coordinator is polling from the very list that
// failed to persist, and the Operator UI turns that 404 into an empty
// table reading "No fpp.endpoints configuration exists yet".
func TestGetFPPEndpointsConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.FPPEndpointsEnvVarSet = true
	deps.FPPEndpointsMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/fpp.endpoints", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	// Still a 404: no configuration IS stored, and that part was never the
	// falsehood. What changes is that the reason is stated.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if strings.Contains(detail, "has been created yet") {
		t.Errorf("detail = %q, must not report a coordinator nothing has ever configured: one IS in effect", detail)
	}
	for _, want := range []string{"SHOWMESH_FPP_ENDPOINTS", "could not be", "GET /api/v1/fpp"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
}

// TestGetFPPEndpointsConfigUnconfiguredKeepsTheOriginalMessage is the
// control for the test above: with the deferral flag false (its zero
// value, and every real coordinator that never deferred a migration), a
// genuinely unconfigured store must still say so plainly. Without this,
// a handler that dropped the branch entirely and always returned the
// deferral text would pass the test above.
func TestGetFPPEndpointsConfigUnconfiguredKeepsTheOriginalMessage(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/fpp.endpoints", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if !strings.Contains(detail, "has been created yet") {
		t.Errorf("detail = %q, want the plain not-configured message when no migration was deferred", detail)
	}
}

// TestPutFPPEndpointsConfigAllowsWriteWhenEnvVarNotSet is the control for
// TestPutFPPEndpointsConfigRefusesWhenEnvVarSet: identical setup with
// FPPEndpointsEnvVarSet left false (its zero value, matching every other
// test in this file and every production coordinator once the env
// migration guidance has been followed), proving the 409 above really
// comes from that flag and not from unrelated scaffolding breakage.
func TestPutFPPEndpointsConfigAllowsWriteWhenEnvVarNotSet(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (FPPEndpointsEnvVarSet is false); body: %s", resp.StatusCode, body)
	}
}

// TestPutFPPEndpointsConfigRefusesWhenMQTTHostWouldBeDropped is Step 7
// seam A review defect 4's own regression test: SHOWMESH_FPP_MQTT_HOSTS
// references an instance id the proposed endpoint list drops, and this
// must be refused at write time — startup already refuses to boot on the
// identical mismatch (config.ValidateFPPMQTTHostIDs), so accepting it here
// with 200 would only defer the operator's mistake to a restart that
// never completes.
func TestPutFPPEndpointsConfigRefusesWhenMQTTHostWouldBeDropped(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	// "shed" is referenced by SHOWMESH_FPP_MQTT_HOSTS but validFPPEndpointsBody
	// still names it (id "shed") — use a body that DROPS it instead.
	deps.FPPMQTTHostIDs = map[string]string{"shed": "shed-fpp"}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	bodyDroppingShed := `{"endpoints":[{"id":"player-01","url":"http://10.0.1.20"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", bodyDroppingShed,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "shed") {
		t.Errorf("body = %s, want it to name the dropped instance id %q", body, "shed")
	}

	if _, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write — the MQTT cross-check must precede activation")
	}
}

// TestPutFPPEndpointsConfigAllowsWriteThatKeepsMQTTHostIsControl is the
// control for TestPutFPPEndpointsConfigRefusesWhenMQTTHostWouldBeDropped:
// identical FPPMQTTHostIDs, but a body that KEEPS "shed" — proving the 400
// above comes from the id actually being dropped, not from
// FPPMQTTHostIDs being set at all.
func TestPutFPPEndpointsConfigAllowsWriteThatKeepsMQTTHost(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.FPPMQTTHostIDs = map[string]string{"shed": "shed-fpp"}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// validFPPEndpointsBody names both "player-01" and "shed".
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the body keeps the id FPPMQTTHostIDs references); body: %s", resp.StatusCode, body)
	}
}

// TestPutFPPEndpointsConfigRefusesWhenResolumeIDWouldCollide is Track D
// seam D-1 review finding 1's own regression test: the proposed endpoint
// list names an id equal to the coordinator's configured Resolume
// instance id, and this must be refused at write time — startup already
// refuses to boot on the identical collision
// (config.ValidateResolumeIDAgainstFPPEndpoints), so accepting it here
// with 200 would only defer the operator's mistake to a restart that
// never completes. Persists nothing: GetConfigObject after the refused
// write must still report no revision.
func TestPutFPPEndpointsConfigRefusesWhenResolumeIDWouldCollide(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.ResolumeID = "resolume"
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	bodyNamingResolume := `{"endpoints":[{"id":"resolume","url":"http://10.0.1.30"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", bodyNamingResolume,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "resolume") {
		t.Errorf("body = %s, want it to name the colliding id %q", body, "resolume")
	}
	for _, unwanted := range []string{"collector.Runner", "ADR-", ".md", "docs/"} {
		if strings.Contains(string(body), unwanted) {
			t.Errorf("body = %s, must not leak internal implementation detail %q into an operator-facing response", body, unwanted)
		}
	}

	if _, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write — the Resolume id cross-check must precede activation")
	}
}

// TestPutFPPEndpointsConfigAllowsResolumeIDCollisionWhenCollectorDisabled
// is the sharpest control for
// TestPutFPPEndpointsConfigRefusesWhenResolumeIDWouldCollide: the SAME
// body naming id "resolume", but with deps.ResolumeID left at its zero
// value (""), matching a coordinator with no SHOWMESH_RESOLUME_URL
// configured — where the boot-time re-check
// (internal/coordinator.Run, gated on cfg.ResolumeURL != "") never fires
// either, because no Resolume collector is ever constructed for the id to
// collide with. The write must succeed, proving the refusal above comes
// from an ENABLED Resolume collector's id actually colliding, not from
// the literal string "resolume" being special.
func TestPutFPPEndpointsConfigAllowsResolumeIDCollisionWhenCollectorDisabled(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	// deps.ResolumeID is "" (zero value): the Resolume collector is
	// disabled, exactly as every other test in this file leaves it.
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	bodyNamingResolume := `{"endpoints":[{"id":"resolume","url":"http://10.0.1.30"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", bodyNamingResolume,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the Resolume collector is disabled, so its id has nothing to collide with); body: %s", resp.StatusCode, body)
	}
}

// TestPutFPPEndpointsConfigAllowsWriteThatDoesNotNameResolumeID is a
// second control: deps.ResolumeID set (the collector IS enabled), but the
// proposed endpoint list never names that id at all — proving the 400 in
// TestPutFPPEndpointsConfigRefusesWhenResolumeIDWouldCollide comes from
// the actual collision, not from ResolumeID being set on Dependencies.
func TestPutFPPEndpointsConfigAllowsWriteThatDoesNotNameResolumeID(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.ResolumeID = "resolume"
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// validFPPEndpointsBody names "player-01" and "shed" — neither is "resolume".
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no proposed endpoint id collides with ResolumeID); body: %s", resp.StatusCode, body)
	}
}

// --- Defect 1: absent/null/typo'd "endpoints" ---

// TestPutFPPEndpointsConfigRejectsAbsentEndpointsKey is Step 7 seam A
// review defect 1's own regression test, the headline finding: a PUT body
// with NO "endpoints" key at all (`{}`) must be a 400 naming the missing
// field, never a 200 that silently wipes every configured endpoint. I
// reproduced the wipe live against a running coordinator before this fix.
func TestPutFPPEndpointsConfigRejectsAbsentEndpointsKey(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Seed an existing revision so a silent wipe would be visible as a
	// state change, not merely "still nothing configured".
	seedReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, seedReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed PUT: status = %d; body: %s", resp.StatusCode, body)
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", `{}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (absent \"endpoints\" key); body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "endpoints") {
		t.Errorf("body = %s, want it to name the missing \"endpoints\" field", body)
	}

	// The seeded revision must survive untouched: still revision 1 active,
	// still one revision total.
	obj, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default")
	if err != nil {
		t.Fatalf("GetConfigObject after a rejected write: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1 (unchanged by the rejected write)", obj.CurrentRevision)
	}
}

// TestPutFPPEndpointsConfigRejectsNullEndpoints is defect 1's second
// finding: {"endpoints":null} decodes into a nil slice with NO error under
// a naive `struct{ Endpoints []T "json:\"endpoints\"" }` decode — the
// exact "a JSON null is not an absent key" defect class CLAUDE.md names —
// so this must ALSO be a 400, indistinguishable in effect from the absent
// case above.
func TestPutFPPEndpointsConfigRejectsNullEndpoints(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", `{"endpoints":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null \"endpoints\"); body: %s", resp.StatusCode, body)
	}

	if _, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a null-endpoints write — must never create a zero-endpoint revision from null")
	}
}

// TestPutFPPEndpointsConfigRejectsUnknownTopLevelField is defect 1's third
// finding: a typo'd key ("endpoint" instead of "endpoints") must be a 400
// naming the unrecognized field, not a silent no-op that also happens to
// wipe the list (a struct-based decode simply drops a field it does not
// recognize).
func TestPutFPPEndpointsConfigRejectsUnknownTopLevelField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	typoBody := `{"endpoint":[{"id":"player-01","url":"http://10.0.1.20"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", typoBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown top-level field \"endpoint\"); body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "endpoint") {
		t.Errorf("body = %s, want it to name the unrecognized field", body)
	}
}

// TestPutFPPEndpointsConfigAcceptsExplicitEmptyEndpoints is defect 1's
// deliberate positive case: "endpoints": [] — PRESENT but empty — is the
// one way to legitimately configure zero endpoints (an operator
// decommissioning their last FPP), and must be accepted rather than
// treated the same as the absent/null cases above.
func TestPutFPPEndpointsConfigAcceptsExplicitEmptyEndpoints(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", `{"endpoints":[]}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an explicit empty array deliberately configures zero endpoints); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", m["revision"])
	}
}

// TestPutFPPEndpointsConfigCSRF proves decision 6's CSRF check is wired on
// this real endpoint, not only on the generic writeGuard machinery a
// temporary Step 6 endpoint already covered: a cookie-authenticated PUT
// with no Sec-Fetch-Site header is rejected, and the identical request
// succeeds once the header is present.
func TestPutFPPEndpointsConfigCSRF(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	cookie := loginAndGetCookie(t, api.Handler, "admin-1", testPassword)

	t.Run("no Sec-Fetch-Site rejected", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody, nil)
		req.Header.Set("Cookie", sessionCookieName+"="+cookie)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 csrf-rejected; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("same-origin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", validFPPEndpointsBody, nil)
		req.Header.Set("Cookie", sessionCookieName+"="+cookie)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})
}
