package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file wires "audio.node.configure" and "audio.settings.configure" —
// the coordinator's ADR-039/ADR-036 configuration push
// (internal/coordinator/audioconfigpush is the coordinator half), the
// only way this agent ever learns its own output binding. Both carry a
// revision and are refused if it is older than the one already held, and
// are a no-op (never re-applied) on an exact replay — see
// [audioBinding.applyNode]/[audioBinding.applySettings].

// audioNodeConfig is "audio.node.configure"'s params shape, mirroring
// internal/coordinator/config.AudioNodePayload's JSON tags exactly —
// independently reproduced, not imported: this package has no
// coordinator dependency, matching every other wire boundary in this
// codebase.
type audioNodeConfig struct {
	ProgramRoute          string `json:"programRoute"`
	LTCRoute              string `json:"ltcRoute"`
	ProgramChannels       []int  `json:"programChannels"`
	LTCChannel            int    `json:"ltcChannel"`
	ClockDomain           string `json:"clockDomain"`
	ClockDomainProvenance string `json:"clockDomainProvenance"`
	Revision              int64  `json:"revision"`
}

// audioSettingsConfig is "audio.settings.configure"'s params shape,
// mirroring internal/coordinator/config.AudioSettingsPayload's JSON tags,
// independently reproduced for the identical reason audioNodeConfig is.
type audioSettingsConfig struct {
	DriftIgnoreThresholdMs   int     `json:"driftIgnoreThresholdMs"`
	DefaultFadeCurve         string  `json:"defaultFadeCurve"`
	DefaultFadeDurationMs    int     `json:"defaultFadeDurationMs"`
	DefaultMaxBackgroundGain float64 `json:"defaultMaxBackgroundGain"`
	DuckTargetGain           float64 `json:"duckTargetGain"`
	LTCFrameRate             string  `json:"ltcFrameRate"`
	LTCDefaultStartOffset    string  `json:"ltcDefaultStartOffset"`
	Revision                 int64   `json:"revision"`
}

// audioBinding holds this node's most recently accepted audio.node and
// audio.settings configuration and calls back once per genuinely newer
// revision — never for a refused or replayed one. agent.go wires onNode
// to rebuild the playback engine and onSettings to
// [audio.Manager.SetSettings].
type audioBinding struct {
	mu sync.Mutex

	haveNode     bool
	nodeRevision int64
	node         audioNodeConfig

	haveSettings     bool
	settingsRevision int64
	settings         audioSettingsConfig

	onNode     func(audioNodeConfig)
	onSettings func(audioSettingsConfig)

	// nodeBroken reports whether this node's current playback engine
	// cannot produce sound right now. nil means never broken (a node
	// with no audio manager wired, and every existing caller that never
	// sets it). Consulted only on an exact-revision replay of
	// audio.node.configure — see [audioBinding.applyNode].
	nodeBroken func() bool
}

func newAudioBinding(onNode func(audioNodeConfig), onSettings func(audioSettingsConfig)) *audioBinding {
	return &audioBinding{onNode: onNode, onSettings: onSettings}
}

// SetNodeBrokenCheck wires the query [audioBinding.applyNode] uses to
// decide whether an exact-revision replay of audio.node.configure should
// still be treated as a no-op. A coordinator's hello push resends the
// SAME revision this node already holds, so a broken output
// pipeline (device unplug, a fatal sink error, a negotiation failure)
// stayed broken until an agent restart or an artificial revision bump —
// nothing about the delivered configuration ever changed, so the old
// no-op rule never gave the node a reason to rebuild. Wiring this lets
// that same unchanged push double as a rebuild request whenever f
// reports true.
func (b *audioBinding) SetNodeBrokenCheck(f func() bool) {
	b.mu.Lock()
	b.nodeBroken = f
	b.mu.Unlock()
}

// applyNode refuses p.Revision older than the currently held one, is a
// no-op on an exact replay of the current revision, and otherwise stores
// p and calls onNode with the lock released (never while holding b.mu,
// so onNode is free to call back into b, e.g. for logging its own
// current revision).
func (b *audioBinding) applyNode(p audioNodeConfig) error {
	b.mu.Lock()
	if b.haveNode {
		if p.Revision < b.nodeRevision {
			b.mu.Unlock()
			return fmt.Errorf("audio.node.configure: revision %d is older than the currently held revision %d; refused", p.Revision, b.nodeRevision)
		}
		if p.Revision == b.nodeRevision {
			broken := b.nodeBroken != nil && b.nodeBroken()
			if !broken {
				b.mu.Unlock()
				return nil
			}
			// Fall through: an exact-revision replay against a broken
			// engine is treated as a rebuild request, not a no-op.
		}
	}
	b.node = p
	b.nodeRevision = p.Revision
	b.haveNode = true
	cb := b.onNode
	b.mu.Unlock()
	if cb != nil {
		cb(p)
	}
	return nil
}

