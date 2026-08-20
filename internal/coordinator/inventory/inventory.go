// Package inventory subscribes to the ADR-008 hello, last-will, and
// observed-state topics and maintains the coordinator's view of node
// inventory on top of internal/coordinator/store: it decodes and validates
// incoming MQTT messages, applies the Step 2 round 2 shared design
// contract's retained-versus-live and boot-ID/sequence rules, persists
// evidence through store, and computes each node's liveness verdict on
// read (see liveness.go).
//
// This package owns exactly one piece of state that store does not: the
// interpretation of internal/coordinator/broker.Message.Retained into an
// observation time and provenance (see classify). That decision — and
// only that decision — is what the rest of this package, and store below
// it, treats as ground truth about whether a delivery is proof of life.
package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// requestTimeout bounds every store call HandleMessage makes. HandleMessage
// runs as a broker.MessageHandler, on paho's dedicated publish-routing
// worker goroutine, not the MQTT client's own read loop or ping handler
// (see broker.MessageHandler's doc comment for what actually runs where and
// why that distinction does not remove the need for this timeout): a hung
// database call would otherwise stall that worker indefinitely, delaying
// this client's PUBACKs and, once paho's inbound buffer fills, acking of
// further inbound publishes.
const requestTimeout = 5 * time.Second

// Manager subscribes to node inventory topics and maintains
// internal/coordinator/store's node records. The zero value is not usable;
// construct with New.
type Manager struct {
	store  *store.Store
	logger *slog.Logger

	// now is the clock used to stamp a live delivery's observation time.
	// Never used for a retained delivery — see classify. A field, rather
	// than a direct time.Now call, so tests can drive it deterministically,
	// matching internal/coordinator/broker's BrokerManager.now.
	now func() time.Time

	// onChange is called after every inventory write HandleMessage
	// successfully commits to the store — never for a message dropped as
	// malformed, or ignored as a duplicate/reorder (see [Store.RecordHealth]'s
	// accepted return value). nil (the default; every Step 2 caller of New)
	// means no notification at all, exactly today's behavior.
	//
	// This is the Step 3 wiring task's hook (contract section 5: "wire the
	// poke from ... the MQTT inventory path") for telling
	// internal/coordinator/api.Hub.Notify a node changed promptly, instead
	// of waiting up to the hub's own render tick (a SHOWMESH HYPOTHESIS
	// default of 5s — see that package's Options.StreamTickInterval). See
	// [WithOnChange].
	onChange func()

	// onHello is called with nodeID after a hello is successfully stored —
	// see [WithOnHello]. nil (the default) means no notification. This is
	// what lets a coordinator-side subscriber (ADR-039/ADR-036's audio
	// config push) reach a node that reconnects after being offline
	// during a config write, without this package importing anything
	// about audio configuration itself.
	onHello func(nodeID string)

	// livenessMu and lastLiveness back [Manager.observeLiveness] (called
	// from both recordLivenessTransition, the message-arrival path, and
	// the exported [Manager.RecordLivenessObservation], the staleness-on-
	// read path apiwiring.go's livenessObservingNodeLister drives): an
	// in-process, in-memory-only record of the last Liveness this Manager
	// instance itself observed for each node, so a genuine transition
	// (online -> offline, etc.) can be recorded as an event exactly once,
	// regardless of which of those two paths happens to notice it first —
	// see observeLiveness's own doc comment for why this is Step 3's actual
	// production path for OBSERVABILITY section 4.3's event history. Reset to
	// empty on every process restart, deliberately: comparing a
	// freshly-started process's first observation of a node against
	// "nothing yet" would manufacture a spurious transition event out of
	// that node's mere existence, not a real change, so the very first
	// observation after a restart is recorded silently and only the NEXT
	// change (if any) produces an event. This means a transition that
	// happens to straddle a coordinator restart is not recorded — an
	// acceptable, bookkeeping-only gap (RES-013 owns real event/metric
	// retention design), not a correctness problem: observed state itself
	// (GET /api/v1/nodes) is unaffected and always current.
	livenessMu   sync.Mutex
	lastLiveness map[string]Liveness

	// renderSink receives every decoded render report — see
	// [WithRenderSink]. nil (the default) means no render ingestion at all:
	// a "render" observed subpath is then dropped exactly like any other
	// subpath this step does not understand (the default case below),
	// never a startup failure.
	renderSink RenderSink

	// renderMu guards renderBaseline and renderFailed: this Manager
	// instance's own in-memory record of each surface's last-seen restart
	// count and failed-lockout state, backing [Manager.observeRenderSurfaces].
	// Reset to empty on every process restart, deliberately, matching
	// lastLiveness's identical rule one field up: comparing a freshly
	// started process's first render report for a surface (typically a
	// retained replay) against "nothing yet" would manufacture a restart or
	// lockout event out of history that predates this coordinator, so the
	// first observation of a surface is always recorded silently and only
	// the next genuine change produces an event.
	renderMu       sync.Mutex
	renderBaseline map[string]int64
	renderFailed   map[string]bool

	// audioSink receives every decoded audio discovery report — see
	// [WithAudioSink]. nil (the default) means no audio ingestion at all:
	// an "audio" observed subpath is then dropped exactly like any other
	// subpath this step does not understand (the default case below).
	audioSink AudioSink
}

