package audio

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// boundedDecodeTimeout bounds the confirmation decode's wait for
// gst-launch-1.0 to exit. A var so tests can shrink it.
var boundedDecodeTimeout = 10 * time.Second

// discovererTimeout bounds gst-discoverer-1.0's wait — MEASURED at 11 ms
// against a 10-minute FLAC, so this is generous headroom, not a tuned
// value. A var so tests can shrink it.
var discovererTimeout = 15 * time.Second

// boundedDecodeBufferLimit is the fakesink num-buffers bound the
// confirmation decode runs under: enough raw-audio buffers to prove real
// content is flowing (ruling 3), never the whole file, and never written
// to disk (ruling 2).
const boundedDecodeBufferLimit = 8

// DurationSource names which evidence a Ready or duration_unknown
// [MediaItemResult] rests on, per ADR-011 provenance: a later reader must
// be able to tell "the container's own metadata" from "only a bounded
// decode confirmed this plays, and no duration was measured" apart.
type DurationSource string

const (
	DurationSourceContainerMetadata DurationSource = "container_metadata"
	DurationSourceBoundedDecode     DurationSource = "bounded_decode"
)

// DiscovererEvidence is what one gst-discoverer-1.0 run against a file
// found. Ran is false when gst-discoverer-1.0 was never invoked at all
// (unresolvable on this node) — ruling 5's fallback case; every other
// field is meaningful only when Ran is true.
type DiscovererEvidence struct {
	Ran        bool
	Errored    bool
	Reason     string // gst-discoverer-1.0's own message, when Errored
	Duration   time.Duration
	Codec      string
	Channels   int
	SampleRate int
}

// DecodeResult is what one real probe attempt against a file found:
// TypeIdentified/MIMEType/Decoded/Codec/Channels/SampleRate come from a
// BOUNDED confirmation decode (ruling 2 — never the whole file, never
// written to disk) that always runs; Discoverer carries gst-discoverer-1.0's
// independent, fast metadata when that tool is available (ruling 1).
// Available is false only when the confirmation decode itself could not be
// attempted at all (no gst-launch-1.0) — [ProbeAsset] reports
// [MediaUnknown] for that, never a fault.
type DecodeResult struct {
	Available bool
	Reason    string

	TypeIdentified bool
	MIMEType       string

	Decoded    bool
	Codec      string
	Channels   int
	SampleRate int

	Discoverer DiscovererEvidence
}

// Decoder abstracts one real probe attempt against a file on disk, so
// [ProbeAsset]'s fault classification (ruling 3) is exercised in tests
// against canned [DecodeResult] values, never a live subprocess.
type Decoder interface {
	Decode(ctx context.Context, path string) DecodeResult
}

// RealDecoder is the real, GStreamer-backed [Decoder]. See
// [RealDecoder.Decode].
type RealDecoder struct{}

// typefindCapsPattern matches gst-launch-1.0 -v's first report of what its
// typefind element identified — MEASURED against gst-launch-1.0 1.26.2:
// a WAV file emits ".../GstTypeFindElement:typefind.GstPad:src: caps = audio/x-wav",
// a plain-text file emits "... caps = text/plain", and unidentifiable bytes
// emit no such line at all. Only the bare MIME token is captured.
var typefindCapsPattern = regexp.MustCompile(`GstTypeFindElement:typefind\.GstPad:src: caps = ([a-zA-Z0-9/+.-]+)`)

// decodedAudioCapsPattern matches decodebin's own child element announcing
// a decoded raw-audio pad and captures the rest of that one line — MEASURED:
// ".../GstDecodeBin:decodebin0/GstWavParse:wavparse0.GstPad:src: caps =
// audio/x-raw, format=(string)S16LE, layout=(string)interleaved,
// channels=(int)2, channel-mask=(bitmask)..., rate=(int)44100" for WAV, but
// "..., format=(string)S16LE, layout=(string)interleaved, rate=(int)44100,
// channels=(int)2, channel-mask=..." for FLAC — rate and channels are NOT
// in a fixed order across elements, so [channelsFieldPattern] and
// [sampleRateFieldPattern] are matched against the captured line
// independently rather than as one fixed sequence. The element class name
// (e.g. "WavParse") is this package's only source for Codec in the
// bounded-decode-only path.
//
// MEASURED, decisive for ruling 3: this line alone is not proof of
// decodability — a FLAC file truncated inside its own metadata still
// announces this caps line (flacdec knows the format from STREAMINFO
// before it has decoded a single frame) and then errors out. [Decode]
// additionally requires [playingMarker] and a zero exit before trusting it.
var decodedAudioCapsPattern = regexp.MustCompile(
	`decodebin\d*/Gst(\w+):\S+\.GstPad:src: caps = audio/x-raw, (.*)`)

var channelsFieldPattern = regexp.MustCompile(`channels=\(int\)(\d+)`)
var sampleRateFieldPattern = regexp.MustCompile(`rate=\(int\)(\d+)`)

// Decode implements [Decoder]: gst-discoverer-1.0, when resolvable, is the
// primary evidence source (ruling 1) and typically returns in low tens of
// milliseconds regardless of file length (MEASURED: 11 ms against a
// 10-minute FLAC) because it reads container metadata rather than decoding.
// A bounded confirmation decode (ruling 2: fakesink, num-buffers-limited,
// nothing written to disk) always runs too, because a container's declared
// duration is not proof the content actually decodes (ruling 3) — MEASURED:
// the same 10-minute FLAC's metadata, truncated to 200 bytes, still reports
// gst-discoverer's Duration as unchanged.
func (RealDecoder) Decode(ctx context.Context, path string) DecodeResult {
	gstPath, ok, reason := resolveGstLaunch()
	if !ok {
		return DecodeResult{Available: false, Reason: reason}
	}

	result := runBoundedDecode(ctx, gstPath, path)

	if discovererPath, ok, _ := resolveGstDiscoverer(); ok {
		result.Discoverer = runDiscoverer(ctx, discovererPath, path)
	}

	return result
}

