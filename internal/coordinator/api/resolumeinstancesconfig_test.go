package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track G seam G-2's own test suite (ADR-039), mirroring
// config_test.go's fpp.endpoints coverage for the identical shape: the
// configuration write surface, ADR-024 decision 11's same-transaction
// rule, the still-set-env-var 409 (including its deferred-migration
// remedy correction), and the absent/null/empty payload discipline —
// applied to resolume.instances rather than re-proving ADR-024 decision
// 11 a second, redundant way (that proof belongs to fpp.endpoints; this
// suite proves this HANDLER wires into it correctly).

const validResolumeInstancesBody = `{"instances":[{"id":"arena-1","url":"http://10.0.1.30:8080"}]}`

// TestPutResolumeInstancesConfigAuthAndScope mirrors
// TestPutFPPEndpointsConfigAuthAndScope.
func TestPutResolumeInstancesConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "config:write") {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
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

func TestGetResolumeInstancesConfigRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range []string{"/api/v1/config/resolume.instances", "/api/v1/config/resolume.instances/revisions"} {
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
			if path == "/api/v1/config/resolume.instances" {
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

func TestPutResolumeInstancesConfigValidatesBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	invalidBody := `{"instances":[{"id":"arena-1","url":"not a url"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", invalidBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "resolume.instances", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a rejected write = %v, want none", revs)
	}
}

// TestPutResolumeInstancesConfigRejectsMoreThanOne is Track G seam G-2's
// own spec: the schema stays a list, but at most one instance is accepted,
// enforced by validation.
func TestPutResolumeInstancesConfigRejectsMoreThanOne(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	twoBody := `{"instances":[{"id":"arena-1","url":"http://10.0.1.30:8080"},{"id":"arena-2","url":"http://10.0.1.31:8080"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", twoBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestPutResolumeInstancesConfigFailsClosedOnAuditFailure mirrors
// TestPutFPPEndpointsConfigFailsClosedOnAuditFailure: a real SQLite
// trigger, not a mock, proving ADR-024 decision 11's same-transaction rule
// holds for this kind too.
func TestPutResolumeInstancesConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (write refused: the audit store is failing); body: %s", resp.StatusCode, body)
	}

	_, err := st.GetConfigObject(context.Background(), "resolume.instances", "default")
	if err == nil {
		t.Fatalf("GetConfigObject succeeded after a write whose audit entry failed to write — same-transaction rule violated")
	}
}

func TestPutResolumeInstancesConfigRefusesWhenEnvVarSet(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.ResolumeInstancesEnvVarSet = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeConflict {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeConflict)
	}
	if !strings.Contains(fmt.Sprint(m["detail"]), "SHOWMESH_RESOLUME_URL") {
		t.Errorf("detail = %v, want it to name SHOWMESH_RESOLUME_URL", m["detail"])
	}

	if _, err := st.GetConfigObject(context.Background(), "resolume.instances", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write")
	}
}

// TestPutResolumeInstancesConfigDeferredMigrationGivesANonDestructiveRemedy
// mirrors TestPutFPPEndpointsConfigDeferredMigrationGivesANonDestructiveRemedy:
// the remedy must differ from the standard one while the migration is
// deferred, because the standard remedy would discard the operator's only
// copy of the instance.
func TestPutResolumeInstancesConfigDeferredMigrationGivesANonDestructiveRemedy(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.ResolumeInstancesEnvVarSet = true
	deps.ResolumeInstancesMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if !strings.Contains(detail, "Do NOT remove SHOWMESH_RESOLUME_URL") {
		t.Errorf("detail = %q, want an explicit warning against removing the variable while the migration is deferred", detail)
	}
	if strings.Contains(detail, "Remove SHOWMESH_RESOLUME_URL and SHOWMESH_RESOLUME_ID and restart this coordinator once, then retry.") {
		t.Errorf("detail = %q, must NOT give the standard remedy while the migration is deferred", detail)
	}
}

// TestGetResolumeInstancesConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured
// mirrors the FPP read-side equivalent: absent evidence is stated with a
// reason, never reported as absence.
func TestGetResolumeInstancesConfigDeferredMigrationStatesItRatherThanReportingNothingConfigured(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.ResolumeInstancesEnvVarSet = true
	deps.ResolumeInstancesMigrationDeferred = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/resolume.instances", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	detail := fmt.Sprint(decodeMap(t, body)["detail"])
	if strings.Contains(detail, "has been created yet") {
		t.Errorf("detail = %q, must not report a coordinator nothing has ever configured: one IS in effect", detail)
	}
	for _, want := range []string{"SHOWMESH_RESOLUME_URL", "could not be", "GET /api/v1/resolume/instances"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to contain %q", detail, want)
		}
	}
}

// --- absent/null/empty ---

func TestPutResolumeInstancesConfigRejectsAbsentInstancesKey(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// Seed an existing revision so a silent wipe would be visible as a
	// state change.
	seedReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, seedReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed PUT: status = %d; body: %s", resp.StatusCode, body)
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", `{}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (absent \"instances\" key); body: %s", resp.StatusCode, body)
	}

	// The seeded revision must survive: a rejected write must not touch it.
	obj, err := st.GetConfigObject(context.Background(), "resolume.instances", "default")
	if err != nil {
		t.Fatalf("GetConfigObject after rejected write: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1 (the seeded revision must survive a rejected write)", obj.CurrentRevision)
	}
}

