package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestActionBindingsListReportsDanglingMacroStepBeforeDispatch is the
// dangling-reference pre-flight design's macro edge acceptance evidence:
// a show.macro step naming a show.action that has since been
// tombstone-deleted must be visible through THIS existing GET
// /api/v1/actions/bindings read, before the macro is ever run. See
// internal/coordinator/macro/resolve_tombstone_test.go's identical edge
// at actual dispatch time; that test proves the run fails safely, this
// one proves an operator can see the same fact before any run is
// submitted.
func TestActionBindingsListReportsDanglingMacroStepBeforeDispatch(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	putAction := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/house-volume", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putAction); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	putMacro := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/begin-set", validShowMacroBody("house-volume"),
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putMacro); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d; body: %s", resp.StatusCode, body)
	}

	// Delete the action the macro step names. The macro is untouched: the
	// dangling reference is entirely on the macro side.
	delReq := newJSONRequest(t, http.MethodDelete, "/api/v1/config/show.action/house-volume", `{"confirm":true}`,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, delReq); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings", map[string]string{"Authorization": "Bearer " + token})
	m := decodeMap(t, listBody)
	bindings, _ := m["bindings"].([]any)

	var found map[string]any
	for _, raw := range bindings {
		b, _ := raw.(map[string]any)
		if b["actionId"] == "house-volume" {
			found = b
		}
	}
	if found == nil {
		t.Fatalf("no binding entry for the dangling action id house-volume before dispatch; bindings: %s", listBody)
	}
	if found["state"] != "broken" {
		t.Fatalf("state = %v, want broken; body: %s", found["state"], listBody)
	}
	reason, _ := found["reason"].(string)
	if !strings.Contains(reason, "begin-set") || !strings.Contains(reason, "start") || !strings.Contains(reason, "house-volume") {
		t.Fatalf("reason = %q, want it to name the macro (begin-set), step (start) and dangling action (house-volume)", reason)
	}
	if found["show"] != "halloween-2026" {
		t.Fatalf("show = %v, want the macro's own show halloween-2026", found["show"])
	}
}

// TestActionBindingsListHealthyMacroAddsNoEntries is the negative half of
// the macro edge: a macro whose step names a live show.action must not
// add anything to the list, since a check that always emits something for
// every macro step could pass an acceptance test that only ever looks at
// the broken case.
func TestActionBindingsListHealthyMacroAddsNoEntries(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	putAction := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/house-volume", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putAction); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	putMacro := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/begin-set", validShowMacroBody("house-volume"),
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putMacro); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d; body: %s", resp.StatusCode, body)
	}

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings", map[string]string{"Authorization": "Bearer " + token})
	m := decodeMap(t, listBody)
	bindings, _ := m["bindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %v, want exactly 1 (the action's own binding, no macro-derived entry): %s", bindings, listBody)
	}
	if bindings[0].(map[string]any)["actionId"] != "house-volume" {
		t.Fatalf("bindings[0] = %v, want the action's own binding only", bindings[0])
	}
}
