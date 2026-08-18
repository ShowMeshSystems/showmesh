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

// runAudioReport publishes this node's audio discovery report to nodeID's
// observed/audio topic on every tick received from ticks. Discovery itself
// runs exactly once, before the loop starts: the throwaway probe pipelines
// this involves must never repeat on a fixed cadence for the life of the
// process (finding 1), so every tick republishes the SAME cached evidence,
// with its own original observation time, never a fresh probe. A live
// device state change is picked up only at the next agent restart, or by
// the operator's own explicit "audio.device.probe" command (audioops.go),
// which probes one named device and is unrelated to this cache.
//
// runAudioReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runRenderReport's identical
// contract.
func runAudioReport(ctx context.Context, pub Publisher, nodeID string, now func() time.Time, ticks <-chan time.Time, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "audio")
	if err != nil {
		// nodeID is validated at config load, matching runRenderReport's
		// identical topic-build guard; should be unreachable in production.
		logger.Error("bug: could not build audio report topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	d := audioDiscoverer(ctx, audioEnumerator)
	payload := buildAudioPayload(d, now())

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			publishAudioPayload(ctx, pub, topic, nodeID, payload, now, logger)
		}
	}
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
