package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track F seam F2's own conformance coverage, following
// openapi_nightsession_test.go's exact pattern one file over: every
// schema this seam added is validated against a REAL response from a
// real coordinator wiring, never hand-built JSON.

func TestOpenAPINightLifecycleDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"NightReadinessCheck", "NightReadiness", "NightPhaseEvidence",
		"NightSessionState", "NightSessionResponse",
		"NightCommandRequest", "NightCommandResult", "NightCommandResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPINightLifecycleResponsesMatchRealResponses drives the full
// seven-command vocabulary against a real coordinator wiring and validates
// every response — including all three 409 refusal classes — against the
// schemas this seam added.
func TestOpenAPINightLifecycleResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, token, _, obs := setupNightControlFixture(t, time.Hour)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// GET with no session ever created.
	_, noneBody := doRequest(t, api.Handler, "GET", "/api/v1/night/session", nil)
	assertMatchesSchema(t, c, "NightSessionResponse", noneBody)

	setHealthyFPPReachable(obs, testNow)

	prepReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/prepare-site", `{}`, auth)
	prepResp, prepBody := doRawRequest(t, api.Handler, prepReq)
	if prepResp.StatusCode != http.StatusAccepted {
		t.Fatalf("prepare-site: status = %d, want 202; body: %s", prepResp.StatusCode, prepBody)
	}
	assertMatchesSchema(t, c, "NightCommandResponse", prepBody)

	readyReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/run-readiness", `{}`, auth)
	_, readyBody := doRawRequest(t, api.Handler, readyReq)
	assertMatchesSchema(t, c, "NightCommandResponse", readyBody)

	preshowReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-preshow", `{}`, auth)
	_, preshowBody := doRawRequest(t, api.Handler, preshowReq)
	assertMatchesSchema(t, c, "NightCommandResponse", preshowBody)

	_, curBody := doRequest(t, api.Handler, "GET", "/api/v1/night/session", nil)
	assertMatchesSchema(t, c, "NightSessionResponse", curBody)

	startReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", `{}`, auth)
	_, startBody := doRawRequest(t, api.Handler, startReq)
	assertMatchesSchema(t, c, "NightCommandResponse", startBody)

	finalReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/request-final-show", `{}`, auth)
	_, finalBody := doRawRequest(t, api.Handler, finalReq)
	assertMatchesSchema(t, c, "NightCommandResponse", finalBody)

	fadeReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/fade-out-night", `{}`, auth)
	_, fadeBody := doRawRequest(t, api.Handler, fadeReq)
	assertMatchesSchema(t, c, "NightCommandResponse", fadeBody)

	powerReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/power-down-presentation", `{}`, auth)
	_, powerBody := doRawRequest(t, api.Handler, powerReq)
	assertMatchesSchema(t, c, "NightCommandResponse", powerBody)

	byIDReq, byIDBody := doRequest(t, api.Handler, "GET", "/api/v1/night/sessions/"+mustDecodeSessionID(t, curBody), nil)
	if byIDReq.StatusCode != http.StatusOK {
		t.Fatalf("GET night/sessions/{id}: status = %d, want 200; body: %s", byIDReq.StatusCode, byIDBody)
	}
	assertMatchesSchema(t, c, "NightSessionResponse", byIDBody)

	notFoundReq, notFoundBody := doRequest(t, api.Handler, "GET", "/api/v1/night/sessions/no-such-session", nil)
	if notFoundReq.StatusCode != http.StatusNotFound {
		t.Fatalf("GET night/sessions/{id} (unknown): status = %d, want 404; body: %s", notFoundReq.StatusCode, notFoundBody)
	}
	assertMatchesSchema(t, c, "Problem", notFoundBody)

	// The three 409 refusal classes.
	notReadyReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/run-readiness", `{}`, auth)
	notReadyResp, notReadyBody := doRawRequest(t, api.Handler, notReadyReq)
	if notReadyResp.StatusCode != http.StatusConflict {
		t.Fatalf("run-readiness after stopped: status = %d, want 409; body: %s", notReadyResp.StatusCode, notReadyBody)
	}
	assertMatchesSchema(t, c, "Problem", notReadyBody)

	stateRejectedReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", `{}`, auth)
	stateRejectedResp, stateRejectedBody := doRawRequest(t, api.Handler, stateRejectedReq)
	if stateRejectedResp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night after stopped: status = %d, want 409; body: %s", stateRejectedResp.StatusCode, stateRejectedBody)
	}
	assertMatchesSchema(t, c, "Problem", stateRejectedBody)

	unsupportedReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/not-a-command", `{}`, auth)
	unsupportedResp, unsupportedBody := doRawRequest(t, api.Handler, unsupportedReq)
	if unsupportedResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported command: status = %d, want 400; body: %s", unsupportedResp.StatusCode, unsupportedBody)
	}
	assertMatchesSchema(t, c, "Problem", unsupportedBody)

	endSessionReq := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/end-session", `{}`, auth)
	endSessionResp, endSessionBody := doRawRequest(t, api.Handler, endSessionReq)
	if endSessionResp.StatusCode != http.StatusAccepted {
		t.Fatalf("end-session: status = %d, want 202; body: %s", endSessionResp.StatusCode, endSessionBody)
	}
	assertMatchesSchema(t, c, "NightCommandResponse", endSessionBody)
}

// TestOpenAPINightAuditUnavailableResponseMatchesRealResponse proves the
// 503 fail-closed response (finding 9) against a real fail-audit trigger,
// following openapi_fppcommand_test.go's own precedent for this shape.
func TestOpenAPINightAuditUnavailableResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	svc, st, storeDir := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	opToken := mustIssueToken(t, svc, operator.ID)

	deps, _ := nightControlTestDeps(svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), NightReadinessMaxAge: time.Hour})

	mustPutShowAction(t, api, adminToken, "lighting-fade-out", validShowActionFPPBody)
	mustCreateNightSessionAsset(t, st, "halloween-2026", "resting-loop", "player-01")
	mustPutNightSession(t, api, adminToken, "halloween-main", validNightSessionBody)
	mustActivateNightSession(t, api, adminToken, "halloween-main")
	installFailAuditTrigger(t, storeDir)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/prepare-site", `{}`, map[string]string{"Authorization": "Bearer " + opToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("prepare-site with a failing audit write: status = %d, want 503; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)
}

// mustDecodeSessionID pulls "session":{"id":"..."} out of a
// NightSessionResponse body without importing v1 (this test package
// already does; kept local and minimal rather than adding a shared helper
// for one call site).
func mustDecodeSessionID(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode session id: %v", err)
	}
	if resp.Session.ID == "" {
		t.Fatalf("session id is empty in body: %s", body)
	}
	return resp.Session.ID
}
