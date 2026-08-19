package audio

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

var errBoom = errors.New("boom")

type fakeEnumerator struct {
	devices    []string
	devicesErr error
	hasCards   bool
	cardsErr   error
}

func (f fakeEnumerator) Devices(ctx context.Context) ([]string, error) {
	return f.devices, f.devicesErr
}
func (f fakeEnumerator) HasHardwareCards(ctx context.Context) (bool, error) {
	return f.hasCards, f.cardsErr
}

func deviceFromArgv(argv []string) string {
	for _, a := range argv {
		if strings.HasPrefix(a, "device=") {
			return strings.TrimPrefix(a, "device=")
		}
	}
	return ""
}

// alwaysPlayingRunner reports PLAYING with fixed achieved caps for every
// device it is asked to probe, and records (thread-safely — Discover's own
// probes are sequential, but a stray future concurrent caller must not
// race this) every device argument it was called with.
func alwaysPlayingRunner() (run probeRunner, probed func() []string) {
	var mu sync.Mutex
	var seen []string
	run = func(ctx context.Context, path string, argv []string) (string, bool) {
		mu.Lock()
		seen = append(seen, deviceFromArgv(argv))
		mu.Unlock()
		return playingOutput(48000, 2, "S16LE"), true
	}
	probed = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	return run, probed
}

