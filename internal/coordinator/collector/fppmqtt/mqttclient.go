package fppmqtt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Provenance-labeled defaults, in the pattern
// internal/coordinator/collector/fpp and pkg/multisync/timeline.go
// established: every threshold states whether it is derived from something
// FPP-authoritative or is a ShowMesh guess awaiting bench verification.
const (
	// DefaultTopicPrefix matches the reference fleet's actual, unprefixed
	// topic root (contract section 1.2: "MQTTPrefix is unset on this
	// fleet, so there is no extra prefix segment"), not a guess. Kept in
	// sync with internal/coordinator/config's own
	// defaultFPPMQTTTopicPrefix by
	// TestDefaultTopicPrefixMatchesConfigDefault.
	DefaultTopicPrefix = "falcon/player"

	// DefaultPollInterval is the recommended cadence for
	// collector.Runner.Add. SHOWMESH HYPOTHESIS, NOT MEASURED: this
	// collector's Poll never touches the network (see render.go), so this
	// interval only bounds how promptly a value already sitting in the
	// message store reaches the store this Collector feeds — cheap to
	// poll more often than the fpp REST collector's 15s, chosen well
	// under the fastest observed FPP publish cadence (contract section
	// 1.2: "port_status on the remotes roughly every 1s") so this
	// collector itself is never the reason a value looks stale.
	DefaultPollInterval = 5 * time.Second

	// DefaultValidFor is how long a live (non-retained) value stays
	// [observation.StateCurrent] before aging to [observation.StateStale].
	// SHOWMESH HYPOTHESIS, NOT MEASURED: generous headroom (roughly 7-8x)
	// over contract section 1.2's fastest observed cadence
	// (~1s for port_status on a remote, ~4s for fppd_status), so a couple
	// of missed publishes do not immediately read stale, while still
	// catching a genuinely stalled publisher meaningfully faster than the
	// REST collector's 45s (fpp.DefaultValidFor). Not applicable to a
	// retained delivery at all — see render.go's buildObservation: a
	// retained value's State is StateUnknownAge regardless of ValidFor,
	// per pkg/observation's StateAt.
	DefaultValidFor = 30 * time.Second

	// silentSinceConnectThreshold: SHOWMESH HYPOTHESIS, NOT MEASURED, own
	// constant (not DefaultValidFor reused, which answers a different
	// question). Set from contract section 1.2's recorded cadences (~1s
	// port_status, ~4s fppd_status), which a working FPP publishes
	// continuously regardless of show state.
	silentSinceConnectThreshold = 30 * time.Second

	// clientID is fixed rather than configurable: the hard interface
	// contract this package was built against (Step 5 spec section 4)
	// does not expose a ClientID field on Options, and this package
	// assumes one coordinator process per configured broker. Two
	// coordinators pointed at the same broker with this same ClientID
	// would have the broker disconnect one on the other's CONNECT — an
	// explicit limitation, not a silent one, worth revisiting if this
	// package ever needs to support that topology.
	clientID = "showmesh-fpp-mqtt-collector"

	// connectKeepAlive and connectTimeout are SHOWMESH HYPOTHESIS, NOT
	// MEASURED, sized the same way internal/coordinator/broker sizes its
	// own control-plane connection's equivalents, applied here to an
	// entirely separate connection (see doc.go for why the two must never
	// share one).
	connectKeepAlive = uint16(30)
	connectTimeout   = 10 * time.Second
)

