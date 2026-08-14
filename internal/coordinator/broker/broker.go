// Package broker owns the coordinator's connection to the MQTT broker: the
// connection manager, the observed BrokerState, the readiness rule derived
// from it, and delivering inbound publishes (with their MQTT RETAIN flag
// preserved, see Message) to whatever subscriptions the caller registers.
// See NewBrokerManager and Readiness.
package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
)

// brokerProbeInterval is how often the background probe goroutine re-checks
// the broker connection and re-stamps BrokerState.ObservedAt, independent of
// the OnConnectionUp/OnConnectionDown callbacks.
const brokerProbeInterval = 5 * time.Second

// brokerProbeTimeout bounds each individual probe so a hung check cannot
// delay the next one or block shutdown.
const brokerProbeTimeout = 250 * time.Millisecond

// evidenceStalenessWindow bounds how old BrokerState.ObservedAt may be
// before Readiness treats a "connected" observation as unknown rather than
// healthy, per ADR-011 (stale or insufficient evidence becomes unknown, not
// healthy). Partition detection latency is ultimately bounded by the MQTT
// keepalive interval (see NewBrokerManager's KeepAlive setting below), so
// this window is a floor on confidence, not a guarantee: a connection can
// go silently dead and still read as fresh for up to one keepalive cycle
// after the partition starts.
const evidenceStalenessWindow = 15 * time.Second

// keepAliveSeconds is evidenceStalenessWindow expressed in the uint16
// seconds autopaho's KeepAlive field requires. This is a constant
// conversion (evidenceStalenessWindow and time.Second are both constants),
// so if evidenceStalenessWindow is ever raised past 65535 seconds this
// fails to compile instead of silently wrapping the way a runtime
// uint16(float64) conversion would. Keeping this at or below
// evidenceStalenessWindow is what makes that window a meaningful floor on
// confidence rather than a number disconnected from the mechanism that
// actually detects loss — see evidenceStalenessWindow's doc comment.
const keepAliveSeconds = uint16(evidenceStalenessWindow / time.Second)

// Subscription is one MQTT topic filter the coordinator wants active for
// as long as it is connected to the broker.
type Subscription struct {
	Filter string
	QoS    byte
}

// Message is one inbound publish, normalized out of paho's wire type.
//
// Retained is the single most consequential field on this type. Per the
// Step 2 round 2 shared design contract, it is what distinguishes a live
// publish (MQTT RETAIN=0, proof the sender is doing something right now)
// from a retained-store replay (RETAIN=1, which only proves the broker
// once held this value — possibly hours or days ago). A caller that stamps
// its own receipt time as an observation time for a message with
// Retained==true reproduces exactly the failure ADR-011 exists to prevent:
// a long-dead node's last heartbeat looking perfectly fresh forever. See
// internal/coordinator/inventory, the one caller in this codebase that
// reads this field.
type Message struct {
	Topic    string
	Payload  []byte
	Retained bool
}

// MessageHandler is called for every inbound publish matching one of the
// coordinator's subscriptions. It should still not block for long: in
// paho.golang v0.23 (see go.mod), OnPublishReceived callbacks do NOT run on
// the connection's read loop. The reader goroutine (Client.incoming) only
// decodes each packet and hands PUBLISH packets to a buffered channel
// (Client.publishPackets, sized to the broker's ReceiveMaximum); a separate
// worker goroutine (Client.routePublishPackets) drains that channel and
// invokes the OnPublishReceived callbacks, one packet at a time, then sends
// the packet's PUBACK. The MQTT keepalive ping handler runs on its own
// goroutine too. So a slow handler here delays this client's own PUBACKs
// (which can eventually make the QoS 1 sender re-deliver) and, once the
// buffered channel fills, delays acking further inbound publishes — it
// does not stall the read loop or the ping handler the way an earlier
// version of this comment claimed, unverified, that it did. This has been
// read from paho.golang's source (Client.incoming/routePublishPackets in
// client.go), not measured under load; requestTimeout in
// internal/coordinator/inventory exists so a hung store call cannot grow
// that PUBACK backlog indefinitely, which remains the right precaution
// regardless of exactly which paho internals a slow handler affects.
type MessageHandler func(Message)

