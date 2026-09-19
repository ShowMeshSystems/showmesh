package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/coordinator/weathertrigger"
)

// decodeJSON decodes body into a fresh T, failing the test on error.
func decodeJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode JSON response %s: %v", body, err)
	}
	return v
}

// newWeatherDelayTriggerTestAPI mirrors newWeatherDelayTestAPI, additionally
// returning a handlers value sharing the SAME store under an independently
// advanceable clock, so a test can move time forward and call a trigger
// loop method directly instead of waiting on a real ticking loop.
func newWeatherDelayTriggerTestAPI(t *testing.T, now func() time.Time) (*API, *handlers, *store.Store, string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st, WeatherDelayTrigger: st,
	}.withDefaults()
	a := New(deps, Options{Clock: now, Logger: testLogger()})
	h := &handlers{deps: deps, clock: now, logger: testLogger()}
	return a, h, st, mustIssueToken(t, svc, admin.ID)
}

func mustPutWeatherDelayTriggerConfig(t *testing.T, api *API, adminToken, payload string) {
	t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.weatherdelay", payload, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.weatherdelay: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func postWeatherDelayTrigger(t *testing.T, api *API, adminToken, source, body string) (*http.Response, []byte) {
	t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	return doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/triggers/"+source, body, auth))
}

func getWeatherDelayState(t *testing.T, api *API, adminToken string) v1.WeatherDelayStateResponse {
	t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/weather-delay", "", auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET weather-delay: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	return decodeJSON[v1.WeatherDelayStateResponse](t, body)
}

// TestWeatherDelayTriggerAsksAPendingDecision proves an inbound trigger with
// no suggestCancel raises a "delay" pending decision, without touching the
// state itself.
func TestWeatherDelayTriggerAsksAPendingDecision(t *testing.T) {
	api, _, st, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))

	resp, body := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning","severity":"Extreme"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trigger: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeJSON[v1.WeatherDelayTriggerResponse](t, body)
	if !got.Accepted || got.PendingDecision == nil {
		t.Fatalf("trigger response = %+v, want accepted with a pending decision", got)
	}
	if got.PendingDecision.Question != weathertrigger.QuestionDelay || got.PendingDecision.DefaultAction != weathertrigger.ActionDelay {
		t.Fatalf("pendingDecision = %+v, want question/defaultAction delay", got.PendingDecision)
	}
	if got.PendingDecision.Reason != "A tornado warning is in effect." {
		t.Fatalf("pendingDecision.Reason = %q, want no clock time and no fixture text", got.PendingDecision.Reason)
	}

	state := getWeatherDelayState(t, api, adminToken)
	if state.PendingDecision == nil || state.PendingDecision.ID != got.PendingDecision.ID {
		t.Fatalf("GET pendingDecision = %+v, want it to match the trigger response", state.PendingDecision)
	}

	rec, ok, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok || rec.Source != "nws" {
		t.Fatalf("stored pending decision = %+v, ok=%v, err=%v", rec, ok, err)
	}
}

// TestWeatherDelayTriggerSuggestCancelAsksDelayOrCancel proves suggestCancel
// is the only way a trigger reaches the delayOrCancel question.
func TestWeatherDelayTriggerSuggestCancelAsksDelayOrCancel(t *testing.T) {
	api, _, _, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))
	mustPutWeatherDelayTriggerConfig(t, api, adminToken, `{"triggers":{"answerWindowSeconds":30,"cancelAnswerWindowSeconds":180}}`)

	_, body := postWeatherDelayTrigger(t, api, adminToken, "ops-console", `{"kind":"lightning","distanceKm":9.6,"suggestCancel":true}`)
	got := decodeJSON[v1.WeatherDelayTriggerResponse](t, body)
	if got.PendingDecision == nil || got.PendingDecision.Question != weathertrigger.QuestionDelayOrCancel || got.PendingDecision.DefaultAction != weathertrigger.ActionCancelNight {
		t.Fatalf("pendingDecision = %+v, want delayOrCancel/cancelNight", got.PendingDecision)
	}
	if got.PendingDecision.Reason != "Lightning was reported 6 miles away." {
		t.Fatalf("pendingDecision.Reason = %q", got.PendingDecision.Reason)
	}
}

// TestWeatherDelayTriggerRejectsUnknownFields proves item 3a's "unknown
// keys refused".
func TestWeatherDelayTriggerRejectsUnknownFields(t *testing.T) {
	api, _, _, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))
	resp, body := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","bogus":1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trigger with unknown field: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayTriggerOnlyAsksOncePending proves item 1's "one at a
// time": a second trigger while one is pending raises no second decision.
func TestWeatherDelayTriggerOnlyAsksOncePending(t *testing.T) {
	api, _, st, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))

	postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	first, _, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil {
		t.Fatalf("GetPendingWeatherDelayDecision: %v", err)
	}

	_, body := postWeatherDelayTrigger(t, api, adminToken, "lightning-01", `{"kind":"lightning"}`)
	got := decodeJSON[v1.WeatherDelayTriggerResponse](t, body)
	if got.PendingDecision == nil || got.PendingDecision.ID != first.ID {
		t.Fatalf("a second trigger raised a different pending decision: %+v, want the first (%s) unchanged", got.PendingDecision, first.ID)
	}
}

