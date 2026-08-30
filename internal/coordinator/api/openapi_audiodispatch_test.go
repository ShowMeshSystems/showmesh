package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the audio session dispatch endpoints' own OpenAPI
// conformance coverage, following openapi_renderdispatch_test.go's split
// exactly: a compile-only pass over every schema this seam added, and a
// real-handler pass driving a REAL [API] built from audiodispatch_test.go's
// own real store.Store/identity.Service fixtures (with a fake in place of
// the MQTT broker — the one genuinely external dependency, exactly as
// audiodispatch_test.go's own tests already do) — never a hand-built JSON
// fixture standing in for a real response.

// TestOpenAPIAudioSessionDispatchSchemasCompile proves every one of the
// thirteen audio session dispatch endpoints' own per-operation request
// schemas, plus AudioSessionCommandResponse and AudioSessionCommandResult,
// are all well-formed and reachable from api/openapi.yaml's components,
// independent of any one test happening to produce a matching response.
func TestOpenAPIAudioSessionDispatchSchemasCompile(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"AudioSessionApplyRequest", "AudioSessionSeekRequest", "AudioGainSetRequest",
		"AudioGainFadeRequest", "AudioSessionNoParamsRequest",
		"AudioSessionApplyParams", "AudioSessionSeekParams", "AudioSessionGainParams",
		"AudioSessionGainFadeParams",
		"AudioSessionCommandResponse", "AudioSessionCommandResult",
	} {
		if _, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name); err != nil {
			t.Errorf("compiling schema %s: %v", name, err)
		}
	}
}

// TestOpenAPIAudioSessionRequestSchemasBindToTheirOwnOperation proves each
// of the thirteen audio session dispatch endpoints' requestBody actually
// points at the per-operation schema this file's own review turned it
// into, never the old, shared AudioSessionCommandRequest whose params
// was a union any operation could satisfy. This is what makes "the
// operation binds its params" true of the DOCUMENT, not just asserted in
// prose: a seek body validating against the gain endpoint's own schema
// would fail this test, not merely look wrong on inspection.
func TestOpenAPIAudioSessionRequestSchemasBindToTheirOwnOperation(t *testing.T) {
	cases := []struct {
		path   string
		method string
		schema string
	}{
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/apply", "post", "AudioSessionApplyRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/prepare", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/start", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/pause", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/resume", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/seek", "post", "AudioSessionSeekRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/advance", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/stop", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/clear", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/gain", "post", "AudioGainSetRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/gain/fade", "post", "AudioGainFadeRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/output/mute", "post", "AudioSessionNoParamsRequest"},
		{"/nodes/{nodeId}/audio/sessions/{sessionId}/output/unmute", "post", "AudioSessionNoParamsRequest"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := requestBodySchemaRef(t, tc.method, tc.path); got != tc.schema {
				t.Errorf("requestBody schema = %q, want %q", got, tc.schema)
			}
		})
	}
}

// TestOpenAPIAudioSessionNoParamsRequestAcceptsEmptyAndOmittedParams proves,
// against the compiled schema rather than asserted in prose, that stop and
// clear (and every other zero-param operation, all sharing
// AudioSessionNoParamsRequest) accept BOTH an explicit empty params object
// and an omitted one - the same case
// dispatchAudioSessionCommand (audiodispatch.go) normalizes a nil params
// body to before dispatch. Restructuring away from the old
// AudioSessionCommandParams union (whose top-level oneOf let an empty
// object match two of its own members at once) must not have left this
// unrepresentable in its new home.
func TestOpenAPIAudioSessionNoParamsRequestAcceptsEmptyAndOmittedParams(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, path := range []string{
		"/nodes/{nodeId}/audio/sessions/{sessionId}/stop",
		"/nodes/{nodeId}/audio/sessions/{sessionId}/clear",
	} {
		schemaName := requestBodySchemaRef(t, "post", path)
		if schemaName != "AudioSessionNoParamsRequest" {
			t.Fatalf("%s: requestBody schema = %q, want AudioSessionNoParamsRequest", path, schemaName)
		}
		assertMatchesSchema(t, c, schemaName, []byte(`{"revision":1,"params":{}}`))
		assertMatchesSchema(t, c, schemaName, []byte(`{"revision":1}`))
	}
}

// TestOpenAPIAudioSessionApplyResponseMatchesRealResponse drives a real
// audio.session.apply dispatch through a real *API and validates the
// real 200 body against AudioSessionCommandResponse.
func TestOpenAPIAudioSessionApplyResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.apply",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no pipeline backend is implemented",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "no pipeline backend is implemented"},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":1,"idempotencyKey":"key-1","params":{"sourceRole":"show"}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}

// TestOpenAPIAudioSessionStopConfirmedResponseMatchesRealResponse proves
// the OTHER outcome branch (a real, non-unconfirmable outcome) also
// matches the schema — the outcome enum specifically.
func TestOpenAPIAudioSessionStopConfirmedResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.stop",
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "stopped", "reason": ""},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":1}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}

// TestOpenAPIAudioSessionConflictResponseMatchesProblemSchema proves the
// 409 conflict body (a reused idempotency key against a different
// action) matches components/schemas/Problem — the same schema every
// other 409 in this document uses.
func TestOpenAPIAudioSessionConflictResponseMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.start",
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "started", "reason": ""},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	key := `{"revision":1,"idempotencyKey":"reused-key-1"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/start", key, token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d; body: %s", resp1.StatusCode, body1)
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	assertMatchesSchema(t, c, "Problem", body2)
}
