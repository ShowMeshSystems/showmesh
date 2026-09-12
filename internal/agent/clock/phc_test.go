//go:build linux

package clock

import (
	"os"
	"testing"
	"time"
)

// TestFdToClockIDMatchesFPPMacro checks fdToClockID against hand-computed
// values of RES-019 section 1's own macro, ((~(clockid_t)(fd) << 3) | 3),
// evaluated in 32-bit two's complement (clockid_t is a 32-bit int on
// Linux) — the exact arithmetic a C compiler would produce, computed here
// independently rather than by calling fdToClockID's own code back at
// itself.
func TestFdToClockIDMatchesFPPMacro(t *testing.T) {
	cases := []struct {
		fd   int
		want int32
	}{
		{0, -5},
		{1, -13},
		{3, -29},
		{7, -61},
	}
	for _, tc := range cases {
		got := fdToClockID(tc.fd)
		if got != tc.want {
			t.Errorf("fdToClockID(%d) = %d, want %d", tc.fd, got, tc.want)
		}
	}
}

func TestPHCIndexForInterfaceLoopbackHasNone(t *testing.T) {
	// lo has no PHC on every platform this runs on; this exercises the
	// ok=false path (never an error for "this interface genuinely has no
	// PHC", per PHCIndexForInterface's own doc comment) without needing
	// real PTP hardware.
	_, ok, err := PHCIndexForInterface("lo")
	if err != nil {
		t.Fatalf("unexpected error for lo: %v", err)
	}
	if ok {
		t.Fatalf("expected lo to report no PHC")
	}
}

func TestPHCIndexForInterfaceUnknownInterfaceErrors(t *testing.T) {
	_, _, err := PHCIndexForInterface("showmesh-does-not-exist-0")
	if err == nil {
		t.Fatalf("expected an error for a nonexistent interface")
	}
}

func TestOpenPHCMissingDeviceFailsHonestly(t *testing.T) {
	// No PHC device exists on this VM (docs/build/BUILD-LOG.md: hardware
	// timestamping is unverified in this environment) — OpenPHC must
	// report that failure honestly rather than silently falling back to
	// anything else (RES-019 section 1's own requirement).
	if _, err := os.Stat("/dev/ptp0"); err == nil {
		t.Skip("a real /dev/ptp0 exists on this machine; this test only covers the absent-device path")
	}
	_, err := OpenPHC(0)
	if err == nil {
		t.Fatalf("expected an error opening a nonexistent PHC device")
	}
}

// TestReadPHCCallCost measures a single already-open [PHCReader]'s Now()
// cost against a real PHC device: RES-019 section 7.2 candidate A's
// pipeline clock callback keeps one open for the engine's whole life
// specifically so this per-call cost is only the clock_gettime syscall
// itself, never an open/close pair; this records what that syscall
// actually costs on real hardware rather than assuming it. Skipped, with
// its own stated reason, on any host with no /dev/ptp0 -- this
// development machine and most CI runners have none, matching every
// other real-hardware-gated test in this package.
func TestReadPHCCallCost(t *testing.T) {
	if _, err := os.Stat("/dev/ptp0"); err != nil {
		t.Skip("skipping: no /dev/ptp0 on this host; this test needs a real PHC device")
	}
	r, err := OpenPHC(0)
	if err != nil {
		t.Skipf("skipping: /dev/ptp0 exists but could not be opened: %v", err)
	}
	defer func() { _ = r.Close() }()

	const calls = 10000
	start := time.Now()
	for i := 0; i < calls; i++ {
		if _, err := r.Now(); err != nil {
			t.Fatalf("Now: %v", err)
		}
	}
	elapsed := time.Since(start)
	perCall := elapsed / calls
	t.Logf("PHCReader.Now (clock_gettime on an already-open /dev/ptp0): %d calls in %s (%s/call)", calls, elapsed, perCall)
	if perCall > time.Millisecond {
		t.Fatalf("PHCReader.Now took %s/call; want well under 1ms (GStreamer's own pipeline clock calls this from its scheduling thread)", perCall)
	}
}
