// Package config holds the ShowMesh node agent's environment-driven runtime
// configuration. It deliberately does NOT import internal/coordinator/config:
// the coordinator and the agent are different processes, with different
// operators and different failure modes, and a dependency in either
// direction would tie their configuration shapes together for no reason —
// they are expected to diverge over time. Naming conventions, the
// getEnvDefault/LoadConfigFrom split, and the slog.LogValuer redaction
// pattern mirror the coordinator's config package (per the Task D spec) so
// the two read the same way side by side, without sharing code.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Config holds node agent runtime configuration loaded from the process
// environment. See docs/architecture/ARCHITECTURE.md sections 4.3 and 6,
// and ADR-002, ADR-008.
type Config struct {
	// NodeID is this agent's identity: the segment embedded in every
	// showmesh/nodes/<node-id>/... topic it publishes to. It defaults to
	// the OS hostname and is always syntactically valid per
	// mqttproto.ValidateNodeID by the time LoadConfig returns successfully;
	// see LoadConfig's doc comment for why a bad hostname fails at load
	// time rather than at first publish.
	NodeID string

	// NodeLabel is an optional human-readable name, distinct from the
	// machine NodeID, carried in the hello payload's Label field.
	NodeLabel string

	// MQTTBroker is the broker URL, e.g. "tcp://localhost:1883".
	MQTTBroker string

	// MQTTClientID is the MQTT client identifier the agent connects with.
	// Unlike the coordinator — a single instance, for which a fixed default
	// client ID is fine — every agent on the network needs a distinct
	// client ID by default, or the broker will boot whichever one connected
	// first each time a second agent dials in with the same ID. See
	// defaultClientID: the default is derived from NodeID rather than
	// fixed.
	MQTTClientID string

	// MQTTUsername and MQTTPassword are optional broker credentials.
	MQTTUsername string
	MQTTPassword string

	// LogLevel is one of "debug", "info", "warn", "error".
	LogLevel string

	// Capabilities is the capability set this agent advertises in its hello
	// payload. It defaults to an empty (non-nil) set: this Step 2 agent has
	// no real capability yet — no GStreamer, no media, no command handling
	// — and ADR-002 makes a capability advertisement a claim the node must
	// actually back up, so advertising anything else here would be exactly
	// the false claim CLAUDE.md forbids. SHOWMESH_NODE_CAPABILITIES (see
	// envNodeCapabilities) can populate this, but only for integration
	// testing or a deliberate operator override — there is no production
	// capability detection to wire it up to yet.
	Capabilities capability.Set

	// AssetDir is the node-local directory show assets (FSEQ, audio, media)
	// are downloaded into and played from. This agent has no other notion of
	// a persistent state directory yet, so the default is "./assets"
	// relative to the process's working directory rather than a subdirectory
	// of some existing state location; revisit this default if this package
	// ever grows a real state-directory concept.
	AssetDir string

	// AgentAPIToken is an optional bearer credential asset.fetch sends when
	// downloading asset bytes from the coordinator's read API. Only needed
	// when the coordinator has closed anonymous reads (ADR-024's
	// CloseReads); empty means send no Authorization header.
	AgentAPIToken string

	// AssetInventoryInterval is how often this agent publishes its asset
	// inventory report when nothing else (a completed asset.fetch) has
	// already triggered one. See internal/agent/assetinventory.go.
	AssetInventoryInterval time.Duration

	// RenderReportInterval is how often this agent publishes its render
	// pipeline health report when no pipeline state transition has already
	// triggered one. See internal/agent/renderreport.go. Shorter than
	// AssetInventoryInterval's default: a stuck or crash-looping render
	// pipeline is show-affecting in a way a stale asset list is not.
	RenderReportInterval time.Duration

	// AudioReportInterval is how often this agent publishes its audio
	// discovery report (internal/agent/audioreport.go) when no probe result
	// has already triggered one. Matches RenderReportInterval's default and
	// reasoning: a node that never runs audio still costs nothing publishing
	// an empty report on this cadence.
	AudioReportInterval time.Duration

	// MultiSyncListenAddr is the local "host:port" the render node's
	// MultiSync listener binds. Defaults to "" meaning
	// pkg/multisync.NewListener's own default (":32320", the fixed FPP
	// control port). Tests and any co-located non-production run MUST
	// override this to a non-32320 port; see pkg/multisync's ADR-013
	// warning on why this agent never sets AllowPortSharing.
	MultiSyncListenAddr string

	// MultiSyncInterface restricts the MultiSync multicast group join to
	// one named network interface. Empty means join every suitable
	// interface (pkg/multisync.NewListener's own default).
	MultiSyncInterface string
}