func (b *audioBinding) currentNodeRevision() (revision int64, have bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nodeRevision, b.haveNode
}

// applySettings mirrors [audioBinding.applyNode] exactly, one
// configuration kind over.
func (b *audioBinding) applySettings(p audioSettingsConfig) error {
	b.mu.Lock()
	if b.haveSettings {
		if p.Revision < b.settingsRevision {
			b.mu.Unlock()
			return fmt.Errorf("audio.settings.configure: revision %d is older than the currently held revision %d; refused", p.Revision, b.settingsRevision)
		}
		if p.Revision == b.settingsRevision {
			b.mu.Unlock()
			return nil
		}
	}
	b.settings = p
	b.settingsRevision = p.Revision
	b.haveSettings = true
	cb := b.onSettings
	b.mu.Unlock()
	if cb != nil {
		cb(p)
	}
	return nil
}

func (b *audioBinding) currentSettingsRevision() (revision int64, have bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settingsRevision, b.haveSettings
}

var audioNodeConfigureKnownKeys = map[string]bool{
	"programRoute": true, "ltcRoute": true, "programChannels": true,
	"ltcChannel": true, "clockDomain": true, "clockDomainProvenance": true,
	"revision": true,
}

// decodeAudioNodeConfig validates params' shape against
// audioNodeConfigureKnownKeys and every field's presence, then decodes it
// via a JSON round trip (params arrives as map[string]any off the wire;
// re-marshaling and unmarshaling into audioNodeConfig is this codebase's
// established pattern — see internal/agent/renderops.go's applySurface).
func decodeAudioNodeConfig(params map[string]any) (audioNodeConfig, error) {
	const action = "audio.node.configure"
	if err := rejectUnknownKeys(action, params, audioNodeConfigureKnownKeys); err != nil {
		return audioNodeConfig{}, err
	}
	for _, field := range []string{"programRoute", "ltcRoute", "programChannels", "ltcChannel", "clockDomain", "clockDomainProvenance", "revision"} {
		if _, ok := params[field]; !ok {
			return audioNodeConfig{}, fmt.Errorf("%s: params.%s is required", action, field)
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return audioNodeConfig{}, fmt.Errorf("%s: encoding params: %w", action, err)
	}
	var p audioNodeConfig
	if err := json.Unmarshal(raw, &p); err != nil {
		return audioNodeConfig{}, fmt.Errorf("%s: params did not decode: %w", action, err)
	}
	if p.ProgramRoute == "" {
		return audioNodeConfig{}, fmt.Errorf("%s: params.programRoute must be a non-empty string", action)
	}
	if p.LTCRoute == "" {
		return audioNodeConfig{}, fmt.Errorf("%s: params.ltcRoute must be a non-empty string", action)
	}
	if len(p.ProgramChannels) == 0 {
		return audioNodeConfig{}, fmt.Errorf("%s: params.programChannels must be a non-empty array", action)
	}
	for _, ch := range p.ProgramChannels {
		if ch < 1 {
			return audioNodeConfig{}, fmt.Errorf("%s: every programChannels index must be positive, got %d", action, ch)
		}
	}
	if p.LTCChannel < 1 {
		return audioNodeConfig{}, fmt.Errorf("%s: params.ltcChannel must be a positive channel index", action)
	}
	if p.ClockDomain == "" {
		return audioNodeConfig{}, fmt.Errorf("%s: params.clockDomain must be a non-empty string", action)
	}
	if p.ClockDomainProvenance == "" {
		return audioNodeConfig{}, fmt.Errorf("%s: params.clockDomainProvenance must be a non-empty string", action)
	}
	if p.Revision < 0 {
		return audioNodeConfig{}, fmt.Errorf("%s: params.revision must not be negative", action)
	}
	return p, nil
}

var audioSettingsConfigureKnownKeys = map[string]bool{
	"driftIgnoreThresholdMs": true, "defaultFadeCurve": true, "defaultFadeDurationMs": true,
	"defaultMaxBackgroundGain": true, "duckTargetGain": true,
	"ltcFrameRate": true, "ltcDefaultStartOffset": true, "revision": true,
}

// decodeAudioSettingsConfig mirrors [decodeAudioNodeConfig], validating
// the curve/frame-rate/timecode fields against pkg/audio's own shared
// vocabulary (this package already imports it) rather than reproducing
// that validation a third time.
func decodeAudioSettingsConfig(params map[string]any) (audioSettingsConfig, error) {
	const action = "audio.settings.configure"
	if err := rejectUnknownKeys(action, params, audioSettingsConfigureKnownKeys); err != nil {
		return audioSettingsConfig{}, err
	}
	for _, field := range []string{
		"driftIgnoreThresholdMs", "defaultFadeCurve", "defaultFadeDurationMs",
		"defaultMaxBackgroundGain", "duckTargetGain", "ltcFrameRate", "ltcDefaultStartOffset", "revision",
	} {
		if _, ok := params[field]; !ok {
			return audioSettingsConfig{}, fmt.Errorf("%s: params.%s is required", action, field)
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: encoding params: %w", action, err)
	}
	var p audioSettingsConfig
	if err := json.Unmarshal(raw, &p); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params did not decode: %w", action, err)
	}
	if err := pkgaudio.FadeCurve(p.DefaultFadeCurve).Validate(); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.defaultFadeCurve: %w", action, err)
	}
	if p.DefaultFadeDurationMs <= 0 {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.defaultFadeDurationMs must be positive", action)
	}
	if err := pkgaudio.Ceiling(p.DefaultMaxBackgroundGain).Validate(); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.defaultMaxBackgroundGain: %w", action, err)
	}
	if err := pkgaudio.Gain(p.DuckTargetGain).Validate(); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.duckTargetGain: %w", action, err)
	}
	if p.DuckTargetGain >= 1 {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.duckTargetGain must be below 1: a gain of 1 or more does not duck anything", action)
	}
	if err := pkgaudio.LTCFrameRate(p.LTCFrameRate).Validate(); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.ltcFrameRate: %w", action, err)
	}
	if err := pkgaudio.LTCTimecode(p.LTCDefaultStartOffset).Validate(); err != nil {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.ltcDefaultStartOffset: %w", action, err)
	}
	if p.Revision < 0 {
		return audioSettingsConfig{}, fmt.Errorf("%s: params.revision must not be negative", action)
	}
	return p, nil
}

