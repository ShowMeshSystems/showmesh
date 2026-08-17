package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// advertiseTimeout bounds the on-connect hello + LWT-online publish pair so
// a hung publish cannot leak the goroutine newMQTTConn's OnConnectionUp
// callback spawns for it.
const advertiseTimeout = 10 * time.Second

// willDisconnectReason is the Reason recorded on the Will payload
// registered at CONNECT time (see newMQTTConn). It describes what the
// broker firing this payload means, not what actually happened — the agent
// cannot know why it disconnected before the fact, only that if the broker
// ever publishes this payload verbatim, the disconnect was not a clean one
// (see shutdownCleanly for the clean-stop path, which publishes its own
// distinct reason and pre-empts this payload from ever being sent).
const willDisconnectReason = "unexpected disconnect: broker-fired last will"

// buildLWTEnvelope builds a showmesh.node.lwt/v1 envelope for nodeID,
// stamping SentAt from now. See mqttproto.LWTPayload's doc comment, and
// newMQTTConn's, for why a caller using this for the registered Will must
// not read the resulting SentAt as a time of death.
func buildLWTEnvelope(now func() time.Time, nodeID string, online bool, reason string) (mqttproto.Envelope, error) {
	return mqttproto.NewLWTEnvelope(now, nodeID, mqttproto.LWTPayload{Online: online, Reason: reason})
}

// platformString renders this process's platform in ARCHITECTURE section
// 6's "os-arch" style, e.g. "linux-amd64".
func platformString() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// publishHello publishes cfg's identity and this node's capability set to
// the retained hello topic, and returns how many capabilities were
// actually published (for publishAdvertisement's own log line — cfg.
// Capabilities alone would undercount whatever [capabilityDetector] found,
// since detection happens inside this function, not before it). The
// capability set is cfg.Capabilities verbatim when SHOWMESH_NODE_CAPABILITIES
// set it (an explicit operator override always wins outright), otherwise a
// fresh call to [detectCapabilities] — real detection, run again on every
// connect (including every reconnect), not cached from boot, so a
// capability that becomes available later (e.g. the NDI runtime gets
// installed) is advertised on this node's next reconnect with no restart
// required.
func publishHello(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time) (int, error) {
	topic, err := mqttproto.HelloTopic(cfg.NodeID)
	if err != nil {
		return 0, fmt.Errorf("building hello topic: %w", err)
	}

	caps := cfg.Capabilities
	if len(caps) == 0 {
		caps = capabilityDetector(ctx)
	}

	env, err := mqttproto.NewHelloEnvelope(time.Now, cfg.NodeID, mqttproto.HelloPayload{
		Label:        cfg.NodeLabel,
		Platform:     platformString(),
		AgentVersion: version.Version,
		BootID:       bootID,
		StartedAt:    startedAt,
		Capabilities: caps,
	})
	if err != nil {
		return 0, fmt.Errorf("building hello envelope: %w", err)
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return 0, fmt.Errorf("marshaling hello envelope: %w", err)
	}

	if err := pub.Publish(ctx, topic, mqttproto.HelloDeliveryPolicy.QoS, mqttproto.HelloDeliveryPolicy.Retain, payload); err != nil {
		return 0, err
	}
	return len(caps), nil
}

// publishLWT publishes an online/reason LWT payload to nodeID's LWT topic,
// using mqttproto.LWTDeliveryPolicy's retain and QoS.
func publishLWT(ctx context.Context, pub Publisher, nodeID string, online bool, reason string) error {
	topic, err := mqttproto.LWTTopic(nodeID)
	if err != nil {
		return fmt.Errorf("building lwt topic: %w", err)
	}

	env, err := buildLWTEnvelope(time.Now, nodeID, online, reason)
	if err != nil {
		return fmt.Errorf("building lwt envelope: %w", err)
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshaling lwt envelope: %w", err)
	}

	return pub.Publish(ctx, topic, mqttproto.LWTDeliveryPolicy.QoS, mqttproto.LWTDeliveryPolicy.Retain, payload)
}

// publishOnline publishes "online: true" to nodeID's LWT topic. Called on
// every successful connect, including reconnects — see newMQTTConn's
// OnConnectionUp.
func publishOnline(ctx context.Context, pub Publisher, nodeID string) error {
	return publishLWT(ctx, pub, nodeID, true, "connected")
}

// publishOffline publishes "online: false" to nodeID's LWT topic with the
// given reason. Used by shutdownCleanly for a planned stop; the registered
// Will (see newMQTTConn) covers the unplanned case and is never invoked by
// this function directly.
func publishOffline(ctx context.Context, pub Publisher, nodeID, reason string) error {
	return publishLWT(ctx, pub, nodeID, false, reason)
}

// publishAdvertisement publishes the retained hello, then the retained
// "online: true" LWT payload, in that order, per the Task D spec. It runs
// once per successful connect (including every reconnect: a broker that
// lost its persistence file has no retained state, and a node that only
// advertised once would be invisible forever after). Both publishes are
// individually best-effort: a failure on either is logged, not treated as
// fatal, since newMQTTConn's caller has no synchronous way to react to an
// OnConnectionUp-triggered publish failing anyway (see its "must not
// block" contract).
func publishAdvertisement(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, advertiseTimeout)
	defer cancel()

	if n, err := publishHello(pubCtx, pub, cfg, bootID, startedAt); err != nil {
		logPublishFailure(logger, "hello", cfg.NodeID, err)
	} else {
		logger.Info("published hello", "node_id", cfg.NodeID, "capability_count", n)
	}

	if err := publishOnline(pubCtx, pub, cfg.NodeID); err != nil {
		logPublishFailure(logger, "lwt online=true", cfg.NodeID, err)
	} else {
		logger.Info("published lwt online=true", "node_id", cfg.NodeID)
	}
}

// logPublishFailure logs a failed publish of what (a short description,
// e.g. "hello" or "lwt online=true"), distinguishing an ADR-024 decision 10
// ACL rejection — a permanent condition where the broker accepted the
// connection and discarded the message — from every other publish failure,
// which is logged as a transient error the caller's own retry path (the
// next reconnect, for hello/online; the next tick, for a heartbeat) is
// expected to resolve on its own. See ErrPublishNotAuthorized's doc comment
// in mqtt.go.
func logPublishFailure(logger *slog.Logger, what, nodeID string, err error) {
	if errors.Is(err, ErrPublishNotAuthorized) {
		logger.Error("failed to publish "+what+": rejected by broker ACL (not authorized); this node's control-plane visibility is degraded until the credential/ACL is fixed",
			"node_id", nodeID, "error", err)
		return
	}
	logger.Error("failed to publish "+what, "node_id", nodeID, "error", err)
}
