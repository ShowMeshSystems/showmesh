package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// audioReportPublishTimeout bounds a single audio report publish attempt,
// matching renderreport.go's identical renderReportPublishTimeout.
const audioReportPublishTimeout = 5 * time.Second

// audioSessionSnapshotter is the read side of an [audio.Manager] this
// package needs: fresh, per-session telemetry, never a command path. A
// nil snapshotter (no asset directory configured — see agent.go) reports
// zero sessions on every tick.
type audioSessionSnapshotter interface {
	Snapshot(ctx context.Context) []audio.SessionSnapshot
}

// ltcGeneratorSnapshotter is the read side of an [audio.LTCGenerator] this
// package needs: fresh liveness evidence on every tick, never a command
// path — matching audioSessionSnapshotter's identical shape. A nil
// snapshotter reports node.audio.ltc.generator.state "stopped" with a
// stated reason, never omits the four LTC generator signals: an absent
// field renders as blank and blank reads as fine (ADR-011).
type ltcGeneratorSnapshotter interface {
	Snapshot() audio.LTCGeneratorSnapshot
}

// noLTCGeneratorReason is what a nil ltcGeneratorSnapshotter reports —
// distinct from [audio.LTCGeneratorStopped]'s own "never started" reason,
// because "no generator is wired into this report loop at all" and "one is
// wired but has never been told to run" are different facts an operator
// debugging silent timecode loss needs told apart.
const noLTCGeneratorReason = "no LTC generator is configured on this node"

// runAudioReport publishes this node's audio report to nodeID's
// observed/audio topic on every tick received from ticks. Hardware
// discovery runs exactly once, before the loop starts: the throwaway
// probe pipelines it involves must never repeat on a fixed cadence for
// the life of the process (finding 1), so every tick republishes the SAME
// cached discovery evidence, with its own original observation time,
// never a fresh probe. A live device state change is picked up only at
// the next agent restart, or by the operator's own explicit
// "audio.device.probe" command (audioops.go), which probes one named
// device and is unrelated to this cache.
//
// Session telemetry is the opposite: mgr, when non-nil, is asked for a
// fresh [audio.Manager.Snapshot] on every tick, because a session's
// state, position, and fault are live facts a cache would make stale
// evidence look current.
//
// runAudioReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runRenderReport's identical
// contract.
func runAudioReport(ctx context.Context, pub Publisher, nodeID string, mgr audioSessionSnapshotter, ltcGen ltcGeneratorSnapshotter, now func() time.Time, ticks <-chan time.Time, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "audio")
	if err != nil {
		// nodeID is validated at config load, matching runRenderReport's
		// identical topic-build guard; should be unreachable in production.
		logger.Error("bug: could not build audio report topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	d := audioDiscoverer(ctx, audioEnumerator)
	discovery := buildAudioPayload(d, now())

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			payload := discovery
			payload.Sessions, payload.SessionsTruncated = buildAudioSessionReports(ctx, mgr)
			applyLTCGeneratorSnapshot(&payload, ltcGen)
			publishAudioPayload(ctx, pub, topic, nodeID, payload, now, logger)
		}
	}
}

// applyLTCGeneratorSnapshot writes ltcGen's current state onto payload's
// four LTC generator fields, fresh on every call — the same "live, never
// cached" rule [buildAudioSessionReports] follows and for the identical
// reason: generator liveness must never be inferred from anything cached.
func applyLTCGeneratorSnapshot(payload *mqttproto.AudioPayload, ltcGen ltcGeneratorSnapshotter) {
	if ltcGen == nil {
		payload.LTCGeneratorState = string(audio.LTCGeneratorStopped)
		payload.LTCGeneratorReason = noLTCGeneratorReason
		return
	}
	snap := ltcGen.Snapshot()
	payload.LTCGeneratorState = string(snap.State)
	payload.LTCGeneratorReason = ""
	if snap.State != audio.LTCGeneratorRunning {
		payload.LTCGeneratorReason = snap.Reason
	}
	if snap.FrameRateKnown {
		payload.LTCFrameRateKnown = true
		payload.LTCFrameRate = string(snap.FrameRate)
	}
	if snap.TimecodeKnown {
		payload.LTCTimecodeKnown = true
		payload.LTCTimecode = string(snap.Timecode)
	}
}

// buildAudioSessionReports turns mgr's current snapshot into wire form,
// bounded to maxAudioSessions via mqttproto's own Validate — this
// function truncates first so a node with more sessions than fit still
// publishes a valid payload rather than none at all. A nil mgr (no asset
// directory configured on this node) reports zero sessions, matching
// buildAudioPayload's own Routes convention: never nil, never omitted.
func buildAudioSessionReports(ctx context.Context, mgr audioSessionSnapshotter) ([]mqttproto.AudioSessionReport, bool) {
	if mgr == nil {
		return []mqttproto.AudioSessionReport{}, false
	}
	snaps := mgr.Snapshot(ctx)
	truncated := false
	if len(snaps) > audioSessionReportLimit {
		snaps = snaps[:audioSessionReportLimit]
		truncated = true
	}
	reports := make([]mqttproto.AudioSessionReport, 0, len(snaps))
	for _, s := range snaps {
		reports = append(reports, sessionReportFromSnapshot(s))
	}
	return reports, truncated
}

// audioSessionReportLimit mirrors mqttproto's unexported maxAudioSessions
// so this package can truncate before Validate ever sees an oversized
// payload, matching renderreport.go's identical surface-truncation
// convention for maxRenderSurfaces.
const audioSessionReportLimit = 16

