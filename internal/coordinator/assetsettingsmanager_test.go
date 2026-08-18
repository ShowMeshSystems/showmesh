package coordinator

// Track G seam G-4's own teardown-regression proof: while a deferred boot
// migration leaves the environment authoritative the store holds no
// assets.settings object, and the reconcile tick must keep the env-derived
// settings rather than manufacturing [config.DefaultAssetSettings] from
// the not-found read — mirroring resolumemanager_test.go and
// fppmqttmanager_test.go's identical deferred-migration tests.

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// TestAssetSettingsDeferredMigrationSurvivesReconcileTick reproduces the
// deferred-migration teardown for assets.settings: a seeded source over an
// empty store must answer the env-derived settings, and applying a tick to
// the sync service must leave those settings in effect, never reset them
// to the defaults.
func TestAssetSettingsDeferredMigrationSurvivesReconcileTick(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	env := config.AssetSettings{
		ContentBaseURL:    "http://coordinator.example:8320",
		MaxUploadBytes:    123456,
		SyncInterval:      42 * time.Second,
		InventoryInterval: 77 * time.Second,
	}
	src := newAssetSettingsSource(st, testLogger(), env)
	svc := assetsync.NewService(st, nil, testLogger(), toAssetSyncSettings(env))

	// The periodic tick's own body (runAssetSettingsReconciler): the store
	// has no assets.settings object (the deferred state), and the tick
	// must not revert the env-built settings to the defaults.
	svc.SetSettings(toAssetSyncSettings(src.Current(ctx)))
	if got := svc.Settings(); got != toAssetSyncSettings(env) {
		t.Fatalf("Settings() after a tick during a deferred migration = %+v, want the env-derived settings %+v", got, toAssetSyncSettings(env))
	}

	// A transient store read error must keep the current settings too: a
	// closed store fails every read with a non-not-found error.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	svc.SetSettings(toAssetSyncSettings(src.Current(ctx)))
	if got := svc.Settings(); got != toAssetSyncSettings(env) {
		t.Fatalf("Settings() after a tick during a transient store read error = %+v, want the env-derived settings %+v", got, toAssetSyncSettings(env))
	}
}
