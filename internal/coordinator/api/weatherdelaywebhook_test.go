package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
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

// runNotifier runs n until the test ends and waits for the worker to exit.
func runNotifier(t *testing.T, n *WeatherDelayNotifier) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// newWeatherDelayWebhookTestAPI is newWeatherDelayTestAPI with a running
// notifier the test can wait on.
func newWeatherDelayWebhookTestAPI(t *testing.T) (*API, *store.Store, string, *WeatherDelayNotifier) {
	t.Helper()
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	cache := NewWeatherDelayGateCache()
	notifier := NewWeatherDelayNotifier(cache, nil)
	api := New(Dependencies{
		Nodes: &fakeNodeLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Config: st, WeatherDelay: st,
		WeatherDelayGateCache: cache, WeatherDelayNotifier: notifier,
	}.withDefaults(), Options{Clock: fixedClock(now), Logger: testLogger()})
	runNotifier(t, notifier)
	return api, st, mustIssueToken(t, svc, admin.ID), notifier
}

func TestWeatherDelayWebhookFiresOnStartCancelAndResume(t *testing.T) {
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, notifier := newWeatherDelayWebhookTestAPI(t)
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"k2"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"k3"}`, auth))
	notifier.waitIdle()

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
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, notifier := newWeatherDelayWebhookTestAPI(t)
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"k1"}`, auth))
	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/resume", `{"idempotencyKey":"k2"}`, auth))
	notifier.waitIdle()

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
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, _, adminToken, notifier := newWeatherDelayWebhookTestAPI(t)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start: status = %d; body: %s", resp.StatusCode, body)
	}
	notifier.waitIdle()
	// No webhook configured: nothing to assert beyond "this did not hang
	// or panic," which a failing test would already report.
}

// TestWeatherDelayWebhookNeverBlocksTheOperatorsRequest holds the worker in
// a wedged delivery, then proves the next operator request still returns.
func TestWeatherDelayWebhookNeverBlocksTheOperatorsRequest(t *testing.T) {
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, _ := newWeatherDelayWebhookTestAPI(t)

	arrived := make(chan struct{}, 8)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(block) })
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	<-arrived
	resp, body := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/cancel-night", `{"idempotencyKey":"k2"}`, auth))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel-night while a delivery is wedged: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

// TestWeatherDelayWebhookDeliversInQueueOrder queues events before the
// worker runs, so the order seen is the queue's, not the scheduler's.
func TestWeatherDelayWebhookDeliversInQueueOrder(t *testing.T) {
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	n := NewWeatherDelayNotifier(NewWeatherDelayGateCache(), nil)
	var want []string
	for i := range 20 {
		event := fmt.Sprintf("event-%02d", i)
		want = append(want, event)
		n.enqueue(weatherDelayNotifyItem{url: srv.URL, body: weatherDelayNotifyPayload{Event: event}})
	}
	runNotifier(t, n)
	n.waitIdle()

	if got := rec.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// TestWeatherDelayWebhookFullQueueDropsTheOldest proves a full queue drops
// its oldest event, says so in lastNotifyError, and delivers the rest in order.
func TestWeatherDelayWebhookFullQueueDropsTheOldest(t *testing.T) {
	srv, rec := newRecordingWebhookServer(t, http.StatusOK)
	cache := NewWeatherDelayGateCache()
	n := NewWeatherDelayNotifier(cache, nil)
	var want []string
	for i := range weatherDelayNotifyQueueSize + 1 {
		event := fmt.Sprintf("event-%02d", i)
		if i > 0 {
			want = append(want, event)
		}
		n.enqueue(weatherDelayNotifyItem{url: srv.URL, body: weatherDelayNotifyPayload{Event: event}})
	}
	if got := cache.notifyError(); got != weatherDelayNotifyDroppedMessage {
		t.Fatalf("lastNotifyError = %q, want %q", got, weatherDelayNotifyDroppedMessage)
	}
	runNotifier(t, n)
	n.waitIdle()

	if got := rec.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v (event-00 dropped)", got, want)
	}
}

func TestWeatherDelayWebhookFailureRecordsLastNotifyError(t *testing.T) {
	t.Cleanup(weatherDelayCancelShutdownBackground.Wait)
	api, st, adminToken, notifier := newWeatherDelayWebhookTestAPI(t)
	srv, _ := newRecordingWebhookServer(t, http.StatusInternalServerError)
	setWeatherDelayWebhook(t, st, srv.URL)
	auth := map[string]string{"Authorization": "Bearer " + adminToken}

	doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPost, "/api/v1/weather-delay/start", `{"idempotencyKey":"k1"}`, auth))
	notifier.waitIdle()

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
	target, rec := newRecordingWebhookServer(t, http.StatusOK)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	reason := weatherDelayPostNotify(context.Background(), redirector.URL, weatherDelayNotifyPayload{Event: "delay_started", Kind: "delay", Active: true})
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

	reason := weatherDelayPostNotify(context.Background(), closedURL, weatherDelayNotifyPayload{Event: "delay_started", Kind: "delay", Active: true})
	if reason == "" {
		t.Fatal("weatherDelayPostNotify() = no error, want a failure against a closed server")
	}
	if strings.Contains(reason, "secret-token-123") {
		t.Fatalf("reason = %q, want no part of the webhook URL path", reason)
	}
}
