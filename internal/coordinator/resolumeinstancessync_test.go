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

// This file is Track G seam G-2's own startup-sequencing suite (ADR-039),
// mirroring configsync_test.go's identical shape for the resolume.instances
// kind — the same cases matter here for the identical reasons: nothing
// configured, migration from env, the disagreement refusal, and (the one
// this seam exists to get right the first time, rather than shipping the
// Step 7 defect and fixing it later) an audit-write failure must NEVER
// refuse to start.

func newTestResolumeInstancesSyncDeps(t *testing.T) (*store.Store, identity.Service) {
	t.Helper()
	st := openTestStore(t)
	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger()))
	return st, svc
}

var testResolumeInstances = []config.ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.30:8080"}}

func TestSyncResolumeInstancesConfigNothingConfiguredIsANoop(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	got, deferred, err := syncResolumeInstancesConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncResolumeInstancesConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty", got)
	}
}

// TestSyncResolumeInstancesConfigMigratesFromEnv proves the env->store
// migration itself: the instance survives, lands as revision 1 with source
// env_migration, and carries NO principal (a startup migration has no
// principal to attribute it to).
func TestSyncResolumeInstancesConfigMigratesFromEnv(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	got, deferred, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncResolumeInstancesConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.ResolumeInstancesEqual(got, testResolumeInstances) {
		t.Errorf("got = %+v, want %+v", got, testResolumeInstances)
	}

	obj, err := st.GetConfigObject(context.Background(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	rev, err := st.GetConfigRevision(context.Background(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID, obj.CurrentRevision)
	if err != nil {
		t.Fatalf("GetConfigRevision: %v", err)
	}
	if rev.Source != config.ResolumeInstancesSourceEnvMigration {
		t.Errorf("Source = %q, want %q", rev.Source, config.ResolumeInstancesSourceEnvMigration)
	}
	if rev.CreatedByPrincipalID != "" {
		t.Errorf("CreatedByPrincipalID = %q, want empty (a startup migration has no principal)", rev.CreatedByPrincipalID)
	}
}

// TestSyncResolumeInstancesConfigDisagreementRefusesToStart is the owner's
// disagreement rule (mirroring FPP's identical rule): once the store holds
// an active resolume.instances configuration, an environment value that
// disagrees with it refuses to start rather than silently overriding the
// store.
func TestSyncResolumeInstancesConfigDisagreementRefusesToStart(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	if _, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	differing := []config.ResolumeInstance{{ID: "arena-1", URL: "http://10.0.1.99:8080"}}
	_, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, differing, time.Now, discardLogger())
	if err == nil {
		t.Fatalf("syncResolumeInstancesConfig() error = nil, want a refusal naming the difference")
	}
	if !errors.Is(err, errResolumeInstancesDisagree) {
		t.Errorf("error = %v, want it to wrap errResolumeInstancesDisagree", err)
	}
	if !strings.Contains(err.Error(), "10.0.1.99") || !strings.Contains(err.Error(), "10.0.1.30") {
		t.Errorf("error = %q, want it to name both differing URLs", err.Error())
	}

	revs, err := st.ListConfigRevisions(context.Background(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (a refused start must not append a revision)", len(revs))
	}
}

// TestSyncResolumeInstancesConfigIdenticalStartsWithWarning proves the
// still-set-but-agreeing case starts cleanly and warns that the variable
// may now be removed.
func TestSyncResolumeInstancesConfigIdenticalStartsWithWarning(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	if _, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	logger, buf := capturingLogger()
	got, deferred, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, logger)
	if err != nil {
		t.Fatalf("syncResolumeInstancesConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.ResolumeInstancesEqual(got, testResolumeInstances) {
		t.Errorf("got = %+v, want %+v", got, testResolumeInstances)
	}
	if !strings.Contains(buf.String(), "may be removed") {
		t.Errorf("log output = %q, want a WARN naming that the variable may now be removed", buf.String())
	}
}

// TestSyncResolumeInstancesConfigEnvUnsetAfterMigrationUsesStore covers the
// state the migration warning steers an operator toward: the variable
// removed entirely after a prior migration. The store stays authoritative.
func TestSyncResolumeInstancesConfigEnvUnsetAfterMigrationUsesStore(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	if _, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncResolumeInstancesConfig() error = %v", err)
	}

	got, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncResolumeInstancesConfig() with env unset error = %v, want nil", err)
	}
	if !config.ResolumeInstancesEqual(got, testResolumeInstances) {
		t.Errorf("got = %+v, want the store's %+v", got, testResolumeInstances)
	}
}

// TestResolveAuthoritativeResolumeInstancesUsesStoreOverEnv exercises the
// actual Run()-facing entry point, not just syncResolumeInstancesConfig's
// own return value — mirroring TestResolveAuthoritativeFPPEndpointsUsesStoreOverEnv.
func TestResolveAuthoritativeResolumeInstancesUsesStoreOverEnv(t *testing.T) {
	st, svc := newTestResolumeInstancesSyncDeps(t)

	if _, _, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	resolved, deferred, err := resolveAuthoritativeResolumeInstances(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("resolveAuthoritativeResolumeInstances() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.ResolumeInstancesEqual(resolved, testResolumeInstances) {
		t.Errorf("resolved = %+v, want %+v (the store-authoritative list, not the empty env value)", resolved, testResolumeInstances)
	}
}

// newTestResolumeInstancesSyncDepsWithDataDir is
// [newTestResolumeInstancesSyncDeps] plus the data directory, mirroring
// configsync_test.go's newTestConfigSyncDepsWithDataDir — needed so
// [makeTableUnwritable] (defined in that file, same package) can reach the
// real SQLite file underneath.
func newTestResolumeInstancesSyncDepsWithDataDir(t *testing.T) (*store.Store, identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger())), dir
}

// TestSyncResolumeInstancesConfigAuditWriteFailureStartsWithoutMigrating is
// ADR-039 decision 3's own regression test, proved from the start rather
// than shipped backwards and fixed later the way fpp.endpoints was: an
// unwritable audit_log must NOT stop this coordinator starting. Mirrors
// TestSyncFPPEndpointsConfigAuditWriteFailureStartsWithoutMigrating's three
// load-bearing assertions together: starting proves the direction,
// returning envInstances proves the manager is not silently left with
// nothing to reconcile, and persisting NOTHING keeps ADR-024 decision 11
// satisfied rather than exempted.
func TestSyncResolumeInstancesConfigAuditWriteFailureStartsWithoutMigrating(t *testing.T) {
	st, svc, dir := newTestResolumeInstancesSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")
	logger, buf := capturingLogger()

	got, deferred, err := syncResolumeInstancesConfig(context.Background(), st, svc, testResolumeInstances, time.Now, logger)
	if err != nil {
		t.Fatalf("syncResolumeInstancesConfig() error = %v, want nil — an audit-write failure must not stop the coordinator starting", err)
	}
	if !config.ResolumeInstancesEqual(got, testResolumeInstances) {
		t.Errorf("got = %+v, want %+v — this boot must still use the instance it used before", got, testResolumeInstances)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true")
	}

	_, err = st.GetConfigObject(context.Background(), config.ResolumeInstancesConfigKind, config.ResolumeInstancesConfigObjectID)
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("GetConfigObject error = %v, want ErrConfigObjectNotFound — the failed migration must persist nothing", err)
	}

	out := buf.String()
	if !strings.Contains(out, "could not migrate SHOWMESH_RESOLUME_URL") {
		t.Errorf("log output = %q, want an ERROR naming the failed migration", out)
	}
}