// newPublishReceivedHandler adapts a [MessageHandler] to paho's
// OnPublishReceived callback shape. It is a standalone function, rather
// than an inline closure inside NewBrokerManager, specifically so its
// RETAIN-flag plumbing can be unit tested without a real broker connection
// (see broker_test.go) — the one piece of "does the coordinator read the
// flag on every incoming message" that a unit test can actually prove.
func newPublishReceivedHandler(handler MessageHandler) func(paho.PublishReceived) (bool, error) {
	return func(pr paho.PublishReceived) (bool, error) {
		if handler != nil {
			handler(Message{
				Topic:    pr.Packet.Topic,
				Payload:  pr.Packet.Payload,
				Retained: pr.Packet.Retain,
			})
		}
		// Always report the message as handled: this is the only consumer
		// in the process, so there is no second OnPublishReceived callback
		// that needs a chance to also see it.
		return true, nil
	}
}

// subscriber is the subset of *autopaho.ConnectionManager's method set
// subscribeAll needs, so tests can exercise the resubscribe logic with a
// fake instead of a real broker connection.
type subscriber interface {
	Subscribe(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error)
}

// subscribeAll (re)establishes every one of the coordinator's
// subscriptions in one MQTT SUBSCRIBE call. It is called from
// OnConnectionUp, which autopaho invokes both on the initial connection and
// on every reconnection after a broker outage (see
// autopaho/examples/basics in the eclipse/paho.golang module, which this
// follows) — that is the mechanism that makes a subscription survive a
// broker restart, since nothing about a subscription is durable on the
// broker side unless the session itself persists, and this codebase does
// not currently rely on session persistence (CleanStartOnInitialConnection
// is left at its default). subscribeAll always sends the complete set
// again on every call rather than tracking what is "already subscribed",
// which is what makes it correct to call repeatedly from OnConnectionUp
// without assuming anything survived the outage.
//
// A subscribe failure here is logged loudly rather than silently retried:
// per the Step 2 round 2 task spec, a subscription that silently fails to
// re-establish after a broker restart is invisible until a node changes
// state and nobody notices, which is exactly the failure mode ADR-011
// exists to prevent one layer up.
func subscribeAll(ctx context.Context, sub subscriber, opts []paho.SubscribeOptions, logger *slog.Logger) {
	if len(opts) == 0 {
		return
	}
	if _, err := sub.Subscribe(ctx, &paho.Subscribe{Subscriptions: opts}); err != nil {
		logger.Error("mqtt subscribe failed after connect; inventory will not receive updates until the next reconnect",
			"error", err)
	}
}

// subscriptionsToOptions converts this package's own [Subscription] type to
// paho's wire-level SubscribeOptions. RetainAsPublished is always left
// false (the zero value) and never made configurable here: per the shared
// contract, the coordinator must be able to tell a retained replay from a
// live publish, and RetainAsPublished=true would make the broker echo
// RETAIN=1 on every subsequent live publish on a topic that was ever
// retained, destroying that distinction for the lifetime of the
// subscription.
func subscriptionsToOptions(subs []Subscription) []paho.SubscribeOptions {
	opts := make([]paho.SubscribeOptions, len(subs))
	for i, s := range subs {
		opts[i] = paho.SubscribeOptions{
			Topic:             s.Filter,
			QoS:               s.QoS,
			RetainAsPublished: false,
		}
	}
	return opts
}

// BrokerState is evidence about the broker connection, not just a flag.
// ADR-011 requires observations to carry freshness; a value with no
// observation time cannot degrade to unknown. The canonical observation
// type arrives in Step 3 with the collector model (OBSERVABILITY 4.1);
// this is the local minimum that keeps the precedent correct.
type BrokerState struct {
	Connected bool

	// Since is when the current Connected value was first observed (i.e.
	// when it last changed). It does not move while Connected is
	// re-confirmed at the same value.
	Since time.Time

	// ObservedAt is when this state was last confirmed, whether or not
	// Connected changed. Callers use it to judge freshness: evidence older
	// than a staleness window is not proof of current health.
	ObservedAt time.Time

	// Rejected is true when the most recent connection attempt failed
	// because the broker authenticated the CONNECT packet and explicitly
	// refused it (an MQTT v5 CONNACK reason code in the authorization
	// family — see isAuthReasonCode), as opposed to being unreachable
	// (connection refused, DNS failure, timeout — none of which ever
	// produce a CONNACK at all). Per ADR-024 decision 10 this is a
	// permanent, self-inflicted condition (a wrong or revoked broker
	// credential) rather than a transient network fault, and CLAUDE.md's
	// standing constraint that the coordinator starts and stays up with no
	// broker reachable now extends to a broker that actively rejects it —
	// this field is what lets Readiness (below) and anything reading
	// BrokerState say so distinctly rather than reporting the same "mqtt
	// broker not connected" an ordinary outage would.
	//
	// Cleared back to false the moment a connection actually succeeds (see
	// setConnected): a stale "rejected" reading must not survive past the
	// evidence it was based on, the same ADR-011 principle that governs
	// every other field here.
	Rejected bool

	// RejectReasonCode and RejectReason are the CONNACK reason code (see
	// isAuthReasonCode's doc comment for which values set Rejected) and any
	// broker-supplied reason string from the rejection that produced
	// Rejected=true. Both are the zero value whenever Rejected is false.
	RejectReasonCode byte
	RejectReason     string
}

