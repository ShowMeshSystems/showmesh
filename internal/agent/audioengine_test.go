package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount proves a route
// discovery already blessed does not get asked for fewer channels than
// it actually negotiated: a four-output interface probed at 4 channels
// but bound to only 3 program/LTC channels must still build a Config
// carrying ChannelCount 4, or the engine's interleave stage requests
// only 3 sink pads against a device that refuses fewer than its own
// negotiated channel count.
func TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount(t *testing.T) {
	withAudioDiscoverer(t, audio.Discovery{
		Routes: []audio.RouteEvidence{
			{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 4, Rate: 48000}},
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
	cfg, _, _ := buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if cfg.ChannelCount != 4 {
		t.Errorf("ChannelCount = %d, want 4 (this route's own probed channel count, not just the bindings' highest index)", cfg.ChannelCount)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
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
