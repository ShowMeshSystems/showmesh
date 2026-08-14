package coordinator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// resolumeProductHandler serves a fixed, well-formed GET /api/v1/product
// response — the exact shape resolume.Client.Product decodes (see
// internal/coordinator/collector/resolume/client.go) — and 404s everything
// else, since this seam's own Collector never calls anything but /product
// (see resolume.Collector.Poll's own doc comment: composition semantics are
// seam D-2, out of scope here).
func resolumeProductHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/product" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Arena","major":7,"minor":23,"micro":2,"revision":51094}`))
	}
}

// waitForObservation polls st for a resolume.reachable=true observation
// under resourceID, or fails the test after d — the same bounded-retry
// shape internal/coordinator/api/stream_test.go's own goroutine-baseline
// and subscriber-count waits already use in this codebase, applied here
// because this test drives a real collector.Runner goroutine on its own
// timer rather than calling Poll synchronously (see this test's own
// comment for why that is the point).
func waitForObservation(t *testing.T, st *store.Store, resourceID string, sig observation.SignalID, d time.Duration) []observation.Observation {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(d)
	var last []observation.Observation
	for time.Now().Before(deadline) {
		obs, err := st.ListObservations(ctx, store.ObservationFilter{
			ResourceKind: observation.ResourceResolume, ResourceID: resourceID, Signal: sig,
		})
		if err != nil {
			t.Fatalf("list observations: %v", err)
		}
		if len(obs) > 0 {
			return obs
		}
		last = obs
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("signal %q for resolume/%s did not appear within %s; last read: %v", sig, resourceID, d, last)
	return last
}

// TestResolumeWiringSurfacesReachableObservation is this task's own named
// deliverable: with SHOWMESH_RESOLUME_URL pointed at an httptest.Server
// serving a real /api/v1/product response, driven through the actual
// coordinator wiring (newResolumeWiring, the shared collector.Runner, and
// *fppSink — the same three seams coordinator.go's Run wires together, not
// a hand-built substitute for any of them), the coordinator's observations
// surface (here, *store.Store — what api.Dependencies.Observations reads
// from via storeObservationLister, see apiwiring.go) reports
// resolume.reachable = true for the configured Resolume id.
//
// This is the wiring-level half; internal/coordinator/collector/resolume's
// own test suite already covers Collector.Poll's decoding and error
// handling in isolation. What only a test at this seam can prove is that
// newResolumeWiring's Add call actually lands the collector on a Runner
// that runs it, and that the generic *fppSink this file's own doc comment
// says is "reused across collector sources" really does persist a
// Resolume observation despite its FPP-suggesting name — an assumption
// worth checking with a real *store.Store rather than trusting the doc
// comment.
//
// coordinator.go's top-level Run is not called here: it loads config from
// the real process environment, installs OS signal handlers, and blocks
// until one arrives, none of which this test wants. Driving
// newResolumeWiring + collector.Runner + *fppSink + *store.Store directly
// is as far as this package's existing wiring-test harness
// (apiwiring_test.go) already reaches for the FPP collector's own
// equivalent (TestFPPSinkRealPortFixturesPruneGhostRowsOnEmptyDelivery) —
// this test follows that same ceiling rather than contorting a new harness
// to invoke Run itself.
func TestResolumeWiringSurfacesReachableObservation(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	// Every other field left at Go zero values: this test exercises
	// newResolumeWiring in isolation, not full config.Validate() (which
	// requires an HTTP addr, MQTT broker, and log level this test has no
	// use for) — config's own test suite already covers Validate().
	cfg := config.Config{ResolumeURL: srv.URL, ResolumeID: "resolume-test"}
	if err := config.ValidateResolumeIDAgainstFPPEndpoints(cfg.ResolumeID, cfg.FPPEndpoints); err != nil {
		t.Fatalf("ValidateResolumeIDAgainstFPPEndpoints: %v", err)
	}

	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())

	wire, err := newResolumeWiring(cfg, runner, testLogger())
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.watcher == nil {
		t.Fatalf("wire.watcher = nil, want a constructed *resolume.Watcher when ResolumeURL is set")
	}
	if wire.adapter == nil {
		t.Fatalf("wire.adapter = nil, want a constructed *resolume.Adapter when ResolumeURL is set (review finding A: coordinator.go must have something to run alongside the watcher)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// runner.loop polls immediately on Run's own start (collector.go's own
	// doc comment: "a freshly-started collector's evidence appears without
	// waiting a full interval"), so no sleep is needed before the poll
	// this test waits on — only before the OBSERVATION becomes visible in
	// the store, which is what waitForObservation's retry loop is for.
	go runner.Run(ctx)
	// The watcher's own WebSocket connection is exercised by
	// internal/coordinator/collector/resolume's own test suite
	// (watch_test.go); this test's srv serves REST only, so the watcher
	// would sit in its dial-error backoff loop harmlessly for the
	// duration of this test — started anyway so this test proves what
	// coordinator.go's real Run does: all three goroutines alive together,
	// none blocking the others. wire.adapter.Run is the goroutine review
	// finding A added — coordinator.go's real Run starts it alongside
	// wire.watcher.Run on the identical backgroundWG, and this test
	// follows suit rather than leaving it unexercised at this seam.
	go wire.watcher.Run(ctx)
	go wire.adapter.Run(ctx)

	obs := waitForObservation(t, st, "resolume-test", "resolume.reachable", 5*time.Second)
	if len(obs) != 1 {
		t.Fatalf("resolume.reachable observations = %d, want exactly 1", len(obs))
	}
	if state := obs[0].StateAt(time.Now()); state != observation.StateCurrent {
		t.Errorf("resolume.reachable state = %q, want %q", state, observation.StateCurrent)
	}
	if v, ok := obs[0].Value.(bool); !ok || !v {
		t.Errorf("resolume.reachable value = %#v, want true", obs[0].Value)
	}

	// The Track D seam D-1 rule this whole file exists to guard: a
	// parameter id must never reach anything this test can see, and the
	// most likely place one would leak by accident is exactly here — a
	// stray %v of a resolume.ParameterID landing in an observation's
	// Value or Reason. Neither collector signal this seam produces
	// (resolume.reachable, resolume.product) is parameter-derived, but
	// this assertion is cheap insurance against that ever changing
	// silently.
	for _, o := range obs {
		if s, ok := o.Value.(string); ok && strings.Contains(strings.ToLower(s), "parameterid") {
			t.Errorf("observation value looks like it leaked a ParameterID: %q", s)
		}
	}
}

// TestResolumeWiringDisabledWhenURLUnset proves the feature-flag half:
// with ResolumeURL empty, newResolumeWiring returns a nil watcher and
// registers nothing on runner — no goroutine, no observation, ever, for a
// Resolume instance nothing configured.
func TestResolumeWiringDisabledWhenURLUnset(t *testing.T) {
	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())

	wire, err := newResolumeWiring(config.Config{}, runner, testLogger())
	if err != nil {
		t.Fatalf("newResolumeWiring: %v", err)
	}
	if wire.watcher != nil {
		t.Errorf("wire.watcher = %v, want nil when ResolumeURL is unset", wire.watcher)
	}
	if wire.adapter != nil {
		t.Errorf("wire.adapter = %v, want nil when ResolumeURL is unset: an unconfigured Resolume collector must contribute no goroutine at all, adapter included", wire.adapter)
	}

	statuses, err := wire.status.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "not_configured" {
		t.Errorf("CollectorStatuses = %+v, want exactly one not_configured entry", statuses)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go runner.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	cancel()

	obs, err := st.ListObservations(context.Background(), store.ObservationFilter{ResourceKind: observation.ResourceResolume})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(obs) != 0 {
		t.Errorf("observations = %v, want none for resource kind resolume when the collector was never configured", obs)
	}
}