// mqttClient is the subset of *autopaho.ConnectionManager's method set the
// broker package depends on: outbound publish, dynamic subscribe and
// unsubscribe for response waiters (see response.go), the liveness probe,
// and clean shutdown. Extracting it — the same technique [subscriber]
// already uses for subscribeAll — is what lets response_test.go exercise
// Publish and AwaitResponse against a fake in-process implementation
// instead of a real broker connection; *autopaho.ConnectionManager
// satisfies it without any adaptation, so production wiring is unchanged.
type mqttClient interface {
	Publish(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error)
	Subscribe(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error)
	Unsubscribe(ctx context.Context, u *paho.Unsubscribe) (*paho.Unsuback, error)
	AwaitConnection(ctx context.Context) error
	Disconnect(ctx context.Context) error
}

// BrokerManager owns the coordinator's connection to the MQTT broker.
//
// Per ADR-008, broker loss is a management-plane outage only: a running
// show must survive it, and so must the coordinator process. BrokerManager
// never blocks startup on a successful connection and never causes the
// coordinator to exit because the broker is unreachable — the underlying
// autopaho connection manager retries forever with backoff. The same rule
// governs [BrokerManager.Publish] and [BrokerManager.AwaitResponse]: with no
// broker, each fails the one operation attempted rather than blocking or
// panicking (see response.go).
type BrokerManager struct {
	cm mqttClient

	// logger is used by response.go's best-effort housekeeping (an
	// unsubscribe issued after the last response waiter on a topic
	// releases — see releaseResponseWaiter) that has no caller to return an
	// error to. Falls back to slog.Default() via [BrokerManager.log] when
	// unset, which happens for BrokerManager values built directly in
	// tests rather than through [NewBrokerManager].
	logger *slog.Logger

	// now is the clock BrokerManager uses to stamp BrokerState. It is a
	// field (rather than a direct time.Now call) so tests can drive state
	// transitions with a fake clock instead of real sleeps.
	now func() time.Time

	mu    sync.Mutex
	state BrokerState

	// respMu guards respTopics, respTopicLocks and nextWaiterID: the
	// response-waiter registry response.go's AwaitResponse machinery uses.
	// Deliberately a separate lock from mu (BrokerState's), so a burst of
	// MQTT response step traffic can never contend with, or be blocked by,
	// BrokerState reads/writes on the readiness path.
	respMu       sync.Mutex
	respTopics   map[string]*responseTopicState
	nextWaiterID atomic.Uint64

	// respTopicLocks holds one *sync.Mutex per response topic ever
	// registered, returned by topicLock (below) and held by
	// registerResponseWaiter/releaseResponseWaiter (response.go) across
	// their network SUBSCRIBE/UNSUBSCRIBE calls — see topicLock's doc
	// comment for why this has to live here, outside responseTopicState
	// itself, and review finding 3 on commit 9dcab74 for the race this
	// closes.
	//
	// Entries are deliberately never removed: response topics come from
	// operator-authored show macro definitions (STEP-9-SPEC.md §7), not
	// arbitrary or attacker-controlled input, so the set of distinct topics
	// this map ever holds is bounded by the deployment's own configuration
	// — the same reasoning [Registry]'s own map (registry.go), which also
	// never shrinks, already relies on. This is not the unbounded,
	// caller-triggerable growth LESSONS.md's "unbounded write on a failure
	// path" rule warns about.
	respTopicLocks map[string]*sync.Mutex

	// fixedSubs is the subscription set passed to NewBrokerManager: the
	// coordinator's own long-lived subscriptions (inventory's hello/lwt
	// filters), which do not change for the life of the process. Read by
	// subscriptionsToResubscribe on every (re)connection; never mutated
	// after NewBrokerManager returns, so it needs no lock of its own.
	fixedSubs []Subscription

	wg sync.WaitGroup
}

