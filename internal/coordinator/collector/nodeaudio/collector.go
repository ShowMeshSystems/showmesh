package nodeaudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// alignmentStateWithinThreshold and alignmentStateBeyondThreshold are
// node.audio.clock.alignment.state's two collected values.
const (
	alignmentStateWithinThreshold = "within_threshold"
	alignmentStateBeyondThreshold = "beyond_threshold"
)

// alignmentAbsentFallbackReason is [alignmentObservation]'s reason for a
// node whose payload carries [mqttproto.AudioPayload.AlignmentMeasured]
// false with no reason of its own: an agent built before the alignment*
// fields existed, which omits the whole block rather than explaining its
// absence.
const alignmentAbsentFallbackReason = "this node's agent build does not report program-to-LTC alignment"

// timelineAbsentReason states why a timeline signal carries no value.
// The node's own [mqttproto.AudioPayload.TimelineReason] is preferred
// whenever it supplied one; the fallbacks cover an agent built before
// these fields existed, which omits the whole block rather than
// explaining its absence.
func timelineAbsentReason(p mqttproto.AudioPayload, scheduled bool) string {
	if p.TimelineReason != "" {
		return p.TimelineReason
	}
	if !scheduled {
		return "this node is running no session against a scheduled start instant"
	}
	return "this node could not read both its media clock and its sink clock on this report tick"
}

// timelineObservations renders the six node.audio.timeline.* signals
// (signals.go) from the node's own report.
//
// The payload carries TWO flags and they are not interchangeable.
// Scheduled makes ScheduledAt, Resyncs and LastResyncReason real;
// Measured makes ExpectedMs, ActualMs and ErrorMs real. A scheduled
// session whose media clock could not be read this tick therefore
// reports the first three and leaves the last three
// [observation.StateNotCollected] with the node's stated reason, rather
// than a zero error that would read as perfectly on time.
//
// LastResyncReason is only real once a resync has actually happened: on a
// scheduled session that has never resynced there is no reason to report,
// and an empty string would read as one.
func timelineObservations(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	reason := timelineAbsentReason(p, p.TimelineScheduled)

	var obs []observation.Observation
	if p.TimelineScheduled {
		obs = append(obs,
			buildValue(nodeID, SignalTimelineScheduledAt, p.TimelineScheduledAtNs, observedAt, rep),
			buildValue(nodeID, SignalTimelineResyncs, p.TimelineResyncs, observedAt, rep),
		)
		if p.TimelineLastResyncReason != "" {
			obs = append(obs, buildValue(nodeID, SignalTimelineLastResyncReason, p.TimelineLastResyncReason, observedAt, rep))
		} else {
			obs = append(obs, notCollected(res, SignalTimelineLastResyncReason, source,
				"this scheduled session has not resynced, so no resync reason is in effect", rep.receivedAt))
		}
	} else {
		obs = append(obs,
			notCollected(res, SignalTimelineScheduledAt, source, reason, rep.receivedAt),
			notCollected(res, SignalTimelineResyncs, source, reason, rep.receivedAt),
			notCollected(res, SignalTimelineLastResyncReason, source, reason, rep.receivedAt),
		)
	}

	if p.TimelineScheduled && p.TimelineMeasured {
		obs = append(obs,
			buildValue(nodeID, SignalTimelineExpectedMs, p.TimelineExpectedMs, observedAt, rep),
			buildValue(nodeID, SignalTimelineActualMs, p.TimelineActualMs, observedAt, rep),
			buildValue(nodeID, SignalTimelineErrorMs, p.TimelineErrorMs, observedAt, rep),
		)
	} else {
		obs = append(obs,
			notCollected(res, SignalTimelineExpectedMs, source, reason, rep.receivedAt),
			notCollected(res, SignalTimelineActualMs, source, reason, rep.receivedAt),
			notCollected(res, SignalTimelineErrorMs, source, reason, rep.receivedAt),
		)
	}
	return obs
}

// alignmentObservation renders node.audio.clock.alignment from the node's
// own report. observedAt is the node's own [mqttproto.AudioPayload.
// AlignmentSampledAt], never rep.receivedAt: a sample taken on an earlier
// tick and republished unchanged must age exactly like any other stale
// evidence, not read as current because the report that carried it just
// arrived.
func alignmentObservation(nodeID string, p mqttproto.AudioPayload, rep report) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	if p.AlignmentMeasured {
		return buildValue(nodeID, SignalClockAlignment, p.AlignmentOffsetMs, p.AlignmentSampledAt, rep)
	}
	reason := p.AlignmentReason
	if reason == "" {
		reason = alignmentAbsentFallbackReason
	}
	return notCollected(res, SignalClockAlignment, source, reason, rep.receivedAt)
}

