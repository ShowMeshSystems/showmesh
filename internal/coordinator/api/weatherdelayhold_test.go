package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is ADR-053 decision 3's own hold-side observable acceptance:
// while a weather delay is active, the routes that would start output are
// refused, and stop/blackout/power-off/emergency-stop are never among
// them.

func setWeatherDelayActive(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.SetWeatherDelayState(context.Background(), store.WeatherDelayStateRecord{
		Active: true, Kind: "delay", StartedAt: time.Now().UTC(), StartedBy: "op-1", Revision: 1,
	}); err != nil {
		t.Fatalf("SetWeatherDelayState: %v", err)
	}
}

func TestCueActivateRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/cues/some-cue/activate", ``, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("activate cue while delayed: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

func TestFPPStartPlaylistRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	body := `{"action":"startPlaylist","idempotencyKey":"key-1","params":{"playlist":"main"}}`
	resp, respBody := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/fpp/inst-1/commands", body, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("startPlaylist while delayed: status = %d, want 409; body: %s", resp.StatusCode, respBody)
	}
}

func TestFPPStopPlaylistNotRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	body := `{"action":"stopPlaylist","idempotencyKey":"key-1"}`
	resp, respBody := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/fpp/inst-1/commands", body, auth))
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("stopPlaylist while delayed must never be refused by the weather delay gate; body: %s", respBody)
	}
}

func TestNightCommandStartPreshowRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-preshow", ``, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-preshow while delayed: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

func TestNightCommandStartNightRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/start-night", ``, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("start-night while delayed: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

func TestNightCommandFadeOutNotRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/night/commands/fade-out-night", ``, auth))
	if resp.StatusCode == http.StatusConflict {
		t.Fatalf("fade-out-night while delayed must never be refused by the weather delay gate; body: %s", body)
	}
}

func TestWeatherDelayConfigPutRefusedDuringWeatherDelay(t *testing.T) {
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.weatherdelay", `{}`, auth))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT show.weatherdelay while delayed: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayHoldsAcrossFreshHandlersOverSameStore proves ADR-053
// decision 2's own "survives a coordinator restart" requirement: a
// brand-new *handlers built over the SAME store as the one that started
// the delay still sees it active, since the check reads the store fresh
// every time rather than caching anything at process/handlers level.
func TestWeatherDelayHoldsAcrossFreshHandlersOverSameStore(t *testing.T) {
	_, st, _, _ := newWeatherDelayTestAPI(t)
	setWeatherDelayActive(t, st)

	fresh := &handlers{deps: Dependencies{WeatherDelay: st}.withDefaults(), clock: time.Now, logger: testLogger()}
	active, err := fresh.weatherDelayActive(context.Background())
	if err != nil {
		t.Fatalf("weatherDelayActive: %v", err)
	}
	if !active {
		t.Fatal("a fresh *handlers over the same store did not see the active delay")
	}
}
