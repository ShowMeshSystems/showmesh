package clock

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// fdToClockID converts an open file descriptor into the POSIX clock id
// naming that fd's own PHC — the FPP_FD_TO_CLOCKID pattern RES-019
// section 1 names explicitly: ((~(clockid_t)(fd) << 3) | 3). No cgo
// needed: this is pure bit arithmetic over a value [unix.ClockGettime]
// already accepts as an int32 clock id.
func fdToClockID(fd int) int32 {
	return int32((^int64(fd) << 3) | 3)
}

// PHCIndexForInterface reports the PTP hardware clock index (/dev/ptpN's
// N) associated with iface, via ETHTOOL_GET_TS_INFO — RES-019 section 1:
// "the PHC index obtained from ETHTOOL_GET_TS_INFO on the configured
// interface". ok is false when the interface has no associated PHC (a
// virtual interface, or hardware timestamping genuinely unsupported),
// distinct from an outright ioctl error (err != nil).
func PHCIndexForInterface(iface string) (index int, ok bool, err error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return 0, false, fmt.Errorf("clock: open a socket for the ethtool ioctl: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	info, err := unix.IoctlGetEthtoolTsInfo(fd, iface)
	if err != nil {
		return 0, false, fmt.Errorf("clock: ETHTOOL_GET_TS_INFO on %s: %w", iface, err)
	}
	if info.Phc_index < 0 {
		return 0, false, nil
	}
	return int(info.Phc_index), true, nil
}

// PHCReader reads the media clock off one open /dev/ptpN device, per
// RES-019 section 1. The zero value is not usable; construct with
// [OpenPHC]. Read access only is required; distributions ship /dev/ptpN
// as root:root 0600, so an unprivileged agent needs a udev rule or group
// membership — [OpenPHC] reports that failure honestly rather than
// silently falling back to anything else (RES-019: "the provider must
// report that failure honestly rather than silently falling back").
type PHCReader struct {
	f       *os.File
	clockID int32
	path    string
}

// OpenPHC opens /dev/ptp<index> and returns a [PHCReader] over it.
func OpenPHC(index int) (*PHCReader, error) {
	path := fmt.Sprintf("/dev/ptp%d", index)
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("clock: open %s: %w", path, err)
	}
	return &PHCReader{f: f, clockID: fdToClockID(int(f.Fd())), path: path}, nil
}

// Now reads the current time off this PHC via clock_gettime on the
// fd-derived POSIX clock id — RES-019 section 1's exact mechanism. The
// returned MediaTime's epoch is whatever this PHC itself counts from
// (Timescale is the caller's own concern, not derivable from the read
// itself — a Provider wraps this with its own PTP status evidence to
// decide Timescale).
func (r *PHCReader) Now() (time.Time, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(r.clockID, &ts); err != nil {
		return time.Time{}, fmt.Errorf("clock: clock_gettime on %s: %w", r.path, err)
	}
	return time.Unix(ts.Sec, ts.Nsec), nil
}

// Close closes the underlying device file.
func (r *PHCReader) Close() error {
	return r.f.Close()
}

// ReadPHC opens index, reads it once, and closes it — a convenience for a
// provider whose PHC index can change across polls (a config reload) and
// therefore does not keep a [PHCReader] open across calls. A supervising
// provider that polls a FIXED index frequently should hold a [PHCReader]
// open directly instead, to avoid an open/close syscall pair every tick.
func ReadPHC(index int) (time.Time, error) {
	r, err := OpenPHC(index)
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = r.Close() }()
	return r.Now()
}