// subscriptionsToResubscribe computes the complete MQTT SUBSCRIBE set to
// send on every (re)connection: fixedSubs plus every response topic that
// currently has at least one live waiter (see registerResponseWaiter in
// response.go), read fresh at call time rather than captured once.
//
// This distinction matters because a response waiter's subscription is
// scoped to one in-flight AwaitResponse call, not to the broker connection's
// own lifetime: a waiter can be registered, and a reconnect can happen,
// entirely independently of each other. Before this method existed,
// OnConnectionUp resent only the construction-time fixed slice, so a broker
// outage during an active AwaitResponse call silently dropped that
// subscription — the external responder's eventual answer would then arrive
// at the broker with nowhere registered to route it to, and the waiter
// would run out its full deadline having genuinely never had a chance to
// see it, indistinguishable on the wire from a responder that never
// answered at all.
//
// A topic present in both sets is only sent once, at the fixed set's QoS:
// resubscribing a topic filter you already hold is a harmless, idempotent
// refresh, but sending it twice in the same SUBSCRIBE packet is needless.
func (b *BrokerManager) subscriptionsToResubscribe() []paho.SubscribeOptions {
	opts := subscriptionsToOptions(b.fixedSubs)
	seen := make(map[string]bool, len(opts))
	for _, o := range opts {
		seen[o.Topic] = true
	}

	b.respMu.Lock()
	defer b.respMu.Unlock()
	for topic, state := range b.respTopics {
		if seen[topic] {
			continue
		}
		opts = append(opts, paho.SubscribeOptions{
			Topic: topic,
			QoS:   state.qos,
			// Left false for the same reason subscriptionsToOptions and
			// registerResponseWaiter both already document: RETAIN=1 must
			// only ever mean "replayed from the retained store", for the
			// lifetime of this subscription including across a reconnect
			// that resubscribes it here.
			RetainAsPublished: false,
			// RetainHandling deliberately left at its zero value — see
			// registerResponseWaiter's own SubscribeOptions in response.go
			// for why review finding 2's optional RetainHandling=2
			// hardening is not applied here either.
		})
		seen[topic] = true
	}
	return opts
}

