package audiorendition

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

// buildTestWAV encodes deinterleaved per-channel int16 samples as a
// minimal RIFF/WAVE PCM file at sampleRate, for use as a small generated
// input fixture, never committed media.
func buildTestWAV(sampleRate int, samples [][]int16) []byte {
	channels := len(samples)
	numFrames := 0
	if channels > 0 {
		numFrames = len(samples[0])
	}
	dataSize := numFrames * channels * 2

	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], wavFormatPCM)
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels*2))
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))

	off := 44
	for i := 0; i < numFrames; i++ {
		for c := 0; c < channels; c++ {
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(samples[c][i]))
			off += 2
		}
	}
	return buf
}

// sineInt16 returns n samples of a sine wave at freq Hz, sampleRate Hz,
// amplitude scaled to roughly half full-scale.
func sineInt16(n, sampleRate int, freq float64) []int16 {
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(16000 * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
	}
	return out
}

func TestRenderMonoWAVDuplicatesToBothChannels(t *testing.T) {
	const sampleRate = 8000
	const n = 4000 // 500ms
	mono := sineInt16(n, sampleRate, 220)
	wav := buildTestWAV(sampleRate, [][]int16{mono})

	format, err := DetectFormat(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}
	if format != FormatWAV {
		t.Fatalf("DetectFormat = %q, want %q", format, FormatWAV)
	}

	rendition, err := Render(bytes.NewReader(wav), format)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	decoded, err := decodeWAV(bytes.NewReader(rendition.WAV))
	if err != nil {
		t.Fatalf("decode produced rendition: %v", err)
	}
	if decoded.SampleRate != renditionSampleRate {
		t.Errorf("rendition sample rate = %d, want %d", decoded.SampleRate, renditionSampleRate)
	}
	if decoded.Channels != 2 {
		t.Fatalf("rendition channels = %d, want 2", decoded.Channels)
	}
	if len(decoded.Samples[0]) != len(decoded.Samples[1]) {
		t.Fatalf("channel lengths differ: %d vs %d", len(decoded.Samples[0]), len(decoded.Samples[1]))
	}
	for i := range decoded.Samples[0] {
		if decoded.Samples[0][i] != decoded.Samples[1][i] {
			t.Fatalf("mono source not duplicated identically at frame %d: left=%v right=%v", i, decoded.Samples[0][i], decoded.Samples[1][i])
		}
	}

	wantMillis := int64(n) * 1000 / sampleRate
	if diff := rendition.DurationMillis - wantMillis; diff < -5 || diff > 5 {
		t.Errorf("duration = %dms, want approximately %dms", rendition.DurationMillis, wantMillis)
	}
}

func TestRenderStereo44100ResamplesTo48kAndKeepsChannelsDistinct(t *testing.T) {
	const sampleRate = 44100
	const n = 44100 // 1s
	left := sineInt16(n, sampleRate, 440)
	right := sineInt16(n, sampleRate, 880)
	wav := buildTestWAV(sampleRate, [][]int16{left, right})

	format, err := DetectFormat(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}

	rendition, err := Render(bytes.NewReader(wav), format)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	decoded, err := decodeWAV(bytes.NewReader(rendition.WAV))
	if err != nil {
		t.Fatalf("decode produced rendition: %v", err)
	}
	if decoded.SampleRate != renditionSampleRate {
		t.Errorf("rendition sample rate = %d, want %d", decoded.SampleRate, renditionSampleRate)
	}

	wantFrames := int(math.Round(float64(n) * renditionSampleRate / sampleRate))
	if diff := len(decoded.Samples[0]) - wantFrames; diff < -2 || diff > 2 {
		t.Errorf("rendition frame count = %d, want approximately %d", len(decoded.Samples[0]), wantFrames)
	}

	wantMillis := int64(n) * 1000 / sampleRate
	if diff := rendition.DurationMillis - wantMillis; diff < -5 || diff > 5 {
		t.Errorf("duration = %dms, want approximately %dms", rendition.DurationMillis, wantMillis)
	}

	// The two channels carry different tones; a resampler that collapsed
	// them (or a nearest-sample implementation badly aliasing one of them)
	// would not reproduce this.
	var sumAbsDiff float64
	for i := range decoded.Samples[0] {
		sumAbsDiff += math.Abs(decoded.Samples[0][i] - decoded.Samples[1][i])
	}
	if sumAbsDiff/float64(len(decoded.Samples[0])) < 0.05 {
		t.Errorf("left and right channels look collapsed after resampling: mean abs diff = %v", sumAbsDiff/float64(len(decoded.Samples[0])))
	}
}

func TestRenderAlreadyAt48kWAVPreservesFrameCountExactly(t *testing.T) {
	const sampleRate = renditionSampleRate
	const n = 2000
	left := sineInt16(n, sampleRate, 300)
	right := sineInt16(n, sampleRate, 300)
	wav := buildTestWAV(sampleRate, [][]int16{left, right})

	format, err := DetectFormat(bytes.NewReader(wav))
	if err != nil {
		t.Fatalf("DetectFormat: %v", err)
	}

	rendition, err := Render(bytes.NewReader(wav), format)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	decoded, err := decodeWAV(bytes.NewReader(rendition.WAV))
	if err != nil {
		t.Fatalf("decode produced rendition: %v", err)
	}
	if len(decoded.Samples[0]) != n {
		t.Errorf("frame count = %d, want exactly %d (no resample should have run)", len(decoded.Samples[0]), n)
	}

	wantMillis := int64(n) * 1000 / sampleRate
	if rendition.DurationMillis != wantMillis {
		t.Errorf("duration = %dms, want exactly %dms", rendition.DurationMillis, wantMillis)
	}
}

func TestDetectFormatRefusesUndecodableInput(t *testing.T) {
	garbage := bytes.Repeat([]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}, 64)

	_, err := DetectFormat(bytes.NewReader(garbage))
	if err == nil {
		t.Fatal("DetectFormat on undecodable input succeeded, want an error")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("error = %v, want it to wrap ErrUnsupportedFormat", err)
	}
	for _, want := range []string{"WAV", "MP3", "FLAC", "Ogg Vorbis"} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Errorf("error %q does not name supported format %q", err.Error(), want)
		}
	}
}

func TestDetectFormatRefusesTruncatedRIFFHeader(t *testing.T) {
	_, err := DetectFormat(bytes.NewReader([]byte("RIFF")))
	if err == nil {
		t.Fatal("DetectFormat on a truncated RIFF header succeeded, want an error")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("error = %v, want it to wrap ErrUnsupportedFormat", err)
	}
}
