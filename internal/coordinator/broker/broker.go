// Package broker owns the coordinator's connection to the MQTT broker: the
// connection manager, the observed BrokerState, the readiness rule derived
// from it, and delivering inbound publishes (with their MQTT RETAIN flag
// preserved, see Message) to whatever subscriptions the caller registers.
// See NewBrokerManager and Readiness.
package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
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
}

// BrokerManager owns the coordinator's connection to the MQTT broker.
//
// Per ADR-008, broker loss is a management-plane outage only: a running
// show must survive it, and so must the coordinator process. BrokerManager
// never blocks startup on a successful connection and never causes the
// coordinator to exit because the broker is unreachable — the underlying
// autopaho connection manager retries forever with backoff.
type BrokerManager struct {
	cm *autopaho.ConnectionManager

	// now is the clock BrokerManager uses to stamp BrokerState. It is a
	// field (rather than a direct time.Now call) so tests can drive state
	// transitions with a fake clock instead of real sleeps.
	now func() time.Time

	mu    sync.Mutex
	state BrokerState

	wg sync.WaitGroup
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

	bm := &BrokerManager{now: time.Now}
	initAt := bm.now()
	bm.state = BrokerState{Connected: false, Since: initAt, ObservedAt: initAt}

	subscribeOpts := subscriptionsToOptions(subs)

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
			// restart; see subscribeAll's doc comment.
			subscribeAll(ctx, cm, subscribeOpts, logger)
		},
		OnConnectionDown: func() bool {
			bm.setConnected(false)
			logger.Warn("mqtt broker connection lost; will retry", "broker", cfg.MQTTBroker)
			return true // never give up retrying
		},
		OnConnectError: func(err error) {
			bm.setConnected(false)
			logger.Warn("mqtt broker connect attempt failed; will retry", "broker", cfg.MQTTBroker, "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID:          cfg.MQTTClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){newPublishReceivedHandler(handler)},
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

// setConnected records a fresh observation of the connection state. Since
// only moves when Connected actually changes value; ObservedAt always
// advances, since a re-confirmation of the same value is still new
// evidence.
func (b *BrokerManager) setConnected(connected bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if connected != b.state.Connected {
		b.state.Since = now
	}
	b.state.Connected = connected
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
//   - Not connected: not ready, reason "mqtt broker not connected".
//   - Connected but evidence stale: not ready. Per ADR-011 this is unknown,
//     not healthy, so it is reported as not-ready rather than papering over
//     the missing confirmation.
func (b *BrokerManager) Readiness() readiness.Report {
	state := b.State()

	if !state.Connected {
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
