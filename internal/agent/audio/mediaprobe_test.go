package audio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

type fakeDecoder struct {
	result DecodeResult
	got    string // path fakeDecoder.Decode was called with
}

func (f *fakeDecoder) Decode(_ context.Context, path string) DecodeResult {
	f.got = path
	return f.result
}

// readyBoundedDecode is a DecodeResult representing a bounded confirmation
// decode that succeeded, with no gst-discoverer-1.0 evidence attached —
// tests that care about the discoverer path attach their own Discoverer.
func readyBoundedDecode(mime, codec string, channels, rate int) DecodeResult {
	return DecodeResult{Available: true, TypeIdentified: true, MIMEType: mime, Decoded: true, Codec: codec, Channels: channels, SampleRate: rate}
}

// writeFixture writes content to dir/name and returns a MediaRef whose
// ContentHash and SizeBytes are computed from content itself (a correct
// reference, for tests that are not exercising the mismatch path).
func writeFixture(t *testing.T, dir, name string, content []byte) pkgaudio.MediaRef {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
	sum := sha256.Sum256(content)
	return pkgaudio.MediaRef{
		AssetID: name, ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)), RuntimeFilename: name,
	}
}

// TestProbeAssetMissingFileReportsMissingNeverUndecodable proves ruling 3's
// missing/undecodable distinction: a MediaRef naming a file this node does
// not hold at all must never reach the decoder.
func TestProbeAssetMissingFileReportsMissingNeverUndecodable(t *testing.T) {
	dec := &fakeDecoder{result: readyBoundedDecode("audio/x-wav", "WavParse", 2, 44100)}
	ref := pkgaudio.MediaRef{AssetID: "ghost", ContentHash: "sha256:whatever", SizeBytes: 10, RuntimeFilename: "ghost.wav"}

	got := ProbeAsset(context.Background(), t.TempDir(), ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultMissing {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=missing", got)
	}
	if dec.got != "" {
		t.Errorf("decoder was called with %q; a missing file must never reach the decoder", dec.got)
	}
}

// TestProbeAssetSizeMismatchReportsHashMismatchWithoutHashing proves the
// cheap short-circuit: a size disagreement alone is conclusive and this
// package never even opens the file to hash it in that case.
func TestProbeAssetSizeMismatchReportsHashMismatchWithoutHashing(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("hello world"))
	ref.SizeBytes = ref.SizeBytes + 1 // now wrong

	dec := &fakeDecoder{}
	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultHashMismatch {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=hash_mismatch", got)
	}
	if dec.got != "" {
		t.Errorf("decoder was called with %q; a size mismatch must never reach the decoder", dec.got)
	}
}

// TestProbeAssetContentHashMismatchSameSizeReportsHashMismatch proves the
// re-check computes a real hash rather than trusting size alone when sizes
// happen to agree — ruling 2's "re-check at the boundary" applied to
// content that changed without changing length.
func TestProbeAssetContentHashMismatchSameSizeReportsHashMismatch(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("hello world"))
	ref.ContentHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	dec := &fakeDecoder{}
	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultHashMismatch {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=hash_mismatch", got)
	}
	if dec.got != "" {
		t.Errorf("decoder was called with %q; a hash mismatch must never reach the decoder", dec.got)
	}
}

// TestProbeAssetDecoderUnavailableReportsUnknownNeverFault proves ruling 5:
// a probe that could not run at all is Unknown, not a fault, even though
// identity checked out.
func TestProbeAssetDecoderUnavailableReportsUnknownNeverFault(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("hello world"))
	dec := &fakeDecoder{result: DecodeResult{Available: false, Reason: "gst-launch-1.0 not found on PATH"}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaUnknown {
		t.Fatalf("ProbeAsset.State = %v, want %v", got.State, MediaUnknown)
	}
	if got.Fault != "" {
		t.Errorf("Fault = %q, want empty — Unknown must never carry a Fault", got.Fault)
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want the decoder's stated reason")
	}
}

