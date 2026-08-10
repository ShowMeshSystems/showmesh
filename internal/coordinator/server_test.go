package coordinator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleHealthzAlwaysOK(t *testing.T) {
	// /healthz must be 200 regardless of broker connectivity: it is a
	// liveness check, not a readiness check.
	for _, connected := range []bool{true, false} {
		srv := NewServer(":0", func() BrokerState {
			return BrokerState{Connected: connected, ObservedAt: time.Now()}
		}, nil)

		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("connected=%v: /healthz status = %d, want %d", connected, rec.Code, http.StatusOK)
		}
	}
}

func TestHandleReadyzReflectsBrokerState(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		srv := NewServer(":0", func() BrokerState {
			return BrokerState{Connected: false, Since: time.Now(), ObservedAt: time.Now()}
		}, nil)

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

	t.Run("connected and fresh", func(t *testing.T) {
		srv := NewServer(":0", func() BrokerState {
			return BrokerState{Connected: true, Since: time.Now(), ObservedAt: time.Now()}
		}, nil)

		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		srv.httpServer.Handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("/readyz status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("connected but stale evidence is unknown, not healthy", func(t *testing.T) {
		staleObservedAt := time.Now().Add(-(readyzStalenessWindow + 5*time.Second))
		srv := NewServer(":0", func() BrokerState {
			return BrokerState{Connected: true, Since: staleObservedAt, ObservedAt: staleObservedAt}
		}, nil)

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
		age, ok := body["observedAgeSecs"].(float64)
		if !ok {
			t.Fatalf("/readyz body missing numeric observedAgeSecs: %v", body)
		}
		if age < readyzStalenessWindow.Seconds() {
			t.Errorf("/readyz observedAgeSecs = %v, want it to reflect the stale age (> %v)", age, readyzStalenessWindow.Seconds())
		}
	})
}

func TestHandleVersionReturnsParseableJSON(t *testing.T) {
	srv := NewServer(":0", func() BrokerState { return BrokerState{} }, nil)

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

func TestNewServerNilBrokerStateFuncDefaultsNotReady(t *testing.T) {
	srv := NewServer(":0", nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with nil brokerState func status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}
