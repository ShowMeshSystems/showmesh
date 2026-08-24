package agent

import (
	"context"
	"log/slog"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// buildGstEngineConfig turns a delivered audio.node binding into a
// complete [gstengine.Config]: the probe-free part from
// [staticGstEngineConfig], then SampleRate from [resolveNodeSampleRate]
// and ChannelCount from [resolveNodeChannelCount] (the binding's own
// floor raised to match this route's probed channel count when that is
// wider). Both resolvers read the SAME fresh [audio.Discovery] run, taken
// exactly once here: audioDiscoverer shells out to real device probes
// (up to nine subprocess runs across the always-present device and
// every candidate route), so a caller that ran it once per resolver
// would double every rebuild's real device-probing cost and let the rate
// and channel count disagree if the device's state changed between two
// separate runs. Because it probes the device, it must run only AFTER
// the outgoing engine has released that device; see
// [audioEngineRebuilder.rebuild] for the ordering.
func buildGstEngineConfig(ctx context.Context, assetDir string, node audioNodeConfig) (cfg gstengine.Config, sampleRateSource, channelCountSource string) {
	d := audioDiscoverer(ctx, audioEnumerator)
	rate, rateSource := resolveNodeSampleRate(d, node.ProgramRoute)
	channelCount, chCountSource := resolveNodeChannelCount(d, node.ProgramRoute, audioNodeChannelCount(node))
	cfg = staticGstEngineConfig(assetDir, node)
	cfg.SampleRate = rate
	cfg.ChannelCount = channelCount
	return cfg, rateSource, chCountSource
}

// staticGstEngineConfig builds the part of a [gstengine.Config] that
// needs no route probe: SinkFactory from [audioEngineSinkFactory] (the
// production "alsasink", or the test-only [envGstAudioSinkOverride]),
// the "device" sink property set to node.ProgramRoute only when building
// against the real "alsasink" (a non-hardware test sink such as
// "fakesink" has no such property, and setting an unknown GObject
// property is itself something to avoid rather than rely on being
// harmless), ProgramChannels and LTCChannel from the binding, and
// ChannelCount at the binding's own floor. SampleRate is left at zero
// and ChannelCount may still be raised: the caller fills both in from a
// real probe once one is safe to run (see [audioEngineRebuilder.rebuild],
// which validates a config built from this BEFORE touching the outgoing
// engine, so a structurally invalid binding never costs a working engine
// its device).
func staticGstEngineConfig(assetDir string, node audioNodeConfig) gstengine.Config {
	sinkFactory := audioEngineSinkFactory()
	props := map[string]any{}
	if sinkFactory == "alsasink" {
		props["device"] = node.ProgramRoute
	}
	return gstengine.Config{
		SinkFactory:     sinkFactory,
		SinkProperties:  props,
		ProgramChannels: node.ProgramChannels,
		LTCChannel:      node.LTCChannel,
		ChannelCount:    audioNodeChannelCount(node),
		Resolve:         gstAssetResolver(assetDir),
	}
}

// newGstEngine constructs the real playback engine from cfg. A
// package-level var, matching audioDiscoverer's own injection
// convention, so audioengine_test.go can prove [audioEngineRebuilder.rebuild]'s
// ordering with an engine double instead of linking GStreamer.
var newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
	return gstengine.New(cfg)
}

// audioEngineRebuilder rebuilds this node's real playback engine every
// time a genuinely newer audio.node binding arrives ([audioBinding]'s
// onNode callback). A gstengine holds its output device until it is
// closed (see [closeReplacedEngine]), so [audioEngineRebuilder.rebuild]
// validates the new binding structurally, THEN closes the outgoing
// engine, THEN probes the route and builds the replacement — never the
// reverse, or the probe observes the device still busy and the
// replacement is opened against a device that is not free (the defect
// this ordering exists to avoid). [audio.Manager.RebindEngine] both
// invalidates every session with a live engine handle and detaches the
// outgoing engine from [audio.SwitchableEngine], in that order, so a
// session in flight is failed visibly before its handle can reach a
// closed engine, and no in-flight call can reach the outgoing engine
// after this method starts closing it.
//
// If building the replacement fails after the outgoing engine is
// already closed, this node is deliberately left with no engine bound
// (switchable reports [audio.SwitchableEngineNoBindingReason]) rather
// than a broken one: every call fails a clean, visible refusal instead
// of silently misbehaving against a handle that was never loaded, and
// the node recovers automatically the next time a genuinely newer
// audio.node binding arrives and this method runs again.
type audioEngineRebuilder struct {
	assetDir   string
	switchable *audio.SwitchableEngine
	mgr        *audio.Manager
	logger     *slog.Logger
}

