package fppmqtt

import (
	"context"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// fixedClock returns a func() time.Time a test can move by assigning to
// *now directly, mirroring internal/coordinator/collector/fpp's
// fixedClock — every test here drives Poll and the publish handler
// synchronously from the test goroutine, so no atomics are needed.
func fixedClock(now *time.Time) func() time.Time {
	return func() time.Time { return *now }
}

// newTestCollector builds a *Collector for direct testing: New() never
// connects to anything (see mqttclient.go), so a syntactically valid but
// unreachable BrokerURL is enough. Run is never called in these tests;
// messages are injected via the handler returned by newPublishHandler,
// exactly the function a real connection would invoke, and connection
// state is set directly via setConnected — both exercise this package's
// real code paths without a broker.
func newTestCollector(t *testing.T, hosts map[string]string, now *time.Time) *Collector {
	t.Helper()
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     hosts,
		Now:       fixedClock(now),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

// deliver simulates one inbound MQTT publish on topic, as the real
// connection's OnPublishReceived callback would present it.
func deliver(c *Collector, topic string, payload []byte, retained bool) {
	handler := c.newPublishHandler()
	_, _ = handler(paho.PublishReceived{
		Packet: &paho.Publish{Topic: topic, Payload: payload, Retain: retained},
	})
}

func findObservation(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	var found []observation.Observation
	for _, o := range obs {
		if o.Signal == sig {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		t.Fatalf("signal %q appeared %d times in Poll result, want exactly 1", sig, len(found))
	}
	return found[0]
}

// --- The load-bearing rule: retained vs live (contract section 4.2) ------

// TestPollRetainedDeliveryIsUnknownAge is the direct unit test of contract
// section 4.2's rule: a message delivered with RETAIN set produces
// MeasuredUnknownAge (ObservedAt nil, State unknown_age), never a value
// stamped with the receipt time.
//
// Before trusting this test, buildObservation was changed to use
// msg.receivedAt as ObservedAt on the retained branch too (exactly the
// defect this rule exists to prevent — see doc.go: "introduced and caught
// three times in this project in different disguises") and confirmed to
// make this test fail; see the Step 5 Seam B report for that verification.
func TestPollRetainedDeliveryIsUnknownAge(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), true)

	obs := c.Poll(context.Background())
	got := findObservation(t, obs, SignalStatus)

	if got.ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil for a retained delivery", got.ObservedAt)
	}
	if got.StateAt(now) != observation.StateUnknownAge {
		t.Errorf("StateAt(now) = %q, want %q", got.StateAt(now), observation.StateUnknownAge)
	}
	if got.Value != "idle" {
		t.Errorf("Value = %#v, want %q (retained still carries the real value)", got.Value, "idle")
	}
}

// TestPollLiveDeliveryUsesReceiptTime is the mirror case: RETAIN clear
// means ObservedAt is a defensible receipt time, StateCurrent while fresh.
func TestPollLiveDeliveryUsesReceiptTime(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), false)

	obs := c.Poll(context.Background())
	got := findObservation(t, obs, SignalStatus)

	if got.ObservedAt == nil {
		t.Fatalf("ObservedAt = nil, want the receipt time for a live delivery")
	}
	if !got.ObservedAt.Equal(now) {
		t.Errorf("ObservedAt = %v, want %v", *got.ObservedAt, now)
	}
	if got.StateAt(now) != observation.StateCurrent {
		t.Errorf("StateAt(now) = %q, want %q", got.StateAt(now), observation.StateCurrent)
	}
}

// TestPollLiveValueAgesIntoStaleWithoutBeingRefreshedByPolling proves
// contract section 4.2's second half: "once a topic has delivered a live
// message, later polls must keep reporting that live observation with its
// original ObservedAt, so it ages into stale naturally rather than being
// refreshed by the act of polling." Two Poll calls, no new message between
// them, with the clock advanced past ValidFor — the ObservedAt from the
// FIRST Poll must equal the ObservedAt from the SECOND.
func TestPollLiveValueAgesIntoStaleWithoutBeingRefreshedByPolling(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), false)

	first := findObservation(t, c.Poll(context.Background()), SignalStatus)
	if first.StateAt(now) != observation.StateCurrent {
		t.Fatalf("first poll StateAt = %q, want current", first.StateAt(now))
	}

	// Advance the clock past DefaultValidFor without delivering anything
	// new on this topic.
	now = now.Add(DefaultValidFor * 2)

	second := findObservation(t, c.Poll(context.Background()), SignalStatus)

	if !second.ObservedAt.Equal(*first.ObservedAt) {
		t.Errorf("second poll's ObservedAt = %v, want unchanged from first poll's %v (polling must not refresh it)", *second.ObservedAt, *first.ObservedAt)
	}
	if second.StateAt(now) != observation.StateStale {
		t.Errorf("second poll's StateAt(now) = %q, want %q now that it has aged past ValidFor", second.StateAt(now), observation.StateStale)
	}
}

