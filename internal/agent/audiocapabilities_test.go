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

// withAudioEngineHeldNode drives [audioEngineHeldNode] deterministically,
// matching withAudioEngineAvailable's own injection convention. Every
// test in this file that predates the busy-device fix calls this with
// ok=false (the zero value's own default), so it need not call this
// helper explicitly; TestWithHeldRouteTrusted and
// TestDetectAudioCapabilitiesTrustsAHeldRouteOverABusyProbe are what
// actually exercise it.
func withAudioEngineHeldNode(t *testing.T, node audioNodeConfig, ok bool) {
	t.Helper()
	orig := audioEngineHeldNode
	audioEngineHeldNode = func() (audioNodeConfig, bool) { return node, ok }
	t.Cleanup(func() { audioEngineHeldNode = orig })
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

// TestWithHeldRouteTrusted proves the busy-device fix directly: a route
// matching the held node's ProgramRoute is reported Available regardless
// of what this run's own probe found for it (including Busy, or absence
// entirely), using the held node's own declared channel count rather
// than a guess, while an UNRELATED route's genuine result passes through
// untouched. With no held node, every route passes through unchanged.
func TestWithHeldRouteTrusted(t *testing.T) {
	held := audioNodeConfig{ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0", ProgramChannels: []int{1, 2}, LTCChannel: 3}

	t.Run("busy held route overridden, unrelated route untouched", func(t *testing.T) {
		lastKnownGoodRoutes.reset()
		t.Cleanup(lastKnownGoodRoutes.reset)
		// The coordinator's own placement validation never accepts an
		// ltcRoute binding unless that device was already among this
		// node's advertised LTC-capable routes, so a genuine prior
		// successful probe is guaranteed to exist by the time an
		// LTC-configured binding can ever be delivered here, seeded
		// directly, matching that real precondition.
		lastKnownGoodRoutes.update([]audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 4}, LTCChannels: 3},
		}, true)

		routes := []audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: false, Busy: true, Reason: "device or resource busy"}},
			{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: false, Reason: "could not open audio device"}},
		}
		out := withHeldRouteTrusted(routes, held, true)
		var got0, got1 *audio.RouteEvidence
		for i := range out {
			switch out[i].Device {
			case "hw:0,0":
				got0 = &out[i]
			case "hw:1,0":
				got1 = &out[i]
			}
		}
		if got0 == nil || !got0.Available || got0.Channels != 3 || got0.LTCChannels != 3 {
			t.Fatalf("held route hw:0,0 = %+v, want Available=true Channels=3 (the held node's own declared floor) LTCChannels=3 (the last known-good probe)", got0)
		}
		if got1 == nil || got1.Available {
			t.Fatalf("unrelated route hw:1,0 = %+v, want its own genuine (unavailable) result untouched", got1)
		}
	})

	t.Run("held route absent from this pass is still added", func(t *testing.T) {
		lastKnownGoodRoutes.reset()
		t.Cleanup(lastKnownGoodRoutes.reset)

		out := withHeldRouteTrusted(nil, held, true)
		if len(out) != 1 || out[0].Device != "hw:0,0" || !out[0].Available {
			t.Fatalf("out = %+v, want one trusted entry for the held route even though this pass enumerated nothing", out)
		}
	})

	t.Run("no held node: routes pass through unchanged", func(t *testing.T) {
		routes := []audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: false, Busy: true}},
		}
		out := withHeldRouteTrusted(routes, audioNodeConfig{}, false)
		if len(out) != 1 || out[0].Available {
			t.Fatalf("out = %+v, want the original busy result untouched when heldOK is false", out)
		}
	})

	// Round 7 finding 1: a PROGRAM-ONLY held fixture (LTCRoute/LTCChannel
	// both unset, the shape every earlier case here omitted) used to make
	// trusted() special-case LTCChannels to 0 unconditionally, silently
	// stripping audio.output.ltc from a device this node already proved
	// LTC-capable the moment an operator bound it program-only, with no
	// way back short of deleting and recreating the audio.node object.
	t.Run("held program-only route still reports LTC from a prior good probe", func(t *testing.T) {
		lastKnownGoodRoutes.reset()
		t.Cleanup(lastKnownGoodRoutes.reset)
		lastKnownGoodRoutes.update([]audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 4}, LTCChannels: 3},
		}, true)

		programOnly := audioNodeConfig{ProgramRoute: "hw:0,0", ProgramChannels: []int{1, 2}}
		routes := []audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: false, Busy: true, Reason: "device or resource busy"}},
		}
		out := withHeldRouteTrusted(routes, programOnly, true)
		if len(out) != 1 || !out[0].Available || out[0].LTCChannels != 3 {
			t.Fatalf("held program-only route hw:0,0 = %+v, want Available=true LTCChannels=3 preserved from the last known-good probe even though this binding declares no LTC role", out[0])
		}
	})
}