// alignmentStateObservation renders node.audio.clock.alignment.state, the
// threshold verdict on the same sample alignmentObservation reported.
// An unmeasured sample carries its own not_collected reason forward.
func alignmentStateObservation(ctx context.Context, nodeID string, p mqttproto.AudioPayload, rep report, clockSrc ClockDomainSource) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)

	if !p.AlignmentMeasured {
		reason := p.AlignmentReason
		if reason == "" {
			reason = alignmentAbsentFallbackReason
		}
		return notCollected(res, SignalClockAlignmentState, source, reason, rep.receivedAt)
	}

	thresholdMs, reason := lookupDriftIgnoreThresholdMs(ctx, clockSrc)
	if reason != "" {
		return failed(res, SignalClockAlignmentState, source, reason, rep.receivedAt)
	}
	if thresholdMs <= 0 {
		return notCollected(res, SignalClockAlignmentState, source, "no drift threshold is configured; audio.settings driftIgnoreThresholdMs is 0", rep.receivedAt)
	}

	offsetMs := p.AlignmentOffsetMs
	if offsetMs < 0 {
		offsetMs = -offsetMs
	}
	state := alignmentStateWithinThreshold
	if offsetMs > int64(thresholdMs) {
		state = alignmentStateBeyondThreshold
	}
	return buildValue(nodeID, SignalClockAlignmentState, state, p.AlignmentSampledAt, rep)
}

// lookupDriftIgnoreThresholdMs reads audio.settings' driftIgnoreThresholdMs
// live through clockSrc. An object nothing has configured reports the
// shipped default (config.AudioSettingsDefaultPayload).
func lookupDriftIgnoreThresholdMs(ctx context.Context, clockSrc ClockDomainSource) (thresholdMs int, reason string) {
	if clockSrc == nil {
		return 0, "no configuration source wired into this coordinator"
	}
	obj, err := clockSrc.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.AudioSettingsDefaultPayload.DriftIgnoreThresholdMs, ""
	case err != nil:
		return 0, fmt.Sprintf("failed to read audio.settings configuration: %v", err)
	case obj.CurrentRevision == 0:
		return config.AudioSettingsDefaultPayload.DriftIgnoreThresholdMs, ""
	}

	rev, err := clockSrc.GetConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return 0, fmt.Sprintf("failed to read active audio.settings configuration: %v", err)
	}
	payload, verr := config.DecodeAudioSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return 0, fmt.Sprintf("stored audio.settings configuration payload is malformed: %v", verr)
	}
	return payload.DriftIgnoreThresholdMs, ""
}

// ltcFrameRateAbsentReason states why a node reports no frame rate, which
// differs between a node that cannot generate LTC at all and one that
// simply has not run it.
func ltcFrameRateAbsentReason(generatorState string) string {
	if generatorState == "unsupported" {
		return "this node cannot generate LTC, so no frame rate is in effect"
	}
	return "no LTC run has reported a frame rate on this node"
}

// Collector implements collector.Collector; enforced at compile time so a
// signature drift is caught here, matching noderender.Collector's identical
// assertion.
var _ collector.Collector = (*Collector)(nil)

// SessionObservationDeleter is nodeaudio's own view onto *store.Store's
// deletion surface for audio_session rows Poll already knows are gone.
// *store.Store satisfies this directly, the same live-wiring precedent
// [ClockDomainSource] already uses.
type SessionObservationDeleter interface {
	DeleteObservationsForResource(ctx context.Context, kind observation.ResourceKind, id string) error
	DeleteOrphanedObservations(ctx context.Context, kind observation.ResourceKind, liveIDs map[string]struct{}) (int64, error)
}

// Collector renders [Store]'s current push cache into observations on a
// collector.Runner's own cadence. The zero value is not usable; construct
// with [New].
type Collector struct {
	store   *Store
	deleter SessionObservationDeleter

	mu    sync.Mutex
	known map[string]map[string]struct{} // nodeID -> session ids named by its last delivery
}

// Option configures a [Collector] at construction. See [WithSessionDeleter].
type Option func(*Collector)

// WithSessionDeleter wires d as where Poll retires a session's observations
// once it drops out of its node's report, and sweeps any audio_session row
// no node currently reports. Omitting this leaves dead sessions
// accumulating exactly as before this option existed.
func WithSessionDeleter(d SessionObservationDeleter) Option {
	return func(c *Collector) { c.deleter = d }
}

// New builds a Collector reading from store, applying opts in order.
func New(store *Store, opts ...Option) *Collector {
	c := &Collector{store: store, known: make(map[string]map[string]struct{})}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// ID returns [SourceName].
func (c *Collector) ID() string { return SourceName }

// Poll renders every node's currently stored report into observations, then
// retires audio_session rows this poll knows are gone. A session present in
// a node's PREVIOUS report but absent from its current one is deleted
// outright, never restated as a single absence row the way noderender
// retires a dropped surface, because a session ends and a new one starts
// far more often than any resource that pattern was built for. A sweep
// additionally removes any audio_session row no node currently reports at
// all, catching a poll it was not running to see the drop of, or a row
// stranded before this deletion existed. It never touches the network, so
// it always returns complete=true, matching noderender.Collector.Poll and
// fppmqtt.Collector.Poll.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	snap := c.store.snapshot()
	var obs []observation.Observation

	c.mu.Lock()
	defer c.mu.Unlock()

	liveIDs := make(map[string]struct{})
	for nodeID, rep := range snap {
		obs = append(obs, nodeObservations(ctx, nodeID, rep, c.store.clockSrc)...)
		obs = append(obs, sessionObservations(nodeID, rep)...)

		cur := make(map[string]struct{}, len(rep.payload.Sessions))
		for _, sess := range rep.payload.Sessions {
			cur[sess.SessionID] = struct{}{}
			liveIDs[sess.SessionID] = struct{}{}
		}
		c.retireDroppedSessions(ctx, nodeID, cur)
		c.known[nodeID] = cur
	}

	c.sweepOrphanedSessions(ctx, liveIDs)

	return obs, true
}

