package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMigrationV47AppliesOnADatabaseAtV46 proves a store stamped at v46
// gains fallback_executor_keys, empty, and that a key round-trips in it.
func TestMigrationV47AppliesOnADatabaseAtV46(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 46)

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 47): %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := st.GetFallbackExecutorKey(ctx, "fpp-1"); !errors.Is(err, ErrFallbackExecutorKeyNotFound) {
		t.Fatalf("a freshly migrated store answered %v for an unregistered key, want not found", err)
	}
	at := time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)
	stored, changed, err := st.PutFallbackExecutorKey(ctx, FallbackExecutorKeyRecord{FPPInstanceUUID: "fpp-1", PublicKeyB64: "key-a", RegisteredAt: at})
	if err != nil || !changed || stored.PublicKeyB64 != "key-a" {
		t.Fatalf("first put = %+v changed %v err %v, want key-a stored as a change", stored, changed, err)
	}
	stored, changed, err = st.PutFallbackExecutorKey(ctx, FallbackExecutorKeyRecord{FPPInstanceUUID: "fpp-1", PublicKeyB64: "key-a", RegisteredAt: at.Add(time.Hour)})
	if err != nil || changed || !stored.RegisteredAt.Equal(at) {
		t.Fatalf("same key again = %+v changed %v err %v, want no change and the first registration time kept", stored, changed, err)
	}
	stored, changed, err = st.PutFallbackExecutorKey(ctx, FallbackExecutorKeyRecord{FPPInstanceUUID: "fpp-1", PublicKeyB64: "key-b", RegisteredAt: at.Add(2 * time.Hour)})
	if err != nil || !changed || stored.PublicKeyB64 != "key-b" {
		t.Fatalf("rotation = %+v changed %v err %v, want key-b stored as a change", stored, changed, err)
	}
	got, err := st.GetFallbackExecutorKey(ctx, "fpp-1")
	if err != nil || got.PublicKeyB64 != "key-b" || !got.RegisteredAt.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("read back = %+v err %v, want key-b at its own registration time", got, err)
	}

	// A rewound user_version must be able to replay the migration.
	if _, err := st.db.ExecContext(ctx, schemaV47); err != nil {
		t.Fatalf("second run of migration 47: %v", err)
	}
}

func TestFreshDatabaseHasFallbackExecutorKeys(t *testing.T) {
	ctx := context.Background()
	st, err := open(ctx, t.TempDir(), nil, time.Now)
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version < 47 {
		t.Fatalf("a fresh store is stamped at version %d, want at least 47", version)
	}
	var count int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fallback_executor_keys`).Scan(&count); err != nil {
		t.Fatalf("a fresh store has no fallback_executor_keys table: %v", err)
	}
	if count != 0 {
		t.Fatalf("a fresh store holds %d executor keys, want none", count)
	}
}
