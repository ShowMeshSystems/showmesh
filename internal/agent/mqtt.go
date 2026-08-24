package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
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

// cmdSubscribeTimeout bounds each SUBSCRIBE attempt against the node's own
// cmd topic, issued fresh on every (re)connect — see newMQTTConn's
// OnConnectionUp. Matches mqttConnectTimeout's "one bounded attempt, not a
// hang" reasoning rather than reusing that constant directly: a SUBSCRIBE
// is a different MQTT control packet than CONNECT and has no reason to
// share a budget with it just because both are currently the same number.
const cmdSubscribeTimeout = 10 * time.Second

// isAuthReasonCode reports whether code is an MQTT v5 reason code in the
// "the broker understood who you are and refused you" family, as opposed to
// a transport-level failure (connection refused, timeout, DNS failure)
// that never produces a reason code at all because no packet was ever
// exchanged. These four values carry the same meaning on every packet type
// that can carry them (CONNACK, PUBACK, PUBREC, SUBACK, ...) per the MQTT
// v5 spec; packets.ConnackNotAuthorized (0x87) and packets.PubackNotAuthorized
// are literally the same numeric value, so one classifier serves both the
// CONNACK case (newMQTTConn's OnConnectError, below) and the PUBACK case
// (mqttConn.Publish, below) rather than duplicating the switch per packet
// type.
//
// Per ADR-024 decision 10, a rejection in this family is a permanent,
// credential-related condition an agent must surface distinctly from "the
// broker is unreachable" — the CONNACK case is the CONNACK reason code
// 0x87 Not Authorized decision 10 names directly; Bad Username or Password,
// Banned, and Bad Authentication Method are the same family of condition
// under a different label and get the same treatment.
func isAuthReasonCode(code byte) bool {
	switch code {
	case packets.ConnackBadUsernameOrPassword, // 0x86
		packets.ConnackNotAuthorized,           // 0x87 (== packets.PubackNotAuthorized)
		packets.ConnackBanned,                  // 0x8A
		packets.ConnackBadAuthenticationMethod: // 0x8C
		return true
	}
	return false
}

// ErrPublishNotAuthorized is wrapped by the error [mqttConn.Publish] returns
// when the broker accepted the publish transaction at the transport level
// (the connection is up, a PUBACK/PUBREC round-trip completed) but rejected
// it with an authorization-family reason code. Per ADR-024 decision 10 this
// is "quieter" than a CONNACK rejection — Mosquitto accepts the connection
// and simply declines the message — and this project's Step 5 lesson about
// GET-only vs. read-only generalizes here: a client that only checks
// "did Publish return non-nil" cannot tell this apart from a transient
// network failure on the same call. Callers (advertise.go, heartbeat.go)
// check for this with errors.Is and log it distinctly rather than folding
// it into the generic "publish failed; will retry" line.
var ErrPublishNotAuthorized = errors.New("mqtt: publish rejected by broker (not authorized)")

// mqttConn adapts *autopaho.ConnectionManager to this package's Publisher
// and Conn interfaces, so production code and tests share the same call
// shape (see publisher.go).
type mqttConn struct {
	cm *autopaho.ConnectionManager
}

func (m *mqttConn) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	resp, err := m.cm.Publish(ctx, &paho.Publish{
		QoS:     qos,
		Retain:  retain,
		Topic:   topic,
		Payload: payload,
	})
	if err != nil && resp != nil && isAuthReasonCode(resp.ReasonCode) {
		return fmt.Errorf("%w: topic %q, reason code %d: %w", ErrPublishNotAuthorized, topic, resp.ReasonCode, err)
	}
	return err
}

func (m *mqttConn) Disconnect(ctx context.Context) error {
	return m.cm.Disconnect(ctx)
}

