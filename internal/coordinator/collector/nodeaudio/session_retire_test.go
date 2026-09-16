package nodeaudio

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// openRealStore opens an ephemeral *store.Store: [SessionObservationDeleter]
// is wired against the real database, not a fake, because the point of
// these tests is that rows actually disappear.
func openRealStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func countAudioSessionRows(t *testing.T, st *store.Store) int {
	t.Helper()
	got, err := st.ListObservations(context.Background(), store.ObservationFilter{ResourceKind: observation.ResourceAudioSession})
	if err != nil {
		t.Fatalf("list observations: %v", err)
	}
	return len(got)
}

// TestSessionDropOutLeavesZeroRows proves a session present in one poll and
// absent from the next has every one of its observations removed, not
// merely restated as a single not_collected row.
func TestSessionDropOutLeavesZeroRows(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()
	audioStore := NewStore()
	c := New(audioStore, WithSessionDeleter(st))

	audioStore.Put("node-a", samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	}), time.Now())
	obs, _ := c.Poll(ctx)
	if err := st.ReplaceObservations(ctx, obs); err != nil {
		t.Fatalf("replace observations: %v", err)
	}
	if got := countAudioSessionRows(t, st); got == 0 {
		t.Fatalf("expected sess-1's rows to be persisted before it ends, got 0")
	}

	audioStore.Put("node-a", samplePayload(), time.Now())
	obs, _ = c.Poll(ctx)
	if err := st.ReplaceObservations(ctx, obs); err != nil {
		t.Fatalf("replace observations after session end: %v", err)
	}

	if got := countAudioSessionRows(t, st); got != 0 {
		t.Errorf("audio_session rows after sess-1 ended = %d, want 0", got)
	}
}

// TestLiveSessionKeepsAllObservations proves a session that stays present
// across polls is never touched by the retire-on-dropout path.
func TestLiveSessionKeepsAllObservations(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()
	audioStore := NewStore()
	c := New(audioStore, WithSessionDeleter(st))

	payload := samplePayloadWithSession(mqttproto.AudioSessionReport{
		SessionID: "sess-1", State: "playing", Fault: "none",
	})
	audioStore.Put("node-a", payload, time.Now())
	obs, _ := c.Poll(ctx)
	if err := st.ReplaceObservations(ctx, obs); err != nil {
		t.Fatalf("replace observations: %v", err)
	}
	firstCount := countAudioSessionRows(t, st)
	if firstCount == 0 {
		t.Fatalf("expected sess-1's rows to be persisted, got 0")
	}

	audioStore.Put("node-a", payload, time.Now())
	obs, _ = c.Poll(ctx)
	if err := st.ReplaceObservations(ctx, obs); err != nil {
		t.Fatalf("replace observations on second poll: %v", err)
	}

	if got := countAudioSessionRows(t, st); got != firstCount {
		t.Errorf("audio_session rows for a still-live session = %d, want %d unchanged", got, firstCount)
	}
}

// TestOrphanSweepRemovesPreExistingStrandedRows proves the sweep clears
// audio_session rows that predate this feature entirely (an old
// "night-bg:" session that ended before drop-out tracking ever saw it
// present), never having transitioned present to absent under this
// collector's own memory.
func TestOrphanSweepRemovesPreExistingStrandedRows(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()

	orphan, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceAudioSession, ID: "night-bg:2026-01-01"},
		SignalSessionState, "playing", time.Now(),
		observation.WithSource(SourceForSession("node-a", "night-bg:2026-01-01")),
	)
	if err != nil {
		t.Fatalf("build orphan observation: %v", err)
	}
	if err := st.UpsertObservation(ctx, orphan); err != nil {
		t.Fatalf("seed orphan observation: %v", err)
	}
	if got := countAudioSessionRows(t, st); got != 1 {
		t.Fatalf("seeded orphan rows = %d, want 1", got)
	}

	audioStore := NewStore() // no node has ever reported this session
	c := New(audioStore, WithSessionDeleter(st))
	if _, complete := c.Poll(ctx); !complete {
		t.Fatalf("Poll must always report complete=true")
	}

	if got := countAudioSessionRows(t, st); got != 0 {
		t.Errorf("audio_session rows after sweep = %d, want 0", got)
	}
}

// TestRepeatedSessionCyclesKeepRowCountBounded proves the audio_session row
// count does not grow per ended session across many create/end cycles: this
// is the defect the coordinator's real rig hit (693 rows from 23 dead
// sessions).
func TestRepeatedSessionCyclesKeepRowCountBounded(t *testing.T) {
	st := openRealStore(t)
	ctx := context.Background()
	audioStore := NewStore()
	c := New(audioStore, WithSessionDeleter(st))

	for i := 0; i < 10; i++ {
		sessionID := fmt.Sprintf("night-bg:cycle-%d", i)
		audioStore.Put("node-a", samplePayloadWithSession(mqttproto.AudioSessionReport{
			SessionID: sessionID, State: "playing", Fault: "none",
		}), time.Now())
		obs, _ := c.Poll(ctx)
		if err := st.ReplaceObservations(ctx, obs); err != nil {
			t.Fatalf("cycle %d: replace observations while live: %v", i, err)
		}

		audioStore.Put("node-a", samplePayload(), time.Now())
		obs, _ = c.Poll(ctx)
		if err := st.ReplaceObservations(ctx, obs); err != nil {
			t.Fatalf("cycle %d: replace observations after end: %v", i, err)
		}

		if got := countAudioSessionRows(t, st); got != 0 {
			t.Fatalf("cycle %d: audio_session rows after end = %d, want 0 (unbounded growth)", i, got)
		}
	}
}