const (
	envNodeID           = "SHOWMESH_NODE_ID"
	envNodeLabel        = "SHOWMESH_NODE_LABEL"
	envNodeCapabilities = "SHOWMESH_NODE_CAPABILITIES"
	envMQTTBroker       = "SHOWMESH_MQTT_BROKER"
	envMQTTClientID     = "SHOWMESH_MQTT_CLIENT_ID"
	envMQTTUsername     = "SHOWMESH_MQTT_USERNAME"
	envMQTTPassword     = "SHOWMESH_MQTT_PASSWORD"
	envLogLevel         = "SHOWMESH_LOG_LEVEL"
	envAssetDir         = "SHOWMESH_ASSET_DIR"

	// envAgentAPIToken is deliberately NOT named SHOWMESH_API_TOKEN: the
	// coordinator refuses to start when it sees that variable name (ADR-024
	// decision 2, which retired it), so an operator who copies an agent env
	// line into a coordinator .env file must not brick the coordinator by
	// doing so.
	envAgentAPIToken          = "SHOWMESH_AGENT_API_TOKEN"
	envAssetInventoryInterval = "SHOWMESH_ASSET_INVENTORY_INTERVAL"
	envRenderReportInterval   = "SHOWMESH_RENDER_REPORT_INTERVAL"
	envAudioReportInterval    = "SHOWMESH_AUDIO_REPORT_INTERVAL"
	envMultiSyncListenAddr    = "SHOWMESH_MULTISYNC_LISTEN_ADDR"
	envMultiSyncInterface     = "SHOWMESH_MULTISYNC_INTERFACE"

	defaultBroker                 = "tcp://localhost:1883"
	defaultLogLevel               = "info"
	defaultAssetDir               = "./assets"
	defaultAssetInventoryInterval = 2 * time.Minute
	defaultRenderReportInterval   = 15 * time.Second
	defaultAudioReportInterval    = 15 * time.Second
)

// validLogLevels enumerates the accepted values for SHOWMESH_LOG_LEVEL.
var validLogLevels = map[string]bool{
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
}

// validBrokerSchemes enumerates the URL schemes accepted for
// SHOWMESH_MQTT_BROKER. url.Parse alone is not sufficient validation: it
// happily accepts a schemeless value like "broker-host:1883" by parsing
// "broker-host" as the scheme and "1883" as an opaque part, which would
// then fail at connect time in a confusing retry loop instead of at config
// load. This duplicates internal/coordinator/config's list rather than
// importing it; see the package doc comment for why.
var validBrokerSchemes = map[string]bool{
	"tcp":   true,
	"ssl":   true,
	"tls":   true,
	"mqtt":  true,
	"mqtts": true,
	"ws":    true,
	"wss":   true,
}

var validBrokerSchemesList = []string{"tcp", "ssl", "tls", "mqtt", "mqtts", "ws", "wss"}

// LoadConfig reads agent configuration from the environment and the OS
// hostname, applying defaults for unset variables, and validates the
// result. On failure the returned error names the offending environment
// variable. SHOWMESH_MQTT_PASSWORD is never included in any error or log
// output.
func LoadConfig() (Config, error) {
	return LoadConfigFrom(os.LookupEnv, os.Hostname)
}

