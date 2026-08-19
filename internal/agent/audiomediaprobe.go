package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// audioMediaProbeKnownKeys is the top-level key allowlist for
// "audio.media.probe": either a single item's identity fields directly, or
// an "items" array for a pinned playlist (ruling 4) — never both, since a
// caller mixing the two shapes has not said which one it means.
var audioMediaProbeKnownKeys = map[string]bool{
	"assetId": true, "contentHash": true, "filename": true, "sizeBytes": true, "items": true,
}

// audioMediaProbeItemKnownKeys is the key allowlist for one object inside
// params.items.
var audioMediaProbeItemKnownKeys = map[string]bool{
	"itemId": true, "index": true, "assetId": true, "contentHash": true, "filename": true, "sizeBytes": true,
}

// audioProbeItems runs the real GStreamer-backed probe, a package-level var
// (matching audioProbeOutput's own injection convention in audioops.go) so
// command_test.go can prove this operation's wiring and fault mapping
// without shelling out to gst-launch-1.0.
var audioProbeItems = func(ctx context.Context, dir string, items []pkgaudio.PlaylistItem) audio.ReadinessReport {
	return audio.ProbeItems(ctx, dir, items, audio.RealDecoder{})
}

// mediaProbeOperation is the OperationFunc for "audio.media.probe": bound to
// dir, the node's asset directory (config.Config.AssetDir), so
// [pkgaudio.MediaRef.RuntimeFilename] resolves to the same files
// asset.fetch writes and assets.go's inventory enumerates.
type mediaProbeOperation struct {
	dir string
}

// run implements [OperationFunc]. A well-formed request never returns an
// error: a fault is genuine evidence (ruling 3), reported as
// Confirmed:false rather than OutcomeFailed, since the operation itself
// did not fail. Confirmed is true only when every item probed Ready.
func (o mediaProbeOperation) run(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	const action = "audio.media.probe"

	if err := rejectUnknownKeys(action, params, audioMediaProbeKnownKeys); err != nil {
		return OperationResult{}, err
	}

	items, err := parseMediaProbeParams(action, params)
	if err != nil {
		return OperationResult{}, err
	}

	executedAt := now()
	report := audioProbeItems(ctx, o.dir, items)
	observedAt := now()

	return OperationResult{
		Confirmed:  report.State == audio.MediaReady,
		Signal:     "audio_session.readiness.state",
		Value:      mediaProbeReportValue(report),
		ExecutedAt: executedAt,
		ObservedAt: observedAt,
	}, nil
}

// mediaProbeReportValue converts report into the evidence Value shape:
// "state" and "reason" carry the two signals this seam reserves
// (audio_session.readiness.state, audio_session.readiness.reason), and
// "items" is every probed item's own evidence — ruling 4, never only the
// first.
func mediaProbeReportValue(report audio.ReadinessReport) map[string]any {
	items := make([]map[string]any, 0, len(report.Items))
	for _, it := range report.Items {
		items = append(items, map[string]any{
			"itemId":         it.ItemID,
			"index":          it.Index,
			"assetId":        it.AssetID,
			"state":          string(it.State),
			"fault":          string(it.Fault),
			"reason":         it.Reason,
			"durationKnown":  it.DurationKnown,
			"duration":       it.Duration.String(),
			"durationSource": string(it.DurationSource),
			"container":      it.Container,
			"codec":          it.Codec,
			"channels":       it.Channels,
			"sampleRate":     it.SampleRate,
		})
	}
	return map[string]any{
		"state":  string(report.State),
		"reason": report.Reason,
		"items":  items,
	}
}

// parseMediaProbeParams extracts either a single media identity (as a
// one-item playlist) or params.items (a pinned playlist) from params.
func parseMediaProbeParams(action string, params map[string]any) ([]pkgaudio.PlaylistItem, error) {
	rawItems, isPlaylist := params["items"]
	if isPlaylist {
		list, ok := rawItems.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: params.items must be an array, got %T", action, rawItems)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("%s: params.items must not be empty", action)
		}
		items := make([]pkgaudio.PlaylistItem, 0, len(list))
		for i, raw := range list {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: params.items[%d] must be an object, got %T", action, i, raw)
			}
			if err := rejectUnknownKeys(action, m, audioMediaProbeItemKnownKeys); err != nil {
				return nil, err
			}
			item, err := parseMediaProbeItem(action, m, i)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	}

	ref, err := parseMediaRef(action, params)
	if err != nil {
		return nil, err
	}
	return []pkgaudio.PlaylistItem{{ItemID: ref.AssetID, Index: 0, Media: ref}}, nil
}

// parseMediaProbeItem parses one params.items[i] object, defaulting itemId
// to the asset id (matching the single-media path's own convention) and
// index to i when absent.
func parseMediaProbeItem(action string, m map[string]any, i int) (pkgaudio.PlaylistItem, error) {
	ref, err := parseMediaRef(action, m)
	if err != nil {
		return pkgaudio.PlaylistItem{}, err
	}

	itemID := ref.AssetID
	if raw, ok := m["itemId"]; ok {
		v, ok := raw.(string)
		if !ok || v == "" {
			return pkgaudio.PlaylistItem{}, fmt.Errorf("%s: params.items[%d].itemId must be a non-empty string, got %T", action, i, raw)
		}
		itemID = v
	}

	index := i
	if raw, ok := m["index"]; ok {
		f, ok := raw.(float64)
		if !ok {
			return pkgaudio.PlaylistItem{}, fmt.Errorf("%s: params.items[%d].index must be a number, got %T", action, i, raw)
		}
		index = int(f)
	}

	return pkgaudio.PlaylistItem{ItemID: itemID, Index: index, Media: ref}, nil
}

// parseMediaRef extracts a MediaRef's fields from m: assetId, contentHash,
// and filename are required non-empty strings; sizeBytes, when present,
// must be a positive number, and is optional — the identity re-check
// skips the size comparison when absent rather than treating it as zero.
func parseMediaRef(action string, m map[string]any) (pkgaudio.MediaRef, error) {
	str := func(key string) (string, error) {
		raw, ok := m[key]
		if !ok {
			return "", fmt.Errorf("%s: params.%s is required", action, key)
		}
		v, ok := raw.(string)
		if !ok || v == "" {
			return "", fmt.Errorf("%s: params.%s must be a non-empty string, got %T", action, key, raw)
		}
		return v, nil
	}

	assetID, err := str("assetId")
	if err != nil {
		return pkgaudio.MediaRef{}, err
	}
	contentHash, err := str("contentHash")
	if err != nil {
		return pkgaudio.MediaRef{}, err
	}
	filename, err := str("filename")
	if err != nil {
		return pkgaudio.MediaRef{}, err
	}
	if err := validateAssetFilename(filename); err != nil {
		return pkgaudio.MediaRef{}, fmt.Errorf("%s: %w", action, err)
	}

	var sizeBytes int64
	if raw, ok := m["sizeBytes"]; ok {
		f, ok := raw.(float64)
		if !ok {
			return pkgaudio.MediaRef{}, fmt.Errorf("%s: params.sizeBytes must be a number, got %T", action, raw)
		}
		if f < 1 {
			return pkgaudio.MediaRef{}, fmt.Errorf("%s: params.sizeBytes must be at least 1, got %v", action, f)
		}
		sizeBytes = int64(f)
	}

	return pkgaudio.MediaRef{AssetID: assetID, ContentHash: contentHash, SizeBytes: sizeBytes, RuntimeFilename: filename}, nil
}
