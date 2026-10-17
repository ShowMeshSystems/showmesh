package api

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Copies of nodeaudio's engine signal literals, for the same no-collector-
// import reason as audioEngineStateSignalID.
const (
	audioEngineSinkBackendSignalID observation.SignalID = "node.audio.engine.sink_backend"
	audioEngineClockSourceSignalID observation.SignalID = "node.audio.engine.clock_source"
	audioEngineSinkPipeWire        string               = "pipewiresink"
	audioEngineClockSourceDefault  string               = "default"
)

const nightAudioClockCheckPrefix = "audio:clock:"

// nightCheckAudioClock is one entry per configured audio.node. A node
// playing through PipeWire on GStreamer's default clock stalls the whole
// PipeWire graph while reporting itself usable, so that combination fails.
func (h *handlers) nightCheckAudioClock(ctx context.Context, now time.Time) []nightReadinessCheck {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.AudioNodeConfigKind)
	if err != nil {
		return []nightReadinessCheck{{name: "audio:clock", health: nightHealthUnknown(), cause: err,
			reason: "The coordinator could not read the list of audio nodes. Run readiness again."}}
	}
	var checks []nightReadinessCheck
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		checks = append(checks, h.nightCheckAudioClockForNode(now, obj.ID))
	}
	return checks
}

func (h *handlers) nightCheckAudioClockForNode(now time.Time, nodeID string) nightReadinessCheck {
	name := nightAudioClockCheckPrefix + nodeID
	var backend, source *observation.Observation
	for _, o := range h.deps.Audio.NodeAudioObservations(nodeID) {
		o := o
		switch o.Signal {
		case audioEngineSinkBackendSignalID:
			backend = &o
		case audioEngineClockSourceSignalID:
			source = &o
		}
	}
	if backend == nil || source == nil || backend.StateAt(now) != observation.StateCurrent || source.StateAt(now) != observation.StateCurrent {
		return nightReadinessCheck{name: name, health: nightCheckStateNotVerifiable, reason: fmt.Sprintf(
			"Node %s has not reported which clock its audio follows, so it could not be checked.", nodeID)}
	}
	if backend.Value == audioEngineSinkPipeWire && source.Value == audioEngineClockSourceDefault {
		return nightReadinessCheck{name: name, health: nightHealthFailed(), reason: fmt.Sprintf(
			"Node %s plays through PipeWire with no clock to follow, so it plays nothing. Set the node's PTP clock to its network interface in Node routing.", nodeID)}
	}
	return nightReadinessCheck{name: name, health: nightHealthHealthy()}
}
