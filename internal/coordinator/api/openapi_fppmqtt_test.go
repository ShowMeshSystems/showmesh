package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track G seam G-3's own OpenAPI conformance suite, mirroring
// openapi_test.go's TestOpenAPIConfigResponsesMatchRealResponses for
// fpp.endpoints and openapi_resolumerecovery_test.go's identical pattern
// for resolume.recovery: drive a REAL [API] and validate its actual
// response body against api/openapi.yaml's own schema for that endpoint.

// TestOpenAPIFPPMQTTConfigResponsesMatchRealResponses covers PUT
// /config/fpp.mqtt's success body, GET /config/fpp.mqtt's body (including
// a stored password reflected only as passwordSet:true, never the value),
// and GET /config/fpp.mqtt/revisions' body.
func TestOpenAPIFPPMQTTConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	putBody := `{"brokerURL":"tcp://10.0.1.5:1883","username":"showmesh","topicPrefix":"falcon/player","hosts":{"player-01":"FPP-Player"},"password":"s3cret"}`
	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/fpp.mqtt", putBody, authHeader)
	putResp, putRespBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putRespBody)
	}
	assertMatchesSchema(t, c, "FPPMQTTConfigResponse", putRespBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.mqtt", authHeader)
	assertMatchesSchema(t, c, "FPPMQTTConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.mqtt/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIFPPMQTTConfigNotConfiguredResponseMatchesRealResponse covers
// the "nothing configured yet" 404 shape — a state with no revision at
// all, distinct from a revision whose payload happens to be empty.
func TestOpenAPIFPPMQTTConfigNotConfiguredResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	deps, _ := fppMQTTConfigTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.mqtt", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)
}