// retireDroppedSessions deletes every audio_session row nodeID reported on
// its LAST poll but not this one (cur). A no-op if no
// [SessionObservationDeleter] is wired in.
func (c *Collector) retireDroppedSessions(ctx context.Context, nodeID string, cur map[string]struct{}) {
	if c.deleter == nil {
		return
	}
	for id := range c.known[nodeID] {
		if _, stillPresent := cur[id]; stillPresent {
			continue
		}
		if err := c.deleter.DeleteObservationsForResource(ctx, observation.ResourceAudioSession, id); err != nil {
			slog.Default().Error("nodeaudio: failed to retire a dropped session's observations",
				"node_id", nodeID, "session_id", id, "error", err)
		}
	}
}

// sweepOrphanedSessions removes every stored audio_session row whose id is
// not in liveIDs, the union of every session every node currently reports.
// A no-op if no [SessionObservationDeleter] is wired in.
func (c *Collector) sweepOrphanedSessions(ctx context.Context, liveIDs map[string]struct{}) {
	if c.deleter == nil {
		return
	}
	if _, err := c.deleter.DeleteOrphanedObservations(ctx, observation.ResourceAudioSession, liveIDs); err != nil {
		slog.Default().Error("nodeaudio: failed to sweep orphaned audio_session observations", "error", err)
	}
}

// NodeAudioObservations returns every node.audio.* observation this
// coordinator currently holds for nodeID's most recently reported audio
// discovery, or nil if nodeID has never published one. The node read
// path's synthesize-at-read-time counterpart to [Collector.Poll], matching
// noderender.Store.NodeRenderObservations exactly. Uses context.Background()
// for its own live clock-domain config read: a local SQLite lookup, not a
// network call, so this stays a synchronous method with no caller-supplied
// context to thread through, matching NodeRenderObservations' own signature.
func (s *Store) NodeAudioObservations(nodeID string) []observation.Observation {
	rep, ok := s.get(nodeID)
	if !ok {
		return nil
	}
	obs := nodeObservations(context.Background(), nodeID, rep, s.clockSrc)
	return append(obs, sessionObservations(nodeID, rep)...)
}

func nodeObservations(ctx context.Context, nodeID string, rep report, clockSrc ClockDomainSource) []observation.Observation {
	p := rep.payload
	// observedAt backs every signal this function builds. [Store] keeps
	// only a node's most recent report and nothing evicts it, so ValidFor
	// is the only thing that ever ages a dark node's signals: stamping a
	// signal with anything but this tick's own evidence time leaves it
	// reading current forever, even off a node that stopped reporting.
	observedAt := p.ObservedAt

	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)

	engineState, engineReason := StateUnavailable, p.EngineReason
	if p.EngineAvailable {
		engineState, engineReason = StateUsable, ""
	}

	obs := []observation.Observation{
		buildValue(nodeID, SignalEngineState, engineState, observedAt, rep),
		buildValue(nodeID, SignalEngineReason, engineReason, observedAt, rep),
	}

	// "We could not enumerate" (HardwareEnumerated false) and "we
	// enumerated and there is no card" must be distinguishable at this
	// surface too: the former reports not_collected for every
	// enumeration-dependent signal, carrying the node's own reason, rather
	// than a confirmed-absent "unavailable" it never earned.
	if !p.HardwareEnumerated {
		reason := p.HardwareEnumeratedReason
		obs = append(obs,
			failed(res, SignalDeviceState, source, reason, rep.receivedAt),
			failed(res, SignalDeviceReason, source, reason, rep.receivedAt),
			failed(res, SignalOutputsCount, source, reason, rep.receivedAt),
			failed(res, SignalProgramState, source, reason, rep.receivedAt),
			failed(res, SignalLTCState, source, reason, rep.receivedAt),
		)
	} else {
		deviceState, deviceReason := StateUnavailable, p.DeviceReason
		if p.DeviceAvailable {
			deviceState, deviceReason = StateUsable, ""
		}
		programState := StateUnavailable
		if p.ProgramAvailable {
			programState = StateUsable
		}
		obs = append(obs,
			buildValue(nodeID, SignalDeviceState, deviceState, observedAt, rep),
			buildValue(nodeID, SignalDeviceReason, deviceReason, observedAt, rep),
			buildValue(nodeID, SignalOutputsCount, p.OutputsCount, observedAt, rep),
			buildValue(nodeID, SignalProgramState, programState, observedAt, rep),
			ltcStateObservation(nodeID, p, observedAt, rep),
		)
	}

	obs = append(obs,
		buildValue(nodeID, SignalOutputsEnumerated, int64(p.EnumeratedCount), observedAt, rep),
		buildValue(nodeID, SignalOutputsTruncated, p.Truncated, observedAt, rep),
		buildValue(nodeID, SignalOutputsPipeWireEnumerated, p.PipeWireEnumerated, observedAt, rep),
		buildValue(nodeID, SignalOutputsPipeWireEnumeratedReason, p.PipeWireEnumeratedReason, observedAt, rep),
	)

	domain, provenance, declaredAt, reason := lookupClockDomain(ctx, clockSrc, nodeID)
	if reason != "" {
		obs = append(obs,
			failed(res, SignalClockDomain, source, reason, rep.receivedAt),
			failed(res, SignalClockProvenance, source, reason, rep.receivedAt),
		)
	} else {
		obs = append(obs,
			buildConfiguredValue(nodeID, SignalClockDomain, domain, declaredAt, rep),
			buildConfiguredValue(nodeID, SignalClockProvenance, provenance, declaredAt, rep),
		)
	}

	obs = append(obs, alignmentObservation(nodeID, p, rep))
	obs = append(obs, alignmentStateObservation(ctx, nodeID, p, rep, clockSrc))

	obs = append(obs,
		buildValue(nodeID, SignalLTCGeneratorState, p.LTCGeneratorState, observedAt, rep),
	)
	if p.LTCGeneratorState != "running" {
		obs = append(obs, buildValue(nodeID, SignalLTCGeneratorReason, p.LTCGeneratorReason, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCGeneratorReason, source, "generator is running; no reason is in effect", rep.receivedAt))
	}

	if p.LTCFrameRateKnown {
		obs = append(obs, buildValue(nodeID, SignalLTCFrameRate, p.LTCFrameRate, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCFrameRate, source, ltcFrameRateAbsentReason(p.LTCGeneratorState), rep.receivedAt))
	}

	if p.LTCTimecodeKnown {
		obs = append(obs, buildValue(nodeID, SignalLTCTimecode, p.LTCTimecode, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCTimecode, source, "the timecode generator is not confirmed running, so no fresh timecode is available", rep.receivedAt))
	}

	obs = append(obs, engineGlitchObservations(nodeID, p, observedAt, rep)...)
	obs = append(obs, engineRestoreObservations(nodeID, p, observedAt, rep)...)
	obs = append(obs, engineBackendObservations(nodeID, p, observedAt, rep)...)
	obs = append(obs, timelineObservations(nodeID, p, observedAt, rep)...)
	obs = append(obs, settingsObservations(nodeID, p, observedAt, rep)...)

	return obs
}

