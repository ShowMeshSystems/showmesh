package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 9 wave 2's own test suite for the four routes per kind
// (STEP-9-SPEC.md section 5.5). It follows config_test.go's existing
// pattern: a real *store.Store and a real identity.Service, driven through
// the real route table — never a hand-built v1 struct asserted against
// itself.

// showConfigTestDeps mirrors configTestDeps, additionally wiring an FPP
// lister with one known instance (for show.action instanceId validation)
// and one declared integration broker (for show.action broker
// validation), and a fakeMacroRunner (unused by this file's own tests,
// but Dependencies.Macros must be non-nil-safe for any route this harness
// might exercise indirectly).
func showConfigTestDeps(svc identity.Service, st *store.Store) Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		FPP: &fakeFPPLister{views: []FPPInstanceView{{InstanceID: "player-01", Endpoint: "http://10.0.1.20"}}},
		IntegrationBrokers: []config.IntegrationBroker{
			{ID: "home-automation", URL: "tcp://10.0.0.5:1883"},
		},
		Identity: svc, Config: st, Macros: &fakeMacroRunner{}, Commands: st,
	}
}

const validShowActionFPPBody = `{
	"show": "halloween-2026",
	"label": "Start the main show",
	"safetyClass": "none",
	"target": {
		"integration": "fpp",
		"instanceId": "player-01",
		"primitive": "startPlaylist",
		"params": {"playlist": "Halloween Main", "ifBusy": "refuse"}
	}
}`

func validShowMacroBody(actionID string) string {
	return `{
		"show": "halloween-2026",
		"label": "Begin set",
		"steps": [
			{
				"id": "start",
				"action": "` + actionID + `",
				"localFallback": {"class": "coordinator-required", "reason": "the coordinator dispatches this step"}
			}
		]
	}`
}

// TestShowConfigReadRequiresOperatorOrShowMacroRunOrConfigWrite is
// acceptance criterion 21's own route-layer half: STEP-9-SPEC.md section
// 5.5's corrected posture, "reading show.macro and show.action requires
// show:macro:run OR config:write". Proved against a REAL operator-role
// principal (identity.RoleOperator, which holds show:macro:run and NOT
// config:write) reading the LIST route, which is exactly the route the
// specification review found renders empty/403 under the wrong posture.
//
// Broken and confirmed to fail: changed showConfigReadScopes
// (auth.go) to []identity.Scope{identity.ScopeConfigWrite} alone (copying
// fpp.endpoints' posture) and reran this test — the operator sub-test
// failed with 403, exactly the defect this test exists to catch. Restored
// afterward.
func TestShowConfigReadRequiresOperatorOrShowMacroRunOrConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})
	t.Run("operator holds show:macro:run, not config:write", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro", map[string]string{"Authorization": "Bearer " + operatorToken})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})
	t.Run("admin holds config:write", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro", map[string]string{"Authorization": "Bearer " + adminToken})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
		}
	})
}

// TestShowConfigReadIsGatedRegardlessOfCloseReads proves readAnyGuard's
// second departure from readGuard: this surface is never toggled by
// [Options.CloseReads] (matching fpp.endpoints and GET /audit), unlike the
// four pre-existing read resources. With CloseReads left at its default
// (false, "reads stay open"), an unauthenticated request to this NEW
// surface must still be refused.
func TestShowConfigReadIsGatedRegardlessOfCloseReads(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger(), CloseReads: false})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (this surface must never open under CloseReads=false); body: %s", resp.StatusCode, body)
	}
}

