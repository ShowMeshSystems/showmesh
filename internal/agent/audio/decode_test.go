package audio

import (
	"context"
	"testing"
	"time"
)

func withResolveGstLaunch(t *testing.T, fn func() (string, bool, string)) {
	t.Helper()
	orig := resolveGstLaunch
	resolveGstLaunch = fn
	t.Cleanup(func() { resolveGstLaunch = orig })
}

func withResolveGstDiscoverer(t *testing.T, fn func() (string, bool, string)) {
	t.Helper()
	orig := resolveGstDiscoverer
	resolveGstDiscoverer = fn
	t.Cleanup(func() { resolveGstDiscoverer = orig })
}

// The following are real gst-launch-1.0 -v captures (1.26.2, arm64, Debian
// 13) of this package's bounded confirmation pipeline
// (filesrc ! decodebin ! audioconvert ! audioresample !
// capsfilter(format=S16LE) ! fakesink num-buffers=8), against a 10-second
// 44.1kHz stereo WAV/FLAC pair and four fixtures ruling 3 names: a WAV
// truncated inside its own header, a FLAC truncated inside its metadata
// (header and payload further apart than WAV's), plain text named .mp3,
// and 2000 bytes of /dev/urandom.

const boundedValidWav = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = audio/x-wav
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = NULL
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstWavParse:wavparse0.GstPad:src: caps = audio/x-raw, format=(string)S16LE, layout=(string)interleaved, channels=(int)2, channel-mask=(bitmask)0x0000000000000003, rate=(int)44100
Pipeline is PREROLLED ...
Setting pipeline to PLAYING ...
Got EOS from element "pipeline0".
Setting pipeline to NULL ...
Freeing pipeline ...
`

const boundedValidFlac = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = audio/x-flac
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = NULL
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacDec:flacdec0.GstPad:src: caps = audio/x-raw, format=(string)S16LE, layout=(string)interleaved, rate=(int)44100, channels=(int)2, channel-mask=(bitmask)0x0000000000000003
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacDec:flacdec0.GstPad:sink: caps = audio/x-flac, channels=(int)2, framed=(boolean)true, rate=(int)44100
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacParse:flacparse0.GstPad:src: caps = audio/x-flac, channels=(int)2, framed=(boolean)true, rate=(int)44100
Pipeline is PREROLLED ...
Setting pipeline to PLAYING ...
Got EOS from element "pipeline0".
Setting pipeline to NULL ...
Freeing pipeline ...
`

// boundedTruncatedHeaderWav: a WAV cut inside the "fmt " chunk itself.
// EXIT=1: wavparse never announces a raw-audio pad at all.
const boundedTruncatedHeaderWav = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = audio/x-wav
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = NULL
ERROR: from element /GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstWavParse:wavparse0: The stream is of a different type than handled by this element.
ERROR: pipeline doesn't want to preroll.
Setting pipeline to NULL ...
Freeing pipeline ...
`

// boundedTruncatedFlac: a FLAC cut to 200 bytes, inside its own metadata
// blocks, well before any encoded frame. This is the decisive ruling-3
// capture: flacdec announces its raw-audio src pad caps (it knows the
// format from STREAMINFO before decoding a single frame) but then ERRORs
// with "No valid frames decoded before end of stream" and the pipeline
// never reaches PLAYING — so the caps line alone is not proof of
// decodability; see [decodedAudioCapsPattern]'s own doc comment.
const boundedTruncatedFlac = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = audio/x-flac
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = NULL
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacDec:flacdec0.GstPad:src: caps = audio/x-raw, format=(string)S16LE, layout=(string)interleaved, rate=(int)44100, channels=(int)2, channel-mask=(bitmask)0x0000000000000003
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacDec:flacdec0.GstPad:sink: caps = audio/x-flac, channels=(int)2, framed=(boolean)true, rate=(int)44100
ERROR: from element /GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstFlacDec:flacdec0: No valid frames decoded before end of stream
ERROR: pipeline doesn't want to preroll.
Setting pipeline to NULL ...
Freeing pipeline ...
`

