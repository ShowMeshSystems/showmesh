package audio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// writeMinimalWAV writes a valid PCM WAV file with actual varying (a 440Hz
// tone), never-silent samples: an all-zero fixture is ambiguous evidence
// for a real decoder (silence still compresses/parses trivially in ways
// that can mask a truncation bug — see mediaprobe_test.go's own history).
// No GStreamer, no external tooling: just the RIFF/WAVE header bytes this
// format requires, generated in the test rather than committed as a binary.
func writeMinimalWAV(t *testing.T, path string, sampleRate, numSamples int) []byte {
	t.Helper()
	const bitsPerSample = 16
	const channels = 1
	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8
	data := make([]byte, numSamples*blockAlign)
	for i := 0; i < numSamples; i++ {
		// A cheap integer approximation of a 440Hz tone — real, varying
		// samples, not a formula that needs math.Sin to be convincing.
		v := int16(((i * 440 * 4) % sampleRate) - sampleRate/2)
		data[2*i] = byte(v)
		data[2*i+1] = byte(v >> 8)
	}

	buf := make([]byte, 0, 44+len(data))
	buf = append(buf, []byte("RIFF")...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(36+len(data)))
	buf = append(buf, []byte("WAVEfmt ")...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1) // PCM
	buf = binary.LittleEndian.AppendUint16(buf, uint16(channels))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(sampleRate))
	buf = binary.LittleEndian.AppendUint32(buf, uint32(byteRate))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(blockAlign))
	buf = binary.LittleEndian.AppendUint16(buf, bitsPerSample)
	buf = append(buf, []byte("data")...)
	buf = binary.LittleEndian.AppendUint32(buf, uint32(len(data)))
	buf = append(buf, data...)

	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("writing WAV fixture: %v", err)
	}
	return buf
}

// writeRealFLAC generates a real FLAC file via gst-launch-1.0 itself
// (audiotestsrc ! audioconvert ! flacenc ! filesink) — this is "generate
// the fixture in the test", using the same real tool this package's own
// probe uses, rather than hand-rolling the FLAC bitstream or committing a
// binary. t.Skip()s if gst-launch-1.0 or flacenc are unavailable, since
// building the fixture is not itself the thing under test.
func writeRealFLAC(t *testing.T, path string) []byte {
	t.Helper()
	gstPath, ok, reason := pipeline.ResolveGstLaunch()
	if !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable to generate a FLAC fixture: %s", reason)
	}
	cmd := exec.Command(gstPath, "audiotestsrc", "wave=sine", "num-buffers=200",
		"!", "audioconvert", "!", "flacenc", "!", "filesink", "location="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("skipping: could not generate a real FLAC fixture (flacenc likely unavailable): %v: %s", err, out)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated FLAC fixture: %v", err)
	}
	return content
}

func refFor(t *testing.T, path string, content []byte) pkgaudio.MediaRef {
	t.Helper()
	sum := sha256.Sum256(content)
	return pkgaudio.MediaRef{
		AssetID: filepath.Base(path), ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)), RuntimeFilename: filepath.Base(path),
	}
}

// assertReadyWithConsistentProvenance is every real-decode "should be
// Ready" test's shared check: this environment may or may not have
// gst-discoverer-1.0 (showmesh-audio-gotest, MEASURED, does not — see the
// build's own report), so a real test cannot assume DurationKnown is
// true. It can and must assume the two are never inconsistent.
func assertReadyWithConsistentProvenance(t *testing.T, got MediaItemResult) {
	t.Helper()
	if got.State != MediaReady {
		t.Fatalf("real probe: State = %v, want ready — %+v", got.State, got)
	}
	switch got.DurationSource {
	case DurationSourceContainerMetadata:
		if !got.DurationKnown || got.Duration <= 0 {
			t.Errorf("DurationSource=container_metadata but DurationKnown/Duration = %v/%v", got.DurationKnown, got.Duration)
		}
	case DurationSourceBoundedDecode:
		if got.DurationKnown {
			t.Error("DurationSource=bounded_decode but DurationKnown = true")
		}
		if got.Reason == "" {
			t.Error("DurationSource=bounded_decode but Reason is empty — ruling 5 requires saying so")
		}
	default:
		t.Errorf("DurationSource = %q, want one of the two known values", got.DurationSource)
	}
}

// TestProbeAssetRealDecodeValidWav drives [ProbeAsset] with the real
// [RealDecoder] (no fake, no canned process output) against a generated,
// genuinely valid WAV file, exercising this package's own regex parsing
// against real GStreamer (and, where available, real gst-discoverer-1.0)
// process output rather than only the canned strings in decode_test.go.
func TestProbeAssetRealDecodeValidWav(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "valid.wav")
	content := writeMinimalWAV(t, path, 44100, 44100) // 1 second, mono, 16-bit
	ref := refFor(t, path, content)

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	assertReadyWithConsistentProvenance(t, got)
	if got.Channels != 1 {
		t.Errorf("real probe: Channels = %d, want 1", got.Channels)
	}
	if got.SampleRate != 44100 {
		t.Errorf("real probe: SampleRate = %d, want 44100", got.SampleRate)
	}
	if got.Container != "audio/x-wav" {
		t.Errorf("real probe: Container = %q, want audio/x-wav", got.Container)
	}
	if got.DurationKnown {
		wantDuration := 1 * time.Second
		if delta := got.Duration - wantDuration; delta < -50*time.Millisecond || delta > 50*time.Millisecond {
			t.Errorf("real probe: Duration = %v, want approximately %v", got.Duration, wantDuration)
		}
	}
}

