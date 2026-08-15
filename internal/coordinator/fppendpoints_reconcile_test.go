package coordinator

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// stubCollector is a Collector that polls into the void. These tests are
// about which collectors the Runner holds, never about what they produce.
type stubCollector struct{ id string }

func (s stubCollector) ID() string { return s.id }

func (s stubCollector) Poll(context.Context) ([]observation.Observation, bool) {
	return nil, true
}

// discardSink satisfies collector.Sink without recording anything.
type discardSink struct{}

func (discardSink) RecordObservations(context.Context, []observation.Observation, bool) {}

// mutableFPPEndpoints is an fppEndpointProvider whose answer a test can
// change between reconcile passes, which is the whole point of the loop
// under test.
type mutableFPPEndpoints struct {
	mu        sync.Mutex
	endpoints []config.FPPEndpoint
	reads     int
}

func (m *mutableFPPEndpoints) Current(context.Context) []config.FPPEndpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	return append([]config.FPPEndpoint(nil), m.endpoints...)
}

func (m *mutableFPPEndpoints) set(endpoints []config.FPPEndpoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = endpoints
}

// waitForSeed blocks until the loop has read the endpoint list at least
// once. Without this a test that mutates the list first can win the race
// against the loop's own seeding pass, so the loop never adopts the
// endpoint and the removal it is supposed to perform never happens.
func (m *mutableFPPEndpoints) waitForSeed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		seeded := m.reads > 0
		m.mu.Unlock()
		if seeded {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the reconcile loop never read the endpoint list")
}

func runnerIDSet(r *collector.Runner) map[string]bool {
	set := map[string]bool{}
	for _, id := range r.IDs() {
		set[id] = true
	}
	return set
}

// waitForRunnerIDs polls the Runner until it holds exactly want, or fails.
// The reconcile loop is driven by its own ticker, so a test observes its
// effect rather than calling a pass directly.
func waitForRunnerIDs(t *testing.T, r *collector.Runner, want map[string]bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var got map[string]bool
	for time.Now().Before(deadline) {
		got = runnerIDSet(r)
		if len(got) == len(want) {
			match := true
			for id := range want {
				if !got[id] {
					match = false
					break
				}
			}
			if match {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("runner never reached the expected collector set: got %v, want %v", got, want)
}

// TestReconcileFPPCollectorsLeavesForeignCollectorsAlone is the regression
// guard for a collector another subsystem owns being torn down by this
// loop. The FPP MQTT collector and the Resolume collector are both
// registered on the SAME Runner this loop reconciles, and neither is an
// fpp.endpoints entry, so an implementation that removed every id not in
// the desired set would stop both.
func TestReconcileFPPCollectorsLeavesForeignCollectorsAlone(t *testing.T) {
	runner := collector.NewRunner(discardSink{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Two collectors this loop must never touch: neither is in
	// fpp.endpoints, and both really do share this Runner in coordinator.go.
	runner.Add(stubCollector{id: "fpp-mqtt"}, time.Hour)
	runner.Add(stubCollector{id: "resolume"}, time.Hour)

	source := &mutableFPPEndpoints{endpoints: []config.FPPEndpoint{
		{ID: "bench-fpp", URL: "http://127.0.0.1:8090"},
	}}
	runner.Add(stubCollector{id: "bench-fpp"}, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go reconcileFPPCollectors(ctx, runner, source,
		func(id, _ string) (collector.Collector, error) { return stubCollector{id: id}, nil },
		time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Remove the only real endpoint. Its collector must go; the other two
	// must survive every subsequent pass.
	source.waitForSeed(t)
	source.set(nil)
	waitForRunnerIDs(t, runner, map[string]bool{"fpp-mqtt": true, "resolume": true})

	// Let several more passes run against an empty desired set.
	time.Sleep(50 * time.Millisecond)
	if got := runnerIDSet(runner); !got["fpp-mqtt"] || !got["resolume"] {
		t.Fatalf("a collector this loop does not own was removed: %v", got)
	}

	// Adding an endpoint back must not disturb them either.
	source.set([]config.FPPEndpoint{{ID: "bench-fpp", URL: "http://127.0.0.1:8090"}})
	waitForRunnerIDs(t, runner, map[string]bool{"fpp-mqtt": true, "resolume": true, "bench-fpp": true})
}

// TestReservedCollectorIDsMatchTheRealRegistrations ties
// internal/coordinator/config's reserved-id list to the id this package
// actually registers the FPP MQTT collector under. The config package
// holds the id as a literal rather than importing the collector package,
// so without this test the two could drift apart silently and the
// reserved-id refusal would start guarding a name nothing uses.
func TestReservedCollectorIDsMatchTheRealRegistrations(t *testing.T) {
	err := config.ValidateFPPEndpoints([]config.FPPEndpoint{
		{ID: fppMQTTCollectorSourceID, URL: "http://127.0.0.1:80"},
	})
	if err == nil {
		t.Fatalf("an FPP endpoint claiming the FPP MQTT collector's own id (%q) was accepted; "+
			"config.reservedCollectorIDs and this package's registration have drifted apart",
			fppMQTTCollectorSourceID)
	}
}
