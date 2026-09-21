package store

import (
	"context"
	"errors"
	"testing"
)

func seedAudioAsset(t *testing.T, st *Store, id, contentHash string, superseded bool) {
	t.Helper()
	rec, _, err := st.CreateAsset(context.Background(), AssetRecord{
		ID: id, ShowID: "show-1", SequenceID: "seq-" + id, TargetKind: AssetTargetKindShow, TargetID: "",
		MediaType: "audio", ContentHash: contentHash, RuntimeFilename: id + ".mp3", SizeBytes: 100,
		Backend: "volume", StorageKey: contentHash, CreatedByPrincipalID: "p1", CreatedByPrincipalName: "P1",
	})
	if err != nil {
		t.Fatalf("seed audio asset %q: %v", id, err)
	}
	if superseded {
		if _, _, err := st.CreateAsset(context.Background(), AssetRecord{
			ID: id + "-v2", ShowID: rec.ShowID, SequenceID: rec.SequenceID, TargetKind: rec.TargetKind, TargetID: rec.TargetID,
			MediaType: "audio", ContentHash: contentHash + "-v2", RuntimeFilename: id + ".mp3", SizeBytes: 100,
			Backend: "volume", StorageKey: contentHash + "-v2", CreatedByPrincipalID: "p1", CreatedByPrincipalName: "P1",
		}); err != nil {
			t.Fatalf("supersede audio asset %q: %v", id, err)
		}
	}
}

func TestGetAudioRenditionNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.GetAudioRendition(context.Background(), "sha256:nope"); !errors.Is(err, ErrAudioRenditionNotFound) {
		t.Fatalf("GetAudioRendition = %v, want ErrAudioRenditionNotFound", err)
	}
}

func TestAudioRenditionLifecycleRenderingReadyFailed(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	const hash = "sha256:original"

	if err := st.SetAudioRenditionRendering(ctx, hash); err != nil {
		t.Fatalf("SetAudioRenditionRendering: %v", err)
	}
	rec, err := st.GetAudioRendition(ctx, hash)
	if err != nil {
		t.Fatalf("GetAudioRendition after rendering: %v", err)
	}
	if rec.Status != AudioRenditionStatusRendering {
		t.Errorf("status = %q, want %q", rec.Status, AudioRenditionStatusRendering)
	}

	if err := st.SetAudioRenditionReady(ctx, hash, AudioRenditionReady{
		ContentHash: "sha256:wav", SizeBytes: 2048, DurationMillis: 1500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("SetAudioRenditionReady: %v", err)
	}
	rec, err = st.GetAudioRendition(ctx, hash)
	if err != nil {
		t.Fatalf("GetAudioRendition after ready: %v", err)
	}
	if rec.Status != AudioRenditionStatusReady || rec.ContentHash != "sha256:wav" || rec.SizeBytes != 2048 ||
		rec.DurationMillis != 1500 || rec.Format != "wav48k16s" {
		t.Errorf("ready rendition = %+v, want the values just written", rec)
	}

	if err := st.SetAudioRenditionFailed(ctx, hash, "decode error: bad frame"); err != nil {
		t.Fatalf("SetAudioRenditionFailed: %v", err)
	}
	rec, err = st.GetAudioRendition(ctx, hash)
	if err != nil {
		t.Fatalf("GetAudioRendition after failed: %v", err)
	}
	if rec.Status != AudioRenditionStatusFailed || rec.FailureReason != "decode error: bad frame" {
		t.Errorf("failed rendition = %+v, want status failed with the given reason", rec)
	}
}

// TestDeleteAudioRenditionRemovesRowAndIsIdempotent proves a rendition row
// deletes cleanly and a repeat delete against an already-gone row is a
// no-op, not an error - matching Backend.Delete's identical posture.
func TestDeleteAudioRenditionRemovesRowAndIsIdempotent(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()
	const hash = "sha256:original"

	if err := st.SetAudioRenditionReady(ctx, hash, AudioRenditionReady{
		ContentHash: "sha256:wav", SizeBytes: 2048, DurationMillis: 1500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("SetAudioRenditionReady: %v", err)
	}

	if err := st.DeleteAudioRendition(ctx, hash); err != nil {
		t.Fatalf("DeleteAudioRendition: %v", err)
	}
	if _, err := st.GetAudioRendition(ctx, hash); !errors.Is(err, ErrAudioRenditionNotFound) {
		t.Errorf("GetAudioRendition after delete = %v, want ErrAudioRenditionNotFound", err)
	}

	if err := st.DeleteAudioRendition(ctx, hash); err != nil {
		t.Errorf("second DeleteAudioRendition = %v, want nil (idempotent)", err)
	}
}

// TestListAudioAssetContentHashesNeedingRenditionSkipsReadyAndSuperseded
// proves the reconcile query only surfaces a current audio asset with no
// ready rendition, never a superseded one and never one already ready,
// this is the query [audiorendition.Service] polls to find work.
func TestListAudioAssetContentHashesNeedingRenditionSkipsReadyAndSuperseded(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	seedAudioAsset(t, st, "needs-work", "sha256:needs-work", false)
	seedAudioAsset(t, st, "already-ready", "sha256:already-ready", false)
	seedAudioAsset(t, st, "superseded-only", "sha256:superseded-only", true)

	if err := st.SetAudioRenditionReady(ctx, "sha256:already-ready", AudioRenditionReady{
		ContentHash: "sha256:already-ready-wav", SizeBytes: 10, DurationMillis: 10, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("seed ready rendition: %v", err)
	}

	got, err := st.ListAudioAssetContentHashesNeedingRendition(ctx)
	if err != nil {
		t.Fatalf("ListAudioAssetContentHashesNeedingRendition: %v", err)
	}

	want := map[string]bool{"sha256:needs-work": true, "sha256:superseded-only-v2": true}
	gotSet := make(map[string]bool, len(got))
	for _, h := range got {
		gotSet[h] = true
	}
	if len(gotSet) != len(want) {
		t.Fatalf("got hashes %v, want exactly %v", got, want)
	}
	for h := range want {
		if !gotSet[h] {
			t.Errorf("missing expected hash %q in %v", h, got)
		}
	}
	if gotSet["sha256:already-ready"] {
		t.Errorf("an asset with a ready rendition was listed as needing one: %v", got)
	}
	if gotSet["sha256:superseded-only"] {
		t.Errorf("a superseded content hash was listed as needing a rendition: %v", got)
	}
}
