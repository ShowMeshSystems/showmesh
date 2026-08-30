package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

func withAudioDiscoverer(t *testing.T, d audio.Discovery) {
	t.Helper()
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery { return d }
	t.Cleanup(func() { audioDiscoverer = orig })
}

// withAudioEngineAvailable drives [audioEngineAvailable] deterministically,
// matching withAudioDiscoverer's own injection convention. Every test in
// this file that predates the engine-availability gate calls this with
// ok=true to keep asserting exactly the probe-evidence behavior it always
// has; TestDetectAudioCapabilitiesEngineGatedOnAvailability and
// TestFakeAudioEngineNeverAdvertisesPlaybackCapability are what actually
// exercise the gate itself.
func withAudioEngineAvailable(t *testing.T, ok bool, reason string) {
	t.Helper()
	orig := audioEngineAvailable
	audioEngineAvailable = func() (bool, string) { return ok, reason }
	t.Cleanup(func() { audioEngineAvailable = orig })
}

// TestDetectAudioCapabilitiesNoEngineReturnsNil proves a node with no
// usable audio engine (every render node, the development laptop)
// advertises no audio capability at all.
func TestDetectAudioCapabilitiesNoEngineReturnsNil(t *testing.T) {
	withAudioDiscoverer(t, audio.Discovery{EngineUsable: false, EngineReason: "gst-launch-1.0 not found"})

	set := detectAudioCapabilities(context.Background())
	if len(set) != 0 {
		t.Errorf("detectAudioCapabilities(no engine) = %v, want empty", set)
	}
}

// TestDetectAudioCapabilitiesEngineOnlyNoHardware proves the middle state:
// engine usable, but every enumerated route failed its own probe, so
// audio.engine is advertised alone. Routes is non-empty (a route that WAS
// enumerated but never reached PLAYING) so this test actually exercises
// splitUsableRoutes's Available/Channels gate, rather than merely relying
// on Routes defaulting to nil — a Discovery literal with only
// HasHardwareCards set proves nothing here, since detectAudioCapabilities
// never reads that field itself.
func TestDetectAudioCapabilitiesEngineOnlyNoHardware(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=GHOST,DEV=0", ProbeResult: audio.ProbeResult{Available: false, Channels: 2, Reason: "could not open audio device"}},
		},
	})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.engine"); !ok {
		t.Error("audio.engine not advertised, want present")
	}
	for _, id := range audioSessionCapabilityIDs {
		if _, ok := set.Lookup(id); !ok {
			t.Errorf("%s not advertised alongside audio.engine, want present", id)
		}
	}
	if _, ok := set.Lookup("audio.output.local"); ok {
		t.Error("audio.output.local advertised from a route that never reached PLAYING, want absent")
	}
	if _, ok := set.Lookup("audio.output.ltc"); ok {
		t.Error("audio.output.ltc advertised from a route that never reached PLAYING, want absent")
	}
}

// TestDetectAudioCapabilitiesLocalWithoutLTC proves a 2-channel-only route
// advertises audio.output.local but never audio.output.ltc.
func TestDetectAudioCapabilitiesLocalWithoutLTC(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=PCH,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"}},
		},
	})

	set := detectAudioCapabilities(context.Background())
	local, ok := set.Lookup("audio.output.local")
	if !ok {
		t.Fatal("audio.output.local not advertised, want present")
	}
	if local.Attributes["outputCount"] != 1 {
		t.Errorf("audio.output.local outputCount = %v, want 1", local.Attributes["outputCount"])
	}
	if _, ok := set.Lookup("audio.output.ltc"); ok {
		t.Error("audio.output.ltc advertised for a 2-channel-only route, want absent")
	}
}

