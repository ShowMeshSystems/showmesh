package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// assertJSONEqual compares a and b by parsed value, not by exact bytes:
// json.MarshalIndent reformats every nested json.RawMessage to match its
// own indent style, so a stored RawParams's exact whitespace is not
// preserved across a Save/Load round trip — only its meaning is.
func assertJSONEqual(t *testing.T, a, b json.RawMessage) {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("RawParams mismatch:\n got: %s\nwant: %s", a, b)
	}
}

// TestAssignmentStoreLoadEmptyWhenAbsent proves a fresh node (no state file
// yet) reports an empty, non-nil slice rather than an error — a node that
// has never received a render.surface.apply must still boot cleanly.
func TestAssignmentStoreLoadEmptyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	store := NewAssignmentStore(dir)
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatalf("Load returned nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("Load returned %d entries, want 0", len(got))
	}
}

// TestAssignmentStoreUpsertThenLoadRoundTrips proves the exact requirement
// this seam is built for: an assignment applied once is still there after a
// fresh Load — modelling "the node restarts with no coordinator reachable
// and resumes from what it persisted" (build contract ruling 4).
func TestAssignmentStoreUpsertThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := NewAssignmentStore(dir)

	entry := Assignment{
		SurfaceID: "surface-1",
		RawParams: json.RawMessage(`{"surfaceId":"surface-1","output":{"transport":"ndi"}}`),
		AppliedAt: time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Upsert(entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// A fresh store instance (simulating a process restart) must read back
	// what the previous instance wrote.
	reloaded := NewAssignmentStore(dir)
	got, err := reloaded.Load()
	if err != nil {
		t.Fatalf("Load after Upsert: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load returned %d entries, want 1", len(got))
	}
	if got[0].SurfaceID != entry.SurfaceID {
		t.Fatalf("SurfaceID = %q, want %q", got[0].SurfaceID, entry.SurfaceID)
	}
	assertJSONEqual(t, got[0].RawParams, entry.RawParams)
	if !got[0].AppliedAt.Equal(entry.AppliedAt) {
		t.Fatalf("AppliedAt = %s, want %s", got[0].AppliedAt, entry.AppliedAt)
	}
}

// TestAssignmentStoreUpsertReplacesSameSurface proves a second apply for the
// same surface replaces its entry rather than accumulating duplicates.
func TestAssignmentStoreUpsertReplacesSameSurface(t *testing.T) {
	dir := t.TempDir()
	store := NewAssignmentStore(dir)

	if err := store.Upsert(Assignment{SurfaceID: "s1", RawParams: json.RawMessage(`{"v":1}`)}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if err := store.Upsert(Assignment{SurfaceID: "s1", RawParams: json.RawMessage(`{"v":2}`)}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Load returned %d entries, want 1 (second Upsert should replace, not append)", len(got))
	}
	assertJSONEqual(t, got[0].RawParams, json.RawMessage(`{"v":2}`))
}

// TestAssignmentStoreRemoveDeletesOnlyThatSurface proves render.surface.clear's
// use of Remove does not disturb ADR-026's N-surfaces-per-node case.
func TestAssignmentStoreRemoveDeletesOnlyThatSurface(t *testing.T) {
	dir := t.TempDir()
	store := NewAssignmentStore(dir)

	if err := store.Upsert(Assignment{SurfaceID: "s1", RawParams: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Upsert s1: %v", err)
	}
	if err := store.Upsert(Assignment{SurfaceID: "s2", RawParams: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Upsert s2: %v", err)
	}
	if err := store.Remove("s1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].SurfaceID != "s2" {
		t.Fatalf("Load after removing s1 = %+v, want only s2", got)
	}
}

// TestAssignmentStoreSaveIsAtomic proves Save never leaves a partially
// written file visible at the final path: it writes to a temp file first
// and renames, matching internal/agent/assets.go's stage-then-rename
// discipline. This test checks the mechanism directly (no .tmp-* file left
// behind after a successful Save) rather than trying to interrupt a write
// mid-flight.
func TestAssignmentStoreSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	store := NewAssignmentStore(dir)

	if err := store.Save([]Assignment{{SurfaceID: "s1", RawParams: json.RawMessage(`{}`)}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stateDir := filepath.Join(dir, assignmentStateDir)
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != assignmentFileName {
			t.Fatalf("unexpected leftover file %q in state dir after Save: temp file was not cleaned up via rename", e.Name())
		}
	}
}
