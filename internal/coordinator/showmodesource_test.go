package coordinator

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// seedShowModeRevision writes mode as revision rev of show.mode and
// activates it, mirroring what a PUT does to the store.
func seedShowModeRevision(t *testing.T, st *store.Store, rev int64, mode string) {
	t.Helper()
	payload, err := config.EncodeShowModePayload(config.ShowModePayload{Mode: mode})
	if err != nil {
		t.Fatalf("EncodeShowModePayload: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.ShowModeConfigKind, ObjectID: config.ShowModeConfigObjectID,
		Revision: rev, PayloadJSON: payload, Source: config.ShowModeSourceAPI,
	}); err != nil {
		t.Fatalf("seed show.mode: create revision %d: %v", rev, err)
	}
	if _, err := st.ActivateConfigRevision(context.Background(), config.ShowModeConfigKind, config.ShowModeConfigObjectID, rev); err != nil {
		t.Fatalf("seed show.mode: activate revision %d: %v", rev, err)
	}
}

// Nothing ever written is the fresh-install default, program, at revision 0
// and NOT stale. A fresh install is by definition being set up.
func TestShowModeSourceUnconfiguredIsProgramAndNotStale(t *testing.T) {
	st := openTestStore(t)
	src := newShowModeSource(st, discardLogger())

	v := src.Current(context.Background())
	if v.Mode != config.ShowModeProgram {
		t.Fatalf("Mode = %q, want program", v.Mode)
	}
	if v.Revision != 0 {
		t.Fatalf("Revision = %d, want 0", v.Revision)
	}
	if v.Stale {
		t.Fatal("an unconfigured mode is a known answer, never a stale one")
	}
}

// ADR-036 decision 1: the value follows the active revision live, with no
// process restart and in both directions.
func TestShowModeSourceFollowsTheActiveRevisionLive(t *testing.T) {
	st := openTestStore(t)
	src := newShowModeSource(st, discardLogger())
	ctx := context.Background()

	seedShowModeRevision(t, st, 1, config.ShowModeShow)
	if v := src.Current(ctx); v.Mode != config.ShowModeShow || v.Revision != 1 {
		t.Fatalf("after revision 1: %+v, want show at revision 1", v)
	}

	seedShowModeRevision(t, st, 2, config.ShowModeProgram)
	if v := src.Current(ctx); v.Mode != config.ShowModeProgram || v.Revision != 2 {
		t.Fatalf("after revision 2: %+v, want program at revision 2", v)
	}

	seedShowModeRevision(t, st, 3, config.ShowModeShow)
	if v := src.Current(ctx); v.Mode != config.ShowModeShow || v.Revision != 3 {
		t.Fatalf("after revision 3: %+v, want show at revision 3", v)
	}
}

// A store that cannot be read returns the LAST KNOWN value and says so,
// never a manufactured one. ADR-036 decision 4 in a subsystem where
// manufacturing an answer would change live behaviour rather than empty a
// list.
func TestShowModeSourceReturnsTheLastKnownValueOnAReadFailure(t *testing.T) {
	st := openTestStore(t)
	src := newShowModeSource(st, discardLogger())
	ctx := context.Background()

	seedShowModeRevision(t, st, 1, config.ShowModeShow)
	if v := src.Current(ctx); v.Mode != config.ShowModeShow || v.Stale {
		t.Fatalf("before the failure: %+v", v)
	}

	// Closing the store is this package's available way to make every
	// subsequent read fail the way a real store fault would.
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	v := src.Current(ctx)
	if v.Mode != config.ShowModeShow {
		t.Fatalf("Mode = %q after a read failure, want the last known value (show)", v.Mode)
	}
	if !v.Stale {
		t.Fatal("a value returned from a failed read must be reported stale")
	}
	if v.Revision != 1 {
		t.Fatalf("Revision = %d, want the last known revision", v.Revision)
	}
}

// With NO successful read to fall back on, the answer is show, the
// conservative side, and never the fresh-install default. Those are two
// different questions and this build must keep them apart: "nothing has
// ever been set" is program, "this coordinator cannot read its own store"
// is ADR-033 decision 5's case.
func TestShowModeSourceWithNoSuccessfulReadFallsBackToShowNotProgram(t *testing.T) {
	st := openTestStore(t)
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	src := newShowModeSource(st, discardLogger())

	v := src.Current(context.Background())
	if v.Mode != config.ShowModeShow {
		t.Fatalf("Mode = %q, want show: a coordinator with no readable store must not assert program", v.Mode)
	}
	if !v.Stale {
		t.Fatal("want the answer marked stale")
	}
}
