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
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// noAlignmentMeasurementReason is [SignalClockAlignment]'s standing
// reason: no runtime path in this seam measures program-to-LTC alignment
// — see that signal's own doc comment for why it may never be inferred
// from anything else this package already reports.
const noAlignmentMeasurementReason = "no program-to-LTC alignment measurement is implemented; nothing in this seam can measure it"

// ltcFrameRateAbsentReason states why a node reports no frame rate, which
// differs between a node that cannot generate LTC at all and one that
// simply has not run it.
func ltcFrameRateAbsentReason(generatorState string) string {
	if generatorState == "unsupported" {
		return "this node cannot generate LTC, so no frame rate is in effect"
	}
	return "no LTC run has reported a frame rate on this node"
}

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
		obs = append(obs, sessionObservations(nodeID, rep)...)
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
	obs := nodeObservations(context.Background(), nodeID, rep, s.clockSrc)
	return append(obs, sessionObservations(nodeID, rep)...)
}

func nodeObservations(ctx context.Context, nodeID string, rep report, clockSrc ClockDomainSource) []observation.Observation {
	p := rep.payload
	// discoveredAt is the one-shot startup probe's evidence time and
	// backs engine/device/outputs/program/ltc-availability; observedAt is
	// this report tick's own live evidence time and backs the LTC
	// generator's four signals below. Sharing one timestamp between them
	// pinned every live signal to the agent's startup probe forever — see
	// [mqttproto.AudioPayload.ObservedAt]'s doc comment.
	discoveredAt := p.DiscoveredAt
	observedAt := p.ObservedAt

	res := observation.ResourceRef{Kind: observation.ResourceNode, ID: nodeID}
	source := SourceFor(nodeID)

	engineState, engineReason := StateUnavailable, p.EngineReason
	if p.EngineAvailable {
		engineState, engineReason = StateUsable, ""
	}

	obs := []observation.Observation{
		buildValue(nodeID, SignalEngineState, engineState, discoveredAt, rep),
		buildValue(nodeID, SignalEngineReason, engineReason, discoveredAt, rep),
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
			buildValue(nodeID, SignalDeviceState, deviceState, discoveredAt, rep),
			buildValue(nodeID, SignalDeviceReason, deviceReason, discoveredAt, rep),
			buildValue(nodeID, SignalOutputsCount, p.OutputsCount, discoveredAt, rep),
			buildValue(nodeID, SignalProgramState, programState, discoveredAt, rep),
			buildValue(nodeID, SignalLTCState, ltcState, discoveredAt, rep),
		)
	}

	obs = append(obs,
		buildValue(nodeID, SignalOutputsEnumerated, int64(p.EnumeratedCount), discoveredAt, rep),
		buildValue(nodeID, SignalOutputsTruncated, p.Truncated, discoveredAt, rep),
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

	obs = append(obs, notCollected(res, SignalClockAlignment, source, noAlignmentMeasurementReason, rep.receivedAt))

	obs = append(obs,
		buildValue(nodeID, SignalLTCGeneratorState, p.LTCGeneratorState, observedAt, rep),
	)
	if p.LTCGeneratorState != "running" {
		obs = append(obs, buildValue(nodeID, SignalLTCGeneratorReason, p.LTCGeneratorReason, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCGeneratorReason, source, "generator is running; no reason is in effect", rep.receivedAt))
	}

	if p.LTCFrameRateKnown {
		obs = append(obs, buildValue(nodeID, SignalLTCFrameRate, p.LTCFrameRate, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCFrameRate, source, ltcFrameRateAbsentReason(p.LTCGeneratorState), rep.receivedAt))
	}

	if p.LTCTimecodeKnown {
		obs = append(obs, buildValue(nodeID, SignalLTCTimecode, p.LTCTimecode, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalLTCTimecode, source, "generator is not confirmed running; no fresh timecode evidence", rep.receivedAt))
	}

	return obs
}

