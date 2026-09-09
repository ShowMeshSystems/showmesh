// analyze measures how much of the leading edge of a known linear sweep is
// missing from a captured WAV file, by two independent methods that must
// agree:
//
//  1. Matched filter (primary, exact): cross-correlate the first window of
//     the captured (query) signal against the known reference sweep across
//     every candidate start offset, and report the offset that maximizes
//     normalized correlation. Because the reference is a deterministic
//     analytic chirp and the query is (at most) a delayed/truncated copy of
//     it, the correlation peak is sharp and the offset is exact to better
//     than a sample via parabolic interpolation.
//
//  2. Instantaneous frequency (independent cross-check, matches the brief's
//     framing directly): estimate the instantaneous frequency actually
//     present in the first few milliseconds of the query via autocorrelation
//     period estimation, then invert the sweep's own linear frequency law
//     f(t) = F0 + k*t to recover t, which is the truncation depth on the
//     assumption that whatever survived starts mid-sweep rather than
//     starting the sweep over.
//
// Both numbers are printed; they are expected to agree to within ~1ms. This
// tool refuses to report "no truncation" for a capture it could not actually
// analyze: silence, a too-short/empty file, or an unreadable file all produce
// an explicit CAPTURE_INVALID failure on stderr and a non-zero exit code,
// never a 0ms reading.
//
// Usage:
//
//	go run ./bench/audio-node/resync-truncation-repro/analyze \
//	    -ref sweep_reference.wav -query captured.wav -f0 100 -f1 2000 -dur 2.0
//
// Exit codes: 0 = measured a truncation value (see stdout). 1 = capture
// invalid (could not measure anything — this is not "zero truncation").
// 2 = usage/argument error.
//
// See ../README.md for the three required controls and their expected
// output, recorded before this tool is pointed at a real capture.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
)

type wavFile struct {
	rate     int
	channels int
	bits     int
	samples  []float64 // mono-downmixed (channel 0 if multi-channel), normalized to [-1,1]
	nFrames  int
}

func readWav(path string) (*wavFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) < 44 {
		return nil, fmt.Errorf("%s: too short to be a WAV file (%d bytes)", path, len(data))
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s: not a RIFF/WAVE file", path)
	}

	var (
		channels, bits, rate int
		dataOff, dataLen     int
	)

	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if body+16 > len(data) {
				return nil, fmt.Errorf("%s: truncated fmt chunk", path)
			}
			channels = int(binary.LittleEndian.Uint16(data[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(data[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(data[body+14 : body+16]))
		case "data":
			dataOff = body
			dataLen = size
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // chunks are word-aligned
		}
	}

	if rate == 0 || channels == 0 || bits == 0 {
		return nil, fmt.Errorf("%s: missing or invalid fmt chunk", path)
	}
	if dataOff == 0 {
		return nil, fmt.Errorf("%s: no data chunk found", path)
	}
	if dataOff+dataLen > len(data) {
		// Some writers (e.g. a killed capture) leave the data chunk size
		// field wrong or zero; fall back to what's actually on disk.
		dataLen = len(data) - dataOff
	}
	if dataLen <= 0 {
		return nil, fmt.Errorf("%s: data chunk is empty (0 bytes of audio)", path)
	}

	bytesPerSample := bits / 8
	frameSize := bytesPerSample * channels
	if frameSize == 0 {
		return nil, fmt.Errorf("%s: invalid frame size (bits=%d channels=%d)", path, bits, channels)
	}
	nFrames := dataLen / frameSize

	w := &wavFile{rate: rate, channels: channels, bits: bits, nFrames: nFrames}
	w.samples = make([]float64, nFrames)

	raw := data[dataOff : dataOff+nFrames*frameSize]
	for i := 0; i < nFrames; i++ {
		off := i * frameSize // channel 0 only
		switch bits {
		case 16:
			v := int16(binary.LittleEndian.Uint16(raw[off : off+2]))
			w.samples[i] = float64(v) / 32768.0
		case 32:
			v := int32(binary.LittleEndian.Uint32(raw[off : off+4]))
			w.samples[i] = float64(v) / 2147483648.0
		default:
			return nil, fmt.Errorf("%s: unsupported bit depth %d", path, bits)
		}
	}
	return w, nil
}

// rmsOf returns the RMS energy of the first n samples (or all, if shorter).
func rmsOf(s []float64, n int) float64 {
	if n > len(s) {
		n = len(s)
	}
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += s[i] * s[i]
	}
	return math.Sqrt(sum / float64(n))
}

