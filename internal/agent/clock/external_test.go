package clock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePHCLookup overrides the package-level phcIndexForInterface/readPHC
// seams (clock.go) for the duration of one test, restoring the real
// [PHCIndexForInterface]/[ReadPHC] implementations on cleanup. This is
// what makes ExternalProvider.Now's PHC-trust branches exercisable on a
// host with no real PTP hardware clock (this build VM included, and most
// CI/dev hosts): the real functions can only ever report "no PHC" here,
// so a test that needs "this interface HAS a PHC at index N" must fake
// it.
func fakePHCLookup(t *testing.T, index int, hasPHC bool, lookupErr error, readErr error) {
	t.Helper()
	origIndex, origRead := phcIndexForInterface, readPHC
	phcIndexForInterface = func(iface string) (int, bool, error) {
		return index, hasPHC, lookupErr
	}
	readPHC = func(idx int) (time.Time, error) {
		if readErr != nil {
			return time.Time{}, readErr
		}
		return time.Unix(1_700_000_000, 0), nil
	}
	t.Cleanup(func() { phcIndexForInterface, readPHC = origIndex, origRead })
}

// TestExternalProviderPMCUnavailableReportsUnknownNotFailed covers the
// second half of the pmc-socket bug: pmc's own client-side setup failing
// (here, the binary is simply missing) is never evidence that the
// OBSERVED ptp4l is down, and must not report StateFailed. The read-only
// UDS socket exists and is healthy; only the agent's own pmc tooling is
// broken.
func TestExternalProviderPMCUnavailableReportsUnknownNotFailed(t *testing.T) {
	uds := filepath.Join(t.TempDir(), "ptp4lro")
	if err := os.WriteFile(uds, nil, 0o666); err != nil {
		t.Fatalf("create fake socket file: %v", err)
	}

	orig := pmcBinary
	pmcBinary = filepath.Join(t.TempDir(), "no-such-pmc")
	defer func() { pmcBinary = orig }()

	p := NewExternalProvider(ExternalConfig{Interface: "eth0", UDSAddress: uds})
	raw := p.Poll(context.Background())

	if !raw.Reachable {
		t.Fatalf("pmc tooling failure must not report Reachable=false (that reads as StateFailed): %+v", raw)
	}
	if raw.Locked {
		t.Fatalf("a failed pmc invocation must never claim a lock: %+v", raw)
	}
	if raw.Reason == "" {
		t.Fatalf("Reason is required when the state is not locked")
	}

	tr := NewTracker(&fakeProvider{raws: []RawStatus{raw}}, TrackerConfig{}, nil)
	status := tr.Poll(context.Background())
	if status.State == StateFailed {
		t.Fatalf("pmc tooling failure must not surface as node.clock.ptp.state=failed, got %q with reason %q", status.State, status.Reason)
	}
}

// TestExternalProviderNowNoPHCUsesRealtimeRegardlessOfDeclaration is
// branch 1: an interface with no PHC at all is unchanged by this seam,
// whether or not phcDevice happens to be empty (the common case).
func TestExternalProviderNowNoPHCUsesRealtimeRegardlessOfDeclaration(t *testing.T) {
	fakePHCLookup(t, 0, false, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eth0"})

	before := time.Now()
	mt := p.Now(context.Background())
	after := time.Now()

	if !mt.Valid {
		t.Fatalf("Valid = false, want true: no PHC means software timestamping, always usable")
	}
	if mt.Time.Before(before) || mt.Time.After(after) {
		t.Errorf("Time = %v, want between %v and %v (CLOCK_REALTIME, not a PHC read)", mt.Time, before, after)
	}
	if !strings.Contains(mt.Reason, "no hardware clock") {
		t.Errorf("Reason = %q, want it to state this interface has no hardware clock", mt.Reason)
	}
	// The software fallback must never be mistaken for a PHC read.
	if strings.Contains(mt.Reason, "Media time is from the hardware clock") {
		t.Errorf("Reason = %q, a software-timestamp reading must never claim to be a PHC read", mt.Reason)
	}
}

// TestExternalProviderNowRefusesDeclaredPHCOnAnInterfaceWithNone is
// branch 2: a phcDevice declared against an interface that turns out to
// have no PHC at all is a misconfiguration, refused by name rather than
// silently falling back to CLOCK_REALTIME as if nothing had been
// declared.
func TestExternalProviderNowRefusesDeclaredPHCOnAnInterfaceWithNone(t *testing.T) {
	fakePHCLookup(t, 0, false, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eth0", PHCDevice: "/dev/ptp0"})

	mt := p.Now(context.Background())
	if mt.Valid {
		t.Fatalf("Valid = true, want false: phcDevice was declared but eth0 has no PHC at all")
	}
	if !strings.Contains(mt.Reason, "/dev/ptp0") || !strings.Contains(mt.Reason, "eth0") || !strings.Contains(mt.Reason, "no hardware clock") {
		t.Errorf("Reason = %q, want it to name both the declared device and the interface", mt.Reason)
	}
}

// TestExternalProviderNowRefusesPHCPresentWithNoDeclaration is branch 3:
// today's refusal, unchanged, when a PHC exists and the operator has
// declared nothing.
func TestExternalProviderNowRefusesPHCPresentWithNoDeclaration(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2"})

	mt := p.Now(context.Background())
	if mt.Valid {
		t.Fatalf("Valid = true, want false: a PHC exists and nothing confirms the observed ptp4l reached hardware timestamping")
	}
	if !strings.Contains(mt.Reason, "eno2") || !strings.Contains(mt.Reason, "cannot tell if it is being used") {
		t.Errorf("Reason = %q, want the refusal wording naming the interface", mt.Reason)
	}
}

// TestExternalProviderNowRefusesMismatchedPHCDevice is branch 4: the
// interface's own PHC index disagrees with the declared device.
func TestExternalProviderNowRefusesMismatchedPHCDevice(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "/dev/ptp1"})

	mt := p.Now(context.Background())
	if mt.Valid {
		t.Fatalf("Valid = true, want false: declared /dev/ptp1 does not match the interface's own PHC index 0")
	}
	if !strings.Contains(mt.Reason, "/dev/ptp1") || !strings.Contains(mt.Reason, "/dev/ptp0") {
		t.Errorf("Reason = %q, want it to name both the declared device and the interface's own PHC", mt.Reason)
	}
}

