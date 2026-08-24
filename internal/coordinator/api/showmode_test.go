package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

const validShowModeBody = `{"mode":"show"}`

// TestGetShowModeConfigUnconfiguredReportsProgram pins the fresh-install
// default (owner ruling): nothing has ever been written, so this answers
// 200 with revision 0, source "default", and mode "program". A fresh
// install is by definition being set up.
func TestGetShowModeConfigUnconfiguredReportsProgram(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
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
	if payload["mode"] != "program" {
		t.Errorf("payload.mode = %v, want program", payload["mode"])
	}
	// ADR-033 decision 3: the mode's effect is stated where the operator
	// can see it, naming the mode as the reason.
	effect, _ := m["resolumeWebSocketEffect"].(string)
	if !strings.Contains(effect, "program mode") {
		t.Errorf("resolumeWebSocketEffect = %q, want it to name the mode", effect)
	}
}

// TestGetShowModeConfigIsReadableWithoutConfigWrite is the deliberate
// departure from every other configuration singleton, and the reason this
// test exists rather than a copy of
// TestGetRenderSettingsConfigRequiresConfigWriteScope: ADR-033 decision 3
// requires the mode to be persistently visible, and the operator at the
// console does not hold config:write. A viewer, holding only the four read
// scopes, must be able to read the CURRENT value even on a coordinator
// with reads closed.
func TestGetShowModeConfigIsReadableWithoutConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)

	t.Run("open reads", func(t *testing.T) {
		api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("closed reads", func(t *testing.T) {
		api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: true})
		for name, token := range map[string]string{"viewer": viewerToken, "operator": operatorToken} {
			resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
				map[string]string{"Authorization": "Bearer " + token})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s: status = %d, want 200; body: %s", name, resp.StatusCode, body)
			}
		}
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated with reads closed: status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})
}

// The HISTORY keeps the ordinary config:write gate. The open read is the
// current value, which is what decision 3 asks to be visible; revision
// metadata carries principal names and is audit-adjacent.
func TestGetShowModeConfigRevisionsRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	resp, body = doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions",
		map[string]string{"Authorization": "Bearer " + viewerToken})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	resp, body = doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestPutShowModeConfigAuthAndScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)

	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody, nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	// The read being open must not have opened the write. A viewer and an
	// operator can SEE the mode and cannot set it.
	for name, token := range map[string]string{"viewer": viewerToken, "operator": operatorToken} {
		t.Run(name+" forbidden naming config:write", func(t *testing.T) {
			req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
				map[string]string{"Authorization": "Bearer " + token})
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "config:write") {
				t.Errorf("body = %s, want it to name the missing scope config:write", body)
			}
		})
	}

	t.Run("admin accepted", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
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
		payload, _ := m["payload"].(map[string]any)
		if payload["mode"] != "show" {
			t.Errorf("payload.mode = %v, want show", payload["mode"])
		}
		effect, _ := m["resolumeWebSocketEffect"].(string)
		if !strings.Contains(effect, "show mode") {
			t.Errorf("resolumeWebSocketEffect = %q, want it to name show mode", effect)
		}
	})
}

// The write is audited with the principal that made it (ADR-024), and the
// audit entry names the mode that was set.
func TestPutShowModeConfigWritesAnAuditEntryNamingThePrincipal(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == "config.write" && e.Target == "show.mode" {
			if e.PrincipalID != admin.ID {
				t.Fatalf("audit entry principal = %q, want %q", e.PrincipalID, admin.ID)
			}
			return
		}
	}
	t.Fatalf("no config.write audit entry targeting show.mode in %v", entries)
}

// ADR-009: a rejected write leaves no revision behind, and the closed enum
// is enforced on the real HTTP path rather than only in the decoder's own
// unit tests. "unknown" is refused specifically: it is a node-side state,
// never a value an operator can write.
func TestPutShowModeConfigRejectsNonMembersBeforeActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, body := range []string{`{"mode":"unknown"}`, `{"mode":"setup"}`, `{"mode":""}`} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", body, headers)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PUT %s: status = %d, want 400; body: %s", body, resp.StatusCode, respBody)
		}
	}

	revs, err := st.ListConfigRevisions(context.Background(), "show.mode", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after rejected writes = %v, want none", revs)
	}
}

// An absent key is refused by name, never treated as "leave it as it was".
func TestPutShowModeConfigRejectsAbsentModeKey(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{}`,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "mode") {
		t.Errorf("body = %s, want it to name mode", respBody)
	}
}

// ADR-024 decision 11's same-transaction rule, against a REAL SQLite
// trigger.
func TestPutShowModeConfigFailsClosedOnAuditFailure(t *testing.T) {
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", validShowModeBody,
		map[string]string{"Authorization": "Bearer " + adminToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}

	revs, err := st.ListConfigRevisions(context.Background(), "show.mode", "default")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Fatalf("revisions after a failed-audit write = %v, want none", revs)
	}
}

func TestGetShowModeConfigRevisionsListsNewestFirst(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + adminToken}

	for _, body := range []string{`{"mode":"show"}`, `{"mode":"program"}`, `{"mode":"show"}`} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", body, headers)
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, respBody)
		}
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions", headers)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["kind"] != "show.mode" {
		t.Errorf("kind = %v, want show.mode", m["kind"])
	}
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

// net/http.ServeMux matches by segment, so "show.mode" is a distinct
// literal that can never be swallowed by "GET /api/v1/config/show/{id}",
// the same guard show.active already carries.
func TestShowModeRouteIsNotSwallowedByShowIDRoute(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
		map[string]string{"Authorization": "Bearer " + adminToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["kind"] != "show.mode" {
		t.Fatalf("kind = %v, want show.mode (the show/{id} route answered instead)", m["kind"])
	}
}
