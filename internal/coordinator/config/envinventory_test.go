package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// startTimeEnvVars is ADR-039 decision 2's allow-list of genuinely
// start-time settings: values the coordinator must know before it can
// read its own store, so they cannot themselves be store-backed. Each
// reason states WHY, not merely restates the name.
var startTimeEnvVars = map[string]string{
	"SHOWMESH_HTTP_ADDR": "the HTTP listener's bind address; the server is constructed once at startup",
	"SHOWMESH_DATA_DIR": "where the coordinator's own SQLite store lives, so it cannot come from the store " +
		"(ADR-039 decision 2's own worked example); also the value the bootstrap/lockout-recovery subcommands " +
		"read directly, bypassing full config validation, per ADR-024 decision 9",
	"SHOWMESH_MQTT_BROKER": "the control-plane broker URL; the agent transport is how the coordinator reaches " +
		"the fleet, and a value fetched through that fleet would be circular",
	"SHOWMESH_MQTT_CLIENT_ID": "needed to open the same startup broker connection as SHOWMESH_MQTT_BROKER",
	"SHOWMESH_MQTT_USERNAME":  "the control-plane broker credential (ADR-039 decision 2)",
	"SHOWMESH_MQTT_PASSWORD":  "the control-plane broker credential (ADR-039 decision 2)",
	"SHOWMESH_LOG_LEVEL":      "log level; logging is initialized before any store access",
	"SHOWMESH_ASSET_DIR": "where the asset store's bytes live on this host, so it cannot come from the store " +
		"it locates (ADR-039 decision 2's second worked example, alongside SHOWMESH_DATA_DIR)",
}

// deploymentPostureEnvVars is a second, narrower group: settings wired
// into the HTTP server's construction at startup (a rate limiter, a
// cookie flag, a CORS allow-list, a proxy trust boundary), each with a
// safe default. Unlike a subsystem capability (e.g. "where is Resolume"),
// none of these is required for the coordinator to function, and each is
// a deployment-topology decision made once when the container is stood
// up — analogous to the bind address, not an operator-facing capability
// ADR-039 requires to be store-backed.
var deploymentPostureEnvVars = map[string]string{
	"SHOWMESH_API_ALLOWED_ORIGINS": "the CORS allow-list is built into the HTTP handler at startup; empty " +
		"(the default) emits no CORS headers at all",
	"SHOWMESH_API_CLOSE_READS": "whether reads require a credential is wired into handler construction at " +
		"startup, defaults to open reads",
	"SHOWMESH_API_SECURE_COOKIE": "the session cookie's Secure flag is set once when the server is built, " +
		"defaults false because ShowMesh terminates no TLS (ADR-022 decision 5)",
	"SHOWMESH_API_LOGIN_CONCURRENCY":      "ADR-024 decision 8's login cost bound; the rate limiter is constructed once at startup",
	"SHOWMESH_API_LOGIN_QUEUE_WAIT":       "ADR-024 decision 8's login cost bound; the same startup-constructed rate limiter",
	"SHOWMESH_API_LOGIN_PER_SOURCE_DELAY": "ADR-024 decision 8's login cost bound; the same startup-constructed rate limiter",
	"SHOWMESH_API_LOGIN_MAX_DELAY":        "ADR-024 decision 8's login cost bound; the same startup-constructed rate limiter",
	"SHOWMESH_API_TRUST_CLIENT_ADDR": "whether RemoteAddr is trusted for audit attribution is a proxy-trust " +
		"boundary installed into request-parsing middleware at startup, defaults false per ADR-022 rule 2",
}

// tuningKnobEnvVars is a third group: values explicitly documented in
// this package as deliberate tuning knobs an operator never needs to
// touch for correct function (a sensible default exists for every one),
// as distinct from a capability an operator must set for a subsystem to
// work at all. See each constant's own doc comment in config.go for the
// "tuning knob, not revisioned config" language this group codifies.
var tuningKnobEnvVars = map[string]string{
	"SHOWMESH_RESOLUME_POLL_INTERVAL": "advanced/debug tuning for the Resolume collector's poll cadence; " +
		"reaching Resolume at all is resolume.instances (store-backed, Track G seam G-2), not this",
	"SHOWMESH_RESOLUME_WEBSOCKET_DISABLED": "a debug/ops toggle to disable the WebSocket change-signal path; " +
		"a safe default (enabled) exists. ADR-033's show.mode now drives the same switch (open in program, " +
		"closed in show), and that is NOT the two-authorities-for-one-setting hazard ADR-039 decision 4 " +
		"forbids: this variable is not retired by show.mode, it stays a startup-only debug kill switch, and " +
		"it only ever forces the footprint SMALLER. When it is set, no mode reopens the WebSocket " +
		"(resolumeManager.SetWebSocketEnabled); when it is unset, the mode alone decides. An operator's " +
		"write can never silently lose to it, because setting the mode is not a way to ask for this " +
		"variable's behaviour",
	"SHOWMESH_RESOLUME_RECOVERY_SETTLE": "explicitly documented in config.go as \"a tuning knob (env var), not " +
		"revisioned show-state config\" (Track D seam D-3a's own doc comment on defaultResolumeRecoverySettle)",
}