func runBoundedDecode(ctx context.Context, gstPath, path string) DecodeResult {
	ctx, cancel := context.WithTimeout(ctx, boundedDecodeTimeout)
	defer cancel()

	argv := []string{"-v", "filesrc", "location=" + path, "!", "decodebin", "!",
		"audioconvert", "!", "audioresample", "!",
		"capsfilter", "caps=audio/x-raw,format=S16LE", "!",
		"fakesink", "num-buffers=" + strconv.Itoa(boundedDecodeBufferLimit)}
	out, err := exec.CommandContext(ctx, gstPath, argv...).CombinedOutput()
	exitedZero := err == nil

	return parseBoundedDecodeOutput(string(out), exitedZero)
}

func parseBoundedDecodeOutput(out string, exitedZero bool) DecodeResult {
	result := DecodeResult{Available: true}

	if m := typefindCapsPattern.FindStringSubmatch(out); m != nil {
		result.TypeIdentified = true
		result.MIMEType = m[1]
	}

	// exitedZero and playingMarker together are what rule out the FLAC
	// STREAMINFO case documented on [decodedAudioCapsPattern]: a caps
	// announcement alone is not proof anything actually decoded.
	if m := decodedAudioCapsPattern.FindStringSubmatch(out); m != nil && exitedZero && strings.Contains(out, playingMarker) {
		result.Decoded = true
		result.Codec = m[1]
		if cm := channelsFieldPattern.FindStringSubmatch(m[2]); cm != nil {
			result.Channels, _ = strconv.Atoi(cm[1])
		}
		if rm := sampleRateFieldPattern.FindStringSubmatch(m[2]); rm != nil {
			result.SampleRate, _ = strconv.Atoi(rm[1])
		}
	}

	return result
}

func runDiscoverer(ctx context.Context, discovererPath, path string) DiscovererEvidence {
	ctx, cancel := context.WithTimeout(ctx, discovererTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, discovererPath, "-v", path).CombinedOutput()
	if ctx.Err() != nil {
		return DiscovererEvidence{Ran: true, Errored: true, Reason: "gst-discoverer-1.0 did not exit within " + discovererTimeout.String()}
	}
	_ = err // gst-discoverer-1.0 exits 0 even when it reports an error for the file; see parseDiscovererOutput.

	return parseDiscovererOutput(string(out))
}

// discovererErrorMarker is gst-discoverer-1.0's own line announcing that
// discovery failed for the file — MEASURED: the line immediately following
// it is the human-readable reason (e.g. "No valid frames decoded before
// end of stream", "This appears to be a text file", "Could not determine
// type of stream.").
const discovererErrorMarker = "An error was encountered while discovering the file"

// discovererDurationPattern matches gst-discoverer-1.0 -v's
// "Duration: H:MM:SS.nnnnnnnnn" line — MEASURED against 1.26.2.
var discovererDurationPattern = regexp.MustCompile(`Duration: (\d+):(\d{2}):(\d{2})\.(\d{9})`)

// discovererAudioCodecPattern matches the human-readable "audio codec: ..."
// tag line gst-discoverer-1.0 prints (e.g. "Uncompressed 16-bit PCM audio",
// "Free Lossless Audio Codec (FLAC)") — MEASURED, preferred over the raw
// caps string under "Codec:" because it is meant to be read.
var discovererAudioCodecPattern = regexp.MustCompile(`audio codec: (.+)`)

var discovererChannelsPattern = regexp.MustCompile(`Channels: (\d+)`)
var discovererSampleRatePattern = regexp.MustCompile(`Sample rate: (\d+)`)

func parseDiscovererOutput(out string) DiscovererEvidence {
	ev := DiscovererEvidence{Ran: true}

	if idx := strings.Index(out, discovererErrorMarker); idx >= 0 {
		ev.Errored = true
		ev.Reason = discovererErrorReason(out[idx+len(discovererErrorMarker):])
	}

	if m := discovererDurationPattern.FindStringSubmatch(out); m != nil {
		h, _ := strconv.Atoi(m[1])
		mnt, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		ns, _ := strconv.Atoi(m[4])
		ev.Duration = time.Duration(h)*time.Hour + time.Duration(mnt)*time.Minute +
			time.Duration(s)*time.Second + time.Duration(ns)*time.Nanosecond
	}
	if m := discovererAudioCodecPattern.FindStringSubmatch(out); m != nil {
		ev.Codec = strings.TrimSpace(m[1])
	}
	if m := discovererChannelsPattern.FindStringSubmatch(out); m != nil {
		ev.Channels, _ = strconv.Atoi(m[1])
	}
	if m := discovererSampleRatePattern.FindStringSubmatch(out); m != nil {
		ev.SampleRate, _ = strconv.Atoi(m[1])
	}

	return ev
}

// discovererErrorReason returns the first non-empty line after
// [discovererErrorMarker].
func discovererErrorReason(rest string) string {
	for _, line := range strings.Split(rest, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return "gst-discoverer-1.0 reported an error with no further detail"
}
