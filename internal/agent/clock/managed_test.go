package clock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requirePTP4L skips t when this environment does not have a real ptp4l
// and pmc available at their expected paths — neither is guaranteed by
// `go test ./...` running outside this seam's own dev VM (see
// docs/build/TRACK-I-clock-and-sync.md's own bench section), so a test
// that needs the real binaries must fail soft here, matching this
// codebase's requireBroker/requireTestDep convention one layer up
// (test/integration/harness_test.go) rather than a hard failure a laptop
// or CI image without linuxptp installed would hit for no actionable
// reason.
func requirePTP4L(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(ptp4lBinary); err != nil {
		t.Skipf("ptp4l not found at %s: %v", ptp4lBinary, err)
	}
	if _, err := os.Stat(pmcBinary); err != nil {
		t.Skipf("pmc not found at %s: %v", pmcBinary, err)
	}
	// A real ptp4l run and pmc's own UDS client bind both need root (raw
	// PTP event sockets, and a write into /run) in every environment this
	// seam was built and tested against — an unprivileged `go test` run
	// (this repository's default) skips rather than failing, with the
	// exact reason, matching this seam's own dependency-guard contract
	// ("when ptp4l or the privileges it needs are absent, the test
	// SKIPS"). Run `sudo go test -run TestManagedProvider ./internal/agent/clock/...`
	// to exercise this for real.
	if os.Geteuid() != 0 {
		t.Skip("this test needs root (ptp4l's raw PTP sockets and pmc's own UDS client bind both require it); run with sudo to exercise it")
	}
}

func TestWritePTP4LConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ptp4l.conf")
	cfg := ManagedConfig{Interface: "lo", Domain: 24, Priority1: 248, ClientOnly: true, HardwareTimestamping: false}
	if err := writePTP4LConfig(path, cfg, false, "/run/x-rw", "/run/x-ro"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"domainNumber 24", "priority1 248", "priority2 128", "clientOnly 1", "time_stamping software", "uds_address /run/x-rw", "uds_ro_address /run/x-ro"} {
		if !strings.Contains(body, want) {
			t.Errorf("config missing %q:\n%s", want, body)
		}
	}
}

func TestWritePTP4LConfigHardwareTimestamping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ptp4l.conf")
	cfg := ManagedConfig{Interface: "eth0", Domain: 0, HardwareTimestamping: true}
	if err := writePTP4LConfig(path, cfg, true, "/run/x-rw", "/run/x-ro"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "time_stamping hardware") {
		t.Errorf("expected hardware timestamping in config:\n%s", raw)
	}
	if strings.Contains(string(raw), "clientOnly") {
		t.Errorf("clientOnly must be absent when not requested:\n%s", raw)
	}
}

func TestFindRunningPTP4LNoMatch(t *testing.T) {
	// A made-up interface name will never be found bound by any real
	// process; this asserts the negative path (no conflict) works and
	// that scanning /proc does not itself error out in this environment.
	if _, _, ok := findRunningPTP4L("showmesh-test-iface-that-does-not-exist"); ok {
		t.Fatalf("did not expect a match for a nonexistent interface")
	}
}