// RenderSink receives a node's decoded render report as it arrives, so
// internal/coordinator/collector/noderender's push cache can be fed without
// this package importing that collector package directly — the same
// "declare the interface at the consumer" convention
// internal/coordinator/collector.Sink documents, applied here because
// noderender.Store is the producer and this package is what has the
// decoded payload plus the retained/live verdict to give it.
// *noderender.Store already satisfies this with no adapter needed.
type RenderSink interface {
	// Put records payload as nodeID's latest render report. retained and
	// receivedAt are exactly [Manager.classify]'s own retained flag and
	// receipt time — see [Manager.handleRender].
	Put(nodeID string, payload mqttproto.RenderPayload, retained bool, receivedAt time.Time)
}

// AudioSink receives a node's decoded audio discovery report as it
// arrives, so internal/coordinator/collector/nodeaudio's push cache can be
// fed without this package importing that collector package directly —
// [RenderSink]'s identical role, one report type over.
// *nodeaudio.Store already satisfies this with no adapter needed.
type AudioSink interface {
	// Put records payload as nodeID's latest audio report. receivedAt is
	// this coordinator's own receipt time — see [Manager.handleAudio].
	Put(nodeID string, payload mqttproto.AudioPayload, receivedAt time.Time)
}

// Option configures optional Manager behavior at [New]. The zero value
// (no options) is Step 2's behavior unchanged.
type Option func(*Manager)

// WithOnChange registers fn to be called after every inventory write
// HandleMessage successfully commits — see [Manager.onChange]'s doc
// comment for exactly when. fn must be safe to call from whatever
// goroutine HandleMessage runs on (see HandleMessage's own doc comment:
// paho's publish-routing worker, not its read loop) and must not block:
// internal/coordinator/api.Hub.Notify already satisfies both, being
// non-blocking by construction.
func WithOnChange(fn func()) Option {
	return func(m *Manager) { m.onChange = fn }
}

// WithOnHello registers fn to be called with a node's id after its hello
// is successfully stored — see [Manager.onHello]'s doc comment. fn must
// be safe to call from whatever goroutine HandleMessage runs on and
// should not block: a caller that needs to do real I/O (an MQTT publish,
// a store read) should launch its own goroutine rather than block
// hello processing for every node behind one slow push.
func WithOnHello(fn func(nodeID string)) Option {
	return func(m *Manager) { m.onHello = fn }
}

// WithRenderSink registers sink to receive every decoded render report —
// see [Manager.renderSink] and [Manager.handleRender]. Optional: the
// default (no sink registered) drops "render" observed messages exactly
// like any other subpath this step does not model.
func WithRenderSink(sink RenderSink) Option {
	return func(m *Manager) { m.renderSink = sink }
}

// WithAudioSink registers sink to receive every decoded audio discovery
// report — see [Manager.audioSink] and [Manager.handleAudio]. Optional:
// the default (no sink registered) drops "audio" observed messages
// exactly like any other subpath this step does not model.
func WithAudioSink(sink AudioSink) Option {
	return func(m *Manager) { m.audioSink = sink }
}

// New builds a Manager backed by st. logger may be nil, in which case
// slog.Default() is used.
func New(st *store.Store, logger *slog.Logger, opts ...Option) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		store:          st,
		logger:         logger,
		now:            time.Now,
		lastLiveness:   make(map[string]Liveness),
		renderBaseline: make(map[string]int64),
		renderFailed:   make(map[string]bool),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// notify calls m.onChange if one was registered via [WithOnChange]. Safe to
