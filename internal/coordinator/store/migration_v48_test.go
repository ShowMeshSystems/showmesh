package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMigrationV48AppliesOnADatabaseAtV47 proves a store stamped at v47
// gains fallback_player_states, empty, and that a report round-trips in it.
func TestMigrationV48AppliesOnADatabaseAtV47(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 47)

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 48): %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := st.GetFallbackPlayerState(ctx, "fpp-1"); !errors.Is(err, ErrFallbackPlayerStateNotFound) {
		t.Fatalf("a freshly migrated store answered %v for a player that never reported, want not found", err)
	}
	at := time.Date(2026, 10, 5, 20, 0, 0, 0, time.UTC)
	want := FallbackPlayerStateRecord{
		FPPInstanceUUID: "fpp-1", BootID: "boot-a", Sequence: 3, State: "fallback", Since: at,
		PlaylistName: "Main Show", PackageID: "pkg-1", PackageRevision: "rev-1", CutoffAt: "2026-10-06T08:00:00Z",
		ReceivedAt: at.Add(time.Second), StateChangedAt: at.Add(time.Second),
		ObservationsIgnoredThrough: at.Add(time.Second),
	}
	if err := st.PutFallbackPlayerState(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := st.GetFallbackPlayerState(ctx, "fpp-1")
	if err != nil || got != want {
		t.Fatalf("read back = %+v err %v, want %+v", got, err, want)
	}

	want.State, want.AckWaitSince = "normal", at.Add(time.Minute)
	if err := st.PutFallbackPlayerState(ctx, want); err != nil {
		t.Fatalf("second put: %v", err)
	}
	if err := st.ClearFallbackPlayerStateAckWait(ctx, "fpp-1", at); err != nil {
		t.Fatalf("clear with another wait time: %v", err)
	}
	if got, _ = st.GetFallbackPlayerState(ctx, "fpp-1"); got.AckWaitSince.IsZero() {
		t.Fatal("clearing a wait the row does not carry ended the wait it does carry")
	}
	if err := st.ClearFallbackPlayerStateAckWait(ctx, "fpp-1", want.AckWaitSince); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ = st.GetFallbackPlayerState(ctx, "fpp-1"); !got.AckWaitSince.IsZero() || got.State != "normal" {
		t.Fatalf("after clearing the wait the row is %+v, want normal with no wait", got)
	}

	list, err := st.ListFallbackPlayerStates(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d rows err %v, want one", len(list), err)
	}
	var deleted bool
	if err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		var derr error
		deleted, derr = tx.DeleteFallbackPlayerState(ctx, "fpp-1")
		return derr
	}); err != nil || !deleted {
		t.Fatalf("delete = %v err %v, want a row removed", deleted, err)
	}
	if _, err := st.GetFallbackPlayerState(ctx, "fpp-1"); !errors.Is(err, ErrFallbackPlayerStateNotFound) {
		t.Fatalf("after delete the store answered %v, want not found", err)
	}

	// A rewound user_version must be able to replay the migration.
	if _, err := st.db.ExecContext(ctx, schemaV48); err != nil {
		t.Fatalf("second run of migration 48: %v", err)
	}
}

func TestFreshDatabaseHasFallbackPlayerStates(t *testing.T) {
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
	if version < 48 {
		t.Fatalf("a fresh store is stamped at version %d, want at least 48", version)
	}
	var count int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fallback_player_states`).Scan(&count); err != nil {
		t.Fatalf("a fresh store has no fallback_player_states table: %v", err)
	}
}