// TestProbeAssetRealDecodeValidFLAC is the same proof as the WAV case,
// against a container whose caps line orders "rate" before "channels" —
// the exact ordering difference [decodedAudioCapsPattern]'s own doc
// comment measures.
func TestProbeAssetRealDecodeValidFLAC(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "valid.flac")
	content := writeRealFLAC(t, path)
	ref := refFor(t, path, content)

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	assertReadyWithConsistentProvenance(t, got)
	if got.Channels != 1 {
		t.Errorf("real probe: Channels = %d, want 1", got.Channels)
	}
	if got.Container != "audio/x-flac" {
		t.Errorf("real probe: Container = %q, want audio/x-flac", got.Container)
	}
}

// TestProbeAssetRealDecodeTruncatedWavHeader drives the real decoder
// against a WAV cut inside its own header — real GStreamer's own rejection
// of a structurally broken container, not a canned string.
func TestProbeAssetRealDecodeTruncatedWavHeader(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.wav")
	full := writeMinimalWAV(t, path, 44100, 44100)
	truncated := full[:30] // inside the fmt chunk, before any sample data
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatalf("truncating fixture: %v", err)
	}
	ref := refFor(t, path, truncated)

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	if got.State != MediaFaulted || got.Fault != MediaFaultUndecodable {
		t.Fatalf("real probe of a header-truncated WAV: got %+v, want State=fault Fault=undecodable", got)
	}
}

// TestProbeAssetRealDecodeTruncatedFLACMetadata is ruling 3's own decisive
// real-environment proof, and the reason this seam changed shape: a FLAC
// cut to 200 bytes — inside its metadata, well past the header, before any
// encoded frame — whose STREAMINFO block survives intact. Where
// gst-discoverer-1.0 is available, MEASURED behavior is that it reports
// BOTH an explicit error AND a full, correct Duration from that surviving
// metadata in the SAME result; this test proves the package-level
// classification never lets that Duration read as Ready.
func TestProbeAssetRealDecodeTruncatedFLACMetadata(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	dir := t.TempDir()
	fullPath := filepath.Join(dir, "full.flac")
	full := writeRealFLAC(t, fullPath)
	if len(full) < 400 {
		t.Fatalf("generated FLAC fixture is only %d bytes, too small to truncate meaningfully", len(full))
	}
	truncated := full[:200] // MEASURED: inside metadata, STREAMINFO intact, no frames
	path := filepath.Join(dir, "truncated.flac")
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatalf("truncating fixture: %v", err)
	}
	ref := refFor(t, path, truncated)

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	if got.State != MediaFaulted || got.Fault != MediaFaultUndecodable {
		t.Fatalf("real probe of a metadata-truncated FLAC: got %+v, want State=fault Fault=undecodable — a surviving STREAMINFO duration must never make this Ready", got)
	}
	if got.DurationKnown {
		t.Error("DurationKnown = true, want false: a fault must never carry a trusted duration")
	}
}

// TestProbeAssetRealDecodeNonAudioContent drives the real decoder against a
// file carrying an audio-looking name but genuinely non-audio content —
// this seam's own named fixture ("a file with an audio extension and
// non-audio content").
func TestProbeAssetRealDecodeNonAudioContent(t *testing.T) {
	if _, ok, reason := pipeline.ResolveGstLaunch(); !ok {
		t.Skipf("skipping: gst-launch-1.0 not resolvable: %s", reason)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "notaudio.mp3")
	content := []byte("this is definitely not audio content, just plain repeated text.\n")
	for len(content) < 4096 {
		content = append(content, content...)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	ref := refFor(t, path, content)

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	if got.State != MediaFaulted {
		t.Fatalf("real probe of plain-text content named .mp3: got State=%v, want fault — %+v", got.State, got)
	}
	if got.Fault != MediaFaultUnsupportedFormat && got.Fault != MediaFaultUndecodable {
		t.Errorf("real probe of plain-text content named .mp3: got Fault=%q, want unsupported_format or undecodable", got.Fault)
	}
}

// TestProbeAssetRealDecodeHashMismatchNeverInvokesDecoder proves the
// identity re-check (ruling 2) runs before any decode attempt, against the
// real filesystem, without needing gst-launch-1.0 at all — this test never
// skips.
func TestProbeAssetRealDecodeHashMismatchNeverInvokesDecoder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.wav")
	content := writeMinimalWAV(t, path, 8000, 8000)
	ref := refFor(t, path, content)
	ref.ContentHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	if got.State != MediaFaulted || got.Fault != MediaFaultHashMismatch {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=hash_mismatch", got)
	}
}

// TestProbeAssetRealDecodeMissingFileNeverInvokesDecoder proves the
// missing-file check runs first against the real filesystem — never skips.
func TestProbeAssetRealDecodeMissingFileNeverInvokesDecoder(t *testing.T) {
	dir := t.TempDir()
	ref := pkgaudio.MediaRef{AssetID: "ghost", ContentHash: "sha256:x", SizeBytes: 1, RuntimeFilename: "ghost.wav"}

	got := ProbeAsset(context.Background(), dir, ref, RealDecoder{})

	if got.State != MediaFaulted || got.Fault != MediaFaultMissing {
		t.Fatalf("ProbeAsset = %+v, want State=fault Fault=missing", got)
	}
}
