package fppconnectpush

import (
	"fmt"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Signal vocabulary for a node's most recently resolved FPP Connect
// channel-range push (docs/build/IDENTIFIER-REGISTER.md): node-level,
// on the node.multisync.* precedent (ADR-044) — one
// fppconnect.configure push carries one channelRanges string per node, so
// attributing a dropped range to a surface would report the same fact once
// per surface and imply that many faults.
const (
	SignalChannelRangeState  observation.SignalID = "node.fppconnect.channel_range.state"
	SignalChannelRangeReason observation.SignalID = "node.fppconnect.channel_range.reason"
)

// AllSignalIDs is both node.fppconnect.channel_range.* signals, in the
// order [StatusStore.NodeFPPConnectObservations] builds them.
var AllSignalIDs = []observation.SignalID{
	SignalChannelRangeState,
	SignalChannelRangeReason,
}

// observationSource names this package in every observation
// [StatusStore.NodeFPPConnectObservations] builds, matching
// noderender.SourceName's identical role one push surface over.
const observationSource = "fppconnect-push"

// Open channel-range state vocabulary for [SignalChannelRangeState]'s
// Value, mirroring mqttproto.RenderPipelineState's identical open-string
// convention: a consumer that does not recognize one treats it as evidence
// with an unrecognized label, never an error.
const (
	// ChannelRangeStateFormatted means the most recently resolved push
	// successfully formatted at least one show.surface's channel range
	// into the string this node's fppconnect.configure push carried.
	ChannelRangeStateFormatted = "formatted"

	// ChannelRangeStateNoSurfaces means this node has no configured
	// show.surface at all, so its most recent push legitimately carried an
	// empty channelRanges string (RES-003 section 10.1) — never a dropped
	// range. SignalChannelRangeReason carries no reason alongside this
	// state.
	ChannelRangeStateNoSurfaces = "no_surfaces"

	// ChannelRangeStateDropped means this node HAS at least one configured
	// show.surface, but [fppconnect.FormatChannelRanges] refused to format
	// them (a refused range, or a combined string too long for the ping's
	// 120-byte field), so the coordinator pushed an empty channelRanges
	// string instead — the gigabytes-per-song case RES-003 section 9.5
	// warns about. SignalChannelRangeReason always carries the refusal's
	// own error text alongside this state.
	ChannelRangeStateDropped = "dropped"
)

// channelRangeStatus is one node's most recently resolved channel-range
// outcome.
type channelRangeStatus struct {
	state      string
	reason     string
	observedAt time.Time
}

// StatusStore holds, per node, the outcome of that node's most recently
// resolved fppconnect.configure channel-range push (see [resolveForNode]
// and [ToNode]) — an in-memory cache, never persisted: a node's next
// hello, or the coordinator's next write to any of the four kinds this
// package watches, recomputes it, the same "recomputed rather than
// durably stored" posture every other field [resolveForNode] resolves
// already has. The zero value is not usable; construct with
// [NewStatusStore].
type StatusStore struct {
	mu     sync.RWMutex
	byNode map[string]channelRangeStatus
}

// NewStatusStore builds an empty StatusStore.
func NewStatusStore() *StatusStore {
	return &StatusStore{byNode: make(map[string]channelRangeStatus)}
}

// record stores nodeID's channel-range resolution outcome. Called from
// [ToNode] on every successfully resolved push, regardless of whether the
// MQTT publish that follows succeeds: the formatting outcome is a fact
// about the RESOLVED state, independent of transport delivery. Nil-safe: a
// nil *StatusStore records nothing, matching [ToNode]'s own "an unwired
// dependency is a no-op, never a panic" posture.
func (s *StatusStore) record(nodeID, state, reason string, observedAt time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byNode[nodeID] = channelRangeStatus{state: state, reason: reason, observedAt: observedAt}
}

// NodeFPPConnectObservations returns nodeID's two
// node.fppconnect.channel_range.* observations, or nil when this node has
// never had a channel-range push resolved for it yet — a freshly declared
// node, or a coordinator that has not completed a first hello/write cycle
// for it. Mirrors [noderender.Store.NodeRenderObservations]'s identical
// synthesize-on-demand shape one push surface over: never null on the
// wire (mapNode renders nil as an empty array), and never blocks on I/O.
// Nil-safe: a nil *StatusStore (an unwired dependency) reports the same
// nil as an empty, never-pushed-to store.
func (s *StatusStore) NodeFPPConnectObservations(nodeID string) []observation.Observation {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	st, ok := s.byNode[nodeID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	stateObs := mustMeasured(res, SignalChannelRangeState, st.state, st.observedAt)

	// SignalChannelRangeReason is always Measured, even when st.reason is
	// "" (the formatted or no_surfaces case), matching
	// noderender.SignalSurfaceReason's identical "always Measured, empty
	// string when not applicable" precedent one signal pair over.
	reasonObs := mustMeasured(res, SignalChannelRangeReason, st.reason, st.observedAt)

	return []observation.Observation{stateObs, reasonObs}
}

// mustMeasured builds a Measured observation from well-formed, package-
// internal inputs. res/sig/observedAt are always valid by construction at
// every call site in this file, so a Validate failure here is a bug in
// this file, not a runtime condition to degrade from gracefully — matching
// noderender's identical failed/notCollected panic convention.
func mustMeasured(res observation.ResourceRef, sig observation.SignalID, value any, observedAt time.Time) observation.Observation {
	o, err := observation.Measured(res, sig, value, observedAt,
		observation.WithSource(observationSource), observation.WithCollectedAt(observedAt))
	if err != nil {
		panic(fmt.Sprintf("fppconnectpush: Measured(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}