// call unconditionally; every handleHello/handleLWT/handleHealth call site
// does, right after a write it knows actually changed something.
func (m *Manager) notify() {
	if m.onChange != nil {
		m.onChange()
	}
}

// recordLivenessTransition re-reads nodeID's stored evidence, derives its
// current Liveness (the same computation Snapshot uses), and hands it to
// [Manager.observeLiveness].
//
// Called from handleHello/handleLWT/handleHealth after a write m.notify()
// already considered real (see each call site) — never for a message
// dropped as malformed or ignored as a duplicate/reorder, for the same
// reason [Manager.onChange] excludes those. This is a message-arrival-only
// path: see [Manager.RecordLivenessObservation]'s doc comment for the
// staleness-only gap that leaves, and how it is closed elsewhere.
func (m *Manager) recordLivenessTransition(ctx context.Context, nodeID string) {
	rec, err := m.store.GetNode(ctx, nodeID)
	if err != nil {
		m.logger.Warn("failed to read node for liveness-transition bookkeeping", "node_id", nodeID, "error", err)
		return
	}
	now := m.now()
	liveness, reason := deriveLiveness(rec, now)
	m.observeLiveness(ctx, nodeID, liveness, reason)
}

// RecordLivenessObservation feeds an already-computed Liveness verdict —
// typically one a caller just got back from [Manager.Snapshot] — into the
// same once-per-actual-transition event bookkeeping
// [recordLivenessTransition] uses, without a second read of the node's
// stored evidence.
//
// This exists to close a gap [recordLivenessTransition] alone leaves (Step
// 3 review finding 3.4): that method runs only from HandleMessage's three
// call sites, so it fires only when a hello, last-will, or heartbeat
// message actually arrives. A node whose heartbeats simply stop — no
// further message ever arrives, and no last will either — transitions
// online -> offline by staleness alone, recomputed fresh every time
// anything calls Snapshot against a later now; nothing on the
// message-arrival path ever re-evaluates it, so the transition never
// reaches event history even though GET /api/v1/nodes and the SSE stream
// both correctly report the new state the moment enough time has passed.
//
// internal/coordinator/apiwiring.go's livenessObservingNodeLister is the
// production caller: it wraps this package's NodeLister so every Snapshot
// call — including the hub's own fixed render tick (contract section 6.5),
// not only a direct API request — also calls this method for every
// returned node, which is the point a staleness-driven transition is
// actually detected, not merely the point a message happens to arrive.
// Declared here, in this package, rather than left for a caller to
// reimplement, because the once-per-actual-transition bookkeeping
// ([Manager.lastLiveness]) is this package's own private state.
func (m *Manager) RecordLivenessObservation(ctx context.Context, nodeID string, liveness Liveness, reason string) {
	m.observeLiveness(ctx, nodeID, liveness, reason)
}

// observeLiveness is the shared bookkeeping [recordLivenessTransition] and
// [Manager.RecordLivenessObservation] both reduce to: compare liveness
// against the last Liveness this Manager instance itself observed for
// nodeID and, if it differs, append a "control_plane" category event
// recording the change, then poke onChange again so a subscriber sees the
// event promptly rather than waiting for the hub's own tick.
//
// This is Step 3's actual production path for event history: before
// recordLivenessTransition existed, nothing outside a test ever called
// [store.Store.AppendEvent] — the events table and GET /api/v1/events were
// fully built and wired end-to-end with nothing feeding them. The shape
// here (category "control_plane", summary "node control-plane state
// changed to <state>") deliberately matches internal/coordinator/api's own
// eventFixture test fixture, which anticipated exactly this producer
// without this package having built it yet.
func (m *Manager) observeLiveness(ctx context.Context, nodeID string, liveness Liveness, reason string) {
	m.livenessMu.Lock()
	prev, known := m.lastLiveness[nodeID]
	m.lastLiveness[nodeID] = liveness
	m.livenessMu.Unlock()

	if !known || prev == liveness {
		// Either the very first observation of this node in this process's
		// lifetime (nothing to compare against — see the field doc comment
		// on lastLiveness for why that must not itself produce an event),
		// or a genuine no-op (liveness did not actually change).
		return
	}

	severity := "informational"
	if liveness == LivenessOffline {
		severity = "warning"
	}
	details, err := json.Marshal(map[string]string{
		"from":   string(prev),
		"to":     string(liveness),
		"reason": reason,
	})
	if err != nil {
		// Unreachable: every value marshaled above is a plain string.
		// Recorded rather than dropped, so a future bug here is visible in
		// logs instead of silently losing the event.
		m.logger.Error("failed to encode liveness-transition event details", "node_id", nodeID, "error", err)
		details = nil
	}

	if _, err := m.store.AppendEvent(ctx, store.EventRecord{
		Source:   "mqtt-inventory",
		Resource: observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID},
		Category: "control_plane",
		Severity: severity,
		Summary:  fmt.Sprintf("node control-plane state changed to %s", liveness),
		Details:  json.RawMessage(details),
	}); err != nil {
		m.logger.Error("failed to append control-plane liveness-transition event", "node_id", nodeID, "error", err)
		return
	}
	m.notify()
}

