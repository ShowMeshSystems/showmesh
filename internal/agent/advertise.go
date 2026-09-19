package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"runtime"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// advertiseTimeout bounds the on-connect hello + LWT-online publish pair so
// a hung publish cannot leak the goroutine newMQTTConn's OnConnectionUp
// callback spawns for it. Since review finding 14, real capability
// detection never runs inside this budget — see capabilityDetectionTimeout
// and scheduleCapabilityDetection.
const advertiseTimeout = 10 * time.Second

// capabilityDetectionTimeout bounds one background detection run
// (scheduleCapabilityDetection), independent of advertiseTimeout.
// detectCapabilities's own worst case is 2 real gst-launch-1.0 probes
// (render surface format, NDI send) plus detectAudioCapabilities's own
// worst case of up to 1 + 2*maxProbedDevices ([audio.Discover]'s engine
// probe, plus an unconstrained and an LTC-constrained probe per candidate
// route) — 11 probes today, each bounded by its own package's probeTimeout
// (8s), for 88s worst case. This budget must stay comfortably above that
// sum; widen it if either probe count or either probeTimeout grows.
var capabilityDetectionTimeout = 120 * time.Second

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

// isWildcardHost reports whether host is empty or a wildcard bind address
// ("0.0.0.0" or "::"), the cases [resolveInboundListener] must never
// publish verbatim: none of them name an address the coordinator, on a
// different host, could actually reach.
func isWildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// outboundAddress returns the local IPv4 address this node's routing table
// would use to reach host, via a UDP "connect" (RFC-standard idiom: no
// packet is ever sent, only a route looked up and a local address bound).
// This is [resolveInboundListener]'s fallback source: this package's MQTT
// client (eclipse/paho.golang/autopaho) does not expose its own connection's
// local address, so the address this node would use to reach the SAME host
// (the broker) stands in for it.
func outboundAddress(host string) (string, error) {
	conn, err := net.Dial("udp4", net.JoinHostPort(host, "9"))
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.IsUnspecified() || addr.IP.IsLoopback() {
		return "", fmt.Errorf("no usable non-loopback local address for %q", host)
	}
	return addr.IP.String(), nil
}

// resolveInboundListener computes [mqttproto.HelloPayload.InboundListener]:
// empty when the FPP Connect listener is not bound (status reports not
// listening — see runFPPConnectHTTPListener's own bind-failure path), the
// listener's own configured host when it is bound to one specific,
// non-wildcard address, otherwise the local address this node uses to
// reach its MQTT broker (never a wildcard or loopback address, which the
// coordinator, on a different host, could never dial back).
func resolveInboundListener(cfg config.Config, status *fppConnectHTTPStatus, logger *slog.Logger) string {
	if status == nil {
		return ""
	}
	listening, _, _ := status.get()
	if !listening {
		return ""
	}

	host, port, err := net.SplitHostPort(cfg.FPPConnectListenAddr)
	if err != nil {
		logger.Warn("failed to parse the fppconnect listen address while building this node's inbound listener address", "listen_addr", cfg.FPPConnectListenAddr, "error", err)
		return ""
	}
	if !isWildcardHost(host) {
		return net.JoinHostPort(host, port)
	}

	brokerHost := cfg.MQTTBroker
	if u, err := url.Parse(cfg.MQTTBroker); err == nil && u.Hostname() != "" {
		brokerHost = u.Hostname()
	}
	ip, err := outboundAddress(brokerHost)
	if err != nil {
		logger.Warn("failed to resolve this node's outbound address toward its mqtt broker; this node's hello will report no inbound listener address", "broker_host", brokerHost, "error", err)
		return ""
	}
	return net.JoinHostPort(ip, port)
}