// TestProbeAssetNoTypeIdentifiedReportsUndecodable proves the "GStreamer
// could not identify any type at all" branch — garbage bytes, no
// recognizable container.
func TestProbeAssetNoTypeIdentifiedReportsUndecodable(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.bin", []byte("hello world"))
	dec := &fakeDecoder{result: DecodeResult{Available: true, TypeIdentified: false}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultUndecodable {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=undecodable", got)
	}
}

// TestProbeAssetNonAudioTypeReportsUnsupportedFormat proves the
// "identified, but not audio" branch stays distinct from undecodable — a
// file with an audio extension and non-audio content (this seam's own
// named fixture).
func TestProbeAssetNonAudioTypeReportsUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.mp3", []byte("this is not audio content"))
	dec := &fakeDecoder{result: DecodeResult{Available: true, TypeIdentified: true, MIMEType: "text/plain"}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultUnsupportedFormat {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=unsupported_format", got)
	}
	if got.Reason == "" || got.Container != "" {
		t.Errorf("got %+v, want a non-empty Reason and no Container claimed for an unsupported type", got)
	}
}

// TestProbeAssetIdentifiedAudioButNotDecodedReportsUndecodable proves the
// "recognized as audio, but a bounded decode produced nothing" branch —
// this seam's own named "truncated file" fixture shape: typefind succeeds,
// the bounded decode never confirms a raw audio pad.
func TestProbeAssetIdentifiedAudioButNotDecodedReportsUndecodable(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("RIFF...short"))
	dec := &fakeDecoder{result: DecodeResult{Available: true, TypeIdentified: true, MIMEType: "audio/x-wav", Decoded: false}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultUndecodable {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=undecodable", got)
	}
}

// TestProbeAssetMetadataClaimsPlayableButBoundedDecodeDisagreesReportsUndecodable
// is this package's own proof of ruling 3 at the classification layer: a
// caller must never trust gst-discoverer-1.0's own success on its own —
// even when Discoverer says the file plays fine, an unconfirmed bounded
// decode (Decoded: false) still wins and reports Undecodable, mentioning
// that metadata is not proof of decodability.
func TestProbeAssetMetadataClaimsPlayableButBoundedDecodeDisagreesReportsUndecodable(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.flac", []byte("fLaC-truncated"))
	dec := &fakeDecoder{result: DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/x-flac", Decoded: false,
		Discoverer: DiscovererEvidence{Ran: true, Errored: false, Duration: 10 * time.Second, Codec: "FLAC", Channels: 2, SampleRate: 44100},
	}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultUndecodable {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=undecodable", got)
	}
	if got.DurationKnown {
		t.Error("DurationKnown = true, want false: a fault must never carry a trusted duration")
	}
}

// TestProbeAssetDiscovererDurationIsAuthoritativeOnReady proves ruling 1:
// when gst-discoverer-1.0 ran, succeeded, and the bounded decode confirms
// playability, Duration/Codec/Channels/SampleRate come from the
// discoverer's evidence, not the bounded decode's own (deliberately
// different, in this test) values.
func TestProbeAssetDiscovererDurationIsAuthoritativeOnReady(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.flac", []byte("fLaC-real"))
	dec := &fakeDecoder{result: DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/x-flac", Decoded: true,
		Codec: "FlacDec", Channels: 1, SampleRate: 8000, // bounded decode's own (should be overridden)
		Discoverer: DiscovererEvidence{
			Ran: true, Errored: false, Duration: 217 * time.Second,
			Codec: "Free Lossless Audio Codec (FLAC)", Channels: 2, SampleRate: 44100,
		},
	}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaReady {
		t.Fatalf("ProbeAsset.State = %v, want %v (got %+v)", got.State, MediaReady, got)
	}
	if !got.DurationKnown || got.Duration != 217*time.Second {
		t.Errorf("DurationKnown/Duration = %v/%v, want true/217s", got.DurationKnown, got.Duration)
	}
	if got.DurationSource != DurationSourceContainerMetadata {
		t.Errorf("DurationSource = %q, want %q", got.DurationSource, DurationSourceContainerMetadata)
	}
	if got.Codec != "Free Lossless Audio Codec (FLAC)" || got.Channels != 2 || got.SampleRate != 44100 {
		t.Errorf("got %+v, want the DISCOVERER's codec/channels/rate, not the bounded decode's", got)
	}
}

// TestProbeAssetDiscovererErroredWithConfirmedDecodeReportsDurationUnknown
// proves the DurationUnknown branch fires when the bounded decode confirms
// playability but gst-discoverer-1.0 itself could not determine a
// duration — this is Ready evidence for decodability but not for timing.
func TestProbeAssetDiscovererErroredWithConfirmedDecodeReportsDurationUnknown(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("weird-but-plays"))
	dec := &fakeDecoder{result: DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/x-wav", Decoded: true,
		Codec: "WavParse", Channels: 2, SampleRate: 44100,
		Discoverer: DiscovererEvidence{Ran: true, Errored: true, Reason: "some discoverer-specific complaint"},
	}}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaFaulted || got.Fault != MediaFaultDurationUnknown {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=duration_unknown", got)
	}
	if got.DurationKnown {
		t.Error("DurationKnown = true, want false")
	}
}

