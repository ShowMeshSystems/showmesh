package nodeaudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// Collector implements collector.Collector; enforced at compile time so a
// signature drift is caught here, matching noderender.Collector's identical
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
// never touches the network, so it always returns complete=true — matching
// noderender.Collector.Poll and fppmqtt.Collector.Poll.
//
// Unlike noderender, this package has no per-item list to diff against a
// previous poll (engine/device/program/ltc are fixed, one-per-node
// signals, never a dynamic set like surfaces), so it needs no dropped-item
// absence bookkeeping.
func (c *Collector) Poll(ctx context.Context) ([]observation.Observation, bool) {
	snap := c.store.snapshot()
	var obs []observation.Observation
	for nodeID, rep := range snap {
		obs = append(obs, nodeObservations(ctx, nodeID, rep, c.store.clockSrc)...)
	}
	return obs, true
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
	return nodeObservations(context.Background(), nodeID, rep, s.clockSrc)
}

func nodeObservations(ctx context.Context, nodeID string, rep report, clockSrc ClockDomainSource) []observation.Observation {
	p := rep.payload
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
		ltcState := StateUnavailable
		if p.LTCAvailable {
			ltcState = StateUsable
		}
		obs = append(obs,
			buildValue(nodeID, SignalDeviceState, deviceState, observedAt, rep),
			buildValue(nodeID, SignalDeviceReason, deviceReason, observedAt, rep),
			buildValue(nodeID, SignalOutputsCount, p.OutputsCount, observedAt, rep),
			buildValue(nodeID, SignalProgramState, programState, observedAt, rep),
			buildValue(nodeID, SignalLTCState, ltcState, observedAt, rep),
		)
	}

	obs = append(obs,
		buildValue(nodeID, SignalOutputsEnumerated, int64(p.EnumeratedCount), observedAt, rep),
		buildValue(nodeID, SignalOutputsTruncated, p.Truncated, observedAt, rep),
	)

	domain, provenance, declaredAt, reason := lookupClockDomain(ctx, clockSrc, nodeID)
	if reason != "" {
		obs = append(obs,
			failed(res, SignalClockDomain, source, reason, rep.receivedAt),
			failed(res, SignalClockProvenance, source, reason, rep.receivedAt),
		)
	} else {
		obs = append(obs,
			buildValue(nodeID, SignalClockDomain, domain, &declaredAt, rep),
			buildValue(nodeID, SignalClockProvenance, provenance, &declaredAt, rep),
		)
	}

	return obs
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

// buildValue stamps ObservedAt from the payload's own evidence timestamp
// (observedAt, i.e. rep.payload.ObservedAt), never rep.receivedAt — the
// coordinator's own bookkeeping time, which stays CollectedAt. Matches
// noderender.buildValue's identical rule (ADR-011, generalized a fourth
// time in this project). observedAt nil means genuinely unknown, matching
// [mqttproto.AudioPayload.ObservedAt]'s own convention.
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