// topicLock returns the *sync.Mutex serializing every network
// SUBSCRIBE/UNSUBSCRIBE call response.go's registerResponseWaiter and
// releaseResponseWaiter issue for topic, creating it on first use.
//
// This has to be a mutex keyed by the topic STRING, held independently of
// any particular [responseTopicState] value, because responseTopicState
// itself is deleted from respTopics and recreated across a topic's
// subscribe/unsubscribe lifecycle (see releaseResponseWaiter and
// registerResponseWaiter): a mutex embedded in that struct would be a new,
// unrelated lock on every recreation and would serialize nothing across the
// boundary where the race actually lives. Returning the SAME *sync.Mutex
// instance across however many times a topic's responseTopicState has been
// created and deleted is what lets a releaseResponseWaiter's in-flight
// UNSUBSCRIBE and a concurrent registerResponseWaiter's SUBSCRIBE for the
// identical topic string actually exclude each other — see review finding
// 3 on commit 9dcab74 and response.go's own doc comments on both functions
// for the full race this closes.
func (b *BrokerManager) topicLock(topic string) *sync.Mutex {
	b.respMu.Lock()
	defer b.respMu.Unlock()
	if b.respTopicLocks == nil {
		b.respTopicLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := b.respTopicLocks[topic]
	if !ok {
		mu = &sync.Mutex{}
		b.respTopicLocks[topic] = mu
	}
	return mu
}

// log returns the logger response.go's housekeeping paths use, falling back
// to slog.Default() for a BrokerManager built directly (e.g. in a test)
// rather than through [NewBrokerManager].
func (b *BrokerManager) log() *slog.Logger {
	if b.logger != nil {
		return b.logger
	}
	return slog.Default()
}

// NewBrokerManager begins connecting to cfg.MQTTBroker in the background and
// returns immediately. It does not wait for the connection to come up, and
// it does not return an error merely because the broker is currently
// unreachable — connection failures are logged and retried.
//
// subs is subscribed (via [subscribeAll]) on every successful connection,
// including every reconnection, and every inbound publish matching one of
// them is delivered to handler (see [newPublishReceivedHandler]). subs and
// handler may be nil/empty for a BrokerManager that only needs the
// connection itself and no message traffic.
//
// The probe goroutine that periodically re-confirms BrokerState is tied to
// ctx: it exits when ctx is done, so callers must cancel ctx (or call
// Disconnect) on shutdown to avoid leaking it.
func NewBrokerManager(ctx context.Context, cfg config.Config, logger *slog.Logger, subs []Subscription, handler MessageHandler) (*BrokerManager, error) {
	if logger == nil {
		logger = slog.Default()
	}

	serverURL, err := url.Parse(cfg.MQTTBroker)
	if err != nil {
		// Config.Validate should already have caught this; guard anyway
		// since a malformed URL here would otherwise loop forever without
		// ever attempting a connection.
		return nil, fmt.Errorf("parsing mqtt broker url %q: %w", cfg.MQTTBroker, err)
	}

	bm := &BrokerManager{now: time.Now, logger: logger}
	initAt := bm.now()
	bm.state = BrokerState{Connected: false, Since: initAt, ObservedAt: initAt}

	// fixedSubs is copied rather than aliasing the caller's slice: nothing
	// today mutates it after this call, but subscriptionsToResubscribe reads
	// it on every reconnect for the life of the process, and a caller
	// reusing or mutating its own backing array later must not be able to
	// change what gets resubscribed.
	bm.fixedSubs = append([]Subscription(nil), subs...)

	// combinedHandler is the ONE process-wide OnPublishReceived callback
	// slot (see MessageHandler's doc comment): it fans every inbound
	// publish out to the caller-supplied handler unchanged, exactly as
	// before this field existed, and also to response.go's waiter registry
	// (dispatchToWaiters), so a second consumer of inbound messages never
	// needs — and must never claim — a second slot of its own. handler
	// runs first and unconditionally, so an AwaitResponse caller can never
	// alter what the original subscriber sees.
	combinedHandler := func(m Message) {
		if handler != nil {
			handler(m)
		}
		bm.dispatchToWaiters(m)
	}

	clientCfg := autopaho.ClientConfig{
		ServerUrls: []*url.URL{serverURL},
		// KeepAlive is deliberately tied to evidenceStalenessWindow (above):
		// partition detection latency is ultimately bounded by this
		// interval, since a silent network partition (no TCP FIN) is only
		// caught when a keepalive round-trip fails. Keeping it at or below
		// the staleness window is what makes that window a meaningful floor
		// on confidence rather than a number disconnected from the
		// mechanism that actually detects loss. See keepAliveSeconds for why
		// this is a compile-time constant conversion rather than a runtime
		// one.
		KeepAlive:        keepAliveSeconds,
		ConnectTimeout:   10 * time.Second,
		ReconnectBackoff: autopaho.DefaultExponentialBackoff(),
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			bm.setConnected(true)
			logger.Info("mqtt broker connection up", "broker", cfg.MQTTBroker, "client_id", cfg.MQTTClientID)
			// Re-subscribing here, unconditionally, on every call (including
			// every reconnect) is what makes a subscription survive a broker
			// restart; see subscribeAll's doc comment. The set is computed
			// fresh on every call via subscriptionsToResubscribe, not
			// captured once at construction, so a response waiter that is
			// live right now (rather than at NewBrokerManager time) also
			// survives the reconnect — see that method's doc comment.
			subscribeAll(ctx, cm, bm.subscriptionsToResubscribe(), logger)
		},
		OnConnectionDown: func() bool {
			bm.setConnected(false)
			logger.Warn("mqtt broker connection lost; will retry", "broker", cfg.MQTTBroker)
			return true // never give up retrying
		},
		OnConnectError: func(err error) {
			// ADR-024 decision 10: with allow_anonymous disabled on the
			// reference broker, a CONNACK authorization rejection is a
			// permanent, self-inflicted condition (a wrong or missing
			// coordinator credential) that presents identically to an
			// ordinary transient outage unless told apart here. This is
			// NOT treated as fatal — the underlying autopaho connection
			// manager keeps retrying with backoff regardless of which
			// branch below runs, exactly as CLAUDE.md's standing
			// constraint 13 (starts and stays up with no broker reachable)
			// already required for a plain outage, now extended to a
			// broker that actively rejects the coordinator. The only
			// difference is what gets recorded in BrokerState and logged.
			if rejected, code, reason := classifyConnectError(err); rejected {
				bm.setRejected(code, reason)
				logger.Error("mqtt broker rejected connection: not authorized; this is a permanent condition (wrong or revoked credential), not a transient network fault, and will not resolve by retrying — the coordinator is NOT exiting and will keep retrying anyway in case the credential is fixed, but see SHOWMESH_MQTT_USERNAME/SHOWMESH_MQTT_PASSWORD",
					"broker", cfg.MQTTBroker, "client_id", cfg.MQTTClientID, "reason_code", code, "reason", reason)
				return
			}
			bm.setConnected(false)
			logger.Warn("mqtt broker connect attempt failed; will retry", "broker", cfg.MQTTBroker, "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID:          cfg.MQTTClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){newPublishReceivedHandler(combinedHandler)},
		},
	}

	if cfg.MQTTUsername != "" {
		clientCfg.ConnectUsername = cfg.MQTTUsername
		clientCfg.ConnectPassword = []byte(cfg.MQTTPassword)
	}

	cm, err := autopaho.NewConnection(ctx, clientCfg)
	if err != nil {
		// This only happens for programmer errors (e.g. no server URLs);
		// it is not a "broker unreachable" condition.
		return nil, fmt.Errorf("starting mqtt connection manager: %w", err)
	}
	bm.cm = cm

	bm.wg.Add(1)
	go bm.runProbe(ctx)

	return bm, nil
}

// runProbe periodically re-confirms the broker connection independent of
// the OnConnectionUp/OnConnectionDown callbacks, so ObservedAt advances even
// when nothing changes and evidence can still go stale (and degrade to
// unknown, per ADR-011) if this loop itself stops running. It exits when
// ctx is done.
func (b *BrokerManager) runProbe(ctx context.Context) {
	defer b.wg.Done()

	ticker := time.NewTicker(brokerProbeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, brokerProbeTimeout)
			err := b.cm.AwaitConnection(probeCtx)
			cancel()
			b.setConnected(err == nil)
		}
	}
}

