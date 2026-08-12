package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// Config holds coordinator runtime configuration loaded from the process
// environment. See docs/architecture/ARCHITECTURE.md and ADR-008 for the
// values these correspond to.
type Config struct {
	// HTTPAddr is the listen address for the coordinator's HTTP server,
	// e.g. ":8080".
	HTTPAddr string

	// MQTTBroker is the broker URL, e.g. "tcp://localhost:1883".
	MQTTBroker string

	// MQTTClientID is the MQTT client identifier the coordinator connects
	// with.
	MQTTClientID string

	// MQTTUsername and MQTTPassword are optional broker credentials.
	MQTTUsername string
	MQTTPassword string

	// DataDir is the coordinator's local data directory (SQLite database,
	// YAML export bundles, etc. — per ADR-009).
	DataDir string

	// LogLevel is one of "debug", "info", "warn", "error".
	LogLevel string

	// --- Step 3 Task D: the versioned public control API (ADR-014) ---

	// APIToken is SHOWMESH_API_TOKEN's value: the optional shared secret
	// that, when non-empty, every /api/v1/* request (including the SSE
	// stream) must present as "Authorization: Bearer <token>" per contract
	// section 6.8. Empty means the API is served with no authentication at
	// all — a deliberate, documented default, not an oversight; the
	// caller that wires internal/coordinator/api.New is responsible for
	// logging the startup warning contract section 6.8 requires when this
	// is empty. Like MQTTPassword, this is a secret and must never appear
	// in an error body, a log line, or a problem detail; see LogValue.
	APIToken string

	// APIAllowedOrigins is SHOWMESH_API_ALLOWED_ORIGINS, comma-split into
	// individual origins. Empty (the default) means no CORS headers are
	// emitted at all for /api/v1/*, per contract section 6.8: this
	// coordinator does not reflect an arbitrary Origin and does not pair a
	// wildcard with credentials.
	APIAllowedOrigins []string

	// --- end Step 3 Task D ---

	// FPPEndpoints are the FPP instances the coordinator's FPP REST
	// collector polls, from SHOWMESH_FPP_ENDPOINTS. Empty (the default,
	// nil) means the collector does not run at all: per the Step 3
	// contract section 2, that is not an error and it is not silence
	// either — see internal/coordinator/collector/fpp's package doc
	// comment for how the API is expected to render "nothing configured"
	// as a stated fact (StateNotCollected) rather than an absent list that
	// reads as a healthy empty system.
	FPPEndpoints []FPPEndpoint

	// --- Step 5 Seam B: the FPP MQTT collector (internal/coordinator/collector/fppmqtt) ---

	// FPPMQTTBrokerURL is SHOWMESH_FPP_MQTT_BROKER_URL, e.g.
	// "tcp://broker.example:1883". Empty (the default) means the FPP
	// MQTT collector is never constructed at all — no startup warning, no
	// failed-connection signals for a feature the operator did not enable.
	// This is a deliberately separate broker connection from MQTTBroker
	// above: MQTTBroker is ADR-008's ShowMesh control plane, and this is a
	// second, unrelated MQTT source (an operator's existing FPP/home-
	// automation broker) that the coordinator only ever subscribes to —
	// see internal/coordinator/collector/fppmqtt's package doc comment for
	// why the two must never be merged.
	FPPMQTTBrokerURL string

	// FPPMQTTUsername and FPPMQTTPassword are optional credentials for
	// FPPMQTTBrokerURL. FPPMQTTPassword is exactly as sensitive as
	// MQTTPassword and never appears in an error, a log line, or LogValue's
	// output in the clear — see LogValue.
	FPPMQTTUsername string
	FPPMQTTPassword string

	// FPPMQTTTopicPrefix is SHOWMESH_FPP_MQTT_TOPIC_PREFIX, the topic root
	// FPP publishes under (e.g. "falcon/player"). Defaults to
	// defaultFPPMQTTTopicPrefix when unset; never assumed empty, because
	// the reference fleet's MQTTPrefix setting is unset today but the
	// field this backs is genuinely configurable on FPP's side (contract
	// section 1.2).
	FPPMQTTTopicPrefix string

	// FPPMQTTHosts maps a coordinator FPP instance id (matching an entry
	// in FPPEndpoints) to that instance's FPP HostName as it appears in
	// its MQTT topics, from SHOWMESH_FPP_MQTT_HOSTS. Empty (the default,
	// nil) means the collector ingests nothing for any host — see
	// Validate for the requirement that every id here also appear in
	// FPPEndpoints, which is what keeps a stray publish from an
	// unconfigured host from ever becoming a new resource (contract
	// section 4.4).
	FPPMQTTHosts map[string]string
}