// settingsObservations renders the three node.audio.settings.* signals
// (see signals.go). State is always collected -- "accepted" is a real
// fact about a node that has never substituted a field, never "not
// collected" -- following [AudioPayload.SettingsState]'s own "\"\" reads
// as accepted" rule for a report from an agent built before this field
// existed. SubstitutedFields and Reason are collected only while State is
// "substituted": an accepted revision has nothing to name and no
// substitution to explain.
func settingsObservations(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)

	state := p.SettingsState
	if state == "" {
		state = "accepted"
	}

	obs := []observation.Observation{
		buildValue(nodeID, SignalSettingsState, state, observedAt, rep),
	}

	if state == "substituted" {
		obs = append(obs,
			buildValue(nodeID, SignalSettingsSubstitutedFields, strings.Join(p.SettingsSubstitutedFields, "; "), observedAt, rep),
			buildValue(nodeID, SignalSettingsReason, p.SettingsReason, observedAt, rep),
		)
	} else {
		reason := "this node applied its most recent audio.settings.configure revision as written; no field was substituted"
		obs = append(obs,
			notCollected(res, SignalSettingsSubstitutedFields, source, reason, rep.receivedAt),
			notCollected(res, SignalSettingsReason, source, reason, rep.receivedAt),
		)
	}

	return obs
}

// engineRestoreObservations renders the four node.audio.engine.restore.*
// signals (see signals.go). State and Attempts are always collected --
// "idle"/0 are real facts about a node that has never had an engine
// problem, never "not collected" -- following [AudioPayload.
// EngineRestoreState]'s own "\"\" reads as idle" rule for a report from
// an agent built before these fields existed. NextAttemptMs is collected
// only while State is "scheduled": 0 is otherwise genuinely ambiguous
// between "never started" and "gave up", which State (not this signal)
// exists to resolve, so reporting it not_collected rather than a
// fabricated 0 keeps that resolution honest. LastReason is collected
// only once Attempts is nonzero, matching sessionObservations' identical
// RestoreLastReason gate one resource kind down.
func engineRestoreObservations(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)

	state := p.EngineRestoreState
	if state == "" {
		state = "idle"
	}

	obs := []observation.Observation{
		buildValue(nodeID, SignalEngineRestoreState, state, observedAt, rep),
		buildValue(nodeID, SignalEngineRestoreAttempts, p.EngineRestoreAttempts, observedAt, rep),
	}

	if state == "scheduled" {
		obs = append(obs, buildValue(nodeID, SignalEngineRestoreNextAttemptMs, p.EngineRestoreNextAttemptMs, observedAt, rep))
	} else {
		reason := "no automatic restore attempt is scheduled: this node's restore-retry driver has not run or has exhausted its bounded schedule"
		obs = append(obs, notCollected(res, SignalEngineRestoreNextAttemptMs, source, reason, rep.receivedAt))
	}

	if p.EngineRestoreAttempts > 0 {
		obs = append(obs, buildValue(nodeID, SignalEngineRestoreLastReason, p.EngineRestoreLastReason, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalEngineRestoreLastReason, source, "no automatic restore attempt has been made on this node", rep.receivedAt))
	}

	return obs
}

