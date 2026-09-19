package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is ADR-053 decision 2's own "a republish loop like showmode.go's
// keeps the retained message fresh" requirement: start/resume
// (internal/coordinator/api's weatherdelay.go) publish the retained state
// on every change, and this loop republishes the CURRENT stored state on a
// short interval so a node that connects, or misses one delivery, still
// converges without an operator having to act again. It never gates, never
// dispatches a stop, and never mutates the stored state — read-and-
// republish only, mirroring runShowMode's identical shape and its
// identical "best effort, log and retry next tick" posture.

// weatherDelayReconcileInterval mirrors showModeReconcileInterval's own
// choice: short enough that a node's own retained-message freshness
// window (were one ever added) would treat a few missed ticks, not one, as
// the coordinator being gone.
const weatherDelayReconcileInterval = 5 * time.Second

// weatherDelayPublisher is this file's MQTT publish dependency, matching
// showModePublisher's identical shape one file over.
type weatherDelayPublisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// weatherDelayAssetPusher pushes one alert asset to one node ahead of
// time. *assetsync.Service already satisfies this with no adapter.
type weatherDelayAssetPusher interface {
	EnsureAssetOnNode(ctx context.Context, assetID, nodeID string) error
}

// runWeatherDelay republishes the current stored weather delay state on
// every tick, plus once immediately (matching runShowMode's own "the
// first pass runs immediately" reasoning). On every tick it also nudges
// the configured alert assets toward every plan node through the
// existing asset sync (build task item 4), independent of whether a
// delay is active: the alert must be ready on a calm day, not only after
// the operator has already pressed start.
func runWeatherDelay(ctx context.Context, st *store.Store, pub weatherDelayPublisher, assets weatherDelayAssetPusher, now func() time.Time, logger *slog.Logger, interval time.Duration) {
	if pub == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	pass := func() {
		rec, err := st.GetWeatherDelayState(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("weather delay: failed to read the stored state; nothing republished this tick", "error", err)
			}
			return
		}
		payload, payloadErr := weatherDelayConfigPayload(ctx, st)
		if payloadErr != nil && logger != nil {
			logger.Warn("weather delay: failed to resolve show.weatherdelay config; republishing with an empty plan", "error", payloadErr)
		}
		if assets != nil && payloadErr == nil {
			pushWeatherDelayAssets(ctx, st, assets, payload, logger)
		}
		plan, err := weatherDelayPlanFromPayload(ctx, st, payload)
		if err != nil && logger != nil {
			logger.Warn("weather delay: failed to build the alert plan; republishing with an empty plan", "error", err)
		}
		kind, startedAt, startedBy := "", time.Time{}, ""
		if rec.Active {
			kind, startedAt, startedBy = rec.Kind, rec.StartedAt, rec.StartedBy
		}
		msg, err := mqttproto.NewWeatherDelayMessage(rec.Active, kind, startedAt, startedBy, rec.Revision, plan, now())
		if err != nil {
			if logger != nil {
				logger.Error("weather delay: refusing to republish an invalid state message", "error", err)
			}
			return
		}
		encoded, err := mqttproto.EncodeWeatherDelayMessage(msg)
		if err != nil {
			if logger != nil {
				logger.Error("weather delay: failed to encode the state message", "error", err)
			}
			return
		}
		if err := pub.Publish(ctx, mqttproto.WeatherDelayTopic(), mqttproto.WeatherDelayDeliveryPolicy.QoS,
			mqttproto.WeatherDelayDeliveryPolicy.Retain, encoded); err != nil {
			if logger != nil {
				logger.Warn("weather delay: failed to republish the retained state; nodes keep the state they already hold",
					"error", err)
			}
		}
	}

	pass()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass()
		}
	}
}

// weatherDelayConfigPayload resolves the show.weatherdelay config,
// defaulting when nothing has ever been written.
func weatherDelayConfigPayload(ctx context.Context, st *store.Store) (config.WeatherDelayPayload, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID)
	switch {
	case err != nil && !isConfigObjectNotFound(err):
		return config.WeatherDelayPayload{}, err
	case err != nil || obj.CurrentRevision == 0:
		return config.WeatherDelayDefaultPayload, nil
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowWeatherDelayConfigKind, config.ShowWeatherDelayConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.WeatherDelayPayload{}, err
	}
	payload, verr := config.DecodeWeatherDelayPayload(rev.PayloadJSON)
	if verr != nil {
		return config.WeatherDelayPayload{}, verr
	}
	return payload, nil
}

// weatherDelayPlanFromPayload resolves show.weatherdelay's alert config
// into the wire plan the republish loop carries, tolerating an asset id
// that no longer resolves by omitting that half of the plan rather than
// refusing the whole republish.
func weatherDelayPlanFromPayload(ctx context.Context, st *store.Store, payload config.WeatherDelayPayload) (mqttproto.WeatherDelayPlan, error) {
	plan := mqttproto.WeatherDelayPlan{RepeatCount: payload.Alert.RepeatCount, NodeIDs: payload.Alert.NodeIDs}
	if payload.Alert.DelayAssetID != "" {
		if rec, err := st.GetAsset(ctx, payload.Alert.DelayAssetID); err == nil {
			plan.Delay = &mqttproto.WeatherDelayAlertAssetRef{AssetID: rec.ID, ContentHash: rec.ContentHash, Filename: rec.RuntimeFilename}
		}
	}
	if payload.Alert.CancelNightAssetID != "" {
		if rec, err := st.GetAsset(ctx, payload.Alert.CancelNightAssetID); err == nil {
			plan.CancelNight = &mqttproto.WeatherDelayAlertAssetRef{AssetID: rec.ID, ContentHash: rec.ContentHash, Filename: rec.RuntimeFilename}
		}
	}
	return plan, nil
}

// pushWeatherDelayAssets nudges every configured alert asset toward every
// plan node (configured explicitly, or every declared audio.node when
// empty), best effort: a failure to list nodes or push one asset is
// logged and never aborts the republish this tick still performs.
func pushWeatherDelayAssets(ctx context.Context, st *store.Store, assets weatherDelayAssetPusher, payload config.WeatherDelayPayload, logger *slog.Logger) {
	if payload.Alert.DelayAssetID == "" && payload.Alert.CancelNightAssetID == "" {
		return
	}
	nodeIDs := payload.Alert.NodeIDs
	if len(nodeIDs) == 0 {
		objs, err := st.ListConfigObjects(ctx, config.AudioNodeConfigKind)
		if err != nil {
			if logger != nil {
				logger.Warn("weather delay: failed to list declared audio.node ids for asset delivery", "error", err)
			}
			return
		}
		for _, obj := range objs {
			if obj.CurrentRevision > 0 {
				nodeIDs = append(nodeIDs, obj.ID)
			}
		}
	}
	for _, nodeID := range nodeIDs {
		if payload.Alert.DelayAssetID != "" {
			if err := assets.EnsureAssetOnNode(ctx, payload.Alert.DelayAssetID, nodeID); err != nil && logger != nil {
				logger.Warn("weather delay: failed to ensure the delay alert asset on a plan node", "nodeId", nodeID, "error", err)
			}
		}
		if payload.Alert.CancelNightAssetID != "" {
			if err := assets.EnsureAssetOnNode(ctx, payload.Alert.CancelNightAssetID, nodeID); err != nil && logger != nil {
				logger.Warn("weather delay: failed to ensure the cancel-night alert asset on a plan node", "nodeId", nodeID, "error", err)
			}
		}
	}
}

func isConfigObjectNotFound(err error) bool {
	return errors.Is(err, store.ErrConfigObjectNotFound)
}
