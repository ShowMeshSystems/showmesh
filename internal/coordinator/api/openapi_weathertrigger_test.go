package api

import (
	"net/http"
	"testing"
	"time"
)

// ADR-053 decision 12's own conformance coverage, following
// openapi_emergencystop_test.go's exact pattern: every schema this seam
// added is validated against a REAL response from a real coordinator
// wiring, never hand-built JSON.

func TestOpenAPIWeatherDelayTriggerDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"WeatherDelayPendingDecision", "WeatherDelaySourceHealth",
		"WeatherDelayTriggerRequest", "WeatherDelayTriggerResponse",
		"WeatherDelayDecisionRequest", "WeatherDelayDecisionResponse",
		"ConfigWeatherDelayNWSTriggerPayload",
	} {
		compileSchema(t, c, name)
	}
}

func TestOpenAPIWeatherDelayTriggerResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, _, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	// The default GET, before any trigger, already carries the (empty)
	// sources array and no pending decision.
	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/weather-delay", auth)
	assertMatchesSchema(t, c, "WeatherDelayStateResponse", getBody)

	triggerReq := newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/triggers/nws",
		`{"kind":"warning","eventType":"Tornado Warning","severity":"Extreme"}`, auth)
	triggerResp, triggerBody := doRawRequest(t, api.Handler, triggerReq)
	if triggerResp.StatusCode != http.StatusOK {
		t.Fatalf("trigger: status = %d, want 200; body: %s", triggerResp.StatusCode, triggerBody)
	}
	assertMatchesSchema(t, c, "WeatherDelayTriggerResponse", triggerBody)

	_, getAfterTrigger := doRequest(t, api.Handler, "GET", "/api/v1/weather-delay", auth)
	assertMatchesSchema(t, c, "WeatherDelayStateResponse", getAfterTrigger)

	pending := decodeJSON[struct {
		PendingDecision struct {
			ID string `json:"id"`
		} `json:"pendingDecision"`
	}](t, getAfterTrigger)

	decisionReq := newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision",
		`{"id":"`+pending.PendingDecision.ID+`","answer":"dismiss"}`, auth)
	decisionResp, decisionBody := doRawRequest(t, api.Handler, decisionReq)
	if decisionResp.StatusCode != http.StatusOK {
		t.Fatalf("decision: status = %d, want 200; body: %s", decisionResp.StatusCode, decisionBody)
	}
	assertMatchesSchema(t, c, "WeatherDelayDecisionResponse", decisionBody)

	badTriggerReq := newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/triggers/nws", `{"kind":"bogus"}`, auth)
	badResp, badBody := doRawRequest(t, api.Handler, badTriggerReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trigger with a bad kind: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)

	badDecisionReq := newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision", `{"id":"missing","answer":"dismiss"}`, auth)
	badDecisionResp, badDecisionBody := doRawRequest(t, api.Handler, badDecisionReq)
	if badDecisionResp.StatusCode != http.StatusConflict {
		t.Fatalf("decision with a missing id: status = %d, want 409; body: %s", badDecisionResp.StatusCode, badDecisionBody)
	}
	assertMatchesSchema(t, c, "Problem", badDecisionBody)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.weatherdelay",
		`{"triggers":{"nws":{"enabled":true,"latitude":39,"longitude":-77,"contact":"ops@example.com"}}}`, auth)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.weatherdelay with triggers.nws: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "WeatherDelayConfigResponse", putBody)
}
