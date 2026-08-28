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

// ltcObserver is the read side of this node's LTC generation — fresh
// evidence on every tick, never a command path, matching
// audioSessionSnapshotter's identical shape. A nil observer reports
// node.audio.ltc.generator.state "unsupported" with a stated reason and
// never omits the four LTC signals: an absent field renders as blank and
// blank reads as fine (ADR-011).
type ltcObserver interface {
	ObserveLTC(ctx context.Context) audio.LTCObservation
}

// engineAvailability is the live playback engine evidence this report
// loop consults on every tick — the same [audio.SwitchableEngine.
// Available] method agent.go already wires into hello capabilities
// (audiocapabilities.go's audioEngineAvailable var), asked here too so
// the published audio report can never disagree with what the node
// would actually claim in its hello advertisement. A nil
// engineAvailability leaves EngineAvailable/EngineReason at the startup
// discovery cache's value, matching this loop's other nil-safe optional
// sources.
type engineAvailability interface {
	Available() (bool, string)
}

// engineGlitchCounts is the optional glitch-evidence side of
// [engineAvailability] this report loop checks for on every tick via a
// type assertion — matching [audio.GlitchObserver]'s own optional-
// interface shape, so a wired engine that does not implement it (the
// gstengine stub, a test fake) reports EngineGlitchCountsKnown false
// rather than a fabricated zero.
type engineGlitchCounts interface {
	GlitchCounts() (audio.GlitchCounts, bool)
}

// noLTCObserverReason is what a nil ltcObserver reports: no LTC source is
// wired into this report loop at all, which is a different fact from an
// engine that cannot generate LTC.
const noLTCObserverReason = "no LTC source is wired into this node's audio report"

// runAudioReport publishes this node's audio report to nodeID's
// observed/audio topic on every tick received from ticks. Hardware
// discovery runs exactly once, before the loop starts: the throwaway
// probe pipelines it involves must never repeat on a fixed cadence for
// the life of the process (finding 1), so every tick republishes the SAME
// cached discovery evidence, carrying its own original probe time in
// [mqttproto.AudioPayload.DiscoveredAt], never a fresh probe. A live
// device state change is picked up only at the next agent restart, or by
// the operator's own explicit "audio.device.probe" command
// (audioops.go), which probes one named device and is unrelated to this
// cache.
//
// Session and LTC telemetry are the opposite: mgr and ltc, when non-nil,
// are asked for fresh evidence on every tick, because a session's state,
// position, and fault, and whether LTC is actually being emitted, are
// live facts a cache would make stale evidence look current. That live
// evidence is stamped with its own tick time in
// [mqttproto.AudioPayload.ObservedAt] — distinct from DiscoveredAt so
// this evidence can never again be reported as though it aged out
// alongside a startup probe that has nothing to do with it.
//
// runAudioReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runRenderReport's identical
// contract.
func runAudioReport(ctx context.Context, pub Publisher, nodeID string, mgr audioSessionSnapshotter, ltc ltcObserver, engine engineAvailability, now func() time.Time, ticks <-chan time.Time, logger *slog.Logger) {
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
			tickAt := now()
			payload.ObservedAt = &tickAt
			payload.Sessions, payload.SessionsTruncated = buildAudioSessionReports(ctx, mgr)
			applyLTCObservation(ctx, &payload, ltc)
			applyEngineAvailability(&payload, engine)
			applyEngineGlitchCounts(&payload, engine)
			publishAudioPayload(ctx, pub, topic, nodeID, payload, now, logger)
		}
	}
}

// applyEngineAvailability overwrites payload's EngineAvailable/
// EngineReason with engine's live evidence, fresh on every call — the
// startup discovery cache buildAudioPayload seeded them from otherwise
// never updates for the life of the process, so a pipeline that goes
// broken hours into a show (device unplug, a fatal sink error) would
// keep reporting the discovery-time verdict forever. A nil engine (no
// asset directory configured on this node, matching
// audioSessionSnapshotter/ltcObserver's identical nil convention) leaves
// the discovery cache's value in place.
func applyEngineAvailability(payload *mqttproto.AudioPayload, engine engineAvailability) {
	if engine == nil {
		return
	}
	ok, reason := engine.Available()
	payload.EngineAvailable = ok
	payload.EngineReason = reason
	if !ok && payload.EngineReason == "" {
		payload.EngineReason = "audio engine probe did not reach PLAYING"
	}
}

// applyEngineGlitchCounts writes engine's cumulative bus-level glitch
// counts onto payload, fresh on every call — same "live, never cached"
// rule as [applyEngineAvailability]. A nil engine, or one that does not
// implement [engineGlitchCounts], leaves EngineGlitchCountsKnown false:
// never a fabricated healthy zero for evidence nothing actually
// collected.
func applyEngineGlitchCounts(payload *mqttproto.AudioPayload, engine engineAvailability) {
	payload.EngineGlitchCountsKnown = false
	payload.EngineGlitchCountsSince = nil
	payload.EngineStreamWarningCount = 0
	payload.EngineResourceWarningCount = 0
	payload.EngineOtherWarningCount = 0
	payload.EngineQosDropCount = 0
	if engine == nil {
		return
	}
	g, ok := engine.(engineGlitchCounts)
	if !ok {
		return
	}
	counts, known := g.GlitchCounts()
	if !known {
		return
	}
	payload.EngineGlitchCountsKnown = true
	since := counts.Since
	payload.EngineGlitchCountsSince = &since
	payload.EngineStreamWarningCount = counts.StreamWarnings
	payload.EngineResourceWarningCount = counts.ResourceWarnings
	payload.EngineOtherWarningCount = counts.OtherWarnings
	payload.EngineQosDropCount = counts.QosEvents
}

