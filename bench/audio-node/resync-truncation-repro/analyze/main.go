// analyze measures a captured WAV against a known linear sweep in two
// stages that must never be conflated:
//
//  1. Onset: where inside the CAPTURE the signal actually starts. A real
//     capture routinely has leading silence that has nothing to do with
//     the aggregator drop this repro exists to detect — the capture
//     branch starts recording when the pipeline is built, and the
//     branch's own content only reaches it once Start runs, some
//     nonzero wall-clock time later. Treating that startup gap as
//     truncation, or refusing the file outright because its first 50ms
//     is silent, are both wrong: the first was never measured (the
//     preceding version of this tool did this and got refusal only
//     because a real onset gap happens to be picked up in the same
//     bytes-are-zero check used for a truly empty capture — a fragile
//     accident, not a designed answer), the second is a false negative.
//  2. Truncation: once the onset is found, how much of the reference
//     sweep's own front is missing from the content that begins there.
//     This is the number the aggregator-drop prediction is actually
//     about.
//
// Truncation is measured by two independent methods that must agree:
//
//   - Matched filter (primary, exact): cross-correlate a window starting
//     at the onset against every candidate offset into the reference,
//     and report the offset that maximizes normalized correlation,
//     refined to sub-sample precision by parabolic interpolation.
//   - Instantaneous frequency (cross-check): estimate the frequency
//     actually present just after onset via zero-crossing period
//     counting, then invert the sweep's own linear law to recover
//     elapsed time. Biased slightly by window averaging on a chirp (see
//     instantaneousFreqHz's own comment) — a cross-check, not primary.
//
// This tool refuses to report a result for a capture it could not
// actually analyze — no onset found anywhere (silent throughout), too
// short to correlate, an unrecognized sample format, or an unreadable
// file all produce an explicit CAPTURE_INVALID failure on stderr and a
// non-zero exit code, never a 0ms reading standing in for "did not run."
//
// Sample formats: PCM 16/32-bit integer and IEEE float 32/64-bit are
// decoded (a real alsasink/wavenc capture is commonly F32LE, not the
// S16LE this tool's own generated reference and early controls used —
// both must work). An unrecognized WAVE format tag or bit depth is
// refused by name, never silently misread as a different format.
//
// The data chunk's declared size is not trusted blindly: wavenc writes
// a fixed placeholder (0x7FFFF000) instead of patching in the real size
// when its filesink cannot seek back to finalize the header at EOS, and
// a naive reader that computes duration from that field massively
// overstates it. Actual usable audio is derived from the file's real
// size instead, and any mismatch is reported, not silently clamped away
// — a silent clamp would hide a genuinely truncated/unfinalized capture
// exactly when that fact matters most.
//
// Usage:
//
//	go run ./bench/audio-node/resync-truncation-repro/analyze \
//	    -ref sweep_reference.wav -query captured.wav -f0 100 -f1 2000 -dur 2.0
//
// Exit codes: 0 = measured ONSET_MS and RESULT_TRUNCATION_MS (see
// stdout). 1 = capture invalid (could not measure anything). 2 =
// usage/argument error.
//
// See ../README.md for the four required controls and their expected
// output, recorded before this tool is pointed at a real capture.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
)

const (
	waveFormatPCM       = 1
	waveFormatIEEEFloat = 3
)

// wavencUnpatchedPlaceholder is the data-chunk size wavenc writes when
// its filesink cannot seek back to patch in the real size at EOS (a
// pipe-like/non-seekable sink gets this fixed sentinel instead of a
// zero or a correct value). Recognizing the exact value lets the header
// mismatch note name the real, ordinary cause instead of describing it
// as unexplained corruption.
const wavencUnpatchedPlaceholder = 0x7FFFF000

type wavFile struct {
	rate     int
	channels int
	bits     int
	format   int       // WAVE format tag: waveFormatPCM or waveFormatIEEEFloat
	samples  []float64 // mono-downmixed (channel 0 if multi-channel), normalized to [-1,1]
	nFrames  int

	// headerDataLenClaimed and headerDataLenActual differ exactly when
	// the data chunk's declared size does not match what the file
	// actually contains (see wavencUnpatchedPlaceholder above).
	headerDataLenClaimed int
	headerDataLenActual  int
}