// Subscriptions returns the broker.Subscription set inventory needs to
// receive every node's hello, last-will, and observed-state topics, for
// passing to broker.NewBrokerManager. QoS 1 everywhere matches
// mqttproto's exported delivery policy for these topics (see
// pkg/mqttproto's HelloDeliveryPolicy, ObservedDeliveryPolicy, and
// LWTDeliveryPolicy) — a subscriber's QoS is its own choice of how it
// wants messages delivered to it and does not have to match the
// publisher's, but matching it here means the coordinator never
// downgrades a QoS-1 publish to QoS-0 delivery to itself.
func (m *Manager) Subscriptions() []broker.Subscription {
	return []broker.Subscription{
		{Filter: mqttproto.SubscribeHello, QoS: 1},
		{Filter: mqttproto.SubscribeLWT, QoS: 1},
		{Filter: mqttproto.SubscribeObserved, QoS: 1},
	}
}

// classify turns an inbound broker.Message's Retained flag into the
// observation time and provenance the store package's model requires. This
// is the one place in this codebase, alongside broker.newPublishReceivedHandler
// (which reads the RETAIN flag off the wire in the first place), that the
// Step 2 round 2 shared contract's central rule is actually enforced:
//
//   - retained == true: ObservedAt is nil (age unknown) and provenance is
//     ProvenanceRetainedBrokerState. m.now is deliberately NOT called here:
//     stamping any receipt time on a retained delivery, even labeled
//     somehow as "less trustworthy", would leave a real timestamp sitting
//     in the store for a future refactor to accidentally start reading as
//     freshness.
//   - retained == false: ObservedAt is m.now() and provenance is
//     ProvenanceAgentReport.
func (m *Manager) classify(retained bool) (*time.Time, store.Provenance) {
	if retained {
		return nil, store.ProvenanceRetainedBrokerState
	}
	now := m.now()
	return &now, store.ProvenanceAgentReport
}

