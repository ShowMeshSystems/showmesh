//go:build !linux

package clock

import (
	"fmt"
	"time"
)

// PHCIndexForInterface always reports no PHC on a non-Linux build:
// ETHTOOL_GET_TS_INFO is a Linux ioctl, so no platform-specific PHC
// index can be truthfully returned here.
func PHCIndexForInterface(iface string) (index int, ok bool, err error) {
	return 0, false, fmt.Errorf("clock: PHC lookup for %s is unavailable on this platform (Linux only)", iface)
}

// ReadPHC always fails on a non-Linux build: there is no PHC device to
// read, so no plausible clock reading is returned.
func ReadPHC(index int) (time.Time, error) {
	return time.Time{}, fmt.Errorf("clock: reading /dev/ptp%d is unavailable on this platform (Linux only)", index)
}

// PHCReader is the non-Linux stand-in for [phc.go]'s real PHCReader, so
// this package always exposes the same type regardless of build
// platform. Its zero value is never usable directly: [OpenPHC] never
// succeeds on this platform.
type PHCReader struct{}

// OpenPHC always fails on a non-Linux build: there is no /dev/ptpN
// device to open here.
func OpenPHC(index int) (*PHCReader, error) {
	return nil, fmt.Errorf("clock: opening /dev/ptp%d is unavailable on this platform (Linux only)", index)
}

// Now always fails: this platform never opened a PHC device.
func (r *PHCReader) Now() (time.Time, error) {
	return time.Time{}, fmt.Errorf("clock: PHC reads are unavailable on this platform (Linux only)")
}

// Close is a no-op: this platform never held a PHC device open.
func (r *PHCReader) Close() error { return nil }
