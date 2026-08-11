package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// openTestStore is this package's own minimal store fixture — apiwiring.go
// had no tests at all before Step 3 review finding 4.5 noted that, and the
// three fixes this file exercises (findings 3.4, 3.7, 3.8) all live at this
// exact wiring seam, so a fake dependency in internal/coordinator/api's own
// test suite would prove nothing about them: they only exist where a real
// *store.Store meets a real *inventory.Manager or real config.FPPEndpoints.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func mustEnvelopeBytes(t *testing.T, env mqttproto.Envelope) []byte {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

// TestLivenessObservingNodeListerRecordsStalenessOnlyTransition is the
// wiring-level half of Step 3 review finding 3.4 (the unit-level half
// lives in internal/coordinator/inventory's own test suite): a node
// brought online by real MQTT messages, then never heard from again, must
// still have its eventual online -> unknown/offline transition — driven by
// nothing but the passage of time — recorded to event history the moment
// something calls Snapshot with a later now, exactly as api.Hub's own
// fixed render tick does in production. No second message is sent
// anywhere in this test; if this passes, the transition can only have
// been detected via [livenessObservingNodeLister], not via
// HandleMessage's message-arrival path.
func TestLivenessObservingNodeListerRecordsStalenessOnlyTransition(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	mgr := inventory.New(st, nil)
	lister := livenessObservingNodeLister{inv: mgr}

	lwtEnv, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	lwtTopic, err := mqttproto.LWTTopic("node-a")
	if err != nil {
		t.Fatalf("lwt topic: %v", err)
	}
	mgr.HandleMessage(broker.Message{Topic: lwtTopic, Payload: mustEnvelopeBytes(t, lwtEnv), Retained: false})

	healthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{BootID: "boot-1", Sequence: 1, AgentState: "running"})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	healthTopic, err := mqttproto.ObservedTopic("node-a", "health")
	if err != nil {
		t.Fatalf("health topic: %v", err)
	}
	mgr.HandleMessage(broker.Message{Topic: healthTopic, Payload: mustEnvelopeBytes(t, healthEnv), Retained: false})

	now := time.Now()
	views, err := lister.Snapshot(ctx, now)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var before inventory.NodeView
	found := false
	for _, v := range views {
		if v.NodeID == "node-a" {
			before, found = v, true
		}
	}
	if !found || before.Liveness != inventory.LivenessOnline {
		t.Fatalf("node-a liveness = %+v, want online before continuing", before)
	}

	eventsBefore, _, err := st.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list events before: %v", err)
	}

	// No further message is ever sent for node-a. Re-derive liveness
	// against a "now" well past inventory.StalenessWindow, exactly the way
	// a hub render tick would some time later — the health heartbeat above
	// ages out and liveness moves off "online" with nothing but time having
	// passed.
	future := now.Add(24 * time.Hour)
	views, err = lister.Snapshot(ctx, future)
	if err != nil {
		t.Fatalf("snapshot at future now: %v", err)
	}
	var after inventory.NodeView
	for _, v := range views {
		if v.NodeID == "node-a" {
			after = v
		}
	}
	if after.Liveness == inventory.LivenessOnline {
		t.Fatalf("node-a liveness = %q at now+24h, want it to have aged off online (StalenessWindow = %s)", after.Liveness, inventory.StalenessWindow)
	}

	eventsAfter, _, err := st.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list events after: %v", err)
	}
	if len(eventsAfter) != len(eventsBefore)+1 {
		t.Fatalf("events after the staleness-only Snapshot call = %d, want %d (exactly one new control-plane transition, with zero further MQTT messages)",
			len(eventsAfter), len(eventsBefore)+1)
	}
	last := eventsAfter[len(eventsAfter)-1]
	if last.Category != "control_plane" {
		t.Errorf("last event Category = %q, want \"control_plane\"", last.Category)
	}

	// A second call with the same, still-not-online now must not record a
	// duplicate: the wrapper shares inventory.Manager's own
	// once-per-actual-transition dedup.
	if _, err := lister.Snapshot(ctx, future); err != nil {
		t.Fatalf("second snapshot at the same future now: %v", err)
	}
	eventsAfterRepeat, _, err := st.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list events after repeat: %v", err)
	}
	if len(eventsAfterRepeat) != len(eventsAfter) {
		t.Errorf("events after a repeated, unchanged Snapshot call = %d, want unchanged at %d", len(eventsAfterRepeat), len(eventsAfter))
	}
}

