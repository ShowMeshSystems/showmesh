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

// TestOpenAPIAudioSessionDispatchSchemasCompile proves
// AudioSessionCommandRequest, AudioSessionCommandResponse, and
// AudioSessionCommandResult are all well-formed and reachable from
// api/openapi.yaml's components, independent of any one test happening
// to produce a matching response.
func TestOpenAPIAudioSessionDispatchSchemasCompile(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"AudioSessionCommandRequest", "AudioSessionCommandResponse", "AudioSessionCommandResult",
	} {
		if _, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name); err != nil {
			t.Errorf("compiling schema %s: %v", name, err)
		}
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
