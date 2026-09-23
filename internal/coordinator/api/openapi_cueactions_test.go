package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// TestOpenAPIShowCueActionsMatchRealResponses checks outputs.actions and
// the hand-fire actions outcomes against real responses.
func TestOpenAPIShowCueActionsMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	compileSchema(t, c, "CueActionOutcome")

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(showObjectsTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	mustPutAction(t, api, token, "blackout-now", validShowActionResolumeBlackoutBody)

	body := `{"show":"halloween-2026","name":"Song one","outputs":{"render":{"sequence":"song-one"},"actions":["blackout-now"]}}`
	resp, respBody := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/song-one", body, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue: status = %d; body: %s", resp.StatusCode, respBody)
	}
	assertMatchesSchema(t, c, "ShowCueConfigResponse", respBody)
	payload, _ := decodeMap(t, respBody)["payload"].(map[string]any)
	outputs, _ := payload["outputs"].(map[string]any)
	if actions, _ := outputs["actions"].([]any); len(actions) != 1 || actions[0] != "blackout-now" {
		t.Fatalf("outputs.actions read back as %v", outputs["actions"])
	}

	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	_, act := cueActionFixture(t, setup, now, "blackout-now")
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)
	h := newCueActionHandlers(setup, now, &fakeResolumeActionDispatcher{results: map[string]ResolumeActionResult{
		"blackout": {Outcome: ResolumeOutcomeConfirmed},
	}})
	rec := httptest.NewRecorder()
	h.handleActivateCue(rec, newCueFireTestRequest(t, act.CueID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("hand-fire status = %d; body: %s", rec.Code, rec.Body.String())
	}
	assertMatchesSchema(t, c, "CueActivateResponse", rec.Body.Bytes())
}