// isAuthReasonCode reports whether code is an MQTT v5 CONNACK reason code
// in the "the broker understood who I am and refused me" family, as
// opposed to a transport-level failure (connection refused, timeout, DNS
// failure) that never reaches a CONNACK at all, or a non-authorization
// CONNACK failure (e.g. the broker being busy). Per ADR-024 decision 10
// this is the reason-code family the coordinator must surface as evidence
// distinct from "broker unreachable" — see BrokerState.Rejected's doc
// comment. Duplicated in internal/agent/mqtt.go's own isAuthReasonCode
// rather than shared: this builder's task deliberately scoped ownership to
// internal/agent, internal/coordinator/broker, deploy, and
// broker-authentication parts of test/integration, and not pkg/mqttproto,
// so the two copies stay independent the same way
// internal/agent/config's package doc comment already explains for
// internal/coordinator/config (different processes, different operators,
// expected to diverge). Keep both in sync if this reason-code family ever
// changes; that risk is the cost of not touching a package outside this
// task's ownership.
func isAuthReasonCode(code byte) bool {
	switch code {
	case packets.ConnackBadUsernameOrPassword, // 0x86
		packets.ConnackNotAuthorized,           // 0x87
		packets.ConnackBanned,                  // 0x8A
		packets.ConnackBadAuthenticationMethod: // 0x8C
		return true
	}
	return false
}

// classifyConnectError inspects an error passed to autopaho's
// OnConnectError callback and reports whether it represents a CONNACK-level
// authorization rejection, mirroring internal/agent/mqtt.go's function of
// the same name and same behavior (see that copy's doc comment for the
// full reasoning, including why errors.As — not a type assertion — is used
// and what autopaho.ConnackError being present, or absent, actually
// proves). Factored out of the OnConnectError closure so it is directly
// unit testable without dialing a broker; see broker_test.go.
func classifyConnectError(err error) (rejected bool, code byte, reason string) {
	var connackErr *autopaho.ConnackError
	if !errors.As(err, &connackErr) {
		return false, 0, ""
	}
	if !isAuthReasonCode(connackErr.ReasonCode) {
		return false, connackErr.ReasonCode, connackErr.Reason
	}
	return true, connackErr.ReasonCode, connackErr.Reason
}