// Options configures a Collector. See the Step 5 contract section 4's hard
// interface: every field here is exactly what Seam C was told to code
// against.
type Options struct {
	// BrokerURL is the MQTT broker to subscribe to, e.g.
	// "tcp://broker.example:1883". Required — [New] returns an error if
	// it is empty or fails the same scheme/host validation
	// internal/coordinator/config applies to it.
	BrokerURL string

	// Username and Password are optional broker credentials. Password is
	// exactly as sensitive as internal/coordinator/config's
	// FPPMQTTPassword and is never included in any error, log line, or
	// observation Reason this package produces — see doc.go.
	Username string
	Password string

	// TopicPrefix is the topic root FPP publishes under. Empty defaults to
	// [DefaultTopicPrefix].
	TopicPrefix string

	// Hosts maps a coordinator FPP instance id to that instance's FPP
	// HostName as it appears in its MQTT topics. Required and must be
	// non-empty — a Collector with nothing to subscribe to is a
	// configuration error, not a degenerate no-op (see
	// internal/coordinator/config's mirrored startup check).
	Hosts map[string]string

	// PollInterval is the recommended [collector.Runner.Add] cadence for
	// this Collector. Zero means [DefaultPollInterval]. Stored and
	// returned by [Collector.PollInterval] for a caller's convenience;
	// Poll's own behavior does not depend on it (see render.go — Poll
	// never blocks on the network and has no cadence of its own to
	// enforce).
	PollInterval time.Duration

	// Logger receives this Collector's diagnostic logging: connection
	// state changes, subscribe failures, and unmatched-host notices (see
	// contract section 4.4). nil means slog.Default().
	Logger *slog.Logger

	// Now is the clock used for every timestamp this Collector stamps that
	// is not itself a message receipt time (see store.go's message type
	// for receipt-time stamping, which always uses this same clock). nil
	// means time.Now. Tests inject a fake clock so staleness/ordering
	// assertions do not need real sleeps.
	Now func() time.Time
}

// Collector implements collector.Collector for FPP's own MQTT-published
// state, entirely independent of internal/coordinator/collector/fpp's REST
// polling and internal/coordinator/broker's ADR-008 control-plane
// connection — see doc.go.
//
// A zero-value Collector is not usable; construct one with [New].
type Collector struct {
	brokerURL   string
	username    string
	password    string
	topicPrefix string

	// hosts is instanceID -> FPP HostName, exactly Options.Hosts (after
	// validation). hostToInstance is its precomputed reverse, HostName ->
	// instanceID, used by the publish handler (mqttclient.go) to route an
	// inbound message. Both are built once in New and never mutated after
	// construction, so they need no lock for concurrent reads.
	hosts          map[string]string
	hostToInstance map[string]string

	pollInterval time.Duration
	validFor     time.Duration
	now          func() time.Time
	logger       *slog.Logger

	store *messageStore

	// connMu guards connected/connReason/connectedAt/messageSinceConnect,
	// set by the connection callbacks (Run) and the publish handler.
	connMu     sync.Mutex
	connected  bool
	connReason string

	// connectedAt: when the current connection came up (zero while
	// disconnected). messageSinceConnect: per configured instance id,
	// whether a message from that host has arrived since. Both reset
	// together on every OnConnectionUp, see setConnected. A reconnect
	// clears every host's latch, not just the one that happens to publish
	// next, so a healthy host's silence never masks a different host's.
	connectedAt         time.Time
	messageSinceConnect map[string]bool

	// unmatchedMu guards unmatchedLogged, contract section 4.4's "logged
	// at info once per host" bookkeeping for a message whose HostName does
	// not match any configured instance.
	unmatchedMu     sync.Mutex
	unmatchedLogged map[string]bool
}

// validBrokerSchemes mirrors internal/coordinator/config's own list:
// duplicated rather than imported, the same "safe to construct directly,
// including in tests, without relying on config package validation having
// already run" reasoning internal/coordinator/collector/fpp.New documents
// for its own duplicated URL checks.
var validBrokerSchemes = map[string]bool{
	"tcp": true, "ssl": true, "tls": true, "mqtt": true, "mqtts": true, "ws": true, "wss": true,
}

// hostNameForbidden matches any character that must never appear in an FPP
// HostName this package accepts: MQTT's own wildcard/level-separator
// characters plus whitespace. A HostName is placed directly into a topic
// filter string (topics.go/subscribeAll), so this is a real
// topic-injection check, not cosmetic — the same reasoning
// pkg/mqttproto.ValidateNodeID documents for node ids, and the same rule
// internal/coordinator/config's parseFPPMQTTHosts enforces independently
// at config-load time (New must enforce it again itself: see this
// function's doc comment on why a *Collector must be safe to construct
// directly, without relying on config package validation having already
// run).
var hostNameForbidden = regexp.MustCompile(`[+#/\s]`)