// TestProbeAssetDiscovererUnavailableFallsBackToBoundedDecodeReady proves
// ruling 5: gst-discoverer-1.0 never ran at all (Discoverer.Ran == false)
// and a confirmed bounded decode reports Ready, never a fault, with
// DurationKnown false and the fallback stated in Reason.
func TestProbeAssetDiscovererUnavailableFallsBackToBoundedDecodeReady(t *testing.T) {
	dir := t.TempDir()
	ref := writeFixture(t, dir, "a.wav", []byte("plays-fine"))
	dec := &fakeDecoder{result: readyBoundedDecode("audio/x-wav", "WavParse", 2, 44100)}

	got := ProbeAsset(context.Background(), dir, ref, dec)

	if got.State != MediaReady {
		t.Fatalf("ProbeAsset.State = %v, want %v (got %+v)", got.State, MediaReady, got)
	}
	if got.DurationKnown {
		t.Error("DurationKnown = true, want false: gst-discoverer-1.0 never ran")
	}
	if got.DurationSource != DurationSourceBoundedDecode {
		t.Errorf("DurationSource = %q, want %q", got.DurationSource, DurationSourceBoundedDecode)
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want it to say gst-discoverer-1.0 was unavailable, even though State is Ready")
	}
	if got.Container != "audio/x-wav" || got.Codec != "WavParse" || got.Channels != 2 || got.SampleRate != 44100 {
		t.Errorf("got %+v, want the bounded decode's own container/codec/channels/rate", got)
	}
}

func item(id string, index int, ref pkgaudio.MediaRef) pkgaudio.PlaylistItem {
	return pkgaudio.PlaylistItem{ItemID: id, Index: index, Media: ref}
}

// TestProbeItemsCoversEveryItemNeverOnlyTheFirst is this package's own
// proof of ruling 4: three items, only the middle one faulted, and the
// rolled-up report must still carry all three, in order, with the overall
// state driven by the worst one.
func TestProbeItemsCoversEveryItemNeverOnlyTheFirst(t *testing.T) {
	dir := t.TempDir()
	refA := writeFixture(t, dir, "a.wav", []byte("aaaa"))
	refB := writeFixture(t, dir, "b.wav", []byte("bbbb"))
	refC := writeFixture(t, dir, "c.wav", []byte("cccc"))
	refB.ContentHash = "sha256:wrong"

	dec := &fakeDecoder{result: readyBoundedDecode("audio/x-wav", "WavParse", 1, 8000)}

	report := ProbeItems(context.Background(), dir, []pkgaudio.PlaylistItem{
		item("a", 0, refA), item("b", 1, refB), item("c", 2, refC),
	}, dec)

	if len(report.Items) != 3 {
		t.Fatalf("report.Items has %d entries, want 3", len(report.Items))
	}
	if report.State != MediaFaulted {
		t.Fatalf("report.State = %v, want %v (one item faulted)", report.State, MediaFaulted)
	}
	if report.Items[0].State != MediaReady || report.Items[2].State != MediaReady {
		t.Errorf("first and third items = %+v / %+v, want both Ready", report.Items[0], report.Items[2])
	}
	if report.Items[1].State != MediaFaulted || report.Items[1].Fault != MediaFaultHashMismatch {
		t.Errorf("second item = %+v, want State=fault Fault=hash_mismatch", report.Items[1])
	}
	if report.Reason == "" {
		t.Error("report.Reason is empty, want it to name the faulted item")
	}
}

// TestProbeItemsAllReadyRollsUpToReady proves the three-way rollup's
// simplest case.
func TestProbeItemsAllReadyRollsUpToReady(t *testing.T) {
	dir := t.TempDir()
	refA := writeFixture(t, dir, "a.wav", []byte("aaaa"))
	dec := &fakeDecoder{result: readyBoundedDecode("audio/x-wav", "WavParse", 1, 8000)}

	report := ProbeItems(context.Background(), dir, []pkgaudio.PlaylistItem{item("a", 0, refA)}, dec)

	if report.State != MediaReady {
		t.Fatalf("report.State = %v, want %v", report.State, MediaReady)
	}
}

// TestProbeItemsUnknownWithNoFaultsRollsUpToUnknown proves the rollup's
// no-faults-but-an-unknown case.
func TestProbeItemsUnknownWithNoFaultsRollsUpToUnknown(t *testing.T) {
	dir := t.TempDir()
	refA := writeFixture(t, dir, "a.wav", []byte("aaaa"))
	dec := &fakeDecoder{result: DecodeResult{Available: false, Reason: "gst-launch-1.0 not found"}}

	report := ProbeItems(context.Background(), dir, []pkgaudio.PlaylistItem{item("a", 0, refA)}, dec)

	if report.State != MediaUnknown {
		t.Fatalf("report.State = %v, want %v", report.State, MediaUnknown)
	}
}
