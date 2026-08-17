package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track G seam G-4's own startup-sequencing suite (ADR-039),
// mirroring resolumeinstancessync_test.go's identical shape for the
// assets.settings kind — the same cases matter here for the identical
// reasons: nothing configured, migration from env, the disagreement
// refusal, and an audit-write failure must NEVER refuse to start.

func newTestAssetSettingsSyncDeps(t *testing.T) (*store.Store, identity.Service) {
	t.Helper()
	st := openTestStore(t)
	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger()))
	return st, svc
}

var testAssetSettings = config.AssetSettings{
	ContentBaseURL: "https://coordinator.example", MaxUploadBytes: 1 << 20,
	SyncInterval: time.Minute, InventoryInterval: 2 * time.Minute,
}

func TestSyncAssetSettingsConfigNothingConfiguredUsesDefaults(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	got, deferred, err := syncAssetSettingsConfig(context.Background(), st, svc, false, config.AssetSettings{}, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncAssetSettingsConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if got != config.DefaultAssetSettings() {
		t.Errorf("got = %+v, want %+v", got, config.DefaultAssetSettings())
	}
}

// TestSyncAssetSettingsConfigMigratesFromEnv proves the env->store
// migration itself: the settings survive, land as revision 1 with source
// env_migration, and carry NO principal (a startup migration has no
// principal to attribute it to).
func TestSyncAssetSettingsConfigMigratesFromEnv(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	got, deferred, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncAssetSettingsConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if got != testAssetSettings {
		t.Errorf("got = %+v, want %+v", got, testAssetSettings)
	}

	obj, err := st.GetConfigObject(context.Background(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	rev, err := st.GetConfigRevision(context.Background(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		t.Fatalf("GetConfigRevision: %v", err)
	}
	if rev.Source != config.AssetSettingsSourceEnvMigration {
		t.Errorf("Source = %q, want %q", rev.Source, config.AssetSettingsSourceEnvMigration)
	}
	if rev.CreatedByPrincipalID != "" {
		t.Errorf("CreatedByPrincipalID = %q, want empty (a startup migration has no principal)", rev.CreatedByPrincipalID)
	}
}

// TestSyncAssetSettingsConfigDisagreementRefusesToStart is the owner's
// disagreement rule (mirroring FPP's and Resolume's identical rule): once
// the store holds an active assets.settings configuration, an environment
// value that disagrees with it refuses to start rather than silently
// overriding the store.
func TestSyncAssetSettingsConfigDisagreementRefusesToStart(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	if _, _, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	differing := testAssetSettings
	differing.SyncInterval = 10 * time.Minute
	_, _, err := syncAssetSettingsConfig(context.Background(), st, svc, true, differing, time.Now, discardLogger())
	if err == nil {
		t.Fatalf("syncAssetSettingsConfig() error = nil, want a refusal naming the difference")
	}
	if !errors.Is(err, errAssetSettingsDisagree) {
		t.Errorf("error = %v, want it to wrap errAssetSettingsDisagree", err)
	}
	if !strings.Contains(err.Error(), "syncInterval") {
		t.Errorf("error = %q, want it to name the differing field", err.Error())
	}

	revs, err := st.ListConfigRevisions(context.Background(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (a refused start must not append a revision)", len(revs))
	}
}

// TestSyncAssetSettingsConfigIdenticalStartsWithWarning proves the
// still-set-but-agreeing case starts cleanly and warns that the variables
// may now be removed.
func TestSyncAssetSettingsConfigIdenticalStartsWithWarning(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	if _, _, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	logger, buf := capturingLogger()
	got, deferred, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, logger)
	if err != nil {
		t.Fatalf("syncAssetSettingsConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if got != testAssetSettings {
		t.Errorf("got = %+v, want %+v", got, testAssetSettings)
	}
	if !strings.Contains(buf.String(), "may be removed") {
		t.Errorf("log output = %q, want a WARN naming that the variables may now be removed", buf.String())
	}
}

// TestSyncAssetSettingsConfigEnvUnsetAfterMigrationUsesStore covers the
// state the migration warning steers an operator toward: the variables
// removed entirely after a prior migration. The store stays authoritative.
func TestSyncAssetSettingsConfigEnvUnsetAfterMigrationUsesStore(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	if _, _, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncAssetSettingsConfig() error = %v", err)
	}

	got, _, err := syncAssetSettingsConfig(context.Background(), st, svc, false, config.AssetSettings{}, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncAssetSettingsConfig() with env unset error = %v, want nil", err)
	}
	if got != testAssetSettings {
		t.Errorf("got = %+v, want the store's %+v", got, testAssetSettings)
	}
}

// TestResolveAuthoritativeAssetSettingsUsesStoreOverEnv exercises the
// actual Run()-facing entry point, not just syncAssetSettingsConfig's own
// return value.
func TestResolveAuthoritativeAssetSettingsUsesStoreOverEnv(t *testing.T) {
	st, svc := newTestAssetSettingsSyncDeps(t)

	if _, _, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	resolved, deferred, err := resolveAuthoritativeAssetSettings(context.Background(), st, svc, false, config.AssetSettings{}, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("resolveAuthoritativeAssetSettings() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if resolved != testAssetSettings {
		t.Errorf("resolved = %+v, want %+v (the store-authoritative settings, not the default)", resolved, testAssetSettings)
	}
}

// newTestAssetSettingsSyncDepsWithDataDir is
// [newTestAssetSettingsSyncDeps] plus the data directory, mirroring
// resolumeinstancessync_test.go's identical helper — needed so
// [makeTableUnwritable] (defined in configsync_test.go, same package) can
// reach the real SQLite file underneath.
func newTestAssetSettingsSyncDepsWithDataDir(t *testing.T) (*store.Store, identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger())), dir
}

// TestSyncAssetSettingsConfigAuditWriteFailureStartsWithoutMigrating is
// ADR-039 decision 3's own regression test, proved from the start: an
// unwritable audit_log must NOT stop this coordinator starting. Mirrors
// TestSyncResolumeInstancesConfigAuditWriteFailureStartsWithoutMigrating's
// three load-bearing assertions together: starting proves the direction,
// returning envSettings proves the reconciler is not silently left with
// nothing to apply, and persisting NOTHING keeps ADR-024 decision 11
// satisfied rather than exempted.
func TestSyncAssetSettingsConfigAuditWriteFailureStartsWithoutMigrating(t *testing.T) {
	st, svc, dir := newTestAssetSettingsSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")
	logger, buf := capturingLogger()

	got, deferred, err := syncAssetSettingsConfig(context.Background(), st, svc, true, testAssetSettings, time.Now, logger)
	if err != nil {
		t.Fatalf("syncAssetSettingsConfig() error = %v, want nil — an audit-write failure must not stop the coordinator starting", err)
	}
	if got != testAssetSettings {
		t.Errorf("got = %+v, want %+v — this boot must still use the settings it used before", got, testAssetSettings)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true")
	}

	_, err = st.GetConfigObject(context.Background(), config.AssetSettingsConfigKind, config.AssetSettingsConfigObjectID)
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("GetConfigObject error = %v, want ErrConfigObjectNotFound — the failed migration must persist nothing", err)
	}

	out := buf.String()
	if !strings.Contains(out, "could not migrate the SHOWMESH_ASSET_*") {
		t.Errorf("log output = %q, want an ERROR naming the failed migration", out)
	}
}
