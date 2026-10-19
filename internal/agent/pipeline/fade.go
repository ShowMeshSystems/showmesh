package pipeline

import "time"

// LevelSource is an optional extension of [HoldBlackSource]: the surface's
// output level from 0 to 1, applied as a uniform multiply on its channel
// bytes (ADR-063). A frame writer whose source does not implement it draws
// at full level.
type LevelSource interface {
	Level(now time.Time) float64
}

// Fade is a linear ramp of a surface's output level from From at Start to
// To at End. It holds From before Start and To after End, until Restore
// when Restore is set, and full level from then on.
type Fade struct {
	From, To   float64
	Start, End time.Time
	Restore    time.Time
}

// Level is the fade's level at now.
func (f Fade) Level(now time.Time) float64 {
	switch {
	case !f.Restore.IsZero() && !now.Before(f.Restore):
		return 1
	case !now.After(f.Start):
		return f.From
	case !now.Before(f.End):
		return f.To
	}
	frac := float64(now.Sub(f.Start)) / float64(f.End.Sub(f.Start))
	return f.From + (f.To-f.From)*frac
}

// ScaleBytes writes src multiplied by level into dst, rounding to nearest.
// dst must be at least len(src).
func ScaleBytes(dst, src []byte, level float64) {
	l := uint32(level*255 + 0.5)
	if l > 255 {
		l = 255
	}
	for i, b := range src {
		dst[i] = byte((uint32(b)*l + 127) / 255)
	}
}
