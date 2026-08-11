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
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
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
}

// New builds a Manager backed by st. logger may be nil, in which case
// slog.Default() is used.
func New(st *store.Store, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{store: st, logger: logger, now: time.Now}
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
		if topic.Subpath == "health" {
			m.handleHealth(ctx, topic.NodeID, msg)
		} else {
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
	}
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
		// is Debug, not Warn: it is not an anomaly.
		m.logger.Debug("ignoring duplicate or reordered health heartbeat",
			"node_id", nodeID, "boot_id", health.BootID, "sequence", health.Sequence)
	}
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
