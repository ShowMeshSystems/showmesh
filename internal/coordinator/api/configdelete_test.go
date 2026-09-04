package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// This file is the tombstone delete seam's own API-level test suite: the
// eight per-object kinds' DELETE routes (deleteConfigObjectRevision,
// showconfig.go), and the twelve singleton kinds' deliberate absence of
// one.

func mustDeleteAudioNode(t *testing.T, api *API, token, id string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	h := map[string]string{"Authorization": "Bearer " + token}
	for k, v := range headers {
		h[k] = v
	}
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/audio.node/"+id, `{"confirm":true}`, h)
	return doRawRequest(t, api.Handler, req)
}

// TestDeleteAudioNodeTombstonesExcludesFromListAndGetAndAudits is this
// seam's core acceptance requirement, exercised through the real HTTP
// route table rather than the store package directly: after DELETE, the
// object is gone from GET (resolution) and from the list, and the delete
// itself is recorded in the audit log.
func TestDeleteAudioNodeTombstonesExcludesFromListAndGetAndAudits(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
		nodeViewWithAudioCapabilities("render-02", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody); status != http.StatusOK {
		t.Fatalf("put render-01: status = %d, body: %s", status, body)
	}
	if status, body := mustPutAudioNode(t, api, token, "render-02", validAudioNodeBody); status != http.StatusOK {
		t.Fatalf("put render-02: status = %d, body: %s", status, body)
	}

	resp, body := mustDeleteAudioNode(t, api, token, "render-01", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete render-01: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	// Excluded from resolution (GET).
	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("GET deleted render-01: status = %d, want 404; body: %s", getResp.StatusCode, getBody)
	}

	// Excluded from the list, while the untouched sibling stays visible.
	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node", map[string]string{"Authorization": "Bearer " + token})
	if containsAll(string(listBody), `"render-01"`) {
		t.Errorf("list still names deleted render-01; body: %s", listBody)
	}
	if !containsAll(string(listBody), `"render-02"`) {
		t.Errorf("list is missing untouched render-02; body: %s", listBody)
	}

	// The delete itself is auditable.
	entries, err := st.ListAuditEntriesNewestFirst(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "config.delete" && e.Target == "audio.node/render-01" {
			found = true
			if e.PrincipalName != "admin-1" {
				t.Errorf("audit entry principal = %q, want admin-1", e.PrincipalName)
			}
		}
	}
	if !found {
		t.Errorf("no config.delete audit entry for audio.node/render-01 among %d entries", len(entries))
	}
}

// TestDeleteAudioNodeLeavesRevisionHistoryReadable proves ADR-009 survives
// through the real route table: GET .../revisions still lists every
// revision after the object is deleted, with none marked active.
func TestDeleteAudioNodeLeavesRevisionHistoryReadable(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	if resp, body := mustDeleteAudioNode(t, api, token, "render-01", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, body: %s", resp.StatusCode, body)
	}

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01/revisions", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(revBody), `"revision":1`) {
		t.Fatalf("revisions after delete missing revision 1; body: %s", revBody)
	}
	if containsAll(string(revBody), `"active":true`) {
		t.Fatalf("revisions after delete still mark one active; body: %s", revBody)
	}
}

// TestDeleteAudioNodeRequiresConfirmBody proves the confirm-body guard
// (mirroring DELETE /nodes/{nodeId}/declaration's own rule): a missing or
// false confirm is refused with 400 and deletes nothing.
func TestDeleteAudioNodeRequiresConfirmBody(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)

	cases := []string{``, `{}`, `{"confirm":false}`}
	for _, body := range cases {
		req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/audio.node/render-01", body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("delete with body %q: status = %d, want 400; body: %s", body, resp.StatusCode, respBody)
		}
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("render-01 must still exist after every unconfirmed delete attempt: status = %d, body: %s", getResp.StatusCode, getBody)
	}
}

