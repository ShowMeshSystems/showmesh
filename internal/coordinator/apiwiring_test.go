package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/fpp"
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
// review finding 3.8's regression guard, extended by Step 5 contract
// section 5.4: a configured FPP instance the store holds nothing for yet
// (no poll has ever completed, from either collector source) must render
// one not_collected [observation.Observation] per STATIC signal the FPP
// REST collector can produce — never a bare empty Observations list, which
// a UI would show as a blank panel indistinguishable from "this instance
// supports nothing" — and LastPollAt/LastPollError must still read as "no
// poll yet" (nil), not be fooled by the synthesized placeholders' own
// CollectedAt.
//
// The len(v.Observations) == len(fppSignals) assertion below is
// necessarily tautological — notYetPolledObservations is defined as "one
// placeholder per fppSignals entry", so this can never fail on its own —
// and is kept anyway as a cheap regression guard against a future edit
// that makes the two diverge (e.g. a filter added to one but not the
// other). It must NOT be read as proving fppSignals contains "every
// signal this instance will ever report": see
// TestFPPSignalsExcludesDynamicSignalFamilies immediately below for the
// property that actually matters after Step 5 — fppSignals (now
// [fpp.AllSignals] itself) is deliberately restricted to the static
// vocabulary, and per-port/per-sensor signals simply do not exist in the
// API at all until a real poll observes them.
func TestFPPInstanceListerSynthesizesNotYetPolledObservations(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	lister := fppInstanceLister{st: st, endpoints: fixedFPPEndpoints{{ID: "player-01", URL: "http://10.0.1.20"}}}

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

// TestFPPSignalsExcludesDynamicSignalFamilies is the Step 5 contract
// section 5.4 property TestFPPInstanceListerSynthesizesNotYetPolledObservations's
// tautological count cannot prove on its own: fppSignals ([fpp.AllSignals])
// must never contain a per-port (fpp.port.<key>.*) or per-sensor
// (fpp.sensor.<key>.*) signal name, because <key> is discovered only from a
// real poll's response and a not-yet-polled placeholder for a key that
// turns out not to exist on a given instance would be a fabricated signal
// name no real poll could ever match. Breaking fppSignals' doc comment's
// promise (e.g. by hand-adding a literal "fpp.port.port_1.kind" entry)
// must fail this test.
func TestFPPSignalsExcludesDynamicSignalFamilies(t *testing.T) {
	for _, sig := range fppSignals {
		s := string(sig)
		if strings.HasPrefix(s, "fpp.port.") || strings.HasPrefix(s, "fpp.sensor.") {
			t.Errorf("fppSignals contains %q, a dynamic per-port/per-sensor signal name — these cannot be known before a real poll and must never appear in the static not-yet-polled placeholder set", s)
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
	lister := fppInstanceLister{st: st, endpoints: fixedFPPEndpoints{{ID: "player-01", URL: "http://10.0.1.20"}}}

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

	many := fppCollectorStatusLister{endpoints: fixedFPPEndpoints{{ID: "a", URL: "http://a"}, {ID: "b", URL: "http://b"}}}
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

// TestFPPMQTTCollectorStatusListerReflectsConfigured is
// [fppMQTTCollectorStatusLister]'s own regression guard, mirroring
// [TestFPPCollectorStatusListerHasStableID] for the second collector
// source Step 5 adds: not_configured with a non-null reason when
// configured is false, running with a nil reason when it is true, always
// exactly one entry at the fixed id [fppMQTTCollectorSourceID].
func TestFPPMQTTCollectorStatusListerReflectsConfigured(t *testing.T) {
	unconfigured := fppMQTTCollectorStatusLister{configured: false}
	states, err := unconfigured.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("collector statuses (unconfigured): %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want exactly 1", len(states))
	}
	if states[0].ID != fppMQTTCollectorSourceID {
		t.Errorf("ID = %q, want %q", states[0].ID, fppMQTTCollectorSourceID)
	}
	if states[0].State != string(api.CollectorNotConfigured) {
		t.Errorf("State = %q, want %q", states[0].State, api.CollectorNotConfigured)
	}
	if states[0].Reason == nil {
		t.Errorf("Reason = nil, want a non-null reason naming why nothing is configured")
	}

	configured := fppMQTTCollectorStatusLister{configured: true}
	states, err = configured.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("collector statuses (configured): %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("len(states) = %d, want exactly 1", len(states))
	}
	if states[0].ID != fppMQTTCollectorSourceID {
		t.Errorf("ID = %q, want %q", states[0].ID, fppMQTTCollectorSourceID)
	}
	if states[0].State != string(api.CollectorRunning) {
		t.Errorf("State = %q, want %q", states[0].State, api.CollectorRunning)
	}
	if states[0].Reason != nil {
		t.Errorf("Reason = %v, want nil while running", states[0].Reason)
	}
}

// TestMultiCollectorStatusListerConcatenatesBothSources is contract section
// 5.4's "generalize fppCollectorStatusLister... so both collectors appear"
// acceptance criterion, exercised directly against the real type
// coordinator.go wires into api.Dependencies.Collectors: both the REST and
// MQTT collectors' own single-row statuses must appear together, in the
// order they were given, with neither one able to suppress or overwrite
// the other's entry.
func TestMultiCollectorStatusListerConcatenatesBothSources(t *testing.T) {
	lister := multiCollectorStatusLister{
		fppCollectorStatusLister{endpoints: fixedFPPEndpoints{{ID: "player-01", URL: "http://10.0.1.20"}}},
		fppMQTTCollectorStatusLister{configured: true},
	}

	states, err := lister.CollectorStatuses(context.Background())
	if err != nil {
		t.Fatalf("collector statuses: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2 (both collector sources visible, per contract section 5.4)", len(states))
	}

	byID := make(map[string]api.CollectorState, len(states))
	for _, s := range states {
		byID[s.ID] = s
	}
	rest, ok := byID[fppCollectorSourceID]
	if !ok {
		t.Fatalf("no %q entry in the combined collector list", fppCollectorSourceID)
	}
	if rest.State != string(api.CollectorRunning) {
		t.Errorf("%s State = %q, want %q", fppCollectorSourceID, rest.State, api.CollectorRunning)
	}
	mqtt, ok := byID[fppMQTTCollectorSourceID]
	if !ok {
		t.Fatalf("no %q entry in the combined collector list", fppMQTTCollectorSourceID)
	}
	if mqtt.State != string(api.CollectorRunning) {
		t.Errorf("%s State = %q, want %q", fppMQTTCollectorSourceID, mqtt.State, api.CollectorRunning)
	}
}

// TestMultiCollectorStatusListerPropagatesError proves a sub-lister's error
// is not silently swallowed into an empty or partial list — a caller
// (GET /api/v1/snapshot's handler) must be able to tell "a dependency
// failed" apart from "no collectors are configured".
func TestMultiCollectorStatusListerPropagatesError(t *testing.T) {
	lister := multiCollectorStatusLister{
		fppCollectorStatusLister{endpoints: nil},
		failingCollectorStatusLister{},
	}
	if _, err := lister.CollectorStatuses(context.Background()); err == nil {
		t.Fatalf("CollectorStatuses succeeded, want the sub-lister's error propagated")
	}
}

// failingCollectorStatusLister always returns an error; used only by
// TestMultiCollectorStatusListerPropagatesError above.
type failingCollectorStatusLister struct{}

func (failingCollectorStatusLister) CollectorStatuses(context.Context) ([]api.CollectorState, error) {
	return nil, errFailingCollectorStatusLister
}

var errFailingCollectorStatusLister = errors.New("failingCollectorStatusLister: deliberate test failure")

// --- fppSink and the collector/Sink completeness contract ------------------
//
// These tests cover the observation-pruning fix: nothing previously deleted
// a row from the observations table, so a signal that stopped being
// reported (a port removed by a shrunk /api/fppd/ports response, a sensor
// dropped from fppd's config) survived forever, aging to stale and
// rendering in the UI's port grid as a ghost of hardware that no longer
// exists. See internal/coordinator/collector.Collector.Poll's and
// internal/coordinator/collector.Sink's doc comments for the completeness
// contract this fixes it with, and internal/coordinator/store's
// replace_observations_test.go for [store.Store.ReplaceObservations]'s own
// unit coverage of the scoped-delete mechanism these tests exercise
// end-to-end.

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestFPPSinkSkippedPollDeletesNothing is the single most important test in
// this fix, named explicitly in the task that produced it: a skipped or
// backed-off poll (complete=false, per fpp.Collector.Poll's doc comment for
// exactly this case) must never be read as "this source now owns zero
// signals" and prune anything, however tempting an implementation that
// checked only "is observations empty" would be to write. Everything a
// previous, real poll stored must survive completely untouched.
//
// The second call below delivers a NON-EMPTY observations slice alongside
// complete=false (updating fpp.reachable, but omitting the two port
// signals and the count) rather than fpp.Collector's literal nil — this is
// deliberate: [store.Store.ReplaceObservations] is already a no-op on a
// zero-length batch by its own contract (see
// TestReplaceObservationsEmptyBatchIsNoOp in internal/coordinator/store),
// so calling this test's sink with nil observations would pass even if
// RecordObservations ignored complete entirely and routed every call
// through ReplaceObservations — it would just happen to route an empty
// batch, which prunes nothing regardless. Using a real, partial delivery is
// what actually exercises fppSink's own routing decision: complete=false
// must go through per-observation upsert only, never
// ReplaceObservations, however non-empty the batch is.
//
// Mutation-checked: temporarily forcing complete=true unconditionally at
// the top of RecordObservations made this test fail — the two omitted port
// signals were deleted — confirming it actually exercises the complete=false
// routing decision and not merely "some rows exist somewhere."
func TestFPPSinkSkippedPollDeletesNothing(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sink := &fppSink{st: st, logger: testLogger()}

	// A real, complete poll's worth of evidence, exactly as
	// fpp.Collector.Poll would deliver it on a healthy cycle.
	seed := []observation.Observation{
		mustMeasured(t, "remote-01", "fpp.reachable", true),
		mustMeasured(t, "remote-01", "fpp.port.16.current_ma", float64(0)),
		mustMeasured(t, "remote-01", "fpp.port.17.current_ma", float64(0)),
		mustMeasured(t, "remote-01", "fpp.ports.count", int64(2)),
	}
	sink.RecordObservations(ctx, seed, true)

	before, err := st.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "remote-01"})
	if err != nil {
		t.Fatalf("list observations (before): %v", err)
	}
	if len(before) != len(seed) {
		t.Fatalf("seeded %d observations, store holds %d before the skipped poll", len(seed), len(before))
	}

	// The literal shape fpp.Collector.Poll's backoff skip actually returns:
	// zero observations, complete=false. Covered first, cheaply, even
	// though (see doc comment above) it cannot alone catch a routing bug.
	sink.RecordObservations(ctx, nil, false)

	// A non-empty, partial delivery under complete=false — the general
	// Sink contract's shape, and the one that actually stresses the
	// routing logic (see doc comment above). Only fpp.reachable is
	// mentioned; the two port signals and the count are omitted.
	partial := []observation.Observation{
		mustMeasured(t, "remote-01", "fpp.reachable", true),
	}
	sink.RecordObservations(ctx, partial, false)

	after, err := st.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "remote-01"})
	if err != nil {
		t.Fatalf("list observations (after): %v", err)
	}
	if len(after) != len(seed) {
		t.Fatalf("after two skipped/partial polls (complete=false): %d observations remain, want unchanged %d — complete=false must delete NOTHING, however non-empty the delivery", len(after), len(seed))
	}
}

