package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

const (
	fallbackReportInterval       = time.Minute
	fallbackReportMinGap         = time.Second
	fallbackReportPublishTimeout = 5 * time.Second
)

// report is what this node currently holds and has answered.
func (g *fallbackIngress) report() mqttproto.FallbackPayload {
	observedAt := g.now()
	decisions, accepted, refused := g.decisions.snapshot()
	if decisions == nil {
		decisions = []mqttproto.FallbackDecision{}
	}
	return mqttproto.FallbackPayload{
		ObservedAt: &observedAt, CoordinatorKeyLoaded: g.coordinatorKey != nil,
		ExecutionRecordProblem: g.executionRecordProblem(),
		Programs:               g.programs.snapshot(), Accepted: accepted, Refused: refused, Decisions: decisions,
	}
}

// runFallbackReport publishes this node's fallback report, retained, on
// every tick and soon after every answer. At most one publish per
// fallbackReportMinGap, so a burst of requests cannot flood the broker.
func runFallbackReport(ctx context.Context, pub Publisher, nodeID string, g *fallbackIngress, ticks <-chan time.Time, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, mqttproto.ObservedSubpathFallback)
	if err != nil {
		logger.Error("bug: could not build fallback report topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}
	publishFallbackReport(ctx, pub, topic, nodeID, g, logger)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
		case <-g.decisions.changed:
			select {
			case <-ctx.Done():
				return
			case <-time.After(fallbackReportMinGap):
			}
		}
		publishFallbackReport(ctx, pub, topic, nodeID, g, logger)
	}
}

func publishFallbackReport(ctx context.Context, pub Publisher, topic, nodeID string, g *fallbackIngress, logger *slog.Logger) {
	env, err := mqttproto.NewFallbackEnvelope(g.now, nodeID, g.report())
	if err != nil {
		logger.Error("failed to build fallback report envelope", "error", err)
		return
	}
	data, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal fallback report envelope", "error", err)
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, fallbackReportPublishTimeout)
	defer cancel()
	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, data); err != nil {
		logger.Warn("fallback report publish failed; will retry next tick", "error", err)
	}
}
