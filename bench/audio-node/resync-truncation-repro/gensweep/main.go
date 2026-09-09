// gensweep generates a deterministic linear-frequency sweep (chirp) WAV file.
//
// The signal is a linear sweep from F0 to F1 Hz over DURATION seconds,
// starting at sample 0 with zero initial phase. Because the sweep is a pure
// analytic function of sample index, the file is byte-identical on every
// regeneration with the same parameters, sample rate, bit depth and channel
// count. No random seed, no dependency on wall-clock time, no GStreamer
// element involved in generation (audiotestsrc is not used here, precisely
// because we later need to trim this file deterministically byte-for-byte
// to build the "known-bad" control, and because using an unrelated
// generator for the ground-truth chirp keeps the analysis tool honest: it
// analyzes a file, it does not share a math implementation with a producer
// that could compensate for the same bug in the same way).
//
// Frequency law:
//
//	f(t) = F0 + (F1-F0)*t/T,           0 <= t <= T
//
// Phase (integral of 2*pi*f(t) dt, zero initial phase):
//
//	phi(t) = 2*pi*( F0*t + (F1-F0)*t^2/(2*T) )
//
// Sample n at sample rate SR corresponds to t = n/SR.
//
// Usage:
//
//	go run ./bench/audio-node/resync-truncation-repro/gensweep \
//	    -out sweep_100_2000_2s_48k_s16le_mono.wav \
//	    -f0 100 -f1 2000 -dur 2.0 -rate 48000 -bits 16 -channels 1 -amp 0.8
//
// See ../README.md for why mono/48000Hz was chosen for this repro.
package main

import (
	"encoding/binary"
	"flag"
	"log"
	"math"
	"os"
)

func main() {
	out := flag.String("out", "sweep.wav", "output wav path")
	f0 := flag.Float64("f0", 100, "start frequency, Hz")
	f1 := flag.Float64("f1", 2000, "end frequency, Hz")
	dur := flag.Float64("dur", 2.0, "sweep duration, seconds")
	rate := flag.Int("rate", 48000, "sample rate, Hz")
	bits := flag.Int("bits", 16, "bits per sample (16 or 32)")
	channels := flag.Int("channels", 2, "channel count (sweep duplicated identically on every channel)")
	amp := flag.Float64("amp", 0.8, "peak amplitude, fraction of full scale (0,1]")
	flag.Parse()

	if *bits != 16 && *bits != 32 {
		log.Fatalf("bits must be 16 or 32, got %d", *bits)
	}
	if *amp <= 0 || *amp > 1 {
		log.Fatalf("amp must be in (0,1], got %v", *amp)
	}

	nSamples := int(math.Round(*dur * float64(*rate)))
	bytesPerSample := *bits / 8
	dataBytes := nSamples * *channels * bytesPerSample

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Fatalf("close %s: %v", *out, err)
		}
	}()

	writeWavHeader(f, *rate, *channels, *bits, dataBytes)

	buf := make([]byte, bytesPerSample)
	T := *dur
	k := (*f1 - *f0) / T // chirp rate, Hz/s

	for n := 0; n < nSamples; n++ {
		t := float64(n) / float64(*rate)
		phase := 2 * math.Pi * (*f0*t + k*t*t/2)
		v := math.Sin(phase) * *amp

		var raw int64
		if *bits == 16 {
			raw = int64(math.Round(v * 32767))
			if raw > 32767 {
				raw = 32767
			}
			if raw < -32768 {
				raw = -32768
			}
		} else {
			raw = int64(math.Round(v * 2147483647))
			if raw > 2147483647 {
				raw = 2147483647
			}
			if raw < -2147483648 {
				raw = -2147483648
			}
		}

		for c := 0; c < *channels; c++ {
			if *bits == 16 {
				binary.LittleEndian.PutUint16(buf, uint16(int16(raw)))
			} else {
				binary.LittleEndian.PutUint32(buf, uint32(int32(raw)))
			}
			if _, err := f.Write(buf); err != nil {
				log.Fatalf("write sample: %v", err)
			}
		}
	}

	log.Printf("wrote %s: %d samples/channel, %d channels, %d-bit, %d Hz, %.3fs, f0=%v f1=%v",
		*out, nSamples, *channels, *bits, *rate, *dur, *f0, *f1)
}

func writeWavHeader(f *os.File, rate, channels, bits, dataBytes int) {
	byteRate := rate * channels * bits / 8
	blockAlign := channels * bits / 8
	riffSize := 36 + dataBytes

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
	write(u32(16)) // PCM fmt chunk size
	write(u16(1))  // PCM format
	write(u16(channels))
	write(u32(rate))
	write(u32(byteRate))
	write(u16(blockAlign))
	write(u16(bits))
	write([]byte("data"))
	write(u32(dataBytes))
}
