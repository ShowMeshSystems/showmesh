package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

func TestSequenceTestSaveRefusesAFilenameAPlaylistPlaysForAnotherCue(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	auth := map[string]string{"Authorization": "Bearer " + token}
	api := New(assetManifestTestDeps(t, svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)
	mustPutCue(t, api, token, "other", `{"show":"halloween-2026","name":"Other","outputs":{"render":{"sequence":"other"}}}`)
	playlist := `{
		"show": "halloween-2026", "name": "Main show", "runner": "fpp",
		"fpp": {"instanceUuid": "u1", "playlistName": "p1", "playlistHash": "` + playlistHash64 + `"},
		"entries": [{"id": "e1", "cue": "thriller", "fpp": {"section": "main", "position": 0, "expectedSequenceFilename": "Thriller.fseq"}}]
	}`
	if resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", playlist, auth)); resp.StatusCode != http.StatusOK {
		t.Fatalf("seed playlist: status = %d; body: %s", resp.StatusCode, body)
	}

	_, defaultBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.sequencetest/halloween-2026", auth)
	assertMatchesSchema(t, c, "SequenceTestConfigResponse", defaultBody)

	put := func(body string) (int, string) {
		resp, b := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.sequencetest/halloween-2026", body, auth))
		return resp.StatusCode, string(b)
	}
	if code, body := put(`{"entries":[{"sequenceFilename":"Thriller.fseq","cue":"other"}]}`); code != http.StatusBadRequest || !strings.Contains(body, "already plays cue thriller") {
		t.Fatalf("claimed filename: status = %d, want 400; body: %s", code, body)
	}
	code, body := put(`{"entries":[{"sequenceFilename":"Thriller.fseq","cue":"thriller"},{"sequenceFilename":"Other.fseq","cue":"other"}]}`)
	if code != http.StatusOK {
		t.Fatalf("valid list: status = %d; body: %s", code, body)
	}
	assertMatchesSchema(t, c, "SequenceTestConfigResponse", []byte(body))
	if !strings.Contains(body, `"revision":1`) || !strings.Contains(body, `"Other.fseq"`) || !strings.Contains(body, `"running":false`) {
		t.Fatalf("PUT body = %s", body)
	}
}
