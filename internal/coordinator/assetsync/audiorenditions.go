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
		rend, err := st.GetAudioRendition(ctx, a.ContentHash)
		if errors.Is(err, store.ErrAudioRenditionNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("assetsync: substitute audio rendition for asset %q: %w", a.AssetID, err)
		}
		if rend.Status != store.AudioRenditionStatusReady {
			continue
		}
		assets[i].ContentHash = rend.ContentHash
		assets[i].SizeBytes = rend.SizeBytes
		assets[i].Filename = renditionFilename(a.Filename)
	}
	return assets, nil
}

// renditionFilename replaces original's own extension with ".wav",
// matching every audio rendition's fixed [audiorendition.RenditionFormat].
func renditionFilename(original string) string {
	ext := filepath.Ext(original)
	if ext == "" {
		return original + ".wav"
	}
	return strings.TrimSuffix(original, ext) + ".wav"
}
