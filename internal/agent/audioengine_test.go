package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount proves a route
// discovery already blessed does not get asked for fewer channels than
// it actually negotiated: a four-output interface bound to only 3
// program/LTC channels must still build a Config carrying ChannelCount
// 4, or the engine's interleave stage requests only 3 sink pads against
// a device that refuses fewer than its own negotiated channel count.
//
// Channels is deliberately set LOWER than the binding needs (2, not the
// device's real 4) and LTCChannels is what actually carries the wider
// evidence (4) -- exactly the shape [audio.Discover] produces for a
// device offering a channel range, where the unconstrained probe
// fixates low. A resolver that read RouteEvidence.Channels instead of
// LTCChannels would see 2, never widen past the binding's own floor of
// 3, and this test would fail with ChannelCount = 3.
func TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount(t *testing.T) {
	withAudioDiscoverer(t, audio.Discovery{
		Routes: []audio.RouteEvidence{
			{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000}, LTCChannels: 4},
		},
	})
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		Revision:        1,
	}
	cfg, _, source := buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if cfg.ChannelCount != 4 {
		t.Errorf("ChannelCount = %d, want 4 (this route's own explicit channel-count probe evidence, not just the bindings' highest index, and not the unconstrained probe's under-reported 2)", cfg.ChannelCount)
	}
	if source == "" {
		t.Error("channelCountSource is empty, want a stated reason")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestBuildGstEngineConfigCallsDiscoveryOnlyOnce proves the sample-rate
// and channel-count resolvers share one discovery run: audioDiscoverer
// shells out to real device probes, so calling it twice per rebuild
// would double every rebuild's real probing cost and let the two
// resolvers read the device's state from two different points in time.
func TestBuildGstEngineConfigCallsDiscoveryOnlyOnce(t *testing.T) {
	orig := audioDiscoverer
	calls := 0
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		calls++
		return audio.Discovery{
			Routes: []audio.RouteEvidence{
				{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100}},
			},
		}
	}
	t.Cleanup(func() { audioDiscoverer = orig })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		Revision:        1,
	}
	buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if calls != 1 {
		t.Errorf("audioDiscoverer called %d times, want exactly 1", calls)
	}
}

// closeCountingEngine is an [audio.Engine] that records how often it was
// closed, so a rebind can be checked for releasing the engine it
// replaced rather than leaving it holding an output device.
type closeCountingEngine struct {
	audio.Engine
	closed int
}

func (e *closeCountingEngine) Close() error {
	e.closed++
	return nil
}

func TestRebindClosesTheEngineItReplaced(t *testing.T) {
	prev := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	closeReplacedEngine(prev, nil)
	if prev.closed != 1 {
		t.Fatalf("outgoing engine closed %d times, want 1: a gstengine holds its output device until it is closed", prev.closed)
	}
}

func TestRebindEngineHandsBackTheEngineItReplaced(t *testing.T) {
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(t.TempDir()), t.TempDir(), audio.RealDecoder{}, time.Now, nil)

	first := audio.NewFakeEngine(time.Now)
	if prev := mgr.RebindEngine(switchable, first, audio.RebindReasonEngineRebind); prev != nil {
		t.Fatalf("first rebind returned %v, want nil: nothing was bound before it", prev)
	}
	second := audio.NewFakeEngine(time.Now)
	prev := mgr.RebindEngine(switchable, second, audio.RebindReasonEngineRebind)
	if prev != audio.Engine(first) {
		t.Fatal("rebind did not hand back the engine it replaced, so nothing can release it")
	}
}

// TestBuildGstEngineConfigCarriesTheBindingsLTCChannel proves the LTC
// channel an operator declared actually reaches the engine that has to
// generate on it: a config built with LTCChannel zero can never run LTC,
// and nothing else in the agent would report that as a fault.
func TestBuildGstEngineConfigCarriesTheBindingsLTCChannel(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	t.Cleanup(func() { audioDiscoverer = orig })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		Revision:        4,
	}
	cfg, _, _ := buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if cfg.LTCChannel != 3 {
		t.Errorf("LTCChannel = %d, want 3", cfg.LTCChannel)
	}
	if cfg.ChannelCount != 3 {
		t.Errorf("ChannelCount = %d, want 3", cfg.ChannelCount)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}
