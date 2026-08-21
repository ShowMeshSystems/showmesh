package api

import (
	"bytes"
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// Track F seam F3 tests for nightResolveFSEQDuration — seam spec rule 1
// and F0 §1's own finding that FrameCount()==0 and StepTimeMS()==0 both
// collapse to duration_ms==0 and must be distinguished at the point
// duration is read.

type fakeAssetLister struct {
	recs []store.AssetRecord
	err  error
}

func (f *fakeAssetLister) ListAssets(_ context.Context, filter store.AssetFilter) ([]store.AssetRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []store.AssetRecord
	for _, r := range f.recs {
		if filter.ShowID != "" && r.ShowID != filter.ShowID {
			continue
		}
		if filter.SequenceID != "" && r.SequenceID != filter.SequenceID {
			continue
		}
		if filter.NodeID != "" && r.TargetID != filter.NodeID {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// putFSEQAsset stages fseq bytes into backend and returns an AssetRecord
// naming them, ready to hand to a fakeAssetLister.
func putFSEQAsset(t *testing.T, backend assetstore.Backend, show, sequence, target string, raw []byte) store.AssetRecord {
	t.Helper()
	blob, err := backend.Put(context.Background(), bytes.NewReader(raw), int64(len(raw))+1)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return store.AssetRecord{
		ID: "asset-" + target, ShowID: show, SequenceID: sequence,
		TargetKind: store.AssetTargetKindNode, TargetID: target,
		MediaType: "fseq", ContentHash: blob.ContentHash,
		RuntimeFilename: target + ".fseq", SizeBytes: blob.SizeBytes,
		Backend: "volume", StorageKey: blob.ContentHash,
	}
}

func nightAssetTestDeps(t *testing.T, lister *fakeAssetLister) (Dependencies, assetstore.Backend) {
	t.Helper()
	backend, err := assetstore.NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewVolumeBackend: %v", err)
	}
	return Dependencies{AssetBackend: backend}, backend
}

func TestNightResolveFSEQDuration_NormalCase(t *testing.T) {
	lister := &fakeAssetLister{}
	deps, backend := nightAssetTestDeps(t, lister)
	raw := fseqtest.Build(4, 6000, 50) // 6000 frames * 50ms = 300000ms, F0's own worked example.
	lister.recs = []store.AssetRecord{putFSEQAsset(t, backend, "halloween-2026", "resting", "node-1", raw)}

	res := nightResolveFSEQDuration(context.Background(), deps, lister, "halloween-2026", config.NightSessionAssetRef{Sequence: "resting", Target: "node-1"})
	if res.Reason != "" {
		t.Fatalf("unexpected failure reason: %s", res.Reason)
	}
	if res.DurationMS != 300000 {
		t.Fatalf("DurationMS = %d, want 300000", res.DurationMS)
	}
}

// F0 §1: FrameCount()==0 and StepTimeMS()==0 are indistinguishable by the
// arithmetic alone and must be told apart in the failure reason.
func TestNightResolveFSEQDuration_ZeroFrameCount(t *testing.T) {
	lister := &fakeAssetLister{}
	deps, backend := nightAssetTestDeps(t, lister)
	raw := fseqtest.Build(4, 0, 50)
	lister.recs = []store.AssetRecord{putFSEQAsset(t, backend, "s", "seq", "node-1", raw)}

	res := nightResolveFSEQDuration(context.Background(), deps, lister, "s", config.NightSessionAssetRef{Sequence: "seq", Target: "node-1"})
	if res.DurationMS != 0 {
		t.Fatalf("DurationMS = %d, want 0", res.DurationMS)
	}
	if res.Reason == "" {
		t.Fatal("expected a failure reason")
	}
	if !nightContainsAll(res.Reason, "frame count") {
		t.Fatalf("reason %q does not name the frame count as the cause", res.Reason)
	}
}

func TestNightResolveFSEQDuration_ZeroStepTime(t *testing.T) {
	lister := &fakeAssetLister{}
	deps, backend := nightAssetTestDeps(t, lister)
	raw := fseqtest.Build(4, 100, 0)
	lister.recs = []store.AssetRecord{putFSEQAsset(t, backend, "s", "seq", "node-1", raw)}

	res := nightResolveFSEQDuration(context.Background(), deps, lister, "s", config.NightSessionAssetRef{Sequence: "seq", Target: "node-1"})
	if res.DurationMS != 0 {
		t.Fatalf("DurationMS = %d, want 0", res.DurationMS)
	}
	if !nightContainsAll(res.Reason, "step time") {
		t.Fatalf("reason %q does not name the step time as the cause", res.Reason)
	}
}

func TestNightResolveFSEQDuration_BothZero(t *testing.T) {
	lister := &fakeAssetLister{}
	deps, backend := nightAssetTestDeps(t, lister)
	raw := fseqtest.Build(4, 0, 0)
	lister.recs = []store.AssetRecord{putFSEQAsset(t, backend, "s", "seq", "node-1", raw)}

	res := nightResolveFSEQDuration(context.Background(), deps, lister, "s", config.NightSessionAssetRef{Sequence: "seq", Target: "node-1"})
	if !nightContainsAll(res.Reason, "frame count", "step time") {
		t.Fatalf("reason %q does not name BOTH causes", res.Reason)
	}
}

func TestNightResolveFSEQDuration_NoCurrentAsset(t *testing.T) {
	lister := &fakeAssetLister{}
	deps, _ := nightAssetTestDeps(t, lister)
	res := nightResolveFSEQDuration(context.Background(), deps, lister, "s", config.NightSessionAssetRef{Sequence: "seq", Target: "node-1"})
	if res.DurationMS != 0 || res.Reason == "" {
		t.Fatalf("expected a failure reason and zero duration, got %+v", res)
	}
}

func nightContainsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !bytes.Contains([]byte(s), []byte(sub)) {
			return false
		}
	}
	return true
}