// LoadConfigFrom is LoadConfig with the environment lookup and hostname
// source made explicit, so tests can exercise both the unset-variable path
// and the hostname-fallback path (including an invalid hostname) without
// touching real process/OS state.
func LoadConfigFrom(lookup func(string) (string, bool), hostname func() (string, error)) (Config, error) {
	nodeID, explicit, err := resolveNodeID(lookup, hostname)
	if err != nil {
		return Config{}, err
	}
	if err := mqttproto.ValidateNodeID(nodeID); err != nil {
		if explicit {
			return Config{}, fmt.Errorf("%s=%q: %w", envNodeID, nodeID, err)
		}
		return Config{}, fmt.Errorf(
			"%s is unset and the OS hostname %q is not a valid node ID: %w; set %s explicitly to a valid node ID",
			envNodeID, nodeID, err, envNodeID)
	}

	capabilities, err := parseCapabilities(getEnvDefault(lookup, envNodeCapabilities, ""))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", envNodeCapabilities, err)
	}

	assetInventoryInterval := defaultAssetInventoryInterval
	if raw, ok := lookup(envAssetInventoryInterval); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s %q is not a valid duration: %w", envAssetInventoryInterval, raw, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s %q must be positive", envAssetInventoryInterval, raw)
		}
		assetInventoryInterval = d
	}

	renderReportInterval := defaultRenderReportInterval
	if raw, ok := lookup(envRenderReportInterval); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s %q is not a valid duration: %w", envRenderReportInterval, raw, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s %q must be positive", envRenderReportInterval, raw)
		}
		renderReportInterval = d
	}

	audioReportInterval := defaultAudioReportInterval
	if raw, ok := lookup(envAudioReportInterval); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s %q is not a valid duration: %w", envAudioReportInterval, raw, err)
		}
		if d <= 0 {
			return Config{}, fmt.Errorf("%s %q must be positive", envAudioReportInterval, raw)
		}
		audioReportInterval = d
	}

	cfg := Config{
		NodeID:                 nodeID,
		NodeLabel:              getEnvDefault(lookup, envNodeLabel, ""),
		MQTTBroker:             getEnvDefault(lookup, envMQTTBroker, defaultBroker),
		MQTTClientID:           getEnvDefault(lookup, envMQTTClientID, defaultClientID(nodeID)),
		MQTTUsername:           getEnvDefault(lookup, envMQTTUsername, ""),
		MQTTPassword:           getEnvDefault(lookup, envMQTTPassword, ""),
		LogLevel:               getEnvDefault(lookup, envLogLevel, defaultLogLevel),
		Capabilities:           capabilities,
		AssetDir:               getEnvDefault(lookup, envAssetDir, defaultAssetDir),
		AgentAPIToken:          getEnvDefault(lookup, envAgentAPIToken, ""),
		AssetInventoryInterval: assetInventoryInterval,
		RenderReportInterval:   renderReportInterval,
		AudioReportInterval:    audioReportInterval,
		MultiSyncListenAddr:    getEnvDefault(lookup, envMultiSyncListenAddr, ""),
		MultiSyncInterface:     getEnvDefault(lookup, envMultiSyncInterface, ""),
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// resolveNodeID returns SHOWMESH_NODE_ID when set to a non-empty value
// (explicit=true), or the OS hostname otherwise (explicit=false). explicit
// is reported back so LoadConfigFrom can phrase a validation error
// differently depending on whether the operator chose the value or it was
// merely inherited from the OS.
func resolveNodeID(lookup func(string) (string, bool), hostname func() (string, error)) (id string, explicit bool, err error) {
	if v, ok := lookup(envNodeID); ok && v != "" {
		return v, true, nil
	}
	h, err := hostname()
	if err != nil {
		return "", false, fmt.Errorf("%s is not set and the OS hostname could not be determined: %w", envNodeID, err)
	}
	return h, false, nil
}

// defaultClientID derives the default MQTT client ID from nodeID; see
// Config.MQTTClientID's doc comment for why the agent cannot share the
// coordinator's fixed-string default.
func defaultClientID(nodeID string) string {
	return "showmesh-agent-" + nodeID
}

// parseCapabilities parses SHOWMESH_NODE_CAPABILITIES: a comma-separated
// list of "id" or "id:version" entries (version defaults to 1 when
// omitted). Surrounding whitespace on each entry is trimmed, and empty
// entries (e.g. from a trailing comma) are skipped. An empty or
// whitespace-only raw string yields an empty, non-nil Set — see
// Config.Capabilities' doc comment for why "explicitly empty" (encodes as
// JSON "[]") matters here, not merely "absent" (which a nil slice would
// encode as JSON "null"). This format cannot express Capability.Attributes;
// that is acceptable because this variable exists for testing/override, not
// as a real capability-declaration mechanism.
func parseCapabilities(raw string) (capability.Set, error) {
	raw = strings.TrimSpace(raw)
	set := capability.Set{}
	if raw == "" {
		return set, nil
	}

	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		id, versionStr, hasVersion := strings.Cut(entry, ":")
		version := 1
		if hasVersion {
			v, err := strconv.Atoi(strings.TrimSpace(versionStr))
			if err != nil {
				return nil, fmt.Errorf("entry %q: version %q is not an integer: %w", entry, versionStr, err)
			}
			version = v
		}

		set = append(set, capability.Capability{ID: capability.ID(id), Version: version})
	}

	if err := set.Validate(); err != nil {
		// set.Validate's own error already names the offending capability;
		// LoadConfigFrom's caller wraps whatever this function returns with
		// "%s: %w" (envNodeCapabilities: err), so wrapping again here would
		// only have added a redundant layer with no new context.
		return nil, err
	}

	return set, nil
}

