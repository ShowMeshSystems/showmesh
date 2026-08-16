package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track D seam D-3/B's own OpenAPI conformance suite,
// mirroring openapi_fppcommand_test.go's own split between request-side
// schema-only coverage (the exact bytes this file is about to POST,
// validated against ResolumeActionRequest's discriminated oneOf BEFORE
// being sent) and response-side coverage (a REAL response from a REAL
// [API], driven against the fake [ResolumeActionDispatcher]
// resolumeaction_test.go already builds, validated against the schema
// that endpoint actually returns).

func TestOpenAPIResolumeActionListResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resolume/actions", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ResolumeActionsResponse", body)
}

// TestOpenAPIResolumeActionLaunchClipWithIDIsRefusedNamingExpectedParams is
// acceptance criterion 2: ADR-037 retired the raw "id" reference entirely,
// so a launchClip request that still sends it is refused as an
// unrecognized parameter — never accepted, never silently ignored — and
// the refusal names what this action actually expects.
func TestOpenAPIResolumeActionLaunchClipWithIDIsRefusedNamingExpectedParams(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("launchClip", "conf-key-still-id", `{"id":"clip-1"}`), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)

	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	for _, want := range []string{"id", "clip", "deck", "layer", "persistent"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to name %q", detail, want)
		}
	}
}

