package inventory

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeClock lets tests drive Manager.now deterministically, matching the
// fakeClock pattern used in internal/coordinator/broker and
// internal/coordinator/store's own tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestManager(t *testing.T, clock *fakeClock) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	m := New(st, testLogger())
	if clock != nil {
		m.now = clock.now
	}
	return m
}

func mustEnvelopeBytes(t *testing.T, env mqttproto.Envelope) []byte {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func helloTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.HelloTopic(nodeID)
	if err != nil {
		t.Fatalf("hello topic: %v", err)
	}
	return topic
}

func lwtTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.LWTTopic(nodeID)
	if err != nil {
		t.Fatalf("lwt topic: %v", err)
	}
	return topic
}

func healthTopic(t *testing.T, nodeID string) string {
	t.Helper()
	topic, err := mqttproto.ObservedTopic(nodeID, "health")
	if err != nil {
		t.Fatalf("health topic: %v", err)
	}
	return topic
}

func TestHandleMessageLiveHelloIsStoredWithObservedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	env, err := mqttproto.NewHelloEnvelope(nil, "node-a", mqttproto.HelloPayload{
		Label: "A", Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "boot-1",
		StartedAt: clock.now(),
		Capabilities: capability.Set{
			{ID: "matrix.render", Version: 1},
		},
	})
	if err != nil {
		t.Fatalf("build hello envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: helloTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false,
	})

	rec, err := m.store.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Hello == nil {
		t.Fatalf("Hello = nil, want stored record")
	}
	if rec.Hello.Provenance != store.ProvenanceAgentReport {
		t.Errorf("Provenance = %q, want agent_report for a live delivery", rec.Hello.Provenance)
	}
	if rec.Hello.ObservedAt == nil || !rec.Hello.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", rec.Hello.ObservedAt, clock.now())
	}
	if len(rec.Hello.Capabilities) != 1 || rec.Hello.Capabilities[0].ID != "matrix.render" {
		t.Errorf("Capabilities = %+v, not round-tripped", rec.Hello.Capabilities)
	}
}

// TestHandleMessageRetainedHelloHasNilObservedAt is the inventory-level
// half of the retained-freshness rule: a retained hello delivery must be
// stored with ObservedAt nil and provenance retained_broker_state, never
// with the coordinator's own receipt time.
func TestHandleMessageRetainedHelloHasNilObservedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	env, err := mqttproto.NewHelloEnvelope(nil, "node-a", mqttproto.HelloPayload{
		Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "boot-1", StartedAt: clock.now(),
	})
	if err != nil {
		t.Fatalf("build hello envelope: %v", err)
	}

	m.HandleMessage(broker.Message{
		Topic: helloTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: true,
	})

	rec, err := m.store.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Hello.ObservedAt != nil {
		t.Fatalf("ObservedAt = %v, want nil for a retained delivery", *rec.Hello.ObservedAt)
	}
	if rec.Hello.Provenance != store.ProvenanceRetainedBrokerState {
		t.Errorf("Provenance = %q, want retained_broker_state", rec.Hello.Provenance)
	}
	if !rec.Hello.Retained {
		t.Errorf("Retained = false, want true")
	}
}

