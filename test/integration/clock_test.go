//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
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

	// Seam I2 runs its scheduled starts on these same two agents, so they
	// need an asset directory and a non-hardware sink. fakesink is the
	// same override audio_gstengine_test.go uses: a real gstengine
	// pipeline, no real audio device opened.
	t.Setenv(envGstAudioSinkOverride, "fakesink")
	t.Setenv(envAudioReportInterval, "500ms")
	assetDir := t.TempDir()
	const benchClip = "clip.wav"
	clipContent, clipHash := writeShortWAV(t, filepath.Join(assetDir, benchClip), 30.0)

	nodeA := "clock-a-" + uniqueSuffix()
	nodeB := "clock-b-" + uniqueSuffix()
	agentA := startAgent(t, agentConfig{nodeID: nodeA, assetDir: assetDir, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})
	agentB := startAgent(t, agentConfig{nodeID: nodeB, assetDir: assetDir, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})

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
	agentC := startAgent(t, agentConfig{nodeID: nodeC, assetDir: assetDir, extraEnv: []string{"SHOWMESH_CLOCK_REPORT_INTERVAL=1s"}})
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

	runScheduledStartScenarios(t, scheduledStartBench{
		assetID: "clip-1", contentHash: clipHash, filename: benchClip, sizeBytes: int64(len(clipContent)),
		locked: []*benchAudioNode{
			{id: nodeA, session: "sched-a-" + uniqueSuffix(), agent: agentA},
			{id: nodeB, session: "sched-b-" + uniqueSuffix(), agent: agentB},
		},
		unsynchronized: &benchAudioNode{id: nodeC, session: "sched-c-" + uniqueSuffix(), agent: agentC},
		coord:          coord,
	})
}

// scheduledStartBench is what the seam I2 scenarios need from the harness
// TestClockProviderBench already built: two nodes locked to the one ptp4l
// instance, one node whose clock configuration was refused so it holds no
// provider at all, and the asset all three play.
type scheduledStartBench struct {
	assetID     string
	contentHash string
	filename    string
	sizeBytes   int64

	locked         []*benchAudioNode
	unsynchronized *benchAudioNode
	coord          *testCoordinator
}

