package api

import (
	"context"
	"net/http"
	"testing"
	"time"

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
		Identity: svc, Config: st, WeatherDelay: st,
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

func TestWeatherDelayCancelNightAnswer501(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"key-1"}`, auth))
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("cancel-night: status = %d, want 501; body: %s", resp.StatusCode, body)
	}
	got, err := st.GetWeatherDelayState(context.Background())
	if err != nil {
		t.Fatalf("GetWeatherDelayState: %v", err)
	}
	if got != (store.WeatherDelayStateRecord{}) {
		t.Fatalf("stored state = %+v, want untouched", got)
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