// TestDetectAudioCapabilitiesLTCRequiresThreeChannels proves the LTC
// threshold: a route whose SEPARATE LTC-constrained probe achieved 4
// channels advertises both local and ltc, with the attribute stating that
// achieving the count is not evidence of physical discreteness.
func TestDetectAudioCapabilitiesLTCRequiresThreeChannels(t *testing.T) {
	if minLTCChannels != 3 {
		t.Fatalf("minLTCChannels = %d, want the ADR-018 threshold pinned at 3", minLTCChannels)
	}

	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{
				Device:      "hw:CARD=X,DEV=0",
				ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
				LTCChannels: 4,
			},
		},
	})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.output.local"); !ok {
		t.Error("audio.output.local not advertised, want present")
	}
	ltc, ok := set.Lookup("audio.output.ltc")
	if !ok {
		t.Fatal("audio.output.ltc not advertised for a route whose LTC probe achieved 4 channels, want present")
	}
	if ltc.Attributes["physicalDiscretenessVerified"] != false {
		t.Errorf("physicalDiscretenessVerified = %v, want false (that check is C0b, not discovery)", ltc.Attributes["physicalDiscretenessVerified"])
	}
	if _, present := ltc.Attributes["clockDomain"]; present {
		t.Error(`audio.output.ltc attributes carry "clockDomain", want absent: a node never claims its own clock domain`)
	}
}

// TestDetectAudioCapabilitiesLTCNotAdvertisedFromUnconstrainedChannelsAlone
// proves finding 3's core rule: a route that achieved 4 channels on its
// UNCONSTRAINED probe, but whose separate LTC-constrained probe never ran
// (LTCChannels left at zero), does not advertise audio.output.ltc — the
// unconstrained achieved count is never itself evidence of LTC capability.
func TestDetectAudioCapabilitiesLTCNotAdvertisedFromUnconstrainedChannelsAlone(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=X,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 4, Rate: 48000, Format: "S16LE"}},
		},
	})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.output.ltc"); ok {
		t.Error("audio.output.ltc advertised from an unconstrained 4-channel probe with no LTC-constrained probe, want absent")
	}
}

// TestDetectAudioCapabilitiesNeverAdvertisesUnprobedRoute proves a route
// that was enumerated but did not itself probe Available never
// contributes to either output capability.
func TestDetectAudioCapabilitiesNeverAdvertisesUnprobedRoute(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=GHOST,DEV=0", ProbeResult: audio.ProbeResult{Available: false, Reason: "could not open audio device"}},
		},
	})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.output.local"); ok {
		t.Error("audio.output.local advertised from an unavailable probe, want absent")
	}
}

// TestDetectAudioCapabilitiesEveryIDValidates is a sanity check that every
// ID this function can mint passes capability.ID.Validate.
func TestDetectAudioCapabilitiesEveryIDValidates(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{
				Device:      "hw:CARD=X,DEV=0",
				ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"},
				LTCChannels: 4,
			},
		},
	})
	set := detectAudioCapabilities(context.Background())
	if err := set.Validate(); err != nil {
		t.Errorf("capability.Set.Validate() = %v, want nil", err)
	}
	for _, c := range set {
		if err := capability.ID(c.ID).Validate(); err != nil {
			t.Errorf("capability.ID(%q).Validate() = %v, want nil", c.ID, err)
		}
	}
}

// TestFakeAudioEngineNeverAdvertisesPlaybackCapability guards against a
// node whose only backend is [audio.FakeEngine] ever advertising ANY
// capability implying it can actually play audio through a session, no
// matter how healthy its ALSA hardware detection reports. This is the
// real rule, not merely a naming convention: [audio.FakeEngine.Available]
// is always false, so wiring its Available method into
// [audioEngineAvailable] (exactly as agent.go wires the real engine's)
// must withhold "audio.engine" itself, in addition to the standing
// "audio.session"/"audio.playback" prefix check below.
func TestFakeAudioEngineNeverAdvertisesPlaybackCapability(t *testing.T) {
	fake := audio.NewFakeEngine(time.Now)
	fakeOK, fakeReason := fake.Available()
	if fakeOK {
		t.Fatal("audio.FakeEngine.Available() must be false")
	}
	withAudioEngineAvailable(t, fakeOK, fakeReason)

	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{
				Device:      "hw:CARD=X,DEV=0",
				ProbeResult: audio.ProbeResult{Available: true, Channels: 4, Rate: 48000, Format: "S16LE"},
				LTCChannels: 4,
			},
		},
	})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.engine"); ok {
		t.Error(`"audio.engine" advertised while the only Engine is FakeEngine (Available() == false); this must never happen`)
	}
	for _, c := range set {
		if strings.HasPrefix(string(c.ID), "audio.session") || strings.HasPrefix(string(c.ID), "audio.playback") ||
			strings.HasPrefix(string(c.ID), "audio.mix") || strings.HasPrefix(string(c.ID), "audio.transition") {
			t.Errorf("capability set advertises %q while the only Engine is FakeEngine; this must never happen", c.ID)
		}
	}
}