func (w *wavFile) headerSizeImplausible() bool {
	return w.headerDataLenClaimed > w.headerDataLenActual
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
		format, channels, bits, rate int
		dataOff, dataLen             int
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
			format = int(binary.LittleEndian.Uint16(data[body : body+2]))
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

	headerDataLenClaimed := dataLen
	if dataOff+dataLen > len(data) {
		// wavenc's own unpatchable-header placeholder, or any other
		// declared size the file does not actually contain: derive the
		// real length from the file instead of trusting the header.
		// headerSizeImplausible() reports this so the caller can say so,
		// rather than clamping in silence.
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

	w := &wavFile{
		rate: rate, channels: channels, bits: bits, format: format, nFrames: nFrames,
		headerDataLenClaimed: headerDataLenClaimed, headerDataLenActual: dataLen,
	}
	w.samples = make([]float64, nFrames)

	raw := data[dataOff : dataOff+nFrames*frameSize]
	for i := 0; i < nFrames; i++ {
		off := i * frameSize // channel 0 only
		v, err := decodeSample(raw[off:off+bytesPerSample], format, bits)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		w.samples[i] = v
	}
	return w, nil
}

// decodeSample converts one channel-0 sample's raw bytes to a float64 in
// [-1,1], for exactly the formats this tool understands. An unrecognized
// (format, bits) pair is refused by name here rather than silently
// misread through the wrong branch (e.g. IEEE float bytes read as PCM
// integer, which produces plausible-looking but meaningless values,
// never an obvious crash).
func decodeSample(b []byte, format, bits int) (float64, error) {
	switch {
	case format == waveFormatPCM && bits == 16:
		return float64(int16(binary.LittleEndian.Uint16(b))) / 32768.0, nil
	case format == waveFormatPCM && bits == 32:
		return float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648.0, nil
	case format == waveFormatIEEEFloat && bits == 32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))), nil
	case format == waveFormatIEEEFloat && bits == 64:
		return math.Float64frombits(binary.LittleEndian.Uint64(b)), nil
	default:
		return 0, fmt.Errorf("unsupported sample format (WAVE format tag=%d, %d-bit) -- only PCM 16/32-bit and IEEE float 32/64-bit are decoded", format, bits)
	}
}