// TestFPPSinkNotifiesOnlyWhenStoreCouldHaveChanged is the regression guard
// for fppSink's notify gate (see its own doc comment): a genuinely empty
// delivery must never poke the hub, because it can never mutate s.st
// either way — and a delivery that DOES prune existing rows (the exact
// case an empty-batch gate would be wrong to skip, if ReplaceObservations
// pruned that way) must still poke it. Reverting the len(observations) > 0
// guard in RecordObservations to an unconditional s.notify() makes this
// test fail on its first assertion (a spurious notify on the empty poll).
func TestFPPSinkNotifiesOnlyWhenStoreCouldHaveChanged(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	var notifyCount int
	sink := &fppSink{st: st, logger: testLogger(), notify: func() { notifyCount++ }}

	// A real, non-empty delivery: must notify.
	seed := []observation.Observation{
		mustMeasured(t, "remote-01", "fpp.reachable", true),
		mustMeasured(t, "remote-01", "fpp.port.16.current_ma", float64(0)),
	}
	sink.RecordObservations(ctx, seed, true)
	if notifyCount != 1 {
		t.Fatalf("notifyCount after a real delivery = %d, want 1", notifyCount)
	}

	// A genuinely empty, complete=true delivery: ReplaceObservations
	// returns immediately on a zero-length batch (see
	// TestReplaceObservationsEmptyBatchIsNoOp in internal/coordinator/store)
	// — nothing is pruned, because there is no (resource, source) key in
	// this call to scope a delete to — so this must not notify either.
	sink.RecordObservations(ctx, nil, true)
	if notifyCount != 1 {
		t.Fatalf("notifyCount after an empty complete=true delivery = %d, want unchanged 1 (nothing could have changed)", notifyCount)
	}

	// An empty, complete=false (backed-off) delivery: same reasoning, the
	// per-observation upsert loop below never runs.
	sink.RecordObservations(ctx, nil, false)
	if notifyCount != 1 {
		t.Fatalf("notifyCount after an empty complete=false delivery = %d, want unchanged 1", notifyCount)
	}

	// A non-empty delivery that OMITS remote-01's port signal (still
	// mentions fpp.reachable, so the resource's key IS present this time)
	// drives a real prune via ReplaceObservations — see
	// TestReplaceObservationsPrunesSignalsNotInDelivery in
	// internal/coordinator/store for the store-level proof. This is the
	// shape a wrongly-scoped "skip on anything empty-ish" gate could get
	// wrong; a plain len(observations) > 0 gate gets it right because the
	// delivery itself is non-empty.
	pruning := []observation.Observation{
		mustMeasured(t, "remote-01", "fpp.reachable", true),
	}
	sink.RecordObservations(ctx, pruning, true)
	if notifyCount != 2 {
		t.Fatalf("notifyCount after a pruning delivery = %d, want 2 (a prune is a real store mutation)", notifyCount)
	}

	after, err := st.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "remote-01"})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("after the pruning delivery: %d observations remain, want 1 (fpp.port.16.current_ma must be pruned)", len(after))
	}
}