// HandleMessage is a broker.MessageHandler: it parses msg's topic,
// dispatches to the matching decoder, and persists the result through
// store. A malformed or unparseable message is logged and skipped, never
// treated as fatal — one bad publisher must not be able to take inventory
// down. HandleMessage must not block for long (see requestTimeout); it runs
// on paho's publish-routing worker goroutine, not its read loop — see
// broker.MessageHandler's doc comment for exactly what that does and does
// not stall.
func (m *Manager) HandleMessage(msg broker.Message) {
	topic, err := mqttproto.ParseTopic(msg.Topic)
	if err != nil {
		// mqttproto.SubscribeObserved's "showmesh/nodes/+/observed/#"
		// filter also matches the bare parent topic
		// "showmesh/nodes/<node-id>/observed" (MQTT '#' matches zero or
		// more levels), which ParseTopic always rejects (observed requires
		// a subpath) — this is documented on the filter constant as an
		// expected, not corrupt, occurrence, so it is logged at Debug
		// rather than Warn like a genuinely malformed topic below.
		if strings.HasSuffix(msg.Topic, "/observed") {
			m.logger.Debug("ignoring bare observed parent topic (expected: SubscribeObserved's wildcard matches it, mqttproto.ParseTopic always rejects it)",
				"topic", msg.Topic)
			return
		}
		m.logger.Warn("skipping message on unparseable topic", "topic", msg.Topic, "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	switch topic.Kind {
	case mqttproto.TopicKindHello:
		m.handleHello(ctx, topic.NodeID, msg)
	case mqttproto.TopicKindLWT:
		m.handleLWT(ctx, topic.NodeID, msg)
	case mqttproto.TopicKindObserved:
		switch topic.Subpath {
		case "health":
			m.handleHealth(ctx, topic.NodeID, msg)
		case "assets":
			m.handleAssetInventory(ctx, topic.NodeID, msg)
		case "render":
			m.handleRender(ctx, topic.NodeID, msg)
		case "audio":
			m.handleAudio(topic.NodeID, msg)
		default:
			m.logger.Debug("ignoring observed subpath this step does not understand",
				"node_id", topic.NodeID, "subpath", topic.Subpath)
		}
	default:
		// SubscribeHello/SubscribeLWT/SubscribeObserved cannot themselves
		// deliver a Cmd/Result/Event topic, but ParseTopic's contract does
		// not promise that, so this stays a defensive skip rather than a
		// panic.
		m.logger.Debug("ignoring topic kind not handled by inventory", "kind", topic.Kind.String(), "topic", msg.Topic)
	}
}

// decodeEnvelope decodes and validates payload's envelope and checks its
// NodeID against topicNodeID (see mqttproto.CheckNodeID), the shared first
// step of handleHello/handleLWT/handleHealth.
func decodeEnvelope(payload []byte, topicNodeID string) (mqttproto.Envelope, error) {
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		return mqttproto.Envelope{}, err
	}
	if err := mqttproto.CheckNodeID(env, topicNodeID); err != nil {
		return mqttproto.Envelope{}, err
	}
	return env, nil
}

func (m *Manager) handleHello(ctx context.Context, nodeID string, msg broker.Message) {
	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("hello", nodeID, err)
		return
	}
	hello, err := mqttproto.DecodeHelloPayload(env)
	if err != nil {
		m.logMalformed("hello", nodeID, err)
		return
	}
	// An invalid capability set (duplicate IDs; see capability.Set.Validate)
	// is treated the same as any other malformed payload and the whole
	// hello is dropped, rather than stored with a set this package cannot
	// vouch for. Set.Validate does not check vocabulary membership — an
	// unknown or withdrawn capability ID is not an error here — only
	// internal well-formedness.
	if err := hello.Capabilities.Validate(); err != nil {
		m.logMalformed("hello", nodeID, err)
		return
	}

	observedAt, provenance := m.classify(msg.Retained)
	rec := store.HelloRecord{
		Label: hello.Label, Platform: hello.Platform, AgentVersion: hello.AgentVersion,
		BootID: hello.BootID, StartedAt: hello.StartedAt, Capabilities: hello.Capabilities,
		ObservedAt: observedAt, Provenance: provenance, Retained: msg.Retained,
	}
	if err := m.store.UpsertHello(ctx, nodeID, rec); err != nil {
		m.logger.Error("failed to store hello", "node_id", nodeID, "error", err)
		return
	}
	m.notify()
	m.recordLivenessTransition(ctx, nodeID)
	if m.onHello != nil {
		m.onHello(nodeID)
	}
}

func (m *Manager) handleLWT(ctx context.Context, nodeID string, msg broker.Message) {
	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("lwt", nodeID, err)
		return
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		m.logMalformed("lwt", nodeID, err)
		return
	}

	// The LWT topic goes through the exact same classify(msg.Retained) path
	// as hello and health: a retained replay (e.g. what a just-restarted
	// coordinator receives on subscribe) gets ObservedAt nil, never the
	// coordinator's own receipt time, so a six-hour-old retained
	// "online: true" can never be stamped as observed just now. See
	// store.LWTRecord's doc comment for why the previous "always stamp
	// receipt time, always ProvenanceBrokerLastWill" special case for this
	// topic was itself the bug: there is no wire-level way to tell a
	// broker-fired Will apart from the agent's own live publish to the same
	// topic, so uniformly labeling every delivery "broker_last_will"
	// mislabeled the agent's live "online: true" reports as something they
	// are not.
	//
	// deriveLiveness's offline branch never reads LWT.ObservedAt (an
	// uncontradicted offline is reported regardless of its age — see
	// liveness.go), so this change does not alter today's liveness verdicts
	// at all; it only stops a false freshness claim from sitting in the
	// stored record for a future reader (e.g. Step 3's read API) to display.
	observedAt, provenance := m.classify(msg.Retained)
	rec := store.LWTRecord{
		Online: lwt.Online, Reason: lwt.Reason,
		ObservedAt: observedAt, Provenance: provenance, Retained: msg.Retained,
	}
	if err := m.store.RecordLWT(ctx, nodeID, rec); err != nil {
		m.logger.Error("failed to store lwt", "node_id", nodeID, "error", err)
		return
	}
	m.notify()
	m.recordLivenessTransition(ctx, nodeID)
}

