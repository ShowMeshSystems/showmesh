package nodeaudio

import (
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SourceName is this collector's identity: the API's collectors[] list and
// [Collector.ID]'s Runner-registration identity, matching [Store]'s own
// reservation (docs/build/IDENTIFIER-REGISTER.md). Individual observations
// this package produces stamp [SourceFor](nodeID) instead — see that
// function's doc comment, matching noderender.SourceName/SourceFor exactly.
const SourceName = "node-audio"

// Signal vocabulary under the "node" resource kind (AUDIO-ENGINE section
// 13's exact list this seam ships).
const (
	SignalEngineState       observation.SignalID = "node.audio.engine.state"
	SignalEngineReason      observation.SignalID = "node.audio.engine.reason"
	SignalDeviceState       observation.SignalID = "node.audio.device.state"
	SignalDeviceReason      observation.SignalID = "node.audio.device.reason"
	SignalOutputsCount      observation.SignalID = "node.audio.outputs.count"
	SignalOutputsEnumerated observation.SignalID = "node.audio.outputs.enumerated"
	SignalOutputsTruncated  observation.SignalID = "node.audio.outputs.truncated"
	SignalProgramState      observation.SignalID = "node.audio.program.state"
	SignalLTCState          observation.SignalID = "node.audio.ltc.state"
	SignalClockDomain       observation.SignalID = "node.audio.clock.domain"
	SignalClockProvenance   observation.SignalID = "node.audio.clock.provenance"

	// SignalClockAlignment is AUDIO-ENGINE section 15's program-to-LTC
	// alignment readiness signal. Nothing in this seam measures it — no
	// loop-back or cross-output timing comparison exists yet — so it is
	// always [observation.StateNotCollected] with a reason, never
	// inferred from the program and LTC buses both being usable and
	// never from ClockDomain/ClockProvenance declaring a shared clock.
	// See [nodeObservations].
	SignalClockAlignment observation.SignalID = "node.audio.clock.alignment"

	// SignalLTCFrameRate, SignalLTCTimecode, SignalLTCGeneratorState, and
	// SignalLTCGeneratorReason are the LTC generator's reserved signals —
	// exact spellings, no additions without the owner. State
	// and Reason are the generator's own supervised-liveness evidence
	// (never inferred from EngineState/DeviceState/ProgramState above:
	// the generator can die while the rest of this node's audio reports
	// usable). Timecode is the generator's own self-reported position,
	// present only while State is "running".
	SignalLTCFrameRate       observation.SignalID = "node.audio.ltc.frame_rate"
	SignalLTCTimecode        observation.SignalID = "node.audio.ltc.timecode"
	SignalLTCGeneratorState  observation.SignalID = "node.audio.ltc.generator.state"
	SignalLTCGeneratorReason observation.SignalID = "node.audio.ltc.generator.reason"
)

// SignalEngineStartedAt, SignalEngineWarningsStream,
// SignalEngineWarningsResource, SignalEngineWarningsOther, and
// SignalEngineQosDrops report gstengine's bus-level glitch evidence
// (mqttproto.AudioPayload.EngineGlitchCounts*). TENTATIVE SPELLINGS: not
// owner-confirmed -- flagged for Eric in PR #87 rather than invented
// freely, per this file's own no-additions-without-the-owner rule.
// StartedAt is the reporting engine instance's counting epoch (a rebind
// resets it); the Warnings signals are WARNING-class bus messages
// bucketed by GError domain, not confirmed to identify an ALSA
// xrun/underrun (see gstengine's watchBus); QosDrops counts QOS-class
// bus messages. All five report [observation.StateNotCollected] when
// the bound engine backend does not collect this evidence.
const (
	SignalEngineStartedAt        observation.SignalID = "node.audio.engine.started_at"
	SignalEngineWarningsStream   observation.SignalID = "node.audio.engine.warnings.stream"
	SignalEngineWarningsResource observation.SignalID = "node.audio.engine.warnings.resource"
	SignalEngineWarningsOther    observation.SignalID = "node.audio.engine.warnings.other"
	SignalEngineQosDrops         observation.SignalID = "node.audio.engine.qos_drops"
)

// AllSignalIDs is every signal this package ever emits, in the order
// [Collector.Poll] builds them for one node.
var AllSignalIDs = []observation.SignalID{
	SignalEngineState,
	SignalEngineReason,
	SignalDeviceState,
	SignalDeviceReason,
	SignalOutputsCount,
	SignalOutputsEnumerated,
	SignalOutputsTruncated,
	SignalProgramState,
	SignalLTCState,
	SignalClockDomain,
	SignalClockProvenance,
	SignalClockAlignment,
	SignalLTCFrameRate,
	SignalLTCTimecode,
	SignalLTCGeneratorState,
	SignalLTCGeneratorReason,
	SignalEngineStartedAt,
	SignalEngineWarningsStream,
	SignalEngineWarningsResource,
	SignalEngineWarningsOther,
	SignalEngineQosDrops,
}

// Signal vocabulary under the "audio_session" resource kind, one
// session per resource id. Every signal here is a claim about the
// session state machine, never proof audio reached an output — see
// SignalSessionStateReason's own doc comment for where that distinction
// is stated explicitly.
const (
	SignalSessionSourceRole       observation.SignalID = "audio_session.source_role"
	SignalSessionPlaylistRevision observation.SignalID = "audio_session.playlist.revision"
	SignalSessionItemID           observation.SignalID = "audio_session.playlist.item_id"
	SignalSessionItemIndex        observation.SignalID = "audio_session.playlist.item_index"
	SignalSessionPositionMs       observation.SignalID = "audio_session.position_ms"

	// SignalSessionReferencePositionMs and SignalSessionDriftMs are
	// AUDIO-ENGINE section 15's reference-position and drift telemetry.
	// No source for either is wired into this seam (ADR-017: drift is
	// measured discretely at track boundaries, not continuously, and
	// that measurement does not exist yet), so both always report
	// [observation.StateNotCollected] with a reason.
	SignalSessionReferencePositionMs observation.SignalID = "audio_session.reference_position_ms"
	SignalSessionDriftMs             observation.SignalID = "audio_session.drift_ms"

	SignalSessionState observation.SignalID = "audio_session.state"

	// SignalSessionStateReason carries AUDIO-ENGINE section 15's distinction whenever
	// State reports Playing or Paused: an engine-side claim, not evidence
	// audio reached an output. Always present, never empty.
	SignalSessionStateReason observation.SignalID = "audio_session.state.reason"

	SignalSessionDesiredRevision observation.SignalID = "audio_session.desired_revision"

	// SignalSessionGain and SignalSessionGainCeiling report in decibels
	// (pkg/audio.GainToDb/CeilingToDb), matching the unit every
	// operator-facing gain input already takes (pkg/audio/gain.go's own
	// coordinator-boundary conversion). The agent's own report
	// (mqttproto.AudioSessionReport.Gain/Ceiling) and everything below
	// it — pkg/audio.Gain/Ceiling, the engine — stay linear; only this
	// collector's read side converts, so an operator who typed -6 sees
	// -6, never the linear multiplier that value produces.
	// audio_session.gain.effective_db and .ceiling_db stay reserved and
	// unshipped (docs/build/IDENTIFIER-REGISTER.md): these two existing
	// signals were converted in place instead of gaining companions.
	SignalSessionGain             observation.SignalID = "audio_session.gain.effective"
	SignalSessionGainCeiling      observation.SignalID = "audio_session.gain.ceiling"
	SignalSessionFadeState        observation.SignalID = "audio_session.fade.state"
	SignalSessionMixDuckedBy      observation.SignalID = "audio_session.mix.ducked_by"
	SignalSessionAssetProbeState  observation.SignalID = "audio_session.readiness.state"
	SignalSessionAssetProbeReason observation.SignalID = "audio_session.readiness.reason"
	SignalSessionFaultKind        observation.SignalID = "audio_session.fault.kind"
	SignalSessionFaultReason      observation.SignalID = "audio_session.fault.reason"

	// SignalSessionItemGapMs and SignalSessionItemGapReason are the
	// measured interval between one playlist item's natural completion
	// and its successor's confirmed start — a measurement, never a
	// restatement of the requested transition. Both report
	// [observation.StateNotCollected] with a stated reason, never zero,
	// whenever the node itself could not measure a gap (a first item, a
	// stopped session, a session that never advanced, or an advance whose
	// predecessor did not complete naturally).
	SignalSessionItemGapMs     observation.SignalID = "audio_session.item_gap_ms"
	SignalSessionItemGapReason observation.SignalID = "audio_session.item_gap.reason"

	// SignalSessionStale is true when the node could not collect this
	// session's OTHER signals fresh this report tick (it was busy inside
	// an in-flight engine call) and every other signal below is instead
	// its last known evidence, not current. Always collected -- never
	// [observation.StateNotCollected] -- and always fresh itself: this
	// tick's own poll is genuine evidence about staleness even when the
	// underlying session data it describes is not.
	SignalSessionStale observation.SignalID = "audio_session.stale"
)

// SessionSignalIDs is every audio_session.* signal this package ever
// emits, in the order [sessionObservations] builds them for one session.
var SessionSignalIDs = []observation.SignalID{
	SignalSessionSourceRole,
	SignalSessionPlaylistRevision,
	SignalSessionItemID,
	SignalSessionItemIndex,
	SignalSessionPositionMs,
	SignalSessionReferencePositionMs,
	SignalSessionDriftMs,
	SignalSessionState,
	SignalSessionStateReason,
	SignalSessionDesiredRevision,
	SignalSessionGain,
	SignalSessionGainCeiling,
	SignalSessionFadeState,
	SignalSessionMixDuckedBy,
	SignalSessionAssetProbeState,
	SignalSessionAssetProbeReason,
	SignalSessionFaultKind,
	SignalSessionFaultReason,
	SignalSessionItemGapMs,
	SignalSessionItemGapReason,
	SignalSessionStale,
}

// StateUsable and StateUnavailable are the two values SignalEngineState,
// SignalDeviceState, SignalProgramState, and SignalLTCState carry — the
// open two-state half of the three states these signals can be in; the
// third ("we do not know yet") is [observation.StateNotCollected] on the
// Absence field, not a third string value here, matching
// RenderPipelineState's identical open-string-plus-Absence convention one
// collector over.
const (
	StateUsable      = "usable"
	StateUnavailable = "unavailable"
)

// DefaultPollInterval is this collector's recommended [collector.Runner]
// cadence — matches noderender.DefaultPollInterval; SHOWMESH HYPOTHESIS,
// NOT MEASURED, per that constant's own doc comment.
const DefaultPollInterval = 5 * time.Second

// DefaultValidFor bounds how long a report stays [observation.StateCurrent]
// before ageing to stale — matches noderender.DefaultValidFor (3x the
// agent's own default publish interval); SHOWMESH HYPOTHESIS, NOT MEASURED.
const DefaultValidFor = 45 * time.Second
