//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
)

// This file is Track I seam I1's own integration bench: two real
// showmesh-agent subprocesses, a real showmesh-coordinator subprocess,
// and one real `ptp4l` (software timestamping) — see
// docs/build/TRACK-I-clock-and-sync.md and internal/agent/clock's own
// package doc comment for the design this proves. Built in the shape
// TRACK-I-clock-and-sync.md's own seam row calls for: two agent
// subprocesses, not a Docker network of agent containers, plus one
// ptp4l process the test harness itself owns and supervises.
//
// Both agents run "external" providers pointed at the SAME ptp4l
// instance's read-only management socket: this sandboxed dev VM's
// loopback interface does not deliver PTP multicast traffic between two
// independent ptp4l processes (each becomes its own free-running
// grandmaster instead of one following the other — confirmed running the
// real binary here while building this seam), so a genuine two-node PTP
// hierarchy is not obtainable in this environment. Both nodes locking to
// the SAME externally-owned instance and reporting the SAME grandmaster
// identity and domain is still real, independently-observed evidence
// that the provider abstraction and the coordinator's telemetry pipeline
// work end to end — it is the multicast delivery between two SEPARATE
// ptp4l instances that this environment cannot exercise, not anything
// this seam's own code is responsible for.

// depPTP names the ptp4l/pmc dependency this file's own tests need,
// checked in addition to (never instead of) [skipOrFatalDependency]'s
// existing envRequireTestDeps contract: ptp4l's raw PTP sockets and pmc's
// own UDS client bind both need root in every environment this seam was
// built and tested against, which envRequireTestDeps alone cannot
// express (it is a broker/fppd presence flag, not a privilege level), so
// this is checked directly rather than added to that list.
const depPTP = "ptp"

// clockBenchPTP4L is a full path for the identical reason
// internal/agent/clock's own ptp4lBinary/pmcBinary vars are: Debian's
// linuxptp package installs both under /usr/sbin, off a non-interactive
// shell's default PATH.
const (
	clockBenchPTP4L = "/usr/sbin/ptp4l"
	clockBenchPMC   = "/usr/sbin/pmc"
)

// requireClockBench skips t (or fails it, under SHOWMESH_REQUIRE_TEST_DEPS=...,ptp,...)
// when ptp4l/pmc are not installed, or this process is not running as
// root — both raw PTP sockets and pmc's own UDS client bind need it. This
// is exactly the "harness may only be held to what it actually starts"
// rule LESSONS.md already states: scripts/test-integration.sh does not
// declare "ptp" in SHOWMESH_REQUIRE_TEST_DEPS (it starts a broker
// container, never ptp4l/root), so this stays a clean skip under `make
// test-integration` and under CI unless that harness is changed to
// supply both, exactly as this seam's own build task allows.
func requireClockBench(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(clockBenchPTP4L); err != nil {
		skipOrFatalDependency(t, depPTP, "ptp4l not found at %s: %v", clockBenchPTP4L, err)
	}
	if _, err := os.Stat(clockBenchPMC); err != nil {
		skipOrFatalDependency(t, depPTP, "pmc not found at %s: %v", clockBenchPMC, err)
	}
	if os.Geteuid() != 0 {
		skipOrFatalDependency(t, depPTP, "this bench needs root (ptp4l's raw PTP sockets and pmc's own UDS client bind both require it); run `sudo -E go test -tags=integration -run TestClockProviderBench ./test/integration/...`")
	}
}