// testRunDir returns a fresh directory under /run for a real-ptp4l test's
// [ManagedConfig.RunDir], removed on cleanup. Deliberately NOT
// [testing.T.TempDir]: that resolves under the OS temp directory (/tmp on
// this platform), and this seam's own bench found pmc's UDS management
// protocol silently undeliverable there in this sandboxed VM even with a
// correctly matched domain — /run is not merely ptp4l's production
// default, it is the one path family confirmed to work here (see
// managed.go's own RunDir doc comment).
func testRunDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/run", "showmesh-clock-test-")
	if err != nil {
		t.Fatalf("create test run dir under /run: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestManagedProviderRealPTP4LLifecycle(t *testing.T) {
	requirePTP4L(t)

	dir := testRunDir(t)
	domain := 25 // distinct from other tests' domains in case of overlap on a shared dev box
	cfg := ManagedConfig{Interface: "lo", Domain: domain, RunDir: dir}

	p, err := NewManagedProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewManagedProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if p.Kind() != ProviderManaged {
		t.Fatalf("Kind() = %v, want managed", p.Kind())
	}

	// Before Start, the process is not running: Poll must report
	// Reachable=false, never a fabricated reading.
	raw := p.Poll(context.Background())
	if raw.Reachable {
		t.Fatalf("expected Reachable=false before Start")
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// A second Start on the same instance is refused (already running),
	// matching every other supervised-process precedent in this codebase.
	if err := p.Start(context.Background()); err == nil {
		t.Fatalf("expected a second Start to be refused")
	}

	var locked RawStatus
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		locked = p.Poll(context.Background())
		if locked.Reachable && locked.Locked {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !locked.Reachable || !locked.Locked {
		t.Fatalf("managed ptp4l did not reach a locked reading within the deadline: %+v", locked)
	}
	if locked.Owner != ManagedOwner {
		t.Fatalf("owner = %q, want %q", locked.Owner, ManagedOwner)
	}
	if !locked.RoleKnown || locked.Role != RoleGrandmaster {
		t.Fatalf("role = %q/%v, want grandmaster (this is the only clock in its domain on loopback)", locked.Role, locked.RoleKnown)
	}
	if !locked.TimestampingKnown || locked.Timestamping != TimestampingSoftware {
		t.Fatalf("timestamping = %q/%v, want software", locked.Timestamping, locked.TimestampingKnown)
	}

	// Software timestamping: Now() reads CLOCK_REALTIME and must succeed.
	mt := p.Now(context.Background())
	if !mt.Valid {
		t.Fatalf("Now() invalid in software timestamping mode: %s", mt.Reason)
	}

	p.Stop()
	after := p.Poll(context.Background())
	if after.Reachable {
		t.Fatalf("expected Reachable=false after Stop")
	}
}

func TestManagedProviderRefusesSecondInstanceSameInterfaceDomain(t *testing.T) {
	requirePTP4L(t)

	dir := testRunDir(t)
	domain := 26
	cfg := ManagedConfig{Interface: "lo", Domain: domain, RunDir: dir}

	first, err := NewManagedProvider(cfg, nil)
	if err != nil {
		t.Fatalf("first NewManagedProvider: %v", err)
	}
	defer func() { _ = first.Close() }()
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// Give the first instance time to actually bind its UDS sockets and
	// the interface before the second one's pre-check runs.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw := first.Poll(context.Background()); raw.Reachable {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	_, err = NewManagedProvider(cfg, nil)
	if err == nil {
		t.Fatalf("expected the second managed provider on the same interface and domain to be refused")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("refusal error does not explain why: %v", err)
	}
}

// --- fake ptp4l helper process, for testing the hardware-timestamping
// fallback without a real ptp4l or root — the standard os/exec
// "TestMain re-exec" pattern (see e.g. os/exec_test.go's own
// TestHelperProcess). Requires none of requirePTP4L's dependencies: the
// fake process never binds a raw PTP socket, so every test below runs
// unprivileged and unconditionally.

const (
	// fakePTP4LMarkerEnv, when set in THIS test binary's own environment,
	// makes a re-exec of it (superviseLoop's exec.Command inherits the
	// parent's environment, since it sets no Env of its own) behave as a
	// fake ptp4l instead of running the normal test suite — see TestMain.
	fakePTP4LMarkerEnv = "SHOWMESH_CLOCK_TEST_FAKE_PTP4L"

	// fakePTP4LHardwareOKEnv, when set, makes a fake ptp4l invocation
	// requesting hardware timestamping succeed (block until killed)
	// instead of exiting immediately. Unset in every test below: this
	// seam has no real hardware timestamping to fake succeeding, and the
	// whole point of these tests is the FALLBACK path.
	fakePTP4LHardwareOKEnv = "SHOWMESH_CLOCK_TEST_FAKE_PTP4L_HARDWARE_OK"

	// fakePTP4LLogEnv names a file a fake ptp4l invocation appends one
	// line to per attempt ("hardware" or "software", whichever its own
	// -f config requested), so a test can assert the exact sequence of
	// modes superviseLoop actually attempted.
	fakePTP4LLogEnv = "SHOWMESH_CLOCK_TEST_FAKE_PTP4L_LOG"
)

// TestMain intercepts a re-exec of this test binary standing in for
// ptp4l, before the normal `go test` machinery (which would otherwise
// choke on runFakePTP4L's own "-f"/"-i"/"-m" arguments) ever runs.
func TestMain(m *testing.M) {
	if os.Getenv(fakePTP4LMarkerEnv) != "" {
		os.Exit(runFakePTP4L())
	}
	os.Exit(m.Run())
}

// runFakePTP4L behaves like ptp4l just enough to test managed.go's
// hardware-timestamping fallback: it reads the "-f" config file's own
// "time_stamping" line and, when it says "hardware" (and
// fakePTP4LHardwareOKEnv is not set), exits immediately with a nonzero
// status — mimicking a driver that does not support hardware
// timestamping, the exact failure this seam's own bench observed
// starting a real ptp4l against a real NIC lacking PHC support.
// Otherwise it blocks until killed, like a real ptp4l that reached its
// requested mode. time.Sleep, not select{}, is what blocks: an empty
// select in a process with no other goroutines is a Go runtime
// deadlock, not a wait, and a real SIGKILL (superviseLoop's own Stop
// path) ends the sleep long before it would ever fire on its own.
func runFakePTP4L() int {
	var confPath string
	args := os.Args[1:]
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			confPath = args[i+1]
		}
	}
	raw, err := os.ReadFile(confPath)
	if err != nil {
		return 2
	}
	mode := "software"
	if strings.Contains(string(raw), "time_stamping hardware") {
		mode = "hardware"
	}
	if logPath := os.Getenv(fakePTP4LLogEnv); logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(mode + "\n")
			_ = f.Close()
		}
	}
	if mode == "hardware" && os.Getenv(fakePTP4LHardwareOKEnv) == "" {
		return 1
	}
	time.Sleep(24 * time.Hour)
	return 0
}