// channelsRequested extracts the channel count requested via the
// "channels=N" caps clause, or 0 if the probe's argv requested no
// specific channel count (an unconstrained probe).
func channelsRequested(argv []string) int {
	for _, a := range argv {
		if strings.Contains(a, "channels=") {
			idx := strings.Index(a, "channels=")
			n := 0
			for _, c := range a[idx+len("channels="):] {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			return n
		}
	}
	return 0
}

// channelAwareRunner reports 2 achieved channels for an unconstrained
// probe and 4 for any probe that explicitly requested at least 3 — this is
// what lets a test prove [Discover] issues a SEPARATE, explicitly
// LTC-constrained probe rather than inferring LTC capability from an
// unconstrained probe's own achieved channel count.
func channelAwareRunner() probeRunner {
	return func(ctx context.Context, path string, argv []string) (string, bool) {
		if channelsRequested(argv) >= 3 {
			return playingOutput(48000, 4, "S16LE"), true
		}
		return playingOutput(48000, 2, "S16LE"), true
	}
}

// TestDiscoverEngineUsableIndependentOfHardware proves the middle state: a
// node whose GStreamer/ALSA chain works but has no real card gets
// EngineUsable=true and zero routes — never conflated into "no audio
// capability at all" the way CandidateDevices alone would suggest.
func TestDiscoverEngineUsableIndependentOfHardware(t *testing.T) {
	fakeGstLaunch(t)
	run, _ := alwaysPlayingRunner()
	withProbeRunner(t, run)

	enum := fakeEnumerator{devices: []string{"null", "default"}, hasCards: false}
	d := Discover(context.Background(), enum)

	if !d.EngineUsable {
		t.Error("Discover.EngineUsable = false, want true (the virtual device probe succeeded)")
	}
	if d.HasHardwareCards {
		t.Error("Discover.HasHardwareCards = true, want false")
	}
	if len(d.Routes) != 0 {
		t.Errorf("Discover.Routes = %v, want none (no hardware cards means no candidate routes)", d.Routes)
	}
	if !d.HardwareEnumerated {
		t.Error("Discover.HardwareEnumerated = false, want true: enumeration itself succeeded, it just found no cards")
	}
}

// TestDiscoverProbesOnlyRealHardwareCandidates proves the null/default
// devices are never handed to ProbeOutput as if they were real routes,
// even though they are always present in the enumerated device list.
func TestDiscoverProbesOnlyRealHardwareCandidates(t *testing.T) {
	fakeGstLaunch(t)
	run, probed := alwaysPlayingRunner()
	withProbeRunner(t, run)

	enum := fakeEnumerator{
		devices:  []string{"null", "default", "hw:CARD=PCH,DEV=0", "hw:CARD=USB,DEV=0"},
		hasCards: true,
	}
	d := Discover(context.Background(), enum)

	if len(d.Routes) != 2 {
		t.Fatalf("Discover.Routes has %d entries, want 2 (the two real devices only): %+v", len(d.Routes), d.Routes)
	}
	// probed() includes the engine's own "null" probe (called first, before
	// enumeration) plus exactly the two real candidates — never "default".
	all := probed()
	for _, dev := range all[1:] {
		if dev == "null" || dev == "default" {
			t.Errorf("Discover probed virtual device %q as a candidate route, want only real hardware", dev)
		}
	}
}

// TestDiscoverTruncatesAtMaxProbedDevices proves more real candidates than
// the cap allows are enumerated but not all probed, and the truncation is
// stated rather than silently dropped.
func TestDiscoverTruncatesAtMaxProbedDevices(t *testing.T) {
	fakeGstLaunch(t)
	run, _ := alwaysPlayingRunner()
	withProbeRunner(t, run)

	devices := []string{"null", "default"}
	for i := 0; i < maxProbedDevices+3; i++ {
		devices = append(devices, "hw:CARD=X"+itoa(i)+",DEV=0")
	}
	enum := fakeEnumerator{devices: devices, hasCards: true}
	d := Discover(context.Background(), enum)

	if !d.Truncated {
		t.Error("Discover.Truncated = false, want true")
	}
	if len(d.Routes) != maxProbedDevices {
		t.Errorf("Discover.Routes has %d entries, want exactly maxProbedDevices=%d", len(d.Routes), maxProbedDevices)
	}
	if d.EnumeratedCount != len(devices) {
		t.Errorf("Discover.EnumeratedCount = %d, want %d (the full enumerated count, not the truncated one)", d.EnumeratedCount, len(devices))
	}
}

// TestDiscoverNoHardwareCardsProducesNoRoutesEvenWithNamedCandidates is the
// decisive "enumerated cleanly, found nothing" case exercised through the
// full Discover path, not just CandidateDevices in isolation.
func TestDiscoverNoHardwareCardsProducesNoRoutesEvenWithNamedCandidates(t *testing.T) {
	fakeGstLaunch(t)
	run, probed := alwaysPlayingRunner()
	withProbeRunner(t, run)

	enum := fakeEnumerator{devices: []string{"null", "default", "hw:CARD=GHOST,DEV=0"}, hasCards: false}
	d := Discover(context.Background(), enum)

	if len(d.Routes) != 0 {
		t.Errorf("Discover.Routes = %+v, want none", d.Routes)
	}
	if !d.HardwareEnumerated {
		t.Error("Discover.HardwareEnumerated = false, want true")
	}
	// Only the engine's own "null" probe should have run.
	if all := probed(); len(all) != 1 || all[0] != "null" {
		t.Errorf("probed devices = %v, want exactly [null]", all)
	}
}

// TestDiscoverDeviceEnumerationErrorIsUnknownNotAbsent proves "we could
// not enumerate" (aplay -L itself failed) is distinguishable from "we
// enumerated and there is no card": HardwareEnumerated is false and the
// real error text is carried, never silently folded into HasHardwareCards
// staying false by coincidence.
func TestDiscoverDeviceEnumerationErrorIsUnknownNotAbsent(t *testing.T) {
	fakeGstLaunch(t)
	run, probed := alwaysPlayingRunner()
	withProbeRunner(t, run)

	enum := fakeEnumerator{devicesErr: errBoom}
	d := Discover(context.Background(), enum)

	if d.HardwareEnumerated {
		t.Error("Discover.HardwareEnumerated = true, want false: aplay -L itself failed")
	}
	if d.HardwareEnumeratedReason == "" || !strings.Contains(d.HardwareEnumeratedReason, "boom") {
		t.Errorf("Discover.HardwareEnumeratedReason = %q, want the underlying error text carried through", d.HardwareEnumeratedReason)
	}
	if len(d.Routes) != 0 {
		t.Errorf("Discover.Routes = %v, want none", d.Routes)
	}
	// Only the engine's own "null" probe should have run; enumeration
	// failed before any candidate could even be named.
	if all := probed(); len(all) != 1 || all[0] != "null" {
		t.Errorf("probed devices = %v, want exactly [null]", all)
	}
}

// TestDiscoverHasHardwareCardsErrorIsUnknownNotAbsent is the identical
// proof one level down: `aplay -L` succeeds but `aplay -l` itself fails.
func TestDiscoverHasHardwareCardsErrorIsUnknownNotAbsent(t *testing.T) {
	fakeGstLaunch(t)
	run, _ := alwaysPlayingRunner()
	withProbeRunner(t, run)

	enum := fakeEnumerator{devices: []string{"null", "default", "hw:CARD=PCH,DEV=0"}, cardsErr: errBoom}
	d := Discover(context.Background(), enum)

	if d.HardwareEnumerated {
		t.Error("Discover.HardwareEnumerated = true, want false: aplay -l itself failed")
	}
	if d.HardwareEnumeratedReason == "" || !strings.Contains(d.HardwareEnumeratedReason, "boom") {
		t.Errorf("Discover.HardwareEnumeratedReason = %q, want the underlying error text carried through", d.HardwareEnumeratedReason)
	}
	if len(d.Routes) != 0 {
		t.Errorf("Discover.Routes = %v, want none", d.Routes)
	}
}

// TestDiscoverLTCChannelsComesFromASeparateConstrainedProbe proves finding
// 3: a route's LTCChannels reflects a SEPARATE probe that explicitly
// requested at least MinLTCChannels, never the unconstrained probe's own
// achieved Channels — channelAwareRunner reports different achieved
// channel counts for the two, so the two fields can only both be correct
// if two distinct probe calls actually happened.
func TestDiscoverLTCChannelsComesFromASeparateConstrainedProbe(t *testing.T) {
	fakeGstLaunch(t)
	withProbeRunner(t, channelAwareRunner())

	enum := fakeEnumerator{devices: []string{"null", "default", "hw:CARD=PCH,DEV=0"}, hasCards: true}
	d := Discover(context.Background(), enum)

	if len(d.Routes) != 1 {
		t.Fatalf("Discover.Routes has %d entries, want 1", len(d.Routes))
	}
	route := d.Routes[0]
	if route.Channels != 2 {
		t.Errorf("route.Channels = %d, want 2 (the unconstrained probe's own achieved count)", route.Channels)
	}
	if route.LTCChannels != 4 {
		t.Errorf("route.LTCChannels = %d, want 4 (the separate, explicitly-constrained probe's achieved count)", route.LTCChannels)
	}
}
