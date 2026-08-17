package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
)

// This file is Track G seam G-4's config kind (ADR-039): the
// config_objects.kind / config_revisions.payload_json shape for
// SHOWMESH_ASSET_CONTENT_BASE_URL/SHOWMESH_ASSET_MAX_UPLOAD_BYTES/
// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL promoted to
// store-backed configuration. Mirrors fppendpoints.go's singleton shape and
// resolumerecovery.go's scalar-payload shape, narrowed to four named
// fields rather than a list or a single bool. SHOWMESH_ASSET_DIR is
// deliberately NOT part of this kind — see ADR-039 decision 2, the worked
// boundary example — and stays environment-only.

const (
	// AssetSettingsConfigKind is config_objects.kind and
	// config_revisions.kind for the asset store's operator-facing
	// settings — the wire and storage identifier for GET/PUT
	// /api/v1/config/assets.settings.
	AssetSettingsConfigKind = "assets.settings"

	// AssetSettingsConfigObjectID is the single config_objects.id this
	// kind ever uses — one settings object per coordinator.
	AssetSettingsConfigObjectID = "default"

	// AssetSettingsSourceAPI and AssetSettingsSourceEnvMigration are the
	// two values config_revisions.source takes for this kind: a write
	// through PUT /api/v1/config/assets.settings, or the one-time startup
	// migration out of the four SHOWMESH_ASSET_* variables
	// (internal/coordinator's assetsettingssync.go).
	AssetSettingsSourceAPI          = "api"
	AssetSettingsSourceEnvMigration = "env_migration"
)

// AssetSettings is config_revisions.payload_json's decoded shape for
// [AssetSettingsConfigKind]: the four operator-facing asset store settings
// ADR-039 decision 2 names as belonging in the store, grouped as one
// object because they were migrated as one group and are written as one
// group. SHOWMESH_ASSET_DIR has no field here — it stays an environment
// variable (a filesystem path that must resolve before the store opens).
type AssetSettings struct {
	// ContentBaseURL is SHOWMESH_ASSET_CONTENT_BASE_URL. Empty is a real,
	// deliberate state: the asset sync service does not run, and nothing
	// ever reaches a node over the network — see assetsync.Service.Enabled.
	ContentBaseURL string
	// MaxUploadBytes bounds a single asset upload. Always positive.
	MaxUploadBytes int64
	// SyncInterval is how often the asset sync service recomputes every
	// declared node's gap, in addition to running on every upload. Always
	// positive.
	SyncInterval time.Duration
	// InventoryInterval is this coordinator's own copy of the agent's
	// inventory-report cadence, used to derive the staleness window a
	// node's report must fall within to be treated as fresh. Always
	// positive.
	InventoryInterval time.Duration
}

// DefaultAssetSettings is what this coordinator uses before any revision
// of this kind has ever been written — the identical values
// LoadConfigFrom's own defaults produce for an unset environment, so a
// freshly migrated coordinator and a coordinator whose operator never
// touched this surface behave identically.
func DefaultAssetSettings() AssetSettings {
	return AssetSettings{
		ContentBaseURL:    "",
		MaxUploadBytes:    assetstore.DefaultMaxUploadBytes,
		SyncInterval:      defaultAssetSyncInterval,
		InventoryInterval: defaultAssetInventoryInterval,
	}
}

// assetSettingsPayload is [AssetSettings]' wire shape. Durations are
// carried as seconds, named "...Seconds" — the wire convention this
// contract already uses elsewhere (e.g. v1.ObservationEvidence.
// ValidForSeconds) because RFC 9457/ADR-020 want wire durations as plain
// numbers, not a Go duration string. FLOAT64, not int64: an integer-second
// encoding silently truncates a sub-second interval to zero (measured —
// this project's own integration harness legitimately configures
// SHOWMESH_ASSET_SYNC_INTERVAL/SHOWMESH_ASSET_INVENTORY_INTERVAL in the
// hundreds of milliseconds to keep the suite fast, and an early version of
// this function turned that into a stored 0s that then refused every
// later boot as a disagreement against the env value), mirroring
// [Dependencies.ResolumeRecoverySettleSeconds]'s identical float64 choice
// for the identical reason one seam over.
type assetSettingsPayload struct {
	ContentBaseURL           string  `json:"contentBaseUrl"`
	MaxUploadBytes           int64   `json:"maxUploadBytes"`
	SyncIntervalSeconds      float64 `json:"syncIntervalSeconds"`
	InventoryIntervalSeconds float64 `json:"inventoryIntervalSeconds"`
}

