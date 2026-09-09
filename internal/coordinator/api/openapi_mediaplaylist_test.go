package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is media.playlist's own conformance coverage, following
// openapi_showcueplaylist_test.go's exact pattern one kind over: every
// schema this kind added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

// TestOpenAPIMediaPlaylistDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this kind added.
func TestOpenAPIMediaPlaylistDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"ConfigMediaPlaylistItem", "ConfigMediaPlaylist", "MediaPlaylistConfigResponse"} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIMediaPlaylistResponsesMatchRealResponses proves every route
// this kind added against a real coordinator wiring.
func TestOpenAPIMediaPlaylistResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch",
		validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"), auth)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT media.playlist: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "MediaPlaylistConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist/porch", auth)
	assertMatchesSchema(t, c, "MediaPlaylistConfigResponse", getBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listBody)

	_, listFilteredBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist?show=halloween-2026", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listFilteredBody)

	nodeFilterResp, nodeFilterBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist?node=node1", auth)
	if nodeFilterResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET media.playlist?node=: status = %d, want 400; body: %s", nodeFilterResp.StatusCode, nodeFilterBody)
	}
	assertMatchesSchema(t, c, "Problem", nodeFilterBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist/porch/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)

	delReq := newJSONRequest(t, http.MethodDelete, "/api/v1/config/media.playlist/porch", `{"confirm":true}`, auth)
	delResp, delBody := doRawRequest(t, api.Handler, delReq)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE media.playlist: status = %d, want 204; body: %s", delResp.StatusCode, delBody)
	}

	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/bad", `{"show":"halloween-2026","label":"x","items":[]}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT invalid media.playlist: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}

// TestOpenAPIMediaPlaylistPutRequestBodyReferencesDocumentedSchema resolves
// the document pointer assertMatchesSchema never reads:
// paths./config/media.playlist/{id}.put.requestBody.content.application/json.schema.$ref
func TestOpenAPIMediaPlaylistPutRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "put", "/config/media.playlist/{id}"); got != "ConfigMediaPlaylist" {
		t.Errorf("PUT /config/media.playlist/{id} requestBody schema = %q, want ConfigMediaPlaylist", got)
	}
}
