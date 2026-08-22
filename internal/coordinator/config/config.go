package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
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

	// APIAllowedOrigins is SHOWMESH_API_ALLOWED_ORIGINS, comma-split into
	// individual origins. Empty (the default) means no CORS headers are
	// emitted at all for /api/v1/*, per contract section 6.8: this
	// coordinator does not reflect an arbitrary Origin and does not pair a
	// wildcard with credentials.
	APIAllowedOrigins []string

	// --- end Step 3 Task D ---

	// --- Step 6: ADR-024 identity, authorization, and audit ---
	//
	// SHOWMESH_API_TOKEN (ADR-021's shared secret) is retired, not
	// replaced by a field here: ADR-024 decision 2 requires this
	// coordinator to REFUSE TO START when that variable is still set,
	// rather than accept and either honor or silently ignore it — see
	// checkAPITokenRetired, called from LoadConfigFrom before any other
	// validation runs.

	// CloseReads is SHOWMESH_API_CLOSE_READS (a bool), ADR-024 decision
	// 2's replacement for what a non-empty SHOWMESH_API_TOKEN used to mean
	// under ADR-021: reads are open by default (false, the zero value,
	// matching every existing v1 read client's expectation) and closable
	// by an operator who wants /api/v1/* to require a credential even for
	// GET. Unlike the retired token, closing reads no longer implies a
	// shared secret authenticates them — it means every read now goes
	// through the same principal/session/token check every write already
	// requires unconditionally (decision 2: "there is no opt-out" for
	// writes). internal/coordinator/coordinator.go logs the ADR-021-carried-
	// forward startup warning naming the exposure whenever this is false.
	CloseReads bool

	// SecureCookie is SHOWMESH_API_SECURE_COOKIE (a bool), passed straight
	// through to api.Options.SecureCookie — see that field's doc comment
	// for why it defaults to false (ShowMesh terminates no TLS by
	// default; ADR-022 decision 5) and must be set true by any deployment
	// that puts TLS in front of the coordinator.
	SecureCookie bool

	// LoginConcurrency, LoginQueueWait, LoginPerSourceDelay, and
	// LoginMaxDelay are SHOWMESH_API_LOGIN_CONCURRENCY,
	// SHOWMESH_API_LOGIN_QUEUE_WAIT, SHOWMESH_API_LOGIN_PER_SOURCE_DELAY,
	// and SHOWMESH_API_LOGIN_MAX_DELAY — ADR-024 decision 8's login cost
	// bound (see internal/coordinator/api's loginlimiter.go). Each
	// defaults to its Go zero value when unset, which
	// api.Options.withDefaults then replaces with that package's own
	// labeled-hypothesis default (concurrency 4, queue wait 2s, per-
	// failure delay 250ms, max delay 5s) — the "sensible default" for
	// every one of these four lives in exactly one place (the api
	// package), not duplicated here as a second copy that could drift
	// from it.
	LoginConcurrency    int
	LoginQueueWait      time.Duration
	LoginPerSourceDelay time.Duration
	LoginMaxDelay       time.Duration

	// TrustClientAddr is SHOWMESH_API_TRUST_CLIENT_ADDR (a bool), passed
	// straight through to api.Options.TrustClientAddr — see that field's
	// doc comment. Off by default: behind the UI container's proxy,
	// RemoteAddr is the proxy's own address, and ADR-022 rule 2 forbids
	// trusting X-Forwarded-For for an audit-attribution decision.
	TrustClientAddr bool

	// --- end Step 6 ---

	// FPPEndpoints are the FPP instances the coordinator's FPP REST
	// collector polls, from SHOWMESH_FPP_ENDPOINTS. Empty (the default,
	// nil) means the collector does not run at all: per the Step 3
	// contract section 2, that is not an error and it is not silence
	// either — see internal/coordinator/collector/fpp's package doc
	// comment for how the API is expected to render "nothing configured"
	// as a stated fact (StateNotCollected) rather than an absent list that
	// reads as a healthy empty system.
	FPPEndpoints []FPPEndpoint

	// FPPEndpointsEnvSet records whether SHOWMESH_FPP_ENDPOINTS is
	// currently set in the PROCESS ENVIRONMENT this LoadConfigFrom call
	// read from — independent of, and never overwritten alongside,
	// FPPEndpoints itself (internal/coordinator's syncFPPEndpointsConfig
	// overwrites FPPEndpoints with the store-authoritative list once the
	// RES-008 D1 migration/disagreement rule has run; this field must
	// keep naming the RAW environment fact so that later resolution can
	// still ask "was the variable ever set" independent of what value the
	// coordinator ended up using). Step 7 seam A's PUT
	// /api/v1/config/fpp.endpoints handler is this field's consumer: it
	// refuses the write with 409 while this is true, because a write that
	// succeeds the coordinator cannot actually apply (the still-set
	// variable will disagree with it on the very next restart — see
	// configsync.go's errFPPEndpointsDisagree) is a worse failure than
	// refusing it up front, while the coordinator is still up and the
	// operator can still read why.
	//
	// "Set" is checked as non-empty, exactly like [checkAPITokenRetired]'s
	// identical convention for SHOWMESH_API_TOKEN: a blank-but-present
	// line in an operator's .env ("SHOWMESH_FPP_ENDPOINTS=") already means
	// "nothing configured" everywhere else this variable is read, so it
	// must not trip a refusal built for the non-empty case.
	FPPEndpointsEnvSet bool

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

	// --- Step 9 wave 2: SHOWMESH_INTEGRATION_BROKERS (integrationbrokers.go) ---

	// IntegrationBrokers is the deployment's declared set of external MQTT
	// brokers a show.action "mqtt" target may name, from
	// SHOWMESH_INTEGRATION_BROKERS. Empty (the default, nil) means no
	// integration broker is declared, and any mqtt show.action write is
	// rejected for naming an undeclared broker (showaction.go). This is
	// deliberately never seeded from MQTTBroker or FPPMQTTBrokerURL — see
	// integrationbrokers.go's own top doc comment for why the control-plane
	// broker must never be auto-registered under any identifier here.
	IntegrationBrokers []IntegrationBroker
	// --- Track D seam D-1: the Resolume Arena collector (internal/coordinator/collector/resolume) ---

	// ResolumeURL is SHOWMESH_RESOLUME_URL, the REST base URL of one
	// Resolume Arena instance, e.g. "http://127.0.0.1:9080" (no version
	// path — see resolume.NewClient's doc comment). Empty (the default)
	// means the Resolume collector does not exist at all for this
	// process: no goroutine, no warning storm, no failed-connection
	// signals for a feature the operator did not enable — exactly the
	// posture FPPMQTTBrokerURL above already established for a second,
	// independently-enabled collector source.
	//
	// RES-001 and TRACK-D-resolume.md both name port 8080 as Resolume's
	// REST port; the bench capture that actually reached a live instance
	// (docs/bench/resolume-control-surface.md) found it running on 9080
	// on the operator's own installation. That is deployment
	// configuration, not a protocol constant, so this value carries no
	// default host and no default port: the full URL is required.
	ResolumeURL string

	// ResolumeID is SHOWMESH_RESOLUME_ID: the identifier this Resolume
	// instance is reported under everywhere one is needed — the
	// observation resource id (pkg/observation.ResourceResolume), the
	// collector.Runner registration id used for logging and out-of-band
	// poll nudges (internal/coordinator/collector.Runner.Nudge), and the
	// "collectors" list id in GET /api/v1/snapshot. Defaults to
	// defaultResolumeID when unset, even when ResolumeURL is empty and
	// the collector never runs — a pure string default, the same
	// "defaults regardless of whether the feature is active" posture
	// FPPMQTTTopicPrefix already documents for itself.
	//
	// Validated with the same [mqttproto.ValidateNodeID] syntax every FPP
	// endpoint id already uses, and — only when ResolumeURL is set —
	// checked against every configured FPP endpoint id: this collector
	// and every FPP REST/MQTT collector share one
	// internal/coordinator/collector.Runner, keyed by this id, so an id a
	// configured FPP endpoint also uses would make an out-of-band poll
	// nudge meant for one device silently retarget the other. See
	// Validate and [ValidateResolumeIDAgainstFPPEndpoints].
	ResolumeID string

	// --- Track D seam D-2/C: the two ADR-033-shaped footprint knobs, plus
	// the composition-level parameter ladder's own gate ---

	// ResolumePollInterval is SHOWMESH_RESOLUME_POLL_INTERVAL: the initial
	// value of resolume.FootprintControls.PollInterval at startup. Zero
	// (the default when unset) leaves that type's own fallback
	// (resolume.DefaultPollInterval) in place. This is only ever the
	// STARTING value — ADR-033/TRACK-D-D2-SPEC.md §3.3 is explicit that
	// the real knob is a runtime-readable value
	// (resolume.FootprintControls), not a constant baked in once; a
	// future installation-wide show mode changes it after startup without
	// touching this field or requiring a restart. Ignored entirely when
	// ResolumeURL is empty (see [Config.ResolumeURL]).
	ResolumePollInterval time.Duration

	// ResolumeWebSocketDisabled is SHOWMESH_RESOLUME_WEBSOCKET_DISABLED: the
	// initial value of resolume.FootprintControls.WebSocketEnabled at
	// startup, INVERTED (false, the Go zero value, means enabled) so a
	// directly-constructed Config with every Resolume field left at zero
	// except ResolumeURL/ResolumeID — exactly what this seam's own tests
	// already build — keeps the WebSocket enabled without having to name
	// it. See ResolumePollInterval's own doc comment for why this is only
	// ever a starting value, not the knob itself.
	ResolumeWebSocketDisabled bool

	// --- Track E seam E5/E6: the asset manifest and sync service (ADR-028) ---

	// AssetDir is SHOWMESH_ASSET_DIR: the root directory
	// internal/coordinator/assetstore's volume backend stores asset bytes
	// under (ADR-028 decision 4: metadata in SQLite, bytes never in it).
	// Defaults to "<DataDir>/assets" when unset.
	AssetDir string

	// AssetMaxUploadBytes is SHOWMESH_ASSET_MAX_UPLOAD_BYTES: the maximum
	// size of a single asset upload. Defaults to
	// assetstore.DefaultMaxUploadBytes when unset.
	AssetMaxUploadBytes int64

	// AssetContentBaseURL is SHOWMESH_ASSET_CONTENT_BASE_URL: the base URL
	// an agent's asset.fetch operation downloads asset bytes from — an
	// asset.fetch command's own "url" param is
	// "<AssetContentBaseURL>/api/v1/assets/<id>/content". Empty (the
	// default) means internal/coordinator/assetsync's sync service does
	// NOT run at all: it logs that once at startup and the asset manifest
	// states it as the reason no node can be confirmed ready, rather than
	// the feature silently doing nothing — see that package's Service.Run.
	AssetContentBaseURL string

	// AssetSyncInterval is SHOWMESH_ASSET_SYNC_INTERVAL: how often the
	// asset sync service recomputes every declared node's gap against the
	// active show and dispatches asset.fetch commands, in addition to
	// running once on every asset upload (ADR-028 decision 7: never at
	// showtime). Defaults to 5 minutes when unset. Ignored entirely when
	// AssetContentBaseURL is empty.
	AssetSyncInterval time.Duration

	// AssetInventoryInterval is SHOWMESH_ASSET_INVENTORY_INTERVAL: this
	// coordinator's OWN COPY of the agent's inventory-report cadence
	// (SHOWMESH_ASSET_INVENTORY_INTERVAL on the agent side), used only to
	// derive the staleness window a node's inventory report must fall
	// within to be treated as fresh (assetsync.StalenessWindow: 3x this
	// value). This coordinator cannot see what an agent is actually
	// configured with — the two must be set to agree by whoever deploys
	// them, and a coordinator expecting a shorter interval than an agent
	// actually publishes on will see that node read unknown for part of
	// every cycle rather than ready. Defaults to 2 minutes, matching the
	// agent's own default.
	AssetInventoryInterval time.Duration

	// AssetSettingsEnvVarsSet records whether ANY of the four
	// SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/
	// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL
	// variables is currently set in the PROCESS ENVIRONMENT this
	// LoadConfigFrom call read from — mirroring [Config.FPPEndpointsEnvSet]'s
	// identical "raw environment fact, independent of what value this
	// coordinator ended up using" role for Track G seam G-4 (ADR-039). The
	// four variables migrate and are refused-while-set as one group (they
	// were promoted together, per this kind's own config/assetsettings.go
	// doc comment), so one bool covers all four rather than four separate
	// flags. "Set" is checked as non-empty, exactly like FPPEndpointsEnvSet's
	// identical convention — a blank-but-present line in an operator's .env
	// must not trip a refusal built for the non-empty case.
	AssetSettingsEnvVarsSet bool

	// ResolumeRecoverySettle is SHOWMESH_RESOLUME_RECOVERY_SETTLE (Track D
	// seam D-3a §5 term 2): how long the crash-recovery gate waits after
	// Resolume becomes reachable again before issuing anything beyond the
	// liveness probe that noticed the return. Default 8s. 0s is permitted
	// and means no settle delay — a test affordance for driving the gate
	// without a real wait, not a recommended production value. Valid range
	// [0s, 60s]; anything else fails [Config.Validate].
	ResolumeRecoverySettle time.Duration
}

