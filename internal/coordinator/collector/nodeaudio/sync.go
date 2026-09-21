package nodeaudio

import (
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is ADR-052 decision 6's sync status for one node:
// node.audio.sync.state/.follows/.offset_ns/.rate_ppm. Nothing here
// measures anything itself.

// LocalClockSourceDerived and LocalClockSourceOverride are the two values
// node.audio.clock.local.source carries: read off the configured program
// route, or named by the operator's localClockOverride (ADR-052).
const (
	LocalClockSourceDerived  = "derived"
	LocalClockSourceOverride = "override"
)

// The three values node.audio.sync.state carries (ADR-052 decision 6).
// SyncStateLocked requires both a locked clock provider and a pipeline
// built on that clock: either alone leaves the output free-running.
const (
	SyncStateLocked      = "locked"
	SyncStateAcquiring   = "acquiring"
	SyncStateFreeRunning = "free_running"
)

// syncObservations builds all four node.audio.sync.* observations for one
// node from what the pipeline runs on and the node's own PTP reading. A
// question neither answers reports [observation.StateNotCollected] with
// what is missing, never a free-running claim nobody checked.
func syncObservations(nodeID string, p mqttproto.AudioPayload, rep report, clockStat ClockStatusSource) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	observedAt := p.ObservedAt

	// An empty clock source is a node with no engine built, or an agent
	// that predates the field: neither is evidence about sync.
	if p.EngineClockSource == "" {
		return notCollectedSync(res, source, "this node has not reported which clock its audio output runs on", rep.receivedAt)
	}
	if clockStat == nil {
		return notCollectedSync(res, source, "no clock report source is wired into this coordinator", rep.receivedAt)
	}
	clock, ok := clockStat.NodeClockStatus(nodeID)
	if !ok {
		return notCollectedSync(res, source, "this node has never reported its clock status", rep.receivedAt)
	}

	state := syncState(p.EngineClockSource, clock.State)
	obs := []observation.Observation{
		buildValue(nodeID, SignalSyncState, state, observedAt, rep),
		buildValue(nodeID, SignalSyncFollows, syncFollows(state, clock), observedAt, rep),
	}

	if state != SyncStateFreeRunning && clock.OffsetKnown {
		obs = append(obs, buildValue(nodeID, SignalSyncOffsetNs, clock.OffsetNs, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSyncOffsetNs, source, syncOffsetAbsentReason(state), rep.receivedAt))
	}

	// Nothing in this codebase measures the rate adjustment PipeWire
	// applies to the output interface, and a zero would read as "no
	// correction is being applied" (RES-019 section 10).
	obs = append(obs, notCollected(res, SignalSyncRatePPM, source,
		"this node does not measure the rate adjustment applied to its output interface", rep.receivedAt))
	return obs
}

// syncState folds the node's pipeline clock and its PTP provider state
// into one answer. A pipeline on GStreamer's own system clock ("default")
// is free-running whatever the provider says: the provider is not what
// the samples come out against.
func syncState(engineClockSource, clockState string) string {
	if engineClockSource == "default" {
		return SyncStateFreeRunning
	}
	switch clockState {
	case "locked":
		return SyncStateLocked
	case "acquiring", "holdover":
		return SyncStateAcquiring
	default:
		return SyncStateFreeRunning
	}
}

// syncFollows names the global clock being followed: the grandmaster
// identity, plus the domain when the provider reported one. Blank while
// free-running or when no grandmaster was reported.
func syncFollows(state string, clock mqttproto.ClockPayload) string {
	if state == SyncStateFreeRunning || !clock.GMKnown || clock.GrandmasterIdentity == "" {
		return ""
	}
	if !clock.DomainKnown {
		return "PTP " + clock.GrandmasterIdentity
	}
	return "PTP " + clock.GrandmasterIdentity + ":" + strconv.FormatInt(clock.Domain, 10)
}

func syncOffsetAbsentReason(state string) string {
	if state == SyncStateFreeRunning {
		return "this node's audio output is not following a global clock, so there is no offset to report"
	}
	return "this node's clock provider did not report an offset for this reading"
}

// notCollectedSync reports all four sync signals as not collected for one
// shared reason, so a missing input never leaves one reading as a
// measurement.
func notCollectedSync(res observation.ResourceRef, source, reason string, at time.Time) []observation.Observation {
	return []observation.Observation{
		notCollected(res, SignalSyncState, source, reason, at),
		notCollected(res, SignalSyncFollows, source, reason, at),
		notCollected(res, SignalSyncOffsetNs, source, reason, at),
		notCollected(res, SignalSyncRatePPM, source, reason, at),
	}
}
