package coordinator

// This file is Track G seam G-3's own startup-sequencing suite (ADR-039),
// mirroring resolumeinstancessync_test.go's identical shape for the
// fpp.mqtt kind, plus the password-specific cases neither fpp.endpoints
// nor resolume.instances has: the password migrates and compares
// alongside the non-secret fields even though it lives in a separate file
// rather than in the payload.

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

func newTestFPPMQTTSyncDeps(t *testing.T) (*store.Store, identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger())), dir
}

var testFPPMQTTConfig = config.FPPMQTTConfig{
	BrokerURL: "tcp://10.0.1.5:1883", TopicPrefix: "falcon/player",
	Hosts: map[string]string{"player-01": "FPP-Player"},
}

const testFPPMQTTPassword = "s3cret"

func TestSyncFPPMQTTConfigNothingConfiguredIsANoop(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	cfg, password, deferred, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, config.FPPMQTTConfig{}, "", time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if cfg.Configured() || password != "" {
		t.Errorf("cfg/password = %+v/%q, want unconfigured and empty", cfg, password)
	}
}

// TestSyncFPPMQTTConfigMigratesFromEnv proves the env->store migration:
// the non-secret fields land as revision 1 with source env_migration and
// no principal, and the password lands in the secret file, never in the
// revision's payload_json.
func TestSyncFPPMQTTConfigMigratesFromEnv(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	cfg, password, deferred, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("cfg/password = %+v/%q, want %+v/%q", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}

	obj, err := st.GetConfigObject(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	rev, err := st.GetConfigRevision(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, obj.CurrentRevision)
	if err != nil {
		t.Fatalf("GetConfigRevision: %v", err)
	}
	if rev.Source != config.FPPMQTTSourceEnvMigration {
		t.Errorf("Source = %q, want %q", rev.Source, config.FPPMQTTSourceEnvMigration)
	}
	if rev.CreatedByPrincipalID != "" {
		t.Errorf("CreatedByPrincipalID = %q, want empty (a startup migration has no principal)", rev.CreatedByPrincipalID)
	}
	if strings.Contains(rev.PayloadJSON, testFPPMQTTPassword) {
		t.Errorf("payload_json = %q, must never contain the password (ADR-039 decision 7)", rev.PayloadJSON)
	}

	stored, present, err := config.ReadFPPMQTTPassword(dir)
	if err != nil {
		t.Fatalf("ReadFPPMQTTPassword: %v", err)
	}
	if !present || stored != testFPPMQTTPassword {
		t.Fatalf("ReadFPPMQTTPassword = (%q, %v), want (%q, true)", stored, present, testFPPMQTTPassword)
	}
}

// TestSyncFPPMQTTConfigDisagreementRefusesToStart mirrors the owner's
// disagreement rule, extended to cover a password-only disagreement: even
// when every non-secret field matches, a differing password refuses to
// start rather than silently overriding the store.
func TestSyncFPPMQTTConfigDisagreementRefusesToStart(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	if _, _, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	_, _, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, "different-password", time.Now, discardLogger())
	if err == nil {
		t.Fatalf("syncFPPMQTTConfig() error = nil, want a refusal")
	}
	if !errors.Is(err, errFPPMQTTDisagree) {
		t.Errorf("error = %v, want it to wrap errFPPMQTTDisagree", err)
	}
	if strings.Contains(err.Error(), "different-password") || strings.Contains(err.Error(), testFPPMQTTPassword) {
		t.Errorf("error = %q, must never name a password value", err.Error())
	}

	revs, err := st.ListConfigRevisions(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (a refused start must not append a revision)", len(revs))
	}
}

func TestSyncFPPMQTTConfigIdenticalStartsWithWarning(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	if _, _, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	logger, buf := capturingLogger()
	cfg, password, deferred, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("cfg/password = %+v/%q, want %+v/%q", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}
	if !strings.Contains(buf.String(), "may be removed") {
		t.Errorf("log output = %q, want a WARN naming that the variables may now be removed", buf.String())
	}
}

