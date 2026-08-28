package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func withAudioProbeItems(t *testing.T, fn func(ctx context.Context, dir string, items []pkgaudio.PlaylistItem) audio.ReadinessReport) {
	t.Helper()
	orig := audioProbeItems
	audioProbeItems = fn
	t.Cleanup(func() { audioProbeItems = orig })
}

func readyItem(itemID string, index int, assetID string) audio.MediaProbeItem {
	return audio.MediaProbeItem{
		ItemID: itemID, Index: index, AssetID: assetID,
		MediaItemResult: audio.MediaItemResult{
			State: audio.MediaReady, Duration: 3 * time.Second,
			Container: "audio/x-wav", Codec: "WavParse", Channels: 2, SampleRate: 44100,
		},
	}
}

// TestMediaProbeSingleAssetWiresAssetIDContentHashFilenameSizeBytes proves
// the single-MediaRef request shape reaches audioProbeItems as a one-item
// playlist with every field carried through.
func TestMediaProbeSingleAssetWiresAssetIDContentHashFilenameSizeBytes(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	var gotDir string
	var gotItems []pkgaudio.PlaylistItem
	withAudioProbeItems(t, func(ctx context.Context, dir string, items []pkgaudio.PlaylistItem) audio.ReadinessReport {
		gotDir, gotItems = dir, items
		return audio.ReadinessReport{State: audio.MediaReady, Items: []audio.MediaProbeItem{readyItem("show-1", 0, "show-1")}}
	})

	result, err := op.run(context.Background(), map[string]any{
		"assetId": "show-1", "contentHash": "sha256:abc", "filename": "show-1.wav", "sizeBytes": float64(1024),
	}, time.Now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotDir != op.dir {
		t.Errorf("dir = %q, want %q", gotDir, op.dir)
	}
	if len(gotItems) != 1 {
		t.Fatalf("items = %v, want exactly 1", gotItems)
	}
	got := gotItems[0]
	if got.Media.AssetID != "show-1" || got.Media.ContentHash != "sha256:abc" ||
		got.Media.RuntimeFilename != "show-1.wav" || got.Media.SizeBytes != 1024 {
		t.Errorf("item.Media = %+v, want the parsed request fields", got.Media)
	}
	if !result.Confirmed {
		t.Error("Confirmed = false, want true for a Ready report")
	}
}

// TestMediaProbePlaylistCoversEveryItemNeverOnlyTheFirst proves ruling 4:
// a multi-item playlist request reaches audioProbeItems with every item,
// in order, and a single faulted item among several ready ones still
// drives Confirmed=false for the whole request.
func TestMediaProbePlaylistCoversEveryItemNeverOnlyTheFirst(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	var gotItems []pkgaudio.PlaylistItem
	withAudioProbeItems(t, func(ctx context.Context, dir string, items []pkgaudio.PlaylistItem) audio.ReadinessReport {
		gotItems = items
		return audio.ReadinessReport{
			State:  audio.MediaFaulted,
			Reason: "1 of 3 item(s) not ready",
			Items: []audio.MediaProbeItem{
				readyItem("a", 0, "asset-a"),
				{ItemID: "b", Index: 1, AssetID: "asset-b", MediaItemResult: audio.MediaItemResult{
					State: audio.MediaFaulted, Fault: audio.MediaFaultMissing, Reason: "not present",
				}},
				readyItem("c", 2, "asset-c"),
			},
		}
	})

	result, err := op.run(context.Background(), map[string]any{
		"items": []any{
			map[string]any{"itemId": "a", "index": float64(0), "assetId": "asset-a", "contentHash": "sha256:a", "filename": "a.wav"},
			map[string]any{"itemId": "b", "index": float64(1), "assetId": "asset-b", "contentHash": "sha256:b", "filename": "b.wav"},
			map[string]any{"itemId": "c", "index": float64(2), "assetId": "asset-c", "contentHash": "sha256:c", "filename": "c.wav"},
		},
	}, time.Now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(gotItems) != 3 {
		t.Fatalf("items forwarded = %d, want 3", len(gotItems))
	}
	if result.Confirmed {
		t.Error("Confirmed = true, want false: one item faulted")
	}
	val := result.Value.(map[string]any)
	items := val["items"].([]map[string]any)
	if len(items) != 3 {
		t.Fatalf("evidence items = %d, want 3 — every item, not only the first", len(items))
	}
	if items[2]["state"] != string(audio.MediaReady) {
		t.Errorf("third item state = %v, want ready — it must still be reported even though item 2 faulted", items[2]["state"])
	}
}

// TestMediaProbeUnreadyIsNotAnOperationError proves a fault or an unknown
// probe outcome is genuine evidence, not an OperationFunc failure — see
// mediaProbeOperation.run's own doc comment.
func TestMediaProbeUnreadyIsNotAnOperationError(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	withAudioProbeItems(t, func(ctx context.Context, dir string, items []pkgaudio.PlaylistItem) audio.ReadinessReport {
		return audio.ReadinessReport{
			State: audio.MediaUnknown, Reason: "probe could not run",
			Items: []audio.MediaProbeItem{{ItemID: "x", MediaItemResult: audio.MediaItemResult{State: audio.MediaUnknown, Reason: "no gst-launch-1.0"}}},
		}
	})

	result, err := op.run(context.Background(), map[string]any{
		"assetId": "x", "contentHash": "sha256:x", "filename": "x.wav",
	}, time.Now)
	if err != nil {
		t.Fatalf("run: unexpected error %v", err)
	}
	if result.Confirmed {
		t.Error("Confirmed = true, want false for MediaUnknown")
	}
	val := result.Value.(map[string]any)
	if val["state"] != string(audio.MediaUnknown) {
		t.Errorf("state = %v, want %q", val["state"], audio.MediaUnknown)
	}
}

func TestMediaProbeRequiresAssetIDOrItems(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	_, err := op.run(context.Background(), map[string]any{}, time.Now)
	if err == nil {
		t.Fatal("run with empty params: got nil error, want one")
	}
}

func TestMediaProbeRejectsUnknownTopLevelKey(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	_, err := op.run(context.Background(), map[string]any{
		"assetId": "x", "contentHash": "sha256:x", "filename": "x.wav", "bogus": "y",
	}, time.Now)
	if err == nil {
		t.Fatal("run with an unrecognized key: got nil error, want one")
	}
}

func TestMediaProbeRejectsEmptyItems(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	_, err := op.run(context.Background(), map[string]any{"items": []any{}}, time.Now)
	if err == nil {
		t.Fatal("run with empty items array: got nil error, want one")
	}
}

func TestMediaProbeRejectsUnsafeFilename(t *testing.T) {
	op := mediaProbeOperation{dir: t.TempDir()}
	_, err := op.run(context.Background(), map[string]any{
		"assetId": "x", "contentHash": "sha256:x", "filename": "../escape.wav",
	}, time.Now)
	if err == nil {
		t.Fatal("run with a path-separator filename: got nil error, want one")
	}
}

// TestMediaProbeIsAllowlisted proves "audio.media.probe" is a real key in
// the agent's command allowlist — Step 6's own lesson, applied here.
func TestMediaProbeIsAllowlisted(t *testing.T) {
	ops := newOperationRegistry(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil)
	if _, ok := ops["audio.media.probe"]; !ok {
		t.Fatal(`newOperationRegistry() does not contain "audio.media.probe"`)
	}
}