// TestDeleteAudioNodeHonoursIfMatchPrecondition proves DELETE respects the
// same revision-guard discipline PUT already offers: a stale If-Match is
// refused with 409 and deletes nothing, and the correct revision succeeds.
func TestDeleteAudioNodeHonoursIfMatchPrecondition(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)

	if resp, body := mustDeleteAudioNode(t, api, token, "render-01", map[string]string{"If-Match": `"7"`}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale If-Match: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if resp, body := mustDeleteAudioNode(t, api, token, "render-01", map[string]string{"If-Match": `"1"`}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("correct If-Match: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteAudioNodeRejectsIfNoneMatch proves If-None-Match, which has no
// coherent meaning on a DELETE, is rejected with 400 rather than silently
// accepted and always-failing.
func TestDeleteAudioNodeRejectsIfNoneMatch(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)

	resp, body := mustDeleteAudioNode(t, api, token, "render-01", map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("If-None-Match on DELETE: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestDeleteAudioNodeRequiresConfigWriteScope proves DELETE is authorized
// with the same scope as PUT: a principal without config:write is refused
// with 403.
func TestDeleteAudioNodeRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutAudioNode(t, api, adminToken, "render-01", validAudioNodeBody)

	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)

	resp, body := mustDeleteAudioNode(t, api, operatorToken, "render-01", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("delete without config:write: status = %d, want 403; body: %s", resp.StatusCode, body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", map[string]string{"Authorization": "Bearer " + adminToken})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("render-01 must still exist after the refused delete: status = %d, body: %s", getResp.StatusCode, getBody)
	}
}

// TestPutAudioNodeAfterDeleteContinuesRevisionNumberingAndUndeletes is this
// seam's re-creation acceptance requirement, exercised through the real
// route table: PUTting a deleted id succeeds, is visible again, and its
// new revision continues from the object's true history rather than
// resetting to 1.
func TestPutAudioNodeAfterDeleteContinuesRevisionNumberingAndUndeletes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody) // revision 1
	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody) // revision 2
	mustDeleteAudioNode(t, api, token, "render-01", nil)

	status, body := mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody) // revision 3, expected
	if status != http.StatusOK {
		t.Fatalf("re-create after delete: status = %d, body: %s", status, body)
	}
	if !containsAll(body, `"revision":3`) {
		t.Fatalf("re-create after delete did not continue numbering at 3; body: %s", body)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/audio.node/render-01", map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("re-created render-01 must resolve again: status = %d, body: %s", getResp.StatusCode, getBody)
	}
}

// TestDeleteShowRefusesWhileActive proves the one referential-safety
// exception this design draws: deleting the show show.active currently
// names is refused with 409, rather than left dangling under a live "what
// is running now" selector.
func TestDeleteShowRefusesWhileActive(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	activeReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"halloween-2026"}`, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, activeReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("put show.active: status = %d, body: %s", resp.StatusCode, body)
	}

	delReq := newJSONRequest(t, http.MethodDelete, "/api/v1/config/show/halloween-2026", `{"confirm":true}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, delReq)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete the active show: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	// Once show.active names something else, the delete succeeds.
	putShow2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show/christmas-2026", `{"name":"Christmas 2026"}`, map[string]string{"Authorization": "Bearer " + token})
	doRawRequest(t, api.Handler, putShow2)
	activeReq2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"christmas-2026"}`, map[string]string{"Authorization": "Bearer " + token})
	doRawRequest(t, api.Handler, activeReq2)

	delReq2 := newJSONRequest(t, http.MethodDelete, "/api/v1/config/show/halloween-2026", `{"confirm":true}`, map[string]string{"Authorization": "Bearer " + token})
	resp2, body2 := doRawRequest(t, api.Handler, delReq2)
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("delete a show no longer active: status = %d, want 204; body: %s", resp2.StatusCode, body2)
	}
}

// TestDeleteNightSessionRefusesWhileActive mirrors
// TestDeleteShowRefusesWhileActive one kind over: deleting the session
// night.session.active currently names is refused with 409.
func TestDeleteNightSessionRefusesWhileActive(t *testing.T) {
	api, _, token := setupNightSessionFixture(t)
	mustPutNightSession(t, api, token, "halloween-main", validNightSessionBody)

	setActive := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":"halloween-main"}`, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, setActive); resp.StatusCode != http.StatusOK {
		t.Fatalf("put night.session.active: status = %d, body: %s", resp.StatusCode, body)
	}

	del := newJSONRequest(t, http.MethodDelete, "/api/v1/config/night.session/halloween-main", `{"confirm":true}`, map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, del)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete the active night.session: status = %d, want 409; body: %s", resp.StatusCode, body)
	}

	clearActive := newJSONRequest(t, http.MethodPut, "/api/v1/config/night.session.active", `{"session":""}`, map[string]string{"Authorization": "Bearer " + token})
	doRawRequest(t, api.Handler, clearActive)

	del2 := newJSONRequest(t, http.MethodDelete, "/api/v1/config/night.session/halloween-main", `{"confirm":true}`, map[string]string{"Authorization": "Bearer " + token})
	resp2, body2 := doRawRequest(t, api.Handler, del2)
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("delete a night.session no longer active: status = %d, want 204; body: %s", resp2.StatusCode, body2)
	}
}

