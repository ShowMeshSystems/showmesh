package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAudioSessionPutGetRoundTrip(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	st := openTestStore(t, clock)
	ctx := context.Background()

	rec := AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{"sourceRole":"show"}`, Revision: 1}
	if err := st.PutAudioSession(ctx, rec); err != nil {
		t.Fatalf("PutAudioSession: %v", err)
	}

	got, err := st.GetAudioSession(ctx, "node-a", "s1")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if got.NodeID != "node-a" || got.DesiredJSON != rec.DesiredJSON || got.Revision != 1 {
		t.Fatalf("got %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("CreatedAt/UpdatedAt not set")
	}
}

func TestAudioSessionPutIsUpsert(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	st := openTestStore(t, clock)
	ctx := context.Background()

	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{}`, Revision: 1}); err != nil {
		t.Fatalf("first put: %v", err)
	}
	first, err := st.GetAudioSession(ctx, "node-a", "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	clock.advance(5 * time.Minute)
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{"revised":true}`, Revision: 2}); err != nil {
		t.Fatalf("second put: %v", err)
	}
	second, err := st.GetAudioSession(ctx, "node-a", "s1")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if second.Revision != 2 || second.DesiredJSON != `{"revised":true}` {
		t.Fatalf("upsert did not apply: %+v", second)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("CreatedAt changed on upsert: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("UpdatedAt did not advance on upsert: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
}

// TestAudioSessionPutRefusesToRewindRevision proves PutAudioSession's own
// SQL-layer guard: a write naming a revision no greater than the stored
// row's current revision must not overwrite it, matching
// pkg/audio.RevisionState's identical anti-rewind rule one layer down.
func TestAudioSessionPutRefusesToRewindRevision(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	st := openTestStore(t, clock)
	ctx := context.Background()

	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{"revision":5}`, Revision: 5}); err != nil {
		t.Fatalf("put revision 5: %v", err)
	}
	clock.advance(time.Minute)
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{"revision":3}`, Revision: 3}); err != nil {
		t.Fatalf("put revision 3: %v", err)
	}

	got, err := st.GetAudioSession(ctx, "node-a", "s1")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if got.Revision != 5 || got.DesiredJSON != `{"revision":5}` {
		t.Fatalf("a lower revision rewound the stored record: %+v", got)
	}
}

// TestAudioSessionSameIDDifferentNodesAreIndependent is this package's own
// acceptance property: audio_sessions is keyed by (node_id, id), not id
// alone, because a session id such as the cue or blackAndSilence session
// is a global constant shared by every node, not unique to one. Two nodes
// dispatching the SAME session id must keep two fully independent rows —
// independent desired state and an independent revision counter each —
// so node B's write can never be silently dropped by node A's revision
// guard, and vice versa.
func TestAudioSessionSameIDDifferentNodesAreIndependent(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}
	st := openTestStore(t, clock)
	ctx := context.Background()

	const sharedID = "cue"

	if err := st.PutAudioSession(ctx, AudioSessionRecord{
		ID: sharedID, NodeID: "node-a", DesiredJSON: `{"sourceRole":"show","node":"a"}`, Revision: 7,
	}); err != nil {
		t.Fatalf("put node-a: %v", err)
	}
	if err := st.PutAudioSession(ctx, AudioSessionRecord{
		ID: sharedID, NodeID: "node-b", DesiredJSON: `{"sourceRole":"show","node":"b"}`, Revision: 3,
	}); err != nil {
		t.Fatalf("put node-b: %v", err)
	}

	a, err := st.GetAudioSession(ctx, "node-a", sharedID)
	if err != nil {
		t.Fatalf("get node-a: %v", err)
	}
	b, err := st.GetAudioSession(ctx, "node-b", sharedID)
	if err != nil {
		t.Fatalf("get node-b: %v", err)
	}
	if a.Revision != 7 || a.DesiredJSON != `{"sourceRole":"show","node":"a"}` {
		t.Fatalf("node-a record clobbered by node-b's write: %+v", a)
	}
	if b.Revision != 3 || b.DesiredJSON != `{"sourceRole":"show","node":"b"}` {
		t.Fatalf("node-b record clobbered by node-a's write: %+v", b)
	}

	// Node A advances to revision 8: node B's row (still at 3) must be
	// untouched, and node A's lower-than-B-would-be write must not be
	// refused by a revision guard that conflates the two nodes' counters.
	clock.advance(time.Minute)
	if err := st.PutAudioSession(ctx, AudioSessionRecord{
		ID: sharedID, NodeID: "node-a", DesiredJSON: `{"sourceRole":"show","node":"a","rev":8}`, Revision: 8,
	}); err != nil {
		t.Fatalf("advance node-a: %v", err)
	}
	// Node B advances independently too, to a revision still below node
	// A's current one — proving the two revision counters are not shared.
	if err := st.PutAudioSession(ctx, AudioSessionRecord{
		ID: sharedID, NodeID: "node-b", DesiredJSON: `{"sourceRole":"show","node":"b","rev":4}`, Revision: 4,
	}); err != nil {
		t.Fatalf("advance node-b: %v", err)
	}

	a, err = st.GetAudioSession(ctx, "node-a", sharedID)
	if err != nil {
		t.Fatalf("get node-a after advance: %v", err)
	}
	b, err = st.GetAudioSession(ctx, "node-b", sharedID)
	if err != nil {
		t.Fatalf("get node-b after advance: %v", err)
	}
	if a.Revision != 8 || a.DesiredJSON != `{"sourceRole":"show","node":"a","rev":8}` {
		t.Fatalf("node-a write dropped or clobbered: %+v", a)
	}
	if b.Revision != 4 || b.DesiredJSON != `{"sourceRole":"show","node":"b","rev":4}` {
		t.Fatalf("node-b write dropped or clobbered: %+v", b)
	}

	list, err := st.ListAudioSessionsByNode(ctx, "node-a")
	if err != nil {
		t.Fatalf("ListAudioSessionsByNode node-a: %v", err)
	}
	if len(list) != 1 || list[0].NodeID != "node-a" {
		t.Fatalf("ListAudioSessionsByNode node-a = %+v, want exactly node-a's own row", list)
	}

	if err := st.DeleteAudioSession(ctx, "node-a", sharedID); err != nil {
		t.Fatalf("delete node-a: %v", err)
	}
	if _, err := st.GetAudioSession(ctx, "node-a", sharedID); !errors.Is(err, ErrAudioSessionNotFound) {
		t.Fatalf("node-a row still present after its own delete: %v", err)
	}
	// Deleting node A's row must never touch node B's row sharing the same id.
	stillB, err := st.GetAudioSession(ctx, "node-b", sharedID)
	if err != nil {
		t.Fatalf("node-b row disappeared after node-a's delete: %v", err)
	}
	if stillB.Revision != 4 {
		t.Fatalf("node-b row mutated by node-a's delete: %+v", stillB)
	}
}

func TestAudioSessionGetNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.GetAudioSession(context.Background(), "node-a", "nope"); !errors.Is(err, ErrAudioSessionNotFound) {
		t.Fatalf("err = %v, want ErrAudioSessionNotFound", err)
	}
}

func TestAudioSessionListByNodeAndDelete(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{}`, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s2", NodeID: "node-a", DesiredJSON: `{}`, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s3", NodeID: "node-b", DesiredJSON: `{}`, Revision: 1}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListAudioSessionsByNode(ctx, "node-a")
	if err != nil {
		t.Fatalf("ListAudioSessionsByNode: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	if err := st.DeleteAudioSession(ctx, "node-a", "s1"); err != nil {
		t.Fatalf("DeleteAudioSession: %v", err)
	}
	if _, err := st.GetAudioSession(ctx, "node-a", "s1"); !errors.Is(err, ErrAudioSessionNotFound) {
		t.Fatalf("s1 still present after delete: %v", err)
	}
	// Deleting an already-absent record is not an error.
	if err := st.DeleteAudioSession(ctx, "node-a", "s1"); err != nil {
		t.Fatalf("DeleteAudioSession on absent record: %v", err)
	}
}
