package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
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

func TestFPPSequencesListsTheUnionAndNamesAnUnreachableFPP(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	fpp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/files/sequences" || r.URL.Query().Get("nameOnly") != "1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`["NewSong.fseq","sub/Thriller.fseq"]`))
	}))
	t.Cleanup(fpp.Close)
	deps := assetManifestTestDeps(t, svc, st)
	deps.FPP = &fakeFPPLister{views: []FPPInstanceView{
		{InstanceID: "player-01", Endpoint: fpp.URL},
		{InstanceID: "player-02", Endpoint: "http://127.0.0.1:1"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/fpp/sequences", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "FPPSequenceListResponse", body)
	var got v1.FPPSequenceListResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sequences) != 2 || got.Sequences[0].Filename != "NewSong.fseq" || got.Sequences[1].Filename != "sub/Thriller.fseq" || got.Sequences[0].Instances[0] != "player-01" {
		t.Fatalf("sequences = %+v", got.Sequences)
	}
	if len(got.Instances) != 2 || got.Instances[0].Error != "" || got.Instances[1].Error == "" {
		t.Fatalf("instances = %+v, want player-02 to carry an error", got.Instances)
	}
}
