package coordinator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"

	// Registers the "sqlite" driver for [makeTableUnwritable]'s direct
	// second connection to the store's own database file. The coordinator
	// itself never imports a SQL driver — internal/coordinator/store owns
	// that (ADR-012: pure-Go modernc.org/sqlite, never mattn/go-sqlite3) —
	// so this is a test-only import and must stay one.
	_ "modernc.org/sqlite"
)

// This file is Step 7 seam A's own startup-sequencing suite: the
// SHOWMESH_FPP_ENDPOINTS -> store migration (RES-008 D1) and the owner's
// 2026-08-12 disagreement rule (acceptance criteria 4 and 5).

func newTestConfigSyncDeps(t *testing.T) (*store.Store, identity.Service) {
	t.Helper()
	st := openTestStore(t)
	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger()))
	return st, svc
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// capturingLogger returns a logger writing plain text into a buffer a test
// can inspect, and the buffer itself — used by the defect-3b and defect-6
// regression tests below to prove a WARN actually fires, not merely that
// the function returns the right value.
func capturingLogger() (*slog.Logger, *strings.Builder) {
	var buf strings.Builder
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), &buf
}

var testEndpoints = []config.FPPEndpoint{
	{ID: "player-01", URL: "http://10.0.1.20"},
	{ID: "shed", URL: "http://10.0.1.21"},
}

// TestSyncFPPEndpointsConfigNothingConfiguredIsANoop covers the case
// neither the store nor the environment has anything: the collector must
// not run, exactly as before this seam existed.
func TestSyncFPPEndpointsConfigNothingConfiguredIsANoop(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	got, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty", got)
	}
}

// TestSyncFPPEndpointsConfigMigratesFromEnv is acceptance criterion 4: an
// existing deployment with SHOWMESH_FPP_ENDPOINTS set migrates without
// losing an endpoint. Also proves the migration is audited with NO
// principal (a startup migration has no principal) and source
// "env_migration".
func TestSyncFPPEndpointsConfigMigratesFromEnv(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	got, deferred, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("migrationDeferred = true on a migration that succeeded")
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v (no endpoint lost in migration)", got, testEndpoints)
	}

	obj, err := st.GetConfigObject(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject after migration: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}

	rev, err := st.GetConfigRevision(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, 1)
	if err != nil {
		t.Fatalf("GetConfigRevision(1): %v", err)
	}
	if rev.Source != config.FPPEndpointsSourceEnvMigration {
		t.Errorf("Source = %q, want %q", rev.Source, config.FPPEndpointsSourceEnvMigration)
	}
	// Decode what was actually WRITTEN to the store, not just what the
	// function happened to return — a write-side truncation (e.g. storing
	// only the first endpoint) is invisible to a check that only inspects
	// syncFPPEndpointsConfig's return value.
	storedPayload, err := config.DecodeFPPEndpointsPayload(rev.PayloadJSON)
	if err != nil {
		t.Fatalf("DecodeFPPEndpointsPayload(rev.PayloadJSON): %v", err)
	}
	if !config.FPPEndpointsEqual(storedPayload, testEndpoints) {
		t.Errorf("stored payload = %+v, want %+v (the full endpoint list, not a truncated one)", storedPayload, testEndpoints)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "config.migrate" {
			found = true
			if e.PrincipalID != "" || e.PrincipalName != "" {
				t.Errorf("migration audit entry has a principal (%q/%q), want none — a startup migration has no principal", e.PrincipalID, e.PrincipalName)
			}
		}
	}
	if !found {
		t.Errorf("no config.migrate audit entry found among %d entries", len(entries))
	}
}

// TestSyncFPPEndpointsConfigIdenticalStartsWithWarning is half of
// acceptance criterion 5: the variable left set and identical to the
// store's active configuration starts, with the store's list returned as
// authoritative (not the raw env list — same set, but this proves the
// function does not just pass envEndpoints through unexamined).
func TestSyncFPPEndpointsConfigIdenticalStartsWithWarning(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	// First call migrates.
	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	// Second call, as if the coordinator restarted with the identical
	// variable still set, in a different order (order-insensitive per
	// this seam's spec).
	reordered := []config.FPPEndpoint{testEndpoints[1], testEndpoints[0]}
	got, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, reordered, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("second syncFPPEndpointsConfig() error = %v, want nil (identical configuration must start)", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v", got, testEndpoints)
	}

	// Still exactly one revision: the identical-and-matching case does not
	// append a second revision on the store's behalf.
	revs, err := st.ListConfigRevisions(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (identical env value must not append a new revision)", len(revs))
	}
}

