package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// keepAliveSeconds is the MQTT keepalive interval this agent negotiates at
// CONNECT.
//
// SHOWMESH CHOICE, NOT A SHARED-CONTRACT VALUE: unlike
// internal/coordinator/broker's keepAliveSeconds (deliberately tied to that
// package's own evidenceStalenessWindow), this constant only bounds how
// quickly the broker notices a dead TCP connection and fires this agent's
// registered Last Will. It is independent of HeartbeatInterval (this
// package's own publish cadence) and of the coordinator's health-staleness
// window (the coordinator's policy, not this agent's concern — see the
// round-2 shared contract's "Timing values" section). 30 seconds is an
// unmeasured, conservative guess.
const keepAliveSeconds = uint16(30)

// mqttConnectTimeout bounds each individual connection attempt.
const mqttConnectTimeout = 10 * time.Second

// mqttConn adapts *autopaho.ConnectionManager to this package's Publisher
// and Conn interfaces, so production code and tests share the same call
// shape (see publisher.go).
type mqttConn struct {
	cm *autopaho.ConnectionManager
}

func (m *mqttConn) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	_, err := m.cm.Publish(ctx, &paho.Publish{
		QoS:     qos,
		Retain:  retain,
		Topic:   topic,
		Payload: payload,
	})
	return err
}

func (m *mqttConn) Disconnect(ctx context.Context) error {
	return m.cm.Disconnect(ctx)
}

// buildWillMessage builds the paho Will registered at CONNECT time for
// nodeID: an LWT envelope with online=false and willDisconnectReason,
// delivered to nodeID's LWT topic with mqttproto.LWTDeliveryPolicy's retain
// and QoS.
//
// The envelope's SentAt is stamped NOW, at connect time — via time.Now,
// the same clock every other envelope in this package uses — NOT at the
// moment the broker eventually fires this Will. The broker republishes
// this exact payload verbatim on an unexpected disconnect, so by the time
// a coordinator receives it, SentAt may already be stale by the entire
// length of the session that just ended. mqttproto.LWTPayload's doc
// comment carries this warning at the payload-type level; it is repeated
// here because this is the one place in the agent that actually stamps the
// value, and "used as a time of death" is exactly the mistake a reader
// here could otherwise make. Per ADR-011 and the shared contract, the
// coordinator's own receipt time is the correct observation time for
// whatever eventually arrives on this topic — this function has no say in
// that.
func buildWillMessage(nodeID string) (*paho.WillMessage, error) {
	topic, err := mqttproto.LWTTopic(nodeID)
	if err != nil {
		return nil, fmt.Errorf("building lwt topic for will: %w", err)
	}

	env, err := buildLWTEnvelope(time.Now, nodeID, false, willDisconnectReason)
	if err != nil {
		return nil, fmt.Errorf("building will envelope: %w", err)
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshaling will envelope: %w", err)
	}

	return &paho.WillMessage{
		Topic:   topic,
		QoS:     mqttproto.LWTDeliveryPolicy.QoS,
		Retain:  mqttproto.LWTDeliveryPolicy.Retain,
		Payload: payload,
	}, nil
}

// newMQTTConn starts connecting to cfg.MQTTBroker in the background and
// returns immediately, mirroring
// internal/coordinator/broker.NewBrokerManager: it never blocks on a
// successful connection and never causes the agent to exit because the
// broker is unreachable — the underlying autopaho connection manager
// retries forever with backoff. A node agent that dies because it cannot
// reach the broker is useless exactly when it is most needed.
//
// The Will registered here (see buildWillMessage) fires only on an
// unexpected disconnect (network loss, crash, killed process); see
// shutdownCleanly for the clean-stop path, which the Will deliberately
// does NOT cover — a normal MQTT DISCONNECT tells the broker to discard
// the registered Will.
//
// On every successful connect, including every reconnect, OnConnectionUp
// spawns publishAdvertisement in its own goroutine (OnConnectionUp itself
// "must not block", per autopaho's ClientConfig doc comment) to
// (re-)publish hello and online=true — required because a broker that lost
// its persistence file has no retained state of its own to fall back on.
//
// OnConnectionUp also signals heartbeatConnected, non-blockingly, on every
// successful connect and reconnect: see runHeartbeat's doc comment on its
// own connected parameter for why. heartbeatConnected should be buffered
// (capacity >= 1) so a connect that happens before anything is listening on
// it — entirely possible, since this callback can fire before Run's
// heartbeat goroutine has started selecting on the channel — is queued
// rather than lost; the send here uses select/default specifically so this
// callback can never block on it either way, honoring the same "must not
// block" contract publishAdvertisement's goroutine exists to respect.
func newMQTTConn(ctx context.Context, cfg config.Config, bootID string, startedAt time.Time, heartbeatConnected chan<- struct{}, logger *slog.Logger) (*mqttConn, error) {
	serverURL, err := url.Parse(cfg.MQTTBroker)
	if err != nil {
		// config.Config.Validate should already have caught this; guard
		// anyway since a malformed URL here would otherwise loop forever
		// without ever attempting a connection.
		return nil, fmt.Errorf("parsing mqtt broker url %q: %w", cfg.MQTTBroker, err)
	}

	will, err := buildWillMessage(cfg.NodeID)
	if err != nil {
		return nil, err
	}

	clientCfg := autopaho.ClientConfig{
		ServerUrls:       []*url.URL{serverURL},
		KeepAlive:        keepAliveSeconds,
		ConnectTimeout:   mqttConnectTimeout,
		ReconnectBackoff: autopaho.DefaultExponentialBackoff(),
		WillMessage:      will,
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			logger.Info("mqtt broker connection up", "broker", cfg.MQTTBroker, "client_id", cfg.MQTTClientID)
			go publishAdvertisement(ctx, &mqttConn{cm: cm}, cfg, bootID, startedAt, logger)

			select {
			case heartbeatConnected <- struct{}{}:
			default:
				// Already has a pending trigger queued (or nothing is
				// listening yet and the buffer is full) — runHeartbeat only
				// needs to know "a connect happened since I last checked",
				// not how many, so dropping this one is correct, not lossy
				// in any way that matters.
			}
		},
		OnConnectionDown: func() bool {
			logger.Warn("mqtt broker connection lost; will retry", "broker", cfg.MQTTBroker)
			return true // never give up retrying
		},
		OnConnectError: func(err error) {
			logger.Warn("mqtt broker connect attempt failed; will retry", "broker", cfg.MQTTBroker, "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: cfg.MQTTClientID,
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

	return &mqttConn{cm: cm}, nil
}
