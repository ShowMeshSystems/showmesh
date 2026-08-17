package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

const validRenderSettingsBody = `{"idleOutput":"hold","restartPolicy":{"initialDelaySeconds":2,"maxDelaySeconds":45,"maxConsecutiveFastFailures":6}}`

// TestGetRenderSettingsConfigUnconfiguredReportsDefault proves this kind
// NEVER 404s (unlike fpp.endpoints): nothing has ever been written, so this
// answers 200 with revision 0, source "default", and the built-in default
// payload — mirrors GET /config/resolume.recovery's identical posture.
func TestGetRenderSettingsConfigUnconfiguredReportsDefault(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/render.settings",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["revision"] != float64(0) {
		t.Errorf("revision = %v, want 0", m["revision"])
	}
	if m["source"] != "default" {
		t.Errorf("source = %v, want default", m["source"])
	}
	payload, _ := m["payload"].(map[string]any)
	if payload["idleOutput"] != "black" {
		t.Errorf("payload.idleOutput = %v, want black", payload["idleOutput"])
	}
}

// TestGetRenderSettingsConfigRequiresConfigWriteScope proves this surface
// is gated by config:write for every read, mirroring
// TestGetFPPEndpointsConfigRequiresConfigWriteScope and
// TestGetResolumeRecoveryConfigRequiresConfigWriteScope.
func TestGetRenderSettingsConfigRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range []string{"/api/v1/config/render.settings", "/api/v1/config/render.settings/revisions"} {
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
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestPutRenderSettingsConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody,
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
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody,
			map[string]string{"Authorization": "Bearer " + operatorToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody,
			map[string]string{"Authorization": "Bearer " + adminToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		m := decodeMap(t, body)
		if m["revision"] != float64(1) {
			t.Errorf("revision = %v, want 1", m["revision"])
		}
		if m["source"] != "api" {
			t.Errorf("source = %v, want api", m["source"])
		}
	})
}

// TestPutRenderSettingsConfigValidatesBeforeActivation is ADR-009's rule: a
// rejected write leaves no revision behind.
func TestPutRenderSettingsConfigValidatesBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	invalidBody := `{"idleOutput":"strobe","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", invalidBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "render.settings", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a rejected write = %v, want none", revs)
	}
}

// TestPutRenderSettingsConfigRejectsAbsentIdleOutputKey proves the "an
// absent key is not an empty value" rule holds on the real HTTP path, not
// only inside config.DecodeRenderSettingsPayload's own unit tests.
func TestPutRenderSettingsConfigRejectsAbsentIdleOutputKey(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", body,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "idleOutput") {
		t.Errorf("body = %s, want it to name idleOutput", respBody)
	}
}

// TestPutRenderSettingsConfigFailsClosedOnAuditFailure is ADR-024 decision
// 11's same-transaction rule, proved against a REAL SQLite trigger —
// mirrors TestPutFPPEndpointsConfigFailsClosedOnAuditFailure exactly.
func TestPutRenderSettingsConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "render.settings", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a failed-audit write = %v, want none", revs)
	}
}

// TestGetRenderSettingsConfigRevisionsListsNewestFirst mirrors
// TestGetFPPEndpointsConfigRevisionsListsNewestFirst.
func TestGetRenderSettingsConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, body := range []string{
		`{"idleOutput":"black","restartPolicy":{"initialDelaySeconds":1,"maxDelaySeconds":30,"maxConsecutiveFastFailures":5}}`,
		`{"idleOutput":"hold","restartPolicy":{"initialDelaySeconds":2,"maxDelaySeconds":40,"maxConsecutiveFastFailures":4}}`,
		`{"idleOutput":"diagnostic","restartPolicy":{"initialDelaySeconds":3,"maxDelaySeconds":50,"maxConsecutiveFastFailures":3}}`,
	} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", body, headers)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, respBody)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/render.settings/revisions", headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	revs, _ := m["revisions"].([]any)
	if len(revs) != 3 {
		t.Fatalf("revisions count = %d, want 3", len(revs))
	}
	first, _ := revs[0].(map[string]any)
	if first["revision"] != float64(3) {
		t.Errorf("revisions[0].revision = %v, want 3 (newest first)", first["revision"])
	}
	if first["active"] != true {
		t.Errorf("revisions[0].active = %v, want true", first["active"])
	}
}

func TestPutRenderSettingsConfigCSRF(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	cookie := loginAndGetCookie(t, api.Handler, "admin-1", testPassword)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody, nil)
	req.Header.Set("Cookie", sessionCookieName+"="+cookie)
	// No Sec-Fetch-Site header: a cookie-authenticated write without it is
	// refused (ADR-024 decision 6), mirroring TestPutFPPEndpointsConfigCSRF.
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (csrf-rejected); body: %s", resp.StatusCode, body)
	}
}