// TestHandleMessageRetainedHealthNeverGoesOnline is the end-to-end version
// of the same rule for the health topic, and the test the shared contract
// calls out as most important: a retained health delivery must not, by
// itself, ever be able to produce a LivenessOnline verdict, even when
// last-will evidence says online.
func TestHandleMessageRetainedHealthNeverGoesOnline(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{t: now}
	m := newTestManager(t, clock)
	ctx := context.Background()

	lwtEnv, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, lwtEnv), Retained: false})

	healthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{
		BootID: "boot-1", Sequence: 1, AgentState: "running",
	})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	// A retained delivery, exactly as a late-subscribing (e.g. just
	// restarted) coordinator would receive on connect: the broker replays
	// whatever it last held on this topic, which could be hours old.
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv), Retained: true})

	views, err := m.Snapshot(ctx, now)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("len(views) = %d, want 1", len(views))
	}
	if views[0].Liveness != LivenessUnknown {
		t.Errorf("Liveness = %q, want unknown: a retained health delivery must never produce online, even with last-will online evidence and now == the delivery instant",
			views[0].Liveness)
	}

	// Now the agent's next real heartbeat arrives live (Retained: false,
	// higher sequence — a genuine subsequent heartbeat, not a redelivery of
	// the same one): liveness must become online within the staleness
	// window, without needing a coordinator restart.
	nextHealthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{
		BootID: "boot-1", Sequence: 2, AgentState: "running",
	})
	if err != nil {
		t.Fatalf("build next health envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, nextHealthEnv), Retained: false})
	views, err = m.Snapshot(ctx, now)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if views[0].Liveness != LivenessOnline {
		t.Errorf("Liveness = %q after a live heartbeat, want online", views[0].Liveness)
	}
}

// TestHandleMessageRetainedLWTHasNilObservedAt is the inventory-level half
// of the LWT retained-freshness fix: a retained LWT delivery — exactly what
// a just-restarted coordinator receives on subscribe, per the shared
// contract's "retained-message freshness trap" — must be stored with
// ObservedAt nil and provenance retained_broker_state, never with the
// coordinator's own receipt time. Before this fix, handleLWT stamped
// m.now() unconditionally, so a six-hour-old retained "online: true" would
// have looked observed just now.
func TestHandleMessageRetainedLWTHasNilObservedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	env, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: true})

	rec, err := m.store.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.LWT == nil {
		t.Fatalf("LWT = nil, want stored record")
	}
	if rec.LWT.ObservedAt != nil {
		t.Fatalf("ObservedAt = %v, want nil for a retained delivery", *rec.LWT.ObservedAt)
	}
	if rec.LWT.Provenance != store.ProvenanceRetainedBrokerState {
		t.Errorf("Provenance = %q, want retained_broker_state", rec.LWT.Provenance)
	}

	// The offline branch of deriveLiveness never reads LWT.ObservedAt (see
	// liveness.go), so this fix must not change today's liveness verdict:
	// LWT says online but there is no health evidence at all yet, which is
	// unknown either way.
	views, err := m.Snapshot(context.Background(), clock.now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(views) != 1 || views[0].Liveness != LivenessUnknown {
		t.Fatalf("views = %+v, want a single unknown node (online LWT, no health evidence yet)", views)
	}
}

// TestHandleMessageLiveLWTHasObservedAtAndAgentReportProvenance is the
// inventory-level regression test for the mislabeling half of the same
// finding: an agent's own live "online: true" publish must be stored as
// ProvenanceAgentReport, not the old hardcoded ProvenanceBrokerLastWill,
// since there is no wire-level way to tell a broker-fired Will apart from
// the agent's own live publish to the same topic.
func TestHandleMessageLiveLWTHasObservedAtAndAgentReportProvenance(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)

	env, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})

	rec, err := m.store.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.LWT == nil || rec.LWT.ObservedAt == nil || !rec.LWT.ObservedAt.Equal(clock.now()) {
		t.Fatalf("LWT = %+v, want ObservedAt %v", rec.LWT, clock.now())
	}
	if rec.LWT.Provenance != store.ProvenanceAgentReport {
		t.Errorf("Provenance = %q, want agent_report for a live delivery", rec.LWT.Provenance)
	}
}

func TestHandleMessageLWTOfflineDrivesLivenessOffline(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	m := newTestManager(t, &fakeClock{t: now})

	env, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: false, Reason: "unexpected disconnect"})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})

	views, err := m.Snapshot(context.Background(), now)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(views) != 1 || views[0].Liveness != LivenessOffline {
		t.Fatalf("views = %+v, want a single offline node", views)
	}
}

