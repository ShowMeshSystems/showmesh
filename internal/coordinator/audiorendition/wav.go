package audiorendition

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// wavFormat is a parsed "fmt " chunk's fields this package needs.
type wavFormat struct {
	audioFormat   uint16
	channels      uint16
	sampleRate    uint32
	bitsPerSample uint16
}

// wavFormatPCM and wavFormatIEEEFloat are RIFF WAVE's own audioFormat tag
// values. wavFormatExtensible marks a WAVE_FORMAT_EXTENSIBLE header, whose
// real tag lives in the first two bytes of its 16-byte SubFormat GUID.
const (
	wavFormatPCM        = 1
	wavFormatIEEEFloat  = 3
	wavFormatExtensible = 0xFFFE
)

// parseWAVChunks reads r fully and returns its "fmt " fields and its
// "data" chunk's raw bytes. r is read whole rather than chunk-by-chunk:
// RIFF chunks are not required to appear in a fixed order, so the "data"
// chunk's size cannot be trusted without also having located "fmt " first.
func parseWAVChunks(r io.Reader) (wavFormat, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return wavFormat{}, nil, err
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return wavFormat{}, nil, errors.New("not a RIFF/WAVE file")
	}

	var format wavFormat
	var pcm []byte
	haveFormat, havePCM := false, false

	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		if size < 0 || body+size > len(data) {
			size = len(data) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return wavFormat{}, nil, errors.New("fmt chunk is smaller than 16 bytes")
			}
			chunk := data[body : body+size]
			format.audioFormat = binary.LittleEndian.Uint16(chunk[0:2])
			format.channels = binary.LittleEndian.Uint16(chunk[2:4])
			format.sampleRate = binary.LittleEndian.Uint32(chunk[4:8])
			format.bitsPerSample = binary.LittleEndian.Uint16(chunk[14:16])
			if format.audioFormat == wavFormatExtensible && size >= 40 {
				format.audioFormat = binary.LittleEndian.Uint16(chunk[24:26])
			}
			haveFormat = true
		case "data":
			pcm = data[body : body+size]
			havePCM = true
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // chunks are word-aligned; an odd chunk size has a pad byte
		}
	}
	if !haveFormat {
		return wavFormat{}, nil, errors.New(`missing "fmt " chunk`)
	}
	if !havePCM {
		return wavFormat{}, nil, errors.New(`missing "data" chunk`)
	}
	return format, pcm, nil
}

// probeWAVHeader is [DetectFormat]'s cheap WAV validation: it parses the
// chunk structure and format tag but decodes no samples.
func probeWAVHeader(r io.Reader) (wavFormat, error) {
	format, _, err := parseWAVChunks(r)
	if err != nil {
		return wavFormat{}, err
	}
	if _, err := wavSampleReader(format); err != nil {
		return wavFormat{}, err
	}
	return format, nil
}

// wavSampleReader returns a function decoding one sample (format's own
// byte width) into [-1, 1], or an error naming why format is unsupported.
func wavSampleReader(format wavFormat) (func(b []byte) float64, error) {
	switch {
	case format.audioFormat == wavFormatPCM && format.bitsPerSample == 8:
		return func(b []byte) float64 { return (float64(b[0]) - 128) / 128 }, nil
	case format.audioFormat == wavFormatPCM && format.bitsPerSample == 16:
		return func(b []byte) float64 { return float64(int16(binary.LittleEndian.Uint16(b))) / 32768 }, nil
	case format.audioFormat == wavFormatPCM && format.bitsPerSample == 24:
		return func(b []byte) float64 {
			v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
			if v&0x00800000 != 0 {
				v -= 1 << 24
			}
			return float64(v) / 8388608
		}, nil
	case format.audioFormat == wavFormatPCM && format.bitsPerSample == 32:
		return func(b []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648 }, nil
	case format.audioFormat == wavFormatIEEEFloat && format.bitsPerSample == 32:
		return func(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }, nil
	case format.audioFormat == wavFormatIEEEFloat && format.bitsPerSample == 64:
		return func(b []byte) float64 { return math.Float64frombits(binary.LittleEndian.Uint64(b)) }, nil
	default:
		return nil, fmt.Errorf("unsupported WAV sample format (audioFormat=%d, bitsPerSample=%d)", format.audioFormat, format.bitsPerSample)
	}
}

// decodeWAV fully decodes r as a RIFF/WAVE PCM or IEEE-float file.
func decodeWAV(r io.Reader) (Decoded, error) {
	format, pcm, err := parseWAVChunks(r)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode wav: %w", err)
	}
	if format.channels == 0 {
		return Decoded{}, errors.New("decode wav: zero channels")
	}
	readSample, err := wavSampleReader(format)
	if err != nil {
		return Decoded{}, fmt.Errorf("decode wav: %w", err)
	}

	bytesPerSample := int(format.bitsPerSample) / 8
	frameSize := bytesPerSample * int(format.channels)
	if frameSize == 0 {
		return Decoded{}, errors.New("decode wav: invalid frame size")
	}
	numFrames := len(pcm) / frameSize

	samples := make([][]float64, format.channels)
	for c := range samples {
		samples[c] = make([]float64, numFrames)
	}
	for i := 0; i < numFrames; i++ {
		base := i * frameSize
		for c := 0; c < int(format.channels); c++ {
			off := base + c*bytesPerSample
			samples[c][i] = readSample(pcm[off : off+bytesPerSample])
		}
	}
	if numFrames == 0 {
		return Decoded{}, errors.New("decode wav: no audio samples decoded")
	}
	return Decoded{SampleRate: int(format.sampleRate), Channels: int(format.channels), Samples: samples}, nil
}

// encodeWAV48k16Stereo encodes samples (exactly 2 equal-length channels,
// already at [renditionSampleRate]) as a RIFF PCM WAV file: 16-bit signed
// little-endian, stereo, 48 kHz: [RenditionFormat]'s own fixed contract.
func encodeWAV48k16Stereo(samples [][]float64) []byte {
	numFrames := 0
	if len(samples) > 0 {
		numFrames = len(samples[0])
	}
	const channels = renditionChannels
	const bitsPerSample = renditionBitsPerSample
	bytesPerSample := bitsPerSample / 8
	dataSize := numFrames * channels * bytesPerSample

	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], wavFormatPCM)
	binary.LittleEndian.PutUint16(buf[22:24], channels)
	binary.LittleEndian.PutUint32(buf[24:28], renditionSampleRate)
	byteRate := renditionSampleRate * channels * bytesPerSample
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*bytesPerSample))
	binary.LittleEndian.PutUint16(buf[34:36], bitsPerSample)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))

	off := 44
	for i := 0; i < numFrames; i++ {
		for c := 0; c < channels; c++ {
			v := samples[c][i]
			switch {
			case v > 1:
				v = 1
			case v < -1:
				v = -1
			}
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(int16(math.Round(v*32767))))
			off += 2
		}
	}
	return buf
}