// mustMeasured builds a StateCurrent [observation.Observation] with source
// "fpp-rest", matching what fpp.Collector actually stamps (fpp.sourceName
// is unexported, so this pins the literal the way
// internal/coordinator/store's own tests do for the same reason).
func mustMeasured(t *testing.T, resourceID string, sig observation.SignalID, value any) observation.Observation {
	t.Helper()
	obs, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: resourceID},
		sig, value, time.Now(),
		observation.WithSource("fpp-rest"),
	)
	if err != nil {
		t.Fatalf("build observation %q: %v", sig, err)
	}
	return obs
}

// fixedPortsHandler serves portsBody at /api/fppd/ports (swappable between
// calls via the pointer, which is how one httptest.Server simulates the
// fleet's port count changing between polls) and a fixed, real, successful
// /api/fppd/status capture at that path — specifically so
// /api/fppd/status never fails: fpp.Collector's backoff is keyed to that
// one request alone (see Poll's recordAttempt doc comment), and a 404
// there would risk the second Poll call in a test landing inside a
// randomized backoff window and being skipped, which has nothing to do
// with what this test is proving. /api/fppd/multiSyncSystems and
// /api/system/info are left unhandled (404, its own independent
// collection_failed) since neither affects backoff or the ports family
// this test actually asserts on.
func fixedPortsHandler(statusBody []byte, portsBody *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fppd/ports":
			_, _ = w.Write(*portsBody)
		case "/api/fppd/status":
			_, _ = w.Write(statusBody)
		default:
			http.NotFound(w, r)
		}
	}
}

