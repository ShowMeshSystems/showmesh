package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// mutableClock returns an advance function and a clock function sharing
// one mutable instant, for a test that needs two writes to land at
// genuinely different times — fixedClock's single frozen value can never
// produce that, which is exactly why the review found a bug this
// package's fixed-clock harness could not have caught.
func mutableClock(start time.Time) (advance func(time.Duration), now func() time.Time) {
	cur := start
	return func(d time.Duration) { cur = cur.Add(d) }, func() time.Time { return cur }
}

// This file is Track F seam F1's own test suite for night.session and
// night.session.active. It follows showconfig_test.go/showobjects_test.go's
// existing pattern one seam over: a real *store.Store and a real
// identity.Service, driven through the real route table.

// nightSessionTestDeps mirrors showConfigTestDeps, additionally wiring
// Assets against the same store so [handlers.nightSessionAssetCurrent] has
// a real ListAssets to read from — showConfigTestDeps leaves Assets nil
// (defaulting to [noAssetStore], under which no asset is ever current),
// which is correct for that file's own tests but wrong here.
func nightSessionTestDeps(svc identity.Service, st *store.Store) Dependencies {
	deps := showConfigTestDeps(svc, st)
	deps.Assets = st
	return deps
}

func mustCreateNightSessionAsset(t *testing.T, st *store.Store, show, sequence, target string) {
	t.Helper()
	_, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: show + "-" + sequence + "-" + target, ShowID: show, SequenceID: sequence,
		TargetKind: store.AssetTargetKindNode, TargetID: target,
		MediaType: "fseq", ContentHash: "sha256:" + sequence, RuntimeFilename: sequence + ".fseq",
		SizeBytes: 1024, Backend: "volume", StorageKey: sequence,
	})
	if err != nil {
		t.Fatalf("create asset %s/%s/%s: %v", show, sequence, target, err)
	}
}

func mustPutShowAction(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

// validNightSessionBody assumes: FPP instance "player-01" (showConfigTestDeps),
// show.action "lighting-fade-out" (created by the caller), and an asset for
// (halloween-2026, resting-loop, player-01) (created by the caller).
const validNightSessionBody = `{
	"show": "halloween-2026",
	"label": "Halloween main loop",
	"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
	"resting": {
		"fppInstanceId": "player-01",
		"playlist": "halloween-resting",
		"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
		"endOfNightRepeat": true
	},
	"enterShow": {
		"cues": [
			{"name": "lighting-fade", "role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true}
		],
		"blackoutHoldMs": 6000
	},
	"enterResting": {"cues": [], "blackoutAfterShowMs": 6000}
}`

func mustPutNightSession(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT night.session/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

// setupNightSessionFixture creates the show.action and asset a valid
// night.session write depends on, returning the admin token.
func setupNightSessionFixture(t *testing.T) (*API, *store.Store, string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShowAction(t, api, token, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	return api, st, token
}

func TestPutAndGetNightSessionRoundTrips(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"endOfNightPlaylist":"halloween-resting"`) {
		t.Fatalf("expected endOfNightPlaylist to default to the resting playlist; body: %s", body)
	}
	if !containsAll(string(body), `"onFailure":"continue"`) {
		t.Fatalf("expected the cue's onFailure default to be resolved on the wire, not left blank; body: %s", body)
	}
}

// TestGetNightSessionRevisionReturnsPastPayload proves the
// /revisions/{n} route returns a SPECIFIC, possibly non-current
// revision's full payload, unlike GET .../night.session/{id} (always the
// active one) and unlike GET .../revisions (metadata only, no payload).
func TestGetNightSessionRevisionReturnsPastPayload(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)
	revision2 := `{
		"show": "halloween-2026",
		"label": "Halloween main loop v2",
		"showPlaylist": {"fppInstanceId": "player-01", "playlist": "halloween-show"},
		"resting": {
			"fppInstanceId": "player-01",
			"playlist": "halloween-resting",
			"timelineAsset": {"show": "halloween-2026", "sequence": "resting-loop", "target": "player-01"},
			"endOfNightRepeat": true
		},
		"enterShow": {"cues": [], "blackoutHoldMs": 6000},
		"enterResting": {"cues": [], "blackoutAfterShowMs": 6000}
	}`
	mustPutNightSession(t, api, token, "halloween-main", revision2)

	resp1, body1 := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/1", map[string]string{"Authorization": "Bearer " + token})
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("revision 1: status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if !containsAll(string(body1), `"label":"Halloween main loop"`) || containsAll(string(body1), "v2") {
		t.Fatalf("expected revision 1's original label, not the current one; body: %s", body1)
	}

	resp2, body2 := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/2", map[string]string{"Authorization": "Bearer " + token})
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("revision 2: status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if !containsAll(string(body2), `"label":"Halloween main loop v2"`) {
		t.Fatalf("expected revision 2's label; body: %s", body2)
	}

	resp3, body3 := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/99", map[string]string{"Authorization": "Bearer " + token})
	if resp3.StatusCode != http.StatusNotFound {
		t.Fatalf("nonexistent revision: status = %d, want 404; body: %s", resp3.StatusCode, body3)
	}
}

// TestGetNightSessionRevisionReportsItsOwnCreationTime is the review's
// finding 6, restated as a test that can actually fail: a fixed clock
// (every other test in this file) gives every revision the SAME
// timestamp, so a mapping bug that reports the wrong one is invisible.
// This test uses a real advancing clock and asserts revision 1 reports
// the FIRST write's time, not the current object's latest write time.
//
// Broken and confirmed to fail: reverted mapNightSessionConfigResponse to
// use obj.UpdatedAt instead of rev.CreatedAt — this test's revision-1
// assertion failed, reporting the second write's timestamp for the first
// revision. Restored afterward.
func TestGetNightSessionRevisionReportsItsOwnCreationTime(t *testing.T) {
	advance, clock := mutableClock(testNow)
	svc, st, _ := newTestIdentityServiceWithStore(t, clock)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: clock, Logger: testLogger()})
	mustPutShowAction(t, api, token, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")

	firstWriteTime := clock()
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)
	advance(time.Hour)
	secondWriteTime := clock()
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	_, rev1Body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/1", map[string]string{"Authorization": "Bearer " + token})
	_, rev2Body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session/halloween-main/revisions/2", map[string]string{"Authorization": "Bearer " + token})

	// .UTC(): the store round-trips a time.Time through SQLite as UTC, so
	// the response's timestamp string is always in that zone; firstWriteTime/
	// secondWriteTime carry testNow's own -05:00 location until normalized
	// the same way, even though both name the identical instant.
	wantRev1 := `"updatedAt":"` + formatTime(firstWriteTime.UTC()) + `"`
	wantRev2 := `"updatedAt":"` + formatTime(secondWriteTime.UTC()) + `"`
	if !containsAll(string(rev1Body), wantRev1) {
		t.Fatalf("revision 1: want %s, body: %s", wantRev1, rev1Body)
	}
	if !containsAll(string(rev2Body), wantRev2) {
		t.Fatalf("revision 2: want %s, body: %s", wantRev2, rev2Body)
	}
	if containsAll(string(rev1Body), wantRev2) {
		t.Fatalf("revision 1 must not report revision 2's timestamp; body: %s", rev1Body)
	}
}

func TestPutNightSessionRejectsCalendarField(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main",
		`{"show":"halloween-2026","label":"x","at":"20:00","showPlaylist":{},"resting":{},"enterShow":{},"enterResting":{}}`,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), "show-config-calendar-field-rejected") {
		t.Fatalf("expected the calendar-field-rejected problem type; body: %s", body)
	}
}

func TestPutNightSessionRejectsSiteControl(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	body := validNightSessionBody[:len(validNightSessionBody)-1] + `,"siteControl":{}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	if !containsAll(string(respBody), "show-config-not-implemented") {
		t.Fatalf("expected the not-implemented problem type; body: %s", respBody)
	}
}

func TestPutNightSessionRejectsDanglingAsset(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShowAction(t, api, token, "lighting-fade-out", validShowActionFPPBody)
	// Deliberately no CreateAsset call: the timelineAsset reference dangles.

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session/halloween-main", validNightSessionBody, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), "show-config-field-unknown-reference") {
		t.Fatalf("expected the field-unknown-reference problem type; body: %s", body)
	}
}

// TestNightSessionReadRequiresOperatorOrShowMacroRunOrConfigWrite mirrors
// TestShowConfigReadRequiresOperatorOrShowMacroRunOrConfigWrite one kind
// over.
func TestNightSessionReadRequiresOperatorOrShowMacroRunOrConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})
	t.Run("operator holds show:macro:run, not config:write", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session", map[string]string{"Authorization": "Bearer " + operatorToken})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})
}

