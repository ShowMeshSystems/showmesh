package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
	"github.com/showmeshsystems/showmesh/internal/agent/config"
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
	if sinkFactory == realAudioSinkFactory {
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
// rebuild refuses outright rather than calling this when the resolved
// rate or channel count is not positive). It is kept as a
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

	// onAvailabilityChange, when set, is called after every rebuildLocked
	// call that actually bound an engine (either branch that calls
	// [audioEngineRebuilder.bind]), so the reserved capability
	// advertisement re-detects and republishes on the same event that
	// changed what [audio.SwitchableEngine.Available] would now report,
	// never on a separate poll or timer. This is the only place
	// [audio.SwitchableEngine.Set] is ever called (see rebuildLocked), so
	// hooking it here catches every availability transition this node can
	// produce: a binding arriving, a rebind that fails to build (route
	// change, no probe evidence), and a later rebind that recovers.
	// Set via [audioEngineRebuilder.SetAvailabilityChangeCallback], never
	// passed to the constructor: it needs the MQTT connection built after
	// this rebuilder is (see agent.go).
	onAvailabilityChange func()

	mu            sync.Mutex
	haveBuilt     bool
	builtRevision int64
}

func newAudioEngineRebuilder(ctx context.Context, assetDir string, switchable *audio.SwitchableEngine, mgr *audio.Manager, logger *slog.Logger) *audioEngineRebuilder {
	return &audioEngineRebuilder{ctx: ctx, assetDir: assetDir, switchable: switchable, mgr: mgr, logger: logger}
}

// SetAvailabilityChangeCallback installs f as [audioEngineRebuilder]'s own
// onAvailabilityChange hook, matching [audioBinding.SetNodeBrokenCheck]'s
// own post-construction setter convention (both need a dependency built
// after their owning struct). Guarded by r.mu, the same lock bind reads
// it under (via rebuildLocked's callers), so this is safe to call
// concurrently with an in-flight rebuild racing in from an already-
// subscribed connection; agent.go still calls it exactly once.
//
// f is also called once, immediately, right after it is installed. This
// closes the residual race a plain assignment leaves open: newMQTTConn's
// subscription can already be live, and so able to deliver a real
// audio.node.configure and run rebuild, before this setter's own call
// site in agent.go executes (audioEngineRebuilder is necessarily
// constructed before the Publisher this callback needs exists, so the
// setter cannot run any earlier). A bind that fires while
// onAvailabilityChange is still nil publishes nothing on its own; this
// unconditional catch-up call runs a fresh detection immediately after
// wiring completes regardless, and since that detection reads
// audioEngineAvailable() fresh rather than a stale snapshot from
// whatever bind may have been missed, it reports the CURRENT state
// correctly whether or not anything was actually missed.
func (r *audioEngineRebuilder) SetAvailabilityChangeCallback(f func()) {
	r.mu.Lock()
	r.onAvailabilityChange = f
	r.mu.Unlock()
	f()
}

// installAudioCapabilityRepublish is agent.go's Run own single call site
// for wiring rebuilder's availability-change hook to
// scheduleCapabilityDetection: a binding arriving, failing to build, or
// later recovering all change what audioEngineAvailable() (and so the
// reserved audio playback/mix/transition capability set alongside
// "audio.engine") would now report; without this, the retained hello
// only reflects that evidence as of the last MQTT (re)connect, so a
// binding delivered after connect ships no capability signal at all
// until the next reconnect, and a binding lost after connect leaves a
// stale positive standing indefinitely.
//
// Pulled out of Run into its own named function specifically so it can
// be exercised directly against a real audioEngineRebuilder and a fake
// Publisher (TestInstallAudioCapabilityRepublishRepublishesOnRebuild),
// without dialing MQTT: a review round found this wiring completely
// unverified when it lived as bare statements inline in Run, since
// go test ./internal/agent/ still passed with the callback-install call
// deleted outright.
func installAudioCapabilityRepublish(rebuilder *audioEngineRebuilder, ctx context.Context, pub Publisher, cfg config.Config, bootID string, startedAt time.Time, logger *slog.Logger) {
	rebuilder.SetAvailabilityChangeCallback(func() {
		scheduleCapabilityDetection(ctx, pub, cfg, bootID, startedAt, logger)
	})
}

// validationSampleRate is a placeholder used only to satisfy
// cfg.Validate()'s SampleRate>0 check before the real route probe runs.
// It never reaches a built engine: [buildGstEngineConfig] overwrites
// SampleRate with real probe evidence after the outgoing engine closes.
const validationSampleRate = 1

// audioRebuildOutcome is what one [audioEngineRebuilder.rebuildResult]
// call actually did — reported back to internal/agent's own automatic
// restore-retry driver (audiorestoreretry.go) so it can tell an
// attempt that genuinely ran the probe-and-build sequence apart from a
// dropped, stale revision, and can report "why the last attempt did not
// build an engine" (docs/build/IDENTIFIER-REGISTER.md
// audio_session.restore.last_reason) without re-deriving it from a log
// line.
type audioRebuildOutcome struct {
	// Attempted is false only when node.Revision was dropped as older
	// than the one already bound, or when [rebuildIfUnavailable] declined
	// to act at all (see Skipped below). The retry driver replays the
	// same, already-accepted binding on every automatic attempt, so a
	// dropped revision can only happen there if a genuinely newer binding
	// raced in concurrently and won.
	Attempted bool
	// Skipped is true only when [rebuildIfUnavailable] found the engine
	// already available, checked atomically with that decision, and did
	// nothing at all — no invalidation, no close, no probe, no build.
	// Always false for [rebuild]/[rebuildResult], which never skip.
	Skipped bool
	// Available and Reason mirror the bound engine's own
	// [audio.Engine.Available] at the moment this call bound it — Reason
	// is populated whenever Available is false, and meaningless
	// (untouched) when Attempted is false.
	Available bool
	Reason    string
}

