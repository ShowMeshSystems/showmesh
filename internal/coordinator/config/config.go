package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
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

	return slog.GroupValue(
		slog.String("http_addr", c.HTTPAddr),
		slog.String("mqtt_broker", c.MQTTBroker),
		slog.String("mqtt_client_id", c.MQTTClientID),
		slog.String("mqtt_username", c.MQTTUsername),
		slog.String("mqtt_password", password),
		slog.String("data_dir", c.DataDir),
		slog.String("log_level", c.LogLevel),
		slog.Any("fpp_endpoints", fppEndpointIDs(c.FPPEndpoints)),
		slog.String("api_token", apiToken),
		slog.Any("api_allowed_origins", c.APIAllowedOrigins),
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
