package api

import (
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the four remaining reserved audio.gain.*/audio.output.*
// dispatch endpoints' own OpenAPI conformance coverage, following
// openapi_audiodispatch_test.go's split: each drives a REAL [API] and
// validates the real response against AudioSessionCommandResponse,
// never a hand-built JSON fixture standing in for one.

func TestOpenAPIAudioGainSetResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.gain.set",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no pipeline backend is implemented",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "no pipeline backend is implemented"},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain",
		`{"revision":1,"idempotencyKey":"key-gain-1","params":{"gain":0.5}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}

func TestOpenAPIAudioGainFadeResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.gain.fade",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no pipeline backend is implemented",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "no pipeline backend is implemented"},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain/fade",
		`{"revision":1,"idempotencyKey":"key-fade-1","params":{"targetGain":0,"durationMs":500,"curve":"linear"}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}

func TestOpenAPIAudioOutputMuteResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.output.mute",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no pipeline backend is implemented",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "no pipeline backend is implemented"},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/output/mute",
		`{"revision":1,"idempotencyKey":"key-mute-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}

func TestOpenAPIAudioOutputUnmuteResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.output.unmute",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no pipeline backend is implemented",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "no pipeline backend is implemented"},
		},
	}
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/output/unmute",
		`{"revision":1,"idempotencyKey":"key-unmute-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "AudioSessionCommandResponse", body)
}