func TestHandleMessageBootIDAndSequenceRules(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	m := newTestManager(t, &fakeClock{t: now})
	ctx := context.Background()

	send := func(bootID string, seq uint64) {
		env, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{BootID: bootID, Sequence: seq})
		if err != nil {
			t.Fatalf("build health envelope: %v", err)
		}
		m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})
	}

	send("boot-1", 5)
	rec, err := m.store.GetNode(ctx, "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.Sequence != 5 {
		t.Fatalf("sequence = %d, want 5", rec.Health.Sequence)
	}

	send("boot-1", 3) // lower sequence, same boot: ignored
	rec, _ = m.store.GetNode(ctx, "node-a")
	if rec.Health.Sequence != 5 {
		t.Errorf("sequence = %d after a lower-sequence duplicate, want unchanged 5", rec.Health.Sequence)
	}

	send("boot-1", 5) // duplicate sequence: ignored
	rec, _ = m.store.GetNode(ctx, "node-a")
	if rec.Health.Sequence != 5 {
		t.Errorf("sequence = %d after an exact duplicate, want unchanged 5", rec.Health.Sequence)
	}

	send("boot-2", 0) // new boot ID: accepted regardless of sequence
	rec, _ = m.store.GetNode(ctx, "node-a")
	if rec.Health.BootID != "boot-2" || rec.Health.Sequence != 0 {
		t.Errorf("after new boot id: BootID=%q Sequence=%d, want boot-2/0", rec.Health.BootID, rec.Health.Sequence)
	}
}

func TestHandleMessageMalformedPayloadIsSkippedNotFatal(t *testing.T) {
	m := newTestManager(t, nil)

	// One garbage message per subscribed topic kind; none of these may
	// panic, and none may leave a node record behind.
	m.HandleMessage(broker.Message{Topic: helloTopic(t, "node-a"), Payload: []byte("not json"), Retained: false})
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: []byte(`{"schema":"wrong/v1","messageId":"x","nodeId":"node-a","sentAt":"2026-08-10T12:00:00Z","payload":{}}`), Retained: false})
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: []byte(`{}`), Retained: false})

	// A hello whose envelope nodeId disagrees with the topic's node ID.
	env, err := mqttproto.NewHelloEnvelope(nil, "node-b", mqttproto.HelloPayload{
		Platform: "p", AgentVersion: "v", BootID: "b", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("build hello envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: helloTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})

	// The bare observed parent topic ParseTopic always rejects; must be a
	// silent no-op, not a warning-logged skip and not a panic.
	m.HandleMessage(broker.Message{Topic: "showmesh/nodes/node-a/observed", Payload: []byte("x"), Retained: true})

	views, err := m.Snapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(views) != 0 {
		t.Errorf("views = %+v, want no nodes stored from any malformed message", views)
	}
}

// TestWithOnChangeFiresOnAcceptedWritesNotOnMalformedOrDuplicate proves the
// Step 3 wiring hook: onChange fires for a real hello/LWT/health write, but
// never for a message HandleMessage drops as malformed, and never for a
// health delivery RecordHealth ignored as a duplicate/reorder — see
// [Manager.onChange]'s doc comment for why the duplicate case is excluded.
func TestWithOnChangeFiresOnAcceptedWritesNotOnMalformedOrDuplicate(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, testLogger())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var calls int
	m := New(st, testLogger(), WithOnChange(func() { calls++ }))

	// A malformed hello: no call.
	m.HandleMessage(broker.Message{Topic: helloTopic(t, "node-a"), Payload: []byte("not json"), Retained: false})
	if calls != 0 {
		t.Fatalf("calls = %d after a malformed message, want 0", calls)
	}

	// A genuine, accepted hello: exactly one call.
	env, err := mqttproto.NewHelloEnvelope(nil, "node-a", mqttproto.HelloPayload{
		Platform: "p", AgentVersion: "v", BootID: "boot-1", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("build hello envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: helloTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})
	if calls != 1 {
		t.Fatalf("calls = %d after an accepted hello, want 1", calls)
	}

	// A genuine, accepted health heartbeat: one more call.
	healthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{
		BootID: "boot-1", Sequence: 1, AgentState: "running",
	})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv), Retained: false})
	if calls != 2 {
		t.Fatalf("calls = %d after an accepted health heartbeat, want 2", calls)
	}

	// The exact same boot ID/sequence again: RecordHealth ignores it as a
	// duplicate, so no additional call.
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv), Retained: false})
	if calls != 2 {
		t.Errorf("calls = %d after a duplicate health heartbeat, want still 2 (no notify for an ignored duplicate)", calls)
	}
}

