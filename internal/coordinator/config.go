package coordinator

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
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
	cfg := Config{
		HTTPAddr:     getEnvDefault(lookup, EnvHTTPAddr, DefaultHTTPAddr),
		MQTTBroker:   getEnvDefault(lookup, envMQTTBroker, defaultBroker),
		MQTTClientID: getEnvDefault(lookup, envMQTTClientID, defaultClientID),
		MQTTUsername: getEnvDefault(lookup, envMQTTUsername, ""),
		MQTTPassword: getEnvDefault(lookup, envMQTTPassword, ""),
		DataDir:      getEnvDefault(lookup, envDataDir, defaultDataDir),
		LogLevel:     getEnvDefault(lookup, envLogLevel, defaultLogLevel),
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
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

	return slog.GroupValue(
		slog.String("http_addr", c.HTTPAddr),
		slog.String("mqtt_broker", c.MQTTBroker),
		slog.String("mqtt_client_id", c.MQTTClientID),
		slog.String("mqtt_username", c.MQTTUsername),
		slog.String("mqtt_password", password),
		slog.String("data_dir", c.DataDir),
		slog.String("log_level", c.LogLevel),
	)
}
