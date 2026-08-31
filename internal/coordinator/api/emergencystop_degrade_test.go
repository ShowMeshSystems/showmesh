package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is round 2's own coverage: NOTHING THAT SUPPORTS THE STOP MAY
// ABORT OR MASK THE STOP. The FPP instance registry, the night-session
// step, and reading this level's own follow-up configuration each
// degrade independently and the stop still proceeds, on the identical
// rule this build already applied to a follow-up action's own failure.

var errFakeFPPListInstances = errors.New("emergencystop_degrade_test: injected FPP registry read failure")
var errFakeNightSessionRead = errors.New("emergencystop_degrade_test: injected night session read failure")

// --- blocker 1: a registry read failure must never report success with
// nothing stopped, and must be WIRE-DISTINGUISHABLE from a confirmed
// zero-instance install. ---

func TestEmergencyStopRegistryReadFailureReportsAFailedOutcomeNotSuccess(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixtureWithDeps(t, now, &fakeFPPLister{err: errFakeFPPListInstances}, nil)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a registry read failure must not turn into an HTTP error either; it is reported inside the result): body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result := m["result"].(map[string]any)
	if noneConfigured, _ := result["noInstancesConfigured"].(bool); noneConfigured {
		t.Fatal("noInstancesConfigured = true for a REGISTRY READ FAILURE, want false: this is not a confirmed zero-instance install")
	}
	stopOutcomes := result["stopOutcomes"].([]any)
	if len(stopOutcomes) != 1 {
		t.Fatalf("stopOutcomes has %d entries, want exactly 1 (a synthetic failure entry), got %v", len(stopOutcomes), stopOutcomes)
	}
	entry := stopOutcomes[0].(map[string]any)
	if entry["outcome"] != "failed" {
		t.Fatalf("stopOutcomes[0].outcome = %v, want %q", entry["outcome"], "failed")
	}
}

// The defect was that a total dispatch failure and a confirmed
// zero-instance install were BYTE-IDENTICAL on the wire. This test proves
// they are now distinguishable, and that BOTH satisfy the openapi schema
// (stopOutcomes is a required array; neither may be null).
func TestEmergencyStopRegistryFailureAndZeroInstancesAreWireDistinguishable(t *testing.T) {
	c := newOpenAPICompiler(t)
	now := time.Now()

	failing := newEmergencyStopFixtureWithDeps(t, now, &fakeFPPLister{err: errFakeFPPListInstances}, nil)
	failing.putConfig(t, emergencyStopEmptyLevelsBody())
	failReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + failing.adminToken})
	failResp, failBody := doRawRequest(t, failing.api.Handler, failReq)
	if failResp.StatusCode != http.StatusOK {
		t.Fatalf("registry-failure case: status = %d, want 200; body: %s", failResp.StatusCode, failBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopResponse", failBody)

	zero := newEmergencyStopFixtureWithDeps(t, now, &fakeFPPLister{views: nil}, nil)
	zero.putConfig(t, emergencyStopEmptyLevelsBody())
	zeroReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + zero.adminToken})
	zeroResp, zeroBody := doRawRequest(t, zero.api.Handler, zeroReq)
	if zeroResp.StatusCode != http.StatusOK {
		t.Fatalf("zero-instances case: status = %d, want 200; body: %s", zeroResp.StatusCode, zeroBody)
	}
	assertMatchesSchema(t, c, "EmergencyStopResponse", zeroBody)

	failMap := decodeMap(t, failBody)["result"].(map[string]any)
	zeroMap := decodeMap(t, zeroBody)["result"].(map[string]any)

	if failMap["noInstancesConfigured"] == zeroMap["noInstancesConfigured"] {
		t.Fatalf("noInstancesConfigured is IDENTICAL (%v) for a registry failure and a confirmed zero-instance install; a client cannot tell them apart", failMap["noInstancesConfigured"])
	}
	failOutcomes, _ := failMap["stopOutcomes"].([]any)
	zeroOutcomes, _ := zeroMap["stopOutcomes"].([]any)
	if failOutcomes == nil {
		t.Fatal("registry-failure case: stopOutcomes is null, violating the required-array contract")
	}
	if zeroOutcomes == nil {
		t.Fatal("zero-instances case: stopOutcomes is null, violating the required-array contract")
	}
	if len(failOutcomes) == len(zeroOutcomes) {
		t.Fatalf("stopOutcomes has the SAME length (%d) for a registry failure and a confirmed zero-instance install; the two remain indistinguishable", len(failOutcomes))
	}
}

// --- blocker 2: a night-session store error must degrade, never abort
// the response or skip follow-ups. ---