// ltcStateObservation derives node.audio.ltc.state from live
// LTCGeneratorState, not the startup-probed LTCAvailable (finding 2).
// "stopped" counts as usable: an unbound channel reports unsupported, not
// stopped, so stopped already proves the node can drive LTC. Do not
// tighten this to running-only: LTC is idle before a show starts on
// every capable node, and that would reintroduce the pre-show wrong answer.
func ltcStateObservation(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) observation.Observation {
	state := StateUnavailable
	if p.LTCGeneratorState == "running" || p.LTCGeneratorState == "stopped" {
		state = StateUsable
	}
	return buildValue(nodeID, SignalLTCState, state, observedAt, rep)
}

// engineGlitchObservations renders the five node.audio.engine.* glitch
// signals (see signals.go for their tentative spellings). known=false
// reports [observation.StateNotCollected] on every one, never a
// fabricated healthy zero.
func engineGlitchObservations(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	if !p.EngineGlitchCountsKnown {
		reason := "this node's audio engine does not report glitch counts"
		return []observation.Observation{
			notCollected(res, SignalEngineStartedAt, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineWarningsStream, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineWarningsResource, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineWarningsOther, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineQosDrops, source, reason, rep.receivedAt),
		}
	}
	startedAt := ""
	if p.EngineGlitchCountsSince != nil {
		startedAt = p.EngineGlitchCountsSince.UTC().Format(time.RFC3339Nano)
	}
	return []observation.Observation{
		buildValue(nodeID, SignalEngineStartedAt, startedAt, observedAt, rep),
		buildValue(nodeID, SignalEngineWarningsStream, int64(p.EngineStreamWarningCount), observedAt, rep),
		buildValue(nodeID, SignalEngineWarningsResource, int64(p.EngineResourceWarningCount), observedAt, rep),
		buildValue(nodeID, SignalEngineWarningsOther, int64(p.EngineOtherWarningCount), observedAt, rep),
		buildValue(nodeID, SignalEngineQosDrops, int64(p.EngineQosDropCount), observedAt, rep),
	}
}

// engineBackendObservations renders the four node.audio.engine.sink_backend/
// sink_target/clock_source/clock_reason signals (see signals.go).
// EngineSinkBackend empty is an absent value, not a claimed cause: it
// covers a node with no engine built, an agent that predates these
// fields, and any other reason the agent's own report left it blank,
// matching [engineGlitchObservations]'s identical "known=false reports
// not_collected on every one" rule, since sink_target/clock_source/
// clock_reason are meaningless without a reported backend. SinkTarget is
// reported not_collected on its own, narrower gate: it only ever applies
// to a pipewiresink backend.
func engineBackendObservations(nodeID string, p mqttproto.AudioPayload, observedAt *time.Time, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	if p.EngineSinkBackend == "" {
		reason := "this node's report did not include an engine sink backend"
		return []observation.Observation{
			notCollected(res, SignalEngineSinkBackend, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineSinkTarget, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineClockSource, source, reason, rep.receivedAt),
			notCollected(res, SignalEngineClockReason, source, reason, rep.receivedAt),
		}
	}

	obs := []observation.Observation{
		buildValue(nodeID, SignalEngineSinkBackend, p.EngineSinkBackend, observedAt, rep),
	}
	if p.EngineSinkBackend == "pipewiresink" {
		obs = append(obs, buildValue(nodeID, SignalEngineSinkTarget, p.EngineSinkTarget, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalEngineSinkTarget, source, "this node's engine sink backend is not pipewiresink; no PipeWire target applies", rep.receivedAt))
	}
	obs = append(obs,
		buildValue(nodeID, SignalEngineClockSource, p.EngineClockSource, observedAt, rep),
		buildValue(nodeID, SignalEngineClockReason, p.EngineClockReason, observedAt, rep),
	)
	return obs
}

// sessionObservations renders every session in rep.payload.Sessions into
// audio_session.* observations, resource id the session id. Unlike
// nodeObservations, this is a dynamic list (sessions come and go), but it
// needs no dropped-item bookkeeping either: [Store] only ever holds a
// node's MOST RECENT report, so a session that no longer appears simply
// stops being reported — it does not need an explicit absence entry any
// more than a route that disappeared from Routes does.
func sessionObservations(nodeID string, rep report) []observation.Observation {
	var obs []observation.Observation
	for _, sess := range rep.payload.Sessions {
		obs = append(obs, oneSessionObservations(nodeID, sess, rep)...)
	}
	return obs
}