// TestOpenAPIResolumeActionRequestAndResponseVariantsMatchSchemas is the
// request+response half proving every one of ResolumeActionRequest's seven
// oneOf variants (ADR-037's named-reference shape per action — launchClip,
// clearLayer, launchColumn, selectDeck, blackout's zero-parameter shape,
// and the two set-layer shapes) both validate as a REQUEST and, once
// dispatched against a real [API], produce a response validating against
// ResolumeActionResponse.
func TestOpenAPIResolumeActionRequestAndResponseVariantsMatchSchemas(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	for _, name := range []string{"launchClip", "clearLayer", "launchColumn", "selectDeck", "blackout", "setLayerBypass", "setLayerMaster"} {
		setup.dispatcher.results[name] = confirmedResult("test evidence for " + name)
	}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	tests := []struct {
		name string
		body string
	}{
		{"launchClip", resolumeActionBody("launchClip", "conf-key-launchClip", `{"clip":"clip-1","deck":"deck-1"}`)},
		{"clearLayer", resolumeActionBody("clearLayer", "conf-key-clearLayer", `{"layer":"layer-1"}`)},
		{"launchColumn", resolumeActionBody("launchColumn", "conf-key-launchColumn", `{"column":"col-1","deck":"deck-1"}`)},
		{"selectDeck", resolumeActionBody("selectDeck", "conf-key-selectDeck", `{"deck":"deck-1"}`)},
		{"blackout", resolumeActionBody("blackout", "conf-key-blackout", "")},
		{"setLayerBypass", resolumeActionBody("setLayerBypass", "conf-key-setLayerBypass", `{"layer":"layer-1","bypassed":true}`)},
		{"setLayerMaster", resolumeActionBody("setLayerMaster", "conf-key-setLayerMaster", `{"layer":"layer-1","master":0.4}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Request-side: the exact bytes about to be sent must match
			// exactly one ResolumeActionRequest oneOf branch.
			assertMatchesSchema(t, c, "ResolumeActionRequest", []byte(tt.body))

			req := newResolumeActionRequest(t, tt.body, token)
			resp, respBody := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, respBody)
			}
			// Response-side: a REAL response from a real [API].
			assertMatchesSchema(t, c, "ResolumeActionResponse", respBody)
		})
	}
}

// TestOpenAPIResolumeActionOutcomeVocabularyResponsesMatchSchema proves
// every one of the five outcome words — including "unconfirmable",
// "refused", and "failed", none of which any FPP primitive ever produces —
// still validates against ResolumeActionResponse on a real 200 response.
func TestOpenAPIResolumeActionOutcomeVocabularyResponsesMatchSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	outcomes := []ResolumeActionOutcome{
		ResolumeOutcomeConfirmed, ResolumeOutcomeUnconfirmed, ResolumeOutcomeUnconfirmable,
		ResolumeOutcomeRefused, ResolumeOutcomeFailed,
	}
	for _, outcome := range outcomes {
		t.Run(string(outcome), func(t *testing.T) {
			setup := newResolumeActionTestSetup(t, fixedClock(testNow))
			setup.dispatcher.results["launchColumn"] = ResolumeActionResult{
				Outcome: outcome, Reason: "test reason", Dispatched: outcome != ResolumeOutcomeRefused,
			}
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
			token := mustIssueToken(t, setup.svc, operator.ID)

			req := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "conf-key-"+string(outcome), `{"column":"col-1","deck":"deck-1"}`), token)
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			assertMatchesSchema(t, c, "ResolumeActionResponse", body)
		})
	}
}

// TestOpenAPIResolumeActionAuditUnavailableResponseMatchesRealResponse
// proves the 503 fail-closed refusal (launchClip, not exempt) validates
// against the shared Problem schema and carries the new
// resolume-action-refused-audit-unavailable type this task added to the
// document's Problem.type enum.
func TestOpenAPIResolumeActionAuditUnavailableResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchClip"] = confirmedResult("clip connected")
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	installFailAuditTrigger(t, setup.storeDir)

	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newResolumeActionRequest(t, resolumeActionBody("launchClip", "conf-key-audit-unavailable", `{"clip":"clip-1","deck":"deck-1"}`), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "Problem", body)

	m := decodeMap(t, body)
	if m["type"] != ProblemTypeResolumeActionRefusedAuditUnavailable {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResolumeActionRefusedAuditUnavailable)
	}
}

// TestOpenAPIResolumeActionAuditEntryOutcomeStateMatchesSchema is Review
// fix 3's own schema-conformance proof: a real GET /audit response
// containing a Resolume action's outcome entry validates against
// AuditEntry.outcomeState's newly-added enum (pkg/observation's six
// states plus "") — before this fix, outcomeState carried this endpoint's
// own outcome word instead ("confirmed", "refused", ...), which is not a
// member of that vocabulary and would fail this exact assertion once the
// enum existed at all.
func TestOpenAPIResolumeActionAuditEntryOutcomeStateMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["blackout"] = confirmedResult("every tracked layer's active_clip reported absent")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, setup.svc, admin.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-audit-schema", ""), adminToken)
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("dispatch status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit", nil)
	auditReq.Header.Set("Authorization", "Bearer "+adminToken)
	resp, body := doRawRequest(t, api.Handler, auditReq)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /audit status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AuditResponse", body)
}

// TestOpenAPIResolumeActionInFlightReplayOutcomeMatchesSchema is Review
// fix 1's own schema-conformance proof, reproducing exactly the scenario
// filed against this endpoint: force Dispatch to fail (the handler answers
// 500 and, by construction, writes nothing further to the row), then
// replay the SAME idempotency key. Before this fix, the resulting
// outcome="" response failed validation against ResolumeActionResult's own
// enum, which had no "" member — the endpoint's own contract rejected a
// value its own handler legitimately produces.
func TestOpenAPIResolumeActionInFlightReplayOutcomeMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.err = errors.New("simulated internal dispatcher failure")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := resolumeActionBody("blackout", "key-schema-dead-dispatch", "")
	req1 := newResolumeActionRequest(t, body, token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first request status = %d, want 500; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m := decodeMap(t, body2)
	result, _ := m["result"].(map[string]any)
	if result["outcome"] != "" {
		t.Fatalf("outcome = %v, want \"\" (fixture setup is wrong if this fails)", result["outcome"])
	}
	assertMatchesSchema(t, c, "ResolumeActionResponse", body2)
}

// TestOpenAPIResolumeActionReplayConflictResponseMatchesSchema proves the
// 409 idempotency-conflict problem validates against the shared Problem
// schema.
func TestOpenAPIResolumeActionReplayConflictResponseMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchColumn"] = confirmedResult("column connected")
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req1 := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "conf-key-conflict", `{"column":"col-1","deck":"deck-1"}`), token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, resolumeActionBody("selectDeck", "conf-key-conflict", `{"deck":"deck-1"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	assertMatchesSchema(t, c, "Problem", body2)
}

// TestOpenAPIResolumeActionRequestBodyTooLargeResponseMatchesSchema proves
// the 413 size refusal (Review fix 5, 2026-08-15) validates against the
// shared Problem schema and carries the SAME "payload-too-large" type
// POST /config/resolume/composition already uses (this file's own
// resolumeActionRequestBodyTooLargeProblem doc comment explains why one
// generic type serves both).
func TestOpenAPIResolumeActionRequestBodyTooLargeResponseMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	padding := strings.Repeat("x", maxResolumeActionRequestBodyBytes+256)
	body := `{"action":"blackout","idempotencyKey":"key-schema-too-large","padding":"` + padding + `"}`
	req := newResolumeActionRequest(t, body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "Problem", respBody)

	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResolumeCompositionTooLarge)
	}
}
