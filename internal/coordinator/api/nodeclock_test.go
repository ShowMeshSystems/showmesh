package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track I seam I1's own test suite for the node.clock
// collection kind: the four routes, mirroring audionode_test.go's own
// shape one kind over, minus the placement-evidence check (node.clock has
// no analogous capability-advertisement dependency).

const validNodeClockBody = `{"provider":"managed","interface":"eth0","domain":24}`

func mustPutNodeClock(t *testing.T, api *API, token, id, body string) (int, string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/node.clock/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	return resp.StatusCode, string(respBody)
}

// TestPutNodeClockAcceptsValidPayload proves the happy path: a write
// succeeds and round-trips through GET.
func TestPutNodeClockAcceptsValidPayload(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutNodeClock(t, api, token, "render-01", validNodeClockBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, body)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/node.clock/render-01", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(getBody), `"provider":"managed"`) || !containsAll(string(getBody), `"interface":"eth0"`) {
		t.Fatalf("GET missing provider/interface; body: %s", getBody)
	}
}

// TestPutNodeClockRejectsUnknownProvider proves the payload decode's
// provider enum check is reachable through the real handler.
func TestPutNodeClockRejectsUnknownProvider(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutNodeClock(t, api, token, "render-01", `{"provider":"bogus","interface":"eth0","domain":0}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

// TestPutNodeClockRejectsFPPWithoutBaseURL proves fppBaseUrl's
// conditional-required rule is reachable through the real handler.
func TestPutNodeClockRejectsFPPWithoutBaseURL(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutNodeClock(t, api, token, "render-01", `{"provider":"fpp","interface":"eth0","domain":0}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
	if !containsAll(body, "fppBaseUrl") {
		t.Fatalf("body does not name the missing field; body: %s", body)
	}
}

// TestPutNodeClockRejectsInvalidObjectID proves the object id is
// validated as a node id BEFORE the request body is even read, mirroring
// TestPutAudioNodeRejectsInvalidObjectID one kind over.
func TestPutNodeClockRejectsInvalidObjectID(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	status, body := mustPutNodeClock(t, api, token, "not_valid", validNodeClockBody)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, body)
	}
}

// TestListNodeClocksReturnsConfiguredObjects proves the list route
// surfaces the provider as its Label, exercising the zero-to-one
// transition ADR-039 requires, mirroring
// TestListAudioNodesReturnsConfiguredObjects one kind over.
func TestListNodeClocksReturnsConfiguredObjects(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, listBody0 := doRequest(t, api.Handler, "GET", "/api/v1/config/node.clock", map[string]string{"Authorization": "Bearer " + token})
	if containsAll(string(listBody0), `"id":"render-01"`) {
		t.Fatalf("zero state already lists render-01; body: %s", listBody0)
	}

	if status, body := mustPutNodeClock(t, api, token, "render-01", validNodeClockBody); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", status, body)
	}

	_, listBody1 := doRequest(t, api.Handler, "GET", "/api/v1/config/node.clock", map[string]string{"Authorization": "Bearer " + token})
	if !containsAll(string(listBody1), `"id":"render-01"`) || !containsAll(string(listBody1), `"label":"managed"`) {
		t.Fatalf("list missing render-01/managed; body: %s", listBody1)
	}
}

// TestGetNodeClockRevisions proves the revisions route is wired.
func TestGetNodeClockRevisions(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if status, body := mustPutNodeClock(t, api, token, "render-01", validNodeClockBody); status != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", status, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/node.clock/render-01/revisions", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !containsAll(string(body), `"revision":1`) {
		t.Fatalf("revisions missing revision 1; body: %s", body)
	}
}