// TestPutShowActionRequiresConfigWrite: an operator (show:macro:run, not
// config:write) may read but not write.
func TestPutShowActionRequiresConfigWrite(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/projectors-on", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + operatorToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestPutShowActionAndGetRoundTrip proves the write/read/list/revisions
// path for show.action end to end against a real store.
func TestPutShowActionAndGetRoundTrip(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	put := decodeMap(t, putBody)
	if put["revision"] != float64(1) {
		t.Errorf("PUT revision = %v, want 1", put["revision"])
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/start-main-show",
		map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET: status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	get := decodeMap(t, getBody)
	payload := get["payload"].(map[string]any)
	if payload["safetyClass"] != "none" {
		t.Errorf("payload.safetyClass = %v, want none", payload["safetyClass"])
	}

	listResp, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action", map[string]string{"Authorization": "Bearer " + token})
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("LIST: status = %d, want 200; body: %s", listResp.StatusCode, listBody)
	}
	list := decodeMap(t, listBody)
	objs := list["objects"].([]any)
	if len(objs) != 1 {
		t.Fatalf("LIST objects = %d, want 1; body: %s", len(objs), listBody)
	}
	first := objs[0].(map[string]any)
	if first["id"] != "start-main-show" || first["show"] != "halloween-2026" {
		t.Errorf("LIST entry = %v, want id=start-main-show show=halloween-2026", first)
	}

	revResp, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/start-main-show/revisions",
		map[string]string{"Authorization": "Bearer " + token})
	if revResp.StatusCode != http.StatusOK {
		t.Fatalf("REVISIONS: status = %d, want 200; body: %s", revResp.StatusCode, revBody)
	}
	rev := decodeMap(t, revBody)
	revs := rev["revisions"].([]any)
	if len(revs) != 1 {
		t.Fatalf("REVISIONS count = %d, want 1; body: %s", len(revs), revBody)
	}
}