// TestRecordLivenessTransitionAppendsEventOnlyOnRealChange proves Step 3's
// actual event-history production path: the first observation of a node in
// a process's lifetime records no event (nothing to compare against — see
// Manager.lastLiveness's doc comment), a genuine online -> offline
// transition appends exactly one control_plane event, and a message that
// does not change liveness (a second live heartbeat while already online)
// appends none.
func TestRecordLivenessTransitionAppendsEventOnlyOnRealChange(t *testing.T) {
	m := newTestManager(t, nil)
	ctx := context.Background()

	countEvents := func() int {
		events, _, err := m.store.ListEvents(ctx, 0, 100)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		return len(events)
	}

	env, err := mqttproto.NewHelloEnvelope(nil, "node-a", mqttproto.HelloPayload{
		Platform: "p", AgentVersion: "v", BootID: "boot-1", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("build hello envelope: %v", err)
	}
	// First-ever observation: no prior liveness to compare against, so no
	// event yet, even though this hello alone may already imply some
	// liveness verdict.
	m.HandleMessage(broker.Message{Topic: helloTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, env), Retained: false})
	if got := countEvents(); got != 0 {
		t.Fatalf("events after first-ever observation = %d, want 0", got)
	}

	// A live LWT online:true completes a real transition to online (from
	// whatever the hello-only state was) — exactly one event.
	lwtEnv, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, lwtEnv), Retained: false})
	healthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{BootID: "boot-1", Sequence: 1, AgentState: "running"})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv), Retained: false})

	view, ok := m.findNodeView(t, "node-a")
	if !ok || view.Liveness != LivenessOnline {
		t.Fatalf("node-a liveness = %+v, want online before continuing", view)
	}
	afterOnline := countEvents()
	if afterOnline == 0 {
		t.Fatalf("events after reaching online = 0, want at least one control_plane transition event")
	}

	// A second live heartbeat while still online: liveness does not change,
	// so no additional event.
	healthEnv2, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{BootID: "boot-1", Sequence: 2, AgentState: "running"})
	if err != nil {
		t.Fatalf("build second health envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv2), Retained: false})
	if got := countEvents(); got != afterOnline {
		t.Errorf("events after a same-liveness heartbeat = %d, want unchanged from %d", got, afterOnline)
	}

	// A live LWT online:false is a genuine transition to offline: one more
	// event, and its Category/Severity/Summary match the shape
	// internal/coordinator/api's own eventFixture anticipated.
	lwtOfflineEnv, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: false, Reason: "unexpected disconnect"})
	if err != nil {
		t.Fatalf("build offline lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, lwtOfflineEnv), Retained: false})

	events, _, err := m.store.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(events) != afterOnline+1 {
		t.Fatalf("events after going offline = %d, want %d", len(events), afterOnline+1)
	}
	last := events[len(events)-1]
	if last.Category != "control_plane" {
		t.Errorf("last event Category = %q, want \"control_plane\"", last.Category)
	}
	if last.Severity != "warning" {
		t.Errorf("last event Severity = %q, want \"warning\" for a transition to offline", last.Severity)
	}
	if last.Summary != "node control-plane state changed to offline" {
		t.Errorf("last event Summary = %q, want %q", last.Summary, "node control-plane state changed to offline")
	}
}

// TestRecordLivenessObservationDetectsStalenessOnlyTransition is the
// regression guard for Step 3 review finding 3.4: recordLivenessTransition
// alone only ever fires from HandleMessage, so a node whose heartbeats
// simply stop — no further message, no last will, ever — could transition
// online -> offline by staleness alone with nothing recording it to event
// history. This proves the exported entry point that closes that gap
// (RecordLivenessObservation, which internal/coordinator/apiwiring.go's
// livenessObservingNodeLister calls after every Snapshot) records exactly
// the same kind of event with zero HandleMessage calls driving it, shares
// the once-per-actual-transition dedup recordLivenessTransition already
// relies on (Manager.lastLiveness), and does not double-record on a
// repeated, unchanged call.
func TestRecordLivenessObservationDetectsStalenessOnlyTransition(t *testing.T) {
	m := newTestManager(t, nil)
	ctx := context.Background()

	countEvents := func() int {
		events, _, err := m.store.ListEvents(ctx, 0, 100)
		if err != nil {
			t.Fatalf("list events: %v", err)
		}
		return len(events)
	}

	// Bring node-a online the ordinary, message-driven way first, matching
	// TestRecordLivenessTransitionAppendsEventOnlyOnRealChange's own setup.
	lwtEnv, err := mqttproto.NewLWTEnvelope(nil, "node-a", mqttproto.LWTPayload{Online: true})
	if err != nil {
		t.Fatalf("build lwt envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: lwtTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, lwtEnv), Retained: false})
	healthEnv, err := mqttproto.NewHealthEnvelope(nil, "node-a", mqttproto.HealthPayload{BootID: "boot-1", Sequence: 1, AgentState: "running"})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	m.HandleMessage(broker.Message{Topic: healthTopic(t, "node-a"), Payload: mustEnvelopeBytes(t, healthEnv), Retained: false})

	view, ok := m.findNodeView(t, "node-a")
	if !ok || view.Liveness != LivenessOnline {
		t.Fatalf("node-a liveness = %+v, want online before continuing", view)
	}
	afterOnline := countEvents()
	if afterOnline == 0 {
		t.Fatalf("events after reaching online = 0, want at least one")
	}

	// No further message ever arrives for node-a from here on. Simulate
	// exactly what livenessObservingNodeLister does on every Snapshot call
	// (including the hub's own render tick): re-derive liveness against a
	// later "now" and feed the verdict straight to RecordLivenessObservation
	// — never HandleMessage — as if staleness alone had tipped it offline.
	m.RecordLivenessObservation(ctx, "node-a", LivenessOffline, "heartbeat staleness window exceeded")

	if got := countEvents(); got != afterOnline+1 {
		t.Fatalf("events after a staleness-only RecordLivenessObservation call = %d, want %d", got, afterOnline+1)
	}
	events, _, err := m.store.ListEvents(ctx, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	last := events[len(events)-1]
	if last.Category != "control_plane" {
		t.Errorf("last event Category = %q, want \"control_plane\"", last.Category)
	}
	if last.Summary != "node control-plane state changed to offline" {
		t.Errorf("last event Summary = %q, want %q", last.Summary, "node control-plane state changed to offline")
	}

	// A repeated call reporting the SAME liveness must not append a second
	// event — the same dedup recordLivenessTransition relies on, now shared
	// by both callers through observeLiveness.
	m.RecordLivenessObservation(ctx, "node-a", LivenessOffline, "heartbeat staleness window exceeded")
	if got := countEvents(); got != afterOnline+1 {
		t.Errorf("events after a repeated, unchanged RecordLivenessObservation call = %d, want unchanged at %d", got, afterOnline+1)
	}
}

// findNodeView is a small helper: the single-node equivalent of Snapshot,
// used only by this test file.
func (m *Manager) findNodeView(t *testing.T, nodeID string) (NodeView, bool) {
	t.Helper()
	views, err := m.Snapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, v := range views {
		if v.NodeID == nodeID {
			return v, true
		}
	}
	return NodeView{}, false
}

func TestSubscriptionsCoverAllThreeFilters(t *testing.T) {
	m := newTestManager(t, nil)
	subs := m.Subscriptions()
	want := map[string]bool{
		mqttproto.SubscribeHello:    false,
		mqttproto.SubscribeLWT:      false,
		mqttproto.SubscribeObserved: false,
	}
	for _, s := range subs {
		if _, ok := want[s.Filter]; !ok {
			t.Errorf("unexpected subscription filter %q", s.Filter)
			continue
		}
		want[s.Filter] = true
	}
	for filter, seen := range want {
		if !seen {
			t.Errorf("missing subscription for filter %q", filter)
		}
	}
}