// loadFixture reads a real fleet capture from
// internal/coordinator/collector/fpp/testdata, the same files that
// package's own tests load, via a relative path (go test's working
// directory is always the package directory, internal/coordinator here).
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("collector/fpp/testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return b
}

// TestFPPSinkRealPortFixturesPruneGhostRowsOnEmptyDelivery is the task's
// named reproduction, driven end to end through a real fpp.Collector (not
// hand-built observations) against the exact fleet captures that exposed
// the defect: fpp-remote-b reporting 48 port elements
// (live_remote04_fppd_ports.json), then fpp-player reporting none
// (live_main_fppd_ports.json, a real "[]" from a Pi with no output cape —
// see fpp/doc.go). Before this fix, the second poll's fpp.port.<key>.*
// rows from the first poll would still be sitting in the store, aging
// toward stale forever and rendering in the UI's port grid as ports of a
// cape that is no longer installed.
func TestFPPSinkRealPortFixturesPruneGhostRowsOnEmptyDelivery(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sink := &fppSink{st: st, logger: testLogger()}

	statusBody := loadFixture(t, "live_remote04_fppd_status.json")
	portsBody := loadFixture(t, "live_remote04_fppd_ports.json")
	srv := httptest.NewServer(fixedPortsHandler(statusBody, &portsBody))
	defer srv.Close()

	c, err := fpp.New("remote-04", srv.URL, fpp.Options{})
	if err != nil {
		t.Fatalf("fpp.New: %v", err)
	}

	obs1, complete1 := c.Poll(ctx)
	if !complete1 {
		t.Fatalf("first Poll complete = false, want true (a real, non-backed-off attempt)")
	}
	sink.RecordObservations(ctx, obs1, complete1)

	afterFirst, err := st.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "remote-04"})
	if err != nil {
		t.Fatalf("list observations (after first poll): %v", err)
	}
	portRows := countPortRows(afterFirst)
	if portRows == 0 {
		t.Fatalf("after polling the 48-element fixture: 0 fpp.port.* rows stored, want many (48 elements' worth of per-port signals)")
	}

	// The same instance now reports an empty ports array — a real capture
	// from a Pi with no output cape, not a failure (see fpp/doc.go).
	portsBody = loadFixture(t, "live_main_fppd_ports.json")

	obs2, complete2 := c.Poll(ctx)
	if !complete2 {
		t.Fatalf("second Poll complete = false, want true")
	}
	sink.RecordObservations(ctx, obs2, complete2)

	afterSecond, err := st.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: "remote-04"})
	if err != nil {
		t.Fatalf("list observations (after second poll): %v", err)
	}
	if got := countPortRows(afterSecond); got != 0 {
		t.Errorf("after the ports response dropped to [] : %d fpp.port.* ghost rows remain, want 0", got)
	}
}

func countPortRows(obs []observation.Observation) int {
	n := 0
	for _, o := range obs {
		if strings.HasPrefix(string(o.Signal), "fpp.port.") {
			n++
		}
	}
	return n
}

// fixedFPPEndpoints is a fppEndpointProvider that always answers with the
// same list. It exists because fppInstanceLister and
// fppCollectorStatusLister stopped holding a []config.FPPEndpoint on
// 2026-08-14 and started resolving the active revision per call (see
// fppendpoints.go): these tests are about what the LISTER does with a
// given endpoint list, not about how the list is resolved, so they supply
// one directly rather than staging config revisions in the store.
type fixedFPPEndpoints []config.FPPEndpoint

func (f fixedFPPEndpoints) Current(context.Context) []config.FPPEndpoint { return f }