// rebuild is the onNode callback [audioBinding] invokes for every
// genuinely newer audio.node.configure delivery; it discards the
// [audioRebuildOutcome] [rebuildResult] returns, since a coordinator-
// pushed binding has nowhere to report it. See [rebuildResult]'s own doc
// comment for the actual rebuild logic.
func (r *audioEngineRebuilder) rebuild(node audioNodeConfig) {
	r.rebuildResult(node)
}

// rebuildResult is [rebuild]'s own body, returning what it actually did.
// A genuinely newer audio.node.configure delivery always rebuilds,
// regardless of the currently bound engine's own availability — an
// operator or the coordinator asked for this specific binding, and a
// stale-availability skip would silently ignore that request. See
// [rebuildIfUnavailable] for the automatic retry driver's own,
// availability-gated entry point.
func (r *audioEngineRebuilder) rebuildResult(node audioNodeConfig) audioRebuildOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuildLocked(node)
}

// rebuildIfUnavailable is the automatic retry driver's own entry point
// (audiorestoreretry.go), replacing a separate, unsynchronised
// engineAvailable() read the driver used to take before calling rebuild:
// that shape was a TOCTOU — a genuine audio.node.configure delivery
// (a real [rebuild] call, from [audioBinding]'s onNode callback on
// another goroutine) could finish in the gap between the driver's stale
// read and its own rebuild call, and the driver, still trusting that
// stale "unavailable" answer, would call rebuild anyway and tear the
// concurrent delivery's own working engine back down — failing every
// session that binding had just restored, for no benefit to the one
// session the retry was trying to help.
//
// This closes that gap by checking [audio.SwitchableEngine.Available]
// itself, under the SAME r.mu that guards every rebuild: no rebuild,
// concurrent or otherwise, can complete between this check and this
// call's own decision to act, because both run under one lock
// acquisition. If the engine already reports available, this returns
// immediately (Skipped: true, Attempted: false) without touching
// anything — no invalidation, no close, no probe, no build; whatever is
// keeping a session pending in that case is not a device problem, and
// rebuilding would only risk the sessions that ARE working.
func (r *audioEngineRebuilder) rebuildIfUnavailable(node audioNodeConfig) audioRebuildOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok, _ := r.switchable.Available(); ok {
		return audioRebuildOutcome{Skipped: true}
	}
	return r.rebuildLocked(node)
}

// rebuildLocked is [rebuildResult]'s and [rebuildIfUnavailable]'s shared
// body. Caller holds r.mu. Every ordering and behavior guarantee in this
// method's own package doc comment (validate, rebind, close, probe,
// build — every probe call site after the close) is unchanged from the
// single rebuildResult this was split out of; this only adds the return
// value alongside each existing return point.
func (r *audioEngineRebuilder) rebuildLocked(node audioNodeConfig) audioRebuildOutcome {
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
		return audioRebuildOutcome{}
	}

	staticCfg := staticGstEngineConfig(r.assetDir, node)
	staticCfg.SampleRate = validationSampleRate
	if err := staticCfg.Validate(); err != nil {
		if r.logger != nil {
			r.logger.Error("audio.node.configure delivered a binding this node cannot build an engine from", "revision", node.Revision, "error", err)
		}
		return audioRebuildOutcome{Attempted: true, Available: false, Reason: err.Error()}
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
		return audioRebuildOutcome{Attempted: true, Available: false, Reason: reason}
	}
	engine, err := newGstEngine(cfg)
	if err != nil {
		// See newGstEngine's doc comment: production never reaches this
		// branch, since cfg already passed the identical structural
		// Validate() above. Kept as a defensive backstop only. Still
		// binds an explicitly unavailable engine (matching the "no probe
		// evidence" branch above) rather than returning with the outgoing
		// engine merely detached: an available-to-unavailable transition
		// is a withdrawal like any other capability publish, not a
		// special case that gets to skip re-detection.
		if r.logger != nil {
			r.logger.Error("failed to build the real audio engine after releasing the outgoing one; this node has no audio engine until the next audio.node.configure", "revision", node.Revision, "error", err)
		}
		r.bind(gstengine.NewUnavailable(err.Error()))
		r.builtRevision = node.Revision
		r.haveBuilt = true
		return audioRebuildOutcome{Attempted: true, Available: false, Reason: err.Error()}
	}
	ok, reason := engine.Available()
	if r.logger != nil {
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
	return audioRebuildOutcome{Attempted: true, Available: ok, Reason: reason}
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
	if r.onAvailabilityChange != nil {
		r.onAvailabilityChange()
	}
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
		DuckTargetGain:           pkgaudio.Gain(p.DuckTargetGain),
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
