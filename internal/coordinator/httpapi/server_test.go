package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
)

// fakeReadinessSource lets tests control what handleReadyz sees without a
// real broker (or any other) dependency.
type fakeReadinessSource struct {
	report readiness.Report
}

func (f fakeReadinessSource) Readiness() readiness.Report { return f.report }

func TestHandleHealthzAlwaysOK(t *testing.T) {
	// /healthz must be 200 regardless of readiness: it is a liveness check,
	// not a readiness check.
	for _, ready := range []bool{true, false} {
		srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{Ready: ready, ObservedAt: time.Now()}}, nil)

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("ready=%v: /healthz status = %d, want %d", ready, rec.Code, http.StatusOK)
		}
	}
}

func TestHandleReadyzReflectsReadinessSource(t *testing.T) {
	t.Run("not ready", func(t *testing.T) {
		srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{
			Ready:      false,
			Reason:     "mqtt broker not connected",
			ObservedAt: time.Now(),
			Details: map[string]any{
				"connected":       false,
				"observedAgeSecs": 0.0,
			},
		}}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("/readyz body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
		}
		if body["reason"] != "mqtt broker not connected" {
			t.Errorf("/readyz reason = %v, want %q", body["reason"], "mqtt broker not connected")
		}
	})

	t.Run("ready", func(t *testing.T) {
		srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{Ready: true, ObservedAt: time.Now()}}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("/readyz status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("not ready reason and details are surfaced verbatim", func(t *testing.T) {
		// This is the shape the broker package's Readiness() produces for
		// stale evidence (see broker_test.go for the staleness rule
		// itself); here we only verify the Server threads a source's
		// Reason and Details through to the response body untouched.
		srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{
			Ready:      false,
			Reason:     "mqtt broker evidence is stale",
			ObservedAt: time.Now(),
			Details: map[string]any{
				"connected":       true,
				"observedAgeSecs": 42.5,
			},
		}}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("/readyz body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
		}
		if body["reason"] != "mqtt broker evidence is stale" {
			t.Errorf("/readyz reason = %v, want it to name stale evidence", body["reason"])
		}
		if body["connected"] != true {
			t.Errorf("/readyz connected = %v, want true", body["connected"])
		}
		age, ok := body["observedAgeSecs"].(float64)
		if !ok {
			t.Fatalf("/readyz body missing numeric observedAgeSecs: %v", body)
		}
		if age != 42.5 {
			t.Errorf("/readyz observedAgeSecs = %v, want %v", age, 42.5)
		}
	})
}

func TestHandleVersionReturnsParseableJSON(t *testing.T) {
	srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/version status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("/version body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
	}

	if body.GoVersion == "" {
		t.Errorf("/version body missing goVersion: %+v", body)
	}
}

func TestNewServerNilReadinessSourceDefaultsNotReady(t *testing.T) {
	srv := NewServer(":0", nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with nil readiness source status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("/readyz body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
	}
	// The nil-source fallback must be dependency-neutral: httpapi knows
	// nothing about MQTT or any other specific dependency (see the package
	// doc comment), so its reason must not name one.
	if body["reason"] != "no readiness source configured" {
		t.Errorf("/readyz reason = %v, want %q", body["reason"], "no readiness source configured")
	}
	if body["status"] != "not ready" {
		t.Errorf("/readyz status = %v, want %q", body["status"], "not ready")
	}
	if _, ok := body["observedAgeSecs"]; ok {
		t.Errorf("/readyz body has observedAgeSecs = %v, want it omitted since the fallback has no evidence to report an age for", body["observedAgeSecs"])
	}
}

func TestWriteNotReadyDetailsCannotOverrideStatusOrReason(t *testing.T) {
	// A source that returns Details containing "status" or "reason" keys
	// must not be able to override the top-level verdict: the HTTP code is
	// always 503 here, and the body's status/reason must match it.
	srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{
		Ready:      false,
		Reason:     "mqtt broker not connected",
		ObservedAt: time.Now(),
		Details: map[string]any{
			"status": "ready",
			"reason": "spoofed",
		},
	}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("/readyz body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
	}
	if body["status"] != "not ready" {
		t.Errorf("/readyz body status = %v, want %q (Details must not override it)", body["status"], "not ready")
	}
	if body["reason"] != "mqtt broker not connected" {
		t.Errorf("/readyz body reason = %v, want %q (Details must not override it)", body["reason"], "mqtt broker not connected")
	}
}

func TestWriteNotReadyDerivesObservedAgeSecsFromObservedAt(t *testing.T) {
	// observedAgeSecs must come from the typed ObservedAt field, not from
	// whatever a source happens to put in Details (see readiness.Report's
	// doc comment and ADR-011).
	observedAt := time.Now().Add(-42 * time.Second)
	srv := NewServer(":0", fakeReadinessSource{report: readiness.Report{
		Ready:      false,
		Reason:     "mqtt broker not connected",
		ObservedAt: observedAt,
		Details:    map[string]any{"connected": false},
	}}, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("/readyz body did not parse as JSON: %v (body=%q)", err, rec.Body.String())
	}

	age, ok := body["observedAgeSecs"].(float64)
	if !ok {
		t.Fatalf("/readyz body missing numeric observedAgeSecs: %v", body)
	}
	if age < 42 {
		t.Errorf("observedAgeSecs = %v, want it derived from ObservedAt (>= 42)", age)
	}
}