// TestPutShowActionGetThenPutRoundTrips is the property a UI reviewer
// found broken and named directly: a GET followed by an unchanged PUT
// must succeed. It had no test, which is why an MQTT show.action with
// expect.kind "number" shipped with a decoder that required a JSON
// number for target.expect.value while the read/stored shape (and
// api/openapi.yaml) always emit it as a JSON string — an action authored
// correctly loaded into a client, then rejected on re-save with an error
// naming a field the operator never touched.
//
// Covers every expect.kind that carries a value ("number" with a value,
// "number" with none, and "match" including an empty value — STEP-9-SPEC.md
// section 7.3 explicitly allows an empty match target) plus an FPP action
// with no MQTT fields at all, and — since the same defect class
// (STEP-9-SPEC.md section 5's absent-key-defaults rule) applies to
// show.macro's onFailure/onUnconfirmed and both kinds' description — a
// show.macro round trip too.
func TestPutShowActionGetThenPutRoundTrips(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	roundTrip := func(t *testing.T, resource, initialBody string) {
		t.Helper()
		putReq := newJSONRequest(t, http.MethodPut, resource, initialBody, map[string]string{"Authorization": "Bearer " + token})
		putResp, putBody := doRawRequest(t, api.Handler, putReq)
		if putResp.StatusCode != http.StatusOK {
			t.Fatalf("initial PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
		}

		getResp, getBody := doRequest(t, api.Handler, "GET", resource, map[string]string{"Authorization": "Bearer " + token})
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("GET: status = %d, want 200; body: %s", getResp.StatusCode, getBody)
		}
		get := decodeMap(t, getBody)
		payload, err := json.Marshal(get["payload"])
		if err != nil {
			t.Fatalf("re-marshaling the GET response's own payload: %v", err)
		}

		// The property under test: PUT the GET response's payload back
		// completely unchanged. If the read shape is not accepted by the
		// write path, this is where it fails.
		secondReq := newJSONRequest(t, http.MethodPut, resource, string(payload), map[string]string{"Authorization": "Bearer " + token})
		secondResp, secondBody := doRawRequest(t, api.Handler, secondReq)
		if secondResp.StatusCode != http.StatusOK {
			t.Fatalf("PUT of the unchanged GET body: status = %d, want 200; body: %s\nGET payload was: %s", secondResp.StatusCode, secondBody, payload)
		}
		second := decodeMap(t, secondBody)
		if second["revision"] != float64(2) {
			t.Errorf("PUT of the unchanged GET body created revision %v, want 2 (a genuinely new, identical revision)", second["revision"])
		}
	}

	t.Run("fpp-action", func(t *testing.T) {
		roundTrip(t, "/api/v1/config/show.action/rt-fpp", validShowActionFPPBody)
	})
	t.Run("fpp-action-with-numeric-param", func(t *testing.T) {
		// This is wave 2's own recorded family of defect (an int64 param
		// coming back as a float64) checked against THIS surface
		// specifically: decodeFPPParamValue already stores an int-kind
		// param as int64 (fppcommand_primitives.go), which Go's
		// encoding/json marshals as a bare integer with no decimal point,
		// so it should already round-trip. This subtest is the empirical
		// check rather than trusting that reasoning.
		body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"fpp","instanceId":"player-01","primitive":"setVolume","params":{"volume":50}}}`
		roundTrip(t, "/api/v1/config/show.action/rt-fpp-volume", body)
	})
	t.Run("mqtt-action-expect-number-with-value", func(t *testing.T) {
		body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"home-automation",
			"publish":{"topic":"home/projectors/set","payload":"ON","qos":1},
			"expect":{"kind":"number","topic":"home/projectors/state","value":"42","deadlineSeconds":10}}}`
		roundTrip(t, "/api/v1/config/show.action/rt-mqtt-number", body)
	})
	t.Run("mqtt-action-expect-number-without-value", func(t *testing.T) {
		body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"home-automation",
			"publish":{"topic":"home/projectors/set","payload":"ON","qos":1},
			"expect":{"kind":"number","topic":"home/projectors/state","deadlineSeconds":10}}}`
		roundTrip(t, "/api/v1/config/show.action/rt-mqtt-number-novalue", body)
	})
	t.Run("mqtt-action-expect-match-with-empty-value", func(t *testing.T) {
		// The other half of what the reviewer asked to be confirmed: the
		// server allows an empty "match" value (decodeRequiredStringAllowEmpty),
		// and that must round-trip too, not merely decode once.
		body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"home-automation",
			"publish":{"topic":"home/projectors/set","payload":"ON","qos":1},
			"expect":{"kind":"match","topic":"home/projectors/state","value":"","deadlineSeconds":10}}}`
		roundTrip(t, "/api/v1/config/show.action/rt-mqtt-match-empty", body)
	})
	t.Run("show-macro", func(t *testing.T) {
		putActionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/rt-macro-action", validShowActionFPPBody,
			map[string]string{"Authorization": "Bearer " + token})
		if resp, body := doRawRequest(t, api.Handler, putActionReq); resp.StatusCode != http.StatusOK {
			t.Fatalf("prerequisite show.action PUT: status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		roundTrip(t, "/api/v1/config/show.macro/rt-macro", validShowMacroBody("rt-macro-action"))
	})
}

// TestPutShowConfigUnknownTopLevelKeyRejected is the HTTP-level half of
// the defect a second review found: config.DecodeShowActionPayload and
// DecodeShowMacroPayload used to silently ignore an unrecognized
// top-level key, which for a field with a default (description,
// onFailure, onUnconfirmed) meant a typo silently stored a DIFFERENT
// policy than the one the operator typed, with no error. Proved here
// through the real route table, asserting the distinct problem type
// rather than a generic 400.
func TestPutShowConfigUnknownTopLevelKeyRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("show.action", func(t *testing.T) {
		body := `{"show":"halloween-2026","label":"x","descriptio":"oops","safetyClass":"none","target":{"integration":"fpp","instanceId":"player-01","primitive":"stopPlaylist"}}`
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/typo-test", body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
		}
		problem := decodeMap(t, respBody)
		if problem["type"] != "https://showmesh.dev/problems/show-config-field-unknown-key" {
			t.Errorf("problem.type = %v, want show-config-field-unknown-key", problem["type"])
		}
	})
	t.Run("show.macro", func(t *testing.T) {
		body := `{"show":"halloween-2026","label":"x","step":[]}`
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/typo-test", body, map[string]string{"Authorization": "Bearer " + token})
		resp, respBody := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
		}
		problem := decodeMap(t, respBody)
		if problem["type"] != "https://showmesh.dev/problems/show-config-field-unknown-key" {
			t.Errorf("problem.type = %v, want show-config-field-unknown-key", problem["type"])
		}
	})
}

