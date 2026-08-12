package api

import (
	"context"
	"database/sql"
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