// applyLTCObservation writes ltc's current evidence onto payload's four
// LTC fields, fresh on every call — the same "live, never cached" rule
// [buildAudioSessionReports] follows and for the identical reason: LTC
// liveness must never be inferred from anything cached.
func applyLTCObservation(ctx context.Context, payload *mqttproto.AudioPayload, ltc ltcObserver) {
	if ltc == nil {
		payload.LTCGeneratorState = string(audio.LTCUnsupported)
		payload.LTCGeneratorReason = noLTCObserverReason
		return
	}
	obs := ltc.ObserveLTC(ctx)
	payload.LTCGeneratorState = string(obs.State)
	payload.LTCGeneratorReason = ""
	if obs.State != audio.LTCRunning {
		payload.LTCGeneratorReason = obs.Reason
	}
	if obs.FrameRateKnown {
		payload.LTCFrameRateKnown = true
		payload.LTCFrameRate = string(obs.FrameRate)
	}
	if obs.TimecodeKnown {
		payload.LTCTimecodeKnown = true
		payload.LTCTimecode = string(obs.Timecode)
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
		SessionID:            string(s.ID),
		HasSourceRole:        s.HasSourceRole,
		SourceRole:           string(s.SourceRole),
		HasPlaylist:          s.HasPlaylist,
		PlaylistRevision:     uint64(s.PlaylistRevision),
		HasItem:              s.HasItem,
		ItemID:               s.ItemID,
		ItemIndex:            int64(s.ItemIndex),
		PositionKnown:        s.PositionKnown,
		PositionMs:           s.Position.Milliseconds(),
		State:                string(s.State),
		DesiredRevision:      uint64(s.DesiredRevision),
		HasGain:              s.HasGain,
		Gain:                 float64(s.Gain),
		HasCeiling:           s.HasCeiling,
		Ceiling:              float64(s.Ceiling),
		FadeState:            string(s.FadeState),
		Ducked:               s.Ducked,
		DuckedBy:             string(s.DuckedBy),
		HasAssetProbe:        s.HasAssetProbe,
		AssetProbeState:      string(s.AssetProbeState),
		AssetProbeReason:     s.AssetProbeReason,
		GapKnown:             s.GapKnown,
		ItemGapMs:            s.Gap.Milliseconds(),
		ItemGapReason:        s.GapReason,
		Fault:                string(s.Fault),
		FaultReason:          s.FaultReason,
		LTCClaimState:        string(s.LTCClaimState),
		LTCClaimReason:       s.LTCClaimReason,
		RestorePending:       s.RestorePending,
		RestoreAttempts:      int64(s.RestoreAttempts),
		RestoreNextAttemptMs: s.RestoreNextAttempt.Milliseconds(),
		RestoreLastReason:    s.RestoreLastReason,
		Stale:                s.Stale,
	}
	if r.Fault == "" {
		r.Fault = "none"
	}
	if r.LTCClaimState == "" {
		r.LTCClaimState = "none"
	}
	if r.FadeState == "" {
		r.FadeState = "none"
	}
	if s.PositionKnown {
		observedAt := s.ObservedAt
		r.ObservedAt = &observedAt
	}
	if s.GapKnown {
		gapObservedAt := s.GapObservedAt
		r.ItemGapObservedAt = &gapObservedAt
	}
	if !s.CollectedAt.IsZero() {
		collectedAt := s.CollectedAt
		r.CollectedAt = &collectedAt
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
//
// probedAt is stamped onto DiscoveredAt, the evidence time for every field
// this function derives. ObservedAt is also seeded from probedAt so a
// payload built here — including by a caller that never runs it through
// runAudioReport's per-tick loop, such as this file's own tests — is
// self-consistently Validate-passing on its own; runAudioReport overwrites
// ObservedAt with the tick's own time on every publish (see that
// function's doc comment).
func buildAudioPayload(d audio.Discovery, probedAt time.Time) mqttproto.AudioPayload {
	p := mqttproto.AudioPayload{
		EngineAvailable:          d.EngineUsable,
		EngineReason:             d.EngineReason,
		HardwareEnumerated:       d.HardwareEnumerated,
		HardwareEnumeratedReason: d.HardwareEnumeratedReason,
		Routes:                   make([]mqttproto.AudioRouteReport, 0, len(d.Routes)),
		Truncated:                d.Truncated,
		EnumeratedCount:          int64(d.EnumeratedCount),
		DiscoveredAt:             &probedAt,
		ObservedAt:               &probedAt,
		Sessions:                 []mqttproto.AudioSessionReport{},
		// Discovery-time default, self-consistent on its own — a caller
		// that never wires an [ltcObserver] (or calls this function
		// directly, as this file's own tests do) still gets a payload
		// that passes [mqttproto.AudioPayload.Validate]. Every per-tick
		// publish overwrites this via [applyLTCObservation].
		LTCGeneratorState:  string(audio.LTCUnsupported),
		LTCGeneratorReason: noLTCObserverReason,
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
