package audio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// MediaFault names why one probed media item is not ready: five distinct
// members, never collapsed into a single value (ruling 3).
type MediaFault string

const (
	MediaFaultMissing           MediaFault = "missing"
	MediaFaultHashMismatch      MediaFault = "hash_mismatch"
	MediaFaultUndecodable       MediaFault = "undecodable"
	MediaFaultDurationUnknown   MediaFault = "duration_unknown"
	MediaFaultUnsupportedFormat MediaFault = "unsupported_format"
)

// MediaReadiness is one item's, or a whole playlist's, probe verdict.
// Unknown is deliberately not Fault (ruling 5): it means the probe itself
// could not run, never that it ran and rejected the file.
type MediaReadiness string

const (
	MediaReady   MediaReadiness = "ready"
	MediaFaulted MediaReadiness = "fault"
	MediaUnknown MediaReadiness = "unknown"
)

// MediaItemResult is one media item's probe evidence: Container, Codec,
// Channels, and SampleRate come only from what GStreamer actually decoded
// or identified (ruling 1), never from the filename.
type MediaItemResult struct {
	State  MediaReadiness
	Fault  MediaFault // empty unless State == MediaFaulted
	Reason string     // required whenever State != MediaReady; also carries
	// an advisory note on a bounded-decode-only Ready

	// DurationKnown and DurationSource are ADR-011 provenance (ruling 4):
	// a Duration is only ever trustworthy when DurationKnown is true, and
	// DurationSource says whether it came from the container's own
	// metadata or was never measured at all (a bounded decode only
	// confirms decodability, never a duration — ruling 2).
	DurationKnown  bool
	DurationSource DurationSource
	Duration       time.Duration

	Container  string
	Codec      string
	Channels   int
	SampleRate int
}

// faulted builds a MediaFaulted MediaItemResult.
func faulted(fault MediaFault, reason string) MediaItemResult {
	return MediaItemResult{State: MediaFaulted, Fault: fault, Reason: reason}
}

// unknown builds a MediaUnknown MediaItemResult.
func unknown(reason string) MediaItemResult {
	return MediaItemResult{State: MediaUnknown, Reason: reason}
}

// hashFile computes path's "sha256:<hex>" content hash — a read-only
// verification hash, not a second asset store (ruling 6).
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// checkIdentity re-verifies ref's content hash and size against the file at
// path, at every probe rather than once at assignment (ruling 2). A size
// mismatch alone is conclusive and skips the hash it would otherwise need.
func checkIdentity(path string, ref pkgaudio.MediaRef) (ok bool, reason string, err error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return false, "", statErr
	}
	if ref.SizeBytes > 0 && info.Size() != ref.SizeBytes {
		return false, fmt.Sprintf("size on disk is %d bytes, assigned reference declares %d", info.Size(), ref.SizeBytes), nil
	}
	gotHash, hashErr := hashFile(path)
	if hashErr != nil {
		return false, "", hashErr
	}
	if gotHash != ref.ContentHash {
		return false, fmt.Sprintf("content hash on disk is %s, assigned reference declares %s", gotHash, ref.ContentHash), nil
	}
	return true, "", nil
}

// classifyDecode applies ruling 3's fault vocabulary to one real
// [DecodeResult]. dr.Available is assumed true — an unavailable decoder is
// [MediaUnknown] and never reaches this function; see [ProbeAsset].
//
// A bounded decode (ruling 2) is the sole gate for Undecodable/
// UnsupportedFormat: it always runs, so this function never trusts
// gst-discoverer-1.0's metadata on its own — MEASURED, decisive for ruling
// 3, on [decodedAudioCapsPattern]'s own doc comment. gst-discoverer-1.0,
// when it ran, is the sole source of an authoritative Duration (ruling 1);
// its absence falls back to a Ready verdict with no duration rather than a
// fault (ruling 5).
func classifyDecode(dr DecodeResult) MediaItemResult {
	if !dr.TypeIdentified {
		return faulted(MediaFaultUndecodable, "GStreamer could not identify any stream type in this file's content")
	}
	if !isAudioMIME(dr.MIMEType) && !dr.Decoded {
		return faulted(MediaFaultUnsupportedFormat, fmt.Sprintf(
			"this file's actual content is %q and a bounded decode of it produced no audio, regardless of its assigned filename", dr.MIMEType))
	}
	if !dr.Decoded {
		reason := fmt.Sprintf("GStreamer identified %q but a bounded decode of it produced no audio", dr.MIMEType)
		if dr.Discoverer.Ran && !dr.Discoverer.Errored {
			reason = "gst-discoverer-1.0's container metadata reported this file as playable, but a bounded decode of it produced no audio — metadata is not proof of decodability"
		}
		return faulted(MediaFaultUndecodable, reason)
	}

	if dr.Discoverer.Ran && !dr.Discoverer.Errored && dr.Discoverer.Duration > 0 {
		return MediaItemResult{
			State: MediaReady, DurationKnown: true, DurationSource: DurationSourceContainerMetadata,
			Duration:  dr.Discoverer.Duration,
			Container: dr.MIMEType, Codec: firstNonEmpty(dr.Discoverer.Codec, dr.Codec),
			Channels:   firstPositive(dr.Discoverer.Channels, dr.Channels),
			SampleRate: firstPositive(dr.Discoverer.SampleRate, dr.SampleRate),
		}
	}

	if dr.Discoverer.Ran {
		reason := "a bounded decode confirms this file plays, but gst-discoverer-1.0 reported no usable duration"
		if dr.Discoverer.Errored {
			reason = fmt.Sprintf("a bounded decode confirms this file plays, but gst-discoverer-1.0 could not determine its duration: %s", dr.Discoverer.Reason)
		}
		return MediaItemResult{
			State: MediaFaulted, Fault: MediaFaultDurationUnknown, Reason: reason, DurationSource: DurationSourceContainerMetadata,
			Container: dr.MIMEType, Codec: dr.Codec, Channels: dr.Channels, SampleRate: dr.SampleRate,
		}
	}

	// Ruling 5: gst-discoverer-1.0 was never invoked at all (unresolvable
	// on this node). A confirmed bounded decode is Ready, never a fault,
	// with no duration claimed.
	return MediaItemResult{
		State: MediaReady, DurationSource: DurationSourceBoundedDecode,
		Reason:    "gst-discoverer-1.0 is not available on this node; readiness rests on a bounded decode only, and duration is not known",
		Container: dr.MIMEType, Codec: dr.Codec, Channels: dr.Channels, SampleRate: dr.SampleRate,
	}
}

