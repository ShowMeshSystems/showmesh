//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// Environment variables this package reads. All are optional; every
// default matches what scripts/test-integration.sh (the `make
// test-integration` target) exports, so `go test -tags=integration
// ./test/integration/...` also works unmodified against a broker started
// that way.
const (
	// envBrokerURL is the MQTT broker URL both the agent subprocess and the
	// coordinator subprocess connect to. Default matches defaultBrokerURL
	// below, a non-production port chosen so this suite does not collide
	// with a developer's already-running reference deployment
	// (deploy/docker-compose.yml's bundled broker listens on 1883).
	envBrokerURL = "SHOWMESH_TEST_MQTT_BROKER"

	// envMosquittoContainer, when set, names a running Docker container
	// this package may `docker restart` for
	// TestBrokerRestartResubscribesAndObservesSubsequentChanges. Left
	// unset, that one test explicitly skips (with a message saying why —
	// see the Task E spec's "say so explicitly" requirement) rather than
	// silently omitting broker-restart coverage.
	envMosquittoContainer = "SHOWMESH_TEST_MOSQUITTO_CONTAINER"

	// envHeartbeatOverride is forwarded verbatim into every agent
	// subprocess's environment; see
	// internal/agent/heartbeat.go's envHeartbeatIntervalOverride, which is
	// the production-code half of this knob.
	envHeartbeatOverride = "SHOWMESH_TEST_HEARTBEAT_INTERVAL"

	// envStalenessOverride must already be set in the process environment
	// before `go test` starts (exactly like envHeartbeatOverride's own
	// constraint, and the package doc comment's "make... `go test
	// -tags=integration ./test/integration/...` also work unmodified
	// against a broker started that way"): it is read here so tests can
	// size their own wait bounds against whatever value is actually in
	// effect (see testStalenessWindow below), AND forwarded verbatim into
	// every showmesh-coordinator subprocess's environment by
	// startCoordinatorWithConfig — internal/coordinator/inventory reads it
	// exactly once, at that subprocess's own package initialization (see
	// that package's envStalenessWindowOverride), and a subprocess with an
	// explicit, non-inherited environment never sees this test binary's
	// own environment variable unless something forwards it. This
	// forwarding was itself missing until Step 3 review finding 4.12's own
	// tests exposed it — every coordinator subprocess before that ran with
	// the real 30s production StalenessWindow regardless of this
	// override, silently, because nothing before depended on the
	// coordinator's own staleness computation happening quickly.
	envStalenessOverride = "SHOWMESH_TEST_STALENESS_WINDOW"

	// envTestMQTTCoordinatorUsername and envTestMQTTCoordinatorPassword name
	// the broker credential scripts/test-integration.sh generates and seeds
	// into the throwaway broker's password file before it ever starts (see
	// that script's comment on why: mosquitto now refuses to start at all
	// with allow_anonymous false and no password_file present — ADR-024
	// decision 10). Every coordinator subprocess this package starts uses
	// this credential by default; a test that specifically needs a WRONG
	// credential (proving a rejection, not merely a successful connect)
	// overrides it via coordinatorConfig's own mqttUsername/mqttPassword
	// fields instead of touching these.
	envTestMQTTCoordinatorUsername = "SHOWMESH_TEST_MQTT_COORDINATOR_USERNAME"
	envTestMQTTCoordinatorPassword = "SHOWMESH_TEST_MQTT_COORDINATOR_PASSWORD"

	// envTestMQTTBurstPublisherUsername and envTestMQTTBurstPublisherPassword
	// name the dedicated, TEST-ONLY broker credential
	// scripts/test-integration.sh provisions (with a matching TEST-ONLY
	// acl.conf stanza appended to a copy of the real committed file — see
	// that script's own comment) for api_test.go's publishHelloBurst. No
	// credential provisioned for a real ADR-024 decision 10 principal class
	// can do what that helper needs (publish hello for many distinct
	// synthetic node IDs from one connection) — see the script's comment on
	// why that is deliberate, not a gap.
	envTestMQTTBurstPublisherUsername = "SHOWMESH_TEST_MQTT_BURST_PUBLISHER_USERNAME"
	envTestMQTTBurstPublisherPassword = "SHOWMESH_TEST_MQTT_BURST_PUBLISHER_PASSWORD"

	// envAssetSyncInterval and envAssetInventoryInterval are, unlike
	// envHeartbeatOverride/envStalenessOverride above, the SAME names the
	// coordinator (SHOWMESH_ASSET_SYNC_INTERVAL) and the agent
	// (SHOWMESH_ASSET_INVENTORY_INTERVAL) read as their own real
	// production config — Track E needed no separate test-only override
	// mechanism because these two already are ordinary env-configured
	// durations. scripts/test-integration.sh exports small values for
	// both (see that script's own comment for the ratio reasoning);
	// startAgent/startCoordinatorWithConfig forward whatever is in THIS
	// process's own environment into every subprocess by default, mirroring
	// envHeartbeatOverride's identical forwarding one field over, so the
	// coordinator's and every agent's idea of the inventory cadence agree
	// unless a test explicitly asks for something else via
	// coordinatorConfig.assetInventoryInterval.
	envAssetSyncInterval      = "SHOWMESH_ASSET_SYNC_INTERVAL"
	envAssetInventoryInterval = "SHOWMESH_ASSET_INVENTORY_INTERVAL"

	// envRenderReportInterval is envAssetInventoryInterval's identical twin
	// for the agent's render report ticker (internal/agent/config's own
	// SHOWMESH_RENDER_REPORT_INTERVAL — real production config, no
	// separate test-only override needed). Forwarded by startAgent exactly
	// like envAssetInventoryInterval; kill_test.go sets it in the test
	// process's own environment so a killed pipeline's restart is visible
	// through the coordinator's real render report cadence within a bounded
	// wait, rather than only at the 15s production default.
	envRenderReportInterval = "SHOWMESH_RENDER_REPORT_INTERVAL"

	// envAudioReportInterval is envRenderReportInterval's identical twin
	// for the agent's audio report ticker (internal/agent/config's own
	// SHOWMESH_AUDIO_REPORT_INTERVAL). Forwarded by startAgent exactly
	// like envRenderReportInterval; audio_broker_loss_test.go sets it in
	// the test process's own environment so a session's post-outage
	// evidence is visible within a bounded wait rather than only at the
	// 15s production default.
	envAudioReportInterval = "SHOWMESH_AUDIO_REPORT_INTERVAL"

	// envGstAudioSinkOverride mirrors internal/agent's own
	// SHOWMESH_GST_AUDIO_SINK_FACTORY (audionodeops.go) — the ONE
	// test-only environment variable Track C phase 1b adds, so an agent
	// under test can build the real gstengine backend against "fakesink"
	// instead of "alsasink" and exercise it with no real audio device.
	// Forwarded by startAgent exactly like the intervals above.
	envGstAudioSinkOverride = "SHOWMESH_GST_AUDIO_SINK_FACTORY"
)