// Validate checks that the configuration is internally consistent. It does
// not attempt to reach the network. NodeID and Capabilities are validated
// during LoadConfigFrom itself (before Config is even fully built), so
// Validate does not repeat those checks.
func (c Config) Validate() error {
	if c.MQTTBroker == "" {
		return fmt.Errorf("%s must not be empty", envMQTTBroker)
	}
	brokerURL, err := url.Parse(c.MQTTBroker)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w, must have one of the schemes %s",
			envMQTTBroker, c.MQTTBroker, err, strings.Join(validBrokerSchemesList, ", "))
	}
	if !validBrokerSchemes[brokerURL.Scheme] {
		return fmt.Errorf("%s %q must use one of the schemes %s",
			envMQTTBroker, c.MQTTBroker, strings.Join(validBrokerSchemesList, ", "))
	}
	if brokerURL.Host == "" {
		return fmt.Errorf("%s %q must include a host, e.g. %s://broker:1883",
			envMQTTBroker, c.MQTTBroker, brokerURL.Scheme)
	}

	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("%s must be one of debug|info|warn|error, got %q", envLogLevel, c.LogLevel)
	}

	// mqtt.go's newMQTTConn only sets ConnectUsername/ConnectPassword on the
	// autopaho client config when MQTTUsername is non-empty (see its "if
	// cfg.MQTTUsername != """ guard), so a password set with no username is
	// silently never sent to the broker. Left unchecked, that surfaces as a
	// confusing broker-side auth failure — or, worse, a broker configured to
	// allow anonymous connects would just let the agent connect
	// unauthenticated, silently discarding a credential the operator thought
	// they had configured. Catch it here, at config load, where the error
	// can name the actual mistake.
	if c.MQTTUsername == "" && c.MQTTPassword != "" {
		return fmt.Errorf("%s is set but %s is empty: an MQTT password requires a username", envMQTTPassword, envMQTTUsername)
	}

	if c.AssetDir == "" {
		return fmt.Errorf("%s must not be empty", envAssetDir)
	}

	if c.AssetInventoryInterval <= 0 {
		return fmt.Errorf("%s must be positive", envAssetInventoryInterval)
	}

	if c.RenderReportInterval <= 0 {
		return fmt.Errorf("%s must be positive", envRenderReportInterval)
	}

	if c.AudioReportInterval <= 0 {
		return fmt.Errorf("%s must be positive", envAudioReportInterval)
	}

	return nil
}

func getEnvDefault(lookup func(string) (string, bool), key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

// redactedPassword is what LogValue prints in place of a non-empty
// MQTTPassword. It is a fixed placeholder, not a hash or length hint, so it
// leaks nothing about the real value.
const redactedPassword = "REDACTED"

// LogValue implements slog.LogValuer so that logging a Config (directly, or
// nested in another value passed to a slog call) never emits
// SHOWMESH_MQTT_PASSWORD or SHOWMESH_AGENT_API_TOKEN in the clear. This is
// what actually enforces the promise documented on LoadConfig; the doc
// comment alone enforces nothing.
func (c Config) LogValue() slog.Value {
	password := ""
	if c.MQTTPassword != "" {
		password = redactedPassword
	}
	token := ""
	if c.AgentAPIToken != "" {
		token = redactedPassword
	}

	return slog.GroupValue(
		slog.String("node_id", c.NodeID),
		slog.String("node_label", c.NodeLabel),
		slog.String("mqtt_broker", c.MQTTBroker),
		slog.String("mqtt_client_id", c.MQTTClientID),
		slog.String("mqtt_username", c.MQTTUsername),
		slog.String("mqtt_password", password),
		slog.String("log_level", c.LogLevel),
		slog.Int("capability_count", len(c.Capabilities)),
		slog.String("asset_dir", c.AssetDir),
		slog.String("agent_api_token", token),
		slog.Duration("asset_inventory_interval", c.AssetInventoryInterval),
		slog.Duration("render_report_interval", c.RenderReportInterval),
		slog.Duration("audio_report_interval", c.AudioReportInterval),
		slog.String("multisync_listen_addr", c.MultiSyncListenAddr),
		slog.String("multisync_interface", c.MultiSyncInterface),
	)
}
