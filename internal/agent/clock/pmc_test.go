package clock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePMC writes a shell script standing in for pmcBinary that dumps its
// own argv to argsOut, then exits 0 with empty stdout (this test only
// cares what runPMC invoked pmc WITH, not pmc's own response parsing).
func fakePMC(t *testing.T, argsOut string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-pmc.sh")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argsOut + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake pmc: %v", err)
	}
	return script
}

// TestRunPMCUsesWritableLocalSocket covers the bug this fix closes: pmc
// with no -i hardcodes /var/run/pmc.$pid, unwritable to a non-root agent,
// so every GET failed with "uds: bind failed: Permission denied" even
// against a perfectly healthy ptp4l. runPMC must pass -i with a path this
// process can certainly write (never under /var/run or /run), and must
// remove it once the call returns.
func TestRunPMCUsesWritableLocalSocket(t *testing.T) {
	argsOut := filepath.Join(t.TempDir(), "args.txt")
	orig := pmcBinary
	pmcBinary = fakePMC(t, argsOut)
	defer func() { pmcBinary = orig }()

	if _, err := runPMC(context.Background(), "/var/run/ptp/ptp4lro", 0, "PORT_DATA_SET", ""); err != nil {
		t.Fatalf("runPMC: %v", err)
	}

	raw, err := os.ReadFile(argsOut)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	args := strings.Fields(string(raw))
	i := -1
	for n, a := range args {
		if a == "-i" {
			i = n
			break
		}
	}
	if i == -1 || i+1 >= len(args) {
		t.Fatalf("pmc invocation carried no -i argument: %q", string(raw))
	}
	sock := args[i+1]
	if strings.HasPrefix(sock, "/var/run") || strings.HasPrefix(sock, "/run") {
		t.Fatalf("-i socket %q sits under a root-owned directory, reintroducing the bind failure", sock)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("-i socket %q was not cleaned up after the call: stat err = %v", sock, err)
	}
}

// TestRunPMCLocalSocketsAreUnique covers two concurrent reads never
// colliding on the same -i path.
func TestRunPMCLocalSocketsAreUnique(t *testing.T) {
	a, err := pmcLocalSocket("")
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	b, err := pmcLocalSocket("")
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if a == b {
		t.Fatalf("pmcLocalSocket returned the same path twice: %q", a)
	}
}

// shortTempDir makes a directory directly under the OS temp root with a
// short, fixed-length name, cleaned up when t ends. t.TempDir() itself
// embeds the full test name in the path (e.g.
// ".../TestPMCLocalSocketPrefersRuntimeDirectory2959333898/001"), long
// enough on macOS's own already-deep TMPDIR that it can by itself trip
// [pmcSocketMaxPathLen] and mask what a test is actually checking.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pmctd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("cleanup: remove %s: %v", dir, err)
		}
	})
	return dir
}

// clearPMCDirEnv clears RUNTIME_DIRECTORY and STATE_DIRECTORY to "" for
// the duration of a test (os.Getenv reads "" identically for empty and
// absent, and t.Setenv restores each on cleanup), so a test exercising
// one fallback candidate is not accidentally satisfied by another one
// still set from the real environment.
func clearPMCDirEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RUNTIME_DIRECTORY", "")
	t.Setenv("STATE_DIRECTORY", "")
}

// TestPMCLocalSocketPrefersRuntimeDirectory covers candidate 1 of the
// fallback order: RUNTIME_DIRECTORY wins over everything else, including
// a hint the caller supplied.
func TestPMCLocalSocketPrefersRuntimeDirectory(t *testing.T) {
	clearPMCDirEnv(t)
	runtimeDir := shortTempDir(t)
	stateDir := shortTempDir(t)
	hintDir := shortTempDir(t)
	t.Setenv("RUNTIME_DIRECTORY", runtimeDir)
	t.Setenv("STATE_DIRECTORY", stateDir)

	path, err := pmcLocalSocket(hintDir)
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != runtimeDir {
		t.Fatalf("pmcLocalSocket = %q, want a path under RUNTIME_DIRECTORY %q", path, runtimeDir)
	}
}

// TestPMCLocalSocketFallsBackToStateDirectory covers candidate 2: no
// RUNTIME_DIRECTORY set, STATE_DIRECTORY wins over the hint.
func TestPMCLocalSocketFallsBackToStateDirectory(t *testing.T) {
	clearPMCDirEnv(t)
	stateDir := shortTempDir(t)
	hintDir := shortTempDir(t)
	t.Setenv("STATE_DIRECTORY", stateDir)

	path, err := pmcLocalSocket(hintDir)
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != stateDir {
		t.Fatalf("pmcLocalSocket = %q, want a path under STATE_DIRECTORY %q", path, stateDir)
	}
}