func TestEmergencyStopNightSessionReadFailureDegradesAndStillDispatchesAndRunsFollowUps(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixtureWithDeps(t, now, nil, &erroringNightSessionStore{err: errFakeNightSessionRead}, "player-01")
	f.putConfig(t, emergencyStopWorklightsOnPowerDownBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop-power-down", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a night-session read failure must not turn into an HTTP error): body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)["result"].(map[string]any)
	stopOutcomes := m["stopOutcomes"].([]any)
	if len(stopOutcomes) != 1 {
		t.Fatalf("stopOutcomes has %d entries, want 1: the stop must still have been attempted", len(stopOutcomes))
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1: the stop must still have been dispatched", got)
	}
	ns := m["nightSession"].(map[string]any)
	if errStr, _ := ns["error"].(string); errStr == "" {
		t.Fatal("nightSession.error is empty, want it to carry the injected read failure")
	}
	followUps := m["followUps"].([]any)
	if len(followUps) != 1 {
		t.Fatalf("followUps has %d entries, want 1: a night-session failure must not skip the follow-ups", len(followUps))
	}
	if f.resolume.callCount() != 1 {
		t.Fatalf("resolume dispatch calls = %d, want 1: the follow-up must still have run", f.resolume.callCount())
	}
}

// --- blocker 3: an undecodable show.emergencystop revision must degrade
// to no follow-ups, never abort the stop. ---

func putUndecodableEmergencyStopConfig(t *testing.T, f *emergencyStopFixture) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.CreateConfigObject(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID); err != nil {
		t.Fatalf("create show.emergencystop config object: %v", err)
	}
	// The shape a downgrade or a forward-compatible writer can leave
	// behind: one unrecognized key inside an otherwise well-formed body.
	// DecodeEmergencyStopPayload's own rejectUnknownTopLevelKeys check
	// refuses this at write time, so it can only ever reach the store by
	// bypassing the PUT handler, exactly as this test does.
	payloadJSON := `{"stop":{"actions":[]},"stopPowerDown":{"actions":[]},"hardStop":{"actions":[]},"futureField":true}`
	if _, err := f.st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowEmergencyStopConfigKind, ObjectID: config.ShowEmergencyStopConfigObjectID,
		Revision: 1, PayloadJSON: payloadJSON, Source: "api",
	}); err != nil {
		t.Fatalf("create show.emergencystop config revision: %v", err)
	}
	if _, err := f.st.ActivateConfigRevision(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID, 1); err != nil {
		t.Fatalf("activate show.emergencystop config revision: %v", err)
	}
}

func TestEmergencyStopUndecodableConfigDegradesAndStillDispatches(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	putUndecodableEmergencyStopConfig(t, f)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an undecodable config row must not turn into an HTTP error): body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)["result"].(map[string]any)
	stopOutcomes := m["stopOutcomes"].([]any)
	if len(stopOutcomes) != 1 {
		t.Fatalf("stopOutcomes has %d entries, want 1: the stop does not need this configuration to proceed", len(stopOutcomes))
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1", got)
	}
	if errStr, _ := m["followUpConfigError"].(string); errStr == "" {
		t.Fatal("followUpConfigError is empty, want it to report the decode failure")
	}
	followUps, _ := m["followUps"].([]any)
	if len(followUps) != 0 {
		t.Fatalf("followUps = %v, want empty: no follow-up list could be read", followUps)
	}
}

// The hard-stop path is where an aborting config resolve used to be a
// genuine LOCKOUT: consume ran before the abort, so every retry burned a
// fresh arm token. Proves the fix from the fire side: fire still succeeds
// (and consumes exactly the one token it was given) even with an
// undecodable config row in place.
func TestEmergencyStopFireWithUndecodableConfigStillConsumesExactlyOneTokenAndSucceeds(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	putUndecodableEmergencyStopConfig(t, f)

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	fireResp, fireBody := doRawRequest(t, f.api.Handler, fireReq)
	if fireResp.StatusCode != http.StatusOK {
		t.Fatalf("fire: status = %d, want 200; body: %s", fireResp.StatusCode, fireBody)
	}
	if got := len(f.sentCommands()); got != 1 {
		t.Fatalf("FPP commands sent = %d, want exactly 1", got)
	}

	// The SAME token must now be refused (already consumed), never
	// "not armed" and never accepted again: exactly one token was spent.
	retryReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-2","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	retryResp, retryBody := doRawRequest(t, f.api.Handler, retryReq)
	if retryResp.StatusCode != http.StatusConflict {
		t.Fatalf("retry with the same (already-consumed) token: status = %d, want 409; body: %s", retryResp.StatusCode, retryBody)
	}
}

// --- consume-ordering: the arm token must NEVER be spent on a request
// that is refused for a reason unrelated to firing. ---

func TestEmergencyStopFireInvalidIdempotencyKeyDoesNotConsumeTheArmToken(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	// A too-long idempotencyKey (finding 6's own bound) must be refused
	// BEFORE consume runs.
	tooLong := make([]byte, maxEmergencyStopIdempotencyKeyLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	badReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"`+string(tooLong)+`","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	badResp, badBody := doRawRequest(t, f.api.Handler, badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("fire with an over-long idempotencyKey: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0: an invalid request must never dispatch", got)
	}

	// The SAME arm token must STILL be valid: the invalid request above
	// must not have consumed it.
	goodReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	goodResp, goodBody := doRawRequest(t, f.api.Handler, goodReq)
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("fire with the SAME token after the invalid request: status = %d, want 200 (the token must not have been burned by the earlier refusal); body: %s", goodResp.StatusCode, goodBody)
	}
}