const defaultBrokerURL = "tcp://localhost:11883"

var (
	// brokerURL, brokerReachable, mosquittoContainer, agentBinPath, and
	// coordinatorBinPath are resolved once in TestMain and read (never
	// written) by every test below.
	brokerURL          string
	brokerReachable    bool
	mosquittoContainer string
	agentBinPath       string
	coordinatorBinPath string
	showmeshctlBinPath string

	// testHeartbeatInterval and testStalenessWindow mirror, in this test
	// binary, the same environment variables the agent subprocess and the
	// coordinator subprocess resolve for themselves — see
	// envHeartbeatOverride and envStalenessOverride above. They exist only
	// so tests can size wait bounds proportionally to whatever cadence is
	// actually in effect, without hardcoding the production 10s/30s values
	// this suite deliberately never runs with.
	testHeartbeatInterval time.Duration
	testStalenessWindow   time.Duration

	// testMQTTCoordinatorUsername and testMQTTCoordinatorPassword are read
	// from envTestMQTTCoordinatorUsername/Password in TestMain — see those
	// constants' doc comment. Empty when unset, which every test that
	// starts a coordinator or an agent must treat as "this suite cannot
	// authenticate to the broker" rather than silently connecting
	// anonymously (the broker no longer allows that at all): see
	// startCoordinatorWithConfig and startAgent, both of which fail t
	// loudly rather than starting a subprocess that can only ever be
	// rejected.
	testMQTTCoordinatorUsername string
	testMQTTCoordinatorPassword string

	// testMQTTBurstPublisherUsername and testMQTTBurstPublisherPassword are
	// read from envTestMQTTBurstPublisherUsername/Password in TestMain —
	// see those constants' doc comment. Used only by api_test.go's
	// publishHelloBurst.
	testMQTTBurstPublisherUsername string
	testMQTTBurstPublisherPassword string
)

func parseDurationEnv(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// TestMain resolves the broker address once, probes it (skipping every
// test with a clear message rather than failing if nothing answers — see
// probeBroker), and builds both the showmesh-agent and showmesh-coordinator
// binaries once for every test in this package to exec.
//
// Building the real showmesh-coordinator binary here — rather than wiring
// internal/coordinator/{store,inventory,broker} together in-process, which
// is what this package did before Step 3 landed a read API to observe the
// coordinator through — is the single highest-value change Step 3's wiring
// task made to this suite. In-process wiring tests the components; it does
// not test coordinator.Run, and Step 2's worst defect (a shutdown-ordering
// bug the unit suite asserted correctly against a fake while the real
// wiring did the opposite) survived specifically because nothing exercised
// the real process. A "coordinator restart" in this package is now a real
// process restart against the same SQLite file and the same broker — see
// startCoordinator and testCoordinator.shutdown.
func TestMain(m *testing.M) {
	brokerURL = os.Getenv(envBrokerURL)
	if brokerURL == "" {
		brokerURL = defaultBrokerURL
	}
	mosquittoContainer = os.Getenv(envMosquittoContainer)

	testHeartbeatInterval = parseDurationEnv(envHeartbeatOverride, 10*time.Second)
	testStalenessWindow = parseDurationEnv(envStalenessOverride, 30*time.Second)

	testMQTTCoordinatorUsername = os.Getenv(envTestMQTTCoordinatorUsername)
	testMQTTCoordinatorPassword = os.Getenv(envTestMQTTCoordinatorPassword)
	testMQTTBurstPublisherUsername = os.Getenv(envTestMQTTBurstPublisherUsername)
	testMQTTBurstPublisherPassword = os.Getenv(envTestMQTTBurstPublisherPassword)

	brokerReachable = probeBroker(brokerURL, 2*time.Second)

	var cleanupFuncs []func()
	code := func() int {
		if !brokerReachable {
			fmt.Fprintf(os.Stderr,
				"integration: no MQTT broker reachable at %s; every test in this package will be skipped.\n"+
					"Run `make test-integration` (starts one for you), or start a broker per\n"+
					"deploy/mosquitto/mosquitto.conf and set %s.\n",
				brokerURL, envBrokerURL)
			return m.Run() // every test skips itself via requireBroker
		}

		agentBin, agentCleanup, err := buildBinary("showmesh-agent", "./cmd/showmesh-agent")
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration: failed to build the showmesh-agent binary: %v\n", err)
			return 1
		}
		agentBinPath = agentBin
		cleanupFuncs = append(cleanupFuncs, agentCleanup)

		coordBin, coordCleanup, err := buildBinary("showmesh-coordinator", "./cmd/showmesh-coordinator")
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration: failed to build the showmesh-coordinator binary: %v\n", err)
			return 1
		}
		coordinatorBinPath = coordBin
		cleanupFuncs = append(cleanupFuncs, coordCleanup)

		showmeshctlBin, ctlCleanup, err := buildBinary("showmeshctl", "./cmd/showmeshctl")
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration: failed to build the showmeshctl binary: %v\n", err)
			return 1
		}
		showmeshctlBinPath = showmeshctlBin
		cleanupFuncs = append(cleanupFuncs, ctlCleanup)

		return m.Run()
	}()

	for _, cleanup := range cleanupFuncs {
		cleanup()
	}
	os.Exit(code)
}

// probeBroker reports whether a bare TCP connection to rawURL's host:port
// succeeds within timeout. It deliberately does not speak MQTT: the point
// is only to distinguish "nothing is listening" (an unprepared laptop, per
// the Task E spec) from every other kind of failure, which surfaces later
// as a normal test failure with the broker's own error attached.
func probeBroker(rawURL string, timeout time.Duration) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// requireBroker skips t, with a clear message, if TestMain could not reach
// a broker. Every test in this package calls this first: per the Task E
// spec, an unprepared laptop must get a skip, never a mysterious failure
// deep inside a subprocess launch or a connection attempt.
func requireBroker(t *testing.T) {
	t.Helper()
	if !brokerReachable {
		skipOrFatalDependency(t, depBroker, "no MQTT broker reachable at %s (set %s, or run `make test-integration`)", brokerURL, envBrokerURL)
	}
}

// envRequireTestDeps names which dependencies the harness that invoked
// `go test` actually guarantees, as a comma-separated list of the depXxx
// constants below (for example "broker" or "broker,fpp"). A dependency
// guard for something on that list becomes a hard test failure instead of
// a skip. docs/build/LESSONS.md: "make test-integration-fpp had been
// silently skipping its main test on every single run since Step 6 landed
// allow_anonymous false ... a skip *looks* like a considered decision, and
// a dependency guard is exactly the kind of considered decision a
// reviewer nods past." Every scripts/test-integration*.sh sets this before
// invoking `go test`, so a missing dependency under a `make
// test-integration*` target whose entire job is to supply that dependency
// fails loudly instead of reporting a quiet, green skip. Run by hand with
// this unset, the skip stays the convenient, laptop-friendly default.
//
// It is a LIST rather than a boolean, and that is the whole point of this
// second revision: the first one was a plain truthy flag, and CI failed on
// it immediately. `make test-integration` starts a broker and no fppd, so
// under a boolean flag it demanded an FPP it never supplies and turned
// three legitimately-skipping tests into failures. A harness may only be
// held to the dependencies it actually provides.
//
// Worth recording alongside that: this passed locally and failed on CI
// for the oldest reason in this project. A bench fppd happened to be
// running on the developer's own localhost:8090, so the FPP guards found a
// live FPP and never fired. The environment differed from the deployment
// environment, and the result reported success on exactly that difference.
const envRequireTestDeps = "SHOWMESH_REQUIRE_TEST_DEPS"