// runScheduledStartScenarios is Track I seam I2's half of this bench:
// scheduled start across two locked nodes, a refused late instant, and a
// node with no clock provider keeping start-on-arrival.
func runScheduledStartScenarios(t *testing.T, b scheduledStartBench) {
	t.Helper()

	for _, node := range append(append([]*benchAudioNode{}, b.locked...), b.unsynchronized) {
		cli, w := startCmdClient(t, node.id)
		awaitAgentReceivingCommands(t, cli, w, node.id)
		node.cli, node.watcher = cli, w
	}
	nodeIDs := []string{b.unsynchronized.id}
	for _, node := range b.locked {
		nodeIDs = append(nodeIDs, node.id)
	}
	audio := subscribeAudioPayloads(t, nodeIDs...)

	// --- Scenario 5: two nodes, one start instant ---
	nodeA, nodeB := b.locked[0], b.locked[1]
	readyA := prepareBenchAudioNode(t, nodeA, b.assetID, b.contentHash, b.filename, b.sizeBytes).requireValid(t, nodeA.id)
	readyB := prepareBenchAudioNode(t, nodeB, b.assetID, b.contentHash, b.filename, b.sizeBytes).requireValid(t, nodeB.id)
	t.Logf("scenario 5: prepare readiness: %s media clock %d ns, preroll %d ms; %s media clock %d ns, preroll %d ms",
		nodeA.id, readyA.mediaClockNowNs, readyA.prerollMs, nodeB.id, readyB.mediaClockNowNs, readyB.prerollMs)

	// The coordinator's own rule (RES-019 section 6): T0 is the latest
	// node ready time plus a delivery bound and margin. Both nodes here
	// follow one ptp4l in software timestamping mode on one host, so both
	// readings come from the same disciplined clock and one absolute
	// instant is meaningful to both.
	t0 := readyA.mediaClockNowNs
	if readyB.mediaClockNowNs > t0 {
		t0 = readyB.mediaClockNowNs
	}
	t0 += scheduledStartMargin.Nanoseconds()

	startA := "cmd-sched-start-a-" + uniqueSuffix()
	startB := "cmd-sched-start-b-" + uniqueSuffix()
	dispatchCmd(t, nodeA.cli, nodeA.id, scheduledStartSessionCmd(nodeA.id, startA, nodeA.session, 3, t0))
	dispatchCmd(t, nodeB.cli, nodeB.id, scheduledStartSessionCmd(nodeB.id, startB, nodeB.session, 3, t0))

	for _, pair := range []struct {
		node *benchAudioNode
		cmd  string
	}{{nodeA, startA}, {nodeB, startB}} {
		res := waitForResult(t, pair.node.watcher, pair.cmd, scheduledStartMargin+30*time.Second)
		outcome, reason := evidenceOutcome(t, res)
		if outcome != "started" {
			t.Fatalf("%s scheduled audio.session.start outcome = %q (%s); agent log:\n%s",
				pair.node.id, outcome, reason, pair.node.agent.logs.String())
		}
	}

	payloadA := waitForMeasuredTimeline(t, audio, nodeA.id, 30*time.Second)
	payloadB := waitForMeasuredTimeline(t, audio, nodeB.id, 30*time.Second)

	if payloadA.TimelineScheduledAtNs != t0 || payloadB.TimelineScheduledAtNs != t0 {
		t.Fatalf("nodes report different start instants: %s %d, %s %d, dispatched %d (a rounded instant looks exactly like this)",
			nodeA.id, payloadA.TimelineScheduledAtNs, nodeB.id, payloadB.TimelineScheduledAtNs, t0)
	}

	// Each node's own timeline error IS its start lateness against the
	// shared T0: expected is media_now minus T0 and actual is what its
	// sink has presented since it started, so a node that began L late
	// reports an error of L for as long as it runs. The skew between the
	// two sinks is therefore the difference of the two errors, and it is
	// measured from the sink clocks rather than from command timing.
	skew := payloadA.TimelineErrorMs - payloadB.TimelineErrorMs
	if skew < 0 {
		skew = -skew
	}
	t.Logf("SCENARIO 5 MEASURED START SKEW: %d ms (%s error %d ms, %s error %d ms, one T0 of %d ns, harness bound %v)",
		skew, nodeA.id, payloadA.TimelineErrorMs, nodeB.id, payloadB.TimelineErrorMs, t0, benchStartSkewBound)
	if time.Duration(skew)*time.Millisecond > benchStartSkewBound {
		t.Fatalf("start skew between the two sink clocks = %d ms, beyond this harness's own bound of %v; %s error %d ms, %s error %d ms",
			skew, benchStartSkewBound, nodeA.id, payloadA.TimelineErrorMs, nodeB.id, payloadB.TimelineErrorMs)
	}

	// --- Scenario 6: a deliberately late instant is refused by a real node ---
	lateSession := "sched-late-" + uniqueSuffix()
	lateApply := "cmd-late-apply-" + uniqueSuffix()
	dispatchCmd(t, nodeA.cli, nodeA.id, applySessionCmd(nodeA.id, lateApply, lateSession, b.assetID, b.contentHash, b.filename, b.sizeBytes))
	if res := waitForResult(t, nodeA.watcher, lateApply, 15*time.Second); res.Outcome == mqttproto.OutcomeFailed {
		t.Fatalf("%s apply for the late-instant session failed: %s", nodeA.id, res.Reason)
	}
	lateReady := prepareBenchAudioNodeSession(t, nodeA, lateSession).requireValid(t, nodeA.id)

	lateStart := "cmd-late-start-" + uniqueSuffix()
	lateT0 := lateReady.mediaClockNowNs - (5 * time.Second).Nanoseconds()
	dispatchCmd(t, nodeA.cli, nodeA.id, scheduledStartSessionCmd(nodeA.id, lateStart, lateSession, 3, lateT0))
	lateRes := waitForResult(t, nodeA.watcher, lateStart, 20*time.Second)
	outcome, reason := evidenceOutcome(t, lateRes)
	if outcome != "refused" {
		t.Fatalf("a real node given a T0 five seconds in its own past reported outcome %q (%s), want refused; agent log:\n%s",
			outcome, reason, nodeA.agent.logs.String())
	}
	if !strings.Contains(reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("late-instant refusal reason = %q, want it to carry %q", reason, pkgaudio.ReasonScheduledStartInPast)
	}
	t.Logf("scenario 6 OK: %s refused a T0 5s in its own past with %q", nodeA.id, pkgaudio.ReasonScheduledStartInPast)

	// --- Scenario 7: a node with no clock provider starts on arrival ---
	nodeC := b.unsynchronized
	if n, ok := b.coord.findNode(t, nodeC.id); !ok || !clockSignalEquals(n, "node.clock.ptp.state", "unsynchronized") {
		state, _ := clockSignalString(n, "node.clock.ptp.state")
		t.Fatalf("node %s reports node.clock.ptp.state %q, want unsynchronized (its managed provider was refused, so it holds none)", nodeC.id, state)
	}
	readyC := prepareBenchAudioNode(t, nodeC, b.assetID, b.contentHash, b.filename, b.sizeBytes)
	if readyC.mediaClockValid {
		t.Fatalf("%s holds no clock provider but its prepare reported a valid media clock reading of %d ns", nodeC.id, readyC.mediaClockNowNs)
	}
	t.Logf("scenario 7: %s reports no usable media clock, with a reason: %q", nodeC.id, readyC.mediaClockReason)

	startC := "cmd-sched-start-c-" + uniqueSuffix()
	unsyncT0 := time.Now().Add(scheduledStartMargin).UnixNano()
	dispatched := time.Now()
	dispatchCmd(t, nodeC.cli, nodeC.id, scheduledStartSessionCmd(nodeC.id, startC, nodeC.session, 3, unsyncT0))
	resC := waitForResult(t, nodeC.watcher, startC, 20*time.Second)
	elapsed := time.Since(dispatched)
	outcomeC, reasonC := evidenceOutcome(t, resC)
	if outcomeC != "started" {
		t.Fatalf("%s (no clock provider) scheduled start outcome = %q (%s), want started on arrival; agent log:\n%s",
			nodeC.id, outcomeC, reasonC, nodeC.agent.logs.String())
	}
	if elapsed >= scheduledStartMargin {
		t.Fatalf("%s took %v to answer a start instant %v in the future; a node with no provider must start ON ARRIVAL, not wait for an instant it cannot read",
			nodeC.id, elapsed, scheduledStartMargin)
	}
	if !strings.Contains(reasonC, "started on arrival") {
		t.Fatalf("%s started without saying it ignored the instant; reason = %q", nodeC.id, reasonC)
	}
	waitFor(t, 20*time.Second, 200*time.Millisecond, func() bool {
		p, ok := audio.latestFor(nodeC.id)
		return ok && !p.TimelineScheduled && p.TimelineReason != ""
	}, "node "+nodeC.id+" to report no scheduled timeline, with a reason")
	t.Logf("scenario 7 OK: %s started on arrival in %v and said so (%q)", nodeC.id, elapsed, reasonC)
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

// benchStartSkewBound is what this bench holds two scheduled starts to.
// It is this HARNESS's own tolerance, not an acceptance criterion and not
// an audibility claim: what counts as aligned to a listener is I0's to
// establish from a two-channel recording of real nodes, and nothing here
// can measure that. The number this bench actually observes is logged
// every run and is the deliverable; this bound exists so a regression
// that pushes the skew into the hundreds of milliseconds fails rather
// than passing quietly.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED.
const benchStartSkewBound = 250 * time.Millisecond

// scheduledStartMargin is how far ahead of the nodes' own media clock
// this bench sets T0: enough for both start commands to be delivered and
// for both nodes to be waiting on the instant before it arrives, which is
// what RES-019 section 6's "command delivery bound plus margin" means
// here.
const scheduledStartMargin = 3 * time.Second

// audioPayloadSubscriber records the latest whole showmesh.node.audio/v1
// payload per node. audio_broker_loss_test.go's own subscriber keeps only
// the per-session reports; the node.audio.timeline.* evidence this file
// asserts on is node-level, so this keeps the payload itself.
type audioPayloadSubscriber struct {
	mu     sync.Mutex
	latest map[string]mqttproto.AudioPayload
}

func (w *audioPayloadSubscriber) onPublish(pr paho.PublishReceived) (bool, error) {
	if pr.Packet == nil {
		return true, nil
	}
	env, err := mqttproto.DecodeEnvelope(pr.Packet.Payload)
	if err != nil || env.Schema != mqttproto.SchemaNodeAudioV1 {
		return true, nil
	}
	p, err := mqttproto.DecodeAudioPayload(env)
	if err != nil {
		return true, nil
	}
	nodeID, ok := nodeIDFromObservedTopic(pr.Packet.Topic)
	if !ok {
		return true, nil
	}
	w.mu.Lock()
	w.latest[nodeID] = p
	w.mu.Unlock()
	return true, nil
}

func (w *audioPayloadSubscriber) latestFor(nodeID string) (mqttproto.AudioPayload, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.latest[nodeID]
	return p, ok
}

// nodeIDFromObservedTopic pulls the node id out of
// showmesh/nodes/<node-id>/observed/audio.
func nodeIDFromObservedTopic(topic string) (string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 || parts[0] != "showmesh" || parts[1] != "nodes" {
		return "", false
	}
	return parts[2], true
}

// subscribeAudioPayloads subscribes one raw coordinator-role client to
// every named node's observed/audio topic.
func subscribeAudioPayloads(t *testing.T, nodeIDs ...string) *audioPayloadSubscriber {
	t.Helper()
	if testMQTTCoordinatorUsername == "" {
		t.Fatalf("no MQTT broker credential available (%s is unset); run via `make test-integration`", envTestMQTTCoordinatorUsername)
	}
	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)
	w := &audioPayloadSubscriber{latest: map[string]mqttproto.AudioPayload{}}
	cli.AddOnPublishReceived(w.onPublish)

	subs := make([]paho.SubscribeOptions, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		topic, err := mqttproto.ObservedTopic(nodeID, "audio")
		if err != nil {
			t.Fatalf("ObservedTopic(%s): %v", nodeID, err)
		}
		subs = append(subs, paho.SubscribeOptions{Topic: topic, QoS: 1})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sa, err := cli.Subscribe(ctx, &paho.Subscribe{Subscriptions: subs})
	if err != nil {
		t.Fatalf("SUBSCRIBE observed/audio: %v", err)
	}
	for i, rc := range sa.Reasons {
		if rc >= 0x80 {
			t.Fatalf("SUBSCRIBE observed/audio rejected: index %d, reason code %d", i, rc)
		}
	}
	return w
}

// benchAudioNode is one node's own command client and session identity
// for the scheduled-start scenarios.
type benchAudioNode struct {
	id      string
	cli     *paho.Client
	watcher *resultWatcher
	session string
	agent   *testAgent
}

// prepareBenchAudioNode configures nodeID's output against the
// non-hardware fakesink override, applies a session over the asset the
// caller already wrote, and prepares it. It returns the node's own
// media-clock reading from the prepare result, which is the reading a
// coordinator would pick a start instant from.
func prepareBenchAudioNode(t *testing.T, node *benchAudioNode, assetID, contentHash, filename string, sizeBytes int64) benchReadiness {
	t.Helper()

	configureID := "cmd-audio-node-configure-" + uniqueSuffix()
	dispatchCmd(t, node.cli, node.id, audioNodeConfigureCmd(node.id, configureID, 1))
	if res := waitForResult(t, node.watcher, configureID, 20*time.Second); res.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("%s audio.node.configure = %q (%s); agent log:\n%s", node.id, res.Outcome, res.Reason, node.agent.logs.String())
	}

	applyID := "cmd-apply-" + uniqueSuffix()
	dispatchCmd(t, node.cli, node.id, applySessionCmd(node.id, applyID, node.session, assetID, contentHash, filename, sizeBytes))
	if res := waitForResult(t, node.watcher, applyID, 15*time.Second); res.Outcome == mqttproto.OutcomeFailed || res.Outcome == mqttproto.OutcomeRefused {
		t.Fatalf("%s audio.session.apply = %q (%s)", node.id, res.Outcome, res.Reason)
	}

	return prepareBenchAudioNodeSession(t, node, node.session)
}

