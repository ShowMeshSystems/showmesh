package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
)

func TestParseAssignmentAuthAbsentIsNilNotZero(t *testing.T) {
	auth, err := parseAssignmentAuth("render.surface.apply", map[string]any{"surfaceId": "s1"})
	if err != nil {
		t.Fatalf("parseAssignmentAuth with no tuple keys: %v", err)
	}
	if auth != nil {
		t.Fatalf("parseAssignmentAuth returned %+v for params with no tuple keys, want nil", auth)
	}
}

func TestParseAssignmentAuthComplete(t *testing.T) {
	params := map[string]any{
		"surfaceId": "s1", "show": "halloween-2026", "generation": float64(3), "catalogRevision": "rev-a",
	}
	auth, err := parseAssignmentAuth("render.surface.apply", params)
	if err != nil {
		t.Fatalf("parseAssignmentAuth: %v", err)
	}
	if auth == nil {
		t.Fatalf("parseAssignmentAuth returned nil for a complete tuple")
	}
	want := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"}
	if *auth != want {
		t.Fatalf("parseAssignmentAuth = %+v, want %+v", *auth, want)
	}
}

func TestParseAssignmentAuthPartialIsRefused(t *testing.T) {
	cases := []map[string]any{
		{"surfaceId": "s1", "generation": float64(3), "catalogRevision": "rev-a"},
		{"surfaceId": "s1", "show": "halloween-2026", "catalogRevision": "rev-a"},
		{"surfaceId": "s1", "show": "halloween-2026", "generation": float64(3)},
	}
	for _, params := range cases {
		if _, err := parseAssignmentAuth("render.surface.apply", params); err == nil {
			t.Fatalf("parseAssignmentAuth accepted a partial tuple: %v", params)
		}
	}
}

// TestApplySurfacePersistsAuthorizationTuple proves render.surface.apply
// carries an optional authorization tuple (per TRACK-H-H3-SPEC.md section
// 6's ruling that cuecatalog.deploy's params carry it "exactly as
// render.surface.apply carries its parameters") through to the persisted
// [pipeline.Assignment], for the boot-clearing check to read back later.
func TestApplySurfacePersistsAuthorizationTuple(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	result, err := renderOps.applySurface(context.Background(), map[string]any{
		"surfaceId": "surface-1", "show": "halloween-2026", "generation": float64(3), "catalogRevision": "rev-a",
	}, clock.now)
	if err != nil {
		t.Fatalf("applySurface: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("applySurface did not confirm")
	}

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 1 {
		t.Fatalf("persisted assignments = %+v, want one entry", reloaded)
	}
	if reloaded[0].Auth == nil {
		t.Fatalf("persisted assignment has no authorization tuple")
	}
	want := pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"}
	if *reloaded[0].Auth != want {
		t.Fatalf("persisted authorization tuple = %+v, want %+v", *reloaded[0].Auth, want)
	}
}

// TestApplySurfaceWithNoAuthorizationTupleIsAcceptedForLegacyCallers
// proves an apply carrying none of the three tuple keys is still accepted
// (a coordinator not yet sending H4's authorization tuple), and persists
// with Auth == nil rather than being refused outright.
func TestApplySurfaceWithNoAuthorizationTupleIsAcceptedForLegacyCallers(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)}
	sup := newRenderTestSupervisor(t, clock)
	store := pipeline.NewAssignmentStore(dir)
	renderOps := newTestRenderOperations(sup, store, dir, clock)

	if _, err := renderOps.applySurface(context.Background(), map[string]any{"surfaceId": "surface-1"}, clock.now); err != nil {
		t.Fatalf("applySurface: %v", err)
	}

	reloaded, err := pipeline.NewAssignmentStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Auth != nil {
		t.Fatalf("persisted assignments = %+v, want one entry with Auth == nil", reloaded)
	}
}
