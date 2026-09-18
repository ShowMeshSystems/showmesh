package assetsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// substituteAudioRenditions rewrites every audio entry in assets to name
// its ready rendition (owner ruling 2026-09-18, PCM show audio) instead of
// the operator's original upload: the rendition's own content hash, size,
// and a runtime filename with the original's extension replaced by .wav,
// so a node fetches and verifies the 48kHz/16-bit/stereo WAV rather than
// the original file. A sequence (fseq) entry is never touched.
//
// An audio entry whose rendition has never been built, is still rendering,
// or last failed is left completely alone, still naming the original: a
// running show's expected set must never name bytes that do not exist yet,
// and an existing audio asset with no rendition (uploaded before this
// feature shipped) keeps playing exactly as it does today until the
// coordinator's own reconcile pass catches up to it.
func substituteAudioRenditions(ctx context.Context, st *store.Store, assets []ExpectedAsset) ([]ExpectedAsset, error) {
	for i, a := range assets {
		if a.MediaType != audioMediaType {
			continue
		}
		original := ResolvedAudioMedia{ContentHash: a.ContentHash, Filename: a.Filename, SizeBytes: a.SizeBytes}
		resolved, err := ResolveAudioMedia(ctx, st, a.AssetID, a.ContentHash, a.Filename, a.SizeBytes)
		if err != nil {
			return nil, fmt.Errorf("assetsync: substitute audio rendition for asset %q: %w", a.AssetID, err)
		}
		if resolved == original {
			continue
		}
		assets[i].ContentHash = resolved.ContentHash
		assets[i].SizeBytes = resolved.SizeBytes
		assets[i].Filename = resolved.Filename
		assets[i].Rendition = true
	}
	return assets, nil
}

// ResolvedAudioMedia is the {contentHash, filename, sizeBytes} shape a
// dispatch carries onto the wire for one audio asset.
type ResolvedAudioMedia struct {
	ContentHash string
	Filename    string
	SizeBytes   int64
}

// ResolveAudioMedia is [substituteAudioRenditions]'s own per-asset
// decision, exported for every other dispatch path naming an audio asset:
// a bed item, an announcement, an ad hoc show.action. Same lookup key
// (originalContentHash), so no caller can ever disagree with the manifest.
func ResolveAudioMedia(ctx context.Context, st *store.Store, assetID, originalContentHash, originalFilename string, originalSizeBytes int64) (ResolvedAudioMedia, error) {
	original := ResolvedAudioMedia{ContentHash: originalContentHash, Filename: originalFilename, SizeBytes: originalSizeBytes}
	rend, err := st.GetAudioRendition(ctx, originalContentHash)
	if errors.Is(err, store.ErrAudioRenditionNotFound) {
		return original, nil
	}
	if err != nil {
		return ResolvedAudioMedia{}, fmt.Errorf("assetsync: resolve audio media for asset %q: %w", assetID, err)
	}
	if rend.Status != store.AudioRenditionStatusReady {
		return original, nil
	}
	return ResolvedAudioMedia{ContentHash: rend.ContentHash, Filename: RenditionFilename(originalFilename), SizeBytes: rend.SizeBytes}, nil
}

// RenditionFilename replaces original's own extension with ".wav",
// matching every audio rendition's fixed [audiorendition.RenditionFormat].
// Exported so the API package's rendition content route (assets.go) names
// the same runtime filename this package's own expected set names.
func RenditionFilename(original string) string {
	ext := filepath.Ext(original)
	if ext == "" {
		return original + ".wav"
	}
	return strings.TrimSuffix(original, ext) + ".wav"
}
