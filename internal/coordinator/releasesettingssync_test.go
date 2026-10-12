package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

var envReleaseSettings = config.ReleaseSettings{Enabled: false, CheckInterval: 10 * time.Minute}

func TestResolveReleaseSettingsUsesTheDefaultWhenNothingIsSetOrStored(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	got, deferred := resolveReleaseSettings(context.Background(), st, svc, false, config.ReleaseSettings{}, time.Now, discardLogger())
	if got != config.DefaultReleaseSettings() || deferred {
		t.Fatalf("got %+v, deferred %v, want the default and not deferred", got, deferred)
	}
	if _, err := st.GetConfigObject(context.Background(), config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID); !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject error = %v, want nothing stored", err)
	}
}

func TestResolveReleaseSettingsMigratesTheVariablesOnce(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)
	ctx := context.Background()

	got, deferred := resolveReleaseSettings(ctx, st, svc, true, envReleaseSettings, time.Now, discardLogger())
	if got != envReleaseSettings || deferred {
		t.Fatalf("got %+v, deferred %v, want the variables' settings and not deferred", got, deferred)
	}
	rev, err := st.GetConfigRevision(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID, 1)
	if err != nil {
		t.Fatalf("GetConfigRevision: %v", err)
	}
	if rev.Source != config.ReleaseSettingsSourceEnvMigration || rev.CreatedByPrincipalID != "" {
		t.Fatalf("revision = %+v, want source env_migration and no principal", rev)
	}

	// A later start with different variables keeps the stored settings and still starts.
	changed := config.ReleaseSettings{Enabled: true, CheckInterval: time.Hour}
	got, deferred = resolveReleaseSettings(ctx, st, svc, true, changed, time.Now, discardLogger())
	if got != envReleaseSettings || deferred {
		t.Fatalf("second start: got %+v, deferred %v, want the stored settings", got, deferred)
	}
	if obj, err := st.GetConfigObject(ctx, config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID); err != nil || obj.CurrentRevision != 1 {
		t.Fatalf("object = %+v, %v, want revision 1 untouched", obj, err)
	}
}

func TestResolveReleaseSettingsStartsWhenTheMigrationCannotBeStored(t *testing.T) {
	st, svc, dir := newTestAssetSettingsSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")

	got, deferred := resolveReleaseSettings(context.Background(), st, svc, true, envReleaseSettings, time.Now, discardLogger())
	if got != envReleaseSettings || !deferred {
		t.Fatalf("got %+v, deferred %v, want the variables' settings and deferred", got, deferred)
	}
	if _, err := st.GetConfigObject(context.Background(), config.ReleaseSettingsConfigKind, config.ReleaseSettingsConfigObjectID); !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject error = %v, want nothing stored", err)
	}
}