// Dependency names carried in envRequireTestDeps. Each names something a
// harness script starts and can therefore be held responsible for.
const (
	depBroker = "broker" // a reachable, credentialed MQTT broker
	depFPP    = "fpp"    // a reachable bench fppd answering /api/fppd/status
)

// requireTestDep reports whether the harness that invoked this test run
// declared that it supplies dep. "1"/"true"/"yes"/"all" mean every
// dependency, so an older or hand-written harness cannot silently weaken
// the guard by naming nothing.
func requireTestDep(dep string) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envRequireTestDeps)))
	switch raw {
	case "", "0", "false", "no":
		return false
	case "1", "true", "yes", "all":
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == dep {
			return true
		}
	}
	return false
}

// skipOrFatalDependency is what every dependency guard in this package
// (and, by the identical env var name, internal/coordinator/collector/fpp's
// and fppmqtt's own integration tests) calls instead of t.Skipf directly:
// a skip when the invoking harness did not claim to supply dep (the
// convenient, unprepared-laptop default), and a hard t.Fatalf when it did
// and then failed to.
func skipOrFatalDependency(t *testing.T, dep string, format string, args ...any) {
	t.Helper()
	if requireTestDep(dep) {
		t.Fatalf(format, args...)
		return
	}
	t.Skipf(format, args...)
}

// moduleRoot walks up from this source file's own directory to find the
// module root (the directory containing go.mod), using runtime.Caller
// rather than os.Getwd so this works regardless of where `go test` is
// invoked from.
func moduleRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed to report this file's path")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// buildBinary builds pkgPath (e.g. "./cmd/showmesh-agent") once into a
// fresh temp directory and returns its path and a cleanup func that
// removes it. Shared by every binary this package execs (the agent, the
// coordinator, and showmeshctl — acceptance criterion (a) requires the
// latter run as a real subprocess too).
func buildBinary(name, pkgPath string) (path string, cleanup func(), err error) {
	root, err := moduleRoot()
	if err != nil {
		return "", nil, err
	}

	dir, err := os.MkdirTemp("", "showmesh-"+name+"-bin-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	bin := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", bin, pkgPath)
	cmd.Dir = root
	cmd.Env = os.Environ() // the build itself needs a normal Go toolchain environment

	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build -o %s %s: %w\n%s", bin, pkgPath, err, out)
	}
	return bin, cleanup, nil
}

// uniqueSuffix returns a per-process-unique, lowercase-alphanumeric string
// safe to embed in an mqttproto node ID or MQTT client ID, so concurrent
// (or merely sequential-but-reused-broker) tests never collide on retained
// topics left over from an earlier run.
var uniqueCounter int64

func uniqueSuffix() string {
	n := atomic.AddInt64(&uniqueCounter, 1)
	return fmt.Sprintf("%d%d", time.Now().UnixNano(), n)
}

// syncBuffer is an io.Writer safe for concurrent use, so a subprocess's
// stdout/stderr can be captured from its own goroutine while the test
// itself keeps running.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// agentConfig is what startAgent needs to launch one showmesh-agent
// subprocess. capabilities, if set, is passed through verbatim as
// SHOWMESH_NODE_CAPABILITIES.
type agentConfig struct {
	nodeID       string
	label        string
	capabilities string

	// mqttUsername and mqttPassword, when mqttUsername is non-empty,
	// override startAgent's default behavior of provisioning a fresh,
	// correct broker credential for nodeID via provisionAgentCredential.
	// Used by broker_auth_test.go to start an agent with a wrong or
	// unprovisioned credential and observe the ADR-024 decision 10
	// rejection behavior, rather than a working connection. Every other
	// caller in this package leaves these empty and gets a real,
	// provisioned credential transparently — see startAgent.
	mqttUsername string
	mqttPassword string

	// assetDir is Track E seam E3/E6's own SHOWMESH_ASSET_DIR: the
	// node-local directory this agent downloads assets into and enumerates
	// on every inventory publish. Left empty, the agent falls back to its
	// own "./assets" relative default — every asset-focused test in this
	// package sets this explicitly to a t.TempDir() subdirectory so it
	// knows exactly where to look for (or corrupt) a node's held bytes.
	assetDir string
}

// testAgent wraps one real showmesh-agent subprocess: a genuine OS process
// with its own MQTT connection and its own registered Last Will, per the
// Task E spec's requirement that criterion 2 (an unclean kill) be exercised
// against something SIGKILL can actually be sent to.
type testAgent struct {
	t        *testing.T
	cmd      *exec.Cmd
	nodeID   string
	logs     *syncBuffer
	waitDone chan struct{}
	waitErr  error
}

