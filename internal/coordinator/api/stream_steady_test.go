package api

import (
	"context"
	"testing"
	"time"

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

	audioValue any
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
		Observations: []observation.Observation{streamObs(f.t, "idle", at, 45*time.Second), ceiling},
		LastPollAt:   &at,
	}})
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

// TestHubSteadyStateBroadcastsOnlyRealChanges runs the hub through a minute
// of re-confirmed but unchanged node, audio and FPP evidence and requires
// silence after the first frames, then a frame for a real value change and
// for evidence that stops being re-confirmed.
func TestHubSteadyStateBroadcastsOnlyRealChanges(t *testing.T) {
	clock := &syncClock{t: testNow}
	fleet := &steadyFleet{t: t, nodes: &fakeNodeLister{}, audio: &fakeNodeAudioLister{}, fpp: &mutableFPPLister{}, audioValue: "running"}
	fleet.report(clock.now())

	api := New(Dependencies{
		Nodes: fleet.nodes, Audio: fleet.audio, FPP: fleet.fpp, Observations: &fakeObservationLister{},
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
	if got := drainFrameKinds(full); got["node.changed"] != 1 || got["fpp.changed"] != 1 {
		t.Fatalf("first render to a full-frame subscriber = %v, want one node.changed and one fpp.changed", got)
	}
	if got := drainFrameKinds(deltas); got["node.changed"] != 1 || got["fpp.changed"] != 1 {
		t.Fatalf("first render to a delta subscriber = %v, want one node.changed and one fpp.changed", got)
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

	clock.advance(time.Minute)
	hub.render(ctx)
	if got := drainFrameKinds(full); got["node.changed"] != 1 || got["fpp.changed"] != 1 {
		t.Errorf("evidence that stopped being re-confirmed sent %v, want node.changed and fpp.changed as it goes stale", got)
	}
}