// TestWeatherDelayTriggerDuringActiveDelayOnlyAsksAboutCancel proves item
// 1's "a delay already active: no new delay question; if the question
// would be delayOrCancel, ask only about changing to cancel night".
func TestWeatherDelayTriggerDuringActiveDelayOnlyAsksAboutCancel(t *testing.T) {
	api, _, st, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))
	setWeatherDelayActive(t, st)

	_, body := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	got := decodeJSON[v1.WeatherDelayTriggerResponse](t, body)
	if got.PendingDecision != nil {
		t.Fatalf("a plain warning during an active delay raised a decision: %+v, want none", got.PendingDecision)
	}

	_, body2 := postWeatherDelayTrigger(t, api, adminToken, "ops-console", `{"kind":"lightning","suggestCancel":true}`)
	got2 := decodeJSON[v1.WeatherDelayTriggerResponse](t, body2)
	if got2.PendingDecision == nil || got2.PendingDecision.Question != weathertrigger.QuestionDelayOrCancel {
		t.Fatalf("a suggestCancel trigger during an active delay = %+v, want a delayOrCancel question", got2.PendingDecision)
	}
}

// TestWeatherDelayDecisionAnswerBeforeDeadlineWins proves an operator
// answer clears the pending decision and starts exactly the requested path,
// and that the deadline then does nothing (it was already cleared).
func TestWeatherDelayDecisionAnswerBeforeDeadlineWins(t *testing.T) {
	advance, now := mutableClock(time.Now())
	api, h, st, adminToken := newWeatherDelayTriggerTestAPI(t, now)

	postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	pending, ok, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision: ok=%v, err=%v", ok, err)
	}

	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision",
		`{"id":"`+pending.ID+`","answer":"delay"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decision: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	decision := decodeJSON[v1.WeatherDelayDecisionResponse](t, body)
	if decision.Result == nil || !decision.Result.Active || decision.Result.Kind != "delay" {
		t.Fatalf("decision result = %+v, want an active delay", decision.Result)
	}
	if decision.Result.StartedByName != "admin-1" {
		t.Fatalf("StartedByName = %q, want the answering operator", decision.Result.StartedByName)
	}

	if _, ok, err := st.GetPendingWeatherDelayDecision(context.Background()); err != nil || ok {
		t.Fatalf("pending decision after an answer: ok=%v, err=%v, want cleared", ok, err)
	}

	// The deadline passing now must do nothing: there is no pending decision.
	advance(time.Hour)
	h.weatherDelayApplyTriggerDeadline(context.Background(), h.now(), pending)
	rec, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if rec.Kind != "delay" || rec.StartedByName != "admin-1" {
		t.Fatalf("state after a stale deadline fired anyway = %+v, want the operator's own delay untouched", rec)
	}
}

// TestWeatherDelayDecisionDismissSuppressesTheSameWarning proves dismiss
// suppresses a new question about the same source and same warning for the
// configured quiet period, and does not suppress a different warning.
func TestWeatherDelayDecisionDismissSuppressesTheSameWarning(t *testing.T) {
	advance, now := mutableClock(time.Now())
	api, _, st, adminToken := newWeatherDelayTriggerTestAPI(t, now)
	mustPutWeatherDelayTriggerConfig(t, api, adminToken, `{"triggers":{"dismissQuietMinutes":30}}`)

	postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	pending, _, _ := st.GetPendingWeatherDelayDecision(context.Background())

	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision",
		`{"id":"`+pending.ID+`","answer":"dismiss"}`, auth))

	_, body := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	suppressed := decodeJSON[v1.WeatherDelayTriggerResponse](t, body)
	if suppressed.PendingDecision != nil {
		t.Fatalf("a dismissed warning raised a new decision: %+v, want suppressed", suppressed.PendingDecision)
	}

	// A different warning from the same source is not suppressed.
	_, body2 := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Severe Thunderstorm Warning"}`)
	other := decodeJSON[v1.WeatherDelayTriggerResponse](t, body2)
	if other.PendingDecision == nil {
		t.Fatalf("a different warning from the same source was suppressed: %+v", other)
	}
	// Clear it so the quiet-period expiry check below starts clean.
	pending2, _, _ := st.GetPendingWeatherDelayDecision(context.Background())
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision",
		`{"id":"`+pending2.ID+`","answer":"dismiss"}`, auth))

	advance(31 * time.Minute)
	_, body3 := postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	afterQuiet := decodeJSON[v1.WeatherDelayTriggerResponse](t, body3)
	if afterQuiet.PendingDecision == nil {
		t.Fatalf("a warning after the quiet period expired was still suppressed: %+v", afterQuiet)
	}
}

