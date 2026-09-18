package audiorendition

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
	"github.com/mewkiz/flac"
)

// Format is one input audio container/codec this package can decode.
type Format string

const (
	FormatWAV       Format = "wav"
	FormatMP3       Format = "mp3"
	FormatFLAC      Format = "flac"
	FormatOggVorbis Format = "ogg_vorbis"
)

// SupportedFormatsDescription names every format [DetectFormat] accepts,
// for an operator-facing refusal reason.
const SupportedFormatsDescription = "WAV, MP3, FLAC, and Ogg Vorbis"

// ErrUnsupportedFormat is wrapped by every error [DetectFormat] returns.
var ErrUnsupportedFormat = errors.New("audiorendition: unsupported audio format")

// Decoded is one fully-decoded audio source: Samples[c][i] is channel c's
// sample i, normalized to [-1, 1].
type Decoded struct {
	SampleRate int
	Channels   int
	Samples    [][]float64
}

// DetectFormat identifies r's audio container/codec from its own content,
// never from a filename, reading only what each format's header needs
// (a magic prefix, plus a metadata-only parse for FLAC and Ogg Vorbis, plus
// locating the first frame for MP3, which also skips a leading ID3 tag).
// It never decodes audio samples. r must support Seek: every candidate
// probe rewinds it before the next one runs, and the caller is expected to
// rewind it again before a subsequent full [Render] pass.
func DetectFormat(r io.ReadSeeker) (Format, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("audiorendition: detect format: %w", err)
	}
	var magic [12]byte
	n, _ := io.ReadFull(r, magic[:])

	switch {
	case n >= 12 && string(magic[0:4]) == "RIFF" && string(magic[8:12]) == "WAVE":
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("audiorendition: detect format: %w", err)
		}
		if _, err := probeWAVHeader(r); err != nil {
			return "", fmt.Errorf("%w: not a valid WAV file: %v; supported formats are %s", ErrUnsupportedFormat, err, SupportedFormatsDescription)
		}
		return FormatWAV, nil
	case n >= 4 && string(magic[0:4]) == "fLaC":
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("audiorendition: detect format: %w", err)
		}
		stream, err := flac.New(r)
		if err != nil {
			return "", fmt.Errorf("%w: not a valid FLAC file: %v; supported formats are %s", ErrUnsupportedFormat, err, SupportedFormatsDescription)
		}
		_ = stream.Close()
		return FormatFLAC, nil
	case n >= 4 && string(magic[0:4]) == "OggS":
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("audiorendition: detect format: %w", err)
		}
		if _, err := oggvorbis.NewReader(r); err != nil {
			return "", fmt.Errorf("%w: not a valid Ogg Vorbis file: %v; supported formats are %s", ErrUnsupportedFormat, err, SupportedFormatsDescription)
		}
		return FormatOggVorbis, nil
	}

	// No recognized magic prefix: the only remaining supported format is
	// MP3, which has none of its own (a bare frame sync, or a leading ID3
	// tag of arbitrary content). mp3.NewDecoder locates the first frame
	// itself, skipping any ID3 tag as it does, so no separate frame-sync
	// scan is needed here.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("audiorendition: detect format: %w", err)
	}
	if _, err := mp3.NewDecoder(r); err == nil {
		return FormatMP3, nil
	}

	return "", fmt.Errorf("%w; supported formats are %s", ErrUnsupportedFormat, SupportedFormatsDescription)
}

// decodeMP3 fully decodes r as MP3 via go-mp3, which always produces
// 16-bit little-endian stereo PCM regardless of the source's own channel
// count, so mono duplication for MP3 is already done by the library.
func decodeMP3(r io.Reader) (Decoded, error) {
	dec, err := mp3.NewDecoder(r)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode mp3: %w", err)
	}
	raw, err := io.ReadAll(dec)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode mp3: %w", err)
	}
	if len(raw) < 4 {
		return Decoded{}, errors.New("decode mp3: no audio samples decoded")
	}
	frames := len(raw) / 4
	left := make([]float64, frames)
	right := make([]float64, frames)
	for i := 0; i < frames; i++ {
		left[i] = float64(int16(binary.LittleEndian.Uint16(raw[i*4:]))) / 32768
		right[i] = float64(int16(binary.LittleEndian.Uint16(raw[i*4+2:]))) / 32768
	}
	return Decoded{SampleRate: dec.SampleRate(), Channels: 2, Samples: [][]float64{left, right}}, nil
}

// decodeFLAC fully decodes r as FLAC via mewkiz/flac. Stream.ParseNext
// already inter-channel-correlates a frame's subframes back to plain
// per-channel samples (see frame.Frame.Parse's own doc comment), so no
// further decorrelation is needed here.
func decodeFLAC(r io.Reader) (Decoded, error) {
	stream, err := flac.New(r)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode flac: %w", err)
	}
	defer func() { _ = stream.Close() }()

	channels := int(stream.Info.NChannels)
	if channels <= 0 {
		return Decoded{}, errors.New("decode flac: invalid channel count")
	}
	bits := int(stream.Info.BitsPerSample)
	if bits <= 0 || bits > 32 {
		return Decoded{}, fmt.Errorf("decode flac: unsupported bits-per-sample %d", bits)
	}
	scale := float64(int64(1) << uint(bits-1))

	samples := make([][]float64, channels)
	for {
		f, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Decoded{}, fmt.Errorf("decode flac: %w", err)
		}
		for c := 0; c < channels && c < len(f.Subframes); c++ {
			for _, s := range f.Subframes[c].Samples {
				samples[c] = append(samples[c], float64(s)/scale)
			}
		}
	}
	if len(samples[0]) == 0 {
		return Decoded{}, errors.New("decode flac: no audio samples decoded")
	}
	return Decoded{SampleRate: int(stream.Info.SampleRate), Channels: channels, Samples: samples}, nil
}

// decodeOggVorbis fully decodes r as Ogg Vorbis via jfreymuth/oggvorbis,
// which yields interleaved float32 samples already in [-1, 1].
func decodeOggVorbis(r io.Reader) (Decoded, error) {
	dec, err := oggvorbis.NewReader(r)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode ogg vorbis: %w", err)
	}
	channels := dec.Channels()
	if channels <= 0 {
		return Decoded{}, errors.New("decode ogg vorbis: invalid channel count")
	}

	samples := make([][]float64, channels)
	buf := make([]float32, 4096*channels)
	var carry []float32
	for {
		n, err := dec.Read(buf)
		if n > 0 {
			carry = append(carry, buf[:n]...)
			frames := len(carry) / channels
			for i := 0; i < frames; i++ {
				for c := 0; c < channels; c++ {
					samples[c] = append(samples[c], float64(carry[i*channels+c]))
				}
			}
			carry = append([]float32(nil), carry[frames*channels:]...)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Decoded{}, fmt.Errorf("decode ogg vorbis: %w", err)
		}
		if n == 0 {
			break
		}
	}
	if len(samples[0]) == 0 {
		return Decoded{}, errors.New("decode ogg vorbis: no audio samples decoded")
	}
	return Decoded{SampleRate: dec.SampleRate(), Channels: channels, Samples: samples}, nil
}