// TestRouteEvidenceCacheEvictsOnlyOnACompleteEnumeration proves round 7's
// busy-vs-absent distinction: a device missing from a complete pass
// (successful, untruncated enumeration) is evicted, since gone hardware
// must not keep asserting a capability, but a device merely missing from
// an incomplete (truncated or failed) pass is left alone, since that
// pass never had a chance to see it either way.
func TestRouteEvidenceCacheEvictsOnlyOnACompleteEnumeration(t *testing.T) {
	lastKnownGoodRoutes.reset()
	t.Cleanup(lastKnownGoodRoutes.reset)
	lastKnownGoodRoutes.update([]audio.RouteEvidence{
		{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 4}, LTCChannels: 3},
	}, true)
	if _, ok := lastKnownGoodRoutes.get("hw:0,0"); !ok {
		t.Fatal("hw:0,0 not cached after seeding, want present")
	}

	lastKnownGoodRoutes.update([]audio.RouteEvidence{
		{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2}},
	}, false)
	if _, ok := lastKnownGoodRoutes.get("hw:0,0"); !ok {
		t.Fatal("hw:0,0 evicted by an incomplete (truncated/failed) enumeration pass that omitted it, want still cached")
	}

	lastKnownGoodRoutes.update([]audio.RouteEvidence{
		{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2}},
	}, true)
	if _, ok := lastKnownGoodRoutes.get("hw:0,0"); ok {
		t.Fatal("hw:0,0 still cached after a complete enumeration pass that no longer found it, want evicted")
	}

	// End to end: once evicted, a held program-only route with nothing
	// left to trust reports LTCChannels honestly as 0, not fabricated.
	programOnly := audioNodeConfig{ProgramRoute: "hw:0,0", ProgramChannels: []int{1, 2}}
	routes := []audio.RouteEvidence{
		{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: false, Busy: true, Reason: "device or resource busy"}},
	}
	out := withHeldRouteTrusted(routes, programOnly, true)
	if len(out) != 1 || out[0].LTCChannels != 0 {
		t.Fatalf("held route hw:0,0 after eviction = %+v, want LTCChannels=0 (no real evidence left to substitute)", out[0])
	}
}

// TestDetectAudioCapabilitiesTrustsAHeldRouteOverABusyProbe proves the
// fix end to end through detectAudioCapabilities itself: a route this
// run's own probe reports busy still ships as audio.output.local when it
// matches the node an actively-available engine was built from.
func TestDetectAudioCapabilitiesTrustsAHeldRouteOverABusyProbe(t *testing.T) {
	held := audioNodeConfig{ProgramRoute: "hw:0,0", ProgramChannels: []int{1, 2}}
	withAudioEngineAvailable(t, true, "")
	withAudioEngineHeldNode(t, held, true)
	withAudioDiscoverer(t, audio.Discovery{
		EngineUsable: true, HasHardwareCards: true,
		Routes: []audio.RouteEvidence{
			{Device: "hw:0,0", ProbeResult: audio.ProbeResult{Available: false, Busy: true, Reason: "device or resource busy"}},
		},
	})

	set := detectAudioCapabilities(context.Background())
	local, ok := set.Lookup("audio.output.local")
	if !ok {
		t.Fatal("audio.output.local not advertised for a busy route the engine actively holds, want present")
	}
	if local.Attributes["outputCount"] != 1 {
		t.Errorf("outputCount = %v, want 1", local.Attributes["outputCount"])
	}
}