// TestWeatherDelayTriggerDeadlineStartsADelay proves an unanswered "delay"
// question starts a delay at its deadline, startedBy the trigger source.
func TestWeatherDelayTriggerDeadlineStartsADelay(t *testing.T) {
	advance, now := mutableClock(time.Now())
	api, h, st, adminToken := newWeatherDelayTriggerTestAPI(t, now)

	postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	pending, ok, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision: ok=%v, err=%v", ok, err)
	}

	advance(time.Duration(pending.Deadline.Sub(now())) + time.Second)
	h.weatherDelayApplyTriggerDeadline(context.Background(), h.now(), pending)

	rec, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !rec.Active || rec.Kind != "delay" || rec.StartedBy != "nws" {
		t.Fatalf("state after the deadline = %+v, want an active delay started by nws", rec)
	}
	if _, ok, _ := st.GetPendingWeatherDelayDecision(context.Background()); ok {
		t.Fatal("pending decision still present after its deadline ran")
	}
}

// TestWeatherDelayTriggerDeadlineCancelsNight proves an unanswered
// delayOrCancel question cancels the night at its deadline.
func TestWeatherDelayTriggerDeadlineCancelsNight(t *testing.T) {
	advance, now := mutableClock(time.Now())
	api, h, st, adminToken := newWeatherDelayTriggerTestAPI(t, now)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)

	postWeatherDelayTrigger(t, api, adminToken, "ops-console", `{"kind":"lightning","suggestCancel":true}`)
	pending, ok, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision: ok=%v, err=%v", ok, err)
	}

	advance(time.Duration(pending.Deadline.Sub(now())) + time.Second)
	h.weatherDelayApplyTriggerDeadline(context.Background(), h.now(), pending)

	rec, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !rec.Active || rec.Kind != "cancelNight" || rec.StartedBy != "ops-console" {
		t.Fatalf("state after the deadline = %+v, want a cancelled night started by ops-console", rec)
	}
}

// TestWeatherDelayTriggerDeadlineSurvivesRestart proves the deadline is
// read from the store, not held only in memory: a fresh handlers value
// over the same store applies it correctly.
func TestWeatherDelayTriggerDeadlineSurvivesRestart(t *testing.T) {
	advance, now := mutableClock(time.Now())
	api, _, st, adminToken := newWeatherDelayTriggerTestAPI(t, now)

	postWeatherDelayTrigger(t, api, adminToken, "nws", `{"kind":"warning","eventType":"Tornado Warning"}`)
	pending, ok, err := st.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok {
		t.Fatalf("GetPendingWeatherDelayDecision: ok=%v, err=%v", ok, err)
	}
	advance(time.Duration(pending.Deadline.Sub(now())) + time.Second)

	// A fresh handlers value, as a coordinator restart would build, reads
	// the same pending decision back from the store.
	freshDeps := Dependencies{Config: st, WeatherDelay: st, WeatherDelayTrigger: st}.withDefaults()
	fresh := &handlers{deps: freshDeps, clock: now, logger: testLogger()}
	reread, ok, err := fresh.deps.WeatherDelayTrigger.GetPendingWeatherDelayDecision(context.Background())
	if err != nil || !ok || reread.ID != pending.ID || !reread.Deadline.Equal(pending.Deadline) {
		t.Fatalf("pending decision after a fresh handlers value = %+v, ok=%v, err=%v, want it unchanged", reread, ok, err)
	}
	fresh.weatherDelayApplyTriggerDeadline(context.Background(), fresh.now(), reread)

	rec, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !rec.Active || rec.Kind != "delay" {
		t.Fatalf("state after a fresh handlers value applied the deadline = %+v", rec)
	}
}

// TestWeatherDelaySourcesReportsNWSHealthAndTheWarningOnlyMessage proves
// GET /weather-delay reports the NWS source's own health and the
// warning-feed-alone sentence only when it is enabled.
func TestWeatherDelaySourcesReportsNWSHealthAndTheWarningOnlyMessage(t *testing.T) {
	api, _, _, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))

	disabled := getWeatherDelayState(t, api, adminToken)
	if len(disabled.Sources) != 0 || disabled.SourcesMessage != "" {
		t.Fatalf("sources with NWS disabled = %+v, want none", disabled)
	}

	mustPutWeatherDelayTriggerConfig(t, api, adminToken,
		`{"triggers":{"nws":{"enabled":true,"latitude":39,"longitude":-77,"contact":"ops@example.com"}}}`)

	enabled := getWeatherDelayState(t, api, adminToken)
	if len(enabled.Sources) != 1 || enabled.Sources[0].Source != weathertrigger.NWSSource || !enabled.Sources[0].Enabled {
		t.Fatalf("sources with NWS enabled = %+v", enabled.Sources)
	}
	if enabled.SourcesMessage == "" {
		t.Fatal("SourcesMessage is empty with NWS as the only enabled source, want the warning-feed-alone sentence")
	}
}

// TestWeatherDelayDecisionRefusesAMismatchedID proves an answer against a
// stale or unknown id is refused rather than silently resolving whatever
// is currently pending.
func TestWeatherDelayDecisionRefusesAMismatchedID(t *testing.T) {
	api, _, _, adminToken := newWeatherDelayTriggerTestAPI(t, fixedClock(time.Now()))
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/decision",
		`{"id":"does-not-exist","answer":"dismiss"}`, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("decision with an unknown id: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}
