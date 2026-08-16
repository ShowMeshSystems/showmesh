package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// assetInventoryPublishTimeout bounds a single inventory publish attempt so
// a hung publish cannot delay the next tick, matching heartbeat.go's
// heartbeatPublishTimeout.
const assetInventoryPublishTimeout = 5 * time.Second

// runAssetInventory publishes this node's asset inventory to nodeID's
// observed/assets topic on every tick received from ticks, and immediately
// (out of cadence) on every signal received from triggered — see
// command.go's CommandHandler.assetFetchTrigger, signalled after a
// completed "asset.fetch" so the coordinator's confirmation can rest on a
// report whose ReportedAt post-dates that fetch's own dispatch, per this
// seam's own evidence-post-dates-the-action rule. dir is the directory
// enumerated on every publish (config.Config.AssetDir); a hash cache local
// to this call persists across every tick and trigger so an unchanged file
// is not re-hashed each time (see assets.go's enumerateAssets).
//
// runAssetInventory returns only when ctx is done; a publish failure never
// causes it to return early, matching runHeartbeat's identical contract.
func runAssetInventory(ctx context.Context, pub Publisher, nodeID, dir string, now func() time.Time, ticks <-chan time.Time, triggered <-chan struct{}, logger *slog.Logger) {
	topic, err := mqttproto.ObservedTopic(nodeID, "assets")
	if err != nil {
		// nodeID is validated at config load, matching runHeartbeat's
		// identical topic-build guard; should be unreachable in production.
		logger.Error("bug: could not build asset inventory topic for a validated node ID", "node_id", nodeID, "error", err)
		return
	}

	cache := make(map[string]hashCacheEntry)

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
			publishOneAssetInventory(ctx, pub, topic, nodeID, dir, cache, now, logger)
		case _, ok := <-triggered:
			if !ok {
				// Only expected from tests exercising the tick-only path;
				// treat it as "no more triggers" rather than spinning on a
				// closed channel forever, matching runHeartbeat's identical
				// handling of its own connected parameter.
				triggered = nil
				continue
			}
			publishOneAssetInventory(ctx, pub, topic, nodeID, dir, cache, now, logger)
		}
	}
}

// publishOneAssetInventory enumerates dir and publishes a single inventory
// report.
func publishOneAssetInventory(ctx context.Context, pub Publisher, topic, nodeID, dir string, cache map[string]hashCacheEntry, now func() time.Time, logger *slog.Logger) {
	pubCtx, cancel := context.WithTimeout(ctx, assetInventoryPublishTimeout)
	defer cancel()

	held, complete, reason := enumerateAssets(dir, cache, now)

	// Assets is built as a non-nil, possibly-empty slice regardless of
	// held's own length: an empty asset directory that WAS fully enumerated
	// is "complete: true, assets: []", never "assets: null", per
	// AssetInventoryPayload.Assets's own no-omitempty rule.
	entries := make([]mqttproto.AssetInventoryEntry, 0, len(held))
	for _, a := range held {
		entries = append(entries, mqttproto.AssetInventoryEntry{
			ContentHash: a.ContentHash,
			Filename:    a.Filename,
			SizeBytes:   a.SizeBytes,
			VerifiedAt:  a.VerifiedAt,
		})
	}

	env, err := mqttproto.NewAssetInventoryEnvelope(now, nodeID, mqttproto.AssetInventoryPayload{
		Complete: complete,
		Reason:   reason,
		Assets:   entries,
	})
	if err != nil {
		logger.Error("failed to build asset inventory envelope", "error", err)
		return
	}
	payload, err := json.Marshal(env)
	if err != nil {
		logger.Error("failed to marshal asset inventory envelope", "error", err)
		return
	}

	if err := pub.Publish(pubCtx, topic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, payload); err != nil {
		logger.Warn("asset inventory publish failed; will retry next tick", "error", err)
		return
	}

	logger.Debug("published asset inventory", "asset_count", len(entries), "complete", complete, "reason", reason)
}
