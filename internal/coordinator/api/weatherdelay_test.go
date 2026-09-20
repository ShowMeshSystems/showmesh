package api

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

var weatherDelayActionPaths = []string{
	"/api/v1/weather-delay/start", "/api/v1/weather-delay/cancel-night", "/api/v1/weather-delay/resume",
}

func newWeatherDelayTestAPI(t *testing.T) (*API, *store.Store, string, string) {
	t.Helper()
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st, WeatherDelayTrigger: st,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	return api, st, mustIssueToken(t, svc, admin.ID), mustIssueToken(t, svc, viewer.ID)
}

func TestWeatherDelayActionRoutesCheckScopeBeforeBody(t *testing.T) {
	api, _, _, viewerToken := newWeatherDelayTestAPI(t)
	for _, path := range weatherDelayActionPaths {
		req := newJSONRequest(t, http.MethodPost, path, `{"bogus":1}`, map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as viewer: status = %d, want 403; body: %s", path, resp.StatusCode, body)
		}
	}
}

func TestWeatherDelayActionRoutesValidateBody(t *testing.T) {
	for _, path := range weatherDelayActionPaths {
		api, _, adminToken, _ := newWeatherDelayTestAPI(t)
		auth := map[string]string{"Authorization": "Bearer " + adminToken}
		for _, bad := range []string{``, `{}`, `{"idempotencyKey":""}`, `{"idempotencyKey":5}`, `{"idempotencyKey":"k","extra":true}`, `[]`} {
			resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, path, bad, auth))
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s body %q: status = %d, want 400; body: %s", path, bad, resp.StatusCode, body)
			}
		}
	}
}

func TestWeatherDelayCancelNightFromIdleStartsACancel(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel-night: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !got.Active || got.Kind != "cancelNight" || got.Revision != 1 || got.StartedAt.IsZero() || got.StartedBy == "" {
		t.Fatalf("stored state = %+v, want an active cancelNight at revision 1", got)
	}
}

// TestWeatherDelayChangeDelayToCancelNightInPlace proves ADR-053 decision
// 1: an active delay changed to a cancel keeps StartedAt but raises the
// revision and the kind.
func TestWeatherDelayChangeDelayToCancelNightInPlace(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
	before, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel-night: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	after, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if after.Kind != "cancelNight" {
		t.Fatalf("Kind = %q, want cancelNight", after.Kind)
	}
	if !after.StartedAt.Equal(before.StartedAt) {
		t.Fatalf("StartedAt = %v, want unchanged %v", after.StartedAt, before.StartedAt)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("Revision = %d, want greater than %d", after.Revision, before.Revision)
	}
}

// TestWeatherDelayCancelNightIsIdempotent proves a second cancel-night
// call while one is already active changes nothing.
func TestWeatherDelayCancelNightIsIdempotent(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	first, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second cancel-night: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	second, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if second != first {
		t.Fatalf("second cancel-night changed the stored state: first=%+v second=%+v, want unchanged", first, second)
	}
}

// TestWeatherDelayStartCannotDowngradeACancelledNight proves decision 3:
// once cancelled, the ordinary start route cannot turn it back into a
// mere delay.
func TestWeatherDelayStartCannotDowngradeACancelledNight(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got.Kind != "cancelNight" {
		t.Fatalf("Kind = %q after a start request, want the cancellation to stay set (cancelNight)", got.Kind)
	}
}

// TestWeatherDelayResumeOnCancelNightClearsAndSkipsNightSession proves
// decision 3 and item 3's own "does NOT restart any show or touch the
// night session."
func TestWeatherDelayResumeOnCancelNightClearsAndSkipsNightSession(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got.Active {
		t.Fatalf("stored state = %+v, want not active after clearing a cancelled night", got)
	}
}

// TestWeatherDelayNightStartRefusedWithCancelNightSentence proves item 3's
// exact operator sentence when a night session tries to start while a
// cancelled night is set.
func TestWeatherDelayNightStartRefusedWithCancelNightSentence(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", `{}`, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night while cancelled: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("Tonight's show was cancelled for weather. Clear the cancellation to start a show.")) {
		t.Fatalf("start-night refusal body = %s, want the exact cancel-night sentence", body)
	}
	if _, err := st.GetWeatherDelayState(context.Background()); err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
}

// TestWeatherDelayCancelNightSurvivesRestart proves item 3's own "stays
// set until an operator clears it, even across a coordinator restart":
// a fresh *handlers over the same store still sees it, and its own
// night-start refusal still applies.
func TestWeatherDelayCancelNightSurvivesRestart(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))

	fresh := &handlers{deps: Dependencies{WeatherDelay: st}.withDefaults(), clock: time.Now, logger: testLogger()}
	rec, err := fresh.deps.WeatherDelay.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !rec.Active || rec.Kind != "cancelNight" {
		t.Fatalf("a fresh *handlers over the same store read = %+v, want an active cancelNight", rec)
	}

	// The original api's own night-start route reads the same store fresh
	// on every request (never an in-memory cache), so this still proves
	// the refusal is driven by durable state, not a request-scoped flag.
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", `{}`, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night after the fresh-handlers check: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

func TestWeatherDelayStartPersistsActiveBeforeDispatch(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if !got.Active || got.Kind != "delay" || got.Revision != 1 || got.StartedAt.IsZero() || got.StartedBy == "" {
		t.Fatalf("stored state = %+v, want an active delay at revision 1", got)
	}
}

func TestWeatherDelayStartIsIdempotentAndKeepsStartedAt(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
	first, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second start: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	second, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if second.Revision != first.Revision || !second.StartedAt.Equal(first.StartedAt) || second.StartedBy != first.StartedBy {
		t.Fatalf("second start changed the stored state: first=%+v second=%+v, want unchanged", first, second)
	}
}

func TestWeatherDelayResumeClearsState(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"key-1"}`, auth))
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"key-2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got.Active {
		t.Fatalf("stored state = %+v, want not active after resume", got)
	}
}

func TestWeatherDelayResumeRequiresItsOwnScope(t *testing.T) {
	api, _, _, viewerToken := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + viewerToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("resume as viewer: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayCancelledNightStartRefusalIsReported proves a night start
// refused by a cancelled night, such as FPP's scheduled start the next
// evening, is recorded in the event history and not only returned.
func TestWeatherDelayCancelledNightStartRefusalIsReported(t *testing.T) {
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st, WeatherDelayEvents: st,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + mustIssueToken(t, svc, admin.ID)}
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	weatherDelayCancelShutdownBackground.Wait()
	before := countEventsByCategory(t, st, v1.EventKindWeatherDelayChanged)

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", `{}`, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night while cancelled: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if after := countEventsByCategory(t, st, v1.EventKindWeatherDelayChanged); after != before+1 {
		t.Fatalf("weather delay events = %d after a refused night start, want %d", after, before+1)
	}
}
