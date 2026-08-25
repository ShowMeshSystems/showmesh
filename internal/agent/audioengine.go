package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

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
	if cfg.SinkFactory != realAudioSinkFactory {
		if rate <= 0 {
			rate, rateSource = scaffoldSampleRate, "fallback: non-hardware sink, no advertised probe evidence for this route"
		}
		if channelCount <= 0 {
			channelCount, chCountSource = audioNodeChannelCount(node), "fallback: non-hardware sink, bindings' highest program or LTC channel index"
		}
	}
	cfg.SampleRate = rate
	cfg.ChannelCount = channelCount
	return cfg, rateSource, chCountSource
}

// scaffoldSampleRate is the rate a config built against a non-hardware
// sink ([envGstAudioSinkOverride]) uses when this node has no probe
// evidence for the bound route. Such a sink accepts whatever it is
// handed, so the value is scaffolding rather than a claim about a
// device. A route bound to the real [realAudioSinkFactory] gets no such
// substitution: see [audioEngineRebuilder.rebuild].
const scaffoldSampleRate = 48000

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
//
// [gstengine.New] returns a non-nil error ONLY when cfg fails
// cfg.Validate(); a genuinely bad or busy device never surfaces as an
// error here (that surfaces later, from the built engine's own
// Available). rebuild's own err != nil branch below is therefore
// unreachable in production, since it only ever calls newGstEngine with
// a cfg already proven to pass Validate() (see rebuild's earlier
// staticCfg.Validate() call, which differs only in SampleRate, and
// [resolveNodeSampleRate] never returns a value <= 0). It is kept as a
// defensive backstop, not as the meaningful failure path for a bad
// device: that path is [gstengine.Engine.Available] reporting false,
// which rebuild binds and logs like any other outcome.
var newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
	return gstengine.New(cfg)
}

// audioEngineRebuilder rebuilds this node's real playback engine every
// time a genuinely newer audio.node binding arrives ([audioBinding]'s
// onNode callback). onNode runs with its own lock released, so two
// deliveries can call [audioEngineRebuilder.rebuild] from different
// goroutines; mu serializes them so at most one rebuild runs at a time,
// and each fully releases what it displaces before the next one starts
// (see [audioEngineRebuilder.bind]). Serialization alone does not order
// revisions: an older delivery's rebuild can still acquire mu AFTER a
// newer delivery's rebuild already bound its engine. builtRevision and
// haveBuilt (also guarded by mu) are what make a slower rebuild unable to
// overwrite a faster, later one: rebuild drops any revision older than
// the one already bound instead of running.
//
// A gstengine holds its output device until it is closed (see
// [closeReplacedEngine]), so rebuild validates the new binding
// structurally, THEN closes the outgoing engine, THEN probes the route
// and builds the replacement, never the reverse, or the probe observes
// the device still busy and the replacement is opened against a device
// that is not free (the defect this ordering exists to avoid).
// [audio.Manager.RebindEngine] both invalidates every session with a
// live engine handle and detaches the outgoing engine from
// [audio.SwitchableEngine], in that order, so a session in flight is
// failed visibly before its handle can reach a closed engine, and no
// in-flight call can reach the outgoing engine after this method starts
// closing it.
//
// A route bound to the real [realAudioSinkFactory] is only ever built
// from this node's own probe evidence. When discovery recorded none for
// it, rebuild REFUSES to build rather than substituting a rate and
// channel count the device never advertised, and binds an engine that
// reports that refusal. Measured on a MOTU M4: the substituted pair
// (48000Hz, the bindings' own highest channel index) is a combination
// that device does not offer at all, while its advertised pair is
// 44100Hz across 4 channels, so the guess turned a binding redelivered
// at boot into an engine that could not reach PLAYING. Waiting for probe
// evidence instead was rejected: [buildGstEngineConfig] runs discovery
// inline on this very call, so "no evidence" means the probe just ran
// against this route and found nothing, and waiting would only hold
// rebuild's own lock, and every later revision behind it, retrying work
// that belongs to discovery. A later revision, or a redelivery of this
// one, rebuilds normally.
//
// While the outgoing engine is closed and the replacement is
// not yet bound, [audio.SwitchableEngine] reports
// [audio.SwitchableEngineRebindInProgressReason] rather than
// [audio.SwitchableEngineNoBindingReason]: a binding WAS delivered, so a
// caller must not read this window as "never configured", and a session
// command attempted in it classifies as [pkgaudio.FaultRouteChanged]
// rather than [pkgaudio.FaultOther].
type audioEngineRebuilder struct {
	// ctx bounds RebindEngine's own retry work (prepare/Load/Start for
	// every deferred session) — the agent's shutdown-signal context, so
	// a binding delivered while the agent is exiting does not run that
	// work uncancellably. Never context.Background(): RestoreAll gets
	// the same sigCtx at startup, and this is the same guarantee for
	// every rebind after it.
	ctx        context.Context
	assetDir   string
	switchable *audio.SwitchableEngine
	mgr        *audio.Manager
	logger     *slog.Logger

	mu            sync.Mutex
	haveBuilt     bool
	builtRevision int64
}