func validateHostName(name string) error {
	if name == "" {
		return fmt.Errorf("HostName must not be empty")
	}
	if hostNameForbidden.MatchString(name) {
		return fmt.Errorf("HostName %q must not contain '/', '+', '#', or whitespace", name)
	}
	return nil
}

// brokerURLUserinfoPattern matches a BrokerURL of the shape
// "scheme://userinfo@..." independent of whether url.Parse can otherwise
// make sense of the rest of the string. Checked BEFORE url.Parse is even
// called, so a broker URL that carries embedded credentials AND happens to
// be malformed some other way is still caught here, ahead of the
// url.Parse failure branch below whose *url.Error naturally embeds the raw
// input string (and, with it, any credential in it) in its own Error()
// text.
var brokerURLUserinfoPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/?#]*@`)

// errBrokerURLUserinfo is returned verbatim — opts.BrokerURL is NEVER
// interpolated into it, by construction — whenever a broker URL carries
// userinfo. See New's doc comment on why this rejection exists.
var errBrokerURLUserinfo = errors.New("fppmqtt: BrokerURL must not include userinfo/credentials; configure Username and Password instead")

// New validates opts and constructs a Collector. It does not connect to
// anything — see [Collector.Run] for that.
//
// Step 5 review finding 6: SHOWMESH_FPP_MQTT_PASSWORD is redacted
// everywhere it is meant to be (this Options' own Password field is never
// logged — see doc.go), but a credential embedded directly in BrokerURL
// (tcp://user:pass@host:1883) is a second door to the exact same leak:
// buildClientConfig stores opts.BrokerURL verbatim as c.brokerURL, and
// EVERY connection-state log line this package emits (OnConnectionUp,
// OnConnectionDown, OnConnectError — mqttclient.go, one per connect
// attempt and reconnect) logs "broker", c.brokerURL. A URL-embedded
// password would therefore repeat in the log on every single reconnect
// cycle for as long as the coordinator runs. New rejects userinfo
// structurally, before a Collector — and therefore before c.brokerURL —
// is ever constructed, so that log line can never carry a credential
// this way. The REST side's fpp.New and
// internal/coordinator/config's FPPEndpoints userinfo rule are the
// existing precedent this mirrors.
func New(opts Options) (*Collector, error) {
	if opts.BrokerURL == "" {
		return nil, fmt.Errorf("fppmqtt: BrokerURL must not be empty")
	}
	if brokerURLUserinfoPattern.MatchString(opts.BrokerURL) {
		return nil, errBrokerURLUserinfo
	}
	parsed, err := url.Parse(opts.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("fppmqtt: invalid BrokerURL %q: %w", opts.BrokerURL, err)
	}
	if parsed.User != nil {
		// Defense in depth: brokerURLUserinfoPattern above already rejects
		// the common "scheme://user:pass@host" shape without needing
		// url.Parse's help; this catches whatever escapes that regex (an
		// unusual percent-encoding, for instance) via url.Parse's own
		// userinfo detection instead. Never interpolates opts.BrokerURL or
		// parsed into the error, for the same reason as above.
		return nil, errBrokerURLUserinfo
	}
	if !validBrokerSchemes[parsed.Scheme] {
		return nil, fmt.Errorf("fppmqtt: BrokerURL %q must use a supported scheme (tcp/ssl/tls/mqtt/mqtts/ws/wss)", opts.BrokerURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("fppmqtt: BrokerURL %q must include a host", opts.BrokerURL)
	}

	if len(opts.Hosts) == 0 {
		return nil, fmt.Errorf("fppmqtt: Hosts must not be empty")
	}
	hosts := make(map[string]string, len(opts.Hosts))
	hostToInstance := make(map[string]string, len(opts.Hosts))
	for id, hostName := range opts.Hosts {
		if err := mqttproto.ValidateNodeID(id); err != nil {
			return nil, fmt.Errorf("fppmqtt: instance id %q: %w", id, err)
		}
		if err := validateHostName(hostName); err != nil {
			return nil, fmt.Errorf("fppmqtt: instance %q: %w", id, err)
		}
		if existing, dup := hostToInstance[hostName]; dup {
			return nil, fmt.Errorf("fppmqtt: HostName %q is configured for both instance %q and %q", hostName, existing, id)
		}
		hosts[id] = hostName
		hostToInstance[hostName] = id
	}

	prefix := strings.Trim(opts.TopicPrefix, "/")
	if prefix == "" {
		prefix = DefaultTopicPrefix
	}

	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Collector{
		brokerURL:       opts.BrokerURL,
		username:        opts.Username,
		password:        opts.Password,
		topicPrefix:     prefix,
		hosts:           hosts,
		hostToInstance:  hostToInstance,
		pollInterval:    pollInterval,
		validFor:        DefaultValidFor,
		now:             now,
		logger:          logger,
		store:           newMessageStore(),
		unmatchedLogged: make(map[string]bool),
		connReason:      "mqtt broker connection not yet established",
	}, nil
}

// PollInterval returns the resolved poll interval a caller should register
// this Collector with (e.g. collector.Runner.Add(c, c.PollInterval())).
func (c *Collector) PollInterval() time.Duration { return c.pollInterval }

// connectionState returns whether the broker connection is currently up
// and, when it is not, the reason last recorded by a connection callback
// (Run). Safe for concurrent use.
func (c *Collector) connectionState() (connected bool, reason string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connected, c.connReason
}

// setConnected records a fresh connection-state observation from an
// autopaho callback. reason is only meaningful when connected is false. A
// transition to connected restarts connectedAt/messageSinceConnect fresh,
// on both the first connect and every reconnect: every configured host's
// latch clears together, since they all share the one underlying broker
// connection this resets.
func (c *Collector) setConnected(connected bool, reason string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.connected = connected
	c.connReason = reason
	if connected {
		c.connectedAt = c.now()
		c.messageSinceConnect = make(map[string]bool, len(c.hosts))
	}
}

// markMessageReceived records that a message from instanceID has arrived
// since the current connection came up. Called only for a message this
// package actually stored, never for one it ignored (unmatched host or
// suffix).
func (c *Collector) markMessageReceived(instanceID string) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.messageSinceConnect[instanceID] = true
}

