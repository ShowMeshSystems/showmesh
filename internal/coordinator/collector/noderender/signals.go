package noderender

import (
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// SourceName is this collector's identity: the API's collectors[] list and
// [Collector.ID]'s Runner-registration identity, matching [Store]'s own
// reservation (docs/build/IDENTIFIER-REGISTER.md). Individual observations
// this package produces stamp [SourceFor](nodeID) instead, not this
// constant bare — see that function's doc comment.
const SourceName = "node-render"

// Signal vocabulary under the "surface" resource kind. One
// [mqttproto.RenderSurfaceReport] field per signal, chosen from the
// contract's list: pipeline state, restart count, consecutive failures,
// frames written/late/dropped, transport availability, and the pipeline's
// stated reason (which doubles as the last-exit reason: it is required
// whenever pipelineState is not "running" — see RenderSurfaceReport.Reason).
const (
	SignalSurfacePipelineState       observation.SignalID = "surface.pipeline.state"
	SignalSurfaceReason              observation.SignalID = "surface.pipeline.reason"
	SignalSurfaceRestartCount        observation.SignalID = "surface.pipeline.restart_count"
	SignalSurfaceConsecutiveFailures observation.SignalID = "surface.pipeline.consecutive_failures"
	SignalSurfaceFramesWritten       observation.SignalID = "surface.frames.written"
	SignalSurfaceFramesLate          observation.SignalID = "surface.frames.late"
	SignalSurfaceFramesDropped       observation.SignalID = "surface.frames.dropped"
	SignalSurfaceTransportAvailable  observation.SignalID = "surface.transport.available"
)

// AllSignalIDs is every signal this package ever emits, in the order
// [Collector.Poll] builds them for one surface — used by tests that need to
// enumerate the full vocabulary without hand-maintaining a second list.
var AllSignalIDs = []observation.SignalID{
	SignalSurfacePipelineState,
	SignalSurfaceReason,
	SignalSurfaceRestartCount,
	SignalSurfaceConsecutiveFailures,
	SignalSurfaceFramesWritten,
	SignalSurfaceFramesLate,
	SignalSurfaceFramesDropped,
	SignalSurfaceTransportAvailable,
}

// DefaultPollInterval is this collector's recommended [collector.Runner]
// cadence: how often the push cache in [Store] is re-rendered into
// observations, not how often a node publishes (that is the agent's own
// SHOWMESH_RENDER_REPORT_INTERVAL, defaulting to 15s — internal/agent/
// config). SHOWMESH HYPOTHESIS, NOT MEASURED, matching every other
// collector's own default in this codebase (fpp.DefaultPollInterval,
// resolume.DefaultPollInterval): short enough that a fresh report reaches
// the observations table quickly, long enough not to spin needlessly on a
// pure cache render with no I/O of its own.
const DefaultPollInterval = 5 * time.Second

// DefaultValidFor bounds how long a LIVE (non-retained) report stays
// [observation.StateCurrent] before ageing to stale. Three times the
// agent's default publish interval, the same "3x the producer's own
// cadence" convention internal/coordinator/collector/fpp.DefaultValidFor
// and internal/coordinator/inventory's heartbeat staleness window both use
// — SHOWMESH HYPOTHESIS, NOT MEASURED: this project has not run a render
// node long enough to know a real number.
const DefaultValidFor = 45 * time.Second