// matchedFilterOffset finds, for query[:winLen], the offset into ref that
// maximizes normalized cross-correlation, searching offsets 0..maxOffset.
// Returns (bestOffsetSamples, bestScore, subSampleRefinementSamples).
func matchedFilterOffset(ref, query []float64, winLen, maxOffset int) (int, float64, float64) {
	if winLen > len(query) {
		winLen = len(query)
	}
	q := query[:winLen]
	qEnergy := 0.0
	for _, v := range q {
		qEnergy += v * v
	}

	best := -1
	bestScore := -1.0
	scores := make([]float64, maxOffset+1)

	for off := 0; off <= maxOffset; off++ {
		if off+winLen > len(ref) {
			break
		}
		r := ref[off : off+winLen]
		var dot, rEnergy float64
		for i := 0; i < winLen; i++ {
			dot += r[i] * q[i]
			rEnergy += r[i] * r[i]
		}
		denom := math.Sqrt(rEnergy*qEnergy) + 1e-12
		score := dot / denom
		scores[off] = score
		if score > bestScore {
			bestScore = score
			best = off
		}
	}

	// Parabolic interpolation around the peak for sub-sample refinement.
	refine := 0.0
	if best > 0 && best < maxOffset {
		y0, y1, y2 := scores[best-1], scores[best], scores[best+1]
		denom := y0 - 2*y1 + y2
		if math.Abs(denom) > 1e-9 {
			refine = 0.5 * (y0 - y2) / denom
		}
	}

	return best, bestScore, refine
}

