package api

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the decibel boundary's acceptance proof: an operator sends
// dB over HTTP, and what leaves for the node is the linear amplitude
// multiplier the agent and the engine have always taken.

func gainDbTestSetup(t *testing.T) (*audioDispatchTestSetup, string) {
	t.Helper()
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "no confirmation evidence was reported",
		Evidence: &mqttproto.ResultEvidence{Value: map[string]any{"outcome": "gain", "reason": ""}},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return setup, mustIssueToken(t, setup.svc, op.ID)
}

func TestAudioGainSetConvertsDecibelsToLinearBeforeDispatch(t *testing.T) {
	setup, token := gainDbTestSetup(t)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain",
		`{"revision":1,"idempotencyKey":"k1","params":{"gainDb":-6.0206}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if _, present := setup.pub.lastParams["gainDb"]; present {
		t.Error("params.gainDb reached the node; the coordinator-to-agent wire must stay linear")
	}
	gain, ok := setup.pub.lastParams["gain"].(float64)
	if !ok {
		t.Fatalf("params.gain = %v, want the linear multiplier the agent's audio.gain.set requires", setup.pub.lastParams["gain"])
	}
	if math.Abs(gain-0.5) > 1e-4 {
		t.Errorf("params.gain = %v, want 0.5 (the linear value of -6.0206 dB)", gain)
	}
}

func TestAudioGainFadeConvertsDecibelsToLinearBeforeDispatch(t *testing.T) {
	setup, token := gainDbTestSetup(t)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain/fade",
		`{"revision":1,"idempotencyKey":"k1","params":{"targetGainDb":-60,"durationMs":500,"curve":"linear"}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if _, present := setup.pub.lastParams["targetGainDb"]; present {
		t.Error("params.targetGainDb reached the node; the coordinator-to-agent wire must stay linear")
	}
	target, ok := setup.pub.lastParams["targetGain"].(float64)
	if !ok {
		t.Fatalf("params.targetGain = %v, want the linear multiplier the agent's audio.gain.fade requires", setup.pub.lastParams["targetGain"])
	}
	if target != 0 {
		t.Errorf("params.targetGain = %v, want exactly 0: the silence floor means silence", target)
	}
	// Everything the node also needs is passed through untouched.
	if setup.pub.lastParams["durationMs"] != float64(500) || setup.pub.lastParams["curve"] != "linear" {
		t.Errorf("the fade's other params were altered: %v", setup.pub.lastParams)
	}
}

// The pre-decibel names are refused rather than accepted, because the two
// units share a number range: a client still sending {"gain": 0.5} means
// a halving and would otherwise dispatch as a half-decibel lift.
func TestAudioGainRefusesPreDecibelParamNames(t *testing.T) {
	for _, tc := range []struct{ path, body, wants string }{
		{"gain", `{"revision":1,"params":{"gain":0.5}}`, "gainDb"},
		{"gain/fade", `{"revision":1,"params":{"targetGain":0.5,"durationMs":500}}`, "targetGainDb"},
	} {
		setup, token := gainDbTestSetup(t)
		api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

		req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/"+tc.path, tc.body, token)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400; body: %s", tc.path, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), tc.wants) {
			t.Errorf("%s refusal must name %s, got: %s", tc.path, tc.wants, body)
		}
		if setup.pub.count() != 0 {
			t.Errorf("%s was dispatched despite being refused", tc.path)
		}
	}
}

// A missing decibel parameter and a decibel value past the typo guard are
// both refused at the boundary, so a node never sees an intent this
// coordinator could not read.
func TestAudioGainRefusesMissingAndOutOfRangeDecibels(t *testing.T) {
	for _, tc := range []struct{ name, body, wants string }{
		{"missing", `{"revision":1,"params":{}}`, "required"},
		{"too loud", `{"revision":1,"params":{"gainDb":40}}`, "12"},
		{"not a number", `{"revision":1,"params":{"gainDb":"loud"}}`, "number"},
	} {
		setup, token := gainDbTestSetup(t)
		api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

		req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain", tc.body, token)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; body: %s", tc.name, resp.StatusCode, body)
		}
		var problem struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatalf("%s: decode problem: %v", tc.name, err)
		}
		if !strings.Contains(problem.Detail, tc.wants) {
			t.Errorf("%s: detail = %q, want it to mention %q", tc.name, problem.Detail, tc.wants)
		}
	}
}

// Every other audio session command is untouched by the boundary: only
// the two gain commands carry a decibel parameter.
func TestAudioNonGainCommandsPassParamsThroughUnchanged(t *testing.T) {
	params := map[string]any{"positionMs": float64(1500)}
	if problem := convertAudioGainParamsToLinear("audio.session.seek", params); problem != nil {
		t.Fatalf("seek was refused by the decibel boundary: %+v", problem)
	}
	if params["positionMs"] != float64(1500) || len(params) != 1 {
		t.Errorf("seek params were altered: %v", params)
	}
}

// The silence floor the boundary uses is the one pkg/audio defines, not a
// second copy that could drift from it.
func TestAudioGainBoundaryUsesTheSharedSilenceFloor(t *testing.T) {
	params := map[string]any{"gainDb": pkgaudio.SilenceFloorDb}
	if problem := convertAudioGainParamsToLinear("audio.gain.set", params); problem != nil {
		t.Fatalf("the silence floor was refused: %+v", problem)
	}
	if params["gain"] != float64(0) {
		t.Errorf("params.gain = %v, want exactly 0", params["gain"])
	}
}

