package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
)

// TestRebuildRefusesAHardwareRouteWithNoProbeEvidence pins the cold-boot
// half of the MOTU M4 defect: a retained audio.node binding redelivered
// before this node has any probe evidence for its route must not be
// built from substituted values. Measured on that device, the
// substituted pair (48000Hz, the bindings' own highest channel index) is
// a combination it does not offer, and the engine could not reach
// PLAYING. The rebuild must refuse, bind something that states why, and
// never construct a real engine.
func TestRebuildRefusesAHardwareRouteWithNoProbeEvidence(t *testing.T) {
	origNewEngine := newGstEngine
	origDiscoverer := audioDiscoverer
	t.Cleanup(func() {
		newGstEngine = origNewEngine
		audioDiscoverer = origDiscoverer
	})

	built := 0
	newGstEngine = func(gstengine.Config) (audio.Engine, error) {
		built++
		return audio.NewFakeEngine(time.Now), nil
	}
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(context.Background(), dir, switchable, mgr, nil)

	r.rebuild(audioNodeConfig{ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}, LTCChannel: 3, Revision: 1})

	if built != 0 {
		t.Errorf("newGstEngine called %d times, want 0: a hardware route with no probe evidence must not be built from substituted values", built)
	}
	ok, reason := switchable.Available()
	if ok {
		t.Fatal("Available() = true, want false: nothing was built")
	}
	if !strings.Contains(reason, "no advertised probe evidence") {
		t.Errorf("reason = %q, want it to state that this route has no advertised probe evidence rather than a rebuild still in progress", reason)
	}
}

// TestRebuildStillBuildsANonHardwareSinkWithoutProbeEvidence is the
// refusal's own boundary: a non-hardware sink accepts whatever it is
// handed, so a dev or test stack with no route evidence must keep
// building rather than being refused alongside a real device.
func TestRebuildStillBuildsANonHardwareSinkWithoutProbeEvidence(t *testing.T) {
	origNewEngine := newGstEngine
	origDiscoverer := audioDiscoverer
	t.Cleanup(func() {
		newGstEngine = origNewEngine
		audioDiscoverer = origDiscoverer
	})
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	built := 0
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		built++
		if cfg.SampleRate <= 0 || cfg.ChannelCount <= 0 {
			t.Errorf("built with SampleRate %d and ChannelCount %d, want both positive", cfg.SampleRate, cfg.ChannelCount)
		}
		return audio.NewFakeEngine(time.Now), nil
	}
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(context.Background(), dir, switchable, mgr, nil)

	r.rebuild(audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, LTCChannel: 3, Revision: 1})

	if built != 1 {
		t.Errorf("newGstEngine called %d times, want 1", built)
	}
}
