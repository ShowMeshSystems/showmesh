package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track H seam H1's own handler test suite for show.cue and
// show.playlist, following showobjects_test.go's pattern one seam over: a
// real *store.Store and a real identity.Service, driven through the real
// route table.

func mustPutCue(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

// TestPutShowCueRejectsUnknownShow proves this handler actually wires
// DecodeShowCuePayload's showExists callback against live store state.
func TestPutShowCueRejectsUnknownShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", validCueBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (show halloween-2026 does not exist yet); body: %s", resp.StatusCode, body)
	}
	problem := decodeMap(t, body)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeFieldUnknownReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v (a nonexistent show must not fall through to some other refusal)", problem["type"], wantType)
	}
}

// TestListShowCuesFiltersByShow proves the ?show= query filter narrows
// the list rather than being ignored.
func TestListShowCuesFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)

	mustPutCue(t, api, token, "thriller", validCueBody)
	christmasCue := `{"show":"christmas-2026","name":"Sleigh Ride","outputs":{"render":{"sequence":"sleigh"}}}`
	mustPutCue(t, api, token, "sleigh", christmasCue)

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"sleigh"`) {
		t.Fatalf("expected sleigh in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"thriller"`) {
		t.Fatalf("thriller (halloween-2026) leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

// TestPutShowCueRequiresConfigWrite proves an operator (show:macro:run,
// never config:write) cannot write show.cue, matching every other config
// kind's write posture.
func TestPutShowCueRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", validCueBody,
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestPutShowPlaylistRejectsUnknownCue proves this handler wires
// DecodeShowPlaylistPayload's resolveCue callback (h.cueLookup) against
// live store state, not a stub that always returns true.
func TestPutShowPlaylistRejectsUnknownCue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "does-not-exist"}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cue does-not-exist does not exist); body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeFieldUnknownReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v (a nonexistent cue must not be reported as a cross-show one)", problem["type"], wantType)
	}
}

// TestPutShowPlaylistRejectsCrossShowCue proves a Cue from a DIFFERENT
// show is refused, not merely a nonexistent one — the resolveCue callback
// must carry the Cue's own show, not just an existence bit.
func TestPutShowPlaylistRejectsCrossShowCue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	christmasCue := `{"show":"christmas-2026","name":"Sleigh Ride","outputs":{"render":{"sequence":"sleigh"}}}`
	mustPutCue(t, api, token, "sleigh", christmasCue)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "sleigh"}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cue sleigh belongs to a different show); body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeCrossShowReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v — a lookup regression that answered \"does not exist\" for every cue would still pass a bare status-code check", problem["type"], wantType)
	}
}

