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

// agentConfigEnvVars is the agent-side counterpart to
// internal/coordinator/config/envinventory_test.go's startTimeEnvVars, but
// for a different reason: the node agent, unlike the coordinator, keeps no
// persistent store of its own (see this package's own doc comment on
// config.go: "environment-driven runtime configuration"). ADR-039 requires
// operator configuration to be store-backed precisely because the
// coordinator has a store a value could instead come from; that condition
// does not hold here, so every one of these is legitimate rather than a
// candidate for promotion. This list is the total documented environment
// surface of a store-less process, not an open question. Each reason
// points at where the value is actually documented, per this test's own
// convention of stating WHY, not restating the name.
var agentConfigEnvVars = map[string]string{
	"SHOWMESH_NODE_ID": "this node's identity segment in every showmesh/nodes/<node-id>/... topic; " +
		"Config.NodeID's doc comment covers the hostname-fallback and validate-at-load-time behavior",
	"SHOWMESH_NODE_LABEL": "optional human-readable name carried in the hello payload, distinct from the " +
		"machine NodeID; Config.NodeLabel",
	"SHOWMESH_NODE_CAPABILITIES": "explicit capability-set override for integration testing or a deliberate " +
		"operator override; Config.Capabilities's own doc comment notes there is no production capability " +
		"detection to wire it up to yet",
	"SHOWMESH_MQTT_BROKER": "the broker URL this agent connects to; Config.MQTTBroker, read once at LoadConfig " +
		"like the coordinator's own start-time MQTT broker variable",
	"SHOWMESH_MQTT_CLIENT_ID": "the MQTT client identifier; Config.MQTTClientID defaults from NodeID because, " +
		"unlike the coordinator's single instance, every agent on the network needs a distinct client ID",
	"SHOWMESH_MQTT_USERNAME": "optional broker credential; Config.MQTTUsername",
	"SHOWMESH_MQTT_PASSWORD": "optional broker credential, never included in any error or log output per " +
		"LoadConfig's own doc comment; mqtt.go's connection-rejection log line names it only to point an " +
		"operator at the fix, never to print its value",
	"SHOWMESH_LOG_LEVEL": "one of debug/info/warn/error, read once at LoadConfig; Config.LogLevel",
	"SHOWMESH_ASSET_DIR": "the node-local directory show assets are downloaded into and played from; " +
		"Config.AssetDir's doc comment notes this agent has no other persistent state-directory concept yet",
	"SHOWMESH_AGENT_API_TOKEN": "bearer credential asset.fetch sends the coordinator's read API; deliberately " +
		"not named SHOWMESH_API_TOKEN so copying an agent env line into a coordinator .env file cannot trigger " +
		"ADR-024 decision 2's coordinator startup refusal (see the envAgentAPIToken doc comment in config.go)",
	"SHOWMESH_ASSET_INVENTORY_INTERVAL": "asset inventory report cadence with a default, documented on " +
		"Config.AssetInventoryInterval",
	"SHOWMESH_RENDER_REPORT_INTERVAL": "render pipeline health report cadence with a default, documented on " +
		"Config.RenderReportInterval",
	"SHOWMESH_AUDIO_REPORT_INTERVAL": "audio discovery report cadence with a default, documented on " +
		"Config.AudioReportInterval",
	"SHOWMESH_MULTISYNC_LISTEN_ADDR": "the local host:port the render node's MultiSync listener binds; " +
		"Config.MultiSyncListenAddr defaults to pkg/multisync's own default",
	"SHOWMESH_MULTISYNC_INTERFACE": "restricts the MultiSync multicast group join to one named interface; " +
		"Config.MultiSyncInterface",
	"SHOWMESH_RENDER_DIAGNOSTIC_SURFACE": "names the node-local diagnostic idle surface, empty (the default) " +
		"disabling it; DiagnosticSurface's own doc comment records why the owner's ruling on diagnostic idle " +
		"output makes this node-local start-time configuration rather than a coordinator-delivered setting",
	"SHOWMESH_RENDER_DIAGNOSTIC_WIDTH":  "the diagnostic surface's width; DiagnosticSurface.Width",
	"SHOWMESH_RENDER_DIAGNOSTIC_HEIGHT": "the diagnostic surface's height; DiagnosticSurface.Height",
	"SHOWMESH_RENDER_DIAGNOSTIC_FRAME_RATE": "the diagnostic surface's tick rate, the whole of its timing " +
		"because it has no FSEQ to read a step time from; DiagnosticSurface.FrameRate",
	"SHOWMESH_RENDER_DIAGNOSTIC_NDI_SOURCE_NAME": "the NDI source the diagnostic surface sends on; empty runs " +
		"the same stated-degraded fakesink fallback an assignment with no usable output gets, per " +
		"DiagnosticSurface.NDISourceName",
	"SHOWMESH_GST_LAUNCH": "points at a gst-launch-1.0 binary outside PATH; internal/agent/pipeline/resolve.go's " +
		"own doc comment documents this as a value an operator can set, used as-is and not re-validated, " +
		"distinct from a subsystem capability because an absent binary degrades to \"unsupported\" rather than " +
		"failing anything (ADR-026 decision 6)",
	"SHOWMESH_GST_DISCOVERER": "points at a gst-discoverer-1.0 binary outside PATH; " +
		"internal/agent/audio/discoverer_resolve.go's own doc comment documents this the same way as " +
		"SHOWMESH_GST_LAUNCH above, an operator-settable binary location, not test-only scaffolding",
}

