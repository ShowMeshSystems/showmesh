package weathertrigger

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Fixtures below are hand-written from the public api.weather.gov
// alerts/active response shape (a GeoJSON FeatureCollection whose
// properties carry many fields; only event, severity, and expires are
// read here). No test in this file calls the real service.

const nwsFixtureOneTornadoWarning = `{
  "type": "FeatureCollection",
  "features": [
    {
      "id": "urn:oid:2.49.0.1.840.0.alert-tornado-0000000000",
      "type": "Feature",
      "properties": {
        "event": "Tornado Warning",
        "severity": "Extreme",
        "certainty": "Observed",
        "urgency": "Immediate",
        "expires": "2026-09-19T21:45:00-04:00",
        "headline": "Tornado Warning issued for a fake test county",
        "description": "This is fixture text that must never be stored or relayed.",
        "instruction": "Take shelter now. This text must never be stored or relayed."
      }
    }
  ]
}`

const nwsFixtureUnrelatedEventType = `{
  "type": "FeatureCollection",
  "features": [
    {
      "id": "urn:oid:2.49.0.1.840.0.alert-heat-0000000000",
      "type": "Feature",
      "properties": {
        "event": "Excessive Heat Warning",
        "severity": "Moderate",
        "expires": "2026-09-19T21:45:00-04:00"
      }
    }
  ]
}`

const nwsFixtureEmpty = `{"type": "FeatureCollection", "features": []}`

func newTestNWSPoller(t *testing.T, handler http.HandlerFunc, onAlert func(TriggerEvent) error, now time.Time) *NWSPoller {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := NWSPollerConfig{
		Latitude: 0, Longitude: 0, Contact: "ops@example.com",
		PollSeconds: 60, EventTypes: []string{"Tornado Warning", "Severe Thunderstorm Warning"},
	}
	return NewNWSPoller(cfg, onAlert, func() time.Time { return now }, nil).WithBaseURL(srv.URL)
}

func TestNWSPollerReportsAMatchingAlert(t *testing.T) {
	var got []TriggerEvent
	var mu sync.Mutex
	onAlert := func(e TriggerEvent) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, e)
		return nil
	}
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Errorf("request had no User-Agent header")
		}
		w.Header().Set("Content-Type", "application/geo+json")
		_, _ = w.Write([]byte(nwsFixtureOneTornadoWarning))
	}, onAlert, time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC))

	p.Poll(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d trigger events, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Source != NWSSource || e.Kind != KindWarning || e.EventType != "Tornado Warning" || e.Severity != "Extreme" {
		t.Fatalf("trigger event = %+v", e)
	}
	if e.ExpiresAt == nil {
		t.Fatalf("trigger event ExpiresAt is nil, want the alert's expires field")
	}
	if e.ExternalID == "" {
		t.Fatalf("trigger event ExternalID is empty, want the alert's own id")
	}
}

func TestNWSPollerIgnoresAnUnconfiguredEventType(t *testing.T) {
	called := false
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nwsFixtureUnrelatedEventType))
	}, func(TriggerEvent) error { called = true; return nil }, time.Now())

	p.Poll(context.Background())

	if called {
		t.Fatal("onAlert was called for an event type not in EventTypes")
	}
}

func TestNWSPollerAsksOncePerAlertID(t *testing.T) {
	var calls int
	var mu sync.Mutex
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nwsFixtureOneTornadoWarning))
	}, func(TriggerEvent) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}, time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC))

	for i := 0; i < 5; i++ {
		p.Poll(context.Background())
	}

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("onAlert was called %d times across 5 polls of the same alert id, want 1", calls)
	}
}

func TestNWSPollerNeverStoresOrRelaysWarningText(t *testing.T) {
	// The fixture's headline, description, and instruction fields contain
	// text that must never survive decoding: [NWSAlertProperties] has no
	// field for any of them, so the JSON decoder itself drops them; this
	// test proves the resulting TriggerEvent carries nothing beyond
	// event, severity, expiry, and the alert's own id.
	var got TriggerEvent
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nwsFixtureOneTornadoWarning))
	}, func(e TriggerEvent) error { got = e; return nil }, time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC))

	p.Poll(context.Background())

	reason := BuildReason(got)
	if reason != "A tornado warning is in effect." {
		t.Fatalf("BuildReason(got) = %q, want no fixture text leaked in", reason)
	}
}

func TestNWSPollerFailureChangesNothing(t *testing.T) {
	called := false
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, func(TriggerEvent) error { called = true; return nil }, time.Now())

	p.Poll(context.Background())

	if called {
		t.Fatal("onAlert was called despite a poll failure")
	}
	health := p.Health()
	if health.LastError == "" {
		t.Fatal("Health().LastError is empty after a failed poll")
	}
	if !health.LastSuccessAt.IsZero() {
		t.Fatalf("Health().LastSuccessAt = %v, want zero after a poll that never succeeded", health.LastSuccessAt)
	}
}

func TestNWSPollerRecordsSuccessOnAnEmptyResponse(t *testing.T) {
	now := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nwsFixtureEmpty))
	}, func(TriggerEvent) error { return nil }, now)

	p.Poll(context.Background())

	health := p.Health()
	if !health.LastSuccessAt.Equal(now) {
		t.Fatalf("Health().LastSuccessAt = %v, want %v", health.LastSuccessAt, now)
	}
	if health.LastError != "" {
		t.Fatalf("Health().LastError = %q, want empty after a successful empty poll", health.LastError)
	}
}

func TestNWSPollerRefusesACrossHostRedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(nwsFixtureOneTornadoWarning))
	}))
	t.Cleanup(elsewhere.Close)

	called := false
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/alerts/active", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	cfg := NWSPollerConfig{PollSeconds: 60, EventTypes: []string{"Tornado Warning"}, Contact: "ops@example.com"}
	p := NewNWSPoller(cfg, func(TriggerEvent) error { called = true; return nil }, time.Now, nil).WithBaseURL(origin.URL)

	p.Poll(context.Background())

	if called {
		t.Fatal("onAlert was called after a cross-host redirect that should have been refused")
	}
	if h := p.Health(); h.LastError == "" {
		t.Fatal("Health().LastError is empty after a refused cross-host redirect")
	}
}

func TestNWSPollerCapsResponseSize(t *testing.T) {
	huge := fmt.Sprintf(`{"type":"FeatureCollection","features":[],"padding":"%s"}`, make([]byte, nwsMaxResponseBytes+1024))
	p := newTestNWSPoller(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(huge))
	}, func(TriggerEvent) error { return nil }, time.Now())

	p.Poll(context.Background())

	if h := p.Health(); h.LastError == "" {
		t.Fatal("Health().LastError is empty after an oversize response")
	}
}