// setConnected records a fresh observation of the connection state. Since
// only moves when Connected actually changes value; ObservedAt always
// advances, since a re-confirmation of the same value is still new
// evidence.
//
// A successful call always clears Rejected: a live connection is direct,
// current proof that whatever credential problem produced an earlier
// rejection (if any) no longer applies, and per ADR-011 evidence must not
// outlive what it was based on.
func (b *BrokerManager) setConnected(connected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if connected != b.state.Connected {
		b.state.Since = now
	}
	b.state.Connected = connected
	if connected {
		b.state.Rejected = false
		b.state.RejectReasonCode = 0
		b.state.RejectReason = ""
	}
	b.state.ObservedAt = now
}

// setRejected records a fresh observation of a CONNACK authorization
// rejection: Connected becomes false (a rejected CONNECT is not a
// connection), Rejected becomes true, and the reason code/string are
// recorded for Readiness (and any other BrokerState reader) to report. See
// BrokerState.Rejected's doc comment for why this is tracked separately
// from an ordinary "not connected" observation.
func (b *BrokerManager) setRejected(code byte, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if b.state.Connected {
		b.state.Since = now
	}
	b.state.Connected = false
	b.state.Rejected = true
	b.state.RejectReasonCode = code
	b.state.RejectReason = reason
	b.state.ObservedAt = now
}

// State returns the most recently observed BrokerState. Safe for concurrent
// use.
func (b *BrokerManager) State() BrokerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Readiness reports readiness from BrokerState, per ADR-011: an observation
// is only as good as its freshness.
//
//   - Connected and evidence fresh (ObservedAt within evidenceStalenessWindow):
//     Ready.
//   - Not connected, and the last attempt was an authorization rejection
//     (ADR-024 decision 10): not ready, reason "mqtt broker rejected
//     connection (not authorized)", with the reason code in Details — kept
//     distinct from the plain "not connected" case below because an
//     operator (or an automated check) reading this must be able to tell
//     "credential problem, fix the config" apart from "network problem,
//     check the wire", per CLAUDE.md's Step 5 "GET-only is not read-only"
//     family of lessons: a fault that presents identically to a different
//     fault sends whoever is debugging it to the wrong place.
//   - Not connected, no rejection on record: not ready, reason "mqtt broker
//     not connected".
//   - Connected but evidence stale: not ready. Per ADR-011 this is unknown,
//     not healthy, so it is reported as not-ready rather than papering over
//     the missing confirmation.
func (b *BrokerManager) Readiness() readiness.Report {
	state := b.State()

	if !state.Connected {
		if state.Rejected {
			details := map[string]any{
				"connected":        state.Connected,
				"rejected":         state.Rejected,
				"rejectReasonCode": state.RejectReasonCode,
			}
			if state.RejectReason != "" {
				details["rejectReason"] = state.RejectReason
			}
			return readiness.Report{
				Ready:      false,
				Reason:     "mqtt broker rejected connection (not authorized)",
				ObservedAt: state.ObservedAt,
				Details:    details,
			}
		}
		return readiness.Report{
			Ready:      false,
			Reason:     "mqtt broker not connected",
			ObservedAt: state.ObservedAt,
			Details: map[string]any{
				"connected": state.Connected,
			},
		}
	}

	age := time.Since(state.ObservedAt)
	if age > evidenceStalenessWindow {
		return readiness.Report{
			Ready:      false,
			Reason:     "mqtt broker evidence is stale",
			ObservedAt: state.ObservedAt,
			Details: map[string]any{
				"connected": state.Connected,
			},
		}
	}

	return readiness.Report{
		Ready:      true,
		ObservedAt: state.ObservedAt,
	}
}

// Disconnect shuts down the broker connection cleanly, honoring ctx's
// deadline. The probe goroutine is stopped via ctx cancellation by the
// caller (the same ctx passed to NewBrokerManager, which the caller must
// cancel before or during the call to Disconnect); Disconnect then waits
// for that goroutine to actually exit before returning, so shutdown joins
// it instead of leaking it. The wait is bounded by ctx: a probe goroutine
// that fails to exit (e.g. because the caller never canceled its ctx)
// cannot block shutdown past ctx's own deadline.
func (b *BrokerManager) Disconnect(ctx context.Context) error {
	var err error
	if b.cm != nil {
		err = b.cm.Disconnect(ctx)
	}

	probeDone := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(probeDone)
	}()

	select {
	case <-probeDone:
	case <-ctx.Done():
	}

	return err
}