// publishHello publishes cfg's identity and caps, this node's capability
// set, to the retained hello topic, and returns how many capabilities were
// published. Since review finding 14, publishHello performs no detection
// of its own — it only builds and publishes an envelope from whatever caps
// its caller already resolved, so it can never be the thing that makes a
// hello publish late. See capabilitiesForImmediateHello and
// scheduleCapabilityDetection for how a caller resolves caps. inboundListener
// is [mqttproto.HelloPayload.InboundListener] (see resolveInboundListener);
// empty when this node runs no inbound listener.
func publishHello(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, caps capability.Set, inboundListener string) (int, error) {
	topic, err := mqttproto.HelloTopic(cfg.NodeID)
	if err != nil {
		return 0, fmt.Errorf("building hello topic: %w", err)
	}

	env, err := mqttproto.NewHelloEnvelope(time.Now, cfg.NodeID, mqttproto.HelloPayload{
		Label:           cfg.NodeLabel,
		Platform:        platformString(),
		AgentVersion:    version.Version,
		BootID:          bootID,
		StartedAt:       startedAt,
		Capabilities:    caps,
		InboundListener: inboundListener,
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

// capabilitiesForImmediateHello resolves the capability set publishAdvertisement
// publishes without waiting on any probing: cfg.Capabilities verbatim when
// SHOWMESH_NODE_CAPABILITIES set it (an explicit operator override always
// wins outright and is never probed for), otherwise whatever
// detectedCapabilityCache last stored — empty, honestly, on this process's
// first-ever connect, before any detection run has completed. This is
// exactly review finding 14's fix: nothing that can block or hang is on
// this path.
func capabilitiesForImmediateHello(cfg config.Config) capability.Set {
	if len(cfg.Capabilities) != 0 {
		return cfg.Capabilities
	}
	cached, _ := detectedCapabilityCache.snapshot()
	return cached
}

// scheduleCapabilityDetection arranges for real capability detection to
// run (outside advertiseTimeout's budget) and republish hello with the
// result: the other half of review finding 14's fix. It is a no-op when
// cfg.Capabilities holds an operator override, matching
// capabilitiesForImmediateHello: an override is never probed for, so there
// is nothing to detect and nothing to republish.
//
// Every call is a trigger on [capabilityGate], not a direct run: this
// function is called both once per successful connect (same as
// publishAdvertisement) and once per real audio-engine availability
// transition (agent.go's rebuild hook), so two triggers close enough
// together must never run concurrently or let the slower one's stale
// result overwrite the faster one's current one. See
// [capabilityDetectionGate]'s own doc comment for the single-flight and
// generation-stamping this relies on. Calling this once per connect
// (including every reconnect) means a capability that becomes available
// later (e.g. the NDI runtime gets installed) is still picked up with no
// agent restart.
//
// ctx is expected to be the agent's own lifetime context (not a
// connection-scoped one, see mqtt.go's OnConnectionUp, which passes the
// same ctx to publishAdvertisement, and agent.go's rebuild hook, which
// passes sigCtx), so the detection goroutine survives past
// advertiseTimeout and past whichever connect or rebuild spawned it; a
// hung probe only ever costs capabilityDetectionTimeout, never the
// agent's ability to have published a hello.
func scheduleCapabilityDetection(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, status *fppConnectHTTPStatus, logger *slog.Logger) {
	if len(cfg.Capabilities) != 0 {
		return
	}
	capabilityGate.trigger(func(gen uint64) {
		runCapabilityDetection(ctx, pub, cfg, bootID, startedAt, status, logger, gen)
	})
}

// runCapabilityDetection is [capabilityDetectionGate]'s own run body for
// one detect+publish cycle tagged with gen: it probes, then checks gen is
// still current BEFORE storing or publishing anything, so a cycle a
// newer trigger has already superseded drops its own result silently
// instead of overwriting a fresher one that finished first (or will
// finish after it). Called only via [capabilityDetectionGate.trigger],
// never directly.
func runCapabilityDetection(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, status *fppConnectHTTPStatus, logger *slog.Logger, gen uint64) {
	detectCtx, cancel := context.WithTimeout(ctx, capabilityDetectionTimeout)
	defer cancel()
	caps := capabilityDetector(detectCtx)

	if !capabilityGate.isCurrent(gen) {
		logger.Info("dropped a superseded capability detection result", "node_id", cfg.NodeID, "generation", gen)
		return
	}
	detectedCapabilityCache.store(caps)

	pubCtx, cancel2 := context.WithTimeout(ctx, advertiseTimeout)
	defer cancel2()
	inboundListener := resolveInboundListener(cfg, status, logger)
	if n, err := publishHello(pubCtx, pub, cfg, bootID, startedAt, caps, inboundListener); err != nil {
		logPublishFailure(logger, "hello (post-detection republish)", cfg.NodeID, err)
	} else {
		logger.Info("republished hello after capability detection", "node_id", cfg.NodeID, "capability_count", n)
	}
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
//
// The hello publish here always uses capabilitiesForImmediateHello — never
// a live probe — so this whole function is bounded by advertiseTimeout
// with no possibility of a hung gst-launch-1.0 subprocess costing this
// node its hello (review finding 14). Real detection, when applicable, is
// kicked off separately by scheduleCapabilityDetection and republishes
// hello on its own schedule once it finishes.
func publishAdvertisement(ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, status *fppConnectHTTPStatus, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, advertiseTimeout)
	defer cancel()

	caps := capabilitiesForImmediateHello(cfg)
	inboundListener := resolveInboundListener(cfg, status, logger)
	if n, err := publishHello(pubCtx, pub, cfg, bootID, startedAt, caps, inboundListener); err != nil {
		logPublishFailure(logger, "hello", cfg.NodeID, err)
	} else {
		logger.Info("published hello", "node_id", cfg.NodeID, "capability_count", n)
	}

	if err := publishOnline(pubCtx, pub, cfg.NodeID); err != nil {
		logPublishFailure(logger, "lwt online=true", cfg.NodeID, err)
	} else {
		logger.Info("published lwt online=true", "node_id", cfg.NodeID)
	}

	// No "go" here: scheduleCapabilityDetection only ever triggers
	// capabilityGate, which decides for itself whether to start a new
	// detection goroutine or coalesce this into an already-running one,
	// and returns immediately either way.
	scheduleCapabilityDetection(ctx, pub, cfg, bootID, startedAt, status, logger)
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