// TestPMCLocalSocketFallsBackToHint covers candidate 3: no systemd
// directories set, the caller's own known-writable hint wins over the OS
// temp directory.
func TestPMCLocalSocketFallsBackToHint(t *testing.T) {
	clearPMCDirEnv(t)
	hintDir := shortTempDir(t)

	path, err := pmcLocalSocket(hintDir)
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != hintDir {
		t.Fatalf("pmcLocalSocket = %q, want a path under the hint directory %q", path, hintDir)
	}
}

// TestPMCLocalSocketFallsBackToTempDir covers candidate 4: nothing else
// set or supplied, os.TempDir() is still tried rather than failing
// outright, matching a bare non-systemd run.
func TestPMCLocalSocketFallsBackToTempDir(t *testing.T) {
	clearPMCDirEnv(t)

	path, err := pmcLocalSocket("")
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != filepath.Clean(os.TempDir()) {
		t.Fatalf("pmcLocalSocket = %q, want a path under os.TempDir() %q", path, os.TempDir())
	}
}

// TestPMCLocalSocketSkipsUnwritableCandidate covers a candidate directory
// that cannot be written (matching /tmp under the shipped agent unit's
// ProtectSystem=strict): pmcLocalSocket must move on to the next
// candidate rather than failing outright.
//
// The blocked candidate is a path under a regular file, not a directory
// whose permission bits were stripped: CI runs this suite as root, and
// root ignores permission bits entirely, so a chmod-0500 directory stayed
// writable there and the candidate was never actually skipped. A regular
// file in a path's directory position fails os.CreateTemp with ENOTDIR
// for every uid, root included, so the skip is exercised on the only
// machine this gate matters on.
func TestPMCLocalSocketSkipsUnwritableCandidate(t *testing.T) {
	clearPMCDirEnv(t)
	base := shortTempDir(t)
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}
	unwritable := filepath.Join(blocker, "sock-dir")
	t.Setenv("RUNTIME_DIRECTORY", unwritable)
	hintDir := shortTempDir(t)

	path, err := pmcLocalSocket(hintDir)
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != hintDir {
		t.Fatalf("pmcLocalSocket = %q, want it to skip the unusable RUNTIME_DIRECTORY and land in the hint %q", path, hintDir)
	}
}

// TestPMCLocalSocketSkipsTooLongCandidate covers the Unix socket path
// length limit: a candidate directory long enough that the reserved
// path would exceed 107 bytes must be skipped in favor of the next
// candidate, never returned as-is for pmc's bind() to reject later.
func TestPMCLocalSocketSkipsTooLongCandidate(t *testing.T) {
	clearPMCDirEnv(t)
	base := shortTempDir(t)
	// "pmc.XXXXXXXXXX.sock" (pmc.*.sock's expanded random suffix) is at
	// most ~20 bytes, so padding the directory itself past
	// pmcSocketMaxPathLen guarantees the joined path is too long.
	longDir := filepath.Join(base, strings.Repeat("a", pmcSocketMaxPathLen))
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatalf("mkdir long dir: %v", err)
	}
	t.Setenv("RUNTIME_DIRECTORY", longDir)
	hintDir := shortTempDir(t)

	path, err := pmcLocalSocket(hintDir)
	if err != nil {
		t.Fatalf("pmcLocalSocket: %v", err)
	}
	if filepath.Dir(path) != hintDir {
		t.Fatalf("pmcLocalSocket = %q, want it to skip the too-long RUNTIME_DIRECTORY and land in the hint %q", path, hintDir)
	}
	if len(path) > pmcSocketMaxPathLen {
		t.Fatalf("pmcLocalSocket returned a path over the Unix socket limit: %q (%d bytes)", path, len(path))
	}
}

// TestPMCLocalSocketAllCandidatesFail covers every candidate being
// unwritable: pmcLocalSocket must report an error, not a path pmc's own
// bind() would then fail on anyway.
func TestPMCLocalSocketAllCandidatesFail(t *testing.T) {
	clearPMCDirEnv(t)
	missing := filepath.Join(shortTempDir(t), "does-not-exist")
	t.Setenv("RUNTIME_DIRECTORY", missing)
	t.Setenv("STATE_DIRECTORY", missing)
	t.Setenv("TMPDIR", missing)

	if _, err := pmcLocalSocket(missing); err == nil {
		t.Fatalf("pmcLocalSocket: want an error when every candidate directory is unusable")
	}
}

// readFixture loads a captured pmc output file from testdata/pmc — see
// that directory's README.md for provenance (real captures vs. hand-edited
// derivatives of real captures, and why some had to be derived).
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/pmc/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestParseTimeStatusNPMaster(t *testing.T) {
	out := readFixture(t, "time_status_np.master.txt")
	s := parseTimeStatusNP(out)
	if !s.MasterOffsetKnown || s.MasterOffsetNs != 0 {
		t.Fatalf("master_offset = %d/%v, want 0/known", s.MasterOffsetNs, s.MasterOffsetKnown)
	}
	if !s.GMPresentKnown || s.GMPresent != false {
		t.Fatalf("gmPresent = %v/%v, want false/known", s.GMPresent, s.GMPresentKnown)
	}
	if !s.GMIdentityKnown || s.GMIdentity != "000000.fffe.000000" {
		t.Fatalf("gmIdentity = %q/%v, want 000000.fffe.000000/known", s.GMIdentity, s.GMIdentityKnown)
	}
}

