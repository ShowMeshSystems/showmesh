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
	if err := writePTP4LConfig(path, cfg, "/run/x-rw", "/run/x-ro"); err != nil {
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
	if err := writePTP4LConfig(path, cfg, "/run/x-rw", "/run/x-ro"); err != nil {
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