// TestPutShowPlaylistAcceptsSameShowCue is the positive twin of the two
// tests above: a same-show Cue reference succeeds.
func TestPutShowPlaylistAcceptsSameShowCue(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "showmesh-audio",
		"entries": [{"id": "e1", "cue": "thriller"}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowPlaylistRequiresConfigWrite matches show.cue's identical
// posture.
func TestPutShowPlaylistRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"show":"halloween-2026","name":"Main show","runner":"showmesh-audio","entries":[{"id":"e1","cue":"thriller"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, respBody)
	}
}

// TestListShowPlaylistsFiltersByShow mirrors TestListShowCuesFiltersByShow
// on the playlist kind.
func TestListShowPlaylistsFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)
	christmasCue := `{"show":"christmas-2026","name":"Sleigh Ride","outputs":{"render":{"sequence":"sleigh"}}}`
	mustPutCue(t, api, token, "sleigh", christmasCue)

	halloweenPlaylist := `{"show":"halloween-2026","name":"Main show","runner":"showmesh-audio","entries":[{"id":"e1","cue":"thriller"}]}`
	christmasPlaylist := `{"show":"christmas-2026","name":"Xmas show","runner":"showmesh-audio","entries":[{"id":"e1","cue":"sleigh"}]}`
	for id, body := range map[string]string{"halloween-main": halloweenPlaylist, "christmas-main": christmasPlaylist} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/"+id, body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.playlist/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
		}
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(filtered), `"christmas-main"`) {
		t.Fatalf("expected christmas-main in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsAll(string(filtered), `"halloween-main"`) {
		t.Fatalf("halloween-main leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

// TestPutShowPlaylistRejectsDuplicateSectionPosition proves the config
// package's own duplicate-(section,position) refusal reaches the wire as
// a 400, exercised through the real route rather than only unit-tested
// against config.DecodeShowPlaylistPayload directly.
func TestPutShowPlaylistRejectsDuplicateSectionPosition(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "fpp",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + playlistHash64 + `"},
		"entries": [
			{"id": "e1", "cue": "thriller", "fpp": {"section": "main", "position": 0}},
			{"id": "e2", "cue": "thriller", "fpp": {"section": "main", "position": 0}}
		]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (duplicate section/position); body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeEntryPositionDuplicate]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v — this test's own name claims the duplicate-position refusal fired, which a bare 400 never proves", problem["type"], wantType)
	}
}

// --- show immutability (TRACK-H-H1-SPEC.md section 2, fix list item 1) ---

// TestPutShowCueRejectsShowChange proves a PUT that re-points an existing
// Cue at a different Show is refused rather than silently accepted as an
// ordinary full replacement.
func TestPutShowCueRejectsShowChange(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	movedBody := `{"show":"christmas-2026","name":"Thriller","outputs":{"render":{"sequence":"thriller"}}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", movedBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (show is immutable); body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeCrossShowReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
	if detail, _ := problem["detail"].(string); !containsAll(detail, "halloween-2026") || !containsAll(detail, "christmas-2026") {
		t.Errorf("problem.detail = %q, want it to name both the stored and incoming show", detail)
	}
}

// TestPutShowCueAcceptsRewriteWithSameShow proves the immutability check
// does not also refuse an ordinary re-PUT that keeps the same show —
// only an actual change is refused.
func TestPutShowCueAcceptsRewriteWithSameShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	renamedBody := `{"show":"halloween-2026","name":"Thriller (renamed)","outputs":{"render":{"sequence":"thriller"}}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", renamedBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (same show, ordinary edit); body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowPlaylistRejectsShowChange is TestPutShowCueRejectsShowChange's
// playlist twin.
func TestPutShowPlaylistRejectsShowChange(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)
	christmasCue := `{"show":"christmas-2026","name":"Sleigh Ride","outputs":{"render":{"sequence":"sleigh"}}}`
	mustPutCue(t, api, token, "sleigh", christmasCue)

	body := `{"show":"halloween-2026","name":"Main show","runner":"showmesh-audio","entries":[{"id":"e1","cue":"thriller"}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial PUT show.playlist/main: status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}

	movedBody := `{"show":"christmas-2026","name":"Main show","runner":"showmesh-audio","entries":[{"id":"e1","cue":"sleigh"}]}`
	moveReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", movedBody,
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
	if detail, _ := problem["detail"].(string); !containsAll(detail, "halloween-2026") || !containsAll(detail, "christmas-2026") {
		t.Errorf("problem.detail = %q, want it to name both the stored and incoming show", detail)
	}
}

// --- safeCueRef at the HTTP layer (fix list item 8) ---

// TestPutShowPlaylistAcceptsSafeCueRef proves mismatchPolicy "safeCue"
// with a same-show safeCueRef reaches the wire and is stored, exercising
// the safeCue path's own trip through the cue lookup — untested at the
// API layer before this.
func TestPutShowPlaylistAcceptsSafeCueRef(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "fpp",
		"mismatchPolicy": "safeCue", "safeCueRef": "thriller",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + playlistHash64 + `"},
		"entries": [{"id": "e1", "cue": "thriller", "fpp": {"section": "main", "position": 0}}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
	var putResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(respBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, respBody)
	}
	if putResp.Payload.MismatchPolicy != "safeCue" || putResp.Payload.SafeCueRef != "thriller" {
		t.Errorf("payload.mismatchPolicy/safeCueRef = %q/%q, want safeCue/thriller", putResp.Payload.MismatchPolicy, putResp.Payload.SafeCueRef)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist/main", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if getResp.Payload.MismatchPolicy != "safeCue" || getResp.Payload.SafeCueRef != "thriller" {
		t.Errorf("GET payload.mismatchPolicy/safeCueRef = %q/%q, want safeCue/thriller", getResp.Payload.MismatchPolicy, getResp.Payload.SafeCueRef)
	}
}

// TestPutShowPlaylistRejectsCrossShowSafeCueRef proves a safeCueRef naming
// a Cue in a DIFFERENT show is refused at the HTTP layer, matching
// entries[].cue's own cross-show refusal.
func TestPutShowPlaylistRejectsCrossShowSafeCueRef(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"Christmas 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)
	christmasCue := `{"show":"christmas-2026","name":"Sleigh Ride","outputs":{"render":{"sequence":"sleigh"}}}`
	mustPutCue(t, api, token, "sleigh", christmasCue)

	body := `{
		"show": "halloween-2026", "name": "Main show", "runner": "fpp",
		"mismatchPolicy": "safeCue", "safeCueRef": "sleigh",
		"fpp": {"instanceUuid": "u", "playlistName": "p", "playlistHash": "` + playlistHash64 + `"},
		"entries": [{"id": "e1", "cue": "thriller", "fpp": {"section": "main", "position": 0}}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (safeCueRef sleigh belongs to a different show); body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeCrossShowReference]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
}

// --- full-payload PUT/GET round trips (fix list items 6, 7) ---

func float64Ptr(v float64) *float64 { return &v }

// TestPutShowCueRoundTripPreservesEveryField sends a Cue with every
// output populated and proves GET returns the identical payload — nothing
// today proves a value survives the store round trip, since the
// conformance test (openapi_showcueplaylist_test.go) checks schema shape
// only.
func TestPutShowCueRoundTripPreservesEveryField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	// render+audio+ltc, deliberately WITHOUT announcement: TRACK-H-cues-
	// and-playlists.md section H5 build item 5's own authoring-time
	// refusal (config/showcue.go's decodeShowCueOutputs) rejects a Cue
	// that declares both ltc and announcement, since a node has one LTC
	// generator tied to the program-audio clock domain and the
	// announcement session is not that domain. Announcement's own field
	// round trip is covered separately by
	// TestPutShowCueRoundTripPreservesAnnouncementFields below.
	body := `{
		"show": "halloween-2026",
		"name": "Full Cue",
		"outputs": {
			"render": {"sequence": "thriller"},
			"audio": {"asset": "thriller-audience", "startOffsetMillis": 1500},
			"ltc": {"startOffsetMillis": 2500}
		}
	}`
	want := v1.ConfigShowCue{
		Show: "halloween-2026", Name: "Full Cue",
		Outputs: v1.ConfigShowCueOutputs{
			Render: &v1.ConfigShowCueRenderOutput{Sequence: "thriller"},
			Audio:  &v1.ConfigShowCueAudioOutput{Asset: "thriller-audience", StartOffsetMillis: 1500},
			LTC:    &v1.ConfigShowCueLTCOutput{StartOffsetMillis: 2500},
		},
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/full", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, putBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, putBody)
	}
	var putResp v1.ShowCueConfigResponse
	if err := json.Unmarshal(putBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, putBody)
	}
	if !reflect.DeepEqual(putResp.Payload, want) {
		t.Errorf("PUT response payload = %+v, want %+v", putResp.Payload, want)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue/full", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.ShowCueConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if !reflect.DeepEqual(getResp.Payload, want) {
		t.Errorf("GET response payload = %+v, want %+v", getResp.Payload, want)
	}
}

// TestPutShowCueRoundTripPreservesAnnouncementFields is
// TestPutShowCueRoundTripPreservesEveryField's own announcement-output
// sibling, split out because a Cue must not declare both ltc and
// announcement (TRACK-H-cues-and-playlists.md section H5 build item 5).
func TestPutShowCueRoundTripPreservesAnnouncementFields(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	body := `{
		"show": "halloween-2026",
		"name": "Announcement Cue",
		"outputs": {
			"render": {"sequence": "thriller"},
			"audio": {"asset": "thriller-audience", "startOffsetMillis": 1500},
			"announcement": {"policy": "duck", "duckGainDb": -18, "fadeMillis": 300}
		}
	}`
	want := v1.ConfigShowCue{
		Show: "halloween-2026", Name: "Announcement Cue",
		Outputs: v1.ConfigShowCueOutputs{
			Render: &v1.ConfigShowCueRenderOutput{Sequence: "thriller"},
			Audio:  &v1.ConfigShowCueAudioOutput{Asset: "thriller-audience", StartOffsetMillis: 1500},
			Announcement: &v1.ConfigShowCueAnnouncementOutput{
				Policy: "duck", DuckGainDb: float64Ptr(-18), FadeMillis: 300,
			},
		},
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/announcement-full", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, putBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, putBody)
	}
	var putResp v1.ShowCueConfigResponse
	if err := json.Unmarshal(putBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, putBody)
	}
	if !reflect.DeepEqual(putResp.Payload, want) {
		t.Errorf("PUT response payload = %+v, want %+v", putResp.Payload, want)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.cue/announcement-full", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.ShowCueConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if !reflect.DeepEqual(getResp.Payload, want) {
		t.Errorf("GET response payload = %+v, want %+v", getResp.Payload, want)
	}
}

// TestPutShowPlaylistFPPRunnerRoundTripPreservesEveryField sends an "fpp"
// runner Playlist with every optional field populated — mismatchPolicy
// "safeCue" plus safeCueRef, the fpp binding, and an entry carrying
// expectedSequenceFilename/expectedMediaFilename (fix list item 7: these
// two fields appeared in no test anywhere, and they are exactly what H2
// compares an observation against) — and proves GET returns it unchanged.
// showmeshAudio is covered by the showmesh-audio runner twin below: a
// Playlist cannot declare both fpp and showmeshAudio.
func TestPutShowPlaylistFPPRunnerRoundTripPreservesEveryField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	body := `{
		"show": "halloween-2026",
		"name": "Main show",
		"runner": "fpp",
		"mismatchPolicy": "safeCue",
		"safeCueRef": "thriller",
		"fpp": {
			"instanceUuid": "11111111-1111-1111-1111-111111111111",
			"playlistName": "Halloween Main",
			"playlistHash": "` + playlistHash64 + `"
		},
		"entries": [
			{
				"id": "e1", "cue": "thriller",
				"fpp": {
					"section": "mainPlaylist", "position": 0,
					"expectedSequenceFilename": "Thriller.fseq",
					"expectedMediaFilename": "Thriller.mp3"
				}
			}
		]
	}`
	want := v1.ConfigShowPlaylist{
		Show: "halloween-2026", Name: "Main show", Runner: "fpp",
		MismatchPolicy: "safeCue", SafeCueRef: "thriller",
		FPP: &v1.ConfigShowPlaylistFPPBinding{
			InstanceUUID: "11111111-1111-1111-1111-111111111111",
			PlaylistName: "Halloween Main",
			PlaylistHash: playlistHash64,
		},
		Entries: []v1.ConfigShowPlaylistEntry{
			{
				ID: "e1", Cue: "thriller",
				FPP: &v1.ConfigShowPlaylistEntryFPP{
					Section: "mainPlaylist", Position: 0,
					ExpectedSequenceFilename: "Thriller.fseq",
					ExpectedMediaFilename:    "Thriller.mp3",
				},
			},
		},
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, putBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, putBody)
	}
	var putResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(putBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, putBody)
	}
	if !reflect.DeepEqual(putResp.Payload, want) {
		t.Errorf("PUT response payload = %+v, want %+v", putResp.Payload, want)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist/main", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if !reflect.DeepEqual(getResp.Payload, want) {
		t.Errorf("GET response payload = %+v, want %+v", getResp.Payload, want)
	}
}

