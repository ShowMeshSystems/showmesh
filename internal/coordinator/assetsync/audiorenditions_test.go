package assetsync

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func TestRenditionFilenameReplacesExtension(t *testing.T) {
	cases := map[string]string{
		"holiday-theme.mp3": "holiday-theme.wav",
		"track.FLAC":        "track.wav",
		"no-extension":      "no-extension.wav",
		"a.b.ogg":           "a.b.wav",
	}
	for in, want := range cases {
		if got := RenditionFilename(in); got != want {
			t.Errorf("RenditionFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestExpectedAssetsForNodeNamesReadyAudioRendition proves the expected set
// names the rendition, not the original upload, once one is ready: hash,
// size, and a runtime filename with the extension replaced by .wav: what
// a node fetches and verifies against.
func TestExpectedAssetsForNodeNamesReadyAudioRendition(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "audio-01")

	createAssetWithMediaType(t, st, "halloween-2026", "opening-theme", store.AssetTargetKindShow, "", "audio", "sha256:original", "opening-theme.mp3")
	if err := st.SetAudioRenditionReady(context.Background(), "sha256:original", store.AudioRenditionReady{
		ContentHash: "sha256:rendition", SizeBytes: 999, DurationMillis: 12345, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("seed ready rendition: %v", err)
	}

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "audio-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	if len(got.Assets) != 1 {
		t.Fatalf("Assets = %+v, want exactly 1", got.Assets)
	}
	a := got.Assets[0]
	if a.ContentHash != "sha256:rendition" {
		t.Errorf("ContentHash = %q, want the rendition's own hash sha256:rendition", a.ContentHash)
	}
	if a.SizeBytes != 999 {
		t.Errorf("SizeBytes = %d, want the rendition's own size 999", a.SizeBytes)
	}
	if a.Filename != "opening-theme.wav" {
		t.Errorf("Filename = %q, want opening-theme.wav", a.Filename)
	}
	if !a.Rendition {
		t.Error("Rendition = false, want true: this entry names a ready rendition")
	}
}

// TestExpectedAssetsForNodeKeepsOriginalWithoutAReadyRendition proves an
// audio asset with no rendition row, a still-rendering one, or a failed
// one all keep naming the original upload: a running show's expected set
// must never name bytes that do not exist yet.
func TestExpectedAssetsForNodeKeepsOriginalWithoutAReadyRendition(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store, hash string)
	}{
		{"never queued", func(t *testing.T, st *store.Store, hash string) {}},
		{"still rendering", func(t *testing.T, st *store.Store, hash string) {
			if err := st.SetAudioRenditionRendering(context.Background(), hash); err != nil {
				t.Fatalf("SetAudioRenditionRendering: %v", err)
			}
		}},
		{"failed", func(t *testing.T, st *store.Store, hash string) {
			if err := st.SetAudioRenditionFailed(context.Background(), hash, "decode error"); err != nil {
				t.Fatalf("SetAudioRenditionFailed: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t)
			putShow(t, st, "halloween-2026", "Halloween 2026")
			putActiveShow(t, st, "halloween-2026")
			declareNode(t, st, "audio-01")

			createAssetWithMediaType(t, st, "halloween-2026", "opening-theme", store.AssetTargetKindShow, "", "audio", "sha256:original", "opening-theme.mp3")
			tc.setup(t, st, "sha256:original")

			got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "audio-01")
			if err != nil {
				t.Fatalf("ExpectedAssetsForNode() error = %v", err)
			}
			if len(got.Assets) != 1 {
				t.Fatalf("Assets = %+v, want exactly 1", got.Assets)
			}
			a := got.Assets[0]
			if a.ContentHash != "sha256:original" {
				t.Errorf("ContentHash = %q, want the original sha256:original", a.ContentHash)
			}
			if a.Filename != "opening-theme.mp3" {
				t.Errorf("Filename = %q, want the original opening-theme.mp3", a.Filename)
			}
			if a.Rendition {
				t.Error("Rendition = true, want false: no ready rendition exists yet")
			}
		})
	}
}

// TestResolveAudioMedia covers a ready rendition (resolved hash/filename/
// size), a pending (rendering or failed) rendition, and no rendition row
// at all: only "ready" ever changes the original values.
func TestResolveAudioMedia(t *testing.T) {
	const assetID = "opening-theme"
	const originalHash = "sha256:original"
	const originalFilename = "opening-theme.mp3"
	const originalSize = int64(12345)

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
		want  ResolvedAudioMedia
	}{
		{
			name:  "no rendition ever queued",
			setup: func(t *testing.T, st *store.Store) {},
			want:  ResolvedAudioMedia{ContentHash: originalHash, Filename: originalFilename, SizeBytes: originalSize},
		},
		{
			name: "rendition still rendering",
			setup: func(t *testing.T, st *store.Store) {
				if err := st.SetAudioRenditionRendering(context.Background(), originalHash); err != nil {
					t.Fatalf("SetAudioRenditionRendering: %v", err)
				}
			},
			want: ResolvedAudioMedia{ContentHash: originalHash, Filename: originalFilename, SizeBytes: originalSize},
		},
		{
			name: "rendition failed",
			setup: func(t *testing.T, st *store.Store) {
				if err := st.SetAudioRenditionFailed(context.Background(), originalHash, "decode error"); err != nil {
					t.Fatalf("SetAudioRenditionFailed: %v", err)
				}
			},
			want: ResolvedAudioMedia{ContentHash: originalHash, Filename: originalFilename, SizeBytes: originalSize},
		},
		{
			name: "rendition ready",
			setup: func(t *testing.T, st *store.Store) {
				if err := st.SetAudioRenditionReady(context.Background(), originalHash, store.AudioRenditionReady{
					ContentHash: "sha256:rendition", SizeBytes: 999, DurationMillis: 12345, Format: "wav48k16s",
				}); err != nil {
					t.Fatalf("SetAudioRenditionReady: %v", err)
				}
			},
			want: ResolvedAudioMedia{ContentHash: "sha256:rendition", Filename: "opening-theme.wav", SizeBytes: 999},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t)
			tc.setup(t, st)

			got, err := ResolveAudioMedia(context.Background(), st, assetID, originalHash, originalFilename, originalSize)
			if err != nil {
				t.Fatalf("ResolveAudioMedia() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveAudioMedia() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestExpectedAssetsForNodeNeverSubstitutesSequenceAssets proves an fseq
// entry is never rewritten, even if a row with its own content hash
// somehow existed in audio_renditions.
func TestExpectedAssetsForNodeNeverSubstitutesSequenceAssets(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putActiveShow(t, st, "halloween-2026")
	declareNode(t, st, "render-01")

	createAsset(t, st, "halloween-2026", "opening", store.AssetTargetKindShow, "", "sha256:sequence", "opening.fseq")
	if err := st.SetAudioRenditionReady(context.Background(), "sha256:sequence", store.AudioRenditionReady{
		ContentHash: "sha256:should-not-apply", SizeBytes: 1, DurationMillis: 1, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("seed unrelated rendition row: %v", err)
	}

	got, err := ExpectedAssetsForNode(context.Background(), st, "halloween-2026", "render-01")
	if err != nil {
		t.Fatalf("ExpectedAssetsForNode() error = %v", err)
	}
	if len(got.Assets) != 1 || got.Assets[0].ContentHash != "sha256:sequence" || got.Assets[0].Filename != "opening.fseq" {
		t.Fatalf("Assets = %+v, want the fseq entry unchanged", got.Assets)
	}
}
