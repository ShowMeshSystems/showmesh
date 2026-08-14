package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 9 wave 2's own conformance coverage, following
// TestOpenAPIConfigResponsesMatchRealResponses' exact pattern one file
// over: every new schema this wave added is validated against a REAL
// response from a real coordinator wiring, never hand-built JSON.

// TestOpenAPIShowConfigDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this wave added.
func TestOpenAPIShowConfigDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigObjectSummary", "ConfigObjectsListResponse",
		"ConfigShowActionMQTTPublish", "ConfigShowActionMQTTExpect", "ConfigShowActionTarget",
		"ConfigShowAction", "ShowActionConfigResponse",
		"ConfigShowMacroLocalFallback", "ConfigShowMacroStep", "ConfigShowMacro", "ShowMacroConfigResponse",
		"MacroRunSummary", "MacroRunStepCommand", "MacroRunStep", "MacroRun",
		"MacroRunResponse", "MacroRunSubmitResponse", "MacroRunsListResponse",
		"MacroPriorFailureRequest", "CreateMacroRunRequest", "MacroRunChangedEvent",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIShowConfigResponsesMatchRealResponses proves the four routes
// per kind (STEP-9-SPEC.md section 5.5) against a real coordinator wiring.
func TestOpenAPIShowConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	putActionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	putActionResp, putActionBody := doRawRequest(t, api.Handler, putActionReq)
	if putActionResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action: status = %d, want 200; body: %s", putActionResp.StatusCode, putActionBody)
	}
	assertMatchesSchema(t, c, "ShowActionConfigResponse", putActionBody)

	_, getActionBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/start-main-show", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ShowActionConfigResponse", getActionBody)

	_, listActionBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listActionBody)

	_, revActionBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/start-main-show/revisions", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revActionBody)

	putMacroReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/begin-set", validShowMacroBody("start-main-show"),
		map[string]string{"Authorization": "Bearer " + token})
	putMacroResp, putMacroBody := doRawRequest(t, api.Handler, putMacroReq)
	if putMacroResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d, want 200; body: %s", putMacroResp.StatusCode, putMacroBody)
	}
	assertMatchesSchema(t, c, "ShowMacroConfigResponse", putMacroBody)

	_, getMacroBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro/begin-set", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ShowMacroConfigResponse", getMacroBody)

	_, listMacroBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigObjectsListResponse", listMacroBody)

	_, revMacroBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro/begin-set/revisions", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revMacroBody)

	// The show.action validation-error shape, one representative sample —
	// mapValidationError's own completeness is proven exhaustively by
	// TestShowConfigValidationCodesAllMapToDistinctProblemTypes
	// (showconfig_test.go); this is the "matches the real Problem schema"
	// half.
	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/no-steps", `{"show":"x","label":"y"}`,
		map[string]string{"Authorization": "Bearer " + token})
	badResp, badBody := doRawRequest(t, api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT invalid show.macro: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}

// TestOpenAPIMacroRunResponsesMatchRealResponses proves the run surface
// (STEP-9-SPEC.md section 6.6) against a real coordinator wiring, driven
// through [fakeMacroRunner] (this package cannot import
// internal/coordinator/macro — see macroruns_test.go's own top comment).
func TestOpenAPIMacroRunResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)

	cmdID := "cmd-1"
	commands := newFakeCommandStore()
	commands.setCommand(store.CommandRecord{
		ID: cmdID, IdempotencyKey: "run-1/0", Action: "startPlaylist", TargetID: "player-01",
		ParamsJSON: `{"playlist":"Halloween Main"}`, ResultJSON: `{"outcome":"confirmed"}`,
		OutcomeState: "current", OutcomeReason: "matched", State: "resolved",
	})
	macros := &fakeMacroRunner{
		submitResult: MacroRunResult{Run: store.MacroRunRecord{ID: "run-1", MacroObjectID: "begin-set", Show: "halloween-2026", Trigger: "ui", CreatedAt: testNow, State: "running"}},
		getResult: MacroRunResult{
			Run: store.MacroRunRecord{ID: "run-1", MacroObjectID: "begin-set", Show: "halloween-2026", Trigger: "ui", CreatedAt: testNow, State: "finished"},
			Steps: []store.MacroRunStepRecord{
				{RunID: "run-1", StepIndex: 0, StepID: "s0", ActionObjectID: "a0", Integration: "fpp", SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "resolved", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok", CommandID: &cmdID},
				{RunID: "run-1", StepIndex: 1, StepID: "s1", ActionObjectID: "a1", Integration: "mqtt", SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "resolved", Outcome: "unconfirmable", OutcomeState: "unconfirmable_declared", OutcomeReason: "no expected response"},
			},
		},
		listResult: []store.MacroRunRecord{{ID: "run-1", MacroObjectID: "begin-set", Show: "halloween-2026", Trigger: "ui", CreatedAt: testNow, State: "finished"}},
	}
	api := New(macroRunTestDeps(svc, macros, commands), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	postReq := newJSONRequest(t, http.MethodPost, "/api/v1/macros/begin-set/runs", `{"idempotencyKey":"k1","trigger":"ui"}`,
		map[string]string{"Authorization": "Bearer " + token})
	postResp, postBody := doRawRequest(t, api.Handler, postReq)
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST run: status = %d, want 202; body: %s", postResp.StatusCode, postBody)
	}
	assertMatchesSchema(t, c, "MacroRunSubmitResponse", postBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/macro-runs/run-1", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "MacroRunResponse", getBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/macro-runs", map[string]string{"Authorization": "Bearer " + token})
	assertMatchesSchema(t, c, "MacroRunsListResponse", listBody)
}
