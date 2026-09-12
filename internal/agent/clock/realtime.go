package clock

import "time"

// RealtimeReader reads this host's own CLOCK_REALTIME, selected as a
// node's pipeline clock when its interface has no associated PHC
// hardware clock at all. With software timestamping, ptp4l disciplines
// CLOCK_REALTIME itself, so it IS that node's media clock (see
// deploy/node/PTP-AUDIO.md on the provisioning branch), proven by hand
// on a Raspberry Pi 3B+ node whose output pipeline never presented a
// sample under GStreamer's own default clock, and played correctly the
// moment its pipeline clock was switched to this one. Unlike [PHCReader],
// nothing is opened and reading it cannot fail.
type RealtimeReader struct{}

// NewRealtimeReader returns a [RealtimeReader]. There is nothing to open.
func NewRealtimeReader() *RealtimeReader {
	return &RealtimeReader{}
}

// Now reads this host's system realtime clock.
func (r *RealtimeReader) Now() (time.Time, error) {
	return time.Now(), nil
}

// Close is a no-op: [RealtimeReader] holds nothing open.
func (r *RealtimeReader) Close() error {
	return nil
}