// TestSyncFPPEndpointsConfigDifferentRefusesToStart is the other half of
// acceptance criterion 5: changed, it refuses to start and names the
// difference.
func TestSyncFPPEndpointsConfigDifferentRefusesToStart(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	changed := []config.FPPEndpoint{
		{ID: "player-01", URL: "http://10.0.1.20"},
		{ID: "shed", URL: "http://10.0.1.99"}, // url changed
	}
	_, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, changed, time.Now, discardLogger())
	if err == nil {
		t.Fatalf("syncFPPEndpointsConfig() error = nil, want a refusal naming the difference")
	}
	if !errors.Is(err, errFPPEndpointsDisagree) {
		t.Errorf("error = %v, want it to wrap errFPPEndpointsDisagree", err)
	}
	if !strings.Contains(err.Error(), "shed") {
		t.Errorf("error = %q, want it to name the differing endpoint id %q", err.Error(), "shed")
	}
	if !strings.Contains(err.Error(), "10.0.1.99") || !strings.Contains(err.Error(), "10.0.1.21") {
		t.Errorf("error = %q, want it to name both differing URLs", err.Error())
	}

	// The store's configuration must be unchanged by a refused start:
	// still exactly one revision, still the original payload.
	revs, err := st.ListConfigRevisions(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (a refused start must not append a revision)", len(revs))
	}
}

// TestSyncFPPEndpointsConfigEnvUnsetAfterMigrationUsesStore covers the
// state RES-008 D1's warning steers an operator toward: the variable
// removed entirely after a prior migration. The store stays authoritative
// with no error and no warning (nothing to compare against).
func TestSyncFPPEndpointsConfigEnvUnsetAfterMigrationUsesStore(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	got, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() with env unset error = %v, want nil", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want the store's %+v", got, testEndpoints)
	}
}

// TestResolveAuthoritativeFPPEndpointsUsesStoreOverEnv is the acceptance
// criterion Step 7 seam A review finding 1 named: "an existing deployment
// with SHOWMESH_FPP_ENDPOINTS set migrates into the configuration table
// without losing an endpoint, and the migrated deployment still collects
// from every host it collected from before." It exercises the actual
// config-resolution path Run() calls (resolveAuthoritativeFPPEndpoints),
// not just syncFPPEndpointsConfig's own return value, so it fails if
// resolveAuthoritativeFPPEndpoints ever stops overwriting
// cfg.FPPEndpoints with the store-authoritative list — the assignment
// every downstream FPP collector consumer (fppInstanceLister, the
// collector construction loop, api.Dependencies.Collectors) depends on.
func TestResolveAuthoritativeFPPEndpointsUsesStoreOverEnv(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	// Simulate a deployment that has already migrated (store populated)
	// and then had SHOWMESH_FPP_ENDPOINTS removed from its environment,
	// exactly the state RES-008 D1's warning steers an operator toward.
	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("seed migration: %v", err)
	}

	cfg := config.Config{FPPEndpoints: nil}
	resolved, _, err := resolveAuthoritativeFPPEndpoints(context.Background(), st, svc, cfg, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("resolveAuthoritativeFPPEndpoints() error = %v, want nil", err)
	}
	if len(resolved.FPPEndpoints) == 0 {
		t.Fatalf("resolved.FPPEndpoints is empty; want the coordinator to still collect from the store's configured hosts")
	}
	if !config.FPPEndpointsEqual(resolved.FPPEndpoints, testEndpoints) {
		t.Errorf("resolved.FPPEndpoints = %+v, want %+v (the store-authoritative list, not the empty env value)", resolved.FPPEndpoints, testEndpoints)
	}
}

// TestDiffFPPEndpointsNamesEveryKindOfDifference proves diffFPPEndpoints
// names an id present only in the store, an id present only in the
// environment, and an id with a differing URL, all in one message.
// TestSyncFPPEndpointsConfigMigrationWarnsOnTheMigratingBoot is Step 7
// seam A review defect 3b's regression test. Before this fix, the "the
// store is now authoritative, you may remove this variable" warning
// existed ONLY in syncFPPEndpointsConfig's identical-values branch, which
// cannot run on the boot that performs the migration (that branch
// requires a store configuration to ALREADY exist). This is the boot
// where the guidance first becomes true, and previously it was silent.
func TestSyncFPPEndpointsConfigMigrationWarnsOnTheMigratingBoot(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)
	logger, buf := capturingLogger()

	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, logger); err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil", err)
	}

	got := buf.String()
	if !strings.Contains(got, "may be removed") {
		t.Errorf("log output = %q, want a WARN naming that SHOWMESH_FPP_ENDPOINTS may now be removed", got)
	}
}

