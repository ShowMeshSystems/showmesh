package agent

import (
	"context"
	"log/slog"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// buildGstEngineConfig turns a delivered audio.node binding into a
// [gstengine.Config]: SinkFactory from [audioEngineSinkFactory] (the
// production "alsasink", or the test-only [envGstAudioSinkOverride]),
// the "device" sink property set to node.ProgramRoute only when building
// against the real "alsasink" (a non-hardware test sink such as
// "fakesink" has no such property, and setting an unknown GObject
// property is itself something to avoid rather than rely on being
// harmless), ProgramChannels/ChannelCount from the binding, and
// SampleRate from this node's own fresh route probe evidence.
func buildGstEngineConfig(ctx context.Context, assetDir string, node audioNodeConfig) (cfg gstengine.Config, sampleRateSource string) {
	sinkFactory := audioEngineSinkFactory()
	props := map[string]any{}
	if sinkFactory == "alsasink" {
		props["device"] = node.ProgramRoute
	}
	rate, rateSource := resolveNodeSampleRate(ctx, node.ProgramRoute)
	return gstengine.Config{
		SinkFactory:     sinkFactory,
		SinkProperties:  props,
		ProgramChannels: node.ProgramChannels,
		LTCChannel:      node.LTCChannel,
		ChannelCount:    audioNodeChannelCount(node),
		SampleRate:      rate,
		Resolve:         gstAssetResolver(assetDir),
	}, rateSource
}

// audioEngineRebuilder rebuilds this node's real playback engine every
// time a genuinely newer audio.node binding arrives ([audioBinding]'s
// onNode callback) and rebinds [audio.Manager] onto it via
// [audio.Manager.RebindEngine] — the only path that ever calls
// [audio.SwitchableEngine.Set], so a session in flight is invalidated
// before any call can reach the new engine with a handle it never
// loaded. gstengine.New itself never fails on a runtime/device problem
// (that surfaces later, from the built engine's own Available) — only
// cfg.Validate() can, for a binding this node structurally cannot build
// an engine from, which is logged and left unrebound rather than handed
// a broken Engine.
type audioEngineRebuilder struct {
	assetDir   string
	switchable *audio.SwitchableEngine
	mgr        *audio.Manager
	logger     *slog.Logger
}

func newAudioEngineRebuilder(assetDir string, switchable *audio.SwitchableEngine, mgr *audio.Manager, logger *slog.Logger) *audioEngineRebuilder {
	return &audioEngineRebuilder{assetDir: assetDir, switchable: switchable, mgr: mgr, logger: logger}
}

func (r *audioEngineRebuilder) rebuild(node audioNodeConfig) {
	cfg, rateSource := buildGstEngineConfig(context.Background(), r.assetDir, node)
	if err := cfg.Validate(); err != nil {
		if r.logger != nil {
			r.logger.Error("audio.node.configure delivered a binding this node cannot build an engine from", "revision", node.Revision, "error", err)
		}
		return
	}
	engine, err := gstengine.New(cfg)
	if err != nil {
		if r.logger != nil {
			r.logger.Error("failed to build the real audio engine", "revision", node.Revision, "error", err)
		}
		return
	}
	if r.logger != nil {
		ok, reason := engine.Available()
		r.logger.Info("rebuilt the real audio engine from a delivered audio.node binding",
			"revision", node.Revision, "program_route", node.ProgramRoute,
			"sample_rate", cfg.SampleRate, "sample_rate_source", rateSource,
			"available", ok, "unavailable_reason", reason)
	}
	prev := r.mgr.RebindEngine(r.switchable, engine, audio.RebindReasonEngineRebind)
	closeReplacedEngine(prev, r.logger)
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