// TestPutShowPlaylistShowmeshAudioRunnerRoundTripPreservesEveryField is
// the "showmesh-audio" runner twin: showmeshAudio.repeat is the field
// only this runner can populate, and mismatchPolicy/fpp are refused for
// it (TRACK-H-H1-SPEC.md section 3), so it needs its own round trip
// rather than sharing the fpp-runner test above.
func TestPutShowPlaylistShowmeshAudioRunnerRoundTripPreservesEveryField(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutCue(t, api, token, "thriller", validCueBody)

	body := `{
		"show": "halloween-2026",
		"name": "Audio only",
		"runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "all"},
		"entries": [{"id": "e1", "cue": "thriller"}]
	}`
	want := v1.ConfigShowPlaylist{
		Show: "halloween-2026", Name: "Audio only", Runner: "showmesh-audio",
		ShowmeshAudio: &v1.ConfigShowPlaylistShowmeshAudio{Repeat: "all"},
		Entries:       []v1.ConfigShowPlaylistEntry{{ID: "e1", Cue: "thriller"}},
	}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/audio-only", body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, putBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", resp.StatusCode, putBody)
	}
	var putResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(putBody, &putResp); err != nil {
		t.Fatalf("decode PUT response: %v; body: %s", err, putBody)
	}
	if !reflect.DeepEqual(putResp.Payload, want) {
		t.Errorf("PUT response payload = %+v, want %+v", putResp.Payload, want)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.playlist/audio-only", map[string]string{"Authorization": "Bearer " + token})
	var getResp v1.ShowPlaylistConfigResponse
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode GET response: %v; body: %s", err, getBody)
	}
	if !reflect.DeepEqual(getResp.Payload, want) {
		t.Errorf("GET response payload = %+v, want %+v", getResp.Payload, want)
	}
}
