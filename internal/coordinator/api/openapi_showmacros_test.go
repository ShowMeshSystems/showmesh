package api

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

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
		"ConfigShowActionMQTTPublish", "ConfigShowActionMQTTPublishWrite",
		"ConfigShowActionMQTTExpect", "ConfigShowActionTarget", "ConfigShowActionTargetWrite",
		"ConfigShowAction", "ConfigShowActionWrite", "ShowActionConfigResponse",
		"ConfigShowMacroLocalFallback", "ConfigShowMacroStep", "ConfigShowMacroStepWrite",
		"ConfigShowMacro", "ConfigShowMacroWrite", "ShowMacroConfigResponse",
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

// TestOpenAPIWriteSchemasAcceptTheOperatorsActualRequestBody is the
// defect this file's own review found: PUT /config/show.macro/{id} and
// PUT /config/show.action/{id} pointed their requestBody at the STRICT
// read/stored schema, which requires onFailure, onUnconfirmed, and
// target.publish.retain, even though the handler (config/showmacro.go,
// config/showaction.go's decodeDefaultedEnum/decodeDefaultedBool) treats
// an absent key on any of the three as "apply the documented default" —
// making the default path unreachable through any client that validates
// its own request against the published schema first.
//
// validShowActionFPPBody and validShowMacroBody (showconfig_test.go) are
// this package's own long-standing fixtures for a real, successful write:
// both omit onFailure/onUnconfirmed already, and this test additionally
// exercises an mqtt target that omits target.publish.retain. All three
// must validate against the WRITE schema. The strict READ schema must
// still reject the identical bodies — proving the split is load-bearing,
// not merely a rename: a reader who deletes the split and points PUT back
// at the strict schema gets a failing test here, not a passing one that
// happens not to notice.
func TestOpenAPIWriteSchemasAcceptTheOperatorsActualRequestBody(t *testing.T) {
	c := newOpenAPICompiler(t)

	assertMatchesSchema(t, c, "ConfigShowActionWrite", []byte(validShowActionFPPBody))
	assertMatchesSchema(t, c, "ConfigShowMacroWrite", []byte(validShowMacroBody("start-main-show")))

	mqttActionOmittingRetain := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"home-automation",
		"publish":{"topic":"home/projectors/set","payload":"ON","qos":1},
		"expect":{"kind":"none"}}}`
	assertMatchesSchema(t, c, "ConfigShowActionWrite", []byte(mqttActionOmittingRetain))

	for _, tc := range []struct {
		schema, body string
	}{
		{"ConfigShowAction", validShowActionFPPBody},
		{"ConfigShowMacro", validShowMacroBody("start-main-show")},
		{"ConfigShowAction", mqttActionOmittingRetain},
	} {
		sch := compileSchema(t, c, tc.schema)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(tc.body)))
		if err != nil {
			t.Fatalf("decoding fixture: %v", err)
		}
		if err := sch.Validate(instance); err == nil {
			t.Errorf("a real, successful write request validated cleanly against the STRICT %s schema; it must be rejected there (that schema requires the resolved value) for %s to be doing anything", tc.schema, tc.schema+"Write")
		}
	}
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
				// A third, still-unresolved step: buildStepRecords (macro/resolve.go)
				// writes State "pending" and Outcome "" for every step at run
				// creation, and STEP-9-SPEC.md section 6.6 requires an in-flight
				// run's steps to be readable, so a real response can and does
				// carry this shape. MacroRunStep.outcome's enum must include ""
				// alongside section 6.4's five resolved values or this response
				// fails validation — this step exists to prove that stays true.
				{RunID: "run-1", StepIndex: 2, StepID: "s2", ActionObjectID: "a2", Integration: "fpp", SafetyClass: "none", LocalFallbackClass: "coordinator-required", State: "pending", Outcome: "", OutcomeState: store.MacroRunStepOutcomeStatePending, OutcomeReason: store.MacroRunStepOutcomeReasonPending},
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