func TestSyncFPPMQTTConfigEnvUnsetAfterMigrationUsesStore(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	if _, _, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPMQTTConfig() error = %v", err)
	}

	cfg, password, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, config.FPPMQTTConfig{}, "", time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() with env unset error = %v, want nil", err)
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("cfg/password = %+v/%q, want the store's %+v/%q", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}
}

func TestResolveAuthoritativeFPPMQTTUsesStoreOverEnv(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)

	if _, _, _, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	cfg, password, deferred, err := resolveAuthoritativeFPPMQTT(context.Background(), st, svc, dir, config.FPPMQTTConfig{}, "", time.Now, discardLogger())
	if err != nil {
		t.Fatalf("resolveAuthoritativeFPPMQTT() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true, want false")
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("resolved cfg/password = %+v/%q, want the store-authoritative %+v/%q, not the empty env value", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}
}

// TestSyncFPPMQTTConfigAuditWriteFailureStartsWithoutMigrating is ADR-039
// decision 3's own regression test: an unwritable audit_log must NOT stop
// this coordinator starting, and must not leave a stray secret file with
// no revision to match it.
func TestSyncFPPMQTTConfigAuditWriteFailureStartsWithoutMigrating(t *testing.T) {
	st, svc, dir := newTestFPPMQTTSyncDeps(t)
	makeTableUnwritable(t, dir, "audit_log")
	logger, buf := capturingLogger()

	cfg, password, deferred, err := syncFPPMQTTConfig(context.Background(), st, svc, dir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() error = %v, want nil — an audit-write failure must not stop the coordinator starting", err)
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("cfg/password = %+v/%q, want %+v/%q — this boot must still use the configuration it used before", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true")
	}

	_, err = st.GetConfigObject(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID)
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("GetConfigObject error = %v, want ErrConfigObjectNotFound — the failed migration must persist nothing", err)
	}

	out := buf.String()
	if !strings.Contains(out, "could not migrate SHOWMESH_FPP_MQTT_*") {
		t.Errorf("log output = %q, want an ERROR naming the failed migration", out)
	}
}

// TestMigrateFPPMQTTFromEnvSecretFileFailureDefersWithoutTouchingStore
// covers the write-ordering guarantee handlePutFPPMQTTConfig's own doc
// comment states for the API path and migrateFPPMQTTFromEnv shares for
// startup: if the secret file cannot be written, the migration is
// deferred and NOTHING is written to the store either.
func TestMigrateFPPMQTTFromEnvSecretFileFailureDefersWithoutTouchingStore(t *testing.T) {
	st, svc, _ := newTestFPPMQTTSyncDeps(t)
	// A data directory that cannot hold the secret file: point at a path
	// whose parent is a file, not a directory, so os.MkdirAll fails.
	unwritableDir := "/dev/null/fpp-mqtt-secret-parent-is-a-file"
	logger, buf := capturingLogger()

	cfg, password, deferred, err := syncFPPMQTTConfig(context.Background(), st, svc, unwritableDir, testFPPMQTTConfig, testFPPMQTTPassword, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPMQTTConfig() error = %v, want nil — a secret-file failure must not stop the coordinator starting", err)
	}
	if !config.FPPMQTTConfigEqual(cfg, testFPPMQTTConfig) || password != testFPPMQTTPassword {
		t.Errorf("cfg/password = %+v/%q, want the raw env values %+v/%q", cfg, password, testFPPMQTTConfig, testFPPMQTTPassword)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true")
	}
	if _, err := st.GetConfigObject(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID); !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("GetConfigObject error = %v, want ErrConfigObjectNotFound — a secret-file failure must not write a revision either", err)
	}
	if !strings.Contains(buf.String(), "secret file could not be written") {
		t.Errorf("log output = %q, want an ERROR naming the secret file failure", buf.String())
	}
}