// startAgent execs agentBinPath as a subprocess with a minimal, explicit
// environment (not inherited from the test process) so nothing about the
// developer's own shell can leak into what is meant to be a hermetic
// subject under test.
func startAgent(t *testing.T, cfg agentConfig) *testAgent {
	t.Helper()
	requireBroker(t)

	username, password := cfg.mqttUsername, cfg.mqttPassword
	if username == "" {
		// ADR-024 decision 10: the broker no longer allows anonymous
		// connections at all, so every agent subprocess this package
		// starts needs a real, working credential unless a test
		// deliberately asked for a broken one (cfg.mqttUsername set — see
		// agentConfig's doc comment). provisionAgentCredential adds it to
		// the running broker's password file and confirms it is live
		// before returning, so every existing test in this package that
		// calls startAgent needs no changes of its own to keep working.
		username, password = provisionAgentCredential(t, cfg.nodeID)
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"), // harmless if unused; costs nothing to include
		"SHOWMESH_NODE_ID=" + cfg.nodeID,
		"SHOWMESH_MQTT_BROKER=" + brokerURL,
		"SHOWMESH_MQTT_CLIENT_ID=showmesh-agent-test-" + cfg.nodeID,
		"SHOWMESH_MQTT_USERNAME=" + username,
		"SHOWMESH_MQTT_PASSWORD=" + password,
		"SHOWMESH_LOG_LEVEL=debug",
	}
	if raw := os.Getenv(envHeartbeatOverride); raw != "" {
		env = append(env, envHeartbeatOverride+"="+raw)
	}
	if raw := os.Getenv(envAssetInventoryInterval); raw != "" {
		env = append(env, envAssetInventoryInterval+"="+raw)
	}
	if raw := os.Getenv(envRenderReportInterval); raw != "" {
		env = append(env, envRenderReportInterval+"="+raw)
	}
	if raw := os.Getenv(envAudioReportInterval); raw != "" {
		env = append(env, envAudioReportInterval+"="+raw)
	}
	if raw := os.Getenv(envGstAudioSinkOverride); raw != "" {
		env = append(env, envGstAudioSinkOverride+"="+raw)
	}
	if cfg.capabilities != "" {
		env = append(env, "SHOWMESH_NODE_CAPABILITIES="+cfg.capabilities)
	}
	if cfg.label != "" {
		env = append(env, "SHOWMESH_NODE_LABEL="+cfg.label)
	}
	if cfg.assetDir != "" {
		env = append(env, "SHOWMESH_ASSET_DIR="+cfg.assetDir)
	}

	cmd := exec.Command(agentBinPath)
	cmd.Env = env

	logs := &syncBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start agent subprocess for node %s: %v", cfg.nodeID, err)
	}

	a := &testAgent{t: t, cmd: cmd, nodeID: cfg.nodeID, logs: logs, waitDone: make(chan struct{})}
	go func() {
		a.waitErr = cmd.Wait()
		close(a.waitDone)
	}()

	t.Cleanup(func() {
		a.stopIfRunning()
		if t.Failed() {
			t.Logf("agent %s (pid %d) combined output:\n%s", cfg.nodeID, cmd.Process.Pid, logs.String())
		}
	})

	return a
}

// sigkill sends SIGKILL — the only honest way to produce the unclean
// disconnect criterion 2 requires (see the package doc comment) — and
// waits for the process to actually exit.
func (a *testAgent) sigkill(t *testing.T) {
	t.Helper()
	if err := a.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL agent %s: %v", a.nodeID, err)
	}
	a.waitForExit(t, 5*time.Second)
}

// sigterm sends SIGTERM (a normal, catchable shutdown signal) without
// waiting for exit; callers that care about the exit itself call
// waitForExit separately, so they can bound how long the agent's own
// clean-shutdown path (publish offline, then disconnect) is allowed to
// take.
func (a *testAgent) sigterm(t *testing.T) {
	t.Helper()
	if err := a.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM agent %s: %v", a.nodeID, err)
	}
}

func (a *testAgent) waitForExit(t *testing.T, timeout time.Duration) {
	t.Helper()
	select {
	case <-a.waitDone:
	case <-time.After(timeout):
		t.Fatalf("agent %s did not exit within %s", a.nodeID, timeout)
	}
}

// sigstop suspends the agent process with SIGSTOP, WITHOUT closing its TCP
// connection to the broker — no FIN, no RST, and (unlike sigkill) no
// registered Last Will fires, because from the broker's point of view the
// session is still fully alive; the process is simply not scheduled to run
// any code, including its own heartbeat publish loop or any MQTT
// keepalive PINGREQ. This is "a node whose heartbeats simply stop, no
// further message ever arrives, and no last will either" (see
// internal/coordinator/inventory's RecordLivenessObservation doc comment)
// made real rather than simulated: staleness-driven liveness has to
// notice this on its own, with no message of any kind to react to. Used
// by TestStalenessDrivenTransitionRecordedByHubTickAloneNoAPICallEver.
func (a *testAgent) sigstop(t *testing.T) {
	t.Helper()
	if err := a.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatalf("SIGSTOP agent %s: %v", a.nodeID, err)
	}
}

// sigcont resumes an agent previously suspended with sigstop. Callers that
// called sigstop must call this before the test returns (a defer right
// after sigstop is the usual shape) — see stopIfRunning's own doc comment
// for why leaving a process stopped is not safe to rely on cleanup alone
// to fix on every platform.
func (a *testAgent) sigcont(t *testing.T) {
	t.Helper()
	if err := a.cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatalf("SIGCONT agent %s: %v", a.nodeID, err)
	}
}

// stopIfRunning kills the agent if it is still alive and always joins the
// Wait goroutine, so cleanup never leaks either a process or a goroutine
// regardless of how a test ended. It sends SIGCONT before SIGKILL
// unconditionally — harmless if the process was never stopped, but a
// defense-in-depth against a test that called sigstop and, on a failing
// assertion partway through, never reached its own sigcont: SIGKILL is
// unblockable and does terminate an already-stopped process on every
// platform this project targets, but resuming it first is the
// belt-and-suspenders choice, since a leaked stopped process is a much
// worse failure to debug than a redundant signal.
func (a *testAgent) stopIfRunning() {
	select {
	case <-a.waitDone:
		return
	default:
	}
	_ = a.cmd.Process.Signal(syscall.SIGCONT)
	_ = a.cmd.Process.Kill()
	<-a.waitDone
}

// findFreePort asks the OS for a currently-unused TCP port on 127.0.0.1 by
// binding to port 0 and immediately releasing it. There is an inherent,
// unavoidable TOCTOU race between this and the coordinator subprocess
// actually binding the same port a moment later; in practice nothing else
// on a CI runner or a developer laptop is racing to grab ephemeral ports at
// the exact moment a `go test` process is starting a child, and
// startCoordinator fails loudly (dumping the subprocess's own stderr) if
// the bind ever does lose that race, rather than hanging.
func findFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// createAdminAndIssueToken provisions a real administrator principal and
// mints a real bearer token for it against dataDir, using the coordinator
// binary's own host-level subcommands (ADR-024 decision 9's path:
// `create-admin` then `issue-token`, both run BEFORE any coordinator
// subprocess is started against this same dataDir, so there is no
// concurrent-SQLite-access question to reason about). This is this
// package's ADR-024 replacement for the retired SHOWMESH_API_TOKEN shared
// secret: TestAPITokenEnforcedWhenSet and TestAPIStreamOpensSuccessfullyWithAuthEnabled
// used to set coordinatorConfig.apiToken, which set SHOWMESH_API_TOKEN —
// a coordinator carrying ADR-024 now refuses to start at all with that
// variable set (config.checkAPITokenRetired), which is why this suite was
// red at HEAD. Every caller that needs a real, scoped credential against
// a closed-reads coordinator calls this once, before starting it, and
// passes the returned token as coordinatorConfig.bearerToken.
func createAdminAndIssueToken(t *testing.T, dataDir, name, password string) (token string) {
	t.Helper()

	runCoordinatorSubcommand(t, dataDir, []string{"create-admin", "-name=" + name}, password+"\n")
	out := runCoordinatorSubcommand(t, dataDir, []string{"issue-token", "-principal=" + name, "-label=integration-test"}, "")

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("issue-token produced no output")
	}
	token = lines[len(lines)-1]
	if !strings.HasPrefix(token, "smsh_") {
		t.Fatalf("last line of issue-token output = %q, want it to look like a token (prefix \"smsh_\"); full output:\n%s", token, out)
	}
	return token
}

