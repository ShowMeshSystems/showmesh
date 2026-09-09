package fppplugin

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fixedResolver resolves every instanceUUID to a single fixed endpoint id
// (or refuses to resolve at all), mirroring a plugin instance whose uuid
// is unambiguously owned by exactly one configured fpp.endpoints entry.
type fixedResolver struct {
	endpointID string
	resolve    bool
}

func (r fixedResolver) ResolveEndpointID(context.Context, string) (string, bool) {
	return r.endpointID, r.resolve
}

func findObservation(t *testing.T, obs []observation.Observation, sig observation.SignalID) (observation.Observation, bool) {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o, true
		}
	}
	return observation.Observation{}, false
}

// TestObserveStartOrPlayingProducesPlayingStatus pins the plain case:
// action "start" or "playing" derives fpp.status "playing".
func TestObserveStartOrPlayingProducesPlayingStatus(t *testing.T) {
	for _, action := range []string{"start", "playing"} {
		t.Run(action, func(t *testing.T) {
			c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
			c.Observe("uuid-1", action, "Halloween Main", "", time.Unix(1000, 0))

			obs, complete := c.Poll(context.Background())
			if !complete {
				t.Fatalf("Poll: complete = false, want true")
			}
			status, ok := findObservation(t, obs, fpp.SignalStatus)
			if !ok {
				t.Fatalf("no fpp.status observation produced")
			}
			if status.Value != "playing" {
				t.Errorf("fpp.status = %v, want %q", status.Value, "playing")
			}
		})
	}
}

// TestObserveStopProducesIdleStatus is the ruling's own required case: a
// "stop" action must produce a state the launch decision reads as
// not-busy, which is fpp.status == "idle" (nightShowLaunchIfBusy's own
// literal comparison).
func TestObserveStopProducesIdleStatus(t *testing.T) {
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
	c.Observe("uuid-1", "playing", "Halloween Main", "", time.Unix(1000, 0))
	c.Observe("uuid-1", "stop", "Halloween Main", "", time.Unix(1001, 0))

	obs, _ := c.Poll(context.Background())
	status, ok := findObservation(t, obs, fpp.SignalStatus)
	if !ok {
		t.Fatalf("no fpp.status observation produced")
	}
	if status.Value != "idle" {
		t.Errorf("fpp.status = %v, want %q", status.Value, "idle")
	}
}

// TestObserveQueryNextNeverProducesPlaying is the ruling's other required
// case: an instance whose only evidence ever received is "query_next"
// must not be read as playing. A derivation that treated any non-stop
// action as "playing" would pass every other test in this file and still
// be wrong exactly here.
func TestObserveQueryNextNeverProducesPlaying(t *testing.T) {
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
	c.Observe("uuid-1", "query_next", "Halloween Main", "", time.Unix(1000, 0))

	obs, _ := c.Poll(context.Background())
	if status, ok := findObservation(t, obs, fpp.SignalStatus); ok {
		t.Errorf("fpp.status = %v, want no status observation at all from query_next alone", status.Value)
	}
	// playlistName is still corroborating evidence and should still be
	// recorded even though no status claim was made.
	name, ok := findObservation(t, obs, fpp.SignalPlaylistName)
	if !ok || name.Value != "Halloween Main" {
		t.Errorf("fpp.playlist.name = %v, %v, want %q, true", name.Value, ok, "Halloween Main")
	}
}

// TestObserveQueryNextDoesNotResetAnEstablishedStatus: once playing is
// established, a later query_next or unknown tick must carry it forward
// unchanged, not clear it.
func TestObserveQueryNextDoesNotResetAnEstablishedStatus(t *testing.T) {
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
	c.Observe("uuid-1", "playing", "Halloween Main", "", time.Unix(1000, 0))
	c.Observe("uuid-1", "query_next", "Halloween Main", "", time.Unix(1001, 0))
	c.Observe("uuid-1", "unknown", "Halloween Main", "", time.Unix(1002, 0))

	obs, _ := c.Poll(context.Background())
	status, ok := findObservation(t, obs, fpp.SignalStatus)
	if !ok || status.Value != "playing" {
		t.Errorf("fpp.status = %v, %v, want %q, true", status.Value, ok, "playing")
	}
}

// TestObserveUnavailableNeverUpdatesPlaylistName: contract §1.2 forbids
// playlistName on an unavailable observation, so Observe must not update
// its own state from one.
func TestObserveUnavailableNeverUpdatesPlaylistName(t *testing.T) {
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
	c.Observe("uuid-1", "playing", "Halloween Main", "", time.Unix(1000, 0))
	c.Observe("uuid-1", "playing", "", "missing_definition", time.Unix(1001, 0))

	obs, _ := c.Poll(context.Background())
	name, ok := findObservation(t, obs, fpp.SignalPlaylistName)
	if !ok || name.Value != "Halloween Main" {
		t.Errorf("fpp.playlist.name = %v, %v, want the last known %q to survive an unavailable observation", name.Value, ok, "Halloween Main")
	}
}

