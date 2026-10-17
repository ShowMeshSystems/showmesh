package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestBedAudioCopyForOrder(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	createAssetWithMediaType(t, st, "halloween-2026", "every-day", store.AssetTargetKindNode, "audio-b", "audio", "sha256:b", "every-day.mp3")
	createAssetWithMediaType(t, st, "halloween-2026", "every-day", store.AssetTargetKindNode, "audio-c", "audio", "sha256:c", "every-day.mp3")
	createAssetWithMediaType(t, st, "halloween-2026", "every-day", store.AssetTargetKindNode, "audio-a", "fseq", "sha256:fseq", "every-day.fseq")

	cases := []struct {
		name, nodeID string
		targets      []string
		want         string
	}{
		{"own upload first", "audio-c", []string{"audio-b"}, "sha256:c"},
		{"listed target before any node", "audio-a", []string{"audio-a", "audio-c"}, "sha256:c"},
		{"any node, lowest id, ignoring the fseq", "audio-a", nil, "sha256:b"},
	}
	for _, tc := range cases {
		rec, ok, err := BedAudioCopyFor(ctx, st, "halloween-2026", "every-day", tc.nodeID, tc.targets)
		if err != nil || !ok || rec.ContentHash != tc.want {
			t.Errorf("%s: got %q ok=%v err=%v, want %q", tc.name, rec.ContentHash, ok, err, tc.want)
		}
	}

	createAssetWithMediaType(t, st, "halloween-2026", "every-day", store.AssetTargetKindShow, "", "audio", "sha256:show", "every-day.mp3")
	if rec, _, _ := BedAudioCopyFor(ctx, st, "halloween-2026", "every-day", "audio-a", []string{"audio-b"}); rec.ContentHash != "sha256:show" {
		t.Errorf("show-wide upload: got %q, want it ahead of another node's copy", rec.ContentHash)
	}
	if _, ok, _ := BedAudioCopyFor(ctx, st, "halloween-2026", "no-such-track", "audio-a", nil); ok {
		t.Error("missing sequence: want ok=false")
	}
}

func TestExpectedAssetsForNodeSendsShowWideAudioOnlyToAudioNodes(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "audio-01")
	declareNode(t, st, "render-01")
	putAudioNode(t, st, "audio-01")
	createAssetWithMediaType(t, st, "halloween-2026", "every-day", store.AssetTargetKindShow, "", "audio", "sha256:bed", "every-day.mp3")
	createAssetWithMediaType(t, st, "halloween-2026", "logo", store.AssetTargetKindShow, "", "media", "sha256:logo", "logo.png")

	audio, err := ExpectedAssetsForNode(ctx, st, "halloween-2026", "audio-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(audio.Assets) != 2 {
		t.Fatalf("audio node expects %+v, want the bed and the logo", audio.Assets)
	}
	render, err := ExpectedAssetsForNode(ctx, st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(render.Assets) != 1 || render.Assets[0].ContentHash != "sha256:logo" {
		t.Fatalf("render node expects %+v, want only the show-wide non-audio file", render.Assets)
	}
}