// ensureShow creates a "show" config object via showmeshctl — the
// prerequisite a show.action, show.macro, or show.surface fixture now
// requires: each validates its own "show" field for existence at write
// time (ADR-027), refusing a name that does not resolve. Every caller
// mints its own showID (uniqueSuffix(), matching every other fixture id
// in this package); this does not check for an existing show first.
func ensureShow(t *testing.T, coord *testCoordinator, token, showID, name string) {
	t.Helper()
	mustCtl(t, coord, token, []string{"show", "set", "--name", name}, showID)
}

// runCoordinatorSubcommand execs coordinatorBinPath as a subprocess with
// the given args and SHOWMESH_DATA_DIR=dataDir (matching
// cmd/showmesh-coordinator's own subcommand dispatch, which reads that
// variable directly rather than going through the full config.LoadConfig
// path — see that package's subcommands.go doc comment), feeding stdin
// and returning stdout. Fails t on a non-zero exit, dumping stderr.
func runCoordinatorSubcommand(t *testing.T, dataDir string, args []string, stdin string) string {
	t.Helper()
	cmd := exec.Command(coordinatorBinPath, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SHOWMESH_DATA_DIR=" + dataDir}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run %s %v: %v; stderr:\n%s", coordinatorBinPath, args, err, stderr.String())
	}
	return stdout.String()
}

// coordinatorConfig is what startCoordinatorWithConfig needs to launch one
// showmesh-coordinator subprocess. Every field has a sensible default so
// the common case (startCoordinator) only needs a data directory and an
// MQTT client ID, matching this package's pre-Step-3 call sites.
type coordinatorConfig struct {
	dataDir   string
	clientID  string
	brokerURL string // defaults to the package-level brokerURL

	// closeReads forwards SHOWMESH_API_CLOSE_READS=true — ADR-024
	// decision 2's read-closure switch. Pair with bearerToken (a real
	// token minted via [createAdminAndIssueToken] BEFORE this coordinator
	// starts) to exercise a closed-reads coordinator; see that function's
	// doc comment for why this replaced the retired apiToken field.
	closeReads bool

	// bearerToken, when non-empty, is attached by [testCoordinator.getRaw]/
	// [readStreamFor] as "Authorization: Bearer <value>" — never written
	// to this subprocess's environment (a real per-principal token is
	// presented per-request, exactly like a real client would, never
	// configured into the coordinator itself the way the retired shared
	// secret was).
	bearerToken string

	fppEndpoints string // SHOWMESH_FPP_ENDPOINTS raw value; defaults to unset

	// httpAddr, when non-empty, is used verbatim instead of allocating a
	// fresh port via findFreePort — used by
	// TestWatchResnapshotsAfterCoordinatorRestart to rebuild a coordinator
	// on the exact same address a client is already pointed at, the same
	// way restart_test.go's coordinator-restart tests reuse the same data
	// directory and MQTT client ID.
	httpAddr string

	// streamSubscriberBuffer, when > 0, is forwarded as
	// SHOWMESH_TEST_STREAM_SUBSCRIBER_BUFFER — internal/coordinator/api's
	// test-support-only override for its SSE per-subscriber buffer size
	// (see that package's envStreamSubscriberBufferOverride). Used by
	// TestSlowSSEConsumerGetsResetAndDisconnected to force contract section
	// 6.4's overflow-then-disconnect behavior deterministically with a
	// small burst of real changes, rather than an implausibly large flood.
	streamSubscriberBuffer int

	// mqttUsername and mqttPassword override the package-level
	// testMQTTCoordinatorUsername/Password (the working credential
	// scripts/test-integration.sh seeds into the broker before it starts —
	// see envTestMQTTCoordinatorUsername's doc comment) when mqttUsername
	// is non-empty. Used by broker_auth_test.go to start a coordinator
	// with a wrong credential and observe the ADR-024 decision 10
	// rejection behavior (the coordinator must still start and stay up —
	// this is deliverable 5's own acceptance test) rather than a
	// successful connection. Every other caller leaves these empty and
	// gets the suite's normal working credential.
	mqttUsername string
	mqttPassword string

	// --- Track E seam E3/E4/E5/E6: the asset store (ADR-028) ---

	// assetDir forwards SHOWMESH_ASSET_DIR when non-empty — the volume
	// backend's root. Left empty, the coordinator falls back to its own
	// "<DataDir>/assets" default; asset tests that need to reach into the
	// content-addressed layout directly (to corrupt a blob, or to confirm
	// nothing was staged) set this explicitly so the path is known rather
	// than reconstructed.
	assetDir string

	// assetContentBaseURL forwards SHOWMESH_ASSET_CONTENT_BASE_URL when
	// non-empty. Left empty (the default), the sync service does not run
	// at all — see that env var's own doc comment in
	// internal/coordinator/config. A test that wants a node to actually
	// fetch bytes over the network sets this to this coordinator's OWN
	// http://host:port (derived from the SAME httpAddr this config
	// allocates — see startAssetCoordinator in assets_test.go) or to a
	// stoppable proxy in front of it.
	assetContentBaseURL string

	// assetSyncInterval and assetInventoryInterval forward
	// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL when
	// non-zero; left zero, startCoordinatorWithConfig forwards whatever
	// scripts/test-integration.sh already exported into THIS process's own
	// environment (see envAssetSyncInterval/envAssetInventoryInterval's own
	// doc comment) rather than the real multi-minute production defaults.
	assetSyncInterval      time.Duration
	assetInventoryInterval time.Duration

	// assetMaxUploadBytes forwards SHOWMESH_ASSET_MAX_UPLOAD_BYTES when
	// non-zero; left zero, the coordinator uses its own
	// assetstore.DefaultMaxUploadBytes (2 GiB) default.
	assetMaxUploadBytes int64
}

// testCoordinator wraps one real showmesh-coordinator subprocess — a
// genuine OS process, its own SQLite file, its own MQTT connection, and its
// own HTTP listener — observed exclusively through /api/v1 and the
// container-healthcheck probes, exactly as an external client (an
// operator's browser, showmeshctl, a monitoring probe) would. See the
// package doc comment for why this replaced the in-process wiring Step 2
// used.
type testCoordinator struct {
	t        *testing.T
	cmd      *exec.Cmd
	httpAddr string // host:port this coordinator's HTTP server listens on

	// token is [coordinatorConfig.bearerToken] carried onto this handle —
	// a real per-principal bearer token minted via
	// [createAdminAndIssueToken] before this coordinator started, ADR-024's
	// replacement for ADR-021's single shared SHOWMESH_API_TOKEN secret
	// this field used to hold directly. Attached by getRaw/readStreamFor
	// exactly the way any real client would present it, per-request.
	token string

	logs   *syncBuffer
	client *http.Client

	waitDone chan struct{}
	waitErr  error

	mu   sync.Mutex
	done bool
}