// newTestConfigSyncDepsWithDataDir is [newTestConfigSyncDeps] plus the data
// directory, so a test can reach the SQLite file underneath a live
// *store.Store and make one table genuinely unwritable — see
// [makeTableUnwritable].
func newTestConfigSyncDepsWithDataDir(t *testing.T) (*store.Store, identity.Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger())), dir
}

// makeTableUnwritable installs a BEFORE INSERT trigger that aborts every
// insert into table, over a second connection to the same SQLite file the
// live *store.Store is using. This is how the two migration-failure tests
// below fail a write, and the choice is load-bearing rather than
// stylistic.
//
// A fake identity.Service overriding AuditedWrite was tried first and a
// review rejected it, correctly. The assertion these tests exist to make
// is that NOTHING is persisted, and the whole reason that assertion holds
// is ADR-024 decision 11's same-transaction rule inside the REAL
// identity.svc.AuditedWrite: fn's config revision and its audit row share
// one transaction, so the audit failure rolls the revision back too. A
// fake that substitutes its own transaction proves that property about
// the fake. Rewriting AuditedWrite to commit fn's work and append the
// audit row separately — a direct violation of decision 11 — would have
// left those tests green.
//
// Aborting inside the transaction is also a truer fault than an error
// returned from a stub: it is what a full volume, a read-only mount, or a
// damaged database actually does, and it is the same mechanism the
// real-binary verification in the build log used against a real
// coordinator process.
func makeTableUnwritable(t *testing.T, dataDir, table string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "showmesh.db"))
	if err != nil {
		t.Fatalf("open sqlite directly: %v", err)
	}
	defer func() { _ = db.Close() }()
	stmt := fmt.Sprintf(
		`CREATE TRIGGER %s_unwritable BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'simulated unwritable %s'); END;`,
		table, table, table)
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("install unwritable trigger on %s: %v", table, err)
	}
}

// dropTrigger reverses [makeTableUnwritable], standing in for an operator
// fixing the data volume between two boots.
func dropTrigger(t *testing.T, dataDir, trigger string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "showmesh.db"))
	if err != nil {
		t.Fatalf("open sqlite directly: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("DROP TRIGGER " + trigger); err != nil {
		t.Fatalf("drop trigger %s: %v", trigger, err)
	}
}

// assertNoFPPEndpointsConfig fails the test unless the store holds no
// fpp.endpoints configuration object at all.
func assertNoFPPEndpointsConfig(t *testing.T, st *store.Store) {
	t.Helper()
	_, err := st.GetConfigObject(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("GetConfigObject error = %v, want ErrConfigObjectNotFound — the failed migration must persist nothing", err)
	}
}

// TestSyncFPPEndpointsConfigAuditWriteFailureStartsWithoutMigrating is the
// regression test for the failure direction this branch shipped backwards
// in Step 7: an unwritable audit_log made the coordinator exit 1, and
// because deploy/docker-compose.yml sets restart: unless-stopped, that is
// a restart loop with no API, no change stream, and no dashboard. It is
// reachable only on the first boot after an existing deployment upgrades
// into Step 7, so the operator would read it as the upgrade having broken
// their coordinator. See migrateFPPEndpointsFromEnv's doc comment for the
// full argument; the short version is that ADR-024 decision 11's
// fail-closed rule protects an operator from an unaccountable actor, and
// this migration has no actor.
//
// The three assertions are load-bearing together. Starting proves the
// direction; returning envEndpoints proves the collector is not silently
// left with nothing (defect 7's case); and persisting NOTHING is what
// keeps decision 11 satisfied rather than exempted, which is the whole
// reason this fix needs no ADR change.
func TestSyncFPPEndpointsConfigAuditWriteFailureStartsWithoutMigrating(t *testing.T) {
	st, svc, dir := newTestConfigSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")
	logger, buf := capturingLogger()

	got, deferred, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil — an audit-write failure must not stop the coordinator starting", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v — this boot must still collect from every host it collected from before", got, testEndpoints)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true: the API cannot state a deferral it is never told about, " +
			"and reports a coordinator nothing has ever configured instead")
	}
	assertNoFPPEndpointsConfig(t, st)

	out := buf.String()
	if !strings.Contains(out, "could not migrate SHOWMESH_FPP_ENDPOINTS") {
		t.Errorf("log output = %q, want an ERROR naming the failed migration", out)
	}
	// Deliberately NOT "audit entry could not be written": that phrase also
	// appears in identity.ErrAuditWrite's own text, which the log line
	// attaches as its error field, so asserting it passed whether or not
	// the two failure modes were named apart at all. Found by mutating the
	// cause clause and watching this test stay green — the project's
	// "a test's name is a claim" rule, applied to a test written in the
	// same commit as the code it guards.
	if !strings.Contains(out, "audit entry could not be recorded alongside it") {
		t.Errorf("log output = %q, want the audit-write failure mode named apart from a plain store failure", out)
	}
}