// TestExternalProviderNowReadsDeclaredMatchingPHCDevice is branch 5's
// success path: the declared device matches the interface's own PHC
// index, so the PHC is read, and the reading's own Reason says plainly
// that this is an operator-declared attestation, never a verified
// hardware-timestamping read.
func TestExternalProviderNowReadsDeclaredMatchingPHCDevice(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "/dev/ptp0"})

	mt := p.Now(context.Background())
	if !mt.Valid {
		t.Fatalf("Valid = false, want true: the declared device matches the interface's own PHC; Reason: %s", mt.Reason)
	}
	if !mt.Time.Equal(time.Unix(1_700_000_000, 0)) {
		t.Errorf("Time = %v, want the fake PHC reading", mt.Time)
	}
	if !strings.Contains(mt.Reason, "Media time is from the hardware clock") || !strings.Contains(mt.Reason, "/dev/ptp0") {
		t.Errorf("Reason = %q, want it to say media time is from the declared hardware clock", mt.Reason)
	}
}

// TestExternalProviderNowRefusesWhenDeclaredPHCReadFails is branch 5's
// own failure path: a matching device that cannot actually be opened
// refuses with ReadPHC's own reason, never a fabricated reading.
func TestExternalProviderNowRefusesWhenDeclaredPHCReadFails(t *testing.T) {
	readErr := errors.New("open /dev/ptp0: permission denied")
	fakePHCLookup(t, 0, true, nil, readErr)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "/dev/ptp0"})

	mt := p.Now(context.Background())
	if mt.Valid {
		t.Fatalf("Valid = true, want false: ReadPHC itself failed")
	}
	if !strings.Contains(mt.Reason, "permission denied") {
		t.Errorf("Reason = %q, want ReadPHC's own error surfaced", mt.Reason)
	}
}

// TestExternalProviderNowRefusesUnparsablePHCDevice proves a malformed
// phcDevice (never expected from the coordinator, which already validates
// the pattern, but never trusted blindly by the agent either) is refused
// honestly rather than causing a panic or a wrong PHC index.
func TestExternalProviderNowRefusesUnparsablePHCDevice(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "not-a-device"})

	mt := p.Now(context.Background())
	if mt.Valid {
		t.Fatalf("Valid = true, want false: phcDevice does not name a PTP clock device")
	}
	if !strings.Contains(mt.Reason, "not-a-device") {
		t.Errorf("Reason = %q, want it to name the unparsable value", mt.Reason)
	}
}

// TestExternalProviderFrequencyPPMNoPHCReadsRealtime mirrors
// [TestExternalProviderNowNoPHCUsesRealtimeRegardlessOfDeclaration]:
// software timestamping is CLOCK_REALTIME's own frequency, not a PHC's.
func TestExternalProviderFrequencyPPMNoPHCReadsRealtime(t *testing.T) {
	fakePHCLookup(t, 0, false, nil, nil)
	fakeFrequencyReaders(t, 999, nil, 14.767, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eth0"})

	ppm, ok := p.frequencyPPM()
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if ppm != 14.767 {
		t.Errorf("ppm = %v, want the realtime reading 14.767", ppm)
	}
}

// TestExternalProviderFrequencyPPMUndeclaredPHCReportsUnknown mirrors
// [TestExternalProviderNowRefusesPHCPresentWithNoDeclaration]: without an
// operator attestation this provider cannot tell whether the PHC is
// actually what is being disciplined.
func TestExternalProviderFrequencyPPMUndeclaredPHCReportsUnknown(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2"})

	if _, ok := p.frequencyPPM(); ok {
		t.Fatalf("ok = true, want false: no phcDevice declared")
	}
}

// TestExternalProviderFrequencyPPMDeclaredMatchingPHCReadsDevice mirrors
// [TestExternalProviderNowReadsDeclaredMatchingPHCDevice]: a declared
// device matching the interface's own PHC index is read.
func TestExternalProviderFrequencyPPMDeclaredMatchingPHCReadsDevice(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	fakeFrequencyReaders(t, 15.286, nil, 999, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "/dev/ptp0"})

	ppm, ok := p.frequencyPPM()
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if ppm != 15.286 {
		t.Errorf("ppm = %v, want the PHC reading 15.286", ppm)
	}
}

// TestExternalProviderFrequencyPPMMismatchedPHCReportsUnknown mirrors
// [TestExternalProviderNowRefusesMismatchedPHCDevice].
func TestExternalProviderFrequencyPPMMismatchedPHCReportsUnknown(t *testing.T) {
	fakePHCLookup(t, 0, true, nil, nil)
	p := NewExternalProvider(ExternalConfig{Interface: "eno2", PHCDevice: "/dev/ptp1"})

	if _, ok := p.frequencyPPM(); ok {
		t.Fatalf("ok = true, want false: declared /dev/ptp1 does not match the interface's own PHC index 0")
	}
}