// startCoordinator is startCoordinatorWithConfig with every field left at
// its default: no auth, no FPP endpoints configured, connecting to this
// package's resolved test broker. Matches every pre-Step-3 call site in
// this package.
func startCoordinator(t *testing.T, dataDir, clientID string) *testCoordinator {
	t.Helper()
	return startCoordinatorWithConfig(t, coordinatorConfig{dataDir: dataDir, clientID: clientID})
}

// startCoordinatorWithConfig execs coordinatorBinPath as a subprocess with
// a minimal, explicit environment (not inherited from the test process),
// waits for its HTTP listener to actually answer /healthz (bounded; the
// coordinator's own startup — opening SQLite, beginning to connect to MQTT
// — is not instantaneous), and returns a handle for observing and
// eventually stopping it.
//
// Passing the same dataDir and clientID to a second call after shutdown()
// is exactly TestCoordinatorRestartRestoresInventoryFromRetainedTopics's
// "teardown and rebuild against the same database and broker" — now a real
// process exiting and a new one starting, not a Go-level struct being torn
// down and reconstructed.
func startCoordinatorWithConfig(t *testing.T, cfg coordinatorConfig) *testCoordinator {
	t.Helper()
	requireBroker(t)

	broker := cfg.brokerURL
	if broker == "" {
		broker = brokerURL
	}
	httpAddr := cfg.httpAddr
	if httpAddr == "" {
		httpAddr = fmt.Sprintf("127.0.0.1:%d", findFreePort(t))
	}

	// ADR-024 decision 10: allow_anonymous is false, so a credential is
	// mandatory, not optional, for every coordinator subprocess this
	// package starts. cfg.mqttUsername overrides the suite's normal
	// working credential (see coordinatorConfig's doc comment) for the
	// one test that deliberately wants a rejection.
	mqttUsername, mqttPassword := cfg.mqttUsername, cfg.mqttPassword
	if mqttUsername == "" {
		if testMQTTCoordinatorUsername == "" {
			t.Fatalf("no MQTT broker credential available (%s is unset) — run via `make test-integration`, which provisions one, rather than `go test` directly against an ad hoc broker",
				envTestMQTTCoordinatorUsername)
		}
		mqttUsername, mqttPassword = testMQTTCoordinatorUsername, testMQTTCoordinatorPassword
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"SHOWMESH_HTTP_ADDR=" + httpAddr,
		"SHOWMESH_MQTT_BROKER=" + broker,
		"SHOWMESH_MQTT_CLIENT_ID=" + cfg.clientID,
		"SHOWMESH_MQTT_USERNAME=" + mqttUsername,
		"SHOWMESH_MQTT_PASSWORD=" + mqttPassword,
		"SHOWMESH_DATA_DIR=" + cfg.dataDir,
		"SHOWMESH_LOG_LEVEL=debug",
	}
	// Step 3 review finding 4.12's own tests exposed this gap while being
	// built: envStalenessOverride's doc comment above says it "must
	// already be set in the process environment before go test starts,
	// because internal/coordinator/inventory reads it exactly once, at
	// package initialization" — true for that package's own init, but
	// that package now runs inside a SEPARATE showmesh-coordinator
	// SUBPROCESS with its own explicit, non-inherited environment (this
	// list), not inside this test binary's process. Every coordinator
	// subprocess this harness ever started was silently running with the
	// real 30s production StalenessWindow regardless of
	// SHOWMESH_TEST_STALENESS_WINDOW, because nothing forwarded it here —
	// invisible to every prior test in this package, since none of them
	// depended on the COORDINATOR's own staleness computation happening
	// quickly (waitOffline's bound already pads generously enough to cover
	// the full 30s default either way). Forwarded here the same way
	// startAgent already forwards envHeartbeatOverride to the agent
	// subprocess, so a test that actually needs a fast staleness window on
	// the coordinator side does not have to wait out the real 30 seconds.
	if raw := os.Getenv(envStalenessOverride); raw != "" {
		env = append(env, envStalenessOverride+"="+raw)
	}
	if cfg.closeReads {
		env = append(env, "SHOWMESH_API_CLOSE_READS=true")
	}
	if cfg.fppEndpoints != "" {
		env = append(env, "SHOWMESH_FPP_ENDPOINTS="+cfg.fppEndpoints)
	}
	if cfg.streamSubscriberBuffer > 0 {
		env = append(env, fmt.Sprintf("SHOWMESH_TEST_STREAM_SUBSCRIBER_BUFFER=%d", cfg.streamSubscriberBuffer))
	}
	if cfg.assetDir != "" {
		env = append(env, "SHOWMESH_ASSET_DIR="+cfg.assetDir)
	}
	if cfg.assetContentBaseURL != "" {
		env = append(env, "SHOWMESH_ASSET_CONTENT_BASE_URL="+cfg.assetContentBaseURL)
	}
	if cfg.assetSyncInterval > 0 {
		env = append(env, "SHOWMESH_ASSET_SYNC_INTERVAL="+cfg.assetSyncInterval.String())
	} else if raw := os.Getenv(envAssetSyncInterval); raw != "" {
		env = append(env, envAssetSyncInterval+"="+raw)
	}
	if cfg.assetInventoryInterval > 0 {
		env = append(env, "SHOWMESH_ASSET_INVENTORY_INTERVAL="+cfg.assetInventoryInterval.String())
	} else if raw := os.Getenv(envAssetInventoryInterval); raw != "" {
		env = append(env, envAssetInventoryInterval+"="+raw)
	}
	if cfg.assetMaxUploadBytes > 0 {
		env = append(env, fmt.Sprintf("SHOWMESH_ASSET_MAX_UPLOAD_BYTES=%d", cfg.assetMaxUploadBytes))
	}

	cmd := exec.Command(coordinatorBinPath)
	cmd.Env = env

	logs := &syncBuffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start coordinator subprocess (client id %s): %v", cfg.clientID, err)
	}

	tc := &testCoordinator{
		t: t, cmd: cmd, httpAddr: httpAddr, token: cfg.bearerToken,
		logs: logs, client: &http.Client{Timeout: 5 * time.Second},
		waitDone: make(chan struct{}),
	}
	go func() {
		tc.waitErr = cmd.Wait()
		close(tc.waitDone)
	}()

	t.Cleanup(func() {
		tc.shutdown()
		if t.Failed() {
			t.Logf("coordinator (client id %s, pid %d) combined output:\n%s", cfg.clientID, cmd.Process.Pid, logs.String())
		}
	})

	tc.waitForHealthz(t, 15*time.Second)
	return tc
}

