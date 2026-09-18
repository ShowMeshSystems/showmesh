package audiorendition

import "math"

// sincHalfTaps is the number of filter taps on each side of the current
// output sample. 16 gives a stopband good enough to suppress audible
// aliasing on a downsample without the cost of a much larger kernel; this
// is not mastering-grade resampling, only good enough that a show bed does
// not audibly degrade when its source rate differs from 48 kHz.
const sincHalfTaps = 16

// resample converts samples (one equal-length slice per channel) from
// srcRate to dstRate with a windowed-sinc filter, never nearest-sample.
// Downsampling scales the sinc's own cutoff to the destination Nyquist
// frequency, which low-pass filters the source first and prevents
// aliasing; upsampling leaves the cutoff at the source's own Nyquist,
// since there is no content above it to alias. Returns samples unchanged
// (not copied) when srcRate == dstRate.
func resample(samples [][]float64, srcRate, dstRate int) [][]float64 {
	if srcRate == dstRate || len(samples) == 0 {
		return samples
	}

	srcLen := len(samples[0])
	ratio := float64(dstRate) / float64(srcRate)
	dstLen := int(math.Round(float64(srcLen) * ratio))
	cutoff := math.Min(1.0, ratio)

	out := make([][]float64, len(samples))
	for c := range out {
		out[c] = make([]float64, dstLen)
	}

	for i := 0; i < dstLen; i++ {
		srcPos := float64(i) / ratio
		center := int(math.Floor(srcPos))
		n0 := center - sincHalfTaps + 1
		n1 := center + sincHalfTaps

		// The window (and therefore its normalization) does not depend on
		// the channel, so it is computed once per output sample and reused
		// across every channel.
		type tap struct {
			n int
			w float64
		}
		taps := make([]tap, 0, n1-n0+1)
		var wsum float64
		for n := n0; n <= n1; n++ {
			if n < 0 || n >= srcLen {
				continue
			}
			w := sincWindow(srcPos-float64(n), cutoff)
			taps = append(taps, tap{n: n, w: w})
			wsum += w
		}
		if wsum == 0 {
			continue
		}
		for c, ch := range samples {
			var sum float64
			for _, t := range taps {
				sum += ch[t.n] * t.w
			}
			out[c][i] = sum / wsum
		}
	}
	return out
}

// sincWindow evaluates a Blackman-windowed, cutoff-scaled sinc kernel at
// offset x (in source-sample units) from the current output position.
func sincWindow(x, cutoff float64) float64 {
	var sinc float64
	if x == 0 {
		sinc = cutoff
	} else {
		px := math.Pi * x
		sinc = math.Sin(px*cutoff) / px
	}

	t := x / float64(sincHalfTaps)
	if t < -1 || t > 1 {
		return 0
	}
	blackman := 0.42 + 0.5*math.Cos(math.Pi*t) + 0.08*math.Cos(2*math.Pi*t)
	return sinc * blackman
}
