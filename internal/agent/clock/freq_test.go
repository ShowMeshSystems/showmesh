package clock

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// TestScaledFreqToPPM checks the kernel's scaled-by-65536 conversion
// against the real node reading RES-019 §1 records: /dev/ptp0's freq
// 1001812 is +15.286 ppm, CLOCK_REALTIME's 967778 is +14.767 ppm
// (2026-09-21, this seam's own WORKER-BRIEF). A negative freq must
// convert to a negative ppm, sign included.
func TestScaledFreqToPPM(t *testing.T) {
	cases := []struct {
		freq int64
		want float64
	}{
		{1001812, 15.286},
		{967778, 14.767},
		{0, 0},
		{-65536, -1},
	}
	for _, tc := range cases {
		got := scaledFreqToPPM(tc.freq)
		diff := got - tc.want
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.001 {
			t.Errorf("scaledFreqToPPM(%d) = %v, want %v", tc.freq, got, tc.want)
		}
	}
}

// fakeFrequencyReaders overrides phcFrequencyPPM/realtimeFrequencyPPM for
// the duration of one test, restoring the originals on cleanup, matching
// fakePHCLookup's own indirection-faking pattern.
func fakeFrequencyReaders(t *testing.T, phcVal float64, phcErr error, rtVal float64, rtErr error) {
	t.Helper()
	origPHC, origRT := phcFrequencyPPM, realtimeFrequencyPPM
	phcFrequencyPPM = func(index int) (float64, error) {
		if phcErr != nil {
			return 0, phcErr
		}
		return phcVal, nil
	}
	realtimeFrequencyPPM = func() (float64, error) {
		if rtErr != nil {
			return 0, rtErr
		}
		return rtVal, nil
	}
	t.Cleanup(func() { phcFrequencyPPM, realtimeFrequencyPPM = origPHC, origRT })
}

// TestFrequencyPPMForModeHardwarePicksTheDevice proves hardware
// timestamping reads the interface's own PHC, not CLOCK_REALTIME.
func TestFrequencyPPMForModeHardwarePicksTheDevice(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	fakeFrequencyReaders(t, 15.286, nil, 999, nil)

	ppm, ok, reason := frequencyPPMForMode("eno2", TimestampingHardware)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if ppm != 15.286 {
		t.Errorf("ppm = %v, want the PHC reading 15.286, not the realtime one", ppm)
	}
	if reason != "" {
		t.Errorf("reason = %q, want empty when ok is true", reason)
	}
}

// TestFrequencyPPMForModeSoftwarePicksTheSystemClock proves software
// timestamping reads CLOCK_REALTIME, never a PHC index.
func TestFrequencyPPMForModeSoftwarePicksTheSystemClock(t *testing.T) {
	fakeFrequencyReaders(t, 999, nil, 14.767, nil)

	ppm, ok, _ := frequencyPPMForMode("eth0", TimestampingSoftware)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if ppm != 14.767 {
		t.Errorf("ppm = %v, want the realtime reading 14.767, not the PHC one", ppm)
	}
}

// TestFrequencyPPMForModeHardwareNoPHCReportsUnknown proves an interface
// with no PHC reports unknown under hardware mode, naming the interface,
// rather than silently falling back to CLOCK_REALTIME.
func TestFrequencyPPMForModeHardwareNoPHCReportsUnknown(t *testing.T) {
	fakePHCLookup(t, 0, false, nil, nil)
	_, ok, reason := frequencyPPMForMode("eth0", TimestampingHardware)
	if ok {
		t.Fatalf("ok = true, want false: eth0 has no PHC")
	}
	if !strings.Contains(reason, "eth0") {
		t.Errorf("reason = %q, want it to name the interface", reason)
	}
}

// TestFrequencyPPMForModeReadErrorReportsUnknown proves a device read
// failure (permission denied, device missing) reports unknown with the
// OS error text, never a fabricated zero.
func TestFrequencyPPMForModeReadErrorReportsUnknown(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	fakeFrequencyReaders(t, 0, errors.New("open /dev/ptp0: permission denied"), 0, nil)

	_, ok, reason := frequencyPPMForMode("eno2", TimestampingHardware)
	if ok {
		t.Fatalf("ok = true, want false: the PHC read failed")
	}
	if !strings.Contains(reason, "permission denied") {
		t.Errorf("reason = %q, want the OS error text surfaced", reason)
	}
}

// TestFrequencyPPMForModePlatformUnavailableReportsUnknown proves a
// platform that cannot read the frequency at all (the non-Linux stub)
// reports unknown with that fact stated, not a fabricated zero. Skipped
// where the real Linux syscall would otherwise run and mask the stub.
func TestFrequencyPPMForModePlatformUnavailableReportsUnknown(t *testing.T) {
	origPHC, origRT := phcFrequencyPPM, realtimeFrequencyPPM
	phcFrequencyPPM = PHCFrequencyPPM
	realtimeFrequencyPPM = RealtimeFrequencyPPM
	t.Cleanup(func() { phcFrequencyPPM, realtimeFrequencyPPM = origPHC, origRT })

	if runtime.GOOS == "linux" {
		t.Skip("skipping: CLOCK_REALTIME is genuinely readable on Linux, this test covers the non-Linux stub")
	}
	_, ok, reason := frequencyPPMForMode("eth0", TimestampingSoftware)
	if ok {
		t.Fatalf("ok = true, want false: this platform cannot read CLOCK_REALTIME's frequency")
	}
	if !strings.Contains(reason, "platform") {
		t.Errorf("reason = %q, want it to say this platform cannot read it", reason)
	}
}
