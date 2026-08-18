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