// --- night.session.active ---

func TestGetNightSessionActiveWithNothingActivatedIs404(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestNightSessionActiveRejectsUnknownSession(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(nightSessionTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"no-such-session"}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestNightSessionActiveZeroToOneToZero is ADR-039 rule 4's own required
// transition, restated as a test: set the pointer, then clear it back to
// unset with an explicit empty session, and confirm GET reflects the clear
// (never a 404 that hides a stale value, never a refusal to clear).
func TestNightSessionActiveZeroToOneToZero(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	set := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"halloween-main"}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, set)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"session":"halloween-main"`) {
		t.Fatalf("expected session halloween-main; body: %s", body)
	}

	clear := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":""}`, map[string]string{"Authorization": "Bearer " + token})
	resp2, body2 := doRawRequest(t, api.Handler, clear)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("clear: status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if !containsAll(string(body2), `"session":""`) {
		t.Fatalf("expected the pointer cleared to empty; body: %s", body2)
	}

	resp3, body3 := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active", map[string]string{"Authorization": "Bearer " + token})
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("get after clear: status = %d, want 200; body: %s", resp3.StatusCode, body3)
	}
	if !containsAll(string(body3), `"session":""`) {
		t.Fatalf("expected GET to reflect the cleared pointer; body: %s", body3)
	}

	revResp, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active/revisions", map[string]string{"Authorization": "Bearer " + token})
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("revisions: status = %d, want 200; body: %s", revResp.StatusCode, revBody)
	}
	if !containsAll(string(revBody), `"revision":2`) {
		t.Fatalf("expected two accumulated revisions (set, then clear); body: %s", revBody)
	}
}

// TestNightSessionActiveObjectIDIsFixedConstant mirrors
// TestShowActiveObjectIDIsFixedConstant one kind over.
func TestNightSessionActiveObjectIDIsFixedConstant(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	for i := 0; i < 2; i++ {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"halloween-main"}`, map[string]string{"Authorization": "Bearer " + token})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("iteration %d: status = %d, want 200; body: %s", i, resp.StatusCode, body)
		}
	}

	revResp, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/night.session.active/revisions", map[string]string{"Authorization": "Bearer " + token})
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("revisions: status = %d, want 200; body: %s", revResp.StatusCode, revBody)
	}
	if !containsAll(string(revBody), `"revision":2`) {
		t.Fatalf("expected two revisions of the SAME object, never two objects; body: %s", revBody)
	}
}