func sessionReportFromSnapshot(s audio.SessionSnapshot) mqttproto.AudioSessionReport {
	r := mqttproto.AudioSessionReport{
		SessionID:        string(s.ID),
		HasSourceRole:    s.HasSourceRole,
		SourceRole:       string(s.SourceRole),
		HasPlaylist:      s.HasPlaylist,
		PlaylistRevision: uint64(s.PlaylistRevision),
		HasItem:          s.HasItem,
		ItemID:           s.ItemID,
		ItemIndex:        int64(s.ItemIndex),
		PositionKnown:    s.PositionKnown,
		PositionMs:       s.Position.Milliseconds(),
		State:            string(s.State),
		DesiredRevision:  uint64(s.DesiredRevision),
		HasGain:          s.HasGain,
		Gain:             float64(s.Gain),
		HasCeiling:       s.HasCeiling,
		Ceiling:          float64(s.Ceiling),
		FadeState:        string(s.FadeState),
		Ducked:           s.Ducked,
		DuckedBy:         string(s.DuckedBy),
		HasAssetProbe:    s.HasAssetProbe,
		AssetProbeState:  string(s.AssetProbeState),
		AssetProbeReason: s.AssetProbeReason,
		Fault:            string(s.Fault),
		FaultReason:      s.FaultReason,
	}
	if r.Fault == "" {
		r.Fault = "none"
	}
	if r.FadeState == "" {
		r.FadeState = "none"
	}
	if s.PositionKnown {
		observedAt := s.ObservedAt
		r.ObservedAt = &observedAt
	}
	return r
}

func publishAudioPayload(ctx context.Context, pub Publisher, topic, nodeID string, payload mqttproto.AudioPayload, now func() time.Time, logger *slog.Logger) {
	env, err := mqttproto.NewAudioEnvelope(now, nodeID, payload)
	if err != nil {
		logger.Error("failed to build audio report envelope", "error", err)
		return
	}
	data, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal audio report envelope", "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, audioReportPublishTimeout)
	defer cancel()

	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, data); err != nil {
		logger.Warn("audio report publish failed; will retry next tick", "error", err)
		return
	}

	logger.Debug("published audio report",
		"engine_available", payload.EngineAvailable, "outputs_count", payload.OutputsCount,
		"program_available", payload.ProgramAvailable, "ltc_available", payload.LTCAvailable)
}

// buildAudioPayload converts d into the wire payload, deriving
// DeviceAvailable/ProgramAvailable/LTCAvailable from the same
// [splitUsableRoutes] split detectAudioCapabilities uses, so the report and
// the advertised capability set can never disagree about what counts as
// usable. When d.HardwareEnumerated is false, DeviceAvailable/
// ProgramAvailable/LTCAvailable all report "we do not know", never "no
// hardware" — a shell-out failure must never be indistinguishable from a
// clean enumeration that genuinely found nothing.
func buildAudioPayload(d audio.Discovery, observedAt time.Time) mqttproto.AudioPayload {
	p := mqttproto.AudioPayload{
		EngineAvailable:          d.EngineUsable,
		EngineReason:             d.EngineReason,
		HardwareEnumerated:       d.HardwareEnumerated,
		HardwareEnumeratedReason: d.HardwareEnumeratedReason,
		Routes:                   make([]mqttproto.AudioRouteReport, 0, len(d.Routes)),
		Truncated:                d.Truncated,
		EnumeratedCount:          int64(d.EnumeratedCount),
		ObservedAt:               &observedAt,
		Sessions:                 []mqttproto.AudioSessionReport{},
		// Discovery-time default, self-consistent on its own — a caller
		// that never wires an [audio.LTCGenerator] (or calls this
		// function directly, as this file's own tests do) still gets a
		// payload that passes [mqttproto.AudioPayload.Validate]. Every
		// per-tick publish overwrites this via [applyLTCGeneratorSnapshot].
		LTCGeneratorState:  string(audio.LTCGeneratorStopped),
		LTCGeneratorReason: noLTCGeneratorReason,
	}
	if !p.EngineAvailable && p.EngineReason == "" {
		p.EngineReason = "audio engine probe did not reach PLAYING"
	}

	for _, r := range d.Routes {
		p.Routes = append(p.Routes, mqttproto.AudioRouteReport{
			Device: r.Device, Available: r.Available, Reason: r.Reason,
			Channels: int64(r.Channels), Rate: int64(r.Rate), Format: r.Format,
		})
	}

	usable, ltc := splitUsableRoutes(d.Routes)
	p.OutputsCount = int64(len(usable))

	if !d.HardwareEnumerated {
		reason := d.HardwareEnumeratedReason
		p.DeviceReason, p.ProgramReason, p.LTCReason = reason, reason, reason
	} else if len(usable) > 0 {
		p.DeviceAvailable = true
		p.ProgramAvailable = true
	} else {
		reason := "no real hardware candidate probed to PLAYING"
		if !d.HasHardwareCards {
			reason = "no ALSA hardware card found on this node"
		}
		p.DeviceReason = reason
		p.ProgramReason = reason
	}

	if d.HardwareEnumerated {
		if len(ltc) > 0 {
			p.LTCAvailable = true
		} else {
			p.LTCReason = "no route achieved 3 or more channels on a probe explicitly requesting them"
		}
	}

	return p
}
