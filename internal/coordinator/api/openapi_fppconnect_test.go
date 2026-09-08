package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is the fppconnect.settings configuration kind's own OpenAPI
// conformance suite (Track E phase 2 seam FC1a, ADR-044 decision 5),
// mirroring openapi_audio_test.go's identical pattern one kind over: a
// REAL [API], its actual response body validated against
// api/openapi.yaml's own schema for that endpoint, never a hand-built JSON
// fixture.

func TestOpenAPIFPPConnectDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"ConfigFPPConnectSettingsPayload", "FPPConnectSettingsConfigResponse"} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIFPPConnectSettingsConfigResponsesMatchRealResponses covers GET
// (unconfigured and configured), PUT, and revisions for fppconnect.settings.
func TestOpenAPIFPPConnectSettingsConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	_, unconfiguredBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings", authHeader)
	assertMatchesSchema(t, c, "FPPConnectSettingsConfigResponse", unconfiguredBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", validFPPConnectSettingsBody, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "FPPConnectSettingsConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings", authHeader)
	assertMatchesSchema(t, c, "FPPConnectSettingsConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fppconnect.settings/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIFPPConnectSettingsRefusalMatchesProblemSchema covers the
// 400 body against the shared Problem schema.
func TestOpenAPIFPPConnectSettingsRefusalMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fppconnect.settings", `{"enabled":true}`, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT: status = %d, want 400; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "Problem", putBody)
}
