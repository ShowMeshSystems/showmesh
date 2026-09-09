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