// instantaneousFreqHz estimates the frequency present at the start of s
// via zero-crossing period counting, growing its analysis window from
// minWinLen up to maxWinLen until it captures enough zero crossings to be
// reliable. Growing matters because the window must hold several periods
// of whatever frequency is actually present at the very start of the
// query, and — that being exactly the unknown this function exists to
// recover — a fixed window sized for a mid-sweep frequency is too short
// once the true start is near F0 (a 100Hz start needs ~20+ms; a 2000Hz
// one needs under 2ms). Because the underlying signal is a *linear*
// sweep, growing the window does bias the estimate slightly toward the
// window's mean instantaneous frequency rather than its exact first
// sample — reported truncation from this method is therefore a
// cross-check on the matched-filter result, not the primary measurement.
func instantaneousFreqHz(s []float64, minWinLen, maxWinLen, rate int) (float64, error) {
	var lastErr error
	for winLen := minWinLen; winLen <= maxWinLen; winLen *= 2 {
		freq, err := instantaneousFreqHzWindow(s, winLen, rate)
		if err == nil {
			return freq, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func instantaneousFreqHzWindow(s []float64, winLen int, rate int) (float64, error) {
	if winLen > len(s) {
		winLen = len(s)
	}
	if winLen < 8 {
		return 0, fmt.Errorf("window too short (%d samples) to estimate frequency", winLen)
	}
	w := s[:winLen]

	var crossings []float64
	for i := 1; i < len(w); i++ {
		if (w[i-1] < 0 && w[i] >= 0) || (w[i-1] >= 0 && w[i] < 0) {
			frac := -w[i-1] / (w[i] - w[i-1])
			crossings = append(crossings, float64(i-1)+frac)
		}
	}
	if len(crossings) < 4 {
		return 0, fmt.Errorf("only %d zero crossings in first %d samples (%.2fms); signal may be silent or window too short", len(crossings), winLen, float64(winLen)/float64(rate)*1000)
	}
	// Period = 2x average spacing between successive zero crossings.
	var spacing float64
	for i := 1; i < len(crossings); i++ {
		spacing += crossings[i] - crossings[i-1]
	}
	spacing /= float64(len(crossings) - 1)
	periodSamples := 2 * spacing
	freq := float64(rate) / periodSamples
	return freq, nil
}

func main() {
	refPath := flag.String("ref", "", "path to reference (untruncated) sweep WAV")
	queryPath := flag.String("query", "", "path to captured WAV to analyze")
	f0 := flag.Float64("f0", 100, "sweep start frequency, Hz (must match generator)")
	f1 := flag.Float64("f1", 2000, "sweep end frequency, Hz (must match generator)")
	dur := flag.Float64("dur", 2.0, "sweep duration, seconds (must match generator)")
	silenceThresh := flag.Float64("silence-thresh", 1e-4, "RMS below this over the analysis window is treated as silence/no-signal")
	flag.Parse()

	if *refPath == "" || *queryPath == "" {
		fmt.Fprintln(os.Stderr, "usage: analyze -ref REF.wav -query QUERY.wav [-f0 100 -f1 2000 -dur 2.0]")
		os.Exit(2)
	}

	ref, err := readWav(*refPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: reference file problem: %v\n", err)
		os.Exit(1)
	}
	query, err := readWav(*queryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: query file problem: %v\n", err)
		os.Exit(1)
	}
	if query.rate != ref.rate {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: sample rate mismatch ref=%d query=%d\n", ref.rate, query.rate)
		os.Exit(1)
	}

	analysisWinMs := 50.0
	winLen := int(float64(query.rate) * analysisWinMs / 1000.0)

	energy := rmsOf(query.samples, winLen)
	if energy < *silenceThresh {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: query is silent/near-silent in first %.0fms (RMS=%.6g < threshold %.6g) — this is NOT a 0ms-truncation result, it means the capture produced no usable signal\n",
			analysisWinMs, energy, *silenceThresh)
		os.Exit(1)
	}
	if query.nFrames < winLen {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: query has only %d frames, need at least %d for a %vms analysis window\n",
			query.nFrames, winLen, analysisWinMs)
		os.Exit(1)
	}

	maxOffsetMs := *dur*1000.0 - analysisWinMs
	maxOffset := int(float64(ref.rate) * maxOffsetMs / 1000.0)

	offSamples, score, refine := matchedFilterOffset(ref.samples, query.samples, winLen, maxOffset)
	if offSamples < 0 || score < 0.9 {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: no confident match against reference sweep found (best score=%.4f at offset=%d) — query is not recognizable as (a prefix-truncated copy of) the reference sweep\n",
			score, offSamples)
		os.Exit(1)
	}
	truncSamplesMF := float64(offSamples) + refine
	truncMsMF := truncSamplesMF / float64(ref.rate) * 1000.0

	minFreqWinLen := int(float64(query.rate) * 2.0 / 1000.0)   // start at 2ms
	maxFreqWinLen := int(float64(query.rate) * 128.0 / 1000.0) // give up past 128ms
	freqHz, ferr := instantaneousFreqHz(query.samples, minFreqWinLen, maxFreqWinLen, query.rate)
	var truncMsFreq float64
	var freqNote string
	if ferr != nil {
		freqNote = fmt.Sprintf("(frequency cross-check unavailable: %v)", ferr)
	} else {
		k := (*f1 - *f0) / *dur
		tSeconds := (freqHz - *f0) / k
		truncMsFreq = tSeconds * 1000.0
		freqNote = fmt.Sprintf("first-sample instantaneous frequency ~%.1f Hz -> t=%.2fms into the sweep", freqHz, truncMsFreq)
	}

	fmt.Printf("MATCHED_FILTER: truncation = %.3f ms (%.3f samples at %d Hz, correlation score %.4f)\n",
		truncMsMF, truncSamplesMF, ref.rate, score)
	fmt.Printf("FREQ_CROSSCHECK: %s\n", freqNote)
	if ferr == nil {
		diff := math.Abs(truncMsMF - truncMsFreq)
		fmt.Printf("AGREEMENT: methods differ by %.3f ms\n", diff)
	}
	fmt.Printf("RESULT_MS: %.3f\n", truncMsMF)
}
