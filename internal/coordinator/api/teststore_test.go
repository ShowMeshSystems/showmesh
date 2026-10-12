package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// testStoreFileName is the database file [store.Open] creates in its data
// directory.
const testStoreFileName = "showmesh.db"

var migratedTemplate struct {
	once  sync.Once
	bytes []byte
	err   error
}

// openTestStore is [store.Open] for this package's tests. A data directory
// with no database yet starts from a copy of one template migrated once per
// test binary; a test of the migration chain itself calls store.Open.
func openTestStore(ctx context.Context, dataDir string, logger *slog.Logger, opts ...store.Option) (*store.Store, error) {
	if err := seedFromMigratedTemplate(ctx, dataDir); err != nil {
		return nil, err
	}
	return store.Open(ctx, dataDir, logger, opts...)
}

func seedFromMigratedTemplate(ctx context.Context, dataDir string) error {
	dbPath := filepath.Join(dataDir, testStoreFileName)
	if _, err := os.Stat(dbPath); !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	migratedTemplate.once.Do(func() {
		migratedTemplate.bytes, migratedTemplate.err = buildMigratedTemplate(ctx)
	})
	if migratedTemplate.err != nil {
		return fmt.Errorf("build migrated template database: %w", migratedTemplate.err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(dbPath, migratedTemplate.bytes, 0o644)
}

func buildMigratedTemplate(ctx context.Context) ([]byte, error) {
	dir, err := os.MkdirTemp("", "showmesh-api-template-db-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	st, err := store.Open(ctx, dir, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	// The main file alone is the whole database only once the last
	// connection has checkpointed and removed the write-ahead log.
	dbPath := filepath.Join(dir, testStoreFileName)
	if info, err := os.Stat(dbPath + "-wal"); err == nil && info.Size() > 0 {
		return nil, fmt.Errorf("closed template still has a %d byte write-ahead log", info.Size())
	}
	return os.ReadFile(dbPath)
}

// schemaFingerprint describes the database in storeDir: its schema version,
// every schema object's SQL, and each table's row count.
func schemaFingerprint(t *testing.T, storeDir string) []string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(storeDir, testStoreFileName))
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()
	ctx := context.Background()

	var version int
	if err := raw.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	fingerprint := []string{fmt.Sprintf("user_version=%d", version)}

	rows, err := raw.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, ddl string
		if err := rows.Scan(&kind, &name, &ddl); err != nil {
			t.Fatalf("scan schema row: %v", err)
		}
		fingerprint = append(fingerprint, kind+" "+name+": "+ddl)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("read schema: %v", err)
	}
	for _, table := range tables {
		var count int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+table+`"`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		fingerprint = append(fingerprint, fmt.Sprintf("rows %s=%d", table, count))
	}
	return fingerprint
}

func TestOpenTestStoreMatchesAFreshMigrationReplay(t *testing.T) {
	ctx := context.Background()
	replayedDir, templatedDir := t.TempDir(), t.TempDir()
	replayed, err := store.Open(ctx, replayedDir, testLogger())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = replayed.Close() })
	templated, err := openTestStore(ctx, templatedDir, testLogger())
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = templated.Close() })

	want, got := schemaFingerprint(t, replayedDir), schemaFingerprint(t, templatedDir)
	if len(want) < 10 {
		t.Fatalf("fingerprint has %d entries, want a migrated schema", len(want))
	}
	if !slices.Equal(got, want) {
		t.Errorf("templated database differs from a fresh migration replay:\n got %q\nwant %q", got, want)
	}
}

func TestOpenTestStoreGivesEachDirectoryItsOwnDatabase(t *testing.T) {
	ctx := context.Background()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	for _, dir := range []string{firstDir, secondDir} {
		st, err := openTestStore(ctx, dir, testLogger())
		if err != nil {
			t.Fatalf("openTestStore: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	}
	before := schemaFingerprint(t, secondDir)
	createMarkerTable(t, firstDir)

	if got := schemaFingerprint(t, secondDir); !slices.Equal(got, before) {
		t.Errorf("a write to one test database changed another:\n got %q\nwant %q", got, before)
	}
}

func TestOpenTestStoreKeepsAnExistingDatabaseOnReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := openTestStore(ctx, dir, testLogger())
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	createMarkerTable(t, dir)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	want := schemaFingerprint(t, dir)

	reopened, err := openTestStore(ctx, dir, testLogger())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := schemaFingerprint(t, dir); !slices.Equal(got, want) {
		t.Errorf("reopening replaced the database:\n got %q\nwant %q", got, want)
	}
	if !slices.Contains(want, "rows test_store_marker=1") {
		t.Errorf("fingerprint %q has no marker row, so this test proves nothing", want)
	}
}

func createMarkerTable(t *testing.T, storeDir string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(storeDir, testStoreFileName))
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.ExecContext(context.Background(),
		`CREATE TABLE test_store_marker (id INTEGER); INSERT INTO test_store_marker VALUES (1)`); err != nil {
		t.Fatalf("create marker table: %v", err)
	}
}