const boundedNotAudioMp3 = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
ERROR: from element /GstPipeline:pipeline0/GstDecodeBin:decodebin0: This appears to be a text file
ERROR: pipeline doesn't want to preroll.
/GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind.GstPad:src: caps = text/plain
ERROR: from element /GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind: Internal data stream error.
ERROR: pipeline doesn't want to preroll.
Setting pipeline to NULL ...
Freeing pipeline ...
`

const boundedGarbage = `Setting pipeline to PAUSED ...
Pipeline is PREROLLING ...
ERROR: from element /GstPipeline:pipeline0/GstDecodeBin:decodebin0/GstTypeFindElement:typefind: Could not determine type of stream.
ERROR: pipeline doesn't want to preroll.
Setting pipeline to NULL ...
Freeing pipeline ...
`

func TestParseBoundedDecodeOutputValidWav(t *testing.T) {
	got := parseBoundedDecodeOutput(boundedValidWav, true)
	if !got.TypeIdentified || got.MIMEType != "audio/x-wav" {
		t.Fatalf("TypeIdentified/MIMEType = %v/%q, want true/audio/x-wav", got.TypeIdentified, got.MIMEType)
	}
	if !got.Decoded || got.Codec != "WavParse" || got.Channels != 2 || got.SampleRate != 44100 {
		t.Fatalf("Decoded/Codec/Channels/SampleRate = %v/%q/%d/%d, want true/WavParse/2/44100",
			got.Decoded, got.Codec, got.Channels, got.SampleRate)
	}
}

func TestParseBoundedDecodeOutputValidFlacUsesTheDecoderLineNotTheParserLine(t *testing.T) {
	// FLAC's decodebin chain has TWO children announcing raw-ish caps
	// (flacparse still audio/x-flac, flacdec audio/x-raw) — this proves
	// the pattern picks the actual raw-audio announcement, not the parser.
	got := parseBoundedDecodeOutput(boundedValidFlac, true)
	if got.Codec != "FlacDec" {
		t.Fatalf("Codec = %q, want FlacDec", got.Codec)
	}
	if got.Channels != 2 || got.SampleRate != 44100 {
		t.Fatalf("Channels/SampleRate = %d/%d, want 2/44100", got.Channels, got.SampleRate)
	}
}

func TestParseBoundedDecodeOutputTruncatedHeaderWavNeverDecoded(t *testing.T) {
	got := parseBoundedDecodeOutput(boundedTruncatedHeaderWav, false)
	if !got.TypeIdentified || got.MIMEType != "audio/x-wav" {
		t.Errorf("TypeIdentified/MIMEType = %v/%q, want true/audio/x-wav", got.TypeIdentified, got.MIMEType)
	}
	if got.Decoded {
		t.Error("Decoded = true, want false: wavparse never announced a raw-audio pad")
	}
}

// TestParseBoundedDecodeOutputTruncatedFlacCapsLineAloneIsNotDecoded is
// this package's own proof of ruling 3: the raw-audio caps line IS present
// in this capture, but exitedZero is false (flacdec errored and the
// pipeline never reached PLAYING), so Decoded must still be false.
func TestParseBoundedDecodeOutputTruncatedFlacCapsLineAloneIsNotDecoded(t *testing.T) {
	got := parseBoundedDecodeOutput(boundedTruncatedFlac, false)
	if !got.TypeIdentified || got.MIMEType != "audio/x-flac" {
		t.Errorf("TypeIdentified/MIMEType = %v/%q, want true/audio/x-flac", got.TypeIdentified, got.MIMEType)
	}
	if got.Decoded {
		t.Fatal("Decoded = true, want false: a caps announcement without PLAYING/exit-zero must not count as decoded")
	}
}

func TestParseBoundedDecodeOutputNotAudioIdentifiedAsTextPlain(t *testing.T) {
	got := parseBoundedDecodeOutput(boundedNotAudioMp3, false)
	if !got.TypeIdentified || got.MIMEType != "text/plain" {
		t.Errorf("TypeIdentified/MIMEType = %v/%q, want true/text/plain", got.TypeIdentified, got.MIMEType)
	}
	if got.Decoded {
		t.Error("Decoded = true, want false")
	}
}

func TestParseBoundedDecodeOutputGarbageNeverIdentifiesAType(t *testing.T) {
	got := parseBoundedDecodeOutput(boundedGarbage, false)
	if got.TypeIdentified {
		t.Errorf("TypeIdentified = true (MIMEType=%q), want false: no typefind line was ever emitted", got.MIMEType)
	}
}

// The following are real gst-discoverer-1.0 -v captures against the same
// fixtures.

const discoverValidWav = `Analyzing file:///data/valid.wav
Done discovering file:///data/valid.wav

Properties:
  Duration: 0:00:10.000000000
  Seekable: yes
  Live: no
  Tags: 
      container format: WAV
      bitrate: 1411200
      audio codec: Uncompressed 16-bit PCM audio
  audio #0: audio/x-wav
    Channels: 2 (front-left, front-right)
    Sample rate: 44100
`

const discoverValidFlac = `Analyzing file:///data/valid.flac
Done discovering file:///data/valid.flac

Properties:
  Duration: 0:00:10.000000000
  Seekable: yes
  Live: no
  Tags: 
      audio codec: Free Lossless Audio Codec (FLAC)
  audio #0: audio/x-flac, channels=(int)2, framed=(boolean)true, rate=(int)44100
    Channels: 2 (front-left, front-right)
    Sample rate: 44100
`

const discoverTruncatedHeaderWav = `Analyzing file:///data/truncated_header.wav
Done discovering file:///data/truncated_header.wav
An error was encountered while discovering the file
 The stream is of a different type than handled by this element.