func oneSessionObservations(nodeID string, sess mqttproto.AudioSessionReport, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sess.SessionID}
	source := SourceForSession(nodeID, sess.SessionID)
	observedAt := rep.payload.ObservedAt // this report tick's own live evidence time; see AudioPayload.ObservedAt.

	// sessionAt is THIS session's own evidence time (mqttproto.
	// AudioSessionReport.CollectedAt), falling back to the report tick's
	// blanket time only when a node has not upgraded to send it yet. A
	// stale fallback (sess.Stale) carries its ORIGINAL CollectedAt
	// forward unchanged, so using it here — rather than the tick's own
	// observedAt — is what lets SignalSessionState/Fault/... genuinely
	// age when the node reports them stale, instead of every session
	// signal looking equally fresh regardless of Stale.
	sessionAt := observedAt
	if sess.CollectedAt != nil {
		sessionAt = sess.CollectedAt
	}

	obs := []observation.Observation{}

	if sess.HasSourceRole {
		obs = append(obs, buildSessionValue(res, source, SignalSessionSourceRole, sess.SourceRole, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionSourceRole, source, "session has no source role set", rep.receivedAt))
	}

	if sess.HasPlaylist {
		obs = append(obs, buildSessionValue(res, source, SignalSessionPlaylistRevision, int64(sess.PlaylistRevision), sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionPlaylistRevision, source, "session has no pinned playlist", rep.receivedAt))
	}

	if sess.HasItem {
		obs = append(obs,
			buildSessionValue(res, source, SignalSessionItemID, sess.ItemID, sessionAt, rep),
			buildSessionValue(res, source, SignalSessionItemIndex, sess.ItemIndex, sessionAt, rep),
		)
	} else {
		obs = append(obs,
			notCollected(res, SignalSessionItemID, source, "session has no current item", rep.receivedAt),
			notCollected(res, SignalSessionItemIndex, source, "session has no current item", rep.receivedAt),
		)
	}

	if sess.PositionKnown {
		posAt := sessionAt
		if sess.ObservedAt != nil {
			posAt = sess.ObservedAt
		}
		obs = append(obs, buildSessionValue(res, source, SignalSessionPositionMs, sess.PositionMs, posAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionPositionMs, source, "no fresh position is available from the engine; it is mid-discontinuity or has nothing loaded", rep.receivedAt))
	}

	obs = append(obs,
		notCollected(res, SignalSessionReferencePositionMs, source, "this build has no reference show-position source", rep.receivedAt),
		notCollected(res, SignalSessionDriftMs, source, "drift is only measured at track changes, and that measurement is not implemented yet", rep.receivedAt),
	)

	obs = append(obs, buildSessionValue(res, source, SignalSessionState, sess.State, sessionAt, rep))
	obs = append(obs, buildSessionValue(res, source, SignalSessionStateReason, sessionStateReason(sess.State), sessionAt, rep))
	obs = append(obs, buildSessionValue(res, source, SignalSessionDesiredRevision, int64(sess.DesiredRevision), sessionAt, rep))

	if sess.HasGain {
		gainDb := pkgaudio.GainToDb(pkgaudio.Gain(sess.Gain))
		obs = append(obs, buildSessionValue(res, source, SignalSessionGain, gainDb, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionGain, source, "session has no gain set", rep.receivedAt))
	}
	if sess.HasCeiling {
		ceilingDb := pkgaudio.CeilingToDb(pkgaudio.Ceiling(sess.Ceiling))
		obs = append(obs, buildSessionValue(res, source, SignalSessionGainCeiling, ceilingDb, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionGainCeiling, source, "session has no gain ceiling set", rep.receivedAt))
	}

	fadeState := sess.FadeState
	if fadeState == "" {
		fadeState = "none"
	}
	obs = append(obs, buildSessionValue(res, source, SignalSessionFadeState, fadeState, sessionAt, rep))

	if sess.Ducked {
		obs = append(obs, buildSessionValue(res, source, SignalSessionMixDuckedBy, sess.DuckedBy, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionMixDuckedBy, source, "session is not currently ducked", rep.receivedAt))
	}

	if sess.HasAssetProbe {
		obs = append(obs,
			buildSessionValue(res, source, SignalSessionAssetProbeState, sess.AssetProbeState, sessionAt, rep),
			buildSessionValue(res, source, SignalSessionAssetProbeReason, sess.AssetProbeReason, sessionAt, rep),
		)
	} else {
		obs = append(obs,
			notCollected(res, SignalSessionAssetProbeState, source, "no asset has been probed for this session yet", rep.receivedAt),
			notCollected(res, SignalSessionAssetProbeReason, source, "no asset has been probed for this session yet", rep.receivedAt),
		)
	}

	fault := sess.Fault
	if fault == "" {
		fault = "none"
	}
	obs = append(obs, buildSessionValue(res, source, SignalSessionFaultKind, fault, sessionAt, rep))
	if fault != "none" {
		obs = append(obs, buildSessionValue(res, source, SignalSessionFaultReason, sess.FaultReason, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionFaultReason, source, "session has no standing fault", rep.receivedAt))
	}

	ltcClaimState := sess.LTCClaimState
	if ltcClaimState == "" {
		ltcClaimState = "none"
	}
	obs = append(obs, buildSessionValue(res, source, SignalSessionLTCClaimState, ltcClaimState, sessionAt, rep))
	if ltcClaimState == "refused" {
		obs = append(obs, buildSessionValue(res, source, SignalSessionLTCClaimReason, sess.LTCClaimReason, sessionAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionLTCClaimReason, source, "session's LTC claim was not refused", rep.receivedAt))
	}

	// Gated on RestorePending, not on RestoreAttempts > 0: attempts
	// starting at 0 is genuinely ambiguous between "nothing queued" and
	// "queued, but the automatic retry driver has not attempted it yet"
	// — exactly the window an operator most needs to see, and the one a
	// gate on the count alone reports as nothing at all.
	if sess.RestorePending {
		obs = append(obs,
			buildSessionValue(res, source, SignalSessionRestoreAttempts, sess.RestoreAttempts, sessionAt, rep),
			buildSessionValue(res, source, SignalSessionRestoreNextAttemptMs, sess.RestoreNextAttemptMs, sessionAt, rep),
		)
		if sess.RestoreAttempts > 0 {
			obs = append(obs, buildSessionValue(res, source, SignalSessionRestoreLastReason, sess.RestoreLastReason, sessionAt, rep))
		} else {
			obs = append(obs, notCollected(res, SignalSessionRestoreLastReason, source, "a restore is queued but the automatic retry driver has not attempted it yet", rep.receivedAt))
		}
	} else {
		obs = append(obs,
			notCollected(res, SignalSessionRestoreAttempts, source, "no restore is currently queued for this session", rep.receivedAt),
			notCollected(res, SignalSessionRestoreNextAttemptMs, source, "no restore is currently queued for this session", rep.receivedAt),
			notCollected(res, SignalSessionRestoreLastReason, source, "no restore is currently queued for this session", rep.receivedAt),
		)
	}

	if sess.GapKnown {
		obs = append(obs,
			buildSessionConfiguredValue(res, source, SignalSessionItemGapMs, sess.ItemGapMs, sess.ItemGapObservedAt, rep),
			buildSessionConfiguredValue(res, source, SignalSessionItemGapReason, sess.ItemGapReason, sess.ItemGapObservedAt, rep),
		)
	} else {
		gapReason := sess.ItemGapReason
		if gapReason == "" {
			gapReason = "node reported no gap measurement and no reason"
		}
		obs = append(obs,
			notCollected(res, SignalSessionItemGapMs, source, gapReason, rep.receivedAt),
			notCollected(res, SignalSessionItemGapReason, source, gapReason, rep.receivedAt),
		)
	}

	// Stale is its own signal, always collected, always stamped with THIS
	// tick's own observedAt: whether the node could gather fresh evidence
	// this tick is itself fresh information, even when what it describes
	// (the signals above) is not.
	obs = append(obs, buildSessionValue(res, source, SignalSessionStale, sess.Stale, observedAt, rep))

	if sess.StartTrigger != "" {
		obs = append(obs, buildSessionValue(res, source, SignalSessionStartTrigger, sess.StartTrigger, sessionAt, rep))
		if sess.StartTrigger == "multisync" {
			obs = append(obs,
				buildSessionValue(res, source, SignalSessionTriggerSequenceFilename, sess.TriggerSequenceFilename, sessionAt, rep),
				buildSessionValue(res, source, SignalSessionTriggerArrivalNs, sess.TriggerArrivalNs, sessionAt, rep),
				buildSessionValue(res, source, SignalSessionStartLeadMs, int64(sess.StartLeadMs), sessionAt, rep),
			)
		} else {
			reason := "this session did not start from a MultiSync START packet"
			obs = append(obs,
				notCollected(res, SignalSessionTriggerSequenceFilename, source, reason, rep.receivedAt),
				notCollected(res, SignalSessionTriggerArrivalNs, source, reason, rep.receivedAt),
				notCollected(res, SignalSessionStartLeadMs, source, reason, rep.receivedAt),
			)
		}
		obs = append(obs, buildSessionValue(res, source, SignalSessionPreparedLate, sess.PreparedLate, sessionAt, rep))
	} else {
		reason := "this session has not started, or this node's build does not report how it started"
		obs = append(obs,
			notCollected(res, SignalSessionStartTrigger, source, reason, rep.receivedAt),
			notCollected(res, SignalSessionTriggerSequenceFilename, source, reason, rep.receivedAt),
			notCollected(res, SignalSessionTriggerArrivalNs, source, reason, rep.receivedAt),
			notCollected(res, SignalSessionStartLeadMs, source, reason, rep.receivedAt),
			notCollected(res, SignalSessionPreparedLate, source, reason, rep.receivedAt),
		)
	}

	return obs
}