// benchPTP4L supervises one real ptp4l process the test owns end to end:
// started, killed, and restarted against the SAME config and UDS socket
// paths, so a test can exercise holdover/failed/recovery against a single
// stable identity (RES-019 section 9).
type benchPTP4L struct {
	t        *testing.T
	confPath string
	roSocket string
	rwSocket string
	iface    string
	domain   int
	logs     *syncBuffer

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// newBenchPTP4L writes iface/domain's config once (RES-019 section 1: /run,
// never /tmp — this seam's own bench found a custom socket path under
// /tmp undeliverable to pmc in this sandboxed VM even with a correctly
// matched domain, while the identical path under /run worked) but does
// NOT start the process — call .start().
func newBenchPTP4L(t *testing.T, iface string, domain int) *benchPTP4L {
	t.Helper()
	dir, err := os.MkdirTemp("/run", "showmesh-clock-bench-")
	if err != nil {
		t.Fatalf("create ptp4l run dir under /run: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	b := &benchPTP4L{
		t: t, iface: iface, domain: domain,
		confPath: dir + "/ptp4l.conf",
		roSocket: dir + "/ptp4l-ro",
		rwSocket: dir + "/ptp4l-rw",
		logs:     &syncBuffer{},
	}
	conf := fmt.Sprintf("[global]\ndomainNumber %d\ntime_stamping software\nuds_address %s\nuds_ro_address %s\n",
		domain, b.rwSocket, b.roSocket)
	if err := os.WriteFile(b.confPath, []byte(conf), 0o644); err != nil {
		t.Fatalf("write ptp4l config: %v", err)
	}
	// Whatever process is running when the test ends must not outlive it —
	// scenario 3 restarts ptp4l and this bench never stops it again on its
	// own success path, so without this a passing test still leaks a real
	// root-owned ptp4l process per run.
	t.Cleanup(b.stop)
	return b
}

// start launches ptp4l against this instance's own config. Fails t if one
// is already running under this handle.
func (b *benchPTP4L) start() {
	b.t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd != nil {
		b.t.Fatalf("benchPTP4L.start: already running (pid %d)", b.cmd.Process.Pid)
	}

	cmd := exec.Command(clockBenchPTP4L, "-f", b.confPath, "-i", b.iface, "-m")
	cmd.Stdout = b.logs
	cmd.Stderr = b.logs
	// Own process group, matching startAgent's identical reasoning: a
	// harness-level kill of the TEST process itself must not orphan this
	// ptp4l, and stop() below kills -pid to take the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		b.t.Fatalf("start ptp4l: %v", err)
	}
	b.cmd = cmd
	b.done = make(chan struct{})
	done := b.done
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
}

// stop kills the current ptp4l process (if any) and waits for it to exit.
// A no-op if nothing is currently running — RES-019 section 9's "ptp4l
// owner stopping it" is exercised by calling this while both agents are
// locked, then start() again for the recovery half.
func (b *benchPTP4L) stop() {
	b.mu.Lock()
	cmd, done := b.cmd, b.done
	b.cmd, b.done = nil, nil
	b.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	// A stopped ptp4l takes its UDS sockets with it; remove any leftover
	// socket special files so a subsequent start() does not confuse a
	// stale inode for a live one (pmc/os.Stat both key on path presence).
	_ = os.Remove(b.rwSocket)
	_ = os.Remove(b.roSocket)
}

// TestClockProviderBench is Track I seam I1's four-scenario acceptance
// bench, run as one sequential test against one shared harness (starting
// three real agent processes, one real coordinator, and one real ptp4l
// process is expensive enough that splitting this into four independent
// Test functions would multiply that cost for no independent value — the
// scenarios are inherently sequential anyway: lock, then lose it, then
// recover it).
func TestClockProviderBench(t *testing.T) {
	requireBroker(t)
	requireClockBench(t)

	domain := 100 + int(time.Now().UnixNano()%50) // 100-149: distinct from any other domain this suite or a developer's own ptp4l might use
	ptp := newBenchPTP4L(t, "lo", domain)

	dataDir := t.TempDir()
	adminToken := createAdminAndIssueToken(t, dataDir, "admin-1", "a-strong-password-1")
	coord := startCoordinatorWithConfig(t, coordinatorConfig{
		dataDir: dataDir, clientID: "showmesh-coordinator-test-clock-" + uniqueSuffix(), bearerToken: adminToken,
	})

	nodeA := "clock-a-" + uniqueSuffix()
	nodeB := "clock-b-" + uniqueSuffix()
	agentA := startAgent(t, agentConfig{nodeID: nodeA, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})
	agentB := startAgent(t, agentConfig{nodeID: nodeB, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})
	_ = agentA
	_ = agentB

	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		_, ok := coord.findNode(t, nodeA)
		return ok
	}, "node "+nodeA+" to appear in inventory")
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		_, ok := coord.findNode(t, nodeB)
		return ok
	}, "node "+nodeB+" to appear in inventory")

	// Configure both nodes as EXTERNAL providers against the same ptp4l
	// instance's read-only socket, short holdover so scenario 2 does not
	// need the 60s production default.
	setExternalClockConfig := func(nodeID string) {
		mustCtl(t, coord, adminToken, []string{
			"node-clock", "set",
			"--provider", "external", "--interface", "lo", "--domain", strconv.Itoa(domain),
			"--external-uds-address", ptp.roSocket, "--holdover-limit-seconds", "3",
		}, nodeID)
	}
	setExternalClockConfig(nodeA)
	setExternalClockConfig(nodeB)

	// --- Scenario 1: ptp4l running, both agents lock to the same grandmaster ---
	ptp.start()

	var gmA, gmB string
	waitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		na, ok := coord.findNode(t, nodeA)
		if !ok || !clockSignalEquals(na, "node.clock.ptp.state", "locked") {
			return false
		}
		nb, ok := coord.findNode(t, nodeB)
		if !ok || !clockSignalEquals(nb, "node.clock.ptp.state", "locked") {
			return false
		}
		gmA, _ = clockSignalString(na, "node.clock.ptp.grandmaster_identity")
		gmB, _ = clockSignalString(nb, "node.clock.ptp.grandmaster_identity")
		return gmA != "" && gmB != ""
	}, fmt.Sprintf("both %s and %s to report locked with a grandmaster identity (ptp4l log:\n%s)", nodeA, nodeB, ptp.logs.String()))

	if gmA != gmB {
		t.Fatalf("grandmaster identity mismatch: %s reports %q, %s reports %q", nodeA, gmA, nodeB, gmB)
	}
	na, _ := coord.findNode(t, nodeA)
	nb, _ := coord.findNode(t, nodeB)
	domA, _ := clockSignalFloat(na, "node.clock.ptp.domain")
	domB, _ := clockSignalFloat(nb, "node.clock.ptp.domain")
	if int(domA) != domain || int(domB) != domain {
		t.Fatalf("domain mismatch: %s reports %v, %s reports %v, want %d", nodeA, domA, nodeB, domB, domain)
	}
	t.Logf("scenario 1 OK: both nodes locked to grandmaster %s, domain %d", gmA, domain)

	// --- Scenario 2: stopping ptp4l moves both through holdover to failed ---
	ptp.stop()

	waitFor(t, 15*time.Second, 300*time.Millisecond, func() bool {
		na, ok := coord.findNode(t, nodeA)
		if !ok {
			return false
		}
		nb, ok := coord.findNode(t, nodeB)
		if !ok {
			return false
		}
		return clockSignalIsOneOf(na, "node.clock.ptp.state", "holdover", "failed", "unsynchronized") &&
			clockSignalIsOneOf(nb, "node.clock.ptp.state", "holdover", "failed", "unsynchronized")
	}, "both nodes to leave locked after ptp4l stops")

	// The holdover limit is 3s; well within this wait, both must have
	// progressed all the way to failed (the socket is gone, not merely a
	// missed poll) or unsynchronized (holdover limit exceeded) — never
	// stuck reporting locked.
	waitFor(t, 15*time.Second, 300*time.Millisecond, func() bool {
		na, ok := coord.findNode(t, nodeA)
		if !ok {
			return false
		}
		nb, ok := coord.findNode(t, nodeB)
		if !ok {
			return false
		}
		return clockSignalIsOneOf(na, "node.clock.ptp.state", "failed", "unsynchronized") &&
			clockSignalIsOneOf(nb, "node.clock.ptp.state", "failed", "unsynchronized")
	}, "both nodes to reach failed or unsynchronized after the holdover limit elapses")
	t.Log("scenario 2 OK: both nodes left locked after ptp4l stopped")

	// --- Scenario 3: restarting ptp4l returns both to locked ---
	ptp.start()

	waitFor(t, 30*time.Second, 500*time.Millisecond, func() bool {
		na, ok := coord.findNode(t, nodeA)
		if !ok || !clockSignalEquals(na, "node.clock.ptp.state", "locked") {
			return false
		}
		nb, ok := coord.findNode(t, nodeB)
		return ok && clockSignalEquals(nb, "node.clock.ptp.state", "locked")
	}, fmt.Sprintf("both nodes to return to locked after ptp4l restarts (ptp4l log:\n%s)", ptp.logs.String()))
	t.Log("scenario 3 OK: both nodes recovered to locked after ptp4l restarted")

	// --- Scenario 4: a second managed provider on the same interface and
	// domain refuses to start, and says why ---
	nodeC := "clock-c-" + uniqueSuffix()
	agentC := startAgent(t, agentConfig{nodeID: nodeC, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		_, ok := coord.findNode(t, nodeC)
		return ok
	}, "node "+nodeC+" to appear in inventory")

	mustCtl(t, coord, adminToken, []string{
		"node-clock", "set",
		"--provider", "managed", "--interface", "lo", "--domain", strconv.Itoa(domain),
	}, nodeC)

	// The managed provider's ownership pre-check refuses because ptp.start()
	// above still owns "lo" (a live process, found via /proc's own process
	// table scan — see internal/agent/clock.findRunningPTP4L). Observed via
	// agentC's own log: internal/agent/clock.Manager.SetConfig logs the
	// refusal reason via this node's logger before returning it as the
	// node.clock.configure command's own failure (RES-019 section 5.3:
	// "says why").
	waitFor(t, 15*time.Second, 300*time.Millisecond, func() bool {
		return strings.Contains(agentC.logs.String(), "node.clock configuration rejected")
	}, fmt.Sprintf("agent %s's log to report the managed provider's refusal (agent log:\n%s)", nodeC, agentC.logs.String()))

	if !strings.Contains(agentC.logs.String(), "refusing to start a managed ptp4l") {
		t.Fatalf("agent %s log does not name the refusal reason; log:\n%s", nodeC, agentC.logs.String())
	}

	// The refused config must never have taken effect: nodeC's own clock
	// report never claims a lock through the rejected managed provider.
	if nc, ok := coord.findNode(t, nodeC); ok && clockSignalEquals(nc, "node.clock.ptp.state", "locked") {
		t.Fatalf("node %s reports locked despite its managed provider being refused", nodeC)
	}
	t.Log("scenario 4 OK: the second managed provider on the same interface/domain was refused, with its reason logged")
}