func newAudioEngineRebuilder(assetDir string, switchable *audio.SwitchableEngine, mgr *audio.Manager, logger *slog.Logger) *audioEngineRebuilder {
	return &audioEngineRebuilder{assetDir: assetDir, switchable: switchable, mgr: mgr, logger: logger}
}

// validationSampleRate is a placeholder used only to satisfy
// cfg.Validate()'s SampleRate>0 check before the real route probe runs.
// It never reaches a built engine: [buildGstEngineConfig] overwrites
// SampleRate with real probe evidence after the outgoing engine closes.
const validationSampleRate = 1

func (r *audioEngineRebuilder) rebuild(node audioNodeConfig) {
	staticCfg := staticGstEngineConfig(r.assetDir, node)
	staticCfg.SampleRate = validationSampleRate
	if err := staticCfg.Validate(); err != nil {
		if r.logger != nil {
			r.logger.Error("audio.node.configure delivered a binding this node cannot build an engine from", "revision", node.Revision, "error", err)
		}
		return
	}

	prev := r.mgr.RebindEngine(r.switchable, nil, audio.RebindReasonEngineRebind)
	closeReplacedEngine(prev, r.logger)

	cfg, rateSource, channelCountSource := buildGstEngineConfig(context.Background(), r.assetDir, node)
	engine, err := newGstEngine(cfg)
	if err != nil {
		if r.logger != nil {
			r.logger.Error("failed to build the real audio engine after releasing the outgoing one; this node has no audio engine until the next audio.node.configure", "revision", node.Revision, "error", err)
		}
		return
	}
	if r.logger != nil {
		ok, reason := engine.Available()
		r.logger.Info("rebuilt the real audio engine from a delivered audio.node binding",
			"revision", node.Revision, "program_route", node.ProgramRoute,
			"sample_rate", cfg.SampleRate, "sample_rate_source", rateSource,
			"channel_count", cfg.ChannelCount, "channel_count_source", channelCountSource,
			"available", ok, "unavailable_reason", reason)
	}
	r.switchable.Set(engine)
}

// audioSettingsFromWire converts a decoded "audio.settings.configure"
// payload into [audio.Settings] — the same conversion boundary
// audioNodeConfig/gstengine.Config crosses, kept as its own small
// function so agent.go's wiring reads as one call rather than an inline
// struct literal.
func audioSettingsFromWire(p audioSettingsConfig) audio.Settings {
	return audio.Settings{
		DefaultFadeCurve:         pkgaudio.FadeCurve(p.DefaultFadeCurve),
		DefaultFadeDurationMs:    p.DefaultFadeDurationMs,
		DefaultMaxBackgroundGain: pkgaudio.Ceiling(p.DefaultMaxBackgroundGain),
		LTCFrameRate:             pkgaudio.LTCFrameRate(p.LTCFrameRate),
		LTCDefaultStartOffset:    pkgaudio.LTCTimecode(p.LTCDefaultStartOffset),
	}
}

// closeReplacedEngine releases an outgoing engine's own resources. A
// gstengine holds its output device until it is closed, so a rebind that
// skipped this would leave the replacement unable to open the same
// device.
func closeReplacedEngine(prev audio.Engine, logger *slog.Logger) {
	closer, ok := prev.(interface{ Close() error })
	if !ok {
		return
	}
	if err := closer.Close(); err != nil && logger != nil {
		logger.Warn("failed to close the audio engine a rebind replaced", "error", err)
	}
}