// prepareBenchAudioNodeSession prepares one session on an already
// configured node and reads the readiness evidence off the result: the
// node's own media-clock reading, and the preroll latency it measured.
func prepareBenchAudioNodeSession(t *testing.T, node *benchAudioNode, sessionID string) benchReadiness {
	t.Helper()
	prepareID := "cmd-prepare-" + uniqueSuffix()
	dispatchCmd(t, node.cli, node.id, prepareSessionCmd(node.id, prepareID, sessionID))
	res := waitForResult(t, node.watcher, prepareID, 20*time.Second)
	if res.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("%s audio.session.prepare = %q (%s); agent log:\n%s", node.id, res.Outcome, res.Reason, node.agent.logs.String())
	}
	if res.Evidence == nil {
		t.Fatalf("%s prepare result carries no evidence", node.id)
	}
	value, ok := res.Evidence.Value.(map[string]any)
	if !ok {
		t.Fatalf("%s prepare evidence value = %#v, want an object", node.id, res.Evidence.Value)
	}
	out := benchReadiness{}
	valid, present := value["mediaClockValid"].(bool)
	if !present {
		t.Fatalf("%s prepare result carries no mediaClockValid; an absent validity flag is read as valid", node.id)
	}
	out.mediaClockValid = valid
	out.mediaClockReason, _ = value["mediaClockReason"].(string)
	if raw, present := value["prerollMs"]; present {
		if f, ok := raw.(float64); ok {
			out.prerollMs = int64(f)
		}
	}
	if !valid {
		if out.mediaClockReason == "" {
			t.Fatalf("%s prepare reported an invalid media clock with no reason", node.id)
		}
		if _, carried := value["mediaClockNowNs"]; carried {
			t.Fatalf("%s prepare carried an instant alongside an invalid reading; it must carry none", node.id)
		}
		return out
	}
	// json.Number, not float64: a nanosecond-scale instant read through a
	// float64 is already rounded, which is exactly what the decode path
	// this asserts against exists to prevent.
	number, ok := value["mediaClockNowNs"].(json.Number)
	if !ok {
		t.Fatalf("%s prepare mediaClockNowNs decoded as %T, want json.Number", node.id, value["mediaClockNowNs"])
	}
	mediaNowNs, err := number.Int64()
	if err != nil {
		t.Fatalf("%s prepare mediaClockNowNs = %q: %v", node.id, number, err)
	}
	out.mediaClockNowNs = mediaNowNs
	return out
}