// sessionStateReason states AUDIO-ENGINE section 15's distinction: Playing
// and Paused are engine-side claims this seam has not independently
// corroborated with anything an audience would experience, whether or not
// the wired engine actually reached an output. Every other state carries
// no such ambiguity to flag.
func sessionStateReason(state string) string {
	switch state {
	case "playing", "paused":
		return "the session state machine reports this; this seam has not independently confirmed audio actually reached an output"
	default:
		return "no playback claim is in effect in this state"
	}
}

// buildSessionValue is [buildValue]'s audio_session counterpart: same
// ADR-011 ObservedAt/CollectedAt split, different resource kind.
func buildSessionValue(res observation.ResourceRef, source string, sig observation.SignalID, value any, observedAt *time.Time, rep report) observation.Observation {
	opts := []observation.Option{
		observation.WithSource(source),
		observation.WithCollectedAt(rep.receivedAt),
	}
	if observedAt == nil {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
		}
		return o
	}
	opts = append(opts, observation.WithValidFor(DefaultValidFor))
	o, err := observation.Measured(res, sig, value, *observedAt, opts...)
	if err != nil {
		return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
	}
	return o
}

// buildConfiguredValue is [buildValue] for a value that is a configured
// fact, not a polled reading: it stays true for as long as the
// configuration that declared it stands, not for a fixed window after
// observedAt. ValidFor is left at zero, pkg/observation's existing "does
// not expire on its own" case, so the wire's validForSeconds renders null
// (see [Observation.StateAt] and internal/coordinator/api/mapping.go's
// mapEvidence) while observedAt still shows when the configuration was
// declared.
func buildConfiguredValue(nodeID string, sig observation.SignalID, value any, observedAt time.Time, rep report) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	o, err := observation.Measured(res, sig, value, observedAt,
		observation.WithSource(source), observation.WithCollectedAt(rep.receivedAt))
	if err != nil {
		return failed(res, sig, source, internalErrorReason(nodeID, err), rep.receivedAt)
	}
	return o
}

