package store

import (
	"context"
	"testing"
	"time"
)

func TestMigrationV50AppliesOnADatabaseAtV49(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	buildStoreAtVersion(t, dir, 49)

	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 50): %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, ok, err := st.GetDisplayCeiling(ctx); err != nil || ok {
		t.Fatalf("GetDisplayCeiling after v50 = ok %v, err %v; want no row", ok, err)
	}
}

func TestDisplayCeilingRoundTripsAndOverwrites(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, nil)

	changed := time.Date(2026, 10, 17, 20, 0, 1, 500, time.UTC)
	first := DisplayCeilingRecord{
		TargetPercent: 60, FadeEndsAt: changed.Add(30 * time.Second), ChangedAt: changed,
		ChangedByKind: "principal", ChangedBy: "p1", ChangedByName: "operator-1",
	}
	if err := st.SetDisplayCeiling(ctx, first); err != nil {
		t.Fatalf("SetDisplayCeiling: %v", err)
	}
	got, ok, err := st.GetDisplayCeiling(ctx)
	if err != nil || !ok {
		t.Fatalf("GetDisplayCeiling = ok %v, err %v", ok, err)
	}
	if got.TargetPercent != 60 || !got.FadeEndsAt.Equal(first.FadeEndsAt) || !got.ChangedAt.Equal(changed) ||
		got.ChangedByKind != "principal" || got.ChangedBy != "p1" || got.ChangedByName != "operator-1" {
		t.Fatalf("read back %+v, want %+v", got, first)
	}

	second := DisplayCeilingRecord{TargetPercent: 0, ChangedAt: changed.Add(time.Minute), ChangedByKind: "fpp", ChangedBy: "fpp-a"}
	if err := st.SetDisplayCeiling(ctx, second); err != nil {
		t.Fatalf("SetDisplayCeiling (overwrite): %v", err)
	}
	got, _, _ = st.GetDisplayCeiling(ctx)
	if got.TargetPercent != 0 || !got.FadeEndsAt.IsZero() || got.ChangedBy != "fpp-a" || got.ChangedByName != "" {
		t.Fatalf("after overwrite read back %+v, want %+v", got, second)
	}

	if err := st.SetDisplayCeiling(ctx, DisplayCeilingRecord{TargetPercent: 101, ChangedAt: changed}); err == nil {
		t.Fatal("a target of 101 was stored; want the CHECK to refuse it")
	}
}