// TestPutShowActionSafetyClassMismatchRejected: startPlaylist's own
// registered class is "none" (fppcommand_primitives.go); declaring
// "powerOff" for it must be refused at write time, per STEP-9-SPEC.md
// section 5.3, with the distinct Code (not a generic bad-value error).
func TestPutShowActionSafetyClassMismatchRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	body := `{"show":"halloween-2026","label":"x","safetyClass":"powerOff","target":{"integration":"fpp","instanceId":"player-01","primitive":"startPlaylist","params":{"playlist":"X","ifBusy":"refuse"}}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/mismatched", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeSafetyClassMismatch]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
}

// TestPutShowActionUnconfiguredInstanceRejected is acceptance criterion
// 20's own show.action half: an action naming an unconfigured instanceId
// is rejected at PUT.
func TestPutShowActionUnconfiguredInstanceRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"fpp","instanceId":"no-such-host","primitive":"startPlaylist","params":{"playlist":"X","ifBusy":"refuse"}}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/bad-instance", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowActionUndeclaredBrokerRejected is acceptance criterion 20's
// mqtt half: an action naming an undeclared broker is rejected at PUT.
func TestPutShowActionUndeclaredBrokerRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	body := `{"show":"halloween-2026","label":"x","safetyClass":"none","target":{"integration":"mqtt","broker":"no-such-broker",
		"publish":{"topic":"home/projectors/set","payload":"ON","qos":1},
		"expect":{"kind":"none"}}}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/bad-broker", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowMacroAbsentStepsIsRejected is one of the six required
// break-the-test verifications: a PUT with no "steps" key must not decode
// to an empty slice and validate as "a macro with no steps" (wave 2
// builder brief section 1: "A PUT with no steps key must not decode to an
// empty slice and validate as a macro with no steps").
//
// Broken and confirmed to fail: temporarily changed
// config.DecodeShowMacroPayload (showmacro.go) to skip the
// !present/isJSONNull checks on "steps" and fall straight to
// json.Unmarshal on a zero-value json.RawMessage(nil), which decodes an
// absent field as a nil, zero-length slice — with that change, this test
// failed (200 instead of 400, an object created with zero steps) because
// len(rawSteps)==0 then hit the "steps must contain at least one step"
// branch by ACCIDENT rather than by the intended absent-key guard, and a
// SEPARATE run confirmed a present "steps": null with that same change
// panics instead of erroring cleanly (json.Unmarshal of the literal `null`
// into a nil slice succeeds silently, only the top-level decode of a
// missing key panics on the nil RawMessage). Restored afterward.
func TestPutShowMacroAbsentStepsIsRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	body := `{"show":"halloween-2026","label":"Begin set"}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/no-steps", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeFieldRequired]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v (field-required, not steps-empty — the key was ABSENT, not an explicit empty array)", problem["type"], wantType)
	}
}

// TestPutShowMacroNullStepsIsRejected: a present JSON null for "steps"
// must also be rejected, and distinctly from both absent and empty.
func TestPutShowMacroNullStepsIsRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	body := `{"show":"halloween-2026","label":"Begin set","steps":null}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/null-steps", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowMacroReferencingMissingActionIsRejected is acceptance
// criterion 20's macro half: a macro naming a nonexistent action is
// rejected at PUT.
func TestPutShowMacroReferencingMissingActionIsRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/dangling", validShowMacroBody("does-not-exist"),
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
}