// agentScaffoldingEnvVars is a distinct category from agentConfigEnvVars
// above: a variable that exists only to let a test, a bench, or a
// development machine substitute a non-production value for something the
// agent would otherwise hardcode, never a value an operator sets on a real
// node. The dividing line is the one CLAUDE.md draws between configuration
// and scaffolding: agentConfigEnvVars is the agent's whole environment-driven
// surface because it keeps no store; this group is narrower still, entries
// an operator is never meant to touch at all.
var agentScaffoldingEnvVars = map[string]string{
	"SHOWMESH_GST_AUDIO_SINK_FACTORY": "substitutes a non-hardware GStreamer sink factory (e.g. \"fakesink\") " +
		"for the production \"alsasink\"; internal/agent/audionodeops.go's own doc comment on " +
		"envGstAudioSinkOverride says this exists so a test, a bench, or a development machine with no ALSA at " +
		"all can exercise the real gstengine backend without opening a real audio device, and calls it " +
		"scaffolding, named as such",
}

// agentEnvConstRegexp matches a whole string literal naming a SHOWMESH_*
// variable, matching internal/coordinator/config/envinventory_test.go's
// envConstRegexp.
var agentEnvConstRegexp = regexp.MustCompile(`^SHOWMESH_[A-Z0-9_]+$`)

// agentEnvSweepRoots are the directories this test sweeps, relative to
// this package's directory (internal/agent/config): every agent package
// plus the agent binary, mirroring
// internal/coordinator/config/envinventory_test.go's envSweepRoots so the
// coordinator and the agent get the same enforcement by construction
// rather than by one being remembered and the other not (ADR-039's own
// lesson about a correction that is not stated as a rule).
var agentEnvSweepRoots = []string{
	filepath.Join(".."), // internal/agent/...
	filepath.Join("..", "..", "..", "cmd", "showmesh-agent"), // the agent binary
}

// TestEveryAgentEnvVarIsOnAnAllowList is the agent-side counterpart to
// internal/coordinator/config/envinventory_test.go's
// TestEveryEnvVarIsOnTheStartTimeAllowList. It closes the gap ADR-039's own
// decision 9 left open: the two enforced tests it names cover the API and
// the CLI, but the coordinator's env inventory test never walked
// internal/agent or cmd/showmesh-agent, so a new agent-side SHOWMESH_ read
// could ship with no allow-list entry and no stated reason. Every
// SHOWMESH_* name appearing as a string literal in agent non-test source
// must appear on exactly one of agentConfigEnvVars/agentScaffoldingEnvVars
// above, each carrying a stated reason.
//
// SHOWMESH_TEST_*-prefixed names are skipped by the same convention as the
// coordinator test: internal/agent/heartbeat.go's
// envHeartbeatIntervalOverride is documented there as TEST-SUPPORT-ONLY and
// never a production tuning surface.
func TestEveryAgentEnvVarIsOnAnAllowList(t *testing.T) {
	found := collectAgentEnvLiterals(t)

	allowed := map[string]bool{}
	for name := range agentConfigEnvVars {
		allowed[name] = true
	}
	for name := range agentScaffoldingEnvVars {
		allowed[name] = true
	}

	for name := range found {
		if !allowed[name] {
			t.Errorf("%s appears in agent source but is on no allow-list — "+
				"a new agent-side environment variable must be added to exactly one of "+
				"agentConfigEnvVars/agentScaffoldingEnvVars in envinventory_test.go, with a stated reason, "+
				"per ADR-039's boundary applied to the agent tree", name)
		}
	}

	// The reverse direction, matching the coordinator test: a stale
	// allow-list entry naming a variable no swept file reads any more is
	// bookkeeping the test must catch, not silently carry forward.
	for name := range allowed {
		if !found[name] {
			t.Errorf("%s is on an allow-list in envinventory_test.go but no string literal in "+
				"the swept agent source names it any more — remove the stale entry", name)
		}
	}
}

// collectAgentEnvLiterals parses every non-test .go file under
// agentEnvSweepRoots and returns the set of SHOWMESH_* names appearing as a
// whole string literal anywhere in the AST, matching
// internal/coordinator/config/envinventory_test.go's
// collectCoordinatorEnvLiterals.
func collectAgentEnvLiterals(t *testing.T) map[string]bool {
	t.Helper()

	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range agentEnvSweepRoots {
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
				if agentEnvConstRegexp.MatchString(value) {
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