// TestFPPInstanceListerSynthesizesNotYetPolledObservations is Step 3
// review finding 3.8's regression guard: a configured FPP instance the
// store holds nothing for yet (no poll has ever completed) must render one
// not_collected [observation.Observation] per signal the FPP collector can
// produce — never a bare empty Observations list, which a UI would show as
// a blank panel indistinguishable from "this instance supports nothing" —
// and LastPollAt/LastPollError must still read as "no poll yet" (nil), not
// be fooled by the synthesized placeholders' own CollectedAt.
func TestFPPInstanceListerSynthesizesNotYetPolledObservations(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	lister := fppInstanceLister{st: st, endpoints: []config.FPPEndpoint{{ID: "player-01", URL: "http://10.0.1.20"}}}

	views, err := lister.ListInstances(ctx)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	v := views[0]
	if v.LastPollAt != nil {
		t.Errorf("LastPollAt = %v, want nil: no poll has ever completed", v.LastPollAt)
	}
	if v.LastPollError != nil {
		t.Errorf("LastPollError = %v, want nil", v.LastPollError)
	}
	if len(v.Observations) != len(fppSignals) {
		t.Fatalf("len(Observations) = %d, want %d (one per fppSignals entry)", len(v.Observations), len(fppSignals))
	}
	for _, o := range v.Observations {
		if o.Absence != observation.StateNotCollected {
			t.Errorf("observation %q Absence = %q, want %q", o.Signal, o.Absence, observation.StateNotCollected)
		}
		if o.Resource.Kind != observation.ResourceFPP || o.Resource.ID != "player-01" {
			t.Errorf("observation %q Resource = %+v, want {fpp player-01}", o.Signal, o.Resource)
		}
	}
}

// TestFPPInstanceListerUsesRealObservationsWhenPresent proves the
// synthesized not-yet-polled placeholders in
// TestFPPInstanceListerSynthesizesNotYetPolledObservations only ever
// appear before a real poll has landed anything: once the store holds even
// one real observation for an instance, ListInstances must return exactly
// that, never padded out with fppSignals placeholders alongside it.
func TestFPPInstanceListerUsesRealObservationsWhenPresent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	lister := fppInstanceLister{st: st, endpoints: []config.FPPEndpoint{{ID: "player-01", URL: "http://10.0.1.20"}}}

	pollAt := time.Now()
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: "player-01"}
	obs, err := observation.Measured(res, "fpp.multisync.enabled", false, pollAt, observation.WithSource("fpp-rest"), observation.WithCollectedAt(pollAt))
	if err != nil {
		t.Fatalf("build observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, obs); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	views, err := lister.ListInstances(ctx)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	v := views[0]
	if len(v.Observations) != 1 {
		t.Fatalf("len(Observations) = %d, want exactly 1 (the real polled row, no synthesized placeholders alongside it)", len(v.Observations))
	}
	if v.LastPollAt == nil || !v.LastPollAt.Equal(pollAt) {
		t.Errorf("LastPollAt = %v, want %v", v.LastPollAt, pollAt)
	}
}

// TestFPPCollectorStatusListerHasStableID is Step 3 review finding 3.7's
// regression guard: the FPP REST collector's own "collectors" entry must
// report the same id, [fppCollectorSourceID], regardless of how many
// instances happen to be configured — never one row per endpoint, which
// gave the collector no single, stable identity at all — and its State
// must be drawn from [api.CollectorRunState]'s closed vocabulary, never
// the evidence-absence vocabulary ([observation.State]) a previous version
// of this code borrowed for the zero-endpoints case.
func TestFPPCollectorStatusListerHasStableID(t *testing.T) {
	zero := fppCollectorStatusLister{}
	states, err := zero.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("collector statuses (zero endpoints): %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want exactly 1", len(states))
	}
	if states[0].ID != fppCollectorSourceID {
		t.Errorf("ID = %q, want %q", states[0].ID, fppCollectorSourceID)
	}
	if states[0].State != string(api.CollectorNotConfigured) {
		t.Errorf("State = %q, want %q", states[0].State, api.CollectorNotConfigured)
	}
	if states[0].Reason == nil {
		t.Errorf("Reason = nil, want a non-null reason naming why nothing is configured")
	}

	many := fppCollectorStatusLister{endpoints: []config.FPPEndpoint{{ID: "a", URL: "http://a"}, {ID: "b", URL: "http://b"}}}
	states, err = many.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("collector statuses (two endpoints): %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want exactly 1 regardless of endpoint count, want the same single, stable id whether 0, 1, or many endpoints are configured", len(states))
	}
	if states[0].ID != fppCollectorSourceID {
		t.Errorf("ID = %q, want %q (same id as the zero-endpoints case)", states[0].ID, fppCollectorSourceID)
	}
	if states[0].State != string(api.CollectorRunning) {
		t.Errorf("State = %q, want %q", states[0].State, api.CollectorRunning)
	}
	if states[0].Reason != nil {
		t.Errorf("Reason = %v, want nil while running", states[0].Reason)
	}
}