// TestPutShowMacroReducedLocalFallbackIsRejected: "reduced" must be
// rejected with its own distinct Code (STEP-9-SPEC.md section 5.4).
func TestPutShowMacroReducedLocalFallbackIsRejected(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	putActionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putActionReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	body := `{"show":"halloween-2026","label":"x","steps":[{"id":"s","action":"start-main-show","localFallback":{"class":"reduced","reason":"x"}}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/reduced-fallback", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	wantType := showConfigValidationProblemTypes[config.ValidationCodeLocalFallbackReduced]
	if problem["type"] != wantType {
		t.Errorf("problem.type = %v, want %v", problem["type"], wantType)
	}
}

// TestPutShowMacroPersistsResolvedPolicyDefaults is wave 2A/2B's flagged
// gap 1 (section 5b item 1 of this wave's brief): PUT a macro whose steps
// name neither onFailure nor onUnconfirmed, read the stored revision back,
// and assert the PERSISTED JSON (not the decoded Go struct, which would
// hide a bug where the JSON on disk still carried empty strings) carries
// the resolved words "abort"/"continue".
func TestPutShowMacroPersistsResolvedPolicyDefaults(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	putActionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putActionReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/defaults", validShowMacroBody("start-main-show"),
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, putReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d; body: %s", resp.StatusCode, body)
	}

	// Read the STORED revision directly, bypassing this package's own GET
	// mapping code entirely — the point of this test is that the bytes on
	// disk carry the resolved defaults, not that the mapping layer happens
	// to render them (which would pass even if EncodeShowMacroPayload had
	// been given the raw, unresolved request body).
	obj, err := st.GetConfigObject(t.Context(), config.ShowMacroConfigKind, "defaults")
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	rev, err := st.GetConfigRevision(t.Context(), config.ShowMacroConfigKind, "defaults", obj.CurrentRevision)
	if err != nil {
		t.Fatalf("GetConfigRevision: %v", err)
	}
	// "continue" since 2026-08-14 (owner decision: a run always runs every
	// step). The assertion is on the RESOLVED value being present at all,
	// not on which value it is: the defect this test exists to catch is a
	// payload stored with the field absent, so a later default change
	// silently rewrites the meaning of macros already on disk.
	if !containsSubstring(rev.PayloadJSON, `"onFailure":"continue"`) {
		t.Errorf("stored payload does not carry the resolved onFailure default; payload: %s", rev.PayloadJSON)
	}
	if !containsSubstring(rev.PayloadJSON, `"onUnconfirmed":"continue"`) {
		t.Errorf("stored payload does not carry the resolved onUnconfirmed default; payload: %s", rev.PayloadJSON)
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

// TestShowConfigValidationCodesAllMapToDistinctProblemTypes is the
// completeness test the wave 2 shared contract section 1 asks for: "a test
// that every exported Code has a mapping, so a new code cannot silently
// fall through to a generic 400." It enumerates the exact set of Codes
// config.go's own const block declares (there is no way to enumerate a
// package's exported const values via reflection, so this list is
// maintained by hand alongside config's own — see this task's report for
// that limitation stated plainly) and asserts each has its OWN, distinct
// problem type URI, never falling back to [ProblemTypeInvalidParameter].
func TestShowConfigValidationCodesAllMapToDistinctProblemTypes(t *testing.T) {
	codes := []string{
		config.ValidationCodeBodyInvalid,
		config.ValidationCodeFieldRequired,
		config.ValidationCodeFieldNull,
		config.ValidationCodeFieldEmpty,
		config.ValidationCodeFieldInvalid,
		config.ValidationCodeFieldUnknownReference,
		config.ValidationCodeSafetyClassMismatch,
		config.ValidationCodeLocalFallbackReduced,
		config.ValidationCodeStepsEmpty,
		config.ValidationCodeStepsTooMany,
		config.ValidationCodeStepIDDuplicate,
		config.ValidationCodeFieldUnknownKey,
		config.ValidationCodeAudioNodeChannelDuplicate,
		config.ValidationCodeAudioNodeChannelOverlap,
		config.ValidationCodeAudioNodeRouteMismatch,
		config.ValidationCodeEntriesEmpty,
		config.ValidationCodeEntryPositionDuplicate,
		config.ValidationCodeCrossShowReference,
	}
	seen := make(map[string]string, len(codes))
	for _, code := range codes {
		problem := mapValidationError(&config.ValidationError{Code: code, Field: "x", Detail: "y"})
		if problem.Type == ProblemTypeInvalidParameter {
			t.Errorf("code %q falls back to the generic invalid-parameter type; add it to showConfigValidationProblemTypes", code)
			continue
		}
		if other, dup := seen[problem.Type]; dup {
			t.Errorf("codes %q and %q share problem type %q; each code must map to a DISTINCT type", code, other, problem.Type)
		}
		seen[problem.Type] = code
	}
}

// TestShowConfigValidationCodeUnrecognizedFallsBackToGeneric proves the
// OTHER half of mapValidationError's contract: an unrecognized Code (one
// this package's map does not list — simulating config growing a code
// this file was never updated for) degrades to a generic 400 rather than
// panicking, per that function's own doc comment.
func TestShowConfigValidationCodeUnrecognizedFallsBackToGeneric(t *testing.T) {
	problem := mapValidationError(&config.ValidationError{Code: "some-future-code", Field: "x", Detail: "y"})
	if problem.Type != ProblemTypeInvalidParameter {
		t.Errorf("problem.Type = %q, want the generic fallback %q", problem.Type, ProblemTypeInvalidParameter)
	}
	if problem.Status != http.StatusBadRequest {
		t.Errorf("problem.Status = %d, want 400", problem.Status)
	}
}

// TestListShowActionsFiltersByShow and TestListShowMacrosFiltersByShow
// prove the ?show= filter, matching TestListShowSurfacesFiltersByShow's
// shape one file over.
func TestListShowActionsFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"christmas-2026"}`)

	christmasBody := `{"show":"christmas-2026","label":"x","safetyClass":"stop","target":{"integration":"fpp","instanceId":"player-01","primitive":"stopPlaylist"}}`
	for id, body := range map[string]string{"halloween-action": validShowActionFPPBody, "christmas-action": christmasBody} {
		req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/"+id, body, map[string]string{"Authorization": "Bearer " + token})
		if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT show.action/%s: status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
		}
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsSubstring(string(filtered), `"christmas-action"`) {
		t.Fatalf("expected christmas-action in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsSubstring(string(filtered), `"halloween-action"`) {
		t.Fatalf("halloween-action leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

func TestListShowMacrosFiltersByShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutShow(t, api, token, "christmas-2026", `{"name":"christmas-2026"}`)

	actionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, actionReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}
	christmasActionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/christmas-action",
		`{"show":"christmas-2026","label":"x","safetyClass":"stop","target":{"integration":"fpp","instanceId":"player-01","primitive":"stopPlaylist"}}`,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, christmasActionReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT christmas show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	macroReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/begin-set", validShowMacroBody("start-main-show"),
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, macroReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.macro: status = %d; body: %s", resp.StatusCode, body)
	}
	christmasMacroBody := `{"show":"christmas-2026","label":"x","steps":[{"id":"s","action":"christmas-action","localFallback":{"class":"none","reason":"x"}}]}`
	christmasMacroReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/christmas-macro", christmasMacroBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, christmasMacroReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT christmas show.macro: status = %d; body: %s", resp.StatusCode, body)
	}

	_, filtered := doRequest(t, api.Handler, "GET", "/api/v1/config/show.macro?show=christmas-2026", map[string]string{"Authorization": "Bearer " + token})
	if !containsSubstring(string(filtered), `"christmas-macro"`) {
		t.Fatalf("expected christmas-macro in the christmas-2026 filtered list; body: %s", filtered)
	}
	if containsSubstring(string(filtered), `"begin-set"`) {
		t.Fatalf("begin-set leaked into a christmas-2026 filtered list; body: %s", filtered)
	}
}