func TestPutResolumeInstancesConfigRejectsNullInstances(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", `{"instances":null}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (null \"instances\"); body: %s", resp.StatusCode, body)
	}
}

func TestPutResolumeInstancesConfigRejectsUnknownTopLevelField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances",
		`{"instance":[{"id":"arena-1","url":"http://10.0.1.30:8080"}]}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (typo'd top-level key); body: %s", resp.StatusCode, body)
	}
}

// TestPutResolumeInstancesConfigAcceptsExplicitEmptyInstances is decision
// 5's own positive case: an explicit empty array is the ONLY way to
// deliberately configure zero instances, and it must be accepted, not
// confused with absent/null.
func TestPutResolumeInstancesConfigAcceptsExplicitEmptyInstances(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", `{"instances":[]}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an explicit empty array deliberately configures zero instances); body: %s", resp.StatusCode, body)
	}
}

// --- collision with fpp.endpoints, both directions ---

// TestPutResolumeInstancesConfigRefusesWhenFPPEndpointWouldCollideLive
// proves the resolume.instances write checks the CURRENT fpp.endpoints
// list, read live through Dependencies.FPP — not a value cached at
// startup, since fpp.endpoints has applied without a restart since
// ADR-036.
func TestPutResolumeInstancesConfigRefusesWhenFPPEndpointWouldCollideLive(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "arena-1", Endpoint: "http://10.0.1.20"}}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", validResolumeInstancesBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (arena-1 collides with a live fpp.endpoints id); body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "arena-1") {
		t.Errorf("body = %s, want it to name the colliding id", body)
	}

	if _, err := st.GetConfigObject(context.Background(), "resolume.instances", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write")
	}
}

// TestPutFPPEndpointsConfigRefusesWhenResolumeInstanceWouldCollideLive is
// the REVERSE direction, newly added by this seam: the fpp.endpoints write
// path must also check the CURRENT resolume.instances configuration, read
// live through Dependencies.Resolume — catching a collision even when
// Dependencies.ResolumeID (the startup snapshot) does not name it, because
// a resolume.instances write landing after this coordinator started is
// invisible to that static field.
func TestPutFPPEndpointsConfigRefusesWhenResolumeInstanceWouldCollideLive(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)

	deps := configTestDeps(svc, st)
	// deps.ResolumeID is deliberately left "" (the static snapshot sees no
	// Resolume collector at all) — only the LIVE ResolumeLister reports the
	// configured instance, proving the refusal below comes from the live
	// check this seam added, not from the pre-existing static one.
	deps.Resolume = &fakeResolumeLister{views: []ResolumeInstanceView{{InstanceID: "arena-1"}}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	bodyNamingArena1 := `{"endpoints":[{"id":"arena-1","url":"http://10.0.1.20"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.endpoints", bodyNamingArena1,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (arena-1 collides with a live resolume.instances id); body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "arena-1") {
		t.Errorf("body = %s, want it to name the colliding id", body)
	}

	if _, err := st.GetConfigObject(context.Background(), "fpp.endpoints", "default"); err == nil {
		t.Fatalf("GetConfigObject succeeded after a refused write")
	}
}

func TestGetResolumeInstancesConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, body := range []string{
		`{"instances":[{"id":"arena-1","url":"http://10.0.1.30:8080"}]}`,
		`{"instances":[{"id":"arena-1","url":"http://10.0.1.31:8080"}]}`,
	} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", body,
			map[string]string{"Authorization": "Bearer " + adminToken})
		if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT: status = %d; body: %s", resp.StatusCode, respBody)
		}
	}

	req := newJSONRequest(t, http.MethodGet, "/api/v1/config/resolume.instances/revisions", "",
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revs, ok := m["revisions"].([]any)
	if !ok || len(revs) != 2 {
		t.Fatalf("revisions = %v, want 2 entries", m["revisions"])
	}
	first := revs[0].(map[string]any)
	if first["revision"] != float64(2) || first["active"] != true {
		t.Errorf("newest revision = %v, want revision 2, active", first)
	}
}

// TestPutResolumeInstancesConfigRevisionPreconditionWiring is a smoke test
// proving handlePutResolumeInstancesConfig actually threads the shared
// revision precondition through to its own call site - checked AFTER the
// still-set-env-var refusal and BEFORE the body is read, exactly like
// every other singleton kind. The full behavioural matrix lives once, on
// the representative kind fpp.endpoints (config_test.go).
func TestPutResolumeInstancesConfigRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	put := func(port string, headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + adminToken}
		for k, v := range headers {
			h[k] = v
		}
		body := fmt.Sprintf(`{"instances":[{"id":"arena-1","url":"http://10.0.1.30:%s"}]}`, port)
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.instances", body, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := put("8080", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional write: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if resp, body := put("8081", map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	resp, body := put("8082", map[string]string{"If-Match": `"1"`})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.instances", map[string]string{"Authorization": "Bearer " + adminToken})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d; body: %s", getResp.StatusCode, getBody)
	}
	if !containsAll(string(getBody), `:8081"`) {
		t.Errorf("the matching-If-Match writer's port (8081) should have survived the refused stale write; body: %s", getBody)
	}
	if containsAll(string(getBody), ":8082") {
		t.Errorf("the refused write's port (8082) must never have been persisted; body: %s", getBody)
	}
}