// SilentSinceConnect reports whether instanceID's host has gone silent:
// connected, but no message has arrived on any of ITS subscribed topics
// for at least silentSinceConnectThreshold since the current connection
// came up. States only that fact, never why the host in particular is
// quiet. Always false while disconnected: Poll (render.go) already
// reports that case, per host, via collection_failed.
//
// A host with an empty message store (see store.go's latestReceivedAt) has
// never published anything at all, ever: a candidate for a HostName/topic
// misconfiguration a working host would never exhibit. A host with a
// non-empty store has published in the past but has gone quiet now: a
// candidate for a broken fast path. Both conditions report silent=true
// with the SAME [api.CollectorRunState] (this package does not introduce a
// new one), but the reason text names which is which, in the past tense in
// one case ("since <timestamp>") and the present-perfect negative in the
// other ("has never received"). This is an operator-readable distinction,
// not a machine-parseable one: reason remains free-form prose, and nothing
// in this package or its caller promises a stable grammar for it.
func (c *Collector) SilentSinceConnect(instanceID string) (silent bool, reason string) {
	c.connMu.Lock()
	connected := c.connected
	connectedAt := c.connectedAt
	publishedThisConnection := c.messageSinceConnect[instanceID]
	c.connMu.Unlock()

	if !connected || publishedThisConnection || connectedAt.IsZero() {
		return false, ""
	}
	if c.now().Sub(connectedAt) < silentSinceConnectThreshold {
		return false, ""
	}

	if lastMsgAt, everPublished := c.store.latestReceivedAt(instanceID); everPublished {
		return true, fmt.Sprintf(
			"connected to the broker but has received no message on any subscribed topic for host %q since %s",
			instanceID, lastMsgAt.Format(time.RFC3339))
	}
	return true, fmt.Sprintf(
		"connected to the broker since %s but has never received a message on any subscribed topic for host %q",
		connectedAt.Format(time.RFC3339), instanceID)
}

