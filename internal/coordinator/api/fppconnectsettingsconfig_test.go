package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track E phase 2 seam FC1a's own test suite for the
// fppconnect.settings singleton, mirroring audiosettings_test.go's own
// shape.

// TestGetFPPConnectSettingsDefaultsBeforeAnyWrite proves GET never 404s:
// the unconfigured state reports the built-in default with revision 0 and
// source "default", matching audio.settings' own posture.
func TestGetFPPConnectSettingsDefaultsBeforeAnyWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"revision":0`) || !containsAll(string(body), `"source":"default"`) {
		t.Fatalf("body missing revision:0/source:default; body: %s", body)
	}
	if !containsAll(string(body), `"enabled":true`) || !containsAll(string(body), `"maxFileBytes":2147483648`) || !containsAll(string(body), `"maxAssetDirBytes":21474836480`) {
		t.Fatalf("body missing the built-in default payload; body: %s", body)
	}
}

const validFPPConnectSettingsBody = `{"enabled":false,"maxFileBytes":1073741824,"maxAssetDirBytes":10737418240}`

// TestPutFPPConnectSettingsThenGetReflectsWrittenValue proves the
// zero-to-one transition: an unconfigured kind, one write, and a
// subsequent GET that reflects it rather than the default.
func TestPutFPPConnectSettingsThenGetReflectsWrittenValue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", validFPPConnectSettingsBody, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"enabled":false`) || !containsAll(string(getBody), `"maxFileBytes":1073741824`) || !containsAll(string(getBody), `"source":"api"`) {
		t.Fatalf("GET does not reflect written value; body: %s", getBody)
	}
}

// TestPutFPPConnectSettingsRejectsInvalidBody proves an invalid payload is
// refused before activation (ADR-009): no revision is created, so a
// following GET still reports the default.
func TestPutFPPConnectSettingsRejectsInvalidBody(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", `{"enabled":true}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"revision":0`) {
		t.Fatalf("a rejected write must leave no revision behind; body: %s", getBody)
	}
}

// TestPutFPPConnectSettingsRejectsAssetDirBelowFileCap proves the
// cross-field rule (maxAssetDirBytes must be at least maxFileBytes) is
// enforced through the HTTP surface, not merely in the config package's
// own unit tests.
func TestPutFPPConnectSettingsRejectsAssetDirBelowFileCap(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", `{"enabled":true,"maxFileBytes":1000,"maxAssetDirBytes":999}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestGetFPPConnectSettingsRevisionsListsWrittenRevisions proves the
// revisions route is reachable and lists what was written, newest first.
func TestGetFPPConnectSettingsRevisionsListsWrittenRevisions(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", validFPPConnectSettingsBody, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings/revisions", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"revision":1`) {
		t.Fatalf("revisions list missing revision 1; body: %s", body)
	}
}