func (m *Manager) handleHealth(ctx context.Context, nodeID string, msg broker.Message) {
	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("health", nodeID, err)
		return
	}
	health, err := mqttproto.DecodeHealthPayload(env)
	if err != nil {
		m.logMalformed("health", nodeID, err)
		return
	}

	observedAt, provenance := m.classify(msg.Retained)
	rec := store.HealthRecord{
		BootID: health.BootID, Sequence: health.Sequence,
		AgentState: health.AgentState, UptimeMS: health.UptimeMS,
		ObservedAt: observedAt, Provenance: provenance, Retained: msg.Retained,
	}
	accepted, err := m.store.RecordHealth(ctx, nodeID, rec)
	if err != nil {
		m.logger.Error("failed to store health", "node_id", nodeID, "error", err)
		return
	}
	if !accepted {
		// Per the shared contract, a duplicate or reordered delivery within
		// the same boot session is expected under QoS 1 redelivery, so this
		// is Debug, not Warn: it is not an anomaly. Logged (Step 3 review
		// finding 3.9: this line existed once and was lost) because it is
		// the only trace that QoS-1 redelivery is actually occurring against
		// this node — nothing else here distinguishes "the agent published
		// twice" from "the agent has been silent". No notify() here: a
		// duplicate/reorder that RecordHealth ignored did not necessarily
		// change anything an API consumer would render differently — see
		// [Manager.onChange]'s doc comment ("never ... ignored as a
		// duplicate/reorder"). A live duplicate's observed_at refresh
		// (RecordHealth's own doc comment) is a real change most of the
		// time in wall-clock terms, but the hub's own tick already re-renders
		// on a fixed interval regardless (contract section 6.5), so missing
		// a prompt poke for this one case costs at most one tick's latency,
		// not correctness.
		m.logger.Debug("ignoring duplicate or reordered health heartbeat",
			"node_id", nodeID, "boot_id", health.BootID, "sequence", health.Sequence)
		return
	}
	m.notify()
	m.recordLivenessTransition(ctx, nodeID)
}

// handleAssetInventory ingests a node's asset inventory report (Track E
// seam E5/E6; ADR-028) into store.ReplaceNodeAssetInventory.
//
// A RETAINED delivery — the subscribe-time replay this coordinator gets
// on every (re)connect, per mqttproto.ObservedDeliveryPolicy — is
// deliberately NOT persisted as a fresh report. hello/lwt/health handle
// this identical situation via classify's nil ObservedAt ("age unknown");
// store.NodeAssetReportRecord.ReportedAt is a plain time.Time, not a
// *time.Time, so there is no equivalent "unknown" value to write here.
// Stamping m.now() on a retained replay would manufacture freshness for a
// report that may be hours old — exactly the "a retained MQTT message is
// not a fresh one" defect Step 5's lessons name — so this delivery is
// simply skipped: whatever row already exists (or none) is left exactly
// as accurate as it was, and the agent's own periodic republish
// (SHOWMESH_ASSET_INVENTORY_INTERVAL) supplies a genuinely live report
// before that row's staleness window elapses.
func (m *Manager) handleAssetInventory(ctx context.Context, nodeID string, msg broker.Message) {
	if msg.Retained {
		m.logger.Debug("ignoring retained asset inventory replay", "node_id", nodeID)
		return
	}

	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("asset inventory", nodeID, err)
		return
	}
	inv, err := mqttproto.DecodeAssetInventoryPayload(env)
	if err != nil {
		m.logMalformed("asset inventory", nodeID, err)
		return
	}

	// ReportedAt is this coordinator's OWN receipt time (m.now()), never
	// the envelope's SentAt: this is the same evidence this record's
	// freshness is later checked against (assetsync.StalenessWindow
	// compares it to the coordinator's own "now"), so both sides of that
	// comparison must live in the same clock domain — ADR-011's rule
	// applied to a report instead of a heartbeat.
	items := make([]store.NodeAssetInventoryRecord, 0, len(inv.Assets))
	for _, a := range inv.Assets {
		items = append(items, store.NodeAssetInventoryRecord{
			NodeID: nodeID, ContentHash: a.ContentHash, RuntimeFilename: a.Filename,
			SizeBytes: a.SizeBytes, VerifiedAt: a.VerifiedAt,
		})
	}
	report := store.NodeAssetReportRecord{NodeID: nodeID, ReportedAt: m.now(), Complete: inv.Complete, Reason: inv.Reason}

	if err := m.store.ReplaceNodeAssetInventory(ctx, nodeID, items, report); err != nil {
		m.logger.Error("failed to store asset inventory", "node_id", nodeID, "error", err)
		return
	}
	m.notify()
}