// subscriber is the ENTIRE method set this package ever calls on its live
// MQTT connection: Subscribe, and nothing else. This is the mechanism
// behind doc.go's "cannot publish, not merely does not" claim — see
// readonly_test.go for the structural test against this interface's method
// set (via reflection, not by reading source), and buildClientConfig below
// for the only place a connection is ever driven.
type subscriber interface {
	Subscribe(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error)
}

// newPublishHandler adapts this Collector's message routing to paho's
// OnPublishReceived callback shape. Every inbound publish is handled here:
// parsed into (HostName, topic suffix), matched against c.hostToInstance
// (contract section 4.4 — "only explicitly configured instance IDs,
// unmatched hosts ignored and logged once"), checked against topicSpecs
// (contract section 4.3's table; anything else, including a command topic
// that could theoretically arrive despite this package never subscribing
// to one — see topics.go — is silently ignored, never stored, never
// logged), and finally recorded in the message store.
func (c *Collector) newPublishHandler() func(paho.PublishReceived) (bool, error) {
	return func(pr paho.PublishReceived) (bool, error) {
		host, suffix, ok := parseHostAndSuffix(c.topicPrefix, pr.Packet.Topic)
		if !ok {
			return true, nil
		}

		instanceID, known := c.hostToInstance[host]
		if !known {
			c.logUnmatchedHostOnce(host)
			return true, nil
		}

		if _, modeled := topicSpecs[suffix]; !modeled {
			return true, nil
		}

		payload := make([]byte, len(pr.Packet.Payload))
		copy(payload, pr.Packet.Payload)

		c.store.put(instanceID, suffix, message{
			payload:    payload,
			retained:   pr.Packet.Retain,
			receivedAt: c.now(),
		})
		c.markMessageReceived(instanceID)

		// Always report the message as handled: this is the only consumer
		// on this connection.
		return true, nil
	}
}

// parseHostAndSuffix splits an inbound topic into the FPP HostName segment
// and the suffix after it, given topicPrefix (e.g. "falcon/player").
// Returns ok=false if topic does not start with "<prefix>/" followed by at
// least one more non-empty segment on each side of the next '/'.
func parseHostAndSuffix(topicPrefix, topic string) (host, suffix string, ok bool) {
	withSlash := topicPrefix + "/"
	if !strings.HasPrefix(topic, withSlash) {
		return "", "", false
	}
	rest := topic[len(withSlash):]
	host, suffix, ok = strings.Cut(rest, "/")
	if !ok || host == "" || suffix == "" {
		return "", "", false
	}
	return host, suffix, true
}

func (c *Collector) logUnmatchedHostOnce(host string) {
	c.unmatchedMu.Lock()
	already := c.unmatchedLogged[host]
	if !already {
		c.unmatchedLogged[host] = true
	}
	c.unmatchedMu.Unlock()

	if !already {
		c.logger.Info("fpp-mqtt: message received for an unconfigured host; ignored (never becomes a resource)",
			"host", host)
	}
}

// subscribeAll (re)establishes every topicSpecs entry for every configured
// host in one MQTT SUBSCRIBE call — called from OnConnectionUp, on both
// the initial connection and every reconnection, mirroring
// internal/coordinator/broker's subscribeAll (see that function's doc
// comment for why calling it unconditionally on every connect is what
// makes a subscription survive a broker restart).
func (c *Collector) subscribeAll(ctx context.Context, sub subscriber) {
	opts := make([]paho.SubscribeOptions, 0, len(c.hosts)*len(topicSpecs))
	for _, hostName := range c.hosts {
		for suffix := range topicSpecs {
			opts = append(opts, paho.SubscribeOptions{
				Topic:             c.topicPrefix + "/" + hostName + "/" + suffix,
				QoS:               1,
				RetainAsPublished: false,
			})
		}
	}
	if len(opts) == 0 {
		return
	}
	if _, err := sub.Subscribe(ctx, &paho.Subscribe{Subscriptions: opts}); err != nil {
		c.logger.Error("fpp-mqtt: subscribe failed after connect; will not receive updates until the next reconnect",
			"error", err)
	}
}

