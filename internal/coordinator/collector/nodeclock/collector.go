package nodeclock

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift is caught here, matching nodeaudio.Collector's identical
// assertion.
var _ collector.Collector = (*Collector)(nil)

// Collector renders [Store]'s current push cache into observations on a
// collector.Runner's own cadence. The zero value is not usable; construct
// with [New].
type Collector struct {
	store *Store
}

// New builds a Collector reading from store.
func New(store *Store) *Collector {
	return &Collector{store: store}
}

// ID returns [SourceName].
func (c *Collector) ID() string { return SourceName }

// Poll renders every node's currently stored report into observations. It
// never touches the network, so it always returns complete=true —
// matching nodeaudio.Collector.Poll and noderender.Collector.Poll.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	snap := c.store.snapshot()
	var obs []observation.Observation
	for nodeID, rep := range snap {
		obs = append(obs, nodeObservations(nodeID, rep)...)
	}
	return obs, true
}

// NodeClockObservations returns every node.clock.ptp.* observation this
// coordinator currently holds for nodeID's most recently reported clock
// status, or nil if nodeID has never published one. The node read path's
// synthesize-at-read-time counterpart to [Collector.Poll], matching
// nodeaudio.Store.NodeAudioObservations exactly.
func (s *Store) NodeClockObservations(nodeID string) []observation.Observation {
	rep, ok := s.get(nodeID)
	if !ok {
		return nil
	}
	return nodeObservations(nodeID, rep)
}

func nodeObservations(nodeID string, rep report) []observation.Observation {
	p := rep.payload
	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)
	observedAt := p.ObservedAt // required by ClockPayload.Validate, never nil in practice

	obs := []observation.Observation{
		buildValue(nodeID, SignalState, p.State, observedAt, rep),
	}

	if p.State == "locked" {
		obs = append(obs, notCollected(res, SignalReason, source, "state is locked; no reason is in effect", rep.receivedAt))
	} else {
		obs = append(obs, buildValue(nodeID, SignalReason, p.Reason, observedAt, rep))
	}

	obs = append(obs, buildValue(nodeID, SignalProvider, p.Provider, observedAt, rep))

	if p.RoleKnown {
		obs = append(obs, buildValue(nodeID, SignalRole, p.Role, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalRole, source, "provider did not report a role for this reading", rep.receivedAt))
	}

	if p.Owner != "" {
		obs = append(obs, buildValue(nodeID, SignalOwner, p.Owner, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalOwner, source, "provider did not report which component owns ptp4l", rep.receivedAt))
	}

	if p.Interface != "" {
		obs = append(obs, buildValue(nodeID, SignalInterface, p.Interface, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalInterface, source, "no interface is configured for this node's clock provider", rep.receivedAt))
	}

	if p.DomainKnown {
		obs = append(obs, buildValue(nodeID, SignalDomain, p.Domain, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalDomain, source, "provider did not report an observed domain", rep.receivedAt))
	}

	if p.GMKnown {
		obs = append(obs, buildValue(nodeID, SignalGrandmasterIdentity, p.GrandmasterIdentity, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalGrandmasterIdentity, source, "provider did not report a grandmaster identity", rep.receivedAt))
	}

	obs = append(obs, buildValue(nodeID, SignalTimescale, p.Timescale, observedAt, rep))

	if p.OffsetKnown {
		obs = append(obs, buildValue(nodeID, SignalOffsetNs, p.OffsetNs, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalOffsetNs, source, "no fresh offset evidence (not currently locked)", rep.receivedAt))
	}

	if p.ClockClassKnown {
		obs = append(obs, buildValue(nodeID, SignalClockClass, p.ClockClass, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalClockClass, source, "provider did not report a clock class", rep.receivedAt))
	}

	if p.TimestampingKnown {
		obs = append(obs, buildValue(nodeID, SignalTimestamping, p.Timestamping, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalTimestamping, source, "provider did not report a timestamping mode", rep.receivedAt))
	}

	if p.LockedSecondsKnown {
		obs = append(obs, buildValue(nodeID, SignalLockedSeconds, p.LockedSeconds, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLockedSeconds, source, "not currently locked or in holdover", rep.receivedAt))
	}

	if p.LastStepKnown {
		lastStepAt := ""
		if p.LastStepAt != nil {
			lastStepAt = p.LastStepAt.UTC().Format(time.RFC3339Nano)
		}
		obs = append(obs,
			buildValue(nodeID, SignalLastStepAt, lastStepAt, observedAt, rep),
			buildValue(nodeID, SignalLastStepNs, p.LastStepNs, observedAt, rep),
		)
	} else {
		reason := "no step (grandmaster change) has been observed since this node's clock provider started"
		obs = append(obs,
			notCollected(res, SignalLastStepAt, source, reason, rep.receivedAt),
			notCollected(res, SignalLastStepNs, source, reason, rep.receivedAt),
		)
	}

	obs = append(obs, buildValue(nodeID, SignalMismatch, p.Mismatch, observedAt, rep))

	return obs
}

func notCollected(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.NotCollected(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("nodeclock: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func failed(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.CollectionFailed(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		// Every call site here passes a non-empty reason and a valid
		// res/sig; a failure is a bug in this file, matching
		// nodeaudio.failed's identical panic.
		panic(fmt.Sprintf("nodeclock: CollectionFailed(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

func internalErrorReason(nodeID string, err error) string {
	return fmt.Sprintf("internal error building observation for node %s: %v", nodeID, err)
}

// buildValue stamps ObservedAt from observedAt (this node's own evidence
// time — [mqttproto.ClockPayload.ObservedAt], required by Validate, so
// never nil in practice), never rep.receivedAt, the coordinator's own
// bookkeeping time, which stays CollectedAt — matching nodeaudio.
// buildValue's identical ADR-011 rule.
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

// SourceFor returns the [observation.Observation.Source] this package
// stamps on every observation it builds for nodeID: SourceName plus that
// node's own id — matches nodeaudio.SourceFor exactly, and for the same
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