// benchReadiness is what one prepare result reports back: the readiness
// evidence a coordinator picks a start instant from.
type benchReadiness struct {
	mediaClockValid  bool
	mediaClockReason string
	mediaClockNowNs  int64
	prerollMs        int64
}

// requireValid fails t unless this node reported a usable media clock.
func (r benchReadiness) requireValid(t *testing.T, nodeID string) benchReadiness {
	t.Helper()
	if !r.mediaClockValid {
		t.Fatalf("%s prepare reported no usable media clock: %s", nodeID, r.mediaClockReason)
	}
	return r
}

// prepareSessionCmd and scheduledStartSessionCmd are this file's own
// command builders; audio_broker_loss_test.go's startSessionCmd carries
// no start instant, which is the case scenario 7 needs unchanged.
func prepareSessionCmd(nodeID, commandID, sessionID string) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: commandID,
		Action: "audio.session.prepare",
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"sessionId": sessionID, "invocationId": commandID, "revision": 2,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

func scheduledStartSessionCmd(nodeID, commandID, sessionID string, revision int64, atNs int64) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID: commandID, IdempotencyKey: commandID,
		Action: "audio.session.start",
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params: map[string]any{
			"sessionId": sessionID, "invocationId": commandID, "revision": revision,
			pkgaudio.ParamScheduledAtNs: atNs,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

// waitForMeasuredTimeline blocks until nodeID publishes an audio report
// whose node.audio.timeline.* evidence is both scheduled and measured,
// and returns it.
func waitForMeasuredTimeline(t *testing.T, w *audioPayloadSubscriber, nodeID string, timeout time.Duration) mqttproto.AudioPayload {
	t.Helper()
	var found mqttproto.AudioPayload
	waitFor(t, timeout, 200*time.Millisecond, func() bool {
		p, ok := w.latestFor(nodeID)
		if !ok || !p.TimelineScheduled || !p.TimelineMeasured {
			return false
		}
		found = p
		return true
	}, "node "+nodeID+" to publish a scheduled, measured timeline")
	return found
}

// evidenceOutcome reads a session command result's own audio outcome and
// reason out of its evidence value. A refused audio outcome rides an
// unconfirmed command result (OperationResult.Confirmed is false for
// every non-success audio outcome), so the command-level outcome alone
// cannot tell a refusal from an unconfirmable engine.
func evidenceOutcome(t *testing.T, res mqttproto.ResultPayload) (outcome, reason string) {
	t.Helper()
	if res.Evidence == nil {
		t.Fatalf("result %s carries no evidence", res.CommandID)
	}
	value, ok := res.Evidence.Value.(map[string]any)
	if !ok {
		t.Fatalf("result %s evidence value = %#v, want an object", res.CommandID, res.Evidence.Value)
	}
	outcome, _ = value["outcome"].(string)
	reason, _ = value["reason"].(string)
	return outcome, reason
}
