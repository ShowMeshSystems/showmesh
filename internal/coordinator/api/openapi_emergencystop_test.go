package api

import (
	"net/http"
	"testing"
)

// The emergency-stop feature's own conformance coverage, following openapi_showmode_test.go's
// exact pattern: every schema this seam added is validated against a REAL
// response from a real coordinator wiring, never hand-built JSON.

func TestOpenAPIEmergencyStopDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"ConfigEmergencyStopLevelPayload", "ConfigEmergencyStopPayload", "EmergencyStopConfigResponse",
		"EmergencyStopRequest", "EmergencyStopInstanceOutcome", "EmergencyStopFollowUpResult",
		"EmergencyStopNightSessionOutcome", "EmergencyStopResult", "EmergencyStopResponse",
		"EmergencyStopArmRequest", "EmergencyStopArmResponse", "EmergencyStopFireRequest",
	} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIEmergencyStopResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	now := testNow
	f := newEmergencyStopFixture(t, now)
	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}

	// Unconfigured default.
	_, defaultBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.emergencystop", auth)
	assertMatchesSchema(t, c, "EmergencyStopConfigResponse", defaultBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop", emergencyStopWorklightsOnStopBody(), auth)
	putResp, putBody := doRawRequest(t, f.api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.emergencystop: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopConfigResponse", putBody)

	_, getBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.emergencystop", auth)
	assertMatchesSchema(t, c, "EmergencyStopConfigResponse", getBody)

	_, revBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.emergencystop/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)

	badReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop",
		`{"stop":{"actions":["no-such-action"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, auth)
	badResp, badBody := doRawRequest(t, f.api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PUT show.emergencystop with an unknown action id: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)

	// Trigger routes.
	stopReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"conf-stop-1"}`, auth)
	stopResp, stopBody := doRawRequest(t, f.api.Handler, stopReq)
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("stop: status = %d, want 200; body: %s", stopResp.StatusCode, stopBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopResponse", stopBody)

	pdReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"conf-pd-1"}`, auth)
	pdResp, pdBody := doRawRequest(t, f.api.Handler, pdReq)
	if pdResp.StatusCode != http.StatusOK {
		t.Fatalf("stop-power-down: status = %d, want 200; body: %s", pdResp.StatusCode, pdBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopResponse", pdBody)

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"conf-arm-1"}`, auth)
	armResp, armBody := doRawRequest(t, f.api.Handler, armReq)
	if armResp.StatusCode != http.StatusOK {
		t.Fatalf("arm: status = %d, want 200; body: %s", armResp.StatusCode, armBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopArmResponse", armBody)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"conf-fire-1","armToken":"`+armToken+`"}`, auth)
	fireResp, fireBody := doRawRequest(t, f.api.Handler, fireReq)
	if fireResp.StatusCode != http.StatusOK {
		t.Fatalf("fire: status = %d, want 200; body: %s", fireResp.StatusCode, fireBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopResponse", fireBody)

	// The not-armed refusal's own problem type.
	fireAgainReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"conf-fire-2","armToken":"`+armToken+`"}`, auth)
	fireAgainResp, fireAgainBody := doRawRequest(t, f.api.Handler, fireAgainReq)
	if fireAgainResp.StatusCode != http.StatusConflict {
		t.Fatalf("second fire: status = %d, want 409; body: %s", fireAgainResp.StatusCode, fireAgainBody)
	}
	assertMatchesSchema(t, c, "Problem", fireAgainBody)
}

func TestOpenAPIEmergencyStopDuplicateActionWithinOneLevelMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	f := newEmergencyStopFixture(t, testNow)
	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.emergencystop",
		`{"stop":{"actions":["worklights-on","worklights-on"]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]}}`, auth)
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)
}