// waitForHealthz blocks until this coordinator's /healthz answers 200, or
// fails t (dumping the subprocess's own output — the fastest way to see
// why a bind or startup failed) once timeout elapses.
func (tc *testCoordinator) waitForHealthz(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := tc.client.Get("http://" + tc.httpAddr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-tc.waitDone:
			t.Fatalf("coordinator subprocess exited before /healthz ever answered (err=%v); output:\n%s", tc.waitErr, tc.logs.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("coordinator /healthz did not answer within %s; output:\n%s", timeout, tc.logs.String())
}

// shutdown sends SIGTERM and waits for the coordinator subprocess to exit
// cleanly (mirroring internal/coordinator.Run's own graceful-shutdown
// path), falling back to SIGKILL if it does not exit within a generous
// bound — a hung shutdown is a test failure to report, not a hang to leave
// the test suite stuck on. Safe to call more than once (idempotent), so
// both an explicit mid-test shutdown() call (the restart tests) and the
// t.Cleanup registered by startCoordinatorWithConfig can both call it
// without double-signaling an already-exited process.
func (tc *testCoordinator) shutdown() {
	tc.mu.Lock()
	if tc.done {
		tc.mu.Unlock()
		return
	}
	tc.done = true
	tc.mu.Unlock()

	select {
	case <-tc.waitDone:
		return // already exited on its own
	default:
	}

	_ = tc.cmd.Process.Signal(syscall.SIGTERM)
	// 15s, deliberately wider than the coordinator's own 10s shutdownCtx
	// (internal/coordinator/coordinator.go): with a macro run in flight,
	// shutdown legitimately runs that full budget (macro.Executor.Stop
	// holds most of it, since a run is uncancellable by design, and the
	// broker disconnects hold a reserved slice) before the process exits.
	// This bound was 10s, equal to the server's, so the first test to tear
	// down mid-run SIGKILLed a coordinator that was 60ms from a clean exit:
	// two timeouts on opposite sides of one contract are a single decision.
	select {
	case <-tc.waitDone:
	case <-time.After(15 * time.Second):
		tc.t.Errorf("coordinator subprocess did not exit within 15s of SIGTERM; sending SIGKILL (this is a shutdown-ordering defect, not expected harness behavior)")
		_ = tc.cmd.Process.Kill()
		<-tc.waitDone
	}
}

// --- Observing the coordinator through /api/v1, the way any real client would ---

// url joins this coordinator's base HTTP address with path.
func (tc *testCoordinator) url(path string) string {
	return "http://" + tc.httpAddr + path
}

// getRaw issues an authenticated GET (when a token is configured) against
// path and returns the status code and raw response body. Every other
// helper in this file is built on this one, so a test that needs to assert
// against literal JSON bytes (contract section 1's standing rule) —
// assertRawHeartbeatIsUnknownAge is the concrete case — can call this
// directly instead of decoding into any struct at all.
func (tc *testCoordinator) getRaw(t *testing.T, path string) (status int, body []byte) {
	t.Helper()
	headers := map[string]string{}
	if tc.token != "" {
		headers["Authorization"] = "Bearer " + tc.token
	}
	return tc.getRawWithHeaders(t, path, headers)
}

// getRawWithHeaders is getRaw with full control over request headers —
// used by tests that need to omit, override, or add headers getRaw always
// sets (a wrong/missing bearer token, an explicit ShowMesh-API-Version).
func (tc *testCoordinator) getRawWithHeaders(t *testing.T, path string, headers map[string]string) (status int, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, tc.url(path), nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", path, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := tc.client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: read body: %v", path, err)
	}
	return resp.StatusCode, b
}

// ready reports whether /readyz currently answers 200. Used in place of
// the pre-Step-3 harness's direct access to a live *broker.BrokerManager
// (coord.bm.State().Connected): that handle no longer exists once the
// coordinator is a subprocess, and /readyz is the coordinator's own,
// intentional way of saying the same thing to any external observer — its
// Ready verdict is exactly readiness.Aggregate{brokerManager,
// store}.Readiness() (internal/coordinator/coordinator.go), so this is not
// a weaker substitute, it is the same fact observed the way ADR-014 says
// every fact about this process must be observable.
func (tc *testCoordinator) ready(t *testing.T) bool {
	t.Helper()
	status, _ := tc.getRaw(t, "/readyz")
	return status == http.StatusOK
}

// findNode fetches GET /api/v1/nodes/{nodeId} and decodes it into
// [v1.Node]. ok is false on a 404 (node not yet in inventory); any other
// non-2xx status fails t outright, since every test in this package that
// calls this expects the coordinator to be answering normally.
func (tc *testCoordinator) findNode(t *testing.T, nodeID string) (v1.Node, bool) {
	t.Helper()
	status, body := tc.getRaw(t, "/api/v1/nodes/"+url.PathEscape(nodeID))
	if status == http.StatusNotFound {
		return v1.Node{}, false
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/nodes/%s: status %d, body: %s", nodeID, status, body)
	}
	var resp v1.NodeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("GET /api/v1/nodes/%s: decode response: %v; body: %s", nodeID, err, body)
	}
	return resp.Node, true
}

// snapshot fetches GET /api/v1/snapshot and decodes it into [v1.Snapshot].
func (tc *testCoordinator) snapshot(t *testing.T) v1.Snapshot {
	t.Helper()
	status, body := tc.getRaw(t, "/api/v1/snapshot")
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/snapshot: status %d, body: %s", status, body)
	}
	var snap v1.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("GET /api/v1/snapshot: decode response: %v; body: %s", err, body)
	}
	return snap
}

// parseObservedAt parses a [v1.Evidence.ObservedAt] pointer (RFC 3339, or
// nil for unknown age) into a *time.Time, failing t on a value that fails
// to parse — every test that calls this already knows, from context,
// whether nil is an expected outcome.
func parseObservedAt(t *testing.T, s *string) *time.Time {
	t.Helper()
	if s == nil {
		return nil
	}
	tm, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		t.Fatalf("parse observedAt %q: %v", *s, err)
	}
	return &tm
}

// waitFor polls cond every interval until it returns true or timeout
// elapses, failing t with msg on timeout. This is the only synchronization
// primitive this package uses to observe coordinator state — never a fixed
// sleep — per the Task E spec.
func waitFor(t *testing.T, timeout, interval time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for: %s", timeout, msg)
		}
		time.Sleep(interval)
	}
}

// provisionMu serializes every call to provisionBrokerCredential: each one
// runs `docker exec ... mosquitto_passwd -b ...` against the SAME password
// file inside the broker container, which reads-modifies-writes the whole
// file, so two concurrent invocations could race and lose an entry. This
// package's tests do not currently call t.Parallel(), so this is
// defense-in-depth against that changing later rather than a fix for an
// observed race.
var provisionMu sync.Mutex

// provisionAgentCredential is provisionBrokerCredential for the common
// case: a credential whose username equals the agent's own node id,
// matching an explicit generated ACL block for that node — which is what
// makes an agent's own credential authorize it for its own node's topics
// and nothing else.
func provisionAgentCredential(t *testing.T, nodeID string) (username, password string) {
	t.Helper()
	return provisionBrokerCredential(t, nodeID)
}