// sessionObservations renders every session in rep.payload.Sessions into
// audio_session.* observations, resource id the session id. Unlike
// nodeObservations, this is a dynamic list (sessions come and go), but it
// needs no dropped-item bookkeeping either: [Store] only ever holds a
// node's MOST RECENT report, so a session that no longer appears simply
// stops being reported — it does not need an explicit absence entry any
// more than a route that disappeared from Routes does.
func sessionObservations(nodeID string, rep report) []observation.Observation {
	var obs []observation.Observation
	for _, sess := range rep.payload.Sessions {
		obs = append(obs, oneSessionObservations(nodeID, sess, rep)...)
	}
	return obs
}

func oneSessionObservations(nodeID string, sess mqttproto.AudioSessionReport, rep report) []observation.Observation {
	res := observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: sess.SessionID}
	source := SourceForSession(nodeID, sess.SessionID)
	observedAt := rep.payload.ObservedAt // this report tick's own live evidence time; see AudioPayload.ObservedAt.

	obs := []observation.Observation{}

	if sess.HasSourceRole {
		obs = append(obs, buildSessionValue(res, source, SignalSessionSourceRole, sess.SourceRole, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionSourceRole, source, "session has no source role set", rep.receivedAt))
	}

	if sess.HasPlaylist {
		obs = append(obs, buildSessionValue(res, source, SignalSessionPlaylistRevision, int64(sess.PlaylistRevision), observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionPlaylistRevision, source, "session has no pinned playlist", rep.receivedAt))
	}

	if sess.HasItem {
		obs = append(obs,
			buildSessionValue(res, source, SignalSessionItemID, sess.ItemID, observedAt, rep),
			buildSessionValue(res, source, SignalSessionItemIndex, sess.ItemIndex, observedAt, rep),
		)
	} else {
		obs = append(obs,
			notCollected(res, SignalSessionItemID, source, "session has no current item", rep.receivedAt),
			notCollected(res, SignalSessionItemIndex, source, "session has no current item", rep.receivedAt),
		)
	}

	if sess.PositionKnown {
		posAt := observedAt
		if sess.ObservedAt != nil {
			posAt = sess.ObservedAt
		}
		obs = append(obs, buildSessionValue(res, source, SignalSessionPositionMs, sess.PositionMs, posAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionPositionMs, source, "no fresh engine evidence: mid-discontinuity or no handle loaded", rep.receivedAt))
	}

	obs = append(obs,
		notCollected(res, SignalSessionReferencePositionMs, source, "no reference show-position source is wired into this seam", rep.receivedAt),
		notCollected(res, SignalSessionDriftMs, source, "drift is measured at track boundaries only (ADR-017); that measurement is not implemented", rep.receivedAt),
	)

	obs = append(obs, buildSessionValue(res, source, SignalSessionState, sess.State, observedAt, rep))
	obs = append(obs, buildSessionValue(res, source, SignalSessionStateReason, sessionStateReason(sess.State), observedAt, rep))
	obs = append(obs, buildSessionValue(res, source, SignalSessionDesiredRevision, int64(sess.DesiredRevision), observedAt, rep))

	if sess.HasGain {
		obs = append(obs, buildSessionValue(res, source, SignalSessionGain, sess.Gain, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionGain, source, "session has no gain set", rep.receivedAt))
	}
	if sess.HasCeiling {
		obs = append(obs, buildSessionValue(res, source, SignalSessionGainCeiling, sess.Ceiling, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionGainCeiling, source, "session has no gain ceiling set", rep.receivedAt))
	}

	fadeState := sess.FadeState
	if fadeState == "" {
		fadeState = "none"
	}
	obs = append(obs, buildSessionValue(res, source, SignalSessionFadeState, fadeState, observedAt, rep))

	if sess.Ducked {
		obs = append(obs, buildSessionValue(res, source, SignalSessionMixDuckedBy, sess.DuckedBy, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionMixDuckedBy, source, "session is not currently ducked", rep.receivedAt))
	}

	if sess.HasAssetProbe {
		obs = append(obs,
			buildSessionValue(res, source, SignalSessionAssetProbeState, sess.AssetProbeState, observedAt, rep),
			buildSessionValue(res, source, SignalSessionAssetProbeReason, sess.AssetProbeReason, observedAt, rep),
		)
	} else {
		obs = append(obs,
			notCollected(res, SignalSessionAssetProbeState, source, "no asset has been probed for this session yet", rep.receivedAt),
			notCollected(res, SignalSessionAssetProbeReason, source, "no asset has been probed for this session yet", rep.receivedAt),
		)
	}

	fault := sess.Fault
	if fault == "" {
		fault = "none"
	}
	obs = append(obs, buildSessionValue(res, source, SignalSessionFaultKind, fault, observedAt, rep))
	if fault != "none" {
		obs = append(obs, buildSessionValue(res, source, SignalSessionFaultReason, sess.FaultReason, observedAt, rep))
	} else {
		obs = append(obs, notCollected(res, SignalSessionFaultReason, source, "session has no standing fault", rep.receivedAt))
	}

	return obs
}

