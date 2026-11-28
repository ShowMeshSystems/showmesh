package api

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// steadyFleet re-stamps every observation at each report, as the live agents
// and collectors do, while the values themselves never change.
type steadyFleet struct {
	t     *testing.T
	nodes *fakeNodeLister
	audio *fakeNodeAudioLister
	fpp   *mutableFPPLister
	runs  *mutableCurrentRuns

	audioValue any
	uptime     int
	runStatus  string
}

type mutableCurrentRuns struct {
	mu       sync.Mutex
	snapshot currentrun.Snapshot
}

func (f *mutableCurrentRuns) Snapshot(context.Context, time.Time) (currentrun.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}

func (f *steadyFleet) report(at time.Time) {
	f.t.Helper()
	node := onlineNodeFixture(f.t)
	node.Health.ObservedAt = &at
	f.nodes.setViews([]inventory.NodeView{node})

	nodeRes := observation.ResourceRef{Kind: observation.ResourceNode, ID: node.NodeID}
	sessionRes := observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: "session-1"}
	engine := mustSteadyObs(f.t)(observation.Measured(nodeRes, "node.audio.engine.state", f.audioValue, at,
		observation.WithSource("node-audio:"+node.NodeID), observation.WithValidFor(45*time.Second), observation.WithCollectedAt(at)))
	idle := mustSteadyObs(f.t)(observation.NotApplicable(sessionRes, "audio_session.position_ms", "Nothing is playing.", at,
		observation.WithSource("node-audio:"+node.NodeID), observation.WithValidFor(45*time.Second), observation.WithCollectedAt(at)))
	f.audio.setObservations(node.NodeID, []observation.Observation{engine, idle})

	fppRes := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}
	ceiling := mustSteadyObs(f.t)(observation.NotApplicable(fppRes, "fpp.brightness.ceiling", "No ceiling is set.", at,
		observation.WithSource("fpp-brightness"), observation.WithValidFor(45*time.Second), observation.WithCollectedAt(at)))
	f.fpp.setViews([]FPPInstanceView{{
		InstanceID: "player-01", Endpoint: "http://10.0.1.20",
		Observations:         []observation.Observation{streamObs(f.t, "idle", at, 45*time.Second), ceiling},
		LastPollAt:           &at,
		PluginLastReportedAt: &at,
	}})

	f.uptime++
	uptime := []currentrun.Evidence{{Signal: "fpp.uptime.seconds", Value: f.uptime, State: "current", ObservedAt: &at, CollectedAt: &at, Source: "fpp-mqtt"}}
	f.runs.mu.Lock()
	f.runs.snapshot = currentrun.Snapshot{Runs: []currentrun.Run{{
		ID: "fpp:player-01", Runner: currentrun.RunnerFPP, Status: f.runStatus,
		Playback:  currentrun.Playback{State: f.runStatus, Evidence: uptime},
		Freshness: currentrun.Freshness{State: "current", ObservedAt: &at, CollectedAt: &at},
		Targets:   []currentrun.Target{{Kind: "fpp", ID: "player-01", Evidence: uptime}},
	}}}
	f.runs.mu.Unlock()
}

func mustSteadyObs(t *testing.T) func(observation.Observation, error) observation.Observation {
	return func(o observation.Observation, err error) observation.Observation {
		t.Helper()
		if err != nil {
			t.Fatalf("building fixture observation: %v", err)
		}
		return o
	}
}

func drainFrameKinds(sub *subscriber) map[string]int {
	kinds := map[string]int{}
	for {
		select {
		case pf := <-sub.frames:
			kinds[pf.event]++
		default:
			return kinds
		}
	}
}