// An authored show.action gain target carries decibels and is converted
// on the way to the node, exactly once.
func TestAuthoredAudioGainParamsConvertDecibels(t *testing.T) {
	params := map[string]any{"gainDb": -6.0206}
	ConvertAuthoredAudioGainParams("audio.gain.set", params)
	if _, present := params["gainDb"]; present {
		t.Error("gainDb survived conversion; the node must receive linear")
	}
	gain, ok := params["gain"].(float64)
	if !ok || math.Abs(gain-0.5) > 1e-4 {
		t.Errorf("gain = %v, want 0.5", params["gain"])
	}

	fade := map[string]any{"targetGainDb": pkgaudio.SilenceFloorDb, "durationMs": float64(500)}
	ConvertAuthoredAudioGainParams("audio.gain.fade", fade)
	if fade["targetGain"] != float64(0) || fade["durationMs"] != float64(500) {
		t.Errorf("fade params = %v, want targetGain 0 and durationMs carried through", fade)
	}
}

// The night background-audio controller builds its own gain targets from
// resting.backgroundAudio.maxGainDb and has already converted them. Those
// carry no decibel key, and this path must leave them exactly alone
// rather than refuse them or convert a second time. A regression here
// silences the resting bed, so it is asserted rather than assumed.
func TestAuthoredAudioGainParamsLeaveAlreadyLinearTargetsAlone(t *testing.T) {
	params := map[string]any{"gain": 0.6}
	ConvertAuthoredAudioGainParams("audio.gain.set", params)
	if params["gain"] != 0.6 || len(params) != 1 {
		t.Errorf("params = %v, want the already-linear gain untouched", params)
	}
}

// Nothing else is a gain action, so nothing else is touched.
func TestAuthoredAudioGainParamsIgnoreOtherActions(t *testing.T) {
	params := map[string]any{"gainDb": -6.0}
	ConvertAuthoredAudioGainParams("audio.session.apply", params)
	if params["gainDb"] != -6.0 || len(params) != 1 {
		t.Errorf("params = %v, want a non-gain action's params untouched", params)
	}
}

// audio.session.apply's own ceiling field is OPTIONAL, unlike gainDb/
// targetGainDb: an apply that omits ceilingDb must dispatch exactly as it
// always has, so a deployed older agent that has never heard of a
// ceiling sees nothing new.
func TestAudioApplyCeilingConvertsDecibelsToLinearBeforeDispatch(t *testing.T) {
	setup, token := gainDbTestSetup(t)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":1,"idempotencyKey":"k1","params":{"ceilingDb":-6.0206}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	if _, present := setup.pub.lastParams["ceilingDb"]; present {
		t.Error("params.ceilingDb reached the node; the coordinator-to-agent wire must stay linear")
	}
	ceiling, ok := setup.pub.lastParams["ceiling"].(float64)
	if !ok {
		t.Fatalf("params.ceiling = %v, want the linear multiplier the agent's audio.session.apply accepts", setup.pub.lastParams["ceiling"])
	}
	if math.Abs(ceiling-0.5) > 1e-4 {
		t.Errorf("params.ceiling = %v, want 0.5 (the linear value of -6.0206 dB)", ceiling)
	}
}

// Omitting ceilingDb entirely must dispatch exactly as apply always has:
// no ceiling key at all reaches the node, and the request is not refused
// for lacking one — unlike gainDb, which audio.gain.set requires.
func TestAudioApplyWithNoCeilingDispatchesUnchanged(t *testing.T) {
	setup, token := gainDbTestSetup(t)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":1,"idempotencyKey":"k1","params":{"sourceRole":"background"}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if _, present := setup.pub.lastParams["ceiling"]; present {
		t.Errorf("params.ceiling = %v present on an apply that never mentioned ceilingDb, want absent", setup.pub.lastParams["ceiling"])
	}
	if _, present := setup.pub.lastParams["ceilingDb"]; present {
		t.Errorf("params.ceilingDb = %v leaked through to the node, want removed or absent", setup.pub.lastParams["ceilingDb"])
	}
}

// The pre-change linear name is refused for apply's own ceiling exactly
// as it is for gain, so a caller cannot silently send a linear multiplier
// where a decibel value is expected.
func TestAudioApplyCeilingRefusesPreDecibelParamName(t *testing.T) {
	setup, token := gainDbTestSetup(t)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":1,"params":{"ceiling":0.5}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "ceilingDb") {
		t.Errorf("refusal must name ceilingDb, got: %s", body)
	}
	if setup.pub.count() != 0 {
		t.Error("apply was dispatched despite being refused")
	}
}

// ceilingDb shares gainDb's own +12 dB typo guard.
func TestAudioApplyCeilingRefusesOutOfRangeDecibelsAndNonNumbers(t *testing.T) {
	for _, tc := range []struct{ name, body, wants string }{
		{"too loud", `{"revision":1,"params":{"ceilingDb":40}}`, "12"},
		{"not a number", `{"revision":1,"params":{"ceilingDb":"loud"}}`, "number"},
	} {
		setup, token := gainDbTestSetup(t)
		api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

		req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply", tc.body, token)
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400; body: %s", tc.name, resp.StatusCode, body)
		}
		var problem struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(body, &problem); err != nil {
			t.Fatalf("%s: decode problem: %v", tc.name, err)
		}
		if !strings.Contains(problem.Detail, tc.wants) {
			t.Errorf("%s: detail = %q, want it to mention %q", tc.name, problem.Detail, tc.wants)
		}
	}
}