// sessionStateReason states AUDIO-ENGINE section 15's distinction: Playing and Paused
// are engine-side claims this seam cannot corroborate with anything an
// audience would experience, because the only Engine this repository
// ships never plays audio (see internal/agent/audio.FakeEngine). Every
// other state carries no such ambiguity to flag.
func sessionStateReason(state string) string {
	switch state {
	case "playing", "paused":
		return "the session state machine reports this; no pipeline backend exists yet to confirm audio actually reached an output"
	default:
		return "no playback claim is in effect in this state"
	}
}

// buildSessionValue is [buildValue]'s audio_session counterpart: same
// ADR-011 ObservedAt/CollectedAt split, different resource kind.
func buildSessionValue(res observation.ResourceRef, source string, sig observation.SignalID, value any, observedAt *time.Time, rep report) observation.Observation {
	opts := []observation.Option{
		observation.WithSource(source),
		observation.WithCollectedAt(rep.receivedAt),
	}
	if observedAt == nil {
		o, err := observation.MeasuredUnknownAge(res, sig, value, opts...)
		if err != nil {
			return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
		}
		return o
	}
	opts = append(opts, observation.WithValidFor(DefaultValidFor))
	o, err := observation.Measured(res, sig, value, *observedAt, opts...)
	if err != nil {
		return notCollected(res, sig, source, fmt.Sprintf("internal error building observation: %v", err), rep.receivedAt)
	}
	return o
}

func notCollected(res observation.ResourceRef, sig observation.SignalID, source, reason string, at time.Time) observation.Observation {
	o, err := observation.NotCollected(res, sig, reason,
		observation.WithSource(source), observation.WithCollectedAt(at))
	if err != nil {
		panic(fmt.Sprintf("nodeaudio: NotCollected(%q) unexpectedly failed: %v", sig, err))
	}
	return o
}

// SourceForSession returns the Source this package stamps on every
// audio_session.* observation for sessionID on nodeID: SourceFor(nodeID)
// plus the session id, so two sessions on the same node (or the same
// session id reused on two nodes, which should never happen but must not
// collide silently if it does) never collide on one observations-table
// row.
func SourceForSession(nodeID, sessionID string) string {
	return SourceFor(nodeID) + sourceNodeSeparator + sessionID
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

// buildValue stamps ObservedAt from whichever evidence timestamp the
// caller passes as observedAt — [mqttproto.AudioPayload.DiscoveredAt] for
// the one-shot discovery signals, [mqttproto.AudioPayload.ObservedAt] for
// the per-tick LTC generator signals — never rep.receivedAt, the
// coordinator's own bookkeeping time, which stays CollectedAt. Matches
// noderender.buildValue's identical rule (ADR-011, generalized a fourth
// time in this project). observedAt nil means genuinely unknown, matching
// those fields' own convention.
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
