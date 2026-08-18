package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track B seam B2c's own OpenAPI conformance suite, mirroring
// openapi_resolumerecovery_test.go's pattern of driving a REAL [API] and
// validating its actual response body against api/openapi.yaml's own schema
// for that endpoint — never a hand-built JSON fixture.

// TestOpenAPIRenderSettingsDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPIRenderSettingsDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigRenderRestartPolicy", "ConfigRenderSettingsPayload", "RenderSettingsConfigResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIRenderSettingsConfigResponsesMatchRealResponses covers PUT
// /config/render.settings' success body, GET /config/render.settings' body
// (both the unconfigured-default case and the configured case), and GET
// /config/render.settings/revisions' body.
func TestOpenAPIRenderSettingsConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	// Unconfigured: revision 0, source "default".
	_, unconfiguredBody := doRequest(t, api.Handler, "GET", "/api/v1/config/render.settings", authHeader)
	assertMatchesSchema(t, c, "RenderSettingsConfigResponse", unconfiguredBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/render.settings", validRenderSettingsBody, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "RenderSettingsConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/render.settings", authHeader)
	assertMatchesSchema(t, c, "RenderSettingsConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/render.settings/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}