// configureNode is the OperationFunc for "audio.node.configure". Evidence
// is the binding's own read-back revision — a genuine re-read, matching
// this package's agent.echo confirmation pattern, not the write's own
// input echoed back.
func (b *audioBinding) configureNode(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	p, err := decodeAudioNodeConfig(params)
	if err != nil {
		return OperationResult{}, err
	}
	executedAt := now()
	if err := b.applyNode(p); err != nil {
		return OperationResult{}, err
	}
	observedAt := now()
	revision, _ := b.currentNodeRevision()
	return OperationResult{
		Confirmed:  revision == p.Revision,
		Signal:     "node.audio.node_config_revision",
		Value:      revision,
		ExecutedAt: executedAt, ObservedAt: observedAt,
	}, nil
}

// configureSettings is the OperationFunc for "audio.settings.configure",
// mirroring [audioBinding.configureNode] exactly.
func (b *audioBinding) configureSettings(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	p, err := decodeAudioSettingsConfig(params)
	if err != nil {
		return OperationResult{}, err
	}
	executedAt := now()
	if err := b.applySettings(p); err != nil {
		return OperationResult{}, err
	}
	observedAt := now()
	revision, _ := b.currentSettingsRevision()
	return OperationResult{
		Confirmed:  revision == p.Revision,
		Signal:     "node.audio.settings_config_revision",
		Value:      revision,
		ExecutedAt: executedAt, ObservedAt: observedAt,
	}, nil
}

// audioNodeConfigureOperations builds the two allowlist entries against
// b. b is nil-safe: a node with no audio manager wired never wires these
// either — see agent.go.
func audioNodeConfigureOperations(b *audioBinding) map[string]OperationFunc {
	if b == nil {
		return nil
	}
	return map[string]OperationFunc{
		"audio.node.configure":     b.configureNode,
		"audio.settings.configure": b.configureSettings,
	}
}

