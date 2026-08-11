//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Environment variables this package reads. All are optional; every
// default matches what scripts/test-integration.sh (the `make
// test-integration` target) exports, so `go test -tags=integration
// ./test/integration/...` also works unmodified against a broker started
// that way.
const (
	// envBrokerURL is the MQTT broker URL both the agent subprocess and the
	// in-process coordinator connect to. Default matches
	// defaultBrokerURL below, a non-production port chosen so this suite
	// does not collide with a developer's already-running reference
	// deployment (deploy/docker-compose.yml's bundled broker listens on
	// 1883).
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

	// envStalenessOverride is NOT read directly by this package — it must
	// already be set in the process environment before `go test` starts,
	// because internal/coordinator/inventory reads it exactly once, at
	// package initialization (see that package's
	// envStalenessWindowOverride). It is read here only so tests can size
	// their own wait bounds against whatever value is actually in effect;
	// see testStalenessWindow below.
	envStalenessOverride = "SHOWMESH_TEST_STALENESS_WINDOW"
)

const defaultBrokerURL = "tcp://localhost:11883"

var (
	// brokerURL, brokerReachable, mosquittoContainer, and agentBinPath are
	// resolved once in TestMain and read (never written) by every test
	// below.
	brokerURL          string
	brokerReachable    bool
	mosquittoContainer string
	agentBinPath       string

	// testHeartbeatInterval and testStalenessWindow mirror, in this test
	// binary, the same environment variables the agent subprocess and the
	// in-process coordinator package resolve for themselves — see
	// envHeartbeatOverride and envStalenessOverride above. They exist only
	// so tests can size wait bounds proportionally to whatever cadence is
	// actually in effect, without hardcoding the production 10s/30s values
	// this suite deliberately never runs with.
	testHeartbeatInterval time.Duration
	testStalenessWindow   time.Duration
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
// probeBroker), and builds the showmesh-agent binary once for every test in
// this package to exec. Per the Task E spec, criterion 2 (an unclean kill)
// can only be honestly proven against a real subprocess, so every test
// here execs the same binary rather than each building its own copy.
func TestMain(m *testing.M) {
	brokerURL = os.Getenv(envBrokerURL)
	if brokerURL == "" {
		brokerURL = defaultBrokerURL
	}
	mosquittoContainer = os.Getenv(envMosquittoContainer)

	testHeartbeatInterval = parseDurationEnv(envHeartbeatOverride, 10*time.Second)
	testStalenessWindow = parseDurationEnv(envStalenessOverride, 30*time.Second)

	brokerReachable = probeBroker(brokerURL, 2*time.Second)

	var cleanupBin func()
	code := func() int {
		if !brokerReachable {
			fmt.Fprintf(os.Stderr,
				"integration: no MQTT broker reachable at %s; every test in this package will be skipped.\n"+
					"Run `make test-integration` (starts one for you), or start a broker per\n"+
					"deploy/mosquitto/mosquitto.conf and set %s.\n",
				brokerURL, envBrokerURL)
			return m.Run() // every test skips itself via requireBroker
		}

		bin, cleanup, err := buildAgentBinary()
		if err != nil {
			fmt.Fprintf(os.Stderr, "integration: failed to build the showmesh-agent binary: %v\n", err)
			return 1
		}
		agentBinPath = bin
		cleanupBin = cleanup

		return m.Run()
	}()

	if cleanupBin != nil {
		cleanupBin()
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
		t.Skipf("no MQTT broker reachable at %s (set %s, or run `make test-integration`)", brokerURL, envBrokerURL)
	}
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

// buildAgentBinary builds cmd/showmesh-agent once into a fresh temp
// directory and returns its path and a cleanup func that removes it.
func buildAgentBinary() (path string, cleanup func(), err error) {
	root, err := moduleRoot()
	if err != nil {
		return "", nil, err
	}

	dir, err := os.MkdirTemp("", "showmesh-agent-bin-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	bin := filepath.Join(dir, "showmesh-agent")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/showmesh-agent")
	cmd.Dir = root
	cmd.Env = os.Environ() // the build itself needs a normal Go toolchain environment

	out, err := cmd.CombinedOutput()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("go build ./cmd/showmesh-agent: %w\n%s", err, out)
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

	env := []string{
		"PATH=" + os.Getenv("PATH"), // harmless if unused; costs nothing to include
		"SHOWMESH_NODE_ID=" + cfg.nodeID,
		"SHOWMESH_MQTT_BROKER=" + brokerURL,
		"SHOWMESH_MQTT_CLIENT_ID=showmesh-agent-test-" + cfg.nodeID,
		"SHOWMESH_LOG_LEVEL=debug",
	}
	if raw := os.Getenv(envHeartbeatOverride); raw != "" {
		env = append(env, envHeartbeatOverride+"="+raw)
	}
	if cfg.capabilities != "" {
		env = append(env, "SHOWMESH_NODE_CAPABILITIES="+cfg.capabilities)
	}
	if cfg.label != "" {
		env = append(env, "SHOWMESH_NODE_LABEL="+cfg.label)
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

// stopIfRunning kills the agent if it is still alive and always joins the
// Wait goroutine, so cleanup never leaks either a process or a goroutine
// regardless of how a test ended.
func (a *testAgent) stopIfRunning() {
	select {
	case <-a.waitDone:
		return
	default:
	}
	_ = a.cmd.Process.Kill()
	<-a.waitDone
}

// testCoordinator wires the coordinator's store, inventory, and broker
// packages together in-process — see the package doc comment for why this
// is not the shipped showmesh-coordinator binary.
type testCoordinator struct {
	logger *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	st     *store.Store
	inv    *inventory.Manager
	bm     *broker.BrokerManager

	mu   sync.Mutex
	done bool
}

// startCoordinator opens (creating if necessary) a SQLite store at dataDir
// and connects to the test broker with clientID. Passing the same dataDir
// and clientID to a second call after shutdown() is exactly
// TestCoordinatorRestartRestoresInventoryFromRetainedTopics's "teardown and
// rebuild against the same database and broker".
func startCoordinator(t *testing.T, dataDir, clientID string) *testCoordinator {
	t.Helper()
	requireBroker(t)

	logger := testLogger()
	ctx, cancel := context.WithCancel(context.Background())

	st, err := store.Open(ctx, dataDir, logger)
	if err != nil {
		cancel()
		t.Fatalf("open store at %s: %v", dataDir, err)
	}

	inv := inventory.New(st, logger)

	cfg := config.Config{
		MQTTBroker:   brokerURL,
		MQTTClientID: clientID,
	}
	bm, err := broker.NewBrokerManager(ctx, cfg, logger, inv.Subscriptions(), inv.HandleMessage)
	if err != nil {
		_ = st.Close()
		cancel()
		t.Fatalf("start broker manager for %s: %v", clientID, err)
	}

	tc := &testCoordinator{logger: logger, ctx: ctx, cancel: cancel, st: st, inv: inv, bm: bm}
	t.Cleanup(tc.shutdown)
	return tc
}

// shutdown disconnects from the broker and closes the store, mirroring
// internal/coordinator.Run's own shutdown ordering. Safe to call more than
// once (idempotent), so both an explicit mid-test shutdown() call (for the
// restart tests) and the t.Cleanup registered by startCoordinator can both
// call it without double-closing anything.
func (tc *testCoordinator) shutdown() {
	tc.mu.Lock()
	if tc.done {
		tc.mu.Unlock()
		return
	}
	tc.done = true
	tc.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tc.cancel() // stops BrokerManager's probe goroutine; see its Disconnect doc comment
	_ = tc.bm.Disconnect(ctx)
	_ = tc.st.Close()
}

// snapshot returns every node inventory currently knows about, evaluated at
// the current wall-clock time — the same call shape
// internal/coordinator/inventory.Manager.Snapshot exposes to real callers.
func (tc *testCoordinator) snapshot(t *testing.T) []inventory.NodeView {
	t.Helper()
	views, err := tc.inv.Snapshot(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return views
}

// findNode returns nodeID's current view, if inventory has ever seen it.
func (tc *testCoordinator) findNode(t *testing.T, nodeID string) (inventory.NodeView, bool) {
	t.Helper()
	for _, v := range tc.snapshot(t) {
		if v.NodeID == nodeID {
			return v, true
		}
	}
	return inventory.NodeView{}, false
}

// testLogger discards everything by default (matching the internal
// packages' own testLogger convention); under `go test -v`, it writes to
// stderr instead, which is often the fastest way to see what actually
// happened when one of these tests fails against a real broker.
func testLogger() *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
