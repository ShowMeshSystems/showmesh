package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// clockReportPublishTimeout bounds a single clock report publish attempt,
// matching audioreport.go's identical audioReportPublishTimeout.
const clockReportPublishTimeout = 5 * time.Second

// clockPoller is the read side of a [clock.Manager] this package needs:
// fresh evidence on every tick, never a command path — matching
// audioreport.go's audioSessionSnapshotter/ltcObserver identical shape. A
// nil poller (no clock manager wired — should not happen in production,
// agent.go always constructs one) reports [clock.StatusUnconfigured].
type clockPoller interface {
	Poll(ctx context.Context) clock.Status
}

// runClockReport publishes this node's PTP clock status to nodeID's
// observed/clock topic on every tick received from ticks, fresh evidence
// every time (unlike audioreport.go's one-shot discovery cache, nothing
// about clock status is safe to cache: a lock lost between ticks must be
// visible on the very next report). A node with no node.clock
// configuration (mgr.Poll returning [clock.StatusUnconfigured]) still
// publishes on this cadence, reporting "unsynchronized" — exactly what an
// unconfigured node reported before this seam existed, now made explicit
// on the wire rather than simply absent.
//
// runClockReport returns only when ctx is done; a publish failure never
// causes it to return early, matching runAudioReport's identical
// contract.
func runClockReport(ctx context.Context, pub Publisher, nodeID string, mgr clockPoller, now func() time.Time, ticks <-chan time.Time, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "clock")
	if err != nil {
		// nodeID is validated at config load, matching runAudioReport's
		// identical topic-build guard; should be unreachable in production.
		logger.Error("bug: could not build clock report topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			var status clock.Status
			if mgr != nil {
				status = mgr.Poll(ctx)
			} else {
				status = clock.StatusUnconfigured(now())
			}
			publishClockStatus(ctx, pub, topic, nodeID, status, now, logger)
		}
	}
}

func publishClockStatus(ctx context.Context, pub Publisher, topic, nodeID string, status clock.Status, now func() time.Time, logger *slog.Logger) {
	payload := clockPayloadFromStatus(status)
	env, err := mqttproto.NewClockEnvelope(now, nodeID, payload)
	if err != nil {
		logger.Error("failed to build clock report envelope", "error", err)
		return
	}
	data, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal clock report envelope", "error", err)
		return
	}

	pubCtx, cancel := context.WithTimeout(ctx, clockReportPublishTimeout)
	defer cancel()

	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, data); err != nil {
		logger.Warn("clock report publish failed; will retry next tick", "error", err)
		return
	}

	logger.Debug("published clock report", "state", status.State, "provider", status.Provider, "role", status.Role)
}

// clockPayloadFromStatus converts status into its wire form. observedAt
// is stamped from status.ObservedAt (this node's own evidence time — see
// [clock.Tracker.Poll] and [clock.StatusUnconfigured]), never the
// publish-time clock read.
func clockPayloadFromStatus(status clock.Status) mqttproto.ClockPayload {
	observedAt := status.ObservedAt
	p := mqttproto.ClockPayload{
		State:               string(status.State),
		Reason:              status.Reason,
		Provider:            string(status.Provider),
		Role:                string(status.Role),
		RoleKnown:           status.RoleKnown,
		Owner:               status.Owner,
		Interface:           status.Interface,
		Domain:              int64(status.Domain),
		DomainKnown:         status.DomainKnown,
		GrandmasterIdentity: status.GrandmasterIdentity,
		GMKnown:             status.GMKnown,
		Timescale:           string(status.Timescale),
		OffsetNs:            status.OffsetNs,
		OffsetKnown:         status.OffsetKnown,
		ClockClass:          int64(status.ClockClass),
		ClockClassKnown:     status.ClockClassKnown,
		Timestamping:        string(status.Timestamping),
		TimestampingKnown:   status.TimestampingKnown,
		LockedSeconds:       status.LockedSeconds,
		LockedSecondsKnown:  status.LockedSecondsKnown,
		LastStepNs:          status.LastStepNs,
		LastStepKnown:       status.LastStepKnown,
		Mismatch:            status.Mismatch,
		MismatchReason:      status.MismatchReason,
		ObservedAt:          &observedAt,
	}
	if status.LastStepKnown {
		lastStepAt := status.LastStepAt
		p.LastStepAt = &lastStepAt
	}
	if p.Timescale == "" {
		p.Timescale = string(clock.TimescaleUnknown)
	}
	return p
}