// classifyConnectError inspects an error passed to autopaho's OnConnectError
// callback and reports whether it represents a CONNACK-level authorization
// rejection (rejected=true, with the reason code and any CONNACK reason
// string the broker supplied) as opposed to every other connect failure
// (connection refused, DNS failure, timeout, TLS failure, ...), which never
// produces a CONNACK at all. autopaho wraps a CONNACK it received — even a
// rejecting one — in *autopaho.ConnackError (see that type's doc comment
// and autopaho/net.go's establishServerConnection, which only constructs
// one when connack != nil); a bare transport error is passed through
// unwrapped, so errors.As returning false here means exactly "no CONNACK
// was ever received," not "the CONNACK we got isn't a ConnackError".
//
// Factored out of newMQTTConn's OnConnectError closure so it can be unit
// tested directly (see mqtt_test.go) without dialing a broker: this is the
// one piece of "does the agent tell a permanent credential rejection apart
// from a transient network fault" that a unit test can actually prove: see
// TestClassifyConnectError*.
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
//
// OnConnectionUp also subscribes showMode to the retained
// installation-wide operating mode topic (ADR-033), for the same
// per-reconnect reason cmdHandler is re-registered below. See showmode.go.
//
// OnConnectionUp also registers cmdHandler against every (re)connect's
// live client (see registerCommandHandling): a fresh underlying
// *paho.Client exists after every reconnect, so both the SUBSCRIBE and the
// publish-received callback binding have to happen again each time, not
// once at startup.
func newMQTTConn(ctx context.Context, cfg config.Config, bootID string, startedAt time.Time, heartbeatConnected chan<- struct{}, cmdHandler *CommandHandler, showMode *ShowModeHolder, logger *slog.Logger) (*mqttConn, error) {
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

			registerCommandHandling(ctx, cm, cfg.NodeID, cmdHandler, logger)

			// ADR-033's mode subscription, re-registered on every connect
			// for registerCommandHandling's own reason. showMode may be nil
			// in a test that does not exercise the mode; a nil holder means
			// this node simply never learns the mode and reads unknown,
			// which behaves as show.
			if showMode != nil {
				registerShowMode(ctx, cm, showMode, time.Now, logger)
			}

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
			// ADR-024 decision 10: before allow_anonymous was disabled on
			// the reference broker, "the broker will not take my
			// connection" was always transient (unreachable, DNS,
			// timeout). A CONNACK authorization rejection is a permanent,
			// self-inflicted condition that presents identically to an
			// operator watching a dashboard unless this agent tells the
			// two apart itself — the broker will never fix a wrong or
			// revoked credential by being retried harder. This is NOT
			// treated as fatal: autopaho keeps retrying with backoff
			// exactly as it does for a transport failure (this callback
			// never stops that), because the agent has no way to know the
			// credential will still be wrong on the next attempt, and an
			// agent that exits on this is useless exactly when it is most
			// needed. There is also, today, no ADR-009 cached local
			// fallback subset for this Step 2-era agent to fall back to —
			// see the internal/agent package doc comment: no GStreamer, no
			// media, no command handling. "Continues" currently means
			// exactly what it already meant for a transport failure: the
			// process keeps running and keeps retrying; the only new
			// behavior here is that the log line below is distinct and
			// unmissable rather than folded into the generic retry
			// message.
			if rejected, code, reason := classifyConnectError(err); rejected {
				logger.Error("mqtt broker rejected connection: not authorized; this is a permanent condition (wrong or revoked credential), not a transient network fault, and will not resolve by retrying — the agent is NOT exiting and will keep retrying anyway in case the credential is fixed, but see SHOWMESH_MQTT_USERNAME/SHOWMESH_MQTT_PASSWORD",
					"broker", cfg.MQTTBroker, "client_id", cfg.MQTTClientID, "reason_code", code, "reason", reason)
				return
			}
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

// registerCommandHandling binds cmdHandler to receive every PUBLISH on
// nodeID's cmd topic, and subscribes to it — both done fresh on every call
// (i.e. every OnConnectionUp), because a reconnect gives autopaho a brand
// new underlying *paho.Client with no memory of a prior SUBSCRIBE or a
// prior publish-received callback registration.
//
// Two distinct autopaho mechanisms are involved, deliberately NOT the
// single paho.ClientConfig.OnPublishReceived field: that field is bound
// into the client config once, at construction time, and its callback
// signature carries no live *autopaho.ConnectionManager to publish a
// result back through. cm.AddOnPublishReceived, in contrast, hands the
// callback an autopaho.PublishReceived carrying exactly that
// ConnectionManager (verified against the vendored
// github.com/eclipse/paho.golang@v0.23.0/autopaho/auto.go source: its
// PublishReceived struct embeds paho.PublishReceived — giving
// .Packet.Topic/.Packet.Payload — plus its own ConnectionManager field),
// which is what lets HandleMessage's eventual result publish go out
// through the actual live connection rather than a stale one from a
// previous session.
//
// Re-registering the publish-received callback on every reconnect is
// intentional, not a leak: AddOnPublishReceived binds against whatever
// *paho.Client is current at the moment it is called (c.cli, read under
// c.mu inside ConnectionManager.AddOnPublishReceived), and c.cli is
// reassigned to a fresh client on every reconnect BEFORE OnConnectionUp
// fires (auto.go's mainLoop: "c.mu.Lock(); c.cli = cli; ...; c.mu.Unlock()"
// happens, then "if cfg.OnConnectionUp != nil { cfg.OnConnectionUp(&c,
// connAck) }" — verified in the same source). A callback registered
// against a previous, now-replaced client is simply never invoked again;
// there is nothing to explicitly unregister, and the removal func
// AddOnPublishReceived returns exists for a caller that wants to stop
// listening entirely, which this agent never does for the lifetime of one
// process.
//
// AddOnPublishReceived's own registration is a fast, local, in-memory call
// (no network round trip — it just appends to a callback list under a
// mutex) and is called synchronously here, before SUBSCRIBE is even
// issued, so the handler is always in place before any message this
// SUBSCRIBE could possibly cause to arrive. The SUBSCRIBE itself IS a
// network round trip, so — honoring OnConnectionUp's "must not block"
// contract, the same one publishAdvertisement's own goroutine already
// respects — it runs in its own goroutine.
//
// The publish-received callback returns (bool, error) per paho's own
// AddOnPublishReceived contract, where the bool means "I handled this
// message" — callbacks registered after this one, and paho's own
// unhandled-message bookkeeping, use that to decide whether the message
// still needs handling. This agent subscribes to exactly one topic today,
// so returning true unconditionally would happen to be harmless right
// now, but it is still wrong: it silently claims every inbound PUBLISH
// regardless of topic, which would swallow messages meant for anything
// else the moment a second subscription exists. The callback therefore
// checks pr.Packet.Topic against nodeID's own cmd topic (computed once,
// up front, and reused by both this callback and the SUBSCRIBE call
// below) and returns false — unhandled — for anything else.
func registerCommandHandling(ctx context.Context, cm *autopaho.ConnectionManager, nodeID string, cmdHandler *CommandHandler, logger *slog.Logger) {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		// nodeID is validated at config load (mqttproto.ValidateNodeID via
		// internal/agent/config.LoadConfig), matching runHeartbeat's
		// identical topic-build guard in heartbeat.go; should be
		// unreachable in production. Neither the publish-received handler
		// nor the SUBSCRIBE can be meaningfully registered without a valid
		// topic, so both are skipped.
		logger.Error("bug: could not build cmd topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	cm.AddOnPublishReceived(func(pr autopaho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != topic {
			return false, nil
		}
		payload := pr.Packet.Payload

		// HandleMessage runs in its own goroutine, on a context DERIVED
		// FROM context.Background() — deliberately NOT ctx (this
		// connection's own lifetime context) and deliberately NOT
		// whatever connection-teardown context a caller elsewhere might
		// be using: an inbound command already received off the wire must
		// be allowed to run to completion and publish its result even if
		// the MQTT connection that delivered it is torn down a moment
		// later, the same way this file's OnConnectionUp already spawns
		// publishAdvertisement on ctx rather than on a shutdown-scoped
		// context. Bounded per-publish timeouts inside HandleMessage
		// (commandPublishTimeout in command.go) are what actually bound
		// this goroutine's lifetime, not this context.
		go cmdHandler.HandleMessage(context.Background(), &mqttConn{cm: pr.ConnectionManager}, topic, payload)

		return true, nil
	})

	go func() {
		subCtx, cancel := context.WithTimeout(ctx, cmdSubscribeTimeout)
		defer cancel()

		suback, err := cm.Subscribe(subCtx, &paho.Subscribe{
			Subscriptions: []paho.SubscribeOptions{
				{Topic: topic, QoS: mqttproto.CmdDeliveryPolicy.QoS},
			},
		})
		// ERROR, not WARN: an agent that fails to subscribe to its own cmd
		// topic will silently never receive a command again until the next
		// reconnect — exactly the "no caller" class of failure this
		// project's CLAUDE.md says a test suite cannot catch on its own,
		// and the kind of thing that must be loud in the logs of a real
		// deployment. Not fatal: matching this file's OnConnectError
		// philosophy, an agent that exists to keep running through exactly
		// this kind of trouble must not crash over it — the next reconnect
		// (autopaho retries forever) gets another attempt.
		if err != nil {
			logger.Error("failed to subscribe to cmd topic; this agent will not receive commands until the next reconnect",
				"node_id", nodeID, "topic", topic, "error", err)
			return
		}
		if len(suback.Reasons) == 0 || suback.Reasons[0] >= 0x80 {
			logger.Error("broker rejected cmd topic subscription; this agent will not receive commands until the next reconnect",
				"node_id", nodeID, "topic", topic, "reasons", suback.Reasons)
			return
		}
		logger.Info("subscribed to cmd topic", "node_id", nodeID, "topic", topic)
	}()
}
