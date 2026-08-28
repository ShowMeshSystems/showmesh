package clock

import (
	"os"
	"testing"
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
