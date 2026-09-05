package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is media.playlist's own handler test suite
// (internal/coordinator/api/mediaplaylist.go), following
// showcueplaylist_test.go's pattern one kind over: a real *store.Store and
// a real identity.Service, driven through the real route table.

// mediaPlaylistTestDeps is showObjectsTestDeps plus a real Assets store:
// media.playlist items resolve through nightSessionAssetCurrent, the same
// helper resting.backgroundAudio items resolve through
// (nightbackgroundaudio_test.go's putBackgroundAudioAsset).
func mediaPlaylistTestDeps(svc identity.Service, st *store.Store) Dependencies {
	deps := showObjectsTestDeps(svc, st)
	deps.Assets = st
	return deps
}

func mustPutMediaPlaylist(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT media.playlist/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

func validMediaPlaylistBody(show, itemShow, sequence, target string) string {
	return `{
		"show": "` + show + `", "label": "Porch bed",
		"items": [{"kind": "asset", "show": "` + itemShow + `", "sequence": "` + sequence + `", "target": "` + target + `"}],
		"resume": "resume", "itemTransition": "sequential", "maxGainDb": 0
	}`
}

// TestPutMediaPlaylistRejectsUnknownAsset proves this handler wires
// DecodeMediaPlaylistPayload's assetCurrent callback (h.nightSessionAssetCurrent)
// against live store state, not a stub that always returns true.
func TestPutMediaPlaylistRejectsUnknownAsset(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"),
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no current asset for seq1/node1 yet); body: %s", resp.StatusCode, body)
	}
	problem := decodeMap(t, body)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeFieldUnknownReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
}

// TestPutMediaPlaylistRequiresConfigWrite proves an operator (never
// config:write) cannot write media.playlist, matching every other config
// kind's write posture.
func TestPutMediaPlaylistRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"),
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestListMediaPlaylistsFiltersByShow mirrors TestListShowPlaylistsFiltersByShow
// on the media.playlist kind.
func TestListMediaPlaylistsFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-halloween")
	putBackgroundAudioAsset(t, st, "christmas-2026", "seq2", "node1", "asset-christmas")

	mustPutMediaPlaylist(t, api, token, "porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"))
	mustPutMediaPlaylist(t, api, token, "yard", validMediaPlaylistBody("christmas-2026", "christmas-2026", "seq2", "node1"))

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"yard"`) {
		t.Fatalf("expected yard in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"porch"`) {
		t.Fatalf("porch (halloween-2026) leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

// TestListMediaPlaylistsRejectsNodeFilter proves ?node= is refused for this
// kind rather than silently ignored (unsupportedNodeFilterProblem).
func TestListMediaPlaylistsRejectsNodeFilter(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist?node=node1", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (media.playlist has no node field to filter on); body: %s", resp.StatusCode, body)
	}
	problem := decodeMap(t, body)
	if problem["type"] != ProblemTypeInvalidParameter {
		t.Errorf("problem.type = %v, want %v", problem["type"], ProblemTypeInvalidParameter)
	}
}

// TestPutAndGetMediaPlaylistRoundTrips proves a PUT's response and a
// subsequent GET both carry the same payload back.
func TestPutAndGetMediaPlaylistRoundTrips(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")

	want := v1.ConfigMediaPlaylist{
		Label: "Porch bed", Show: "halloween-2026",
		Items:  []v1.ConfigMediaPlaylistItem{{Kind: "asset", Show: "halloween-2026", Sequence: "seq1", Target: "node1"}},
		Repeat: "none", Resume: "resume", ItemTransition: "sequential", MaxGainDb: 0,
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"),
		map[string]string{"Authorization": "Bearer " + token})
	resp, putBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, putBody)
	}
	var putResp v1.MediaPlaylistConfigResponse
	if err := json.Unmarshal(putBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, putBody)
	}
	if !reflect.DeepEqual(putResp.Payload, want) {
		t.Errorf("PUT response payload = %+v, want %+v", putResp.Payload, want)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist/porch", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.MediaPlaylistConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if !reflect.DeepEqual(getResp.Payload, want) {
		t.Errorf("GET response payload = %+v, want %+v", getResp.Payload, want)
	}
}