// provisionBrokerCredential adds a broker credential for username to the
// running Mosquitto test container's password file, reloads the broker via
// SIGHUP so the addition takes effect (ADR-024 decision 10: Mosquitto only
// re-reads passwd/acl.conf on SIGHUP), and confirms the credential is
// actually usable before returning — via a real, throwaway MQTT CONNECT
// (mqttCredentialWorks), not a fixed sleep — so callers never race the
// reload. username need not be a valid ShowMesh node id: broker_auth_test.go
// uses this directly (not through provisionAgentCredential) to provision
// the "healthcheck" and "fpp" role usernames mosquitto/acl.conf grants
// broader per-user access to, and to deliberately provision a credential
// under one username while an agent subprocess is told to use a DIFFERENT
// node id, to prove the ACL's per-node pattern rules actually bind
// publish access to the authenticated username rather than to whatever
// node id the client claims in its own topics.
//
// Requires envMosquittoContainer to be set, exactly like restartBroker:
// `docker exec` needs a container to exec into, and
// scripts/test-integration.sh (the `make test-integration` path) always
// sets it. A developer running `go test -tags=integration` directly
// against their own already-running broker, without going through that
// script, gets a clear skip here rather than every agent-starting test
// failing deep inside a subprocess launch.
func provisionBrokerCredential(t *testing.T, username string) (gotUsername, password string) {
	t.Helper()
	if mosquittoContainer == "" {
		t.Skipf(
			"%s is not set, so this harness has no way to add a broker credential for %q (the broker now requires one — ADR-024 decision 10); "+
				"run via `make test-integration` (which sets it), or export it yourself pointing at a running eclipse-mosquitto container whose password file you can `docker exec` into",
			envMosquittoContainer, username)
	}

	provisionMu.Lock()
	defer provisionMu.Unlock()

	password = fmt.Sprintf("test-%s-%s", username, uniqueSuffix())

	cmd := exec.Command("docker", "exec", mosquittoContainer,
		"mosquitto_passwd", "-b", "/mosquitto/config/passwd", username, password)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker exec %s mosquitto_passwd -b ... %s: %v\n%s", mosquittoContainer, username, err, out)
	}

	// Fixed roles receive their complete ACL only from the committed base.
	// Every other credential receives a narrow, explicit agent block. Global
	// Mosquitto `pattern` rules would also apply these grants to fixed roles.
	switch username {
	case "coordinator", "fpp", "healthcheck":
		// No agent ACL block for a fixed role.
	default:
		acl := fmt.Sprintf(`

# Provisioned ShowMesh agent: %s
user %s
topic write showmesh/nodes/%s/hello
topic write showmesh/nodes/%s/lwt
topic write showmesh/nodes/%s/observed/#
topic write showmesh/nodes/%s/result/+
topic read  showmesh/nodes/%s/cmd
`, username, username, username, username, username, username, username)
		appendACL := exec.Command("docker", "exec", "-i", mosquittoContainer,
			"sh", "-ec", "cat >> /mosquitto/config/acl.generated.conf")
		appendACL.Stdin = strings.NewReader(acl)
		if out, err := appendACL.CombinedOutput(); err != nil {
			t.Fatalf("docker exec %s append explicit agent ACL for %q: %v\n%s", mosquittoContainer, username, err, out)
		}
	}

	reload := exec.Command("docker", "kill", "--signal=HUP", mosquittoContainer)
	if out, err := reload.CombinedOutput(); err != nil {
		t.Fatalf("docker kill --signal=HUP %s: %v\n%s", mosquittoContainer, err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if mqttCredentialWorks(ctx, brokerURL, username, password) {
			return username, password
		}
		if time.Now().After(deadline) {
			t.Fatalf("credential for %q was added and the broker was reloaded, but never became usable within 5s", username)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// mqttCredentialWorks attempts one throwaway MQTT CONNECT against
// brokerURL with username/password, and reports whether the broker
// accepted it (CONNACK success). It speaks just enough MQTT to answer that
// one question — no subscriptions, no publishes — and always disconnects
// cleanly (or simply drops the TCP connection on failure), so it never
// leaves a session behind for a later test to trip over. Used by
// provisionAgentCredential to confirm a just-added credential is actually
// live rather than assuming a fixed delay was enough.
func mqttCredentialWorks(ctx context.Context, brokerURL, username, password string) bool {
	u, err := url.Parse(brokerURL)
	if err != nil {
		return false
	}

	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", u.Host)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	cli := paho.NewClient(paho.ClientConfig{Conn: conn})
	connCtx, cancelConn := context.WithTimeout(ctx, 2*time.Second)
	defer cancelConn()
	ack, err := cli.Connect(connCtx, &paho.Connect{
		ClientID:     "showmesh-test-credcheck-" + uniqueSuffix(),
		UsernameFlag: true,
		Username:     username,
		PasswordFlag: true,
		Password:     []byte(password),
		KeepAlive:    30,
		CleanStart:   true,
	})
	if err != nil || ack == nil || ack.ReasonCode != 0 {
		return false
	}
	_ = cli.Disconnect(&paho.Disconnect{ReasonCode: 0})
	return true
}

// restartBroker docker-restarts the Mosquitto container named by
// envMosquittoContainer. Callers must check mosquittoContainer != "" (or
// just call this, which skips t itself) before relying on it.
func restartBroker(t *testing.T) {
	t.Helper()
	if mosquittoContainer == "" {
		t.Skipf(
			"%s is not set, so this harness has no way to restart the broker container; "+
				"run via `make test-integration` (which sets it) or export it yourself pointing at a running eclipse-mosquitto container",
			envMosquittoContainer)
	}
	cmd := exec.Command("docker", "restart", mosquittoContainer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker restart %s: %v\n%s", mosquittoContainer, err, out)
	}
}

// stopBroker and startBroker docker-stop/docker-start the same Mosquitto
// container restartBroker restarts, split into two calls rather than one
// restart so a test can hold the broker down for a real interval and
// observe what happens WHILE it is gone, not only that things resume
// after. Same skip contract as restartBroker: no envMosquittoContainer,
// no broker control, an explicit skip rather than a silent no-op.
func stopBroker(t *testing.T) {
	t.Helper()
	if mosquittoContainer == "" {
		t.Skipf(
			"%s is not set, so this harness has no way to stop the broker container; "+
				"run via `make test-integration` (which sets it) or export it yourself pointing at a running eclipse-mosquitto container",
			envMosquittoContainer)
	}
	cmd := exec.Command("docker", "stop", mosquittoContainer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker stop %s: %v\n%s", mosquittoContainer, err, out)
	}
}

func startBroker(t *testing.T) {
	t.Helper()
	if mosquittoContainer == "" {
		t.Skipf("%s is not set, so this harness has no way to start the broker container", envMosquittoContainer)
	}
	cmd := exec.Command("docker", "start", mosquittoContainer)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker start %s: %v\n%s", mosquittoContainer, err, out)
	}
}
