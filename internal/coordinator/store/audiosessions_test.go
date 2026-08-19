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

	got, err := st.GetAudioSession(ctx, "s1")
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
	first, err := st.GetAudioSession(ctx, "s1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	clock.advance(5 * time.Minute)
	if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "s1", NodeID: "node-a", DesiredJSON: `{"revised":true}`, Revision: 2}); err != nil {
		t.Fatalf("second put: %v", err)
	}
	second, err := st.GetAudioSession(ctx, "s1")
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

	got, err := st.GetAudioSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if got.Revision != 5 || got.DesiredJSON != `{"revision":5}` {
		t.Fatalf("a lower revision rewound the stored record: %+v", got)
	}
}

func TestAudioSessionGetNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.GetAudioSession(context.Background(), "nope"); !errors.Is(err, ErrAudioSessionNotFound) {
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

	if err := st.DeleteAudioSession(ctx, "s1"); err != nil {
		t.Fatalf("DeleteAudioSession: %v", err)
	}
	if _, err := st.GetAudioSession(ctx, "s1"); !errors.Is(err, ErrAudioSessionNotFound) {
		t.Fatalf("s1 still present after delete: %v", err)
	}
	// Deleting an already-absent record is not an error.
	if err := st.DeleteAudioSession(ctx, "s1"); err != nil {
		t.Fatalf("DeleteAudioSession on absent record: %v", err)
	}
}