// findOnset returns the first sample index where sustained signal
// begins: |s[i]| >= ampThresh, confirmed by the following sustainMs
// window averaging at least ampThresh/2 in absolute value. The sustain
// check exists so a single noise/click sample near the true silence
// can't be mistaken for onset. Returns -1 if no such point exists
// anywhere in s -- a capture silent throughout, never a 0ms answer.
func findOnset(s []float64, rate int, ampThresh, sustainMs float64) int {
	sustainLen := int(float64(rate) * sustainMs / 1000.0)
	if sustainLen < 1 {
		sustainLen = 1
	}
	for i := 0; i < len(s); i++ {
		if math.Abs(s[i]) < ampThresh {
			continue
		}
		end := i + sustainLen
		if end > len(s) {
			end = len(s)
		}
		if end-i < (sustainLen+1)/2 {
			// too close to EOF to confirm a sustained onset here
			continue
		}
		var sum float64
		for j := i; j < end; j++ {
			sum += math.Abs(s[j])
		}
		if sum/float64(end-i) >= ampThresh/2 {
			return i
		}
	}
	return -1
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
	onsetThresh := flag.Float64("onset-thresh", 0.005, "sample amplitude at/above which sustained signal is considered to have begun")
	onsetSustainMs := flag.Float64("onset-sustain-ms", 5.0, "how long the signal must stay above onset-thresh/2 (average) to confirm a real onset, not a click")
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

	if query.headerSizeImplausible() {
		fmt.Printf("NOTE: %s's WAV header declares a data chunk of %d bytes, but the file only contains %d bytes; using the actual byte count.",
			*queryPath, query.headerDataLenClaimed, query.headerDataLenActual)
		if query.headerDataLenClaimed == wavencUnpatchedPlaceholder {
			fmt.Printf(" This is wavenc's own unpatchable-header placeholder (0x7FFFF000), written when its filesink could not seek back to finalize the header at EOS -- not evidence of a truncated capture by itself.\n")
		} else {
			fmt.Printf(" This does not by itself mean the capture is truncated or unfinalized, but is worth knowing.\n")
		}
	}

	onset := findOnset(query.samples, query.rate, *onsetThresh, *onsetSustainMs)
	if onset < 0 {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: no sustained signal found above amplitude %.4g anywhere in %d frames (%.1fms) — capture is silent throughout, this is NOT a 0ms result\n",
			*onsetThresh, query.nFrames, float64(query.nFrames)/float64(query.rate)*1000)
		os.Exit(1)
	}
	onsetMs := float64(onset) / float64(query.rate) * 1000.0
	fromOnset := query.samples[onset:]

	analysisWinMs := 50.0
	winLen := int(float64(query.rate) * analysisWinMs / 1000.0)
	if len(fromOnset) < winLen {
		winLen = len(fromOnset)
	}
	minAnalysisSamples := int(float64(query.rate) * 8.0 / 1000.0) // need at least ~8ms post-onset to say anything
	if winLen < minAnalysisSamples {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: only %d samples (%.1fms) survive after onset at %.3fms — not enough to correlate against the reference\n",
			len(fromOnset), float64(len(fromOnset))/float64(query.rate)*1000, onsetMs)
		os.Exit(1)
	}

	maxOffsetMs := *dur*1000.0 - analysisWinMs
	maxOffset := int(float64(ref.rate) * maxOffsetMs / 1000.0)

	offSamples, score, refine := matchedFilterOffset(ref.samples, fromOnset, winLen, maxOffset)
	if offSamples < 0 || score < 0.9 {
		fmt.Fprintf(os.Stderr, "CAPTURE_INVALID: no confident match against reference sweep found starting from onset at %.3fms (best score=%.4f at offset=%d) — content there is not recognizable as (a prefix-truncated copy of) the reference sweep\n",
			onsetMs, score, offSamples)
		os.Exit(1)
	}
	truncSamplesMF := float64(offSamples) + refine
	truncMsMF := truncSamplesMF / float64(ref.rate) * 1000.0

	minFreqWinLen := int(float64(query.rate) * 2.0 / 1000.0)   // start at 2ms
	maxFreqWinLen := int(float64(query.rate) * 128.0 / 1000.0) // give up past 128ms
	freqHz, ferr := instantaneousFreqHz(fromOnset, minFreqWinLen, maxFreqWinLen, query.rate)
	var truncMsFreq float64
	var freqNote string
	if ferr != nil {
		freqNote = fmt.Sprintf("(frequency cross-check unavailable: %v)", ferr)
	} else {
		k := (*f1 - *f0) / *dur
		tSeconds := (freqHz - *f0) / k
		truncMsFreq = tSeconds * 1000.0
		freqNote = fmt.Sprintf("first-sample-after-onset instantaneous frequency ~%.1f Hz -> t=%.2fms into the sweep", freqHz, truncMsFreq)
	}

	fmt.Printf("ONSET_MS: %.3f (signal within the capture begins this far in; this is capture/pipeline-startup timing, NOT sweep truncation)\n", onsetMs)
	fmt.Printf("MATCHED_FILTER: truncation = %.3f ms (%.3f samples at %d Hz, correlation score %.4f)\n",
		truncMsMF, truncSamplesMF, ref.rate, score)
	fmt.Printf("FREQ_CROSSCHECK: %s\n", freqNote)
	if ferr == nil {
		diff := math.Abs(truncMsMF - truncMsFreq)
		fmt.Printf("AGREEMENT: methods differ by %.3f ms\n", diff)
	}
	fmt.Printf("RESULT_ONSET_MS: %.3f\n", onsetMs)
	fmt.Printf("RESULT_TRUNCATION_MS: %.3f\n", truncMsMF)
}