func TestParseTimeStatusNPSlave(t *testing.T) {
	out := readFixture(t, "time_status_np.slave.txt")
	s := parseTimeStatusNP(out)
	if !s.MasterOffsetKnown || s.MasterOffsetNs != -42 {
		t.Fatalf("master_offset = %d/%v, want -42/known", s.MasterOffsetNs, s.MasterOffsetKnown)
	}
	if !s.GMPresentKnown || s.GMPresent != true {
		t.Fatalf("gmPresent = %v/%v, want true/known", s.GMPresent, s.GMPresentKnown)
	}
	if s.GMIdentity != "3cecef.fffe.a1b2c3" {
		t.Fatalf("gmIdentity = %q", s.GMIdentity)
	}
}

func TestParsePortDataSetMaster(t *testing.T) {
	out := readFixture(t, "port_data_set.master.txt")
	s := parsePortDataSet(out)
	if !s.PortStateKnown || s.PortState != "MASTER" {
		t.Fatalf("portState = %q/%v, want MASTER/known", s.PortState, s.PortStateKnown)
	}
}

func TestParsePortDataSetSlave(t *testing.T) {
	out := readFixture(t, "port_data_set.slave.txt")
	s := parsePortDataSet(out)
	if s.PortState != "SLAVE" {
		t.Fatalf("portState = %q, want SLAVE", s.PortState)
	}
}

func TestParsePortDataSetListening(t *testing.T) {
	out := readFixture(t, "port_data_set.listening.txt")
	s := parsePortDataSet(out)
	if s.PortState != "LISTENING" {
		t.Fatalf("portState = %q, want LISTENING", s.PortState)
	}
	if portStateLocked(s.PortState) {
		t.Fatalf("LISTENING must not be reported as locked")
	}
}

func TestParseTimePropertiesDataSetArb(t *testing.T) {
	out := readFixture(t, "time_properties_data_set.arb.txt")
	s := parseTimePropertiesDataSet(out)
	if !s.PTPTimescaleKnown || s.PTPTimescale != false {
		t.Fatalf("ptpTimescale = %v/%v, want false/known", s.PTPTimescale, s.PTPTimescaleKnown)
	}
}

func TestParseTimePropertiesDataSetPTP(t *testing.T) {
	out := readFixture(t, "time_properties_data_set.ptp.txt")
	s := parseTimePropertiesDataSet(out)
	if !s.PTPTimescaleKnown || s.PTPTimescale != true {
		t.Fatalf("ptpTimescale = %v/%v, want true/known", s.PTPTimescale, s.PTPTimescaleKnown)
	}
}

func TestParseDefaultDataSet(t *testing.T) {
	out := readFixture(t, "default_data_set.txt")
	s := parseDefaultDataSet(out)
	if !s.DomainNumberKnown || s.DomainNumber != 24 {
		t.Fatalf("domainNumber = %d/%v, want 24/known", s.DomainNumber, s.DomainNumberKnown)
	}
	if !s.ClockClassKnown || s.ClockClass != 248 {
		t.Fatalf("clockClass = %d/%v, want 248/known", s.ClockClass, s.ClockClassKnown)
	}
}

func TestParseAgainstEmptyOutputReportsUnknown(t *testing.T) {
	s := parseTimeStatusNP("")
	if s.MasterOffsetKnown || s.GMPresentKnown || s.GMIdentityKnown {
		t.Fatalf("empty pmc output must report every field unknown, got %+v", s)
	}
}

// TestParseAgainstOnlySendingLine covers the real shape pmc prints when a
// management request gets no response at all (wrong domain, ptp4l down,
// or a query the running version does not implement) — exactly this
// seam's own bench discovered running pmc against a live ptp4l with a
// mismatched -d: only "sending: GET <name>\n", no RESPONSE block.
func TestParseAgainstOnlySendingLine(t *testing.T) {
	s := parseTimeStatusNP("sending: GET TIME_STATUS_NP\n")
	if s.MasterOffsetKnown {
		t.Fatalf("a bare \"sending:\" line must not be read as evidence")
	}
}

func TestPortStateToRole(t *testing.T) {
	cases := map[string]Role{"MASTER": RoleGrandmaster, "SLAVE": RoleFollower, "PASSIVE": RolePassive, "LISTENING": RoleListening}
	for portState, want := range cases {
		got, ok := portStateToRole(portState)
		if !ok || got != want {
			t.Errorf("portStateToRole(%q) = %q/%v, want %q/true", portState, got, ok, want)
		}
	}
	if _, ok := portStateToRole("FAULTY"); ok {
		t.Errorf("portStateToRole(FAULTY) should report unknown, not a guessed role")
	}
}