// buildClientConfig constructs the autopaho.ClientConfig this Collector
// connects with, WITHOUT connecting — a pure function of c, so
// readonly_test.go can assert its shape (no WillMessage, CleanStart true,
// no session expiry) without a broker. Run is the only caller that
// actually dials.
//
// Per contract section 4.1: "CleanStart true, no session expiry, no Last
// Will, and no publish path." CleanStartOnInitialConnection true plus
// SessionExpiryInterval 0 (the zero value, left unset below) means no
// broker-side session survives a disconnect — this collector never relies
// on QoS>0 durability across a reconnect, since every topic it cares about
// is either retained (replayed automatically on resubscribe) or expected
// to arrive again soon on its own cadence.
func (c *Collector) buildClientConfig(ctx context.Context, serverURL *url.URL) autopaho.ClientConfig {
	cfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{serverURL},
		CleanStartOnInitialConnection: true,
		KeepAlive:                     connectKeepAlive,
		ConnectTimeout:                connectTimeout,
		ReconnectBackoff:              autopaho.DefaultExponentialBackoff(),
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			c.setConnected(true, "")
			c.logger.Info("fpp-mqtt: broker connection up", "broker", c.brokerURL)
			// Narrow to the read-only subscriber interface as the very
			// first thing this callback does with cm, before anything
			// else touches it — see doc.go's "read-only by construction"
			// section for exactly what this does and does not guarantee:
			// autopaho.ClientConfig.OnConnectionUp's own field type is
			// func(*autopaho.ConnectionManager, *paho.Connack), fixed by
			// the autopaho library, not by this package, so cm itself
			// arrives here as the wide, Publish-capable type no matter
			// what — sub is what keeps every call AFTER this line, and
			// everything subscribeAll itself does, from ever seeing
			// anything wider than Subscribe.
			var sub subscriber = cm
			c.subscribeAll(ctx, sub)
		},
		OnConnectionDown: func() bool {
			c.setConnected(false, "mqtt broker connection lost; will retry")
			c.logger.Warn("fpp-mqtt: broker connection lost; will retry", "broker", c.brokerURL)
			return true // never give up retrying — ADR-008's "broker loss is a management-plane outage only" applies here too
		},
		OnConnectError: func(err error) {
			// err is an autopaho/paho connection-level error (dial
			// failure, CONNACK reason code, etc.) — never the configured
			// password itself, so including it verbatim does not violate
			// doc.go's "password must never reach a log line" rule. It is
			// still never logged with the password alongside it: only
			// c.brokerURL (no userinfo per New's validation) and err.
			c.setConnected(false, "mqtt broker connect attempt failed: "+err.Error())
			c.logger.Warn("fpp-mqtt: broker connect attempt failed; will retry", "broker", c.brokerURL, "error", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID:          clientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){c.newPublishHandler()},
		},
	}

	if c.username != "" {
		cfg.ConnectUsername = c.username
		cfg.ConnectPassword = []byte(c.password)
	}

	// WillMessage is deliberately never set — see doc.go's "read-only by
	// construction" section and readonly_test.go's assertion on this exact
	// field.
	return cfg
}

// Run owns this Collector's connection to the broker: it connects (in the
// background; Run does not block on the connection coming up, mirroring
// internal/coordinator/broker.NewBrokerManager's "never blocks startup on
// a successful connection"), keeps it alive and resubscribed across
// reconnects, and returns once ctx is done, after a bounded graceful
// disconnect.
func (c *Collector) Run(ctx context.Context) error {
	serverURL, err := url.Parse(c.brokerURL)
	if err != nil {
		// New already validates this; guard anyway so a Collector built by
		// hand outside New (e.g. a test) fails clearly rather than looping
		// forever without ever attempting a connection.
		return fmt.Errorf("fppmqtt: parsing broker url %q: %w", c.brokerURL, err)
	}

	cfg := c.buildClientConfig(ctx, serverURL)

	cm, err := autopaho.NewConnection(ctx, cfg)
	if err != nil {
		return fmt.Errorf("fppmqtt: starting mqtt connection manager: %w", err)
	}

	<-ctx.Done()

	c.setConnected(false, "mqtt collector shutting down")

	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cm.Disconnect(dctx)
}