// fakePTP4LSetup points ptp4lBinary at this test binary itself (marked
// via fakePTP4LMarkerEnv) and shrinks the probe window/backoff so a test
// does not wait on production timing, all restored via t.Cleanup/
// t.Setenv's own automatic restoration.
func fakePTP4LSetup(t *testing.T) (logPath string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve this test binary's own path: %v", err)
	}
	origPTP4L := ptp4lBinary
	ptp4lBinary = exe
	t.Cleanup(func() { ptp4lBinary = origPTP4L })

	origProbe := hardwareTimestampingProbeWindow
	hardwareTimestampingProbeWindow = 200 * time.Millisecond
	t.Cleanup(func() { hardwareTimestampingProbeWindow = origProbe })

	origBackoff := managedRestartBackoff
	managedRestartBackoff = 50 * time.Millisecond
	t.Cleanup(func() { managedRestartBackoff = origBackoff })

	t.Setenv(fakePTP4LMarkerEnv, "1")
	logPath = filepath.Join(t.TempDir(), "fake-ptp4l.log")
	t.Setenv(fakePTP4LLogEnv, logPath)
	return logPath
}

func waitReachedTimestamping(t *testing.T, p *ManagedProvider, timeout time.Duration) (Timestamping, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if reached, known := p.reachedTimestampingMode(); known {
			return reached, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "", false
}

func readFakePTP4LLog(t *testing.T, logPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake ptp4l log %s: %v", logPath, err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestManagedProviderFallsBackToSoftwareTimestampingOnEarlyExit proves
// both halves of the defect this test file exists for: a hardware
// timestamping request that fails its startup probe falls back to
// software (never loops forever reporting failed), and every subsequent
// consumer of this provider — the config file on disk, [ManagedProvider.
// reachedTimestampingMode], and [ManagedProvider.Now] — reports the mode
// ACTUALLY REACHED, never the mode originally requested.
func TestManagedProviderFallsBackToSoftwareTimestampingOnEarlyExit(t *testing.T) {
	logPath := fakePTP4LSetup(t)

	dir := t.TempDir()
	cfg := ManagedConfig{Interface: "showmesh-fake0", Domain: 30, HardwareTimestamping: true, RunDir: dir}
	p, err := NewManagedProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewManagedProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Before anything has run long enough to confirm a mode, Now() must
	// not guess — never a fabricated PHC or CLOCK_REALTIME reading.
	if mt := p.Now(context.Background()); mt.Valid {
		t.Fatalf("Now() before Start reported Valid=true unexpectedly: %+v", mt)
	}

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Between the early exit and the software retry reaching running,
	// Poll must be Reachable=false with a reason naming the fallback —
	// "the reason says a fallback happened when it did." Polled tightly
	// (not merely inferred from the log) because this window is real but
	// brief: the retry itself starts immediately (see superviseLoop's own
	// backoff.Reset(0) comment) and only needs to survive the shrunk
	// probe window from fakePTP4LSetup before reaching running.
	sawFallbackReason := false
	fallbackDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(fallbackDeadline) {
		raw := p.Poll(context.Background())
		if !raw.Reachable && strings.Contains(raw.Reason, "falling back to software timestamping") {
			sawFallbackReason = true
			break
		}
		if raw.Reachable {
			break // already past the fallback window
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawFallbackReason {
		t.Fatalf("never observed Poll() reporting the fallback reason before the software retry reached running")
	}

	reached, known := waitReachedTimestamping(t, p, 5*time.Second)
	if !known {
		t.Fatalf("timestamping mode never confirmed reached")
	}
	if reached != TimestampingSoftware {
		t.Fatalf("reached = %q, want %q (the hardware attempt should have fallen back)", reached, TimestampingSoftware)
	}

	raw, err := os.ReadFile(p.confPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "time_stamping software") {
		t.Errorf("config was not rewritten to software after the fallback:\n%s", raw)
	}
	if strings.Contains(string(raw), "time_stamping hardware") {
		t.Errorf("config still requests hardware after the fallback:\n%s", raw)
	}

	// Now() must select CLOCK_REALTIME, never attempt a PHC read, once
	// the reached mode is software — this is RES-019 §4.1 item 6's own
	// hazard: reading a PHC ptp4l fell back away from disciplining would
	// report a locked clock with the wrong time.
	mt := p.Now(context.Background())
	if !mt.Valid {
		t.Fatalf("Now() invalid after falling back to software: %s", mt.Reason)
	}
	if !strings.Contains(mt.Reason, "software timestamping") {
		t.Errorf("Now() reason does not name software timestamping: %q", mt.Reason)
	}

	// Exactly one hardware attempt, ever, within this Start.
	lines := readFakePTP4LLog(t, logPath)
	if len(lines) != 2 || lines[0] != "hardware" || lines[1] != "software" {
		t.Fatalf("fake ptp4l attempt sequence = %v, want exactly [hardware software]", lines)
	}
}

// TestManagedProviderDoesNotRetryHardwareAfterFallbackWithinOneStart
// proves the other explicit requirement: a crash-restart AFTER a
// fallback has already happened must retry software again, never
// hardware, for the rest of that one Start — but a fresh Start (Stop,
// then Start again) is a new capability probe and may attempt hardware
// again.
func TestManagedProviderDoesNotRetryHardwareAfterFallbackWithinOneStart(t *testing.T) {
	logPath := fakePTP4LSetup(t)

	dir := t.TempDir()
	cfg := ManagedConfig{Interface: "showmesh-fake1", Domain: 31, HardwareTimestamping: true, RunDir: dir}
	p, err := NewManagedProvider(cfg, nil)
	if err != nil {
		t.Fatalf("NewManagedProvider: %v", err)
	}
	defer func() { _ = p.Close() }()

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, known := waitReachedTimestamping(t, p, 5*time.Second); !known {
		t.Fatalf("timestamping mode never confirmed reached after the first fallback")
	}
	if lines := readFakePTP4LLog(t, logPath); len(lines) != 2 || lines[0] != "hardware" || lines[1] != "software" {
		t.Fatalf("attempt sequence after the first fallback = %v, want [hardware software]", lines)
	}

	// Simulate an unrelated crash of the now-running (software) process
	// and confirm the restart retries software, not hardware.
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatalf("no running process to crash")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the running fake ptp4l: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if lines := readFakePTP4LLog(t, logPath); len(lines) >= 3 {
			if lines[2] != "software" {
				t.Fatalf("attempt after a post-fallback crash = %q, want software (never hardware again within one Start)", lines[2])
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lines := readFakePTP4LLog(t, logPath); len(lines) < 3 {
		t.Fatalf("no restart attempt observed after the crash; log = %v", lines)
	}

	// A fresh Start (Stop, then Start again) is a new capability probe:
	// it may attempt hardware again.
	p.Stop()
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("truncate fake ptp4l log for the second Start: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if _, known := waitReachedTimestamping(t, p, 5*time.Second); !known {
		t.Fatalf("timestamping mode never confirmed reached after the second Start's own fallback")
	}
	if lines := readFakePTP4LLog(t, logPath); len(lines) != 2 || lines[0] != "hardware" || lines[1] != "software" {
		t.Fatalf("attempt sequence after a fresh Start = %v, want [hardware software] (a fresh Start may attempt hardware again)", lines)
	}
}