// firstNonEmpty returns a if non-empty, else b — used to prefer
// gst-discoverer-1.0's own codec description over the bounded decode's
// element class name when both are available.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstPositive returns a if positive, else b.
func firstPositive(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// isAudioMIME reports whether mimeType is an audio type by GStreamer's own
// typefind naming convention (an "audio/" prefix). typefind names the
// OUTERMOST container, which for a tagged file is the tag wrapper
// ("application/x-id3" for any ID3v2-tagged MP3), so this is not on its
// own decisive: a bounded decode that produced raw audio outranks it.
func isAudioMIME(mimeType string) bool {
	return len(mimeType) > len("audio/") && mimeType[:len("audio/")] == "audio/"
}

// ProbeAsset re-checks ref's identity (ruling 2), then decodes and
// classifies (ruling 3). Every filesystem access here is a read (ruling 6).
func ProbeAsset(ctx context.Context, dir string, ref pkgaudio.MediaRef, dec Decoder) MediaItemResult {
	path := filepath.Join(dir, ref.RuntimeFilename)

	identOK, identReason, err := checkIdentity(path, ref)
	if err != nil {
		if os.IsNotExist(err) {
			return faulted(MediaFaultMissing, fmt.Sprintf("asset %s is not present at %s", ref.AssetID, path))
		}
		return faulted(MediaFaultMissing, fmt.Sprintf("asset %s could not be read at %s: %v", ref.AssetID, path, err))
	}
	if !identOK {
		return faulted(MediaFaultHashMismatch, identReason)
	}

	dr := dec.Decode(ctx, path)
	if !dr.Available {
		return unknown(dr.Reason)
	}
	return classifyDecode(dr)
}

// MediaProbeItem is one playlist slot's identity plus its probe evidence.
type MediaProbeItem struct {
	ItemID  string
	Index   int
	AssetID string
	MediaItemResult
}

// ReadinessReport is a whole playlist's (or, for a lone [pkgaudio.MediaRef],
// a single-item playlist's) rolled-up probe verdict — ruling 4: covers
// every item, never only the first.
type ReadinessReport struct {
	State  MediaReadiness
	Reason string
	Items  []MediaProbeItem
}

// ProbeItems probes every item against dir with dec and rolls the results
// up: any fault makes the report Fault, else any unknown makes it Unknown.
func ProbeItems(ctx context.Context, dir string, items []pkgaudio.PlaylistItem, dec Decoder) ReadinessReport {
	results := make([]MediaProbeItem, 0, len(items))
	for _, item := range items {
		results = append(results, MediaProbeItem{
			ItemID: item.ItemID, Index: item.Index, AssetID: item.Media.AssetID,
			MediaItemResult: ProbeAsset(ctx, dir, item.Media, dec),
		})
	}
	return summarize(results)
}

func summarize(items []MediaProbeItem) ReadinessReport {
	report := ReadinessReport{State: MediaReady, Items: items}

	var faultReasons, unknownReasons []string
	for _, it := range items {
		switch it.State {
		case MediaFaulted:
			faultReasons = append(faultReasons, fmt.Sprintf("item %s (index %d): %s: %s", it.ItemID, it.Index, it.Fault, it.Reason))
		case MediaUnknown:
			unknownReasons = append(unknownReasons, fmt.Sprintf("item %s (index %d): %s", it.ItemID, it.Index, it.Reason))
		}
	}

	switch {
	case len(faultReasons) > 0:
		report.State = MediaFaulted
		report.Reason = fmt.Sprintf("%d of %d item(s) not ready: %s", len(faultReasons), len(items), joinReasons(faultReasons))
	case len(unknownReasons) > 0:
		report.State = MediaUnknown
		report.Reason = fmt.Sprintf("%d of %d item(s) could not be probed: %s", len(unknownReasons), len(items), joinReasons(unknownReasons))
	}
	return report
}

func joinReasons(reasons []string) string {
	out := ""
	for i, r := range reasons {
		if i > 0 {
			out += "; "
		}
		out += r
	}
	return out
}
