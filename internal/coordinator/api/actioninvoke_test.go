package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fakeMQTTBrokerRegistry is a minimal [MQTTBrokerRegistry] fake: publish
// calls are recorded, and AwaitResponse answers with a preconfigured
// message or error per broker id.
type fakeMQTTBrokerRegistry struct {
	mu        sync.Mutex
	publishes int
	msg       broker.Message
	err       error
	// delay, when set, is slept inside AwaitResponse before returning —
	// used to prove dispatchedAt is stamped BEFORE the wait, not derived
	// from whatever instant the wait resolved at.
	delay time.Duration
}

func (f *fakeMQTTBrokerRegistry) Publish(context.Context, string, string, byte, bool, []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes++
	return f.err
}

func (f *fakeMQTTBrokerRegistry) AwaitResponse(context.Context, string, broker.ResponseRequest) (broker.Message, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes++
	if f.err != nil {
		return broker.Message{}, f.err
	}
	return f.msg, nil
}

func (f *fakeMQTTBrokerRegistry) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publishes
}

func mustPutAction(t *testing.T, api *API, token, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.action/"+id, body,
		map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.action/%s: status = %d; body: %s", id, resp.StatusCode, respBody)
	}
}

func invokeActionRequest(id, idempotencyKey, bearerToken string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/"+id+"/invocations",
		strings.NewReader(`{"idempotencyKey":"`+idempotencyKey+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	return req
}

// TestInvokeActionRejectsUnrecognizedRequestBodyKeys is A3: this is the
// one endpoint ADR-029 decision 3's raw hatch must never leak through, so
// a caller trying to smuggle a protocol parameter alongside
// idempotencyKey is refused by name, never silently ignored and
// dispatched with the caller's own parameters discarded.
func TestInvokeActionRejectsUnrecognizedRequestBodyKeys(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	deps.ResolumeActions = dispatcher
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions/blackout-now/invocations",
		strings.NewReader(`{"idempotencyKey":"key-1","params":{"topic":"falcon/player/bench-fpp/command/run"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "params") {
		t.Errorf("problem detail = %q, want it to name the unrecognized key", detail)
	}
	if dispatcher.callCount() != 0 {
		t.Errorf("resolume dispatch calls = %d, want 0 (a refused request must never dispatch)", dispatcher.callCount())
	}
}

// TestInvokeActionRequiresShowActionInvokeScope proves the scope gate: a
// viewer (no show:action:invoke) is refused 403; an operator (which holds
// it) is not.
func TestInvokeActionRequiresShowActionInvokeScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	adminToken := mustIssueToken(t, svc, admin.ID)
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeActions = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, adminToken, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, adminToken, "blackout-now", validShowActionResolumeBlackoutBody)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/actions/blackout-now/invocations",
		`{"idempotencyKey":"key-viewer"}`, map[string]string{"Authorization": "Bearer " + viewerToken})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer: status = %d, want 403; body: %s", resp.StatusCode, body)
	}

	req2 := newJSONRequest(t, http.MethodPost, "/api/v1/actions/blackout-now/invocations",
		`{"idempotencyKey":"key-admin"}`, map[string]string{"Authorization": "Bearer " + adminToken})
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
}

// TestInvokeActionResolumeConfirmed proves the resolume branch dispatches
// through [Dependencies.ResolumeActions].Dispatch — the same seam
// resolumeaction.go's own HTTP handler and macro's step both use — and
// reports the outcome honestly.
func TestInvokeActionResolumeConfirmed(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "every layer went dark"},
	}}
	deps.ResolumeActions = dispatcher
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "key-1", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if result["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want confirmed; body: %s", result["outcome"], body)
	}
	if result["actionId"] != "blackout-now" {
		t.Errorf("actionId = %v, want blackout-now", result["actionId"])
	}
	if dispatcher.callCount() != 1 {
		t.Errorf("resolume dispatch calls = %d, want exactly 1", dispatcher.callCount())
	}
}

// TestInvokeActionReplayReturnsOriginalResultWithoutRedispatching proves
// idempotency: the same key against the same action answers with the
// original result and dispatches nothing a second time.
func TestInvokeActionReplayReturnsOriginalResultWithoutRedispatching(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	dispatcher := &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	deps.ResolumeActions = dispatcher
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	resp1, body1 := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "replay-key", token))
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first: status = %d; body: %s", resp1.StatusCode, body1)
	}
	resp2, body2 := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "replay-key", token))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second: status = %d; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2 := m2["result"].(map[string]any)
	if replay, _ := result2["replay"].(bool); !replay {
		t.Errorf("second request's replay = %v, want true", result2["replay"])
	}
	if dispatcher.callCount() != 1 {
		t.Errorf("resolume dispatch calls = %d, want exactly 1 (replay must not re-dispatch)", dispatcher.callCount())
	}
}

// TestInvokeActionIdempotencyKeyReusedForDifferentActionIs409 proves the
// same key against a DIFFERENT action id is refused as a conflict, not
// silently treated as a replay.
func TestInvokeActionIdempotencyKeyReusedForDifferentActionIs409(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeActions = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)
	mustPutAction(t, api, token, "blackout-again", validShowActionResolumeBlackoutBody)

	if resp, body := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "shared-key", token)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first: status = %d; body: %s", resp.StatusCode, body)
	}
	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("blackout-again", "shared-key", token))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestInvokeActionReplayCarriesTheOriginalLabel proves a replayed
// response reports the same label field the original dispatch did,
// rather than silently dropping it.
func TestInvokeActionReplayCarriesTheOriginalLabel(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	// showConfigTestDeps leaves Commands unwired, which makes
	// UpdateCommandOutcome a silent no-op; wiring it to st matches real
	// coordinator wiring (coordinator.go) and is required to observe
	// what a replay actually reads back.
	deps.Commands = st
	deps.ResolumeActions = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	resp1, body1 := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "label-key", token))
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first: status = %d; body: %s", resp1.StatusCode, body1)
	}
	m1 := decodeMap(t, body1)
	result1 := m1["result"].(map[string]any)
	if result1["label"] != "Blackout everything" {
		t.Fatalf("first response label = %v, want %q; body: %s", result1["label"], "Blackout everything", body1)
	}

	resp2, body2 := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", "label-key", token))
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second: status = %d; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2 := m2["result"].(map[string]any)
	if replay, _ := result2["replay"].(bool); !replay {
		t.Fatalf("second request's replay = %v, want true", result2["replay"])
	}
	if result2["label"] != "Blackout everything" {
		t.Errorf("replayed label = %v, want %q (a replay must carry the same fields the original dispatch did); body: %s",
			result2["label"], "Blackout everything", body2)
	}
}

// TestInvokeActionIdempotencyKeyReusedByADifferentCommandFamilyIs409
// proves that TargetID's grammar is operator-chosen and not namespaced
// per command family, so an FPP instance id can collide with a show.action id
// of the same text. A key already used by an FPP command whose TargetID
// happens to equal the requested action id must be refused as a conflict,
// never answered as a replay carrying that unrelated command's result.
func TestInvokeActionIdempotencyKeyReusedByADifferentCommandFamilyIs409(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.ResolumeActions = &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed, Reason: "went dark"},
	}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	const sharedKey = "cross-family-key"
	rec, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "other-command", IdempotencyKey: sharedKey, Action: "fpp.stopPlaylist",
		TargetKind: "fpp", TargetID: "blackout-now",
		IssuerPrincipalID: "operator-1", IssuerPrincipalName: "operator-1",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed unrelated command: %v", err)
	}
	resolvedState := "resolved"
	resultJSON := `{"outcome":"confirmed"}`
	if err := st.UpdateCommandOutcome(context.Background(), rec.ID, store.CommandOutcomeUpdate{
		State: &resolvedState, ResultJSON: &resultJSON,
	}); err != nil {
		t.Fatalf("resolve unrelated command: %v", err)
	}

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("blackout-now", sharedKey, token))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (a command from a different family must never resolve as a replay); body: %s",
			resp.StatusCode, body)
	}
}

// TestInvokeActionMQTTConfirmedAndUnconfirmable proves the mqtt branch
// dispatches through [DispatchMQTTAction] over [Dependencies.MQTTBrokers]:
// a "none" expect kind reports unconfirmable, never success dressed up as
// confirmed.
func TestInvokeActionMQTTConfirmedAndUnconfirmable(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	brokers := &fakeMQTTBrokerRegistry{}
	deps.MQTTBrokers = brokers
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "relay-on", validShowActionMQTTNoneBody)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("relay-on", "mqtt-key-1", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if result["outcome"] != "unconfirmable" {
		t.Errorf("outcome = %v, want unconfirmable; body: %s", result["outcome"], body)
	}
	if brokers.publishes == 0 {
		t.Errorf("expected the mqtt broker to have been published to")
	}
}

// TestInvokeActionMQTTNonConfirmedResultPersistsASpecificOutcomeState
// proves that the macro path computes a specific OutcomeState for an
// MQTT result (negative_answer, malformed_payload, deadline_exceeded, ...);
// action invocation must persist that same specific state rather than an
// empty one for a non-confirmed MQTT result.
func TestInvokeActionMQTTNonConfirmedResultPersistsASpecificOutcomeState(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	deps.Commands = st
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("false")}}
	deps.MQTTBrokers = brokers
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "relay-check", validShowActionMQTTBooleanBody)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("relay-check", "mqtt-state-key", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if result["outcome"] != "failed" {
		t.Fatalf("outcome = %v, want failed (a boolean expect answered false); body: %s", result["outcome"], body)
	}
	cmdID, _ := result["id"].(string)
	if cmdID == "" {
		t.Fatalf("response carried no command id; body: %s", body)
	}

	rec, err := st.GetCommand(context.Background(), cmdID)
	if err != nil {
		t.Fatalf("GetCommand: %v", err)
	}
	if rec.OutcomeState == "" {
		t.Fatalf("persisted OutcomeState is empty, want a specific state (%q)", mqttActionStateNegativeAnswer)
	}
	if rec.OutcomeState != mqttActionStateNegativeAnswer {
		t.Errorf("persisted OutcomeState = %q, want %q", rec.OutcomeState, mqttActionStateNegativeAnswer)
	}
}

// TestInvokeActionMQTTDispatchedAtPredatesResolvedAt is a review finding:
// dispatchedAt is ADR-003's own anchor for "evidence post-dates dispatch"
// and must be stamped when the publish is attempted, not derived from
// whatever instant the wait resolved at — an mqtt action can legitimately
// wait up to 120s between the two. Proved against a real clock and a fake
// broker that sleeps inside AwaitResponse, so a dispatchedAt collapsed to
// resolvedAt (the defect this test was added to catch) reports a gap far
// smaller than the sleep.
func TestInvokeActionMQTTDispatchedAtPredatesResolvedAt(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, time.Now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showConfigTestDeps(svc, st)
	const wait = 80 * time.Millisecond
	brokers := &fakeMQTTBrokerRegistry{msg: broker.Message{Payload: []byte("true")}, delay: wait}
	deps.MQTTBrokers = brokers
	api := New(deps, Options{Clock: time.Now, Logger: testLogger()})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "relay-check", validShowActionMQTTBooleanBody)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("relay-check", "mqtt-timing-key", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	dispatchedAtStr, _ := result["dispatchedAt"].(string)
	resolvedAtStr, _ := result["resolvedAt"].(string)
	if dispatchedAtStr == "" || resolvedAtStr == "" {
		t.Fatalf("expected both dispatchedAt and resolvedAt to be set; body: %s", body)
	}
	dispatchedAt, err := time.Parse(time.RFC3339Nano, dispatchedAtStr)
	if err != nil {
		t.Fatalf("parse dispatchedAt: %v", err)
	}
	resolvedAt, err := time.Parse(time.RFC3339Nano, resolvedAtStr)
	if err != nil {
		t.Fatalf("parse resolvedAt: %v", err)
	}
	gap := resolvedAt.Sub(dispatchedAt)
	if gap < wait/2 {
		t.Fatalf("resolvedAt - dispatchedAt = %s, want at least ~%s (dispatchedAt must predate the mqtt wait, not be "+
			"derived from when it resolved)", gap, wait)
	}
}

// TestInvokeActionAuditUnavailableExemptSafetyClassStillDispatches proves
// ADR-024 decision 11's boundary read straight off the stored action's
// own safetyClass: a "stop"-classed FPP action still dispatches when the
// audit store is unwritable, with degraded attribution reported rather
// than refused.
func TestInvokeActionAuditUnavailableExemptSafetyClassStillDispatches(t *testing.T) {
	fppSrv, fppFake := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs(nil)

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	deps := setup.deps()
	deps.Config = setup.st
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 20 * 1e6, // 20ms: this test does not care about confirmation, only dispatch.
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "stop-now", validShowActionFPPStopBody)

	installFailAuditTrigger(t, setup.storeDir)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("stop-now", "stop-key-1", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stop is exempt from the audit fail-closed rule); body: %s", resp.StatusCode, body)
	}
	if fppFake.hitCount() != 1 {
		t.Errorf("fpp hits = %d, want exactly 1 (the exemption must still dispatch)", fppFake.hitCount())
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if degraded, _ := result["attributionDegraded"].(bool); !degraded {
		t.Errorf("attributionDegraded = %v, want true", result["attributionDegraded"])
	}
	if reason, _ := result["dispatchAttributionReason"].(string); reason != degradedAttributionReasonSafetyClassExemption {
		t.Errorf("dispatchAttributionReason = %q, want %q (the durable record and the API response must say WHY "+
			"this ran unaudited, and this action's own stop safetyClass is the real reason)", reason, degradedAttributionReasonSafetyClassExemption)
	}
}

// Isolates the pre-dispatch half of the exemption proved above, using
// installFailDispatchAuditTrigger to fail only the DISPATCH audit insert
// and let the OUTCOME insert succeed. attributionDegraded is dispatchDegraded
// || outcomeDegraded, so pinning outcomeDegraded false is what stops this
// assertion passing for a reason other than the one it names.
func TestInvokeActionAuditUnavailableExemptSafetyClassDegradesOnDispatchAlone(t *testing.T) {
	fppSrv, fppFake := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs(nil)

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	deps := setup.deps()
	deps.Config = setup.st
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 20 * 1e6, // 20ms: this test does not care about confirmation, only dispatch.
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "stop-now", validShowActionFPPStopBody)

	installFailDispatchAuditTrigger(t, setup.storeDir)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("stop-now", "stop-key-2", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stop is exempt from the audit fail-closed rule); body: %s", resp.StatusCode, body)
	}
	if fppFake.hitCount() != 1 {
		t.Errorf("fpp hits = %d, want exactly 1 (the exemption must still dispatch)", fppFake.hitCount())
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if degraded, _ := result["attributionDegraded"].(bool); !degraded {
		t.Errorf("attributionDegraded = %v, want true: the pre-dispatch audit write failed even though the post-dispatch one succeeded", result["attributionDegraded"])
	}
}

// TestInvokeActionAuditUnavailableNonExemptRunsWithDegradedAttribution
// proves ADR-024 decision 11's amendment (owner ruling, 2026-08-26): an
// FPP action whose safetyClass is "none" still dispatches to
// FPP, with degraded attribution, when the audit store is unwritable,
// never refused for that reason alone. Observations are set to idle
// (rather than TestInvokeActionAuditUnavailableNonExemptRefusesWithNoDispatch's
// former setObs(nil)) so startPlaylist's own ifBusy=refuse guard clears
// and this proves the action's OWN normal outcome (confirmed dispatch),
// not merely that the pre-dispatch guard's own unrelated 409-shaped
// "refused" outcome still fires.
func TestInvokeActionAuditUnavailableNonExemptRunsWithDegradedAttribution(t *testing.T) {
	fppSrv, fppFake := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	deps := setup.deps()
	deps.Config = setup.st
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 20 * 1e6, // 20ms: this test does not care about confirmation, only dispatch.
	})
	mustPutShow(t, api, token, "halloween-2026", `{"name":"halloween-2026"}`)
	mustPutAction(t, api, token, "start-now", validShowActionFPPStartOnBenchBody)

	installFailAuditTrigger(t, setup.storeDir)

	resp, body := doRawRequest(t, api.Handler, invokeActionRequest("start-now", "start-key-1", token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ADR-024 decision 11 amended 2026-08-26: audit unavailability never blocks "+
			"an action); body: %s", resp.StatusCode, body)
	}
	if fppFake.hitCount() != 1 {
		t.Errorf("fpp hits = %d, want exactly 1 (the action must actually dispatch)", fppFake.hitCount())
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if outcome, _ := result["outcome"].(string); outcome == "refused" {
		t.Errorf("outcome = %q, want a normal dispatch outcome, not a refusal", outcome)
	}
	if degraded, _ := result["attributionDegraded"].(bool); !degraded {
		t.Errorf("attributionDegraded = %v, want true", result["attributionDegraded"])
	}
	if reason, _ := result["dispatchAttributionReason"].(string); reason != degradedAttributionReasonAuditNeverBlocks {
		t.Errorf("dispatchAttributionReason = %q, want %q (this action's own safetyClass is \"none\": it must "+
			"never claim it ran unaudited because of the blackout/stop/power-off safety class)", reason, degradedAttributionReasonAuditNeverBlocks)
	}
}

const validShowActionResolumeBlackoutBody = `{
	"show": "halloween-2026",
	"label": "Blackout everything",
	"safetyClass": "blackout",
	"target": {
		"integration": "resolume",
		"action": "blackout"
	}
}`

const validShowActionMQTTNoneBody = `{
	"show": "halloween-2026",
	"label": "Turn the relay on",
	"safetyClass": "none",
	"target": {
		"integration": "mqtt",
		"broker": "home-automation",
		"publish": {"topic": "relay/on", "payload": "1", "qos": 1, "retain": false},
		"expect": {"kind": "none"}
	}
}`

const validShowActionMQTTBooleanBody = `{
	"show": "halloween-2026",
	"label": "Check the relay",
	"safetyClass": "none",
	"target": {
		"integration": "mqtt",
		"broker": "home-automation",
		"publish": {"topic": "relay/check", "payload": "1", "qos": 1, "retain": false},
		"expect": {"kind": "boolean", "topic": "relay/state", "deadlineSeconds": 5}
	}
}`

const validShowActionFPPStartOnBenchBody = `{
	"show": "halloween-2026",
	"label": "Start the main show",
	"safetyClass": "none",
	"target": {
		"integration": "fpp",
		"instanceId": "bench-fpp",
		"primitive": "startPlaylist",
		"params": {"playlist": "Halloween Main", "ifBusy": "refuse"}
	}
}`

const validShowActionFPPStopBody = `{
	"show": "halloween-2026",
	"label": "Stop the main show",
	"safetyClass": "stop",
	"target": {
		"integration": "fpp",
		"instanceId": "bench-fpp",
		"primitive": "stopPlaylist",
		"params": {}
	}
}`

// TestActionInvokeHTTPWriteDeadlineExceedsMQTTMaxDeadline is the
// reconciliation actionInvokeHTTPWriteDeadline's own doc comment claims:
// an mqtt action's expect.deadlineSeconds can legitimately reach
// broker.MaxResponseDeadline, and this endpoint's own write deadline must
// stay ahead of that with real margin, or a slow-but-healthy mqtt
// confirmation would abort with a false transport failure before the
// coordinator ever answers. cmd/showmeshctl's own
// minActionInvokeClientTimeout (cmd_action.go) is reconciled against THIS
// value by TestMinActionInvokeClientTimeoutExceedsServerDeadline there —
// two independently chosen literals, since that program cannot import
// this package.
func TestActionInvokeHTTPWriteDeadlineExceedsMQTTMaxDeadline(t *testing.T) {
	const margin = 20 * time.Second
	need := broker.MaxResponseDeadline + margin
	if actionInvokeHTTPWriteDeadline < need {
		t.Fatalf("actionInvokeHTTPWriteDeadline (%s) is below broker.MaxResponseDeadline (%s) plus a %s margin — "+
			"an mqtt action whose expect.deadlineSeconds is set to the maximum could have its own write deadline "+
			"expire first, aborting a healthy, still-working conversation.",
			actionInvokeHTTPWriteDeadline, broker.MaxResponseDeadline, margin)
	}
}