// --- Connection-down (contract section 4.1) -------------------------------

// TestPollConnectionDownProducesCollectionFailedForEverySignal covers
// "The connection state itself is a signal — a collection_failed on every
// signal with the reason naming the broker failure when the connection is
// down, never silence." It also proves this OVERRIDES cached data: a live
// value was received before the connection dropped, and after the drop
// Poll must still report collection_failed for that signal, not the cached
// value.
func TestPollConnectionDownProducesCollectionFailedForEverySignal(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")
	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), false)
	_ = c.Poll(context.Background()) // sanity: was healthy before the drop

	c.setConnected(false, "mqtt broker connect attempt failed: dial tcp: connection refused")

	obs := c.Poll(context.Background())
	if len(obs) != len(allStaticSignalIDs) {
		t.Fatalf("Poll() returned %d observations while disconnected, want exactly %d (one per static signal)", len(obs), len(allStaticSignalIDs))
	}
	for _, o := range obs {
		if o.Absence != observation.StateCollectionFailed {
			t.Errorf("signal %q: Absence = %q, want %q while disconnected", o.Signal, o.Absence, observation.StateCollectionFailed)
		}
		// Reason just needs to be non-empty and mention the failure; not
		// asserting exact text since that is not part of the contract.
		if o.Reason == "" {
			t.Errorf("signal %q: empty Reason while disconnected, want it to name the broker failure", o.Signal)
		}
	}

	// The specific signal that had a live value moments ago must ALSO now
	// read collection_failed — proving the override, not just that new
	// signals appear failed.
	got := findObservation(t, obs, SignalStatus)
	if got.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.status while disconnected: Absence = %q, want %q even though a live value was cached", got.Absence, observation.StateCollectionFailed)
	}
}

// --- Not yet collected -----------------------------------------------------

func TestPollNeverReceivedTopicIsNotCollected(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	obs := c.Poll(context.Background())
	got := findObservation(t, obs, SignalStatus)
	if got.Absence != observation.StateNotCollected {
		t.Errorf("fpp.status with no message ever received: Absence = %q, want %q", got.Absence, observation.StateNotCollected)
	}
	if got.Reason == "" {
		t.Errorf("fpp.status: empty Reason, want it to say no message has been received")
	}
}

// --- Decode failure isolation ----------------------------------------------

func TestPollDecodeFailureIsolatedToItsOwnTopic(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), false)
	deliver(c, "falcon/player/FPP-Main/ready", []byte("not-a-valid-ready-value"), false)

	obs := c.Poll(context.Background())

	status := findObservation(t, obs, SignalStatus)
	if status.Absence != "" {
		t.Errorf("fpp.status: Absence = %q, want a measured value unaffected by the ready topic's decode failure", status.Absence)
	}

	ready := findObservation(t, obs, SignalReady)
	if ready.Absence != observation.StateCollectionFailed {
		t.Errorf("fpp.ready: Absence = %q, want %q for an undecodable payload", ready.Absence, observation.StateCollectionFailed)
	}
}

// --- Host matching: no unbounded resource creation (contract section 4.4) -

func TestPollUnmatchedHostNeverBecomesResource(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{"main": "FPP-Main"}, &now)
	c.setConnected(true, "")

	deliver(c, "falcon/player/FPP-Shed/status", []byte("idle"), false)

	obs := c.Poll(context.Background())
	for _, o := range obs {
		if o.Resource.ID == "shed" || o.Resource.ID == "FPP-Shed" {
			t.Fatalf("an unconfigured host produced an observation for resource %q; it must never become a resource", o.Resource.ID)
		}
	}
	// Every observation must belong to the one configured instance.
	for _, o := range obs {
		if o.Resource.ID != "main" {
			t.Errorf("observation for resource %q, want only the configured instance %q", o.Resource.ID, "main")
		}
	}
}

// --- The acceptance demonstration: the real FPP-01 ghost -------------------