// FPPEndpoint is one configured FPP instance for the coordinator's FPP REST
// collector to poll, parsed from one "id=url" pair in
// SHOWMESH_FPP_ENDPOINTS.
type FPPEndpoint struct {
	// ID identifies this instance on the wire and in logs. Same syntax as
	// an agent node ID (Step 3 contract section 7): validated with
	// [mqttproto.ValidateNodeID] rather than a second, possibly-drifting
	// copy of that regexp.
	ID string

	// URL is the base URL of this FPP's HTTP API, e.g. "http://10.0.1.20".
	// Validate rejects a URL carrying userinfo (e.g.
	// "http://user:pass@host") at config load, rather than deferring that
	// leak risk to whatever code later logs a poll failure or renders this
	// endpoint on the API (contract section 2: "must not leak a
	// credential or a full URL with userinfo"). FPP's REST API has no
	// notion of per-request credentials in userinfo form anyway, so
	// rejecting it costs nothing real and closes the leak at its only
	// entry point instead of relying on every downstream consumer to
	// remember to scrub it.
	URL string
}

const (
	// EnvHTTPAddr is the environment variable naming the HTTP listen
	// address. It is exported so callers (e.g. the -healthcheck flag) can
	// honor it without going through full config validation.
	EnvHTTPAddr = "SHOWMESH_HTTP_ADDR"

	// DefaultHTTPAddr is used when EnvHTTPAddr is unset.
	DefaultHTTPAddr = ":8080"

	envMQTTBroker   = "SHOWMESH_MQTT_BROKER"
	envMQTTClientID = "SHOWMESH_MQTT_CLIENT_ID"
	envMQTTUsername = "SHOWMESH_MQTT_USERNAME"
	envMQTTPassword = "SHOWMESH_MQTT_PASSWORD"
	envDataDir      = "SHOWMESH_DATA_DIR"
	envLogLevel     = "SHOWMESH_LOG_LEVEL"
	defaultBroker   = "tcp://localhost:1883"
	defaultClientID = "showmesh-coordinator"
	defaultDataDir  = "/var/lib/showmesh"
	defaultLogLevel = "info"

	// envFPPEndpoints is SHOWMESH_FPP_ENDPOINTS, a comma-separated list of
	// "id=url" pairs, e.g.
	// "player-01=http://10.0.1.20,shed=http://10.0.1.21". Unset or empty
	// means no FPP collector runs; see [Config.FPPEndpoints].
	envFPPEndpoints = "SHOWMESH_FPP_ENDPOINTS"

	// envAPIToken and envAPIAllowedOrigins back [Config.APIToken] and
	// [Config.APIAllowedOrigins]. See those fields' doc comments.
	envAPIToken          = "SHOWMESH_API_TOKEN"
	envAPIAllowedOrigins = "SHOWMESH_API_ALLOWED_ORIGINS"

	// envFPPMQTTBrokerURL, envFPPMQTTUsername, envFPPMQTTPassword,
	// envFPPMQTTTopicPrefix, and envFPPMQTTHosts back the Step 5 Seam B
	// fields above. See each field's doc comment.
	envFPPMQTTBrokerURL   = "SHOWMESH_FPP_MQTT_BROKER_URL"
	envFPPMQTTUsername    = "SHOWMESH_FPP_MQTT_USERNAME"
	envFPPMQTTPassword    = "SHOWMESH_FPP_MQTT_PASSWORD"
	envFPPMQTTTopicPrefix = "SHOWMESH_FPP_MQTT_TOPIC_PREFIX"
	envFPPMQTTHosts       = "SHOWMESH_FPP_MQTT_HOSTS"

	// defaultFPPMQTTTopicPrefix matches the reference fleet's actual,
	// unprefixed topic root (contract section 1.2: "MQTTPrefix is unset on
	// this fleet, so there is no extra prefix segment"), not a guess.
	defaultFPPMQTTTopicPrefix = "falcon/player"
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
// load.
var validBrokerSchemes = map[string]bool{
	"tcp":   true,
	"ssl":   true,
	"tls":   true,
	"mqtt":  true,
	"mqtts": true,
	"ws":    true,
	"wss":   true,
}

// validBrokerSchemesList is validBrokerSchemes rendered for error messages,
// in a stable order.
var validBrokerSchemesList = []string{"tcp", "ssl", "tls", "mqtt", "mqtts", "ws", "wss"}

// LoadConfig reads coordinator configuration from the environment, applying
// defaults for unset variables, and validates the result. On failure the
// returned error names the offending environment variable. The
// SHOWMESH_MQTT_PASSWORD value is never included in any error or log output.
func LoadConfig() (Config, error) {
	return LoadConfigFrom(os.LookupEnv)
}

// LoadConfigFrom is LoadConfig with the environment lookup made explicit, so
// tests can exercise the unset-variable path without touching process-wide
// environment state (which t.Setenv cannot do, since an empty string is a
// meaningful, distinct value from "unset" for every one of these variables).
func LoadConfigFrom(lookup func(string) (string, bool)) (Config, error) {
	fppEndpoints, err := parseFPPEndpoints(getEnvDefault(lookup, envFPPEndpoints, ""))
	if err != nil {
		return Config{}, err
	}

	fppMQTTHosts, err := parseFPPMQTTHosts(getEnvDefault(lookup, envFPPMQTTHosts, ""))
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:     getEnvDefault(lookup, EnvHTTPAddr, DefaultHTTPAddr),
		MQTTBroker:   getEnvDefault(lookup, envMQTTBroker, defaultBroker),
		MQTTClientID: getEnvDefault(lookup, envMQTTClientID, defaultClientID),
		MQTTUsername: getEnvDefault(lookup, envMQTTUsername, ""),
		MQTTPassword: getEnvDefault(lookup, envMQTTPassword, ""),
		DataDir:      getEnvDefault(lookup, envDataDir, defaultDataDir),
		LogLevel:     getEnvDefault(lookup, envLogLevel, defaultLogLevel),
		FPPEndpoints: fppEndpoints,

		APIToken:          getEnvDefault(lookup, envAPIToken, ""),
		APIAllowedOrigins: parseAPIAllowedOrigins(getEnvDefault(lookup, envAPIAllowedOrigins, "")),

		FPPMQTTBrokerURL:   getEnvDefault(lookup, envFPPMQTTBrokerURL, ""),
		FPPMQTTUsername:    getEnvDefault(lookup, envFPPMQTTUsername, ""),
		FPPMQTTPassword:    getEnvDefault(lookup, envFPPMQTTPassword, ""),
		FPPMQTTTopicPrefix: getEnvDefault(lookup, envFPPMQTTTopicPrefix, defaultFPPMQTTTopicPrefix),
		FPPMQTTHosts:       fppMQTTHosts,
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// parseFPPEndpoints splits raw (SHOWMESH_FPP_ENDPOINTS's value) into
// structural id=url pairs. It is deliberately shallow: it rejects a
// malformed pair shape (missing "=", an empty id or url) by name, but
// leaves the semantic checks — id syntax, URL scheme/host, no userinfo,
// duplicate ids — to [Config.Validate], mirroring how MQTTBroker's URL
// syntax is validated there rather than at parse time. An empty raw string
// (the unset/default case) returns (nil, nil): no endpoints, not an error.
func parseFPPEndpoints(raw string) ([]FPPEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	entries := strings.Split(raw, ",")
	endpoints := make([]FPPEndpoint, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("%s: contains an empty entry (check for a stray comma)", envFPPEndpoints)
		}

		id, rawURL, ok := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		rawURL = strings.TrimSpace(rawURL)
		if !ok || id == "" || rawURL == "" {
			return nil, fmt.Errorf("%s: entry %q must have the form id=url", envFPPEndpoints, entry)
		}

		endpoints = append(endpoints, FPPEndpoint{ID: id, URL: rawURL})
	}

	return endpoints, nil
}

// parseFPPMQTTHosts splits raw (SHOWMESH_FPP_MQTT_HOSTS's value) into
// id=HostName pairs, mirroring [parseFPPEndpoints]'s shape and division of
// labor: structural checks (an empty entry, a missing "=", an empty id or
// HostName, a duplicate id within this variable) are rejected here by
// name; the semantic cross-check against SHOWMESH_FPP_ENDPOINTS is
// [Config.Validate]'s job, the same split parseFPPEndpoints uses for its
// own id syntax and URL checks. An empty raw string (the unset/default
// case) returns (nil, nil): no hosts, not an error.
//
// Unlike parseFPPEndpoints's URL half, HostName's syntax IS checked here
// (via [mqttproto.ValidateNodeID] for the id half and a direct character
// check for HostName): HostName is placed directly into an MQTT topic
// filter string by internal/coordinator/collector/fppmqtt, so a HostName
// containing '/', '+', '#', or whitespace is a topic-injection risk the
// same way an unvalidated node id would be (see
// pkg/mqttproto.ValidateNodeID's doc comment for the precedent), not
// merely a cosmetic concern deferred to a later layer.
func parseFPPMQTTHosts(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	entries := strings.Split(raw, ",")
	hosts := make(map[string]string, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("%s: contains an empty entry (check for a stray comma)", envFPPMQTTHosts)
		}

		id, hostName, ok := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		hostName = strings.TrimSpace(hostName)
		if !ok || id == "" || hostName == "" {
			return nil, fmt.Errorf("%s: entry %q must have the form id=HostName", envFPPMQTTHosts, entry)
		}

		if err := mqttproto.ValidateNodeID(id); err != nil {
			return nil, fmt.Errorf("%s: instance id %q: %w", envFPPMQTTHosts, id, err)
		}
		if err := validateFPPMQTTHostName(hostName); err != nil {
			return nil, fmt.Errorf("%s: instance %q: %w", envFPPMQTTHosts, id, err)
		}
		if _, dup := hosts[id]; dup {
			return nil, fmt.Errorf("%s: duplicate instance id %q", envFPPMQTTHosts, id)
		}

		hosts[id] = hostName
	}

	return hosts, nil
}

