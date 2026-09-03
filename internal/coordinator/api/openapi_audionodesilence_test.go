package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is audio.node.silence's own OpenAPI conformance coverage,
// following openapi_audiodispatch_test.go's split one file over: a
// compile-only pass over the schemas this endpoint added, plus a
// real-handler pass driving a REAL [API] against api/openapi.yaml's
// response schema.

// TestOpenAPIAudioNodeSilenceSchemasCompile proves the four schemas this
// endpoint added are well-formed and reachable from api/openapi.yaml's
// components.
func TestOpenAPIAudioNodeSilenceSchemasCompile(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"AudioNodeSilenceRequest", "AudioNodeSilenceResponse",
		"AudioNodeSilenceResult", "AudioNodeSilenceSessionResult",
	} {
		if _, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name); err != nil {
			t.Errorf("compiling schema %s: %v", name, err)
		}
	}
}

// TestOpenAPIAudioNodeSilenceRequestSchemaBindsToOperation proves
// POST /nodes/{nodeId}/audio/silence's requestBody points at
// AudioNodeSilenceRequest, never AudioSessionNoParamsRequest (which
// requires a revision this operation does not take).
func TestOpenAPIAudioNodeSilenceRequestSchemaBindsToOperation(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/nodes/{nodeId}/audio/silence"); got != "AudioNodeSilenceRequest" {
		t.Errorf("requestBody schema = %q, want AudioNodeSilenceRequest", got)
	}
}

// TestOpenAPIAudioNodeSilenceConfirmedResponseMatchesRealResponse drives a
// real, confirmed audio.node.silence dispatch through a real *API and
// validates the real 200 body against AudioNodeSilenceResponse.
func TestOpenAPIAudioNodeSilenceConfirmedResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, []map[string]any{
		{"sessionId": "cue", "outcome": "stopped", "reason": ""},
	})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioNodeSilenceResponse", body)
}

// TestOpenAPIAudioNodeSilenceRefusedResponseMatchesRealResponse proves the
// OTHER outcome branch (an old-agent refusal, carrying no evidence) also
// matches the schema.
func TestOpenAPIAudioNodeSilenceRefusedResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeRefused,
		Reason:  `operation "audio.node.silence" is not on the agent's allowlist`,
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioNodeSilenceResponse", body)
}

// TestOpenAPIAudioNodeSilenceConflictResponseMatchesProblemSchema proves
// the 409 conflict body (a reused idempotency key against a different
// node) matches components/schemas/Problem.
func TestOpenAPIAudioNodeSilenceConflictResponseMatchesProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, nil)
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	key := `{"idempotencyKey":"reused-silence-openapi-key"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", key, token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d; body: %s", resp1.StatusCode, body1)
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-b/audio/silence", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	assertMatchesSchema(t, c, "Problem", body2)
}
