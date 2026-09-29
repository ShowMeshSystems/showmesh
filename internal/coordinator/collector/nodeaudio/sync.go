package nodeaudio

import (
	"strconv"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is ADR-052 decision 6's sync status for one node:
// node.audio.sync.state/.follows/.offset_ns. Nothing here measures
// anything itself.

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

	// Every value below comes from the clock report, so all of them carry
	// its own evidence time: stamped with the audio report's, a clock
	// report that stopped hours ago would keep reading as current.
	observedAt := clock.ObservedAt

	state := syncState(p.EngineClockSource, clock.State)
	obs := []observation.Observation{buildValue(nodeID, SignalSyncState, state, observedAt, rep)}

	follows, followsKnown := syncFollows(state, clock)
	if followsKnown {
		obs = append(obs, buildValue(nodeID, SignalSyncFollows, follows, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSyncFollows, source,
			"this node's clock provider did not report a grandmaster for this reading", rep.receivedAt))
	}

	if state != SyncStateFreeRunning && clock.OffsetKnown {
		obs = append(obs, buildValue(nodeID, SignalSyncOffsetNs, clock.OffsetNs, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSyncOffsetNs, source, syncOffsetAbsentReason(state), rep.receivedAt))
	}

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
// free-running, which is a value. known is false when a following node's
// provider reported no grandmaster, which is not a value.
func syncFollows(state string, clock mqttproto.ClockPayload) (follows string, known bool) {
	if state == SyncStateFreeRunning {
		return "", true
	}
	if !clock.GMKnown || clock.GrandmasterIdentity == "" {
		return "", false
	}
	if !clock.DomainKnown {
		return "PTP " + clock.GrandmasterIdentity, true
	}
	return "PTP " + clock.GrandmasterIdentity + ":" + strconv.FormatInt(clock.Domain, 10), true
}

func syncOffsetAbsentReason(state string) string {
	if state == SyncStateFreeRunning {
		return "this node's audio output is not following a global clock, so there is no offset to report"
	}
	return "this node's clock provider did not report an offset for this reading"
}

// notCollectedSync reports all three sync signals as not collected for one
// shared reason, so a missing input never leaves one reading as a
// measurement.
func notCollectedSync(res observation.ResourceRef, source, reason string, at time.Time) []observation.Observation {
	return []observation.Observation{
		notCollected(res, SignalSyncState, source, reason, at),
		notCollected(res, SignalSyncFollows, source, reason, at),
		notCollected(res, SignalSyncOffsetNs, source, reason, at),
	}
}
