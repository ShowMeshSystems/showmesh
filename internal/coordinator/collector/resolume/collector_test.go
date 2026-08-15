package resolume

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func newTestCollector(t *testing.T, baseURL string, opts Options) *Collector {
	t.Helper()
	c, err := New("resolume-main", baseURL, opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestNewRejectsInvalidID(t *testing.T) {
	if _, err := New("Not Valid!", "http://127.0.0.1:9080", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for an id that fails mqttproto.ValidateNodeID")
	}
}

func TestNewRejectsBadBaseURL(t *testing.T) {
	if _, err := New("resolume-main", "http://127.0.0.1:9080/composition", Options{}); err == nil {
		t.Fatalf("New() error = nil, want an error for a base URL carrying a path")
	}
}

func TestCollectorIDReturnsConfiguredID(t *testing.T) {
	c := newTestCollector(t, "http://127.0.0.1:9080", Options{})
	if got := c.ID(); got != "resolume-main" {
		t.Errorf("ID() = %q, want %q", got, "resolume-main")
	}
}

func TestPollSuccessProducesBothSignals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			t.Errorf("unexpected request path %q; Poll must only ever request /product", r.URL.Path)
		}
		w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll() complete = false, want true always (see Poll's doc comment)")
	}
	if len(obs) != 2 {
		t.Fatalf("len(Poll() observations) = %d, want exactly 2 (SignalReachable, SignalProduct)", len(obs))
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.StateAt(now) != observation.StateCurrent {
		t.Errorf("resolume.reachable.StateAt(now) = %q, want current", reachable.StateAt(now))
	}
	if reachable.Value != true {
		t.Errorf("resolume.reachable.Value = %v, want true", reachable.Value)
	}
	if reachable.Quality != observation.QualityDerived {
		t.Errorf("resolume.reachable.Quality = %q, want derived (reachability is derived from the request succeeding, not a field Resolume reports)", reachable.Quality)
	}
	if reachable.Source != sourceName {
		t.Errorf("resolume.reachable.Source = %q, want %q", reachable.Source, sourceName)
	}

	product := findSignal(t, obs, SignalProduct)
	if product.StateAt(now) != observation.StateCurrent {
		t.Errorf("resolume.product.StateAt(now) = %q, want current", product.StateAt(now))
	}
	wantProduct := "Arena 7.23.2 (r51094)"
	if product.Value != wantProduct {
		t.Errorf("resolume.product.Value = %v, want %q", product.Value, wantProduct)
	}
	if product.Quality != observation.QualityDirect {
		t.Errorf("resolume.product.Quality = %q, want direct", product.Quality)
	}
}

// --- Required test 2: connection refused produces collection_failed --------

// TestPollConnectionRefusedProducesCollectionFailedOnBothSignals is the
// required reproduction: an unreachable Resolume must degrade BOTH
// signals to collection_failed with a classified reason, and StateAt must
// yield collection_failed — never a value, never healthy. Mirrors
// internal/coordinator/collector/fpp's own
// TestPollUnreachableProducesCollectionFailedNeverFabricatedFalse.
//
// Before trusting this test, Poll's error branch was temporarily changed
// to still emit resolume.reachable as a measured `false` (the fabrication
// CLAUDE.md's "absent evidence is stated, never omitted" rule forbids)
// and this test's Absence assertion was confirmed to fail — a measured
// false observation has Absence == "" by construction, so the check
// failed loudly rather than silently. Reverted afterward.
func TestPollConnectionRefusedProducesCollectionFailedOnBothSignals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listening on this port for the rest of the test

	now := time.Now()
	c := newTestCollector(t, url, Options{Now: fixedClock(&now)})

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("Poll() complete = false, want true always")
	}

	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if reachable.Value != nil {
		t.Errorf("resolume.reachable Value = %v, want nil — never a fabricated false", reachable.Value)
	}
	if reachable.Reason == "" {
		t.Errorf("resolume.reachable Reason is empty, want a classified failure reason")
	}
	if got := reachable.StateAt(now); got != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable.StateAt(now) = %q, want collection_failed — never a value, never healthy", got)
	}

	product := findSignal(t, obs, SignalProduct)
	if product.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.product Absence = %q, want collection_failed", product.Absence)
	}
	if product.Value != nil {
		t.Errorf("resolume.product Value = %v, want nil", product.Value)
	}
	if got := product.StateAt(now); got != observation.StateCollectionFailed {
		t.Errorf("resolume.product.StateAt(now) = %q, want collection_failed", got)
	}

	if reachable.Reason != product.Reason {
		t.Errorf("resolume.reachable Reason (%q) and resolume.product Reason (%q) differ; a single failed /product request should classify identically for both", reachable.Reason, product.Reason)
	}
}

func TestPollHTTPStatusErrorProducesCollectionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, _ := c.Poll(context.Background())
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if !contains(reachable.Reason, "503") {
		t.Errorf("resolume.reachable Reason = %q, want it to mention the 503 status", reachable.Reason)
	}
}

func TestPollDecodeErrorProducesCollectionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})

	obs, _ := c.Poll(context.Background())
	reachable := findSignal(t, obs, SignalReachable)
	if reachable.Absence != observation.StateCollectionFailed {
		t.Errorf("resolume.reachable Absence = %q, want collection_failed", reachable.Absence)
	}
	if reachable.Reason != "decode error" {
		t.Errorf("resolume.reachable Reason = %q, want %q", reachable.Reason, "decode error")
	}
}

// TestPollNeverRequestsComposition proves the D-1/D-2 boundary at the
// wire level: Poll must issue exactly one request, to /product, and never
// touch /composition — composition semantics are out of scope for this
// seam's Collector (see this package's doc comment: GET /composition is
// forbidden outright, not merely out of scope for Poll specifically).
func TestPollNeverRequestsComposition(t *testing.T) {
	requested := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested[r.URL.Path]++
		w.Write(loadTestdata(t, "product.json"))
	}))
	defer srv.Close()

	now := time.Now()
	c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})
	c.Poll(context.Background())

	if requested["/api/v1/composition"] != 0 {
		t.Errorf("Poll() requested /composition %d time(s); this Collector must never read composition state", requested["/api/v1/composition"])
	}
	if requested["/api/v1/product"] != 1 {
		t.Errorf("Poll() requested /product %d time(s), want exactly 1", requested["/api/v1/product"])
	}
}

// TestPollNeverEmitsAnySignalOtherThanTheTwo guards seam D-1's boundary
// from the observation side: only resolume.reachable and resolume.product
// may ever appear, on any code path.
func TestPollNeverEmitsAnySignalOtherThanTheTwo(t *testing.T) {
	allowed := map[observation.SignalID]bool{
		SignalReachable: true,
		SignalProduct:   true,
	}

	run := func(name string, srv *httptest.Server) {
		t.Run(name, func(t *testing.T) {
			defer srv.Close()
			now := time.Now()
			c := newTestCollector(t, srv.URL, Options{Now: fixedClock(&now)})
			obs, _ := c.Poll(context.Background())
			for _, o := range obs {
				if !allowed[o.Signal] {
					t.Errorf("Poll() emitted signal %q; seam D-1 may only ever emit resolume.reachable and resolume.product", o.Signal)
				}
			}
		})
	}

	run("success", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(loadTestdata(t, "product.json"))
	})))
	run("failure", httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
}