// TestListShowActionsUnknownShowFilterReturnsEmptyNotRefusal proves a
// show id that once existed and was deleted, or was simply mistyped, is a
// legitimate filter value. The correct answer is an empty list, never a
// 400 — only "?node=" on these two kinds is refused.
func TestListShowActionsUnknownShowFilterReturnsEmptyNotRefusal(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action?show=never-existed", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an unknown show filter is an empty answer, not a refusal); body: %s", resp.StatusCode, body)
	}
	list := decodeMap(t, body)
	if objs := list["objects"].([]any); len(objs) != 0 {
		t.Fatalf("expected zero objects for an unknown show filter, got %d; body: %s", len(objs), body)
	}
}

// TestPutShowActionRefusesUnknownShow and TestPutShowMacroRefusesUnknownShow
// prove the zero-shows-defined case: with no shows configured, writing
// the very first action or macro must fail with a refusal naming the
// missing show, not a bare 400 and not silent acceptance.
func TestPutShowActionRefusesUnknownShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (zero shows defined; the write must refuse); body: %s", resp.StatusCode, body)
	}
	problem := decodeMap(t, body)
	if detail, _ := problem["detail"].(string); !containsSubstring(detail, "halloween-2026") {
		t.Fatalf("problem.detail must name the missing show %q, got %v", "halloween-2026", problem["detail"])
	}

	// The other half of the transition: create the show, and the identical
	// write now succeeds.
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	req2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after creating the show; body: %s", resp2.StatusCode, body2)
	}
}

