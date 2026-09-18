package audiorendition

import (
	"errors"
	"fmt"
	"io"
)

// RenditionFormat is the fixed value recorded for every rendition [Render]
// produces, independent of the source [Format] it was decoded from.
const RenditionFormat = "wav48k16s"

const (
	renditionSampleRate    = 48000
	renditionChannels      = 2
	renditionBitsPerSample = 16
)

// Rendition is [Render]'s result.
type Rendition struct {
	// WAV is a complete RIFF PCM file: 48 kHz, 16-bit, stereo.
	WAV []byte
	// DurationMillis is WAV's own playable duration, computed from its
	// final frame count at 48 kHz, never estimated from the source.
	DurationMillis int64
}

// Render fully decodes r (already identified as format by [DetectFormat]),
// duplicates a mono source to both channels, resamples to 48 kHz with a
// windowed-sinc filter when the source rate differs, and encodes the
// result as [RenditionFormat]. It is never called from a request handler,
// see [Service].
func Render(r io.Reader, format Format) (Rendition, error) {
	var decoded Decoded
	var err error
	switch format {
	case FormatWAV:
		decoded, err = decodeWAV(r)
	case FormatMP3:
		decoded, err = decodeMP3(r)
	case FormatFLAC:
		decoded, err = decodeFLAC(r)
	case FormatOggVorbis:
		decoded, err = decodeOggVorbis(r)
	default:
		return Rendition{}, fmt.Errorf("audiorendition: render: unknown format %q", format)
	}
	if err != nil {
		return Rendition{}, fmt.Errorf("audiorendition: %w", err)
	}
	if decoded.Channels == 0 || len(decoded.Samples) == 0 || len(decoded.Samples[0]) == 0 {
		return Rendition{}, errors.New("audiorendition: decoded zero audio samples")
	}

	stereo := toStereo(decoded.Samples)
	stereo = resample(stereo, decoded.SampleRate, renditionSampleRate)

	wav := encodeWAV48k16Stereo(stereo)
	durationMillis := int64(len(stereo[0])) * 1000 / renditionSampleRate
	return Rendition{WAV: wav, DurationMillis: durationMillis}, nil
}

// toStereo duplicates a mono source to both channels, passes a stereo
// source through unchanged, and downmixes a source with more than two
// channels by keeping only the first two: the show's audio path is stereo
// end to end, and there is no operator-facing channel-selection concept to
// honor for a wider source.
func toStereo(samples [][]float64) [][]float64 {
	switch len(samples) {
	case 1:
		return [][]float64{samples[0], append([]float64(nil), samples[0]...)}
	case 2:
		return samples
	default:
		return [][]float64{samples[0], samples[1]}
	}
}
