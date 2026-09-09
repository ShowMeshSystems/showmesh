package currentrun

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCoordinatorNormalizesAndSortsZeroToManyRuns(t *testing.T) {
	reader := Coordinator{Read: func(context.Context, time.Time) (Snapshot, error) {
		return Snapshot{Runs: []Run{
			{ID: "audio-b", Runner: RunnerShowmeshAudio, Targets: []Target{{ID: "node-b"}}},
			{ID: "fpp-a", Runner: RunnerFPP},
			{ID: "audio-a", Runner: RunnerShowmeshAudio, Targets: []Target{{ID: "node-a"}}},
		}}, nil
	}}

	got, err := reader.Snapshot(context.Background(), time.Unix(100, 0))
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(got.Runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(got.Runs))
	}
	if got.Runs[0].Runner != RunnerFPP || got.Runs[1].ID != "audio-a" || got.Runs[2].ID != "audio-b" {
		t.Fatalf("runs not deterministically sorted: %#v", got.Runs)
	}
	for _, run := range got.Runs {
		if run.Targets == nil || run.Playback.Evidence == nil {
			t.Fatalf("run %q has nil collections: %#v", run.ID, run)
		}
		for _, target := range run.Targets {
			if target.Evidence == nil {
				t.Fatalf("target %q has nil evidence", target.ID)
			}
		}
	}
}

func TestCoordinatorPreservesAuthoritativeNextAndErrors(t *testing.T) {
	wantErr := errors.New("reader failed")
	reader := Coordinator{Read: func(context.Context, time.Time) (Snapshot, error) {
		return Snapshot{}, wantErr
	}}
	if _, err := reader.Snapshot(context.Background(), time.Time{}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}

	got, err := (Coordinator{Read: func(context.Context, time.Time) (Snapshot, error) {
		return Snapshot{Runs: []Run{{ID: "run", Next: &Next{ItemID: "next", ItemIndex: 1}}}}, nil
	}}).Snapshot(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got.Runs[0].Next == nil || got.Runs[0].Next.ItemID != "next" {
		t.Fatalf("authoritative next was not preserved: %#v", got.Runs[0].Next)
	}

	empty, err := (Coordinator{}).Snapshot(context.Background(), time.Time{})
	if err != nil || empty.Runs == nil || len(empty.Runs) != 0 {
		t.Fatalf("nil reader should return an explicit empty run set: %#v, %v", empty, err)
	}
}