// TestFPP01GhostAllSignalsReadUnknownAgeIndefinitely is contract section
// 4.2's named acceptance demonstration, run against the REAL captured
// FPP-01 payloads (testdata/FPP-01_fppd_status.json,
// testdata/FPP-01_port_status.json): "Point the collector at the real
// broker with FPP-01 configured as an instance and assert every one of its
// signals reports unknown_age, indefinitely, while FPP-Main's report
// current. If a code path exists that could make FPP-01 read healthy, the
// rule is not implemented."
//
// This test cannot dial the real broker (section 0's absolute rule: never
// connect to it during development), so it reproduces the broker's
// documented behavior exactly instead: every one of FPP-01's topics
// arrived with the retain flag set during the 60-second capture (contract
// section 1.2), which is simulated here by delivering its real captured
// bodies with retained=true, on the same code path (newPublishHandler)
// a real connection would use. FPP-Main is delivered live (retained=false)
// in the same test as the contrasting case the contract asks for.
func TestFPP01GhostAllSignalsReadUnknownAgeIndefinitely(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	c := newTestCollector(t, map[string]string{
		"ghost": "FPP-01",
		"main":  "FPP-Main",
	}, &now)
	c.setConnected(true, "")

	// FPP-01: every topic retained, exactly as captured.
	deliver(c, "falcon/player/FPP-01/fppd_status", readTestdata(t, "FPP-01_fppd_status.json"), true)
	deliver(c, "falcon/player/FPP-01/port_status", readTestdata(t, "FPP-01_port_status.json"), true)

	// FPP-Main: delivered live, as the contrasting "current" case.
	deliver(c, "falcon/player/FPP-Main/fppd_status", readTestdata(t, "FPP-Main_fppd_status.json"), false)
	deliver(c, "falcon/player/FPP-Main/status", []byte("idle"), false)

	// Simulate a little real elapsed time between delivery and the poll
	// that checks it (still well within DefaultValidFor, so FPP-Main's
	// live value below is checked as "current", not "stale") — the
	// ghost's unknown_age property does not depend on ValidFor at all
	// (see pkg/observation.Observation.StateAt: ObservedAt == nil returns
	// StateUnknownAge unconditionally, before ValidFor is even
	// consulted), which is exactly why "indefinitely" is checked
	// separately, much later, below.
	now = now.Add(5 * time.Second)
	obs := c.Poll(context.Background())

	ghostObservations := 0
	for _, o := range obs {
		if o.Resource.ID != "ghost" {
			continue
		}
		ghostObservations++
		if o.Absence != "" {
			// An absence observation (e.g. a mode-explained Unsupported,
			// or a signal from a topic FPP-01 never published like
			// "ready") carries no ObservedAt at all in this model and so
			// is trivially never "healthy" — only a value-bearing signal
			// can fail this assertion.
			continue
		}
		if o.ObservedAt != nil {
			t.Errorf("ghost resource, signal %q: ObservedAt = %v, want nil (retained replay of unknown age)", o.Signal, *o.ObservedAt)
		}
		if state := o.StateAt(now); state != observation.StateUnknownAge {
			t.Errorf("ghost resource, signal %q: StateAt = %q, want %q — this signal must never read healthy", o.Signal, state, observation.StateUnknownAge)
		}
	}
	if ghostObservations == 0 {
		t.Fatalf("no observations at all for the ghost resource; test setup is broken")
	}

	// A much later poll: the rule is "indefinitely", not "until the next
	// poll happens to run".
	now = now.Add(24 * time.Hour)
	laterObs := c.Poll(context.Background())
	for _, o := range laterObs {
		if o.Resource.ID != "ghost" || o.Absence != "" {
			continue
		}
		if state := o.StateAt(now); state != observation.StateUnknownAge {
			t.Errorf("ghost resource, signal %q, 24h later: StateAt = %q, want %q still", o.Signal, state, observation.StateUnknownAge)
		}
	}

	// The contrast: FPP-Main's live-delivered fpp.status must read
	// current, not unknown_age. Ghost also has a SignalStatus observation
	// (NotCollected — its own "status" topic was never delivered, only
	// fppd_status/port_status were), so this must be filtered by resource,
	// not just by signal.
	var mainStatus *observation.Observation
	for i := range obs {
		if obs[i].Signal == SignalStatus && obs[i].Resource.ID == "main" {
			mainStatus = &obs[i]
		}
	}
	if mainStatus == nil {
		t.Fatalf("no fpp.status observation found for resource %q", "main")
	}
	if got := mainStatus.StateAt(now.Add(-24 * time.Hour)); got != observation.StateCurrent {
		t.Errorf("FPP-Main's live fpp.status: StateAt = %q shortly after delivery, want %q", got, observation.StateCurrent)
	}
}