// --- finding 6: an over-long idempotencyKey must be rejected at the
// boundary, never silently reported as a per-instance "refused". ---

func TestEmergencyStopOverLongIdempotencyKeyIsRejectedAtTheBoundary(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	tooLong := make([]byte, maxEmergencyStopIdempotencyKeyLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"`+string(tooLong)+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0: an over-long key must never reach per-instance dispatch", got)
	}
}

func emergencyStopWorklightsOnPowerDownBody() string {
	return `{"stop":{"actions":[]},"stopPowerDown":{"actions":["worklights-on"]},"hardStop":{"actions":[]}}`
}

// --- finding 7: the concurrent per-instance dispatch path itself, run
// under -race with more than one instance, not merely functionally. ---

func TestEmergencyStopDispatchesToEveryConfiguredInstanceConcurrently(t *testing.T) {
	now := time.Now()
	const instanceCount = 8
	f := newEmergencyStopFixtureWithInstances(t, now, instanceCount)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/stop", `{"idempotencyKey":"key-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)["result"].(map[string]any)
	stopOutcomes := m["stopOutcomes"].([]any)
	if len(stopOutcomes) != instanceCount {
		t.Fatalf("stopOutcomes has %d entries, want %d", len(stopOutcomes), instanceCount)
	}
	seen := make(map[string]bool, instanceCount)
	for _, o := range stopOutcomes {
		id, _ := o.(map[string]any)["instanceId"].(string)
		seen[id] = true
	}
	if len(seen) != instanceCount {
		t.Fatalf("saw %d distinct instance ids, want %d: %v", len(seen), instanceCount, seen)
	}
	if got := len(f.sentCommands()); got != instanceCount {
		t.Fatalf("FPP commands sent = %d, want %d", got, instanceCount)
	}
}

// --- gap: one principal's own token used by another. ---

func TestEmergencyStopFireRefusesAnotherPrincipalsArmToken(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())
	otherAdmin := mustCreatePrincipal(t, f.svc, "admin-2", identity.RoleAdmin)
	otherToken := mustIssueToken(t, f.svc, otherAdmin.ID)

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	fireReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + otherToken})
	resp, body := doRawRequest(t, f.api.Handler, fireReq)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("principal B firing principal A's own arm token: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if typ, _ := m["type"].(string); typ != ProblemTypeEmergencyStopHardStopNotArmed {
		t.Errorf("problem type = %q, want %q (principal B was never armed itself)", typ, ProblemTypeEmergencyStopHardStopNotArmed)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0", got)
	}

	// Principal A's own token must still be valid: principal B's attempt
	// must not have consumed it.
	fireAgainReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-2","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp2, body2 := doRawRequest(t, f.api.Handler, fireAgainReq)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("principal A firing its own still-valid token: status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
}

// --- round 3 finding 3: an oversized fire body must say so, not report
// the generic "not valid JSON" message a truncated body produces. ---

func TestEmergencyStopFireOversizedBodySaysTooLargeAndDoesNotConsumeTheArmToken(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	padding := make([]byte, maxEmergencyStopRequestBodyBytes+1)
	for i := range padding {
		padding[i] = ' '
	}
	oversized := `{"idempotencyKey":"fire-1","armToken":"` + armToken + `",` + string(padding) + `}`
	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire", oversized,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized fire body: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "too large") {
		t.Fatalf("problem detail = %q, want it to say the body is too large, not report a generic JSON parse failure", detail)
	}
	if got := len(f.sentCommands()); got != 0 {
		t.Fatalf("FPP commands sent = %d, want 0", got)
	}

	// The arm token must still be valid: the oversized, refused request
	// must not have consumed it.
	goodReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-2","armToken":"`+armToken+`"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	goodResp, goodBody := doRawRequest(t, f.api.Handler, goodReq)
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("fire with the SAME token after the oversized request: status = %d, want 200 (the token must not have been burned); body: %s", goodResp.StatusCode, goodBody)
	}
}

// Fire's own tolerance of an unrecognized key is INTENTIONAL (see
// handleEmergencyStopFire's own doc comment), unlike every sibling route:
// a stray field on the "big red button" route must never refuse the stop.
func TestEmergencyStopFireToleratesUnknownKeys(t *testing.T) {
	now := time.Now()
	f := newEmergencyStopFixture(t, now)
	f.putConfig(t, emergencyStopEmptyLevelsBody())

	armReq := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/arm", `{"idempotencyKey":"arm-1"}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	_, armBody := doRawRequest(t, f.api.Handler, armReq)
	armToken, _ := decodeMap(t, armBody)["armToken"].(string)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/emergency-stop/hard-stop/fire",
		`{"idempotencyKey":"fire-1","armToken":"`+armToken+`","aFieldFromANewerClient":true}`,
		map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fire with an unrecognized key: status = %d, want 200 (this route tolerates it on purpose); body: %s", resp.StatusCode, body)
	}
}