// EncodeAssetSettingsPayload marshals s into config_revisions'
// payload_json column shape.
func EncodeAssetSettingsPayload(s AssetSettings) (string, error) {
	b, err := json.Marshal(assetSettingsPayload{
		ContentBaseURL:           s.ContentBaseURL,
		MaxUploadBytes:           s.MaxUploadBytes,
		SyncIntervalSeconds:      s.SyncInterval.Seconds(),
		InventoryIntervalSeconds: s.InventoryInterval.Seconds(),
	})
	if err != nil {
		return "", fmt.Errorf("config: encode assets.settings payload: %w", err)
	}
	return string(b), nil
}

// DecodeAssetSettingsPayload is [EncodeAssetSettingsPayload]'s inverse. A
// decode failure means a config_revisions row this package never wrote in
// this shape — every writer of this kind goes through
// EncodeAssetSettingsPayload — so callers treat it as a store-integrity
// error, not a validation outcome to recover from.
func DecodeAssetSettingsPayload(raw string) (AssetSettings, error) {
	var payload assetSettingsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return AssetSettings{}, fmt.Errorf("config: decode assets.settings payload: %w", err)
	}
	return AssetSettings{
		ContentBaseURL:    payload.ContentBaseURL,
		MaxUploadBytes:    payload.MaxUploadBytes,
		SyncInterval:      time.Duration(payload.SyncIntervalSeconds * float64(time.Second)),
		InventoryInterval: time.Duration(payload.InventoryIntervalSeconds * float64(time.Second)),
	}, nil
}

// AssetSettingsEqual reports whether a and b name the identical settings —
// used by the env->store migration's disagreement check
// (assetsettingssync.go), mirroring [ResolumeInstancesEqual]'s identical
// role for its own kind.
func AssetSettingsEqual(a, b AssetSettings) bool {
	return a == b
}

// ValidateAssetSettings validates one assets.settings config payload: a
// positive upload limit, a positive sync interval, a positive inventory
// interval, and — only when non-empty, since an empty ContentBaseURL is
// itself a valid, deliberate "sync disabled" state — an http/https URL
// with a non-empty host and no userinfo. The URL check is duplicated from
// [validateAssetConfig] rather than shared, mirroring
// [ValidateResolumeInstances]'s identical choice for its own per-field
// checks: the two validate the same shape against unrelated structs (the
// process environment's parsed Config versus this API payload), and a
// future change to one must not silently reach through to the other.
func ValidateAssetSettings(s AssetSettings) error {
	if s.MaxUploadBytes <= 0 {
		return fmt.Errorf("assets.settings: maxUploadBytes must be positive, got %d", s.MaxUploadBytes)
	}
	if s.SyncInterval <= 0 {
		return fmt.Errorf("assets.settings: syncIntervalSeconds must be positive, got %s", s.SyncInterval)
	}
	if s.InventoryInterval <= 0 {
		return fmt.Errorf("assets.settings: inventoryIntervalSeconds must be positive, got %s", s.InventoryInterval)
	}
	if s.ContentBaseURL == "" {
		return nil
	}
	u, err := url.Parse(s.ContentBaseURL)
	if err != nil {
		return fmt.Errorf("assets.settings: contentBaseUrl %q is not a valid URL: %w", s.ContentBaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("assets.settings: contentBaseUrl %q must use http or https", s.ContentBaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("assets.settings: contentBaseUrl %q must include a host", s.ContentBaseURL)
	}
	if u.User != nil {
		return fmt.Errorf("assets.settings: contentBaseUrl must not include userinfo/credentials")
	}
	return nil
}