// handleRender ingests a node's render pipeline health report (Track B seam
// B2b; ADR-011, ADR-026) into m.renderSink, if one was registered — see
// [WithRenderSink]. A nil renderSink is not an error: it means nothing has
// wired render ingestion in yet, and the message is silently dropped
// exactly like any subpath this step does not understand.
//
// Unlike handleAssetInventory, a RETAINED delivery IS stored here, not
// skipped: [RenderSink.Put] (backed by noderender.Store) already carries
// its own retained/unknown-age distinction all the way through to the
// observation layer (see that package's buildValue), so there is no
// "store.NodeAssetReportRecord.ReportedAt has no unknown-age representation"
// gap here to work around — this payload's own model supports it directly.
func (m *Manager) handleRender(ctx context.Context, nodeID string, msg broker.Message) {
	if m.renderSink == nil {
		m.logger.Debug("ignoring render report: no render sink registered", "node_id", nodeID)
		return
	}

	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("render", nodeID, err)
		return
	}
	render, err := mqttproto.DecodeRenderPayload(env)
	if err != nil {
		m.logMalformed("render", nodeID, err)
		return
	}

	// receivedAt is this coordinator's own receipt time in BOTH branches —
	// bookkeeping (when the message was processed), never evidence of the
	// subject's own state. [RenderSink.Put] (backed by noderender.Store)
	// is the one place msg.Retained decides whether that also doubles as
	// ObservedAt (live) or is kept strictly separate from it (retained) —
	// see that package's buildValue, ADR-011's rule applied one layer
	// down. This deliberately does NOT go through [Manager.classify]:
	// classify's *time.Time return is store's ObservedAt convention, and
	// calling it here would tempt a future edit into passing that pointer
	// through as if it were this bookkeeping timestamp.
	m.renderSink.Put(nodeID, render, msg.Retained, m.now())
	m.observeRenderSurfaces(ctx, nodeID, render)
	m.notify()
}

// handleAudio ingests a node's audio discovery report into m.audioSink, if
// one was registered — see [WithAudioSink]. A nil audioSink silently drops
// the message. Unlike handleAssetInventory, a RETAINED delivery IS stored:
// [AudioPayload.ObservedAt] is the node's own evidence timestamp, so age
// is reasoned about from that, not from delivery kind.
func (m *Manager) handleAudio(nodeID string, msg broker.Message) {
	if m.audioSink == nil {
		m.logger.Debug("ignoring audio report: no audio sink registered", "node_id", nodeID)
		return
	}

	env, err := decodeEnvelope(msg.Payload, nodeID)
	if err != nil {
		m.logMalformed("audio", nodeID, err)
		return
	}
	audio, err := mqttproto.DecodeAudioPayload(env)
	if err != nil {
		m.logMalformed("audio", nodeID, err)
		return
	}

	// receivedAt is this coordinator's own receipt time — bookkeeping,
	// never evidence of the node's own state (see [Manager.handleRender]'s
	// identical comment on why this bypasses [Manager.classify]).
	m.audioSink.Put(nodeID, audio, m.now())
	m.notify()
}

// renderSurfaceKey identifies one surface's bookkeeping entry in
// renderBaseline/renderFailed. Keyed by node as well as surface id because
// [RenderSink] is keyed per node and this bookkeeping must never conflate
// two nodes that happen to report the same show.surface configuration id.
func renderSurfaceKey(nodeID, surfaceID string) string {
	return nodeID + "/" + surfaceID
}

