package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Covers item 5's own optional show.weatherdelay.notify webhook.

// recordingWebhookServer records every delivered weatherDelayNotifyPayload.
type recordingWebhookServer struct {
	mu       sync.Mutex
	received []weatherDelayNotifyPayload
	status   int
}

func newRecordingWebhookServer(t *testing.T, status int) (*httptest.Server, *recordingWebhookServer) {
	t.Helper()
	rec := &recordingWebhookServer{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload weatherDelayNotifyPayload
		_ = json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		rec.received = append(rec.received, payload)
		rec.mu.Unlock()
		w.WriteHeader(rec.status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *recordingWebhookServer) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.received))
	for i, p := range r.received {
		out[i] = p.Event
	}
	return out
}

func setWeatherDelayWebhook(t *testing.T, st *store.Store, webhookURL string) {
	t.Helper()
	payload, err := config.EncodeWeatherDelayPayload(config.WeatherDelayPayload{
		Alert: config.WeatherDelayDefaultPayload.Alert, Triggers: config.WeatherDelayDefaultPayload.Triggers,
		Notify: config.WeatherDelayNotifyPayload{WebhookURL: webhookURL},
	})
	if err != nil {
		t.Fatalf("encode show.weatherdelay payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, payload)
}

func TestWeatherDelayWebhookFiresOnStartCancelAndResume(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"k2"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"k3"}`, auth))
	weatherDelayNotifyBackground.Wait()

	got := rec.events()
	want := []string{"delay_started", "changed_to_cancel_night", "cleared"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestWeatherDelayWebhookFiresOnDirectCancelNightAndPlainResume(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"k1"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"k2"}`, auth))
	weatherDelayNotifyBackground.Wait()

	got := rec.events()
	want := []string{"cancel_night_started", "cleared"}
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestWeatherDelayWebhookNeverFiresWhenUnconfigured(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, _, adminToken, _ := newWeatherDelayTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: status = %d; body: %s", resp.StatusCode, body)
	}
	weatherDelayNotifyBackground.Wait()
	// No webhook configured: nothing to assert beyond "this did not hang
	// or panic," which a failing test would already report.
}

func TestWeatherDelayWebhookNeverBlocksTheOperatorsRequest(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	done := make(chan struct{})
	go func() {
		resp, _ := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
		if resp.StatusCode != http.StatusOK {
			t.Errorf("start: status = %d, want 200", resp.StatusCode)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the operator's own start request waited on a wedged webhook delivery")
	}
}

func TestWeatherDelayWebhookFailureRecordsLastNotifyError(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, _ := newWeatherDelayTestAPI(t)
	srv, _ := newRecordingWebhookServer(t, http.StatusInternalServerError)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	weatherDelayNotifyBackground.Wait()

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodGet, "/api/v1/weather-delay", "", auth))
	_ = resp
	var state struct {
		LastNotifyError string `json:"lastNotifyError"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if state.LastNotifyError == "" {
		t.Fatal("lastNotifyError is empty, want the recorded webhook failure")
	}
}

func TestWeatherDelayWebhookNeverFollowsARedirect(t *testing.T) {
	t.Cleanup(weatherDelayNotifyBackground.Wait)
	target, rec := newRecordingWebhookServer(t, http.StatusOK)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	h := &handlers{}
	reason := h.weatherDelayPostNotify(redirector.URL, weatherDelayNotifyPayload{Event: "delay_started", Kind: "delay", Active: true})
	if reason == "" {
		t.Fatal("weatherDelayPostNotify() = no error, want a refusal for a redirect response")
	}
	if len(rec.events()) != 0 {
		t.Fatal("the webhook followed a redirect to the target server")
	}
}

// TestWeatherDelayWebhookErrorNeverEchoesTheURL proves lastNotifyError, which
// any observation reader sees, never repeats a token held in the webhook URL.
func TestWeatherDelayWebhookErrorNeverEchoesTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := srv.URL + "/api/webhook/secret-token-123"
	srv.Close()

	reason := (&handlers{}).weatherDelayPostNotify(closedURL, weatherDelayNotifyPayload{Event: "delay_started", Kind: "delay", Active: true})
	if reason == "" {
		t.Fatal("weatherDelayPostNotify() = no error, want a failure against a closed server")
	}
	if strings.Contains(reason, "secret-token-123") {
		t.Fatalf("reason = %q, want no part of the webhook URL path", reason)
	}
}