// fppMQTTHostNameForbidden matches any character that must never appear in
// an FPP HostName accepted by SHOWMESH_FPP_MQTT_HOSTS: MQTT's own
// wildcard/level-separator characters plus whitespace. See
// parseFPPMQTTHosts's doc comment for why this is a real injection check,
// not cosmetic validation.
var fppMQTTHostNameForbidden = regexp.MustCompile(`[+#/\s]`)

func validateFPPMQTTHostName(hostName string) error {
	if fppMQTTHostNameForbidden.MatchString(hostName) {
		return fmt.Errorf("HostName %q must not contain '/', '+', '#', or whitespace", hostName)
	}
	return nil
}

// parseAPIAllowedOrigins splits raw (SHOWMESH_API_ALLOWED_ORIGINS's value)
// on commas, trimming whitespace and dropping empty entries (so a trailing
// comma or accidental double comma does not produce a spurious empty-string
// "origin" that could never legitimately match a request's Origin header
// anyway). An empty raw string returns nil: no configured origins, meaning
// no CORS headers are ever emitted — see [Config.APIAllowedOrigins].
func parseAPIAllowedOrigins(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var origins []string
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimSpace(o)
		if o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

// Validate checks that the configuration is internally consistent. It does
// not attempt to reach the network.
func (c Config) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("%s must not be empty", EnvHTTPAddr)
	}

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

	if err := validateFPPEndpoints(c.FPPEndpoints); err != nil {
		return err
	}

	if err := validateFPPMQTTConfig(c); err != nil {
		return err
	}

	return nil
}