func TestPutShowMacroRefusesUnknownShow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)

	actionReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/start-main-show", validShowActionFPPBody,
		map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, actionReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup PUT show.action: status = %d; body: %s", resp.StatusCode, body)
	}

	body := `{"show":"never-created","label":"x","steps":[{"id":"s","action":"start-main-show","localFallback":{"class":"coordinator-required","reason":"x"}}]}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.macro/begin-set", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, respBody)
	}
	problem := decodeMap(t, respBody)
	if detail, _ := problem["detail"].(string); !containsSubstring(detail, "never-created") {
		t.Fatalf("problem.detail must name the missing show %q, got %v", "never-created", problem["detail"])
	}
}

// TestShowActionStaleShowReferenceStillReadsAndLists proves the GET/LIST
// half of ADR-009's own rule: a show.action written before this existence
// check shipped (or whose show was later deleted) must keep reading and
// listing — validation is a write-time gate only, never re-run on read.
// Simulated here by writing the object's revision directly into the store,
// bypassing DecodeShowActionPayload exactly as a pre-seam revision would
// have been written under the old, more permissive decoder. The RUN half
// of this rule is covered separately, in macro/run_test.go's
// TestRunExecutesWithAShowThatDoesNotExist.
func TestShowActionStaleShowReferenceStillReadsAndLists(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showConfigTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	staleAction := config.ShowActionPayload{
		Show: "long-deleted-show", Label: "Stop the show", SafetyClass: config.ShowSafetyClassStop,
		Target: config.ShowActionTarget{Integration: config.ShowActionIntegrationFPP, InstanceID: "player-01", Primitive: "stopPlaylist"},
	}
	payloadJSON, err := config.EncodeShowActionPayload(staleAction)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := st.CreateConfigObject(t.Context(), config.ShowActionConfigKind, "stale-action"); err != nil {
		t.Fatalf("CreateConfigObject: %v", err)
	}
	rev, err := st.CreateConfigRevision(t.Context(), store.ConfigRevisionRecord{
		Kind: config.ShowActionConfigKind, ObjectID: "stale-action", Revision: 1,
		PayloadJSON: payloadJSON, Source: "api",
	})
	if err != nil {
		t.Fatalf("CreateConfigRevision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(t.Context(), config.ShowActionConfigKind, "stale-action", rev.Revision); err != nil {
		t.Fatalf("ActivateConfigRevision: %v", err)
	}

	getResp, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action/stale-action", map[string]string{"Authorization": "Bearer " + token})
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET stale action: status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}

	listResp, listBody := doRequest(t, api.Handler, "GET", "/api/v1/config/show.action", map[string]string{"Authorization": "Bearer " + token})
	if listResp.StatusCode != http.StatusOK || !containsSubstring(string(listBody), `"stale-action"`) {
		t.Fatalf("LIST must still include the stale action: status=%d body=%s", listResp.StatusCode, listBody)
	}
}