// knownGapEnvVars is a fourth group, and the one that must not be
// confused with the three above: capabilities that read like they belong
// on startTimeEnvVars but do not pass ADR-039 decision 2's own test (an
// operator needs to change this without a restart). Track G's audit
// (TRACK-G-surface-parity.md) scoped itself to
// "internal/coordinator/config/config.go" by filename and so never saw
// these, which live in a sibling file of the same package. Each entry
// here is a known, open gap — reported rather than silently promoted to
// startTimeEnvVars with a reason that would not survive scrutiny.
var knownGapEnvVars = map[string]string{
	"SHOWMESH_INTEGRATION_BROKERS": "genuinely operator-facing (Step 9 wave 2: declares the external MQTT " +
		"brokers a show.action's mqtt target may name) and fails decision 2's own test — an operator needs to " +
		"add a broker without a restart — but it lives in integrationbrokers.go, outside Track G's audited " +
		"config.go scope, and was never converted to a store-backed kind. Recorded as a gap, not endorsed as " +
		"start-time; see docs/private/DECISION-QUEUE.md.",
	"SHOWMESH_INTEGRATION_BROKER_": "not a variable itself but the literal prefix integrationbrokers.go " +
		"concatenates into SHOWMESH_INTEGRATION_BROKER_<ID>_USERNAME/_PASSWORD, the per-broker credentials of " +
		"the SHOWMESH_INTEGRATION_BROKERS family recorded above — the same gap, one entry per literal the " +
		"sweep can see",
}

// retiredEnvVars is ADR-039 decision 3's group: a variable a store-backed
// kind has replaced. Each name here is read exactly twice in the whole
// coordinator — once by its own env->store migration at startup, and
// once by the still-set check that refuses a write while the variable
// remains set (decision 4) — and by nothing else. That boundary was
// checked by hand for this fold (grep across the repository, excluding
// this package's own config.go/coordinator.go/configsync.go wiring); it
// is not re-checked by this test, which only enforces that every retired
// name is accounted for somewhere, not that its usage stays confined.
var retiredEnvVars = map[string]string{
	"SHOWMESH_FPP_ENDPOINTS": "Step 7's own migration to the fpp.endpoints store-backed kind — predates " +
		"ADR-039 but is the identical pattern decision 3 now names",
	"SHOWMESH_RESOLUME_URL":             "Track G seam G-2's migration to the resolume.instances store-backed kind",
	"SHOWMESH_RESOLUME_ID":              "Track G seam G-2's migration to the resolume.instances store-backed kind",
	"SHOWMESH_FPP_MQTT_BROKER_URL":      "Track G seam G-3's migration to the fpp.mqtt store-backed kind",
	"SHOWMESH_FPP_MQTT_USERNAME":        "Track G seam G-3's migration to the fpp.mqtt store-backed kind",
	"SHOWMESH_FPP_MQTT_PASSWORD":        "Track G seam G-3's migration to the fpp.mqtt store-backed kind",
	"SHOWMESH_FPP_MQTT_TOPIC_PREFIX":    "Track G seam G-3's migration to the fpp.mqtt store-backed kind",
	"SHOWMESH_FPP_MQTT_HOSTS":           "Track G seam G-3's migration to the fpp.mqtt store-backed kind",
	"SHOWMESH_ASSET_MAX_UPLOAD_BYTES":   "Track G seam G-4's migration to the assets.settings store-backed kind",
	"SHOWMESH_ASSET_CONTENT_BASE_URL":   "Track G seam G-4's migration to the assets.settings store-backed kind",
	"SHOWMESH_ASSET_SYNC_INTERVAL":      "Track G seam G-4's migration to the assets.settings store-backed kind",
	"SHOWMESH_ASSET_INVENTORY_INTERVAL": "Track G seam G-4's migration to the assets.settings store-backed kind",
}

// retiredRefusesToStartEnvVars is the narrowest group of all: a variable
// that is not migrated anywhere because ADR-024 decision 2 requires this
// coordinator to refuse to start when it is still set. It is read only
// to check its own absence.
var retiredRefusesToStartEnvVars = map[string]string{
	"SHOWMESH_API_TOKEN": "ADR-021's retired shared secret; ADR-024 decision 2 requires refusing to start " +
		"when this is still set, rather than accepting and honoring or silently ignoring it",
}

