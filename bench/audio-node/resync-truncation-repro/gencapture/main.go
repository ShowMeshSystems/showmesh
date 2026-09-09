// gencapture builds a "capture-shaped" WAV fixture for control 4 (see
// ../README.md): N ms of exact digital silence, followed by the
// reference sweep with its own first M ms cut off -- in one file, the
// same shape a real capture has (a pipeline-startup gap before the
// branch's content ever arrives, then whatever the aggregator did or
// did not drop from that content's own front). Reading gensweep's own
// PCM output and writing IEEE float by default also exercises the
// analyzer's float-decoding path with the same fixture, since a real
// capture is commonly F32LE rather than the sweep's own S16LE.
//
// Deliberately separate from gensweep (which only ever emits the
// unmodified reference signal) and from the arms/harness: this tool
// exists solely to build a synthetic control, never a stand-in for a
// real capture.
//
// Usage:
//
//	go run ./bench/audio-node/resync-truncation-repro/gencapture \
//	    -ref sweep_100_2000_2s_48k_s16le_mono.wav \
//	    -out capture_shaped_control.wav \
//	    -silence-ms 300 -cut-ms 210 -format f32
package main

import (
	"encoding/binary"
	"flag"
	"log"
	"math"
	"os"
)

// readPCMWav reads a PCM 16/32-bit mono WAV (gensweep's own output
// shape) into a float64 slice normalized to [-1,1]. Deliberately
// narrower than analyze's own reader: this tool only ever consumes a
// file this repo's own gensweep produced.
func readPCMWav(path string) (samples []float64, rate int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var bits, channels int
	var dataOff, dataLen int
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(data[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(data[body+14 : body+16]))
		case "data":
			dataOff, dataLen = body, size
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	if channels != 1 {
		log.Fatalf("%s: gencapture only accepts a mono reference (got %d channels)", path, channels)
	}
	bytesPerSample := bits / 8
	nFrames := dataLen / bytesPerSample
	samples = make([]float64, nFrames)
	raw := data[dataOff : dataOff+nFrames*bytesPerSample]
	for i := 0; i < nFrames; i++ {
		off := i * bytesPerSample
		switch bits {
		case 16:
			samples[i] = float64(int16(binary.LittleEndian.Uint16(raw[off:off+2]))) / 32768.0
		case 32:
			samples[i] = float64(int32(binary.LittleEndian.Uint32(raw[off:off+4]))) / 2147483648.0
		default:
			log.Fatalf("%s: unsupported reference bit depth %d", path, bits)
		}
	}
	return samples, rate, nil
}

func main() {
	refPath := flag.String("ref", "", "reference sweep WAV to build the control from (gensweep's own PCM output)")
	out := flag.String("out", "capture_shaped_control.wav", "output path")
	silenceMs := flag.Float64("silence-ms", 300, "leading silence duration, ms (a capture-shaped analog of pipeline-startup gap)")
	cutMs := flag.Float64("cut-ms", 210, "how much of the reference sweep's own front to omit, ms (the truncation this control asserts)")
	format := flag.String("format", "f32", "output sample format: pcm16, pcm32, f32, f64")
	flag.Parse()

	if *refPath == "" {
		log.Fatal("usage: gencapture -ref REF.wav -out OUT.wav [-silence-ms 300 -cut-ms 210 -format f32]")
	}

	refSamples, rate, err := readPCMWav(*refPath)
	if err != nil {
		log.Fatalf("reading %s: %v", *refPath, err)
	}

	silenceFrames := int(math.Round(*silenceMs * float64(rate) / 1000.0))
	cutFrames := int(math.Round(*cutMs * float64(rate) / 1000.0))
	if cutFrames >= len(refSamples) {
		log.Fatalf("-cut-ms %v (%d frames) is not shorter than the reference itself (%d frames)", *cutMs, cutFrames, len(refSamples))
	}

	outSamples := make([]float64, silenceFrames+len(refSamples)-cutFrames)
	copy(outSamples[silenceFrames:], refSamples[cutFrames:])

	var bits int
	var isFloat bool
	switch *format {
	case "pcm16":
		bits = 16
	case "pcm32":
		bits = 32
	case "f32":
		bits, isFloat = 32, true
	case "f64":
		bits, isFloat = 64, true
	default:
		log.Fatalf("unknown -format %q (want pcm16, pcm32, f32, or f64)", *format)
	}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Fatalf("close %s: %v", *out, err)
		}
	}()

	bytesPerSample := bits / 8
	dataBytes := len(outSamples) * bytesPerSample
	writeWavHeader(f, rate, 1, bits, isFloat, dataBytes)

	buf := make([]byte, bytesPerSample)
	for _, v := range outSamples {
		switch {
		case !isFloat && bits == 16:
			raw := clampInt(v, 32767)
			binary.LittleEndian.PutUint16(buf, uint16(int16(raw)))
		case !isFloat && bits == 32:
			raw := clampInt(v, 2147483647)
			binary.LittleEndian.PutUint32(buf, uint32(int32(raw)))
		case isFloat && bits == 32:
			binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
		case isFloat && bits == 64:
			binary.LittleEndian.PutUint64(buf, math.Float64bits(v))
		}
		if _, err := f.Write(buf); err != nil {
			log.Fatalf("write sample: %v", err)
		}
	}

	log.Printf("wrote %s: %.1fms silence + %d frames of sweep starting %.1fms in, %d-bit %s, %d Hz",
		*out, *silenceMs, len(refSamples)-cutFrames, *cutMs, bits, map[bool]string{true: "float", false: "PCM"}[isFloat], rate)
}

func clampInt(v float64, max int64) int64 {
	raw := int64(math.Round(v * float64(max)))
	if raw > max {
		raw = max
	}
	if raw < -max-1 {
		raw = -max - 1
	}
	return raw
}

func writeWavHeader(f *os.File, rate, channels, bits int, isFloat bool, dataBytes int) {
	byteRate := rate * channels * bits / 8
	blockAlign := channels * bits / 8
	riffSize := 36 + dataBytes
	formatTag := 1
	if isFloat {
		formatTag = 3
	}

	write := func(b []byte) {
		if _, err := f.Write(b); err != nil {
			log.Fatalf("write header: %v", err)
		}
	}
	u32 := func(v int) []byte {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(v))
		return b
	}
	u16 := func(v int) []byte {
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(v))
		return b
	}

	write([]byte("RIFF"))
	write(u32(riffSize))
	write([]byte("WAVE"))
	write([]byte("fmt "))
	write(u32(16))
	write(u16(formatTag))
	write(u16(channels))
	write(u32(rate))
	write(u32(byteRate))
	write(u16(blockAlign))
	write(u16(bits))
	write([]byte("data"))
	write(u32(dataBytes))
}