// envGstAudioSinkOverride names the test-only environment variable that
// substitutes a non-hardware GStreamer sink factory (e.g. "fakesink")
// for the production "alsasink" — matching
// internal/agent/audio/discoverer_resolve.go's SHOWMESH_GST_DISCOVERER
// convention. It exists so a test, a bench, or this development machine
// (macOS, no ALSA at all) can exercise the real gstengine backend
// without opening a real audio device. This is scaffolding, named as
// such, and is the only environment variable this seam adds.
const envGstAudioSinkOverride = "SHOWMESH_GST_AUDIO_SINK_FACTORY"

// gstAssetResolver maps a session's [pkgaudio.MediaRef] to its local
// path under assetDir, matching
// internal/agent/audio/mediaprobe.go's ProbeAsset — identity (content
// hash) is already verified there, before Engine.Load is ever reached,
// so this resolver does no verification of its own.
func gstAssetResolver(assetDir string) func(pkgaudio.MediaRef) (string, error) {
	return func(media pkgaudio.MediaRef) (string, error) {
		if media.RuntimeFilename == "" {
			return "", fmt.Errorf("gstengine asset resolver: media %s has no runtime filename", media.AssetID)
		}
		return filepath.Join(assetDir, media.RuntimeFilename), nil
	}
}

// resolveNodeSampleRate reports the sample rate this node's own probe
// evidence recorded for programRoute, out of d — one [audio.Discovery]
// run fresh by the caller (matching detectAudioCapabilities's own
// no-caching rule) and shared with [resolveNodeChannelCount] rather than
// each resolver re-running discovery's own real device probes. Falls
// back to 48000 (reported as the source, never silently) when no matching
// route evidence exists (a route not yet probed) or its rate is 0.
func resolveNodeSampleRate(d audio.Discovery, programRoute string) (rate int, source string) {
	for _, r := range d.Routes {
		if r.Device == programRoute && r.Available && r.Rate > 0 {
			return r.Rate, "advertised route probe evidence"
		}
	}
	return 48000, "fallback: no advertised probe evidence for this route"
}

// audioEngineSinkFactory reports the GStreamer sink factory this node
// builds against: [envGstAudioSinkOverride] when set, "alsasink"
// otherwise.
func audioEngineSinkFactory() string {
	if v := os.Getenv(envGstAudioSinkOverride); v != "" {
		return v
	}
	return "alsasink"
}

// audioNodeChannelCount is the highest channel index the binding uses,
// program or LTC — [gstengine.Config.ChannelCount]'s own required
// invariant. It is a floor, not the device's own channel count: see
// [resolveNodeChannelCount].
func audioNodeChannelCount(p audioNodeConfig) int {
	count := p.LTCChannel
	for _, ch := range p.ProgramChannels {
		if ch > count {
			count = ch
		}
	}
	return count
}

// resolveNodeChannelCount reports the channel count this node's output
// pipeline must actually build against, out of the same d
// [resolveNodeSampleRate] reads: the higher of bindingCount (the
// program/LTC bindings' own required floor, from
// [audioNodeChannelCount]) and this route's own probe evidence. A device
// that negotiated more channels than the bindings alone would ask for (a
// four-output interface bound to three channels) must still get an
// engine built for its own wider layout, or a raw hw: route discovery
// already proved usable refuses the engine's narrower request outright.
//
// [audio.RouteEvidence.Channels] is deliberately NOT read here on its
// own: it comes from an unconstrained probe (requestedChannels=0,
// [audio.ProbeOutput]'s own doc), so for a device offering a channel
// range it reports whatever the throwaway source's own default
// negotiates — often the low end of that range, not the device's real
// capability. [audio.RouteEvidence.LTCChannels] is the reliable evidence
// already sitting in d: [audio.Discover] runs it as an EXPLICIT probe
// requesting at least [audio.MinLTCChannels], and only ever records the
// count actually achieved against that request, matching a hw: route's
// own behavior of fixating to what it truly carries regardless of what
// was asked. It is read here purely as evidence of the device's real
// channel count, independent of whether this binding uses LTC at all.
func resolveNodeChannelCount(d audio.Discovery, programRoute string, bindingCount int) (count int, source string) {
	for _, r := range d.Routes {
		if r.Device != programRoute || !r.Available {
			continue
		}
		if r.LTCChannels > bindingCount {
			return r.LTCChannels, "advertised route probe evidence (explicit channel-count probe)"
		}
		if r.Channels > bindingCount {
			return r.Channels, "advertised route probe evidence (unconstrained probe)"
		}
	}
	return bindingCount, "bindings: highest program or LTC channel index"
}