// FPPEndpoint is one configured FPP instance for the coordinator's FPP REST
// collector to poll, parsed from one "id=url" pair in
// SHOWMESH_FPP_ENDPOINTS.
// Step 7 seam A (RES-008 D1): ID and URL carry JSON tags because this type
// is now ALSO config_revisions.payload_json's decoded shape for the
// fpp.endpoints configuration kind (see fppendpoints.go), not only an
// env-var-parsed value — a config revision's payload and
// SHOWMESH_FPP_ENDPOINTS's parsed shape are the same (id, url) pairs by
// design (that identity is what makes the env->store migration a straight
// copy, not a translation), so this struct is reused rather than declaring
// a second, payload-only type that could drift from it.
type FPPEndpoint struct {
	// ID identifies this instance on the wire and in logs. Same syntax as
	// an agent node ID (Step 3 contract section 7): validated with
	// [mqttproto.ValidateNodeID] rather than a second, possibly-drifting
	// copy of that regexp.
	ID string `json:"id"`

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
	URL string `json:"url"`
}

const (
	// EnvHTTPAddr is the environment variable naming the HTTP listen
	// address. It is exported so callers (e.g. the -healthcheck flag) can
	// honor it without going through full config validation.
	EnvHTTPAddr = "SHOWMESH_HTTP_ADDR"

	// DefaultHTTPAddr is used when EnvHTTPAddr is unset.
	DefaultHTTPAddr = ":8080"

	// EnvDataDir and DefaultDataDir are exported for the same reason
	// EnvHTTPAddr/DefaultHTTPAddr already are (see cmd/showmesh-coordinator's
	// -healthcheck flag): the coordinator's bootstrap/lockout-recovery
	// subcommands (ADR-024 decision 9) need the data directory's env var
	// and default WITHOUT going through full [LoadConfig] validation —
	// running config.Validate (and, since Step 6, checkAPITokenRetired)
	// against a recovery tool would make a leftover SHOWMESH_API_TOKEN
	// block the exact tool an operator needs to migrate off of it. See
	// cmd/showmesh-coordinator/main.go's subcommand dispatch.
	EnvDataDir     = "SHOWMESH_DATA_DIR"
	DefaultDataDir = "/var/lib/showmesh"

	envMQTTBroker   = "SHOWMESH_MQTT_BROKER"
	envMQTTClientID = "SHOWMESH_MQTT_CLIENT_ID"
	envMQTTUsername = "SHOWMESH_MQTT_USERNAME"
	envMQTTPassword = "SHOWMESH_MQTT_PASSWORD"
	envLogLevel     = "SHOWMESH_LOG_LEVEL"
	defaultBroker   = "tcp://localhost:1883"
	defaultClientID = "showmesh-coordinator"
	defaultLogLevel = "info"

	// envFPPEndpoints is SHOWMESH_FPP_ENDPOINTS, a comma-separated list of
	// "id=url" pairs, e.g.
	// "player-01=http://10.0.1.20,shed=http://10.0.1.21". Unset or empty
	// means no FPP collector runs; see [Config.FPPEndpoints].
	envFPPEndpoints = "SHOWMESH_FPP_ENDPOINTS"

	// envAPIToken is SHOWMESH_API_TOKEN, ADR-021's shared secret. ADR-024
	// decision 2 retires it: this constant now exists only so
	// checkAPITokenRetired can name it in the refusal-to-start error and
	// check the raw environment for its presence, never to populate a
	// Config field again — see that function's doc comment.
	envAPIToken = "SHOWMESH_API_TOKEN"

	// envAPIAllowedOrigins backs [Config.APIAllowedOrigins]. See that
	// field's doc comment.
	envAPIAllowedOrigins = "SHOWMESH_API_ALLOWED_ORIGINS"

	// envAPICloseReads, envAPISecureCookie, envAPILoginConcurrency,
	// envAPILoginQueueWait, envAPILoginPerSourceDelay,
	// envAPILoginMaxDelay, and envAPITrustClientAddr back
	// [Config.CloseReads]/[Config.SecureCookie]/[Config.LoginConcurrency]/
	// [Config.LoginQueueWait]/[Config.LoginPerSourceDelay]/
	// [Config.LoginMaxDelay]/[Config.TrustClientAddr]. See those fields'
	// doc comments.
	envAPICloseReads          = "SHOWMESH_API_CLOSE_READS"
	envAPISecureCookie        = "SHOWMESH_API_SECURE_COOKIE"
	envAPILoginConcurrency    = "SHOWMESH_API_LOGIN_CONCURRENCY"
	envAPILoginQueueWait      = "SHOWMESH_API_LOGIN_QUEUE_WAIT"
	envAPILoginPerSourceDelay = "SHOWMESH_API_LOGIN_PER_SOURCE_DELAY"
	envAPILoginMaxDelay       = "SHOWMESH_API_LOGIN_MAX_DELAY"
	envAPITrustClientAddr     = "SHOWMESH_API_TRUST_CLIENT_ADDR"

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

	// envResolumeURL and envResolumeID back the Track D seam D-1 fields
	// above. See [Config.ResolumeURL] and [Config.ResolumeID].
	envResolumeURL = "SHOWMESH_RESOLUME_URL"
	envResolumeID  = "SHOWMESH_RESOLUME_ID"

	// defaultResolumeID is used when SHOWMESH_RESOLUME_ID is unset. Plain
	// and short, matching every other default id this codebase mints
	// (e.g. defaultClientID above) — there is only ever one Resolume
	// instance this seam configures, so no numbering scheme is needed.
	defaultResolumeID = "resolume"

	// envResolumePollInterval and envResolumeWebSocketDisabled back the
	// Track D seam D-2/C fields above. See [Config.ResolumePollInterval]
	// and [Config.ResolumeWebSocketDisabled].
	//
	// SHOWMESH_RESOLUME_COMPOSITION_LADDER_ENABLED existed here once, back
	// when composition.bypassed/master/name were pursued through a
	// composition-level parameter ladder. That ladder was deleted (defect
	// 2, 2026-08-15) because no `GET /composition/{parameter}` path exists
	// anywhere in Arena's own OpenAPI specification — see
	// internal/coordinator/collector/resolume/client.go's own doc comment
	// — so there is nothing left for a flag to gate. The env var name is
	// deliberately NOT reused for anything else.
	envResolumePollInterval      = "SHOWMESH_RESOLUME_POLL_INTERVAL"
	envResolumeWebSocketDisabled = "SHOWMESH_RESOLUME_WEBSOCKET_DISABLED"

	// envAssetDir, envAssetMaxUploadBytes, envAssetContentBaseURL,
	// envAssetSyncInterval, and envAssetInventoryInterval back the Track E
	// seam E5/E6 fields above. See each Config field's own doc comment.
	envAssetDir               = "SHOWMESH_ASSET_DIR"
	envAssetMaxUploadBytes    = "SHOWMESH_ASSET_MAX_UPLOAD_BYTES"
	envAssetContentBaseURL    = "SHOWMESH_ASSET_CONTENT_BASE_URL"
	envAssetSyncInterval      = "SHOWMESH_ASSET_SYNC_INTERVAL"
	envAssetInventoryInterval = "SHOWMESH_ASSET_INVENTORY_INTERVAL"

	defaultAssetSyncInterval      = 5 * time.Minute
	defaultAssetInventoryInterval = 2 * time.Minute
	// envResolumeRecoverySettle backs [Config.ResolumeRecoverySettle]
	// (Track D seam D-3a). Deliberately a tuning knob (env var), not
	// revisioned show-state config: see that field's own doc comment.
	envResolumeRecoverySettle = "SHOWMESH_RESOLUME_RECOVERY_SETTLE"

	// defaultResolumeRecoverySettle is [Config.ResolumeRecoverySettle]'s
	// default. SHOWMESH GUESS, NOT MEASURED — chosen to sit comfortably
	// past the measured 1.2s wrong-composition window with room for a
	// slower host; not timed against a real Arena restart.
	defaultResolumeRecoverySettle = 8 * time.Second

	// maxResolumeRecoverySettle bounds [Config.ResolumeRecoverySettle].
	maxResolumeRecoverySettle = 60 * time.Second
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
	// Checked FIRST, before any other parsing: ADR-024 decision 2 requires
	// this coordinator to refuse to start deterministically and visibly
	// the moment a leftover SHOWMESH_API_TOKEN is found, not after
	// spending effort parsing everything else. See checkAPITokenRetired's
	// doc comment for why this is the harshest of three possible
	// behaviors and why it is chosen anyway.
	if err := checkAPITokenRetired(lookup); err != nil {
		return Config{}, err
	}

	rawFPPEndpoints := getEnvDefault(lookup, envFPPEndpoints, "")
	fppEndpoints, err := parseFPPEndpoints(rawFPPEndpoints)
	if err != nil {
		return Config{}, err
	}

	fppMQTTHosts, err := parseFPPMQTTHosts(getEnvDefault(lookup, envFPPMQTTHosts, ""))
	if err != nil {
		return Config{}, err
	}

	integrationBrokers, err := parseIntegrationBrokers(getEnvDefault(lookup, envIntegrationBrokers, ""), lookup)
	if err != nil {
		return Config{}, err
	}

	closeReads, err := parseBoolEnv(lookup, envAPICloseReads, false)
	if err != nil {
		return Config{}, err
	}
	secureCookie, err := parseBoolEnv(lookup, envAPISecureCookie, false)
	if err != nil {
		return Config{}, err
	}
	trustClientAddr, err := parseBoolEnv(lookup, envAPITrustClientAddr, false)
	if err != nil {
		return Config{}, err
	}
	loginConcurrency, err := parseIntEnv(lookup, envAPILoginConcurrency, 0)
	if err != nil {
		return Config{}, err
	}
	loginQueueWait, err := parseDurationEnv(lookup, envAPILoginQueueWait, 0)
	if err != nil {
		return Config{}, err
	}
	loginPerSourceDelay, err := parseDurationEnv(lookup, envAPILoginPerSourceDelay, 0)
	if err != nil {
		return Config{}, err
	}
	loginMaxDelay, err := parseDurationEnv(lookup, envAPILoginMaxDelay, 0)
	if err != nil {
		return Config{}, err
	}

	resolumePollInterval, err := parseDurationEnv(lookup, envResolumePollInterval, 0)
	if err != nil {
		return Config{}, err
	}
	resolumeWebSocketDisabled, err := parseBoolEnv(lookup, envResolumeWebSocketDisabled, false)
	if err != nil {
		return Config{}, err
	}
	resolumeRecoverySettle, err := parseDurationEnv(lookup, envResolumeRecoverySettle, defaultResolumeRecoverySettle)
	if err != nil {
		return Config{}, err
	}

	dataDir := getEnvDefault(lookup, EnvDataDir, DefaultDataDir)

	assetMaxUploadBytes, err := parseInt64Env(lookup, envAssetMaxUploadBytes, assetstore.DefaultMaxUploadBytes)
	if err != nil {
		return Config{}, err
	}
	assetSyncInterval, err := parseDurationEnv(lookup, envAssetSyncInterval, defaultAssetSyncInterval)
	if err != nil {
		return Config{}, err
	}
	assetInventoryInterval, err := parseDurationEnv(lookup, envAssetInventoryInterval, defaultAssetInventoryInterval)
	if err != nil {
		return Config{}, err
	}

	// assetSettingsEnvVarsSet backs [Config.AssetSettingsEnvVarsSet]: true
	// when ANY of the four variables is present and non-empty, regardless
	// of whether its parsed value above equals its default — a variable
	// explicitly set to the default is still "set" for this purpose,
	// exactly like [Config.FPPEndpointsEnvSet]'s identical convention.
	rawAssetContentBaseURL, _ := lookup(envAssetContentBaseURL)
	rawAssetMaxUploadBytes, _ := lookup(envAssetMaxUploadBytes)
	rawAssetSyncInterval, _ := lookup(envAssetSyncInterval)
	rawAssetInventoryInterval, _ := lookup(envAssetInventoryInterval)
	assetSettingsEnvVarsSet := rawAssetContentBaseURL != "" || rawAssetMaxUploadBytes != "" ||
		rawAssetSyncInterval != "" || rawAssetInventoryInterval != ""

	cfg := Config{
		HTTPAddr:     getEnvDefault(lookup, EnvHTTPAddr, DefaultHTTPAddr),
		MQTTBroker:   getEnvDefault(lookup, envMQTTBroker, defaultBroker),
		MQTTClientID: getEnvDefault(lookup, envMQTTClientID, defaultClientID),
		MQTTUsername: getEnvDefault(lookup, envMQTTUsername, ""),
		MQTTPassword: getEnvDefault(lookup, envMQTTPassword, ""),
		DataDir:      dataDir,
		LogLevel:     getEnvDefault(lookup, envLogLevel, defaultLogLevel),
		FPPEndpoints: fppEndpoints,
		// Non-empty, mirroring checkAPITokenRetired's "set" convention —
		// see FPPEndpointsEnvSet's own doc comment for why blank-but-present
		// must not count.
		FPPEndpointsEnvSet: rawFPPEndpoints != "",

		APIAllowedOrigins: parseAPIAllowedOrigins(getEnvDefault(lookup, envAPIAllowedOrigins, "")),

		CloseReads:          closeReads,
		SecureCookie:        secureCookie,
		TrustClientAddr:     trustClientAddr,
		LoginConcurrency:    loginConcurrency,
		LoginQueueWait:      loginQueueWait,
		LoginPerSourceDelay: loginPerSourceDelay,
		LoginMaxDelay:       loginMaxDelay,

		FPPMQTTBrokerURL:   getEnvDefault(lookup, envFPPMQTTBrokerURL, ""),
		FPPMQTTUsername:    getEnvDefault(lookup, envFPPMQTTUsername, ""),
		FPPMQTTPassword:    getEnvDefault(lookup, envFPPMQTTPassword, ""),
		FPPMQTTTopicPrefix: getEnvDefault(lookup, envFPPMQTTTopicPrefix, defaultFPPMQTTTopicPrefix),
		FPPMQTTHosts:       fppMQTTHosts,

		IntegrationBrokers: integrationBrokers,
		ResolumeURL:        getEnvDefault(lookup, envResolumeURL, ""),
		ResolumeID:         getEnvDefault(lookup, envResolumeID, defaultResolumeID),

		ResolumePollInterval:      resolumePollInterval,
		ResolumeWebSocketDisabled: resolumeWebSocketDisabled,

		AssetDir:                getEnvDefault(lookup, envAssetDir, filepath.Join(dataDir, "assets")),
		AssetMaxUploadBytes:     assetMaxUploadBytes,
		AssetContentBaseURL:     getEnvDefault(lookup, envAssetContentBaseURL, ""),
		AssetSyncInterval:       assetSyncInterval,
		AssetInventoryInterval:  assetInventoryInterval,
		AssetSettingsEnvVarsSet: assetSettingsEnvVarsSet,
		ResolumeRecoverySettle:  resolumeRecoverySettle,
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// checkAPITokenRetired implements ADR-024 decision 2's refusal-to-start
// rule: "If the [SHOWMESH_API_TOKEN] variable is still set when a
// coordinator carrying this record starts, the coordinator refuses to
// start and names the migration in the error." This is deliberately the
// harshest of three possible behaviors — decision 2's own reasoning,
// restated here because this function is where it is enforced: ignoring
// the variable would silently reopen a read API an operator deliberately
// closed (a security control failing open on a container tag change);
// honoring it would keep a retired shared secret alive indefinitely and
// contradict the retirement outright. A refusal is deterministic, visible
// in the first seconds of a failed startup, and fixed by editing one line
// of an operator's .env.
//
// "Set" is checked as non-empty, not merely present in the environment
// (lookup's ok return alone): under the retired ADR-021 posture,
// SHOWMESH_API_TOKEN="" already meant "no authentication" — functionally
// identical to unset — so a blank-but-present line in an operator's .env
// file (a common pattern, and not evidence anyone ever closed their read
// API with it) must not trip a refusal the decision's own reasoning does
// not apply to. A genuinely non-empty value is the only case that ever
// had the old meaning this decision requires migrating off of.
func checkAPITokenRetired(lookup func(string) (string, bool)) error {
	raw, ok := lookup(envAPIToken)
	if !ok || raw == "" {
		return nil
	}
	return fmt.Errorf(
		"%s is set, but ADR-024 retired it: this coordinator no longer accepts a shared bearer-token secret for the read API. "+
			"Remove %s from your environment. If you want the read API to require a credential the way a non-empty %s used to, "+
			"set %s=true instead, then create your first administrator via the one-time bootstrap code "+
			"(POST /api/v1/bootstrap, or run `showmesh-coordinator bootstrap` against this coordinator's data volume) — "+
			"see ADR-024 decision 2 for the full migration",
		envAPIToken, envAPIToken, envAPIToken, envAPICloseReads)
}

// parseBoolEnv reads key via lookup and parses it with strconv.ParseBool,
// returning def when key is unset or empty. A present-but-invalid value
// (e.g. "yes" instead of "true") is a startup error naming key, matching
// this file's existing "a malformed value is a startup error, not a
// silently-ignored default" posture for every other typed field.
func parseBoolEnv(lookup func(string) (string, bool), key string, def bool) (bool, error) {
	raw := getEnvDefault(lookup, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not a valid boolean (true/false/1/0/...): %w", key, raw, err)
	}
	return v, nil
}

// parseIntEnv mirrors [parseBoolEnv] for an integer-valued variable.
func parseIntEnv(lookup func(string) (string, bool), key string, def int) (int, error) {
	raw := getEnvDefault(lookup, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid integer: %w", key, raw, err)
	}
	return v, nil
}

// parseInt64Env mirrors [parseIntEnv] for a variable that must fit an
// int64 (SHOWMESH_ASSET_MAX_UPLOAD_BYTES's byte count is the first such
// case in this file; parseIntEnv's plain int would truncate on a 32-bit
// build).
func parseInt64Env(lookup func(string) (string, bool), key string, def int64) (int64, error) {
	raw := getEnvDefault(lookup, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid integer: %w", key, raw, err)
	}
	return v, nil
}

// parseDurationEnv mirrors [parseBoolEnv] for a time.Duration-valued
// variable, in Go duration syntax (e.g. "2s", "250ms").
func parseDurationEnv(lookup func(string) (string, bool), key string, def time.Duration) (time.Duration, error) {
	raw := getEnvDefault(lookup, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a valid duration (e.g. \"2s\", \"250ms\"): %w", key, raw, err)
	}
	return v, nil
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

	if err := validateResolumeConfig(c); err != nil {
		return err
	}
	if c.ResolumePollInterval < 0 {
		return fmt.Errorf("%s must not be negative, got %s", envResolumePollInterval, c.ResolumePollInterval)
	}
	if c.ResolumeRecoverySettle < 0 || c.ResolumeRecoverySettle > maxResolumeRecoverySettle {
		return fmt.Errorf("%s must be between 0s and %s, got %s", envResolumeRecoverySettle, maxResolumeRecoverySettle, c.ResolumeRecoverySettle)
	}

	if err := validateAssetConfig(c); err != nil {
		return err
	}

	return nil
}

// ValidateFPPEndpoints is [validateFPPEndpoints] exported for Step 7 seam
// A's config:write surface (internal/coordinator/api's PUT
// /api/v1/config/fpp.endpoints and the startup env->store migration in
// internal/coordinator's configsync.go): ADR-009 requires invalid
// configuration be rejected before activation, and the BUILD-PLAN Step 7
// spec requires reusing this package's existing validation rather than
// writing a second one that could drift from what SHOWMESH_FPP_ENDPOINTS
// itself enforces at config-load time. Every rule below — id syntax, URL
// scheme/host, no userinfo, no duplicate ids — applies identically whether
// the list came from the environment or from an API request body.
func ValidateFPPEndpoints(endpoints []FPPEndpoint) error {
	return validateFPPEndpoints(endpoints)
}

// reservedCollectorIDs are ids the coordinator registers on the shared
// collector.Runner for collectors that are not FPP endpoints.
//
// An endpoint may not claim one, and the failure without this check is
// silent rather than loud: collector.Runner.Add returns early on an id it
// already holds, so the endpoint is accepted, logged as started, and never
// polled, while removing it later calls Runner.Remove on that id and stops
// the collector that really owns it. Startup ordering does not save this
// either, because fpp.endpoints is a configuration surface that changes
// while the process runs.
//
// Held as literals rather than imported from the collector packages so
// this rule runs at config-load time and on the API write path without
// this package depending on either; TestReservedCollectorIDsMatchTheReal
// Registrations (internal/coordinator) is what keeps the two in step.
//
// SHOWMESH_RESOLUME_ID is deliberately NOT here: it is operator-settable
// and is cross-checked against the endpoint list by
// [ValidateResolumeIDAgainstFPPEndpoints] only when a Resolume instance is
// actually configured, so an endpoint named "resolume" stays legal on a
// coordinator that has no Resolume.
var reservedCollectorIDs = map[string]bool{
	"fpp-mqtt": true, // internal/coordinator/collector/fppmqtt's Collector.ID
}

// validateFPPEndpoints enforces the semantic rules a structural
// parseFPPEndpoints pair must additionally satisfy: a valid node-ID-syntax
// id (contract section 7), a URL with an http/https scheme and a host, no
// embedded userinfo, and no id repeated across the list. Per the Step 3
// Task C spec, a malformed entry is a startup error naming the offending
// value, not a silently skipped endpoint — every error here names the
// specific id or URL that failed.
// Every message below names the "fpp.endpoints" configuration kind rather
// than envFPPEndpoints (ADR-039 decision 1): this function validates the
// same shape whether it was reached from LoadConfig's env parse or from
// the store-backed config:write surface ([ValidateFPPEndpoints]), and the
// remedy for a shape defect is the same in both cases regardless of which
// path is still authoritative for this deployment.
func validateFPPEndpoints(endpoints []FPPEndpoint) error {
	seen := make(map[string]bool, len(endpoints))

	for _, ep := range endpoints {
		if err := mqttproto.ValidateNodeID(ep.ID); err != nil {
			return fmt.Errorf("fpp.endpoints: instance id %q: %w", ep.ID, err)
		}
		if reservedCollectorIDs[ep.ID] {
			return fmt.Errorf("fpp.endpoints: instance id %q is reserved for one of this coordinator's own collectors and cannot name an FPP endpoint; rename the endpoint",
				ep.ID)
		}
		if seen[ep.ID] {
			return fmt.Errorf("fpp.endpoints: duplicate instance id %q", ep.ID)
		}
		seen[ep.ID] = true

		u, err := url.Parse(ep.URL)
		if err != nil {
			return fmt.Errorf("fpp.endpoints: instance %q: url %q is not valid: %w", ep.ID, ep.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("fpp.endpoints: instance %q: url %q must use http or https", ep.ID, ep.URL)
		}
		if u.Host == "" {
			return fmt.Errorf("fpp.endpoints: instance %q: url %q must include a host", ep.ID, ep.URL)
		}
		if u.User != nil {
			// See FPPEndpoint.URL's doc comment: rejected here, at the
			// only entry point, rather than relying on every downstream
			// consumer (log lines, API rendering, error reasons) to
			// remember to strip it.
			return fmt.Errorf("fpp.endpoints: instance %q: url must not include userinfo/credentials", ep.ID)
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

	// Step 7 seam A (RES-008 D1): once SHOWMESH_FPP_ENDPOINTS is empty, the
	// true endpoint list may be store-authoritative rather than absent —
	// an operator who followed the migration warning and removed the
	// variable from .env still has FPPMQTTHosts referencing real instance
	// ids, just ones this package cannot see at config-load time (it has
	// no store access). Rejecting here would make a fully-migrated,
	// correctly-configured deployment fail to start. The cross-check is
	// NOT skipped, only deferred: internal/coordinator's startup sequence
	// re-runs [ValidateFPPMQTTHostIDs] against the resolved,
	// authoritative endpoint list (store or env, whichever the disagreement
	// rule settles on) before constructing any collector — see that
	// package's configsync.go — so "must not silently stop running" (this
	// step's spec) still holds; it just cannot run from inside this
	// function once the endpoint list has genuinely left the environment.
	//
	// When FPPEndpoints is non-empty (env still carries the list, migrated
	// or not), the check below still runs here exactly as before — this
	// only widens the empty-FPPEndpoints case, it does not weaken the
	// common one.
	if len(c.FPPEndpoints) == 0 {
		return nil
	}

	return ValidateFPPMQTTHostIDs(c.FPPMQTTHosts, c.FPPEndpoints)
}

// ValidateFPPMQTTHostIDs checks that every id in hosts also appears in
// endpoints — contract section 4.4's cross-check ("every id in
// SHOWMESH_FPP_MQTT_HOSTS must also appear in SHOWMESH_FPP_ENDPOINTS;
// reject the configuration ... with a message naming the unmatched ID"),
// factored out so [validateFPPMQTTConfig] (against the env-parsed list at
// config-load time) and internal/coordinator's post-startup re-validation
// against the store-authoritative list (Step 7 seam A, RES-008 D1 — see
// that package's configsync.go) run the IDENTICAL rule rather than two
// copies that could silently drift apart from each other.
func ValidateFPPMQTTHostIDs(hosts map[string]string, endpoints []FPPEndpoint) error {
	known := make(map[string]bool, len(endpoints))
	for _, ep := range endpoints {
		known[ep.ID] = true
	}
	for id := range hosts {
		if !known[id] {
			return fmt.Errorf("instance id %q is not a configured FPP endpoint; add it with `showmeshctl fpp-endpoints set` (fpp.endpoints), or %s if this coordinator has not migrated yet", id, envFPPEndpoints)
		}
	}
	return nil
}

// validateResolumeConfig enforces Track D seam D-1's startup rules for
// SHOWMESH_RESOLUME_URL/SHOWMESH_RESOLUME_ID. Every check here runs only
// when ResolumeURL is non-empty: an empty URL means the collector never
// gets constructed (see [Config.ResolumeURL]), so an id syntax error or an
// id collision the operator would never actually hit is not worth
// rejecting startup over — the identical reasoning
// [validateFPPMQTTConfig] already applies to its own broker-URL-gated
// checks.
//
//   - The URL must be http or https with a host and no userinfo — the same
//     three checks [validateFPPEndpoints] applies to an FPP endpoint URL,
//     duplicated here rather than shared because the two validate
//     unrelated fields of unrelated structs; resolume.NewClient re-checks
//     the identical shape at construction time for the "safe to construct
//     directly, without relying on config validation having already run"
//     reason its own doc comment states, so a failure there would mean
//     these two checks have drifted apart, not a condition this function
//     needs to anticipate.
//   - The id must satisfy [mqttproto.ValidateNodeID], the same syntax
//     every FPP endpoint id already uses.
//   - The id must not collide with any configured FPP endpoint id — see
//     [ValidateResolumeIDAgainstFPPEndpoints].
func validateResolumeConfig(c Config) error {
	if c.ResolumeURL == "" {
		return nil
	}

	u, err := url.Parse(c.ResolumeURL)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w", envResolumeURL, c.ResolumeURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must use http or https", envResolumeURL, c.ResolumeURL)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q must include a host", envResolumeURL, c.ResolumeURL)
	}
	if u.User != nil {
		// See FPPEndpoint.URL's identical rule: rejected here, at the only
		// entry point, rather than relying on every downstream consumer
		// (log lines, error reasons, the API) to remember to scrub it.
		return fmt.Errorf("%s: url must not include userinfo/credentials", envResolumeURL)
	}

	if err := mqttproto.ValidateNodeID(c.ResolumeID); err != nil {
		return fmt.Errorf("%s: %q: %w", envResolumeID, c.ResolumeID, err)
	}

	return ValidateResolumeIDAgainstFPPEndpoints(c.ResolumeID, c.FPPEndpoints)
}

// ValidateResolumeIDAgainstFPPEndpoints checks that resolumeID does not
// collide with any id in endpoints, factored out — mirroring
// [ValidateFPPMQTTHostIDs]'s identical split — so [validateResolumeConfig]
// (against the env-parsed FPP endpoint list at config-load time) and
// internal/coordinator's post-migration re-validation (against the
// store-authoritative list, once SHOWMESH_FPP_ENDPOINTS may no longer be
// the source of truth — see internal/coordinator/configsync.go) run the
// IDENTICAL rule rather than two copies that could silently drift apart.
//
// The collision this guards against is concrete, not theoretical: the
// Resolume collector and every configured FPP collector are registered on
// one shared internal/coordinator/collector.Runner, whose Add and Nudge
// both key their internal maps by this exact id string
// (internal/coordinator/collector/collector.go). A resolumeID equal to an
// FPP endpoint id would make the second Add call silently overwrite the
// first's nudge channel, so an out-of-band poll nudge meant for one device
// would silently retarget the other — a startup error naming both ids is
// what stops that from ever being reachable, rather than a silent rename.
func ValidateResolumeIDAgainstFPPEndpoints(resolumeID string, endpoints []FPPEndpoint) error {
	for _, ep := range endpoints {
		if ep.ID == resolumeID {
			return fmt.Errorf("resolume id %q collides with a configured FPP endpoint id; rename one — "+
				"the Resolume instance with `showmeshctl resolume-instances set` (resolume.instances), "+
				"or the FPP endpoint with `showmeshctl fpp-endpoints set` (fpp.endpoints), or %s/%s if this coordinator has not migrated yet",
				resolumeID, envResolumeID, envFPPEndpoints)
		}
	}
	return nil
}

// validateAssetConfig checks the Track E seam E5/E6 asset fields.
// AssetContentBaseURL's rule mirrors validateResolumeConfig's exactly
// (http/https, a host, no userinfo) except that empty is a valid,
// deliberate "the sync service does not run" state rather than something
// this function gates the rest of its checks on — AssetDir and
// AssetMaxUploadBytes matter to the (separately built) upload/store
// surface regardless of whether the sync service is enabled.
func validateAssetConfig(c Config) error {
	if c.AssetDir == "" {
		return fmt.Errorf("%s must not be empty", envAssetDir)
	}
	if c.AssetMaxUploadBytes <= 0 {
		return fmt.Errorf("%s must be positive, got %d", envAssetMaxUploadBytes, c.AssetMaxUploadBytes)
	}
	if c.AssetSyncInterval <= 0 {
		return fmt.Errorf("%s must be positive, got %s", envAssetSyncInterval, c.AssetSyncInterval)
	}
	if c.AssetInventoryInterval <= 0 {
		return fmt.Errorf("%s must be positive, got %s", envAssetInventoryInterval, c.AssetInventoryInterval)
	}

	if c.AssetContentBaseURL == "" {
		return nil
	}
	u, err := url.Parse(c.AssetContentBaseURL)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid URL: %w", envAssetContentBaseURL, c.AssetContentBaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q must use http or https", envAssetContentBaseURL, c.AssetContentBaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q must include a host", envAssetContentBaseURL, c.AssetContentBaseURL)
	}
	if u.User != nil {
		return fmt.Errorf("%s: url must not include userinfo/credentials", envAssetContentBaseURL)
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

	// fppMQTTPassword is redacted the same way and for the same reason as
	// mqtt_password above: this is SHOWMESH_FPP_MQTT_PASSWORD,
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
		slog.Any("api_allowed_origins", c.APIAllowedOrigins),
		// None of the seven ADR-024 fields below is a credential — see
		// each Config field's doc comment — so, unlike password/token
		// fields, they are logged directly rather than redacted.
		slog.Bool("api_close_reads", c.CloseReads),
		slog.Bool("api_secure_cookie", c.SecureCookie),
		slog.Bool("api_trust_client_addr", c.TrustClientAddr),
		slog.Int("api_login_concurrency", c.LoginConcurrency),
		slog.Duration("api_login_queue_wait", c.LoginQueueWait),
		slog.Duration("api_login_per_source_delay", c.LoginPerSourceDelay),
		slog.Duration("api_login_max_delay", c.LoginMaxDelay),
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
		// Step 9 wave 2: ids only, matching fpp_endpoints above — the
		// per-broker Username/Password are never logged, in the clear or
		// otherwise (see IntegrationBroker.Password's own doc comment).
		slog.Any("integration_brokers", integrationBrokerIDs(c.IntegrationBrokers)),
		// ResolumeURL carries no credential (Validate rejects userinfo, so
		// there is structurally nothing to redact — unlike MQTTBroker/
		// FPPMQTTBrokerURL, whose protocols do allow it), so it is logged
		// directly rather than through redactURLUserinfo.
		slog.String("resolume_url", c.ResolumeURL),
		slog.String("resolume_id", c.ResolumeID),
		slog.Duration("resolume_poll_interval", c.ResolumePollInterval),
		slog.Bool("resolume_websocket_disabled", c.ResolumeWebSocketDisabled),
		// Track E seam E5/E6: none of these four is a credential, so all
		// are logged directly.
		slog.String("asset_dir", c.AssetDir),
		slog.Int64("asset_max_upload_bytes", c.AssetMaxUploadBytes),
		// Not a credential (validateAssetConfig forbids userinfo, same as
		// resolume_url above), so logged directly. Empty means the sync
		// service is disabled — see AssetContentBaseURL's own doc comment.
		slog.String("asset_content_base_url", c.AssetContentBaseURL),
		slog.Duration("asset_sync_interval", c.AssetSyncInterval),
		slog.Duration("asset_inventory_interval", c.AssetInventoryInterval),
		slog.Duration("resolume_recovery_settle", c.ResolumeRecoverySettle),
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