// validateFPPEndpoints enforces the semantic rules a structural
// parseFPPEndpoints pair must additionally satisfy: a valid node-ID-syntax
// id (contract section 7), a URL with an http/https scheme and a host, no
// embedded userinfo, and no id repeated across the list. Per the Step 3
// Task C spec, a malformed entry is a startup error naming the offending
// value, not a silently skipped endpoint — every error here names the
// specific id or URL that failed.
func validateFPPEndpoints(endpoints []FPPEndpoint) error {
	seen := make(map[string]bool, len(endpoints))

	for _, ep := range endpoints {
		if err := mqttproto.ValidateNodeID(ep.ID); err != nil {
			return fmt.Errorf("%s: instance id %q: %w", envFPPEndpoints, ep.ID, err)
		}
		if seen[ep.ID] {
			return fmt.Errorf("%s: duplicate instance id %q", envFPPEndpoints, ep.ID)
		}
		seen[ep.ID] = true

		u, err := url.Parse(ep.URL)
		if err != nil {
			return fmt.Errorf("%s: instance %q: url %q is not valid: %w", envFPPEndpoints, ep.ID, ep.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("%s: instance %q: url %q must use http or https", envFPPEndpoints, ep.ID, ep.URL)
		}
		if u.Host == "" {
			return fmt.Errorf("%s: instance %q: url %q must include a host", envFPPEndpoints, ep.ID, ep.URL)
		}
		if u.User != nil {
			// See FPPEndpoint.URL's doc comment: rejected here, at the
			// only entry point, rather than relying on every downstream
			// consumer (log lines, API rendering, error reasons) to
			// remember to strip it.
			return fmt.Errorf("%s: instance %q: url must not include userinfo/credentials", envFPPEndpoints, ep.ID)
		}
	}

	return nil
}

// validateFPPMQTTConfig enforces the Step 5 contract section 4.4 rule
// ("every id in SHOWMESH_FPP_MQTT_HOSTS must also appear in
// SHOWMESH_FPP_ENDPOINTS; reject the configuration at startup otherwise,
// with a message naming the unmatched ID") plus the two structural checks
// that make the feature-flag shape ("unset broker URL means the collector
// is never constructed") actually coherent at startup rather than only at
// [fppmqtt.New] time:
//
//   - FPPMQTTBrokerURL, when set, must be a valid URL with an accepted
//     scheme and a host — the same rule MQTTBroker enforces above, applied
//     to the second, unrelated broker connection this feature configures.
//   - FPPMQTTBrokerURL set with no FPPMQTTHosts configured is rejected: a
//     collector with nowhere to subscribe is very likely a missing
//     SHOWMESH_FPP_MQTT_HOSTS, not a deliberate "connect and do nothing"
//     configuration, and rejecting it here gives a startup error instead of
//     a collector that silently ingests nothing forever.
//
// The id<->FPPEndpoints cross-check runs whenever FPPMQTTHosts is
// non-empty, independent of whether FPPMQTTBrokerURL is set: a HOSTS
// mapping prepared for a collector that will never run (broker URL still
// unset) is far more likely a typo than a deliberate "prepare for later"
// gesture, and the contract requires the cross-check unconditionally.
// brokerURLHasUserinfo reports whether raw carries userinfo (`user:pass@`)
// in its authority section, using a purely textual check that runs BEFORE
// url.Parse.
//
// The ordering is the whole point. url.Parse's own *url.Error embeds the
// URL it failed on, so a malformed AND credentialed value reaches a log or
// an operator's terminal through the error text even if every later format
// string is careful. A password supplied through SHOWMESH_FPP_MQTT_PASSWORD
// is redacted in LogValue; the same password supplied inside the broker URL
// was not, which made the redaction a convention rather than a guarantee.
//
// The check is: between "//" and the first subsequent "/", is there an "@"?
// That is exactly RFC 3986's authority component, and it does not depend on
// the rest of the string being parseable.
func brokerURLHasUserinfo(raw string) bool {
	_, after, found := strings.Cut(raw, "//")
	if !found {
		return false
	}
	authority, _, _ := strings.Cut(after, "/")
	return strings.Contains(authority, "@")
}

// redactURLUserinfo returns raw with any authority-section userinfo replaced
// by "redacted", for use in log output. It is deliberately textual and total:
// it must not fail, and must not return the original on a value it could not
// parse, because the unparseable case is exactly when a caller would
// otherwise log the raw string.
func redactURLUserinfo(raw string) string {
	before, after, found := strings.Cut(raw, "//")
	if !found {
		return raw
	}
	authority, rest, hadRest := strings.Cut(after, "/")
	if !strings.Contains(authority, "@") {
		return raw
	}
	_, host, _ := strings.Cut(authority, "@")
	out := before + "//redacted@" + host
	if hadRest {
		out += "/" + rest
	}
	return out
}

func validateFPPMQTTConfig(c Config) error {
	if c.FPPMQTTBrokerURL != "" {
		// Checked first, and reported without echoing the value, so a
		// credential embedded in the URL never reaches an error string.
		// internal/coordinator/collector/fppmqtt's New rejects the same
		// shape, so accepting it here would only defer the failure to a
		// point where the operator has less context.
		if brokerURLHasUserinfo(c.FPPMQTTBrokerURL) {
			return fmt.Errorf("%s must not embed credentials in the URL; set %s and %s instead",
				envFPPMQTTBrokerURL, envFPPMQTTUsername, envFPPMQTTPassword)
		}
		brokerURL, err := url.Parse(c.FPPMQTTBrokerURL)
		if err != nil {
			return fmt.Errorf("%s %q is not a valid URL: %w, must have one of the schemes %s",
				envFPPMQTTBrokerURL, c.FPPMQTTBrokerURL, err, strings.Join(validBrokerSchemesList, ", "))
		}
		if !validBrokerSchemes[brokerURL.Scheme] {
			return fmt.Errorf("%s %q must use one of the schemes %s",
				envFPPMQTTBrokerURL, c.FPPMQTTBrokerURL, strings.Join(validBrokerSchemesList, ", "))
		}
		if brokerURL.Host == "" {
			return fmt.Errorf("%s %q must include a host, e.g. %s://broker:1883",
				envFPPMQTTBrokerURL, c.FPPMQTTBrokerURL, brokerURL.Scheme)
		}
		if len(c.FPPMQTTHosts) == 0 {
			return fmt.Errorf("%s is set but %s configures no hosts", envFPPMQTTBrokerURL, envFPPMQTTHosts)
		}
	}

	if len(c.FPPMQTTHosts) == 0 {
		return nil
	}

	knownFPPEndpoints := make(map[string]bool, len(c.FPPEndpoints))
	for _, ep := range c.FPPEndpoints {
		knownFPPEndpoints[ep.ID] = true
	}
	for id := range c.FPPMQTTHosts {
		if !knownFPPEndpoints[id] {
			return fmt.Errorf("%s: instance id %q is not configured in %s", envFPPMQTTHosts, id, envFPPEndpoints)
		}
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
// SHOWMESH_MQTT_PASSWORD in the clear. This is what actually enforces the
// promise documented on LoadConfig; the doc comment alone enforced nothing.
func (c Config) LogValue() slog.Value {
	password := ""
	if c.MQTTPassword != "" {
		password = redactedPassword
	}

	// apiToken is redacted the same way and for the same reason as
	// MQTTPassword above: this field is a shared secret (contract section
	// 6.8), and the enforcement that matters is this method existing at
	// all, not a doc comment promising it — see [Config.APIToken].
	apiToken := ""
	if c.APIToken != "" {
		apiToken = redactedPassword
	}

	// fppMQTTPassword is redacted the same way and for the same reason as
	// mqtt_password and api_token above: this is SHOWMESH_FPP_MQTT_PASSWORD,
	// exactly as sensitive as the control-plane broker's password, and the
	// Step 5 contract requires it "never appear in a log line" — see
	// [Config.FPPMQTTPassword].
	fppMQTTPassword := ""
	if c.FPPMQTTPassword != "" {
		fppMQTTPassword = redactedPassword
	}

	return slog.GroupValue(
		slog.String("http_addr", c.HTTPAddr),
		// Both broker URLs are redacted rather than logged verbatim.
		// SHOWMESH_MQTT_BROKER is NOT rejected for carrying userinfo the
		// way the FPP MQTT one is, because it is the ADR-008 control-plane
		// broker and an existing deployment may legitimately be configured
		// that way; redaction is the part that must hold either way.
		slog.String("mqtt_broker", redactURLUserinfo(c.MQTTBroker)),
		slog.String("mqtt_client_id", c.MQTTClientID),
		slog.String("mqtt_username", c.MQTTUsername),
		slog.String("mqtt_password", password),
		slog.String("data_dir", c.DataDir),
		slog.String("log_level", c.LogLevel),
		slog.Any("fpp_endpoints", fppEndpointIDs(c.FPPEndpoints)),
		slog.String("api_token", apiToken),
		slog.Any("api_allowed_origins", c.APIAllowedOrigins),
		slog.String("fpp_mqtt_broker_url", redactURLUserinfo(c.FPPMQTTBrokerURL)),
		slog.String("fpp_mqtt_username", c.FPPMQTTUsername),
		slog.String("fpp_mqtt_password", fppMQTTPassword),
		slog.String("fpp_mqtt_topic_prefix", c.FPPMQTTTopicPrefix),
		// HostNames are not secret (see parseFPPMQTTHosts's doc comment:
		// they are validated topic-safe strings, not credentials), so the
		// full id->HostName map is logged directly, unlike FPPEndpoints
		// (which strips to ids only because its URLs, while not secret
		// either, are simply less useful here than knowing which hosts
		// this feature is watching).
		slog.Any("fpp_mqtt_hosts", c.FPPMQTTHosts),
	)
}

// fppEndpointIDs renders FPPEndpoints as just their ids for logging: the
// URLs themselves are not secret (Validate forbids userinfo, so there is
// nothing to redact), but a log line naming which instances are configured
// is more useful for debugging than a struct dump, and staying just the
// ids keeps this stable if FPPEndpoint ever grows a field that is
// sensitive.
func fppEndpointIDs(endpoints []FPPEndpoint) []string {
	ids := make([]string, len(endpoints))
	for i, ep := range endpoints {
		ids[i] = ep.ID
	}
	return ids
}
