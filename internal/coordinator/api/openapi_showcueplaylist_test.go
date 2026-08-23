package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track H seam H1's own conformance coverage, following
// openapi_showobjects_test.go's exact pattern one seam over: every schema
// this seam added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

// TestOpenAPIShowCuePlaylistDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPIShowCuePlaylistDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigShowCueRenderOutput", "ConfigShowCueAudioOutput", "ConfigShowCueLTCOutput",
		"ConfigShowCueAnnouncementOutput", "ConfigShowCueOutputs", "ConfigShowCue", "ShowCueConfigResponse",
		"ConfigShowPlaylistFPPBinding", "ConfigShowPlaylistShowmeshAudio", "ConfigShowPlaylistEntryFPP",
		"ConfigShowPlaylistEntry", "ConfigShowPlaylist", "ShowPlaylistConfigResponse",
	} {
		compileSchema(t, c, name)
	}
}

const validCueBody = `{
	"show": "halloween-2026",
	"name": "Thriller",
	"outputs": {
		"render": {"sequence": "thriller"},
		"audio": {"asset": "thriller-audience", "startOffsetMillis": 0}
	}
}`

// TestOpenAPIShowCuePlaylistResponsesMatchRealResponses proves every route
// TRACK-H-H1-SPEC.md section 6 requires against a real coordinator wiring.
func TestOpenAPIShowCuePlaylistResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}

	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	// --- kind "show.cue" ---
	putCueReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", validCueBody, auth)
	putCueResp, putCueBody := doRawRequest(t, api.Handler, putCueReq)
	if putCueResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue: status = %d, want 200; body: %s", putCueResp.StatusCode, putCueBody)
	}
	assertMatchesSchema(t, c, "ShowCueConfigResponse", putCueBody)

	_, getCueBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue/thriller", auth)
	assertMatchesSchema(t, c, "ShowCueConfigResponse", getCueBody)

	_, listCueBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listCueBody)

	_, listCueFilteredBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue?show=halloween-2026", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listCueFilteredBody)

	cueNodeFilterResp, cueNodeFilterBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue?node=render-01", auth)
	if cueNodeFilterResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show.cue?node=: status = %d, want 400; body: %s", cueNodeFilterResp.StatusCode, cueNodeFilterBody)
	}
	assertMatchesSchema(t, c, "Problem", cueNodeFilterBody)

	_, revCueBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue/thriller/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revCueBody)

	// A Cue with every output populated, to prove ltc/announcement's own
	// schemas too.
	fullCueBody := `{
		"show": "halloween-2026",
		"name": "Full Cue",
		"outputs": {
			"render": {"sequence": "thriller"},
			"audio": {"asset": "thriller-audience", "startOffsetMillis": 0},
			"ltc": {"startOffsetMillis": 0},
			"announcement": {"policy": "duck", "duckGainDb": -18, "fadeMillis": 300}
		}
	}`
	putFullCueReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/full", fullCueBody, auth)
	putFullCueResp, putFullCueRespBody := doRawRequest(t, api.Handler, putFullCueReq)
	if putFullCueResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue (full): status = %d, want 200; body: %s", putFullCueResp.StatusCode, putFullCueRespBody)
	}
	assertMatchesSchema(t, c, "ShowCueConfigResponse", putFullCueRespBody)

	// --- kind "show.playlist" ---
	playlistBody := `{
		"show": "halloween-2026",
		"name": "Main show",
		"runner": "fpp",
		"mismatchPolicy": "hold",
		"fpp": {
			"instanceUuid": "11111111-1111-1111-1111-111111111111",
			"playlistName": "Halloween Main",
			"playlistHash": "` + playlistHash64 + `"
		},
		"entries": [
			{"id": "e1", "cue": "thriller", "fpp": {"section": "mainPlaylist", "position": 0}}
		]
	}`
	putPlaylistReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", playlistBody, auth)
	putPlaylistResp, putPlaylistBody := doRawRequest(t, api.Handler, putPlaylistReq)
	if putPlaylistResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist: status = %d, want 200; body: %s", putPlaylistResp.StatusCode, putPlaylistBody)
	}
	assertMatchesSchema(t, c, "ShowPlaylistConfigResponse", putPlaylistBody)

	_, getPlaylistBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist/main", auth)
	assertMatchesSchema(t, c, "ShowPlaylistConfigResponse", getPlaylistBody)

	_, listPlaylistBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist", auth)
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listPlaylistBody)

	playlistNodeFilterResp, playlistNodeFilterBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist?node=render-01", auth)
	if playlistNodeFilterResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET show.playlist?node=: status = %d, want 400; body: %s", playlistNodeFilterResp.StatusCode, playlistNodeFilterBody)
	}
	assertMatchesSchema(t, c, "Problem", playlistNodeFilterBody)

	_, revPlaylistBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist/main/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revPlaylistBody)

	// A showmesh-audio runner Playlist, to prove that branch's schemas too.
	audioPlaylistBody := `{
		"show": "halloween-2026",
		"name": "Audio only",
		"runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "all"},
		"entries": [{"id": "e1", "cue": "thriller"}]
	}`
	putAudioReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/audio-only", audioPlaylistBody, auth)
	putAudioResp, putAudioBody := doRawRequest(t, api.Handler, putAudioReq)
	if putAudioResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist (showmesh-audio): status = %d, want 200; body: %s", putAudioResp.StatusCode, putAudioBody)
	}
	assertMatchesSchema(t, c, "ShowPlaylistConfigResponse", putAudioBody)

	// A validation-error sample, to prove the Problem shape one more time
	// on this seam's own refusal path.
	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/bad", `{"show":"halloween-2026","name":"x","outputs":{}}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT invalid show.cue: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}

// TestOpenAPIShowCuePlaylistPutRequestBodiesReferenceDocumentedSchemas
// resolves the document pointer assertMatchesSchema never reads:
// paths./config/show.cue/{id}.put.requestBody.content.application/json.schema.$ref
// and the equivalent for show.playlist.
func TestOpenAPIShowCuePlaylistPutRequestBodiesReferenceDocumentedSchemas(t *testing.T) {
	if got := requestBodySchemaRef(t, "put", "/config/show.cue/{id}"); got != "ConfigShowCue" {
		t.Errorf("PUT /config/show.cue/{id} requestBody schema = %q, want ConfigShowCue", got)
	}
	if got := requestBodySchemaRef(t, "put", "/config/show.playlist/{id}"); got != "ConfigShowPlaylist" {
		t.Errorf("PUT /config/show.playlist/{id} requestBody schema = %q, want ConfigShowPlaylist", got)
	}
}
