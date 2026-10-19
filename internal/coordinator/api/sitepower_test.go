package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

const sitePowerTwoSequencesBody = `{
	"shutdownSequences": [
		{"id": "lights", "label": "Lights", "steps": [
			{"action": "worklights-on", "requireConfirmation": true},
			{"delayMs": 1},
			{"action": "worklights-on", "powerDomain": "presentation", "domainProvenance": "operator-declared"}
		]},
		{"id": "projectors", "label": "Projectors", "steps": [
			{"action": "lamps-off", "requireConfirmation": true},
			{"delayMs": 1},
			{"action": "worklights-on", "powerDomain": "presentation", "domainProvenance": "operator-declared"}
		]}
	],
	"powerOnActions": ["worklights-on"]
}`

func newSitePowerFixture(t *testing.T) *emergencyStopFixture {
	t.Helper()
	f := newEmergencyStopFixture(t, testNow)
	mustPutAction(t, f.api, f.adminToken, "lamps-off", validShowActionResolumeBlackoutBody)
	return f
}

func (f *emergencyStopFixture) putSitePower(t *testing.T, body string) (int, string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.sitepower", body, map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, respBody := doRawRequest(t, f.api.Handler, req)
	return resp.StatusCode, string(respBody)
}

// deleteLampsOff removes the lamps-off action after it is referenced, so
// running it fails the way a deleted action does on the rig.
func (f *emergencyStopFixture) deleteLampsOff(t *testing.T) {
	t.Helper()
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/show.action/lamps-off", `{"confirm":true}`, map[string]string{"Authorization": "Bearer " + f.adminToken})
	if resp, body := doRawRequest(t, f.api.Handler, req); resp.StatusCode/100 != 2 {
		t.Fatalf("DELETE show.action/lamps-off: status = %d; body: %s", resp.StatusCode, body)
	}
}

func (f *emergencyStopFixture) resolumeActionsSent() []string {
	f.resolume.mu.Lock()
	defer f.resolume.mu.Unlock()
	out := make([]string, 0, len(f.resolume.calls))
	for _, c := range f.resolume.calls {
		out = append(out, c.action)
	}
	return out
}

func TestSitePowerConfigRoundTripsAndMatchesSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	f := newSitePowerFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + f.adminToken}

	_, defaultBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.sitepower", auth)
	assertMatchesSchema(t, c, "SitePowerConfigResponse", defaultBody)
	if !strings.Contains(string(defaultBody), `"shutdownSequences":[]`) {
		t.Fatalf("default body = %s, want empty shutdownSequences", defaultBody)
	}

	if code, body := f.putSitePower(t, sitePowerTwoSequencesBody); code != http.StatusOK {
		t.Fatalf("PUT show.sitepower: status = %d; body: %s", code, body)
	}
	_, getBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.sitepower", auth)
	assertMatchesSchema(t, c, "SitePowerConfigResponse", getBody)
	if !strings.Contains(string(getBody), `"id":"projectors"`) || !strings.Contains(string(getBody), `"revision":1`) {
		t.Fatalf("GET after PUT = %s", getBody)
	}
	_, revBody := doRequest(t, f.api.Handler, "GET", "/api/v1/config/show.sitepower/revisions", auth)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

func TestSitePowerConfigRefusesUnsafeOrUnknownSteps(t *testing.T) {
	f := newSitePowerFixture(t)
	cases := map[string]struct{ body, want string }{
		"environmental power": {`{"shutdownSequences":[{"id":"heat","label":"Heat","steps":[{"action":"worklights-on","powerDomain":"environmental","domainProvenance":"operator-declared"}]}]}`, "Only show equipment power"},
		"unknown action":      {`{"powerOnActions":["no-such-action"]}`, "does not exist"},
		"action and wait":     {`{"shutdownSequences":[{"id":"a","label":"A","steps":[{"action":"worklights-on","delayMs":5}]}]}`, "both an action and a wait"},
		"duplicate id":        {`{"shutdownSequences":[{"id":"a","label":"A","steps":[{"delayMs":5}]},{"id":"a","label":"B","steps":[{"delayMs":5}]}]}`, "used twice"},
		"unknown field":       {`{"shutdownSequences":[],"extra":1}`, "does not know, extra"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			code, body := f.putSitePower(t, tc.body)
			if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
				t.Fatalf("status = %d body = %s, want 400 containing %q", code, body, tc.want)
			}
		})
	}
}

func TestSystemPowerOffRunsSequencesAndStopsOneAtAFailedStep(t *testing.T) {
	f := newSitePowerFixture(t)
	if code, body := f.putSitePower(t, sitePowerTwoSequencesBody); code != http.StatusOK {
		t.Fatalf("PUT show.sitepower: status = %d; body: %s", code, body)
	}
	f.deleteLampsOff(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/site-power/off", `{}`, map[string]string{"Authorization": "Bearer " + f.adminToken})
	resp, body := doRawRequest(t, f.api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST site-power/off: status = %d; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "SitePowerRunResponse", body)
	f.api.h.sitePowerRuns.Wait()

	counts := map[string]int{}
	for _, a := range f.resolumeActionsSent() {
		counts[a]++
	}
	// lights: both actions sent; projectors: lamps-off fails, so its power cut is never sent.
	if counts["blackout"] != 2 {
		t.Fatalf("resolume actions sent = %v, want blackout x2 from the lights sequence only", counts)
	}
}

func TestHardStopModeSendsEveryActionAndSkipsWaits(t *testing.T) {
	f := newSitePowerFixture(t)
	seq := config.ShutdownSequence{ID: "projectors", Label: "Projectors", Steps: []config.ShutdownStep{
		{Action: "lamps-off", RequireConfirmation: true},
		{DelayMs: 60 * 60 * 1000},
		{Action: "worklights-on", PowerDomain: config.NightPowerDomainPresentation, DomainProvenance: config.NightDomainProvenanceOperatorDeclared},
	}}
	f.deleteLampsOff(t)
	results := f.api.h.runShutdownSequenceLive(context.Background(), seq, true, "test", authContext{}, "")
	if results[0].Outcome == outcomeWordConfirmed || results[1].Outcome != outcomeWordSkipped || results[2].Outcome != outcomeWordConfirmed {
		t.Fatalf("results = %+v, want an unconfirmed lamp step, a skipped wait and a sent power cut", results)
	}
}

func TestSystemPowerRefusesWithoutConfigOrScope(t *testing.T) {
	f := newSitePowerFixture(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/site-power/on", `{}`, map[string]string{"Authorization": "Bearer " + f.adminToken})
	if resp, body := doRawRequest(t, f.api.Handler, req); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "No power actions are set up") {
		t.Fatalf("site-power/on with nothing set up: status = %d; body: %s", resp.StatusCode, body)
	}
	req = newJSONRequest(t, http.MethodPost, "/api/v1/site-power/off", `{}`, map[string]string{"Authorization": "Bearer " + f.viewerToken})
	if resp, _ := doRawRequest(t, f.api.Handler, req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("site-power/off as viewer: status = %d, want 403", resp.StatusCode)
	}
}