// TestDetectAudioCapabilitiesShipsOnlySequentialTransition is a
// packaging check, not a behavioral one: it proves detectAudioCapabilities
// ships exactly what [audioSessionCapabilityIDs] lists, no more, so a
// careless edit adding "audio.transition.gapless" or
// "audio.transition.crossfade" to that literal list is caught here. It
// canNOT, by itself, prove either ability is actually unimplemented -
// that is [audio.Session.advanceLocked]'s own behavior, which this
// package cannot observe. The real, behavioral proof of that claim -
// the one that would actually break if a future change made
// advanceLocked genuinely overlap two items - lives in
// internal/agent/audio's own
// TestAdvanceReleasesThePredecessorBeforeTheSuccessorEverLoadsRegardlessOfRequestedTransition,
// which drives a real Manager/Session/FakeEngine through a natural
// playlist advance and asserts the engine call order directly, the same
// way [TestFakeAudioEngineNeverAdvertisesPlaybackCapability] drives a
// real [audio.FakeEngine.Available] rather than a literal.
func TestDetectAudioCapabilitiesShipsOnlySequentialTransition(t *testing.T) {
	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, audio.Discovery{EngineUsable: true})

	set := detectAudioCapabilities(context.Background())
	if _, ok := set.Lookup("audio.transition.gapless"); ok {
		t.Error(`"audio.transition.gapless" advertised, want absent: no engine in this repository implements it`)
	}
	if _, ok := set.Lookup("audio.transition.crossfade"); ok {
		t.Error(`"audio.transition.crossfade" advertised, want absent: no engine in this repository implements it`)
	}
	if _, ok := set.Lookup("audio.transition.sequential"); !ok {
		t.Error(`"audio.transition.sequential" not advertised while the engine is available, want present`)
	}
}

// TestDetectAudioCapabilitiesEngineGatedOnAvailability proves the gate
// directly: identical probe evidence (usable GStreamer, a usable route)
// advertises "audio.engine" when [audioEngineAvailable] reports true and
// withholds it when false — never inferred from GStreamer/ALSA probe
// success alone.
func TestDetectAudioCapabilitiesEngineGatedOnAvailability(t *testing.T) {
	discovery := audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:CARD=PCH,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000, Format: "S16LE"}},
		},
	}

	withAudioEngineAvailable(t, false, "no audio.node binding delivered yet")
	withAudioDiscoverer(t, discovery)
	unavailable := detectAudioCapabilities(context.Background())
	if _, ok := unavailable.Lookup("audio.engine"); ok {
		t.Error(`"audio.engine" advertised while audioEngineAvailable() == false, want absent`)
	}
	for _, id := range audioSessionCapabilityIDs {
		if _, ok := unavailable.Lookup(id); ok {
			t.Errorf("%s advertised while audioEngineAvailable() == false, want absent", id)
		}
	}

	withAudioEngineAvailable(t, true, "")
	withAudioDiscoverer(t, discovery)
	available := detectAudioCapabilities(context.Background())
	if _, ok := available.Lookup("audio.engine"); !ok {
		t.Error(`"audio.engine" not advertised while audioEngineAvailable() == true, want present`)
	}
	for _, id := range audioSessionCapabilityIDs {
		if _, ok := available.Lookup(id); !ok {
			t.Errorf("%s not advertised while audioEngineAvailable() == true, want present", id)
		}
	}
}
