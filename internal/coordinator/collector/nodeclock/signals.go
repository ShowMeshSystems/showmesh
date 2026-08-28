package nodeclock

import (
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SourceName is this collector's identity: the API's collectors[] list
// and [Collector.ID]'s Runner-registration identity, matching [Store]'s
// own reservation (docs/build/IDENTIFIER-REGISTER.md). Individual
// observations this package produces stamp [SourceFor](nodeID) instead —
// matching noderender.SourceName/SourceFor and nodeaudio.SourceName/
// SourceFor exactly.
const SourceName = "node-clock"

// Signal vocabulary under the "node" resource kind — RES-019 section 5.2
// / 10, reserved 2026-08-28 before this seam started
// (docs/build/IDENTIFIER-REGISTER.md). Exact spellings; a seam that
// emits a spelling not in that table renames the code before it ships.
const (
	SignalState               observation.SignalID = "node.clock.ptp.state"
	SignalReason              observation.SignalID = "node.clock.ptp.reason"
	SignalProvider            observation.SignalID = "node.clock.ptp.provider"
	SignalRole                observation.SignalID = "node.clock.ptp.role"
	SignalOwner               observation.SignalID = "node.clock.ptp.owner"
	SignalInterface           observation.SignalID = "node.clock.ptp.interface"
	SignalDomain              observation.SignalID = "node.clock.ptp.domain"
	SignalGrandmasterIdentity observation.SignalID = "node.clock.ptp.grandmaster_identity"
	SignalTimescale           observation.SignalID = "node.clock.ptp.timescale"
	SignalOffsetNs            observation.SignalID = "node.clock.ptp.offset_ns"
	SignalClockClass          observation.SignalID = "node.clock.ptp.clock_class"
	SignalTimestamping        observation.SignalID = "node.clock.ptp.timestamping"
	SignalLockedSeconds       observation.SignalID = "node.clock.ptp.locked_seconds"
	SignalLastStepAt          observation.SignalID = "node.clock.ptp.last_step_at"
	SignalLastStepNs          observation.SignalID = "node.clock.ptp.last_step_ns"
	SignalMismatch            observation.SignalID = "node.clock.ptp.mismatch"
)

// AllSignalIDs is every signal this package ever emits, in the order
// [Collector.Poll] builds them for one node.
var AllSignalIDs = []observation.SignalID{
	SignalState,
	SignalReason,
	SignalProvider,
	SignalRole,
	SignalOwner,
	SignalInterface,
	SignalDomain,
	SignalGrandmasterIdentity,
	SignalTimescale,
	SignalOffsetNs,
	SignalClockClass,
	SignalTimestamping,
	SignalLockedSeconds,
	SignalLastStepAt,
	SignalLastStepNs,
	SignalMismatch,
}

// DefaultPollInterval is this collector's recommended [collector.Runner]
// cadence — matches nodeaudio.DefaultPollInterval; SHOWMESH HYPOTHESIS,
// NOT MEASURED, per that constant's own doc comment.
const DefaultPollInterval = 5 * time.Second

// DefaultValidFor bounds how long a report stays [observation.StateCurrent]
// before ageing to stale — matches nodeaudio.DefaultValidFor (3x the
// agent's own default publish interval); SHOWMESH HYPOTHESIS, NOT
// MEASURED.
const DefaultValidFor = 45 * time.Second