// TestSyncFPPEndpointsConfigRevisionWriteFailureStartsWithoutMigrating is
// the same direction for the OTHER failure mode identity.AuditedWrite
// keeps distinguishable: fn's own error, returned unwrapped, meaning the
// config revision itself could not be written. The argument is identical
// (a store write failing at boot must not cost the operator the read API
// either) and only the remedy differs, so only the message does.
//
// An earlier version of this test produced its failure by calling
// migrateFPPEndpointsFromEnv twice, hitting store.ErrConfigRevisionExists.
// A review pointed out that syncFPPEndpointsConfig only ever calls that
// function on ErrConfigObjectNotFound, so the state under test was
// unreachable in production and the test's name named a function it never
// called. It now goes through syncFPPEndpointsConfig from a genuinely
// empty store, with config_revisions itself unwritable.
func TestSyncFPPEndpointsConfigRevisionWriteFailureStartsWithoutMigrating(t *testing.T) {
	st, svc, dir := newTestConfigSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "config_revisions")
	logger, buf := capturingLogger()

	got, deferred, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil — a revision-write failure must not stop the coordinator starting", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v", got, testEndpoints)
	}
	if !deferred {
		t.Error("migrationDeferred = false, want true")
	}
	assertNoFPPEndpointsConfig(t, st)

	// The audit row must not survive its own transaction either. This is
	// the mirror of the audit-failure case and it is the assertion that
	// catches decision 11's rule being broken in the other direction: an
	// audit entry describing a config revision that does not exist.
	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action == "config.migrate" {
			t.Errorf("found a config.migrate audit entry for a migration that did not happen: %+v", e)
		}
	}

	out := buf.String()
	if !strings.Contains(out, "config revision itself could not be written") {
		t.Errorf("log output = %q, want the revision-write failure mode named apart from an audit-write failure", out)
	}
	if strings.Contains(out, "may be removed") {
		t.Errorf("log output = %q, must NOT tell the operator the store is authoritative and the variable may be removed: "+
			"nothing was migrated", out)
	}
	// The deferred state's one destructive trap, asserted on the log line
	// the operator actually sees: removing SHOWMESH_FPP_ENDPOINTS here
	// resolves this coordinator to zero endpoints, because the store holds
	// no copy of the list.
	if !strings.Contains(out, "DO NOT remove SHOWMESH_FPP_ENDPOINTS") {
		t.Errorf("log output = %q, want an explicit warning against removing the variable while the migration is deferred", out)
	}
}