// TestPollUnresolvableInstanceProducesNoObservation: an instanceUUID this
// collector holds state for, but that no single configured endpoint
// currently claims, must not be guessed at.
func TestPollUnresolvableInstanceProducesNoObservation(t *testing.T) {
	c := New(fixedResolver{resolve: false}, nil)
	c.Observe("uuid-1", "playing", "Halloween Main", "", time.Unix(1000, 0))

	obs, complete := c.Poll(context.Background())
	if !complete {
		t.Errorf("complete = false, want true")
	}
	if len(obs) != 0 {
		t.Errorf("Poll produced %d observations for an unresolvable instance, want 0: %+v", len(obs), obs)
	}
}

// TestObserveStampsCoordinatorClockNeverPluginTime pins the corrected
// owner ruling: ObservedAt and CollectedAt both come from the argument
// Observe is called with (the coordinator's own clock at acceptance),
// never anything derived from the plugin's own observedAtMillis, which
// Observe is not even given.
func TestObserveStampsCoordinatorClockNeverPluginTime(t *testing.T) {
	acceptedAt := time.Unix(5000, 0).UTC()
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil)
	c.Observe("uuid-1", "playing", "Halloween Main", "", acceptedAt)

	obs, _ := c.Poll(context.Background())
	status, ok := findObservation(t, obs, fpp.SignalStatus)
	if !ok {
		t.Fatalf("no fpp.status observation produced")
	}
	if status.ObservedAt == nil || !status.ObservedAt.Equal(acceptedAt) {
		t.Errorf("ObservedAt = %v, want %v", status.ObservedAt, acceptedAt)
	}
	if !status.CollectedAt.Equal(acceptedAt) {
		t.Errorf("CollectedAt = %v, want %v", status.CollectedAt, acceptedAt)
	}
}

// TestValidForMatchesRESTCollectorDefault pins the owner's 2026-09-09
// ruling: this collector's ValidFor is fpp.DefaultValidFor (45s), a
// deliberate reuse of the REST collector's own bound rather than a number
// derived from any measured or guessed plugin tick cadence.
func TestValidForMatchesRESTCollectorDefault(t *testing.T) {
	if DefaultValidFor != fpp.DefaultValidFor {
		t.Errorf("DefaultValidFor = %v, want fpp.DefaultValidFor = %v", DefaultValidFor, fpp.DefaultValidFor)
	}
}

// fakeSink records every RecordObservations call, so the latency test
// below can measure the wall-clock gap between Observe and delivery.
type fakeSink struct {
	delivered chan []observation.Observation
}

func (s *fakeSink) RecordObservations(_ context.Context, obs []observation.Observation, _ bool) {
	if len(obs) == 0 {
		return
	}
	select {
	case s.delivered <- obs:
	default:
	}
}

// TestPushDeliversWithinOneSecondNotViaOrdinaryPoll is this package's own
// end-to-end test, in the shape the brief asks for: a real
// collector.Runner, this collector's real Poll and Observe, a PollInterval
// set deliberately far longer than one second (30s) so a delivery inside
// the assertion window cannot be an ordinary poll tick, and a short
// collector.WithNudgeMinInterval on the collector's OWN dedicated Runner
// — never the value production shares with the FPP command dispatch
// path's 2s nudge floor, which this collector must not inherit.
func TestPushDeliversWithinOneSecondNotViaOrdinaryPoll(t *testing.T) {
	sink := &fakeSink{delivered: make(chan []observation.Observation, 8)}
	c := New(fixedResolver{endpointID: "ep1", resolve: true}, nil, WithPollInterval(30*time.Second))

	runner := collector.NewRunner(sink, slog.New(slog.NewTextHandler(io.Discard, nil)), collector.WithNudgeMinInterval(200*time.Millisecond))
	runner.Add(c, c.PollInterval())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runner.Run(ctx)
		close(done)
	}()

	// The Runner's own immediate first poll (loop's documented behavior)
	// fires before any Observe below and finds nothing, which is fine and
	// unrelated to what this test measures.
	<-time.After(50 * time.Millisecond)

	pushAt := time.Now()
	c.Observe("uuid-1", "playing", "Halloween Main", "", pushAt)
	if ok := runner.Nudge(CollectorID); !ok {
		t.Fatalf("Nudge(%q) = false, want true (this is the very first nudge for this id)", CollectorID)
	}

	select {
	case obs := <-sink.delivered:
		latency := time.Since(pushAt)
		if latency > time.Second {
			t.Errorf("push-to-store latency = %s, want < 1s", latency)
		}
		t.Logf("push-to-store latency: %s", latency)
		if _, ok := findObservation(t, obs, fpp.SignalStatus); !ok {
			t.Errorf("delivered observations did not include fpp.status: %+v", obs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery within 2s of Observe+Nudge; the push path is not reaching the sink")
	}

	cancel()
	<-done
}