// observeRenderSurfaces compares payload against this Manager instance's
// own record of each surface's last-seen restart count and failed-lockout
// state (renderBaseline/renderFailed) and appends an event for a genuine
// forward restart-count increase or a genuine transition into the failed
// lockout state — never for the first observation of a surface this
// process has made (see renderBaseline's doc comment on Manager) and never
// for a restart count that goes backward. A backward count is not a bug to
// investigate: the supervisor's RestartCount is scoped to one pipeline
// process lifetime (internal/agent/pipeline.Supervisor's own restartState),
// so it resets to zero whenever the agent process itself restarts, and a
// coordinator that treated that reset as "negative restarts" would either
// panic on an underflow or manufacture a nonsense event. The lower count is
// simply re-baselined, silently, exactly like a first observation.
func (m *Manager) observeRenderSurfaces(ctx context.Context, nodeID string, payload mqttproto.RenderPayload) {
	for _, s := range payload.Surfaces {
		key := renderSurfaceKey(nodeID, s.SurfaceID)

		m.renderMu.Lock()
		prevCount, knownCount := m.renderBaseline[key]
		m.renderBaseline[key] = s.RestartCount
		wasFailed, knownFailed := m.renderFailed[key]
		nowFailed := s.PipelineState == mqttproto.RenderPipelineStateFailed
		m.renderFailed[key] = nowFailed
		m.renderMu.Unlock()

		if knownCount && s.RestartCount > prevCount {
			m.appendRenderEvent(ctx, nodeID, s, "warning",
				fmt.Sprintf("render pipeline for surface %q on node %q restarted", s.SurfaceID, nodeID))
		}

		// A transition INTO the failed lockout, never merely "is failed" —
		// matching observeLiveness's "prev == liveness" no-op check — so a
		// surface that stays failed across many reports produces one event,
		// not one per poll.
		if knownFailed && !wasFailed && nowFailed {
			m.appendRenderEvent(ctx, nodeID, s, "critical",
				fmt.Sprintf("render pipeline for surface %q on node %q entered failed lockout", s.SurfaceID, nodeID))
		}
	}
}

// appendRenderEvent builds and appends one surface-scoped event, following
// [Manager.observeLiveness]'s shape: Source "mqtt-inventory", Resource is
// the surface (ADR-026 — a surface, not the node running it, is the thing
// observed), Details carries enough to be useful without a second lookup:
// which node, the new restart count, and the supervisor's own reason.
func (m *Manager) appendRenderEvent(ctx context.Context, nodeID string, s mqttproto.RenderSurfaceReport, severity, summary string) {
	detail := map[string]any{
		"nodeId":              nodeID,
		"surfaceId":           s.SurfaceID,
		"pipelineState":       s.PipelineState,
		"restartCount":        s.RestartCount,
		"consecutiveFailures": s.ConsecutiveFailures,
		"reason":              s.Reason,
	}
	details, err := json.Marshal(detail)
	if err != nil {
		// Unreachable: every value above is a plain string or number.
		// Recorded rather than dropped, so a future bug here is visible in
		// logs instead of silently losing the event.
		m.logger.Error("failed to encode render event details", "node_id", nodeID, "surface_id", s.SurfaceID, "error", err)
		details = nil
	}

	if _, err := m.store.AppendEvent(ctx, store.EventRecord{
		Source:   "mqtt-inventory",
		Resource: observation.ResourceRef{Kind: observation.ResourceSurface, ID: s.SurfaceID},
		Category: "render_pipeline",
		Severity: severity,
		Summary:  summary,
		Details:  json.RawMessage(details),
	}); err != nil {
		m.logger.Error("failed to append render event", "node_id", nodeID, "surface_id", s.SurfaceID, "error", err)
		return
	}
	m.notify()
}

func (m *Manager) logMalformed(kind, nodeID string, err error) {
	var unsupported *mqttproto.UnsupportedSchemaError
	if errors.As(err, &unsupported) {
		// A schema this coordinator does not recognize (e.g. a newer agent)
		// is expected forward-compatibility behavior, not corruption; see
		// pkg/mqttproto's package doc comment.
		m.logger.Debug("skipping message with unrecognized schema", "kind", kind, "node_id", nodeID, "error", err)
		return
	}
	m.logger.Warn("skipping malformed message", "kind", kind, "node_id", nodeID, "error", err)
}