func newAudioEngineRebuilder(ctx context.Context, assetDir string, switchable *audio.SwitchableEngine, mgr *audio.Manager, logger *slog.Logger) *audioEngineRebuilder {
	return &audioEngineRebuilder{ctx: ctx, assetDir: assetDir, switchable: switchable, mgr: mgr, logger: logger}
}

// validationSampleRate is a placeholder used only to satisfy
// cfg.Validate()'s SampleRate>0 check before the real route probe runs.
// It never reaches a built engine: [buildGstEngineConfig] overwrites
// SampleRate with real probe evidence after the outgoing engine closes.
const validationSampleRate = 1

func (r *audioEngineRebuilder) rebuild(node audioNodeConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// applyNode orders acceptance, not execution: it records a newer
	// revision and releases its own lock before calling onNode, so an
	// older delivery's rebuild can still reach mu after a newer one has
	// already bound its engine. Running it would tear the newer binding
	// down and leave the node playing a superseded one.
	if r.haveBuilt && node.Revision < r.builtRevision {
		if r.logger != nil {
			r.logger.Warn("dropped an audio.node revision older than the one currently bound",
				"revision", node.Revision, "bound_revision", r.builtRevision)
		}
		return
	}

	staticCfg := staticGstEngineConfig(r.assetDir, node)
	staticCfg.SampleRate = validationSampleRate
	if err := staticCfg.Validate(); err != nil {
		if r.logger != nil {
			r.logger.Error("audio.node.configure delivered a binding this node cannot build an engine from", "revision", node.Revision, "error", err)
		}
		return
	}

	prev := r.mgr.RebindEngine(r.ctx, r.switchable, nil, audio.RebindReasonEngineRebind)
	closeReplacedEngine(prev, r.logger)

	cfg, rateSource, channelCountSource := buildGstEngineConfig(context.Background(), r.assetDir, node)
	if cfg.SampleRate <= 0 || cfg.ChannelCount <= 0 {
		reason := fmt.Sprintf("this node has no advertised probe evidence for %q, so no output pipeline was built (sample rate: %s; channel count: %s)",
			node.ProgramRoute, rateSource, channelCountSource)
		if r.logger != nil {
			r.logger.Error("refused to build the real audio engine against a route with no probe evidence", "revision", node.Revision, "program_route", node.ProgramRoute, "reason", reason)
		}
		r.bind(gstengine.NewUnavailable(reason))
		r.builtRevision = node.Revision
		r.haveBuilt = true
		return
	}
	engine, err := newGstEngine(cfg)
	if err != nil {
		// See newGstEngine's doc comment: production never reaches this
		// branch, since cfg already passed the identical structural
		// Validate() above. Kept as a defensive backstop only.
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
	r.bind(engine)
	// Advanced only on a revision whose engine is actually bound, so the
	// guard above never drops a revision on the strength of a failed build.
	r.builtRevision = node.Revision
	r.haveBuilt = true
}

// bind installs engine as this node's current engine through
// [audio.Manager.RebindEngine], so the swap and the retry of every
// restore deferred while no engine was bound run under the manager's
// rebindMu and cannot interleave with a concurrent binding. The engine
// RebindEngine hands back is closed as a defensive backstop: under
// [audioEngineRebuilder.rebuild]'s own mu it is always nil today, since
// rebuild's earlier detach-and-close step already released whatever
// this node held before probing and building the replacement.
func (r *audioEngineRebuilder) bind(engine audio.Engine) {
	closeReplacedEngine(r.mgr.RebindEngine(r.ctx, r.switchable, engine, audio.RebindReasonEngineRebind), r.logger)
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
// device. This assumes [audio.Engine.Close] accurately reports the
// device as released: the real gstengine's Close always returns nil even
// if its SetState(NULL) teardown exceeded its own timeout, so a stuck
// teardown is not detected here.
func closeReplacedEngine(prev audio.Engine, logger *slog.Logger) {
	closer, ok := prev.(interface{ Close() error })
	if !ok {
		return
	}
	if err := closer.Close(); err != nil && logger != nil {
		logger.Warn("failed to close the audio engine a rebind replaced", "error", err)
	}
}