// TestHubSteadyStateBroadcastsOnlyRealChanges runs the hub through a minute of
// re-confirmed node, audio, FPP, plugin and current-run evidence and requires
// silence, then frames for real changes and for evidence that goes stale.
func TestHubSteadyStateBroadcastsOnlyRealChanges(t *testing.T) {
	clock := &syncClock{t: testNow}
	fleet := &steadyFleet{
		t: t, nodes: &fakeNodeLister{}, audio: &fakeNodeAudioLister{}, fpp: &mutableFPPLister{}, runs: &mutableCurrentRuns{},
		audioValue: "running", runStatus: "playing",
	}
	fleet.report(clock.now())

	api := New(Dependencies{
		Nodes: fleet.nodes, Audio: fleet.audio, FPP: fleet.fpp, CurrentRuns: fleet.runs, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{
		Clock: clock.now, Logger: testLogger(),
		StreamTickInterval: time.Hour, StreamKeepaliveInterval: time.Hour, StreamSubscriberBuffer: 1024,
	})
	hub := api.Hub
	_, full := hub.subscribe(false, nil)
	_, deltas := hub.subscribe(true, nil)
	ctx := context.Background()

	hub.render(ctx)
	for name, sub := range map[string]*subscriber{"full-frame": full, "delta": deltas} {
		if got := drainFrameKinds(sub); got["node.changed"] != 1 || got["fpp.changed"] != 1 || got["currentRuns.changed"] != 1 {
			t.Fatalf("first render to the %s subscriber = %v, want one node.changed, fpp.changed and currentRuns.changed", name, got)
		}
	}

	for i := 0; i < 30; i++ {
		fleet.report(clock.advance(2 * time.Second))
		hub.render(ctx)
	}
	if got := drainFrameKinds(full); len(got) != 0 {
		t.Errorf("full-frame subscriber received %v over 60s of unchanged state, want nothing", got)
	}
	if got := drainFrameKinds(deltas); len(got) != 0 {
		t.Errorf("delta subscriber received %v over 60s of unchanged state, want nothing", got)
	}

	fleet.audioValue = "failed"
	fleet.report(clock.advance(2 * time.Second))
	hub.render(ctx)
	if got := drainFrameKinds(full); got["node.changed"] != 1 || len(got) != 1 {
		t.Errorf("a real audio value change sent %v, want exactly one node.changed", got)
	}
	drainFrameKinds(deltas)

	fleet.runStatus = "stopped"
	fleet.report(clock.advance(2 * time.Second))
	hub.render(ctx)
	if got := drainFrameKinds(deltas); got["currentRuns.changed"] != 1 || len(got) != 1 {
		t.Errorf("a run stopping sent %v to the delta subscriber, want exactly one currentRuns.changed", got)
	}
	drainFrameKinds(full)

	clock.advance(time.Minute)
	hub.render(ctx)
	if got := drainFrameKinds(full); got["node.changed"] != 1 || got["fpp.changed"] != 1 {
		t.Errorf("evidence that stopped being re-confirmed sent %v, want node.changed and fpp.changed as it goes stale", got)
	}
}

func TestNightSessionDiffProjectionIgnoresOnlyUpdatedAt(t *testing.T) {
	a := v1.NightSessionState{ID: "s-1", State: "preshow", UpdatedAt: "2026-11-27T23:00:00Z"}
	b := a
	b.UpdatedAt = "2026-11-27T23:00:10Z"
	same := func(x, y v1.NightSessionState) bool {
		return reflect.DeepEqual(nightSessionDiffProjection(x), nightSessionDiffProjection(y))
	}
	if !same(a, b) {
		t.Errorf("a rewritten updatedAt alone changed the night session's diff projection")
	}
	b.State = "show"
	if same(a, b) {
		t.Errorf("a state change was hidden by the night session's diff projection")
	}
}

type countingFPPLister struct {
	mu    sync.Mutex
	calls int
}

func (c *countingFPPLister) ListInstances(context.Context) ([]FPPInstanceView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return nil, nil
}

func TestHubRenderPassReadsFPPInstancesOnce(t *testing.T) {
	fpp := &countingFPPLister{}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: fpp, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}
	deps.CurrentRuns = NewCurrentRunsReader(deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger(), StreamTickInterval: time.Hour, StreamKeepaliveInterval: time.Hour})

	api.Hub.render(context.Background())
	api.Hub.render(context.Background())
	if fpp.calls != 2 {
		t.Errorf("two render passes listed FPP instances %d times, want once per pass", fpp.calls)
	}
}