func findClockSignal(node v1.Node, signal string) (v1.ObservationEntry, bool) {
	for _, e := range node.Clock {
		if e.Signal == signal {
			return e, true
		}
	}
	return v1.ObservationEntry{}, false
}

func clockSignalEquals(node v1.Node, signal, want string) bool {
	e, ok := findClockSignal(node, signal)
	if !ok {
		return false
	}
	got, ok := e.Value.(string)
	return ok && got == want
}

func clockSignalIsOneOf(node v1.Node, signal string, want ...string) bool {
	e, ok := findClockSignal(node, signal)
	if !ok {
		return false
	}
	got, ok := e.Value.(string)
	if !ok {
		return false
	}
	for _, w := range want {
		if got == w {
			return true
		}
	}
	return false
}

func clockSignalString(node v1.Node, signal string) (string, bool) {
	e, ok := findClockSignal(node, signal)
	if !ok || e.Value == nil {
		return "", false
	}
	s, ok := e.Value.(string)
	return s, ok
}

// clockSignalFloat reads a numeric clock signal — encoding/json decodes
// every JSON number into float64 when the target is `any`, matching
// v1.Evidence.Value's own doc comment.
func clockSignalFloat(node v1.Node, signal string) (float64, bool) {
	e, ok := findClockSignal(node, signal)
	if !ok || e.Value == nil {
		return 0, false
	}
	f, ok := e.Value.(float64)
	return f, ok
}