// envConstRegexp matches a whole string literal naming a SHOWMESH_*
// variable, e.g. `"SHOWMESH_HTTP_ADDR"` or an inline
// os.Getenv("SHOWMESH_HTTP_ADDR") argument.
var envConstRegexp = regexp.MustCompile(`^SHOWMESH_[A-Z0-9_]+$`)

// envSweepRoots are the directories this test sweeps, relative to this
// package's directory (internal/coordinator/config): every coordinator
// package plus the coordinator binary, not just this package — a
// SHOWMESH_ read anywhere in the coordinator is subject to ADR-039, not
// only one routed through this package's constants.
var envSweepRoots = []string{
	filepath.Join(".."), // internal/coordinator/...
	filepath.Join("..", "..", "..", "cmd", "showmesh-coordinator"), // the coordinator binary
}

// TestEveryEnvVarIsOnTheStartTimeAllowList is ADR-039 decision 9's first
// enforced test: every SHOWMESH_* name appearing as a string literal in
// coordinator non-test source (a constant, an inline os.Getenv argument,
// anything) must appear on exactly one of the allow-lists above, each
// carrying a stated reason. It parses source rather than hardcoding a
// second list of names, so a new SHOWMESH_ read added anywhere in the
// swept tree fails this test by construction — the point CLAUDE.md makes
// about ParameterID.MarshalJSON returning an error rather than a comment
// asking nicely.
//
// SHOWMESH_TEST_*-prefixed names are the one allowed convention: they
// are documented TEST-SUPPORT-ONLY harness knobs (see
// internal/coordinator/api/api.go's envStreamSubscriberBufferOverride
// and internal/coordinator/inventory/liveness.go's
// envStalenessWindowOverride), never operator configuration, and the
// sweep skips them by prefix rather than listing each one.
func TestEveryEnvVarIsOnTheStartTimeAllowList(t *testing.T) {
	found := collectCoordinatorEnvLiterals(t)

	allowed := map[string]bool{}
	for name := range startTimeEnvVars {
		allowed[name] = true
	}
	for name := range deploymentPostureEnvVars {
		allowed[name] = true
	}
	for name := range tuningKnobEnvVars {
		allowed[name] = true
	}
	for name := range knownGapEnvVars {
		allowed[name] = true
	}
	for name := range retiredEnvVars {
		allowed[name] = true
	}
	for name := range retiredRefusesToStartEnvVars {
		allowed[name] = true
	}

	for name := range found {
		if !allowed[name] {
			t.Errorf("%s appears in coordinator source but is on no allow-list — "+
				"a new operator-facing environment variable must be added to exactly one of "+
				"startTimeEnvVars/deploymentPostureEnvVars/tuningKnobEnvVars/knownGapEnvVars/"+
				"retiredEnvVars/retiredRefusesToStartEnvVars in envinventory_test.go, with a stated "+
				"reason, per ADR-039 decision 9", name)
		}
	}

	// The reverse direction: an allow-list entry no longer read by any
	// source file is stale bookkeeping, not a passing test — the whole
	// point of parsing source instead of hardcoding names is that this
	// list stays honest in both directions.
	for name := range allowed {
		if !found[name] {
			t.Errorf("%s is on an allow-list in envinventory_test.go but no string literal in "+
				"the swept coordinator source names it any more — remove the stale entry", name)
		}
	}
}

// collectCoordinatorEnvLiterals parses every non-test .go file under
// envSweepRoots and returns the set of SHOWMESH_* names appearing as a
// whole string literal ANYWHERE in the AST — a constant, an inline
// os.Getenv argument, a map key — not only in const declarations, so an
// inline read cannot slip past the allow-list. SHOWMESH_TEST_*-prefixed
// harness knobs are skipped by convention. Names built at runtime by
// string concatenation (e.g. integrationBrokerUsernameEnv's
// per-identifier derivation in integrationbrokers.go) are not literals
// and are intentionally not enumerable this way; their literal PREFIX
// (SHOWMESH_INTEGRATION_BROKERS, via envIntegrationBrokers) still is, and
// that is what carries the family onto the allow-list.
func collectCoordinatorEnvLiterals(t *testing.T) map[string]bool {
	t.Helper()

	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range envSweepRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}

			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if strings.HasPrefix(value, "SHOWMESH_TEST_") {
					return true
				}
				if envConstRegexp.MatchString(value) {
					found[value] = true
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	return found
}
