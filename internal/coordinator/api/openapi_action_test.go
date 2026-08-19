package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestOpenAPIActionDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed with the action-binding and
// action-invocation schemas.
func TestOpenAPIActionDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ActionBinding", "ActionBindingResponse", "ActionBindingsResponse",
		"ActionInvocationRequest", "ActionInvocationResult", "ActionInvocationResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIActionBindingResponsesMatchRealResponses proves GET
// /actions/{id}/binding and GET /actions/bindings against a real
// coordinator wiring.
func TestOpenAPIActionBindingResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "start-main", validShowActionFPPBody)

	_, oneBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/start-main/binding", nil)
	assertMatchesSchema(t, c, "ActionBindingResponse", oneBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/actions/bindings", nil)
	assertMatchesSchema(t, c, "ActionBindingsResponse", listBody)
}

// TestOpenAPIActionInvocationResponseMatchesRealResponse proves POST
// /actions/{id}/invocations' response against a real coordinator wiring.
func TestOpenAPIActionInvocationResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeActions = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "openapi-key-1", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ActionInvocationResponse", body)

	// idempotencyKey reused against a different action id: a real 409,
	// validated against the shared Problem schema and its own type enum.
	mustPutAction(t, api, token, "blackout-again", validShowActionResolumeBlackoutBody)
	conflictResp, conflictBody := doRawRequest(t, api.Handler, invokeActionRequest("blackout-again", "openapi-key-1", token))
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", conflictResp.StatusCode, conflictBody)
	}
	assertMatchesSchema(t, c, "Problem", conflictBody)
}
