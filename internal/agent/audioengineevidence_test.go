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

// TestRebuildStillBuildsAPipewireRouteWithNoProbeEvidence pins the tonight
// defect directly: a pipewiresink-backed node has no ALSA probe evidence
// for its route (PipeWire holds the card, so nothing else can open it to
// probe), and must still build rather than being refused alongside a bare
// alsasink route. It must also report its fallback rate and channel count
// honestly, never reusing the ALSA probe's own vocabulary for a value that
// did not come from a probe.
func TestRebuildStillBuildsAPipewireRouteWithNoProbeEvidence(t *testing.T) {
	origNewEngine := newGstEngine
	origDiscoverer := audioDiscoverer
	t.Cleanup(func() {
		newGstEngine = origNewEngine
		audioDiscoverer = origDiscoverer
	})

	built := 0
	var built0 gstengine.Config
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		built++
		built0 = cfg
		return audio.NewFakeEngine(time.Now), nil
	}
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(context.Background(), dir, switchable, mgr, nil)

	r.rebuild(audioNodeConfig{
		ProgramRoute:       "hw:CARD=M4,DEV=0",
		SinkBackend:        pipewireAudioSinkFactory,
		PipewireTargetNode: "showmesh-program",
		ProgramChannels:    []int{1, 2},
		LTCChannel:         3,
		Revision:           1,
	})

	if built != 1 {
		t.Fatalf("newGstEngine called %d times, want 1: a pipewiresink route with no ALSA probe evidence must still build", built)
	}
	if built0.SampleRate <= 0 || built0.ChannelCount <= 0 {
		t.Errorf("built with SampleRate %d and ChannelCount %d, want both positive", built0.SampleRate, built0.ChannelCount)
	}
}

// TestBuildGstEngineConfigReportsPipewireFallbackHonestly pins the
// wording an operator reads: a pipewiresink route falling back to a
// non-probed rate or channel count must say plainly that no device probe
// was possible, never reuse [noProbeEvidenceSource]'s "no advertised
// probe evidence" wording, which reads as the ALSA refusal outcome.
func TestBuildGstEngineConfigReportsPipewireFallbackHonestly(t *testing.T) {
	origDiscoverer := audioDiscoverer
	t.Cleanup(func() { audioDiscoverer = origDiscoverer })
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}

	node := audioNodeConfig{
		ProgramRoute:    "hw:CARD=M4,DEV=0",
		SinkBackend:     pipewireAudioSinkFactory,
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
	}
	cfg, rateSource, channelCountSource := buildGstEngineConfig(context.Background(), t.TempDir(), node)

	if cfg.SampleRate <= 0 || cfg.ChannelCount <= 0 {
		t.Fatalf("SampleRate %d, ChannelCount %d, want both positive", cfg.SampleRate, cfg.ChannelCount)
	}
	if strings.Contains(rateSource, "advertised probe evidence") {
		t.Errorf("rate source = %q, want it to say no device probe was possible, not reuse the probe-evidence vocabulary", rateSource)
	}
	if strings.Contains(channelCountSource, "advertised probe evidence") {
		t.Errorf("channel count source = %q, want it to say no device probe was possible, not reuse the probe-evidence vocabulary", channelCountSource)
	}
	if !strings.Contains(rateSource, "pipewiresink") || !strings.Contains(channelCountSource, "pipewiresink") {
		t.Errorf("rate source %q / channel count source %q, want both to name the pipewiresink backend", rateSource, channelCountSource)
	}
}

// TestBuildGstEngineConfigUsesThePipeWireGraphsRealChannelCount pins the
// symptom-2 defect directly: a 4-channel PipeWire node must build a
// 4-channel pipeline from the graph's own evidence, never the bindings'
// own channel floor (program 1-2 plus LTC 3, floor 3) — the mismatch that
// made the real output pipeline refuse to reach PLAYING on node-01.
func TestBuildGstEngineConfigUsesThePipeWireGraphsRealChannelCount(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origPWDiscoverer := audioPipeWireDiscoverer
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		audioPipeWireDiscoverer = origPWDiscoverer
	})
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	audioPipeWireDiscoverer = func(context.Context, audio.PipeWireEnumerator) audio.PipeWireDiscovery {
		return audio.PipeWireDiscovery{Enumerated: true, Routes: []audio.RouteEvidence{
			{Device: "alsa_output.usb-MOTU_M4-00.analog-surround-4",
				ProbeResult: audio.ProbeResult{Available: true, Channels: 4, Rate: 48000},
				FromGraph:   true, LTCChannels: 4},
		}}
	}

	node := audioNodeConfig{
		ProgramRoute:       "alsa_output.usb-MOTU_M4-00.analog-surround-4",
		SinkBackend:        pipewireAudioSinkFactory,
		PipewireTargetNode: "alsa_output.usb-MOTU_M4-00.analog-surround-4",
		ProgramChannels:    []int{1, 2},
		LTCChannel:         3,
	}
	cfg, rateSource, channelCountSource := buildGstEngineConfig(context.Background(), t.TempDir(), node)

	if cfg.SampleRate != 48000 {
		t.Errorf("SampleRate = %d, want 48000 (the graph's own rate)", cfg.SampleRate)
	}
	if cfg.ChannelCount != 4 {
		t.Errorf("ChannelCount = %d, want 4 (the graph's own channel count, not the bindings' floor of 3)", cfg.ChannelCount)
	}
	if strings.Contains(rateSource, "probe") || strings.Contains(channelCountSource, "probe") {
		t.Errorf("rate source %q / channel count source %q, want PipeWire graph evidence worded distinctly from an ALSA probe", rateSource, channelCountSource)
	}
	if !strings.Contains(rateSource, "PipeWire") || !strings.Contains(channelCountSource, "PipeWire") {
		t.Errorf("rate source %q / channel count source %q, want both to name PipeWire as the evidence source", rateSource, channelCountSource)
	}
}

// TestBuildGstEngineConfigReportsAnUnreadablePipeWireGraphInTheFallback
// proves acceptance 5: a pipewiresink route whose PipeWire graph could
// not be read (pw-dump ran but returned garbage) still builds, but its
// fallback reason names the actual read failure rather than reusing the
// generic "no device probe is possible" wording that also covers a graph
// simply not (yet) naming this route.
func TestBuildGstEngineConfigReportsAnUnreadablePipeWireGraphInTheFallback(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origPWDiscoverer := audioPipeWireDiscoverer
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		audioPipeWireDiscoverer = origPWDiscoverer
	})
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	audioPipeWireDiscoverer = func(context.Context, audio.PipeWireEnumerator) audio.PipeWireDiscovery {
		return audio.PipeWireDiscovery{EnumeratedReason: "decoding pw-dump JSON: unexpected end of JSON input"}
	}

	node := audioNodeConfig{
		ProgramRoute:       "alsa_output.usb-MOTU_M4-00.analog-surround-4",
		SinkBackend:        pipewireAudioSinkFactory,
		PipewireTargetNode: "alsa_output.usb-MOTU_M4-00.analog-surround-4",
		ProgramChannels:    []int{1, 2},
		LTCChannel:         3,
	}
	_, rateSource, channelCountSource := buildGstEngineConfig(context.Background(), t.TempDir(), node)

	if !strings.Contains(rateSource, "could not be read") || !strings.Contains(channelCountSource, "could not be read") {
		t.Errorf("rate source %q / channel count source %q, want both to say the PipeWire graph could not be read", rateSource, channelCountSource)
	}
	if !strings.Contains(rateSource, "unexpected end of JSON input") {
		t.Errorf("rate source %q, want the underlying read failure text carried through", rateSource)
	}
}
