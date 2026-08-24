package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is ADR-033's own conformance coverage, following
// openapi_showcueplaylist_test.go's exact pattern: every schema this seam
// added is validated against a REAL response from a real coordinator
// wiring, never hand-built JSON. openapi_test.go injects
// additionalProperties:false into every schema with properties, so an
// undeclared field on the wire fails here.

func TestOpenAPIShowModeDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"ConfigShowModePayload", "ShowModeConfigResponse"} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIShowModeResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	// The unconfigured default response is a distinct code path from the
	// stored-revision one, so it gets its own schema check.
	_, defaultBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", auth)
	assertMatchesSchema(t, c, "ShowModeConfigResponse", defaultBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{"mode":"show"}`, auth)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.mode: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "ShowModeConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode", auth)
	assertMatchesSchema(t, c, "ShowModeConfigResponse", getBody)

	// The read that ADR-033 decision 3 exists for: a principal with no
	// config:write reads the same schema.
	viewerResp, viewerBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode",
		map[string]string{"Authorization": "Bearer " + viewerToken})
	if viewerResp.StatusCode != http.StatusOK {
		t.Fatalf("viewer GET show.mode: status = %d, want 200; body: %s", viewerResp.StatusCode, viewerBody)
	}
	assertMatchesSchema(t, c, "ShowModeConfigResponse", viewerBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.mode/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)

	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.mode", `{"mode":"unknown"}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT show.mode with a non-member: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}
