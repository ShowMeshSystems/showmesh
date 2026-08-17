package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track E seam E1/E2's own conformance coverage, following
// openapi_showmacros_test.go's exact pattern one file over: every schema
// this seam added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

// TestOpenAPIShowObjectsDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPIShowObjectsDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigShow", "ConfigShowWrite", "ShowConfigResponse",
		"ConfigShowSurfaceChannelRange", "ConfigShowSurfaceGeometry",
		"ConfigShowSurfaceNDIOutput", "ConfigShowSurfaceHDMI", "ConfigShowSurfaceOutput",
		"ConfigShowSurface", "ShowSurfaceConfigResponse",
		"ConfigShowActive", "ShowActiveConfigResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIShowObjectsResponsesMatchRealResponses proves every route in
// TRACK-E-SESSION-SPEC.md section 2.4 against a real coordinator wiring.
func TestOpenAPIShowObjectsResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	// --- kind "show" ---
	putShowReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show/halloween-2026", `{"name":"Halloween 2026","notes":"draft"}`, auth)
	putShowResp, putShowBody := doRawRequest(t, api.Handler, putShowReq)
	if putShowResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show: status = %d, want 200; body: %s", putShowResp.StatusCode, putShowBody)
	}
	assertMatchesSchema(t, c, "ShowConfigResponse", putShowBody)

	_, getShowBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show/halloween-2026", auth)
	assertMatchesSchema(t, c, "ShowConfigResponse", getShowBody)

	_, listShowBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listShowBody)

	_, revShowBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show/halloween-2026/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revShowBody)

	// --- kind "show.surface" ---
	mustDeclareNode(t, st, "render-01")
	putSurfaceReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", validSurfaceBodyNDI, auth)
	putSurfaceResp, putSurfaceBody := doRawRequest(t, api.Handler, putSurfaceReq)
	if putSurfaceResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface: status = %d, want 200; body: %s", putSurfaceResp.StatusCode, putSurfaceBody)
	}
	assertMatchesSchema(t, c, "ShowSurfaceConfigResponse", putSurfaceBody)

	_, getSurfaceBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface/garage", auth)
	assertMatchesSchema(t, c, "ShowSurfaceConfigResponse", getSurfaceBody)

	_, listSurfaceBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listSurfaceBody)

	// PR #14 review finding: GET /config/show.surface?node= against a real
	// response, and the sibling kinds' 400 Problem for the same parameter.
	_, listSurfaceByNodeBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface?node=render-01", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listSurfaceByNodeBody)

	nodeFilterRejectedResp, nodeFilterRejectedBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action?node=render-01", auth)
	if nodeFilterRejectedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show.action?node=: status = %d, want 400; body: %s", nodeFilterRejectedResp.StatusCode, nodeFilterRejectedBody)
	}
	assertMatchesSchema(t, c, "Problem", nodeFilterRejectedBody)

	_, revSurfaceBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.surface/garage/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revSurfaceBody)

	// An HDMI-transport surface, to prove that branch of ConfigShowSurfaceOutput too.
	hdmiSurfaceBody := `{
		"show": "halloween-2026",
		"name": "Porch",
		"node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 8},
		"geometry": {"width": 2, "height": 1, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	putHDMIReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/porch", hdmiSurfaceBody, auth)
	putHDMIResp, putHDMIRespBody := doRawRequest(t, api.Handler, putHDMIReq)
	if putHDMIResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface (hdmi): status = %d, want 200; body: %s", putHDMIResp.StatusCode, putHDMIRespBody)
	}
	assertMatchesSchema(t, c, "ShowSurfaceConfigResponse", putHDMIRespBody)

	// --- kind "show.active" ---
	putActiveReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`, auth)
	putActiveResp, putActiveBody := doRawRequest(t, api.Handler, putActiveReq)
	if putActiveResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.active: status = %d, want 200; body: %s", putActiveResp.StatusCode, putActiveBody)
	}
	assertMatchesSchema(t, c, "ShowActiveConfigResponse", putActiveBody)

	_, getActiveBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active", auth)
	assertMatchesSchema(t, c, "ShowActiveConfigResponse", getActiveBody)

	_, revActiveBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.active/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revActiveBody)

	// A validation-error sample, to prove the Problem shape one more time
	// on this seam's own refusal path.
	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/bad", `{"show":"halloween-2026","name":"x","node":"render-01"}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT invalid show.surface: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}

// TestOpenAPIShowObjectsPutRequestBodiesReferenceDocumentedSchemas resolves
// the document pointer assertMatchesSchema never reads: paths./config/show/
// {id}.put.requestBody.content.application/json.schema.$ref and the
// equivalent for show.surface and show.active — mirroring
// TestOpenAPIShowConfigPutRequestBodiesReferenceWriteSchemas
// (openapi_showmacros_test.go) one seam over.
func TestOpenAPIShowObjectsPutRequestBodiesReferenceDocumentedSchemas(t *testing.T) {
	if got := requestBodySchemaRef(t, "put", "/config/show/{id}"); got != "ConfigShowWrite" {
		t.Errorf("PUT /config/show/{id} requestBody schema = %q, want ConfigShowWrite", got)
	}
	if got := requestBodySchemaRef(t, "put", "/config/show.surface/{id}"); got != "ConfigShowSurface" {
		t.Errorf("PUT /config/show.surface/{id} requestBody schema = %q, want ConfigShowSurface", got)
	}
	if got := requestBodySchemaRef(t, "put", "/config/show.active"); got != "ConfigShowActive" {
		t.Errorf("PUT /config/show.active requestBody schema = %q, want ConfigShowActive", got)
	}
}