// TestDeleteAudioNodeNotFoundForNeverCreatedAndAlreadyDeleted proves the
// 404 path: an id that was never created, and a second delete of one
// already deleted, both refuse the same way.
func TestDeleteAudioNodeNotFoundForNeverCreatedAndAlreadyDeleted(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{
		nodeViewWithAudioCapabilities("render-01", []string{"hw:0,0"}, []string{"hw:0,0"}),
	})
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if resp, body := mustDeleteAudioNode(t, api, token, "never-created", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete a never-created id: status = %d, want 404; body: %s", resp.StatusCode, body)
	}

	mustPutAudioNode(t, api, token, "render-01", validAudioNodeBody)
	mustDeleteAudioNode(t, api, token, "render-01", nil)
	if resp, body := mustDeleteAudioNode(t, api, token, "render-01", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete of an already-deleted id: status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// singletonConfigDeletePaths lists every one of the twelve singleton
// configuration kinds' own GET/PUT path (config.go's own doc comment for
// the design ruling behind this list): a singleton is emptied or reset
// through its own PUT and is never deleted, so none of these registers a
// DELETE handler.
var singletonConfigDeletePaths = []string{
	"/api/v1/config/assets.settings",
	"/api/v1/config/audio.settings",
	"/api/v1/config/show.emergencystop",
	"/api/v1/config/fppconnect.settings",
	"/api/v1/config/fpp.endpoints",
	"/api/v1/config/fpp.mqtt",
	"/api/v1/config/night.session.active",
	"/api/v1/config/render.settings",
	"/api/v1/config/resolume.instances",
	"/api/v1/config/resolume.recovery",
	"/api/v1/config/show.active",
	"/api/v1/config/show.mode",
}

// TestDeleteSingletonPathsReturn405WithAllowHeader is the contract
// correction the owner ruling required: not merely an emergent property of
// net/http.ServeMux's own method-not-allowed behavior, but a named,
// tested guarantee, so a future refactor cannot quietly change it. No
// Authorization header is sent: ServeMux answers 405 for a matched path
// pattern carrying no DELETE handler before any scope guard ever runs.
func TestDeleteSingletonPathsReturn405WithAllowHeader(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	for _, path := range singletonConfigDeletePaths {
		req := newJSONRequest(t, http.MethodDelete, path, "", nil)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("DELETE %s: status = %d, want 405; body: %s", path, resp.StatusCode, body)
		}
		if resp.Header.Get("Allow") == "" {
			t.Errorf("DELETE %s: no Allow header on the 405 response", path)
		}
	}
}