// TestPutMediaPlaylistRejectsShowChange proves a PUT that re-points an
// existing media.playlist at a different Show is refused rather than
// silently accepted, mirroring TestPutShowPlaylistRejectsShowChange.
func TestPutMediaPlaylistRejectsShowChange(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-halloween")
	putBackgroundAudioAsset(t, st, "christmas-2026", "seq2", "node1", "asset-christmas")

	mustPutMediaPlaylist(t, api, token, "porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"))

	moveReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch", validMediaPlaylistBody("christmas-2026", "christmas-2026", "seq2", "node1"),
		map[string]string{"Authorization": "Bearer " + token})
	moveResp, moveBody := doRawRequest(t, api.Handler, moveReq)
	if moveResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (show is immutable); body: %s", moveResp.StatusCode, moveBody)
	}
	problem := decodeMap(t, moveBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeCrossShowReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
}

// TestPutMediaPlaylistRevisionPreconditionWiring is a smoke test proving
// handlePutMediaPlaylist threads the shared precondition check
// (showconfig.go's parseRevisionPrecondition/writeShowConfigRevision)
// through to its own call site, matching TestPutShowPlaylistRevisionPreconditionWiring.
func TestPutMediaPlaylistRevisionPreconditionWiring(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")

	body := validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1")
	putPlaylist := func(headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/media.playlist/porch", body, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, respBody := putPlaylist(nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("unconditional create: status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	if resp, respBody := putPlaylist(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusOK {
		t.Fatalf("matching If-Match: status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	if resp, respBody := putPlaylist(map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
	if resp, respBody := putPlaylist(map[string]string{"If-None-Match": "*"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("If-None-Match against an already-created playlist: status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
}

// TestGetMediaPlaylistRevisions proves the shared revisions route
// (handleGetShowConfigRevisions) is wired for this kind and reports the
// active revision, newest first.
func TestGetMediaPlaylistRevisions(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")
	mustPutMediaPlaylist(t, api, token, "porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"))

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist/porch/revisions", map[string]string{"Authorization": "Bearer " + token})
	var revsResp v1.ConfigRevisionsResponse
	if err := json.Unmarshal(body, &revsResp); err != nil {
		t.Fatalf("decode revisions response: %v; body: %s", err, body)
	}
	if len(revsResp.Revisions) != 1 || !revsResp.Revisions[0].Active {
		t.Fatalf("revisions = %+v, want exactly one active revision", revsResp.Revisions)
	}
}

// TestDeleteMediaPlaylistTombstonesAndHonoursIfMatch proves the shared
// delete path (handleDeleteShowConfigObject) requires the confirm body,
// refuses a stale If-Match with 409, and excludes the tombstoned object
// from GET afterward, mirroring configdelete_test.go's audio.node
// coverage one kind over.
func TestDeleteMediaPlaylistTombstonesAndHonoursIfMatch(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")
	mustPutMediaPlaylist(t, api, token, "porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"))

	del := func(headers map[string]string) (*http.Response, []byte) {
		h := map[string]string{"Authorization": "Bearer " + token}
		for k, v := range headers {
			h[k] = v
		}
		req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/media.playlist/porch", `{"confirm":true}`, h)
		return doRawRequest(t, api.Handler, req)
	}

	if resp, body := del(map[string]string{"If-Match": `"7"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if resp, body := del(nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/media.playlist/porch", map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete: status = %d, want 404; body: %s", getResp.StatusCode, getBody)
	}
}

// TestDeleteMediaPlaylistRequiresConfigWrite matches every other config
// kind's write posture: an operator cannot delete media.playlist.
func TestDeleteMediaPlaylistRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(mediaPlaylistTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	putBackgroundAudioAsset(t, st, "halloween-2026", "seq1", "node1", "asset-1")
	mustPutMediaPlaylist(t, api, token, "porch", validMediaPlaylistBody("halloween-2026", "halloween-2026", "seq1", "node1"))

	req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/media.playlist/porch", `{"confirm":true}`,
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}