// buildSessionConfiguredValue is [buildSessionValue]'s counterpart for a
// measurement taken once at an event: it stays current until the next such
// event replaces it, or the session ends, never aged by a fixed time
// window. See [buildConfiguredValue]'s identical node-level rule.
func buildSessionConfiguredValue(res observation.ResourceRef, source string, sig observation.SignalID, value any, observedAt *time.Time, rep report) observation.Observation {
	opts := []observation.Option{
		observation.WithSource(source),
		observation.WithCollectedAt(rep.receivedAt),
	}
	if observedAt == nil {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
		}
		return o
	}
	o, err := observation.Measured(res, sig, value, *observedAt, opts...)
	if err != nil {
		return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
	}
	return o
}

func notCollected(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.NotCollected(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("nodeaudio: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

// SourceForSession returns the Source this package stamps on every
// audio_session.* observation for sessionID on nodeID: SourceFor(nodeID)
// plus the session id, so two sessions on the same node (or the same
// session id reused on two nodes, which should never happen but must not
// collide silently if it does) never collide on one observations-table
// row.
func SourceForSession(nodeID, sessionID string) string {
	return SourceFor(nodeID) + sourceNodeSeparator + sessionID
}

// lookupClockDomain reads nodeID's active audio.node configuration
// (ADR-039) from clockSrc, live, on every call — never cached — so an
// operator's write is reflected on the very next observation, the same
// live-read rule audionode.go's own placement check applies to capability
// evidence. reason is non-empty exactly when domain/provenance/declaredAt
// are not usable: no source wired in, no configuration ever activated for
// this node, or a store/decode failure.
func lookupClockDomain(ctx context.Context, src ClockDomainSource, nodeID string) (domain, provenance string, declaredAt time.Time, reason string) {
	if src == nil {
		return "", "", time.Time{}, "no configuration source wired into this coordinator"
	}
	obj, err := src.GetConfigObject(ctx, config.AudioNodeConfigKind, nodeID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return "", "", time.Time{}, "no audio.node configuration has been activated for this node"
	case err != nil:
		return "", "", time.Time{}, fmt.Sprintf("failed to read audio.node configuration: %v", err)
	case obj.CurrentRevision == 0:
		return "", "", time.Time{}, "no audio.node configuration has been activated for this node"
	}

	rev, err := src.GetConfigRevision(ctx, config.AudioNodeConfigKind, nodeID, obj.CurrentRevision)
	if err != nil {
		return "", "", time.Time{}, fmt.Sprintf("failed to read active audio.node configuration: %v", err)
	}
	var payload config.AudioNodePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return "", "", time.Time{}, fmt.Sprintf("stored audio.node configuration payload is malformed: %v", err)
	}
	return payload.ClockDomain, payload.ClockDomainProvenance, rev.CreatedAt, ""
}

// buildValue stamps ObservedAt from the caller-supplied observedAt:
// [mqttproto.AudioPayload.ObservedAt] at every call site in this package,
// never rep.receivedAt, the coordinator's own bookkeeping time, which
// stays CollectedAt. Matches noderender.buildValue's identical rule
// (ADR-011). observedAt nil means genuinely unknown, matching that
// field's own convention.
func buildValue(nodeID string, sig observation.SignalID, value any, observedAt *time.Time, rep report) observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	opts := []observation.Option{
		observation.WithSource(source),
		observation.WithCollectedAt(rep.receivedAt),
	}

	if observedAt == nil {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return failed(res, sig, source, internalErrorReason(nodeID, err), rep.receivedAt)
		}
		return o
	}

	opts = append(opts, observation.WithValidFor(DefaultValidFor))
	o, err := observation.Measured(res, sig, value, *observedAt, opts...)
	if err != nil {
		return failed(res, sig, source, internalErrorReason(nodeID, err), rep.receivedAt)
	}
	return o
}

func failed(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.CollectionFailed(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		// Every call site here passes a non-empty reason and a valid
		// res/sig; a failure is a bug in this file, matching
		// noderender.failed's identical panic.
		panic(fmt.Sprintf("nodeaudio: CollectionFailed(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func internalErrorReason(nodeID string, err error) string {
	return fmt.Sprintf("internal error building observation for node %s: %v", nodeID, err)
}

// SourceFor returns the [observation.Observation.Source] this package
// stamps on every observation it builds for nodeID: SourceName plus that
// node's own id — matches noderender.SourceFor exactly, and for the same
// reason (two nodes must never collide on one observations-table row).
func SourceFor(nodeID string) string {
	return SourceName + sourceNodeSeparator + nodeID
}

const sourceNodeSeparator = ":"

// NodeFromSource extracts the node id from a source built by [SourceFor],
// or ("", false) if source does not carry this package's prefix.
func NodeFromSource(source string) (string, bool) {
	prefix := SourceName + sourceNodeSeparator
	if !strings.HasPrefix(source, prefix) {
		return "", false
	}
	return strings.TrimPrefix(source, prefix), true
}