`

// discoverTruncatedFlac is the decisive capture behind ruling 1/3 together:
// gst-discoverer-1.0 reports BOTH an explicit error AND a full
// Duration/Channels/Sample-rate block from the intact STREAMINFO metadata
// of a FLAC truncated to 200 bytes (well before any encoded frame) — proof
// that a metadata block surviving truncation is not proof of decodability.
const discoverTruncatedFlac = `Analyzing file:///data/truncated.flac
Done discovering file:///data/truncated.flac
An error was encountered while discovering the file
 No valid frames decoded before end of stream

Properties:
  Duration: 0:00:10.000000000
  Seekable: yes
  Live: no
  audio #0: audio/x-flac, channels=(int)2, framed=(boolean)true, rate=(int)44100
    Channels: 2 (front-left, front-right)
    Sample rate: 44100
`

const discoverNotAudioMp3 = `Analyzing file:///data/notaudio.mp3
Done discovering file:///data/notaudio.mp3
An error was encountered while discovering the file
 This appears to be a text file
`

func TestParseDiscovererOutputValidWav(t *testing.T) {
	got := parseDiscovererOutput(discoverValidWav)
	if !got.Ran || got.Errored {
		t.Fatalf("Ran/Errored = %v/%v, want true/false", got.Ran, got.Errored)
	}
	if got.Duration != 10*time.Second {
		t.Errorf("Duration = %v, want 10s", got.Duration)
	}
	if got.Codec != "Uncompressed 16-bit PCM audio" {
		t.Errorf("Codec = %q, want the audio-codec tag text", got.Codec)
	}
	if got.Channels != 2 || got.SampleRate != 44100 {
		t.Errorf("Channels/SampleRate = %d/%d, want 2/44100", got.Channels, got.SampleRate)
	}
}

func TestParseDiscovererOutputValidFlac(t *testing.T) {
	got := parseDiscovererOutput(discoverValidFlac)
	if !got.Ran || got.Errored {
		t.Fatalf("Ran/Errored = %v/%v, want true/false", got.Ran, got.Errored)
	}
	if got.Duration != 10*time.Second {
		t.Errorf("Duration = %v, want 10s", got.Duration)
	}
	if got.Codec != "Free Lossless Audio Codec (FLAC)" {
		t.Errorf("Codec = %q, want the FLAC audio-codec tag text", got.Codec)
	}
}

func TestParseDiscovererOutputTruncatedHeaderWavErrorsWithNoProperties(t *testing.T) {
	got := parseDiscovererOutput(discoverTruncatedHeaderWav)
	if !got.Ran || !got.Errored {
		t.Fatalf("Ran/Errored = %v/%v, want true/true", got.Ran, got.Errored)
	}
	if got.Reason != "The stream is of a different type than handled by this element." {
		t.Errorf("Reason = %q", got.Reason)
	}
	if got.Duration != 0 {
		t.Errorf("Duration = %v, want 0 (no Properties block was ever printed)", got.Duration)
	}
}

// TestParseDiscovererOutputTruncatedFlacReportsBothAnErrorAndAFullDuration
// is this package's proof, at the discoverer layer, of exactly what ruling
// 3 warns about: Errored is true AND Duration is the full 10s from
// STREAMINFO, in the SAME result. A caller trusting Duration without
// checking Errored would be wrong.
func TestParseDiscovererOutputTruncatedFlacReportsBothAnErrorAndAFullDuration(t *testing.T) {
	got := parseDiscovererOutput(discoverTruncatedFlac)
	if !got.Errored {
		t.Fatal("Errored = false, want true")
	}
	if got.Duration != 10*time.Second {
		t.Errorf("Duration = %v, want the metadata's own (misleading) 10s — proving classifyDecode must not trust Duration alone", got.Duration)
	}
}

func TestParseDiscovererOutputNotAudio(t *testing.T) {
	got := parseDiscovererOutput(discoverNotAudioMp3)
	if !got.Errored {
		t.Fatal("Errored = false, want true")
	}
	if got.Reason != "This appears to be a text file" {
		t.Errorf("Reason = %q", got.Reason)
	}
}

func TestRealDecoderMissingGstLaunchReportsUnavailable(t *testing.T) {
	withResolveGstLaunch(t, func() (string, bool, string) { return "", false, "gst-launch-1.0 not found on PATH" })

	got := RealDecoder{}.Decode(context.Background(), "/does/not/matter")
	if got.Available {
		t.Fatal("Available = true, want false when gst-launch-1.0 cannot be resolved")
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want an explanation")
	}
}

func TestRealDecoderNeverInvokesDiscovererWhenUnresolvable(t *testing.T) {
	withResolveGstDiscoverer(t, func() (string, bool, string) { return "", false, "gst-discoverer-1.0 not found" })
	withResolveGstLaunch(t, func() (string, bool, string) { return "", false, "gst-launch-1.0 not found" })

	got := RealDecoder{}.Decode(context.Background(), "/does/not/matter")
	if got.Discoverer.Ran {
		t.Error("Discoverer.Ran = true, want false: gst-discoverer-1.0 was reported unresolvable")
	}
}
