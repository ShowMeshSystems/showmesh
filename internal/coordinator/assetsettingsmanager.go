package coordinator

// This file is Track G seam G-4's no-restart apply (ADR-039 decision 6,
// applying ADR-036): assetsettingsSource reads the active assets.settings
// configuration live, and runAssetSettingsReconciler keeps the already-
// running *assetsync.Service's own settings matching it, with no restart in
// either direction. Unlike resolumemanager.go this never starts or stops a
// collector — the asset sync service is constructed once, at coordinator
// startup, and stays running for this process's whole life (it already
// tolerates being "enabled" with zero work to do — see assetsync.Service.Run's
// own doc comment); reconciling here only ever updates its live settings,
// which is a smaller problem than resolumeManager's build/tear-down one.

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// assetSettingsReconcileInterval mirrors resolumeInstanceReconcileInterval's
// identical reasoning: well inside a show-prep operator's own patience, so
// a newly configured (or changed) setting is in effect promptly.
const assetSettingsReconcileInterval = 10 * time.Second

// assetSettingsSource is [resolumeInstanceSource]'s mirror for the
// assets.settings kind: resolves the currently active configuration on
// demand, caching the decoded value against the revision number it came
// from. See that type's own doc comment for the "stale-but-real beats
// manufactured empty" reasoning, which applies unchanged here — the
// manufactured-empty equivalent for this kind would be silently reverting
// to [config.DefaultAssetSettings], which is exactly as wrong as
// resolumeInstanceSource falling back to zero instances on a transient
// store error.
type assetSettingsSource struct {
	st     *store.Store
	logger *slog.Logger

	mu       sync.Mutex
	revision int64
	cached   config.AssetSettings
}

// newAssetSettingsSource seeds the source with the boot-resolved
// AUTHORITATIVE settings — see newResolumeInstanceSource for why: while a
// deferred boot migration leaves the environment authoritative the store
// holds no assets.settings object, and an unseeded source would answer
// [config.DefaultAssetSettings] and silently revert the env-configured
// values on the first reconcile tick.
func newAssetSettingsSource(st *store.Store, logger *slog.Logger, initial config.AssetSettings) *assetSettingsSource {
	return &assetSettingsSource{st: st, logger: logger, cached: initial}
}

// Current returns the active assets.settings. A missing config object is a
// steady state and answers the seed; any other store error keeps the last
// known settings, and logs.
func (s *assetSettingsSource) Current(ctx context.Context) config.AssetSettings {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, err := s.st.GetConfigObject(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return s.cached
	}
	if err != nil {
		s.logWarn("failed to read the active assets.settings configuration; continuing with the last known settings", err)
		return s.cached
	}
	if obj.CurrentRevision == s.revision {
		return s.cached
	}

	rev, err := s.st.GetConfigRevision(ctx, config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		s.logWarn("failed to read the active assets.settings revision; continuing with the last known settings", err)
		return s.cached
	}
	settings, err := config.DecodeAssetSettingsPayload(rev.PayloadJSON)
	if err != nil {
		s.logWarn("failed to decode the active assets.settings revision; continuing with the last known settings", err)
		return s.cached
	}

	s.revision = obj.CurrentRevision
	s.cached = settings
	return settings
}

func (s *assetSettingsSource) logWarn(msg string, err error) {
	if s.logger != nil {
		s.logger.Warn(msg, "error", err)
	}
}

// runAssetSettingsReconciler keeps svc's live settings matching
// source.Current, until ctx is cancelled. The FIRST reconcile is
// deliberately NOT performed here — coordinator.go's Run applies the
// startup-resolved settings directly via assetsync.NewService's own initial
// value, before this goroutine (and the HTTP server) ever starts, mirroring
// resolumeManager.Run's identical "no request may observe a pre-reconcile
// state" property.
func runAssetSettingsReconciler(ctx context.Context, source *assetSettingsSource, svc *assetsync.Service) {
	ticker := time.NewTicker(assetSettingsReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			svc.SetSettings(toAssetSyncSettings(source.Current(ctx)))
		}
	}
}

// toAssetSyncSettings adapts config.AssetSettings to assetsync.Settings —
// two structurally identical types kept separate so package assetsync does
// not need to import internal/coordinator/config for one struct shape (the
// same "declare narrowly at the consumer" posture this codebase already
// applies elsewhere).
func toAssetSyncSettings(s config.AssetSettings) assetsync.Settings {
	return assetsync.Settings{
		ContentBaseURL:    s.ContentBaseURL,
		MaxUploadBytes:    s.MaxUploadBytes,
		SyncInterval:      s.SyncInterval,
		InventoryInterval: s.InventoryInterval,
	}
}
