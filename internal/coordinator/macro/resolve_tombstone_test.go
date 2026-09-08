package macro

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestResolveActionFailsSafelyWhenActionIsTombstoned is the show.macro ->
// show.action edge of the tombstone delete design's referential-safety
// decision: this codebase has no pre-flight check for a macro step's
// action reference (unlike show.action's own targets and night.session's
// action bindings, both covered by existing readiness surfaces), so a
// dangling reference here must fail safely at the point it is actually
// used, not before. resolveAction is that point for a submitted macro run.
//
// A step naming a deleted action must return an error (wrapping
// store.ErrConfigObjectNotFound, the same outcome an action id that was
// never created produces), never panic and never silently resolve to a
// zero-value action.
func TestResolveActionFailsSafelyWhenActionIsTombstoned(t *testing.T) {
	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	putAction(t, st, "house-volume", fppAction("fpp-main", "setVolume", "none", map[string]any{"volume": int64(50)}))

	if _, err := st.TombstoneConfigObject(context.Background(), "show.action", "house-volume"); err != nil {
		t.Fatalf("tombstone show.action: %v", err)
	}

	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, nil)
	_, err := e.resolveAction(context.Background(), "house-volume")
	if err == nil {
		t.Fatal("resolveAction() error = nil, want a non-nil error for a tombstoned action")
	}
	if !errors.Is(err, store.ErrConfigObjectNotFound) {
		t.Errorf("resolveAction() error = %v, want it to wrap store.ErrConfigObjectNotFound", err)
	}
}