// TestSyncFPPEndpointsConfigDeferredMigrationRetriesOnNextBoot proves the
// self-healing claim migrateFPPEndpointsFromEnv's doc comment rests on. A
// deferred migration that never completed would be worse than the exit it
// replaced: the store would stay permanently unconfigured while every log
// line after the first boot looked fine.
func TestSyncFPPEndpointsConfigDeferredMigrationRetriesOnNextBoot(t *testing.T) {
	st, svc, dir := newTestConfigSyncDepsWithDataDir(t)
	makeTableUnwritable(t, dir, "audit_log")

	_, deferred, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("failed-write boot: syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if !deferred {
		t.Fatal("failed-write boot: migrationDeferred = false, want true")
	}
	assertNoFPPEndpointsConfig(t, st)

	// Next boot, store writable again. Dropping the trigger is exactly
	// what fixing the data volume does: nothing in the coordinator
	// changes, and the next start retries the migration.
	dropTrigger(t, dir, "audit_log_unwritable")
	got, deferred, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("retry boot: syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if deferred {
		t.Error("retry boot: migrationDeferred = true, want false — the migration completed, so the API must stop " +
			"telling the operator not to remove SHOWMESH_FPP_ENDPOINTS")
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v", got, testEndpoints)
	}

	obj, err := st.GetConfigObject(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject after retry: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1 — the deferred migration must complete on a later boot", obj.CurrentRevision)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var migrations int
	for _, e := range entries {
		if e.Action == "config.migrate" {
			migrations++
		}
	}
	if migrations != 1 {
		t.Errorf("config.migrate audit entries = %d, want exactly 1 — the deferred attempt must leave no entry behind", migrations)
	}
}

// TestSyncFPPEndpointsConfigZeroCurrentRevisionStartsWithWarning is Step 7
// seam A review defect 6's regression test, the CurrentRevision == 0 half:
// the store-integrity defence handleGetFPPEndpointsConfig already has
// (internal/coordinator/api/config.go) must ALSO cover the boot path — a
// coordinator that refuses to start over a store-integrity condition, when
// "treat it as unconfigured" is available and safe, violates constraint
// 13 ("the coordinator must start and stay up").
func TestSyncFPPEndpointsConfigZeroCurrentRevisionStartsWithWarning(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)
	logger, buf := capturingLogger()

	// Produces exactly the state store.CreateConfigObject's own doc
	// comment names as reachable: "declared, nothing active" —
	// CurrentRevision == 0 with no revision ever activated. Reached
	// directly through the store rather than through this seam's own PUT
	// handler, which never leaves an object in this state (see
	// config.go's handleGetFPPEndpointsConfig doc comment) — this test
	// exists for the state existing at all, from whatever future caller
	// might produce it.
	if _, err := st.CreateConfigObject(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID); err != nil {
		t.Fatalf("CreateConfigObject: %v", err)
	}

	got, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil (must start, not refuse)", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty (no active configuration)", got)
	}
	if logged := buf.String(); !strings.Contains(logged, "no active revision") {
		t.Errorf("log output = %q, want a WARN naming the current_revision == 0 condition", logged)
	}
}

// TestSyncFPPEndpointsConfigDanglingRevisionPointerStartsWithWarning is
// defect 6's other half: obj.CurrentRevision names a revision this store
// does not hold (a store-integrity condition GetConfigRevision reports as
// [store.ErrConfigRevisionNotFound]). Before this fix, syncFPPEndpointsConfig
// ran the identical GetConfigRevision call with no defence at all and
// turned this into a fatal startup error; this test pins the safer
// outcome instead.
func TestSyncFPPEndpointsConfigDanglingRevisionPointerStartsWithWarning(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)
	logger, buf := capturingLogger()

	if _, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Corrupt the pointer directly at the store layer: activate a
	// revision number nothing backs. ActivateConfigRevision's own
	// contract (store/config.go) is "activate only what you just
	// created"; this test deliberately violates it to reach the dangling
	// state defensively rather than through any normal code path.
	if _, err := st.ActivateConfigRevision(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, 99); err != nil {
		t.Fatalf("ActivateConfigRevision(99): %v", err)
	}

	got, _, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, logger)
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil (must start, not refuse)", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty (no active configuration)", got)
	}
	if logged := buf.String(); !strings.Contains(logged, "store-integrity condition") {
		t.Errorf("log output = %q, want a WARN naming the store-integrity condition", logged)
	}
}

func TestDiffFPPEndpointsNamesEveryKindOfDifference(t *testing.T) {
	stored := []config.FPPEndpoint{
		{ID: "shared", URL: "http://10.0.1.1"},
		{ID: "store-only", URL: "http://10.0.1.2"},
	}
	env := []config.FPPEndpoint{
		{ID: "shared", URL: "http://10.0.1.99"},
		{ID: "env-only", URL: "http://10.0.1.3"},
	}

	msg := diffFPPEndpoints(stored, env)
	for _, want := range []string{"shared", "10.0.1.1", "10.0.1.99", "store-only", "10.0.1.2", "env-only", "10.0.1.3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("diffFPPEndpoints message = %q, want it to contain %q", msg, want)
		}
	}
}
