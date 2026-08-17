package fseq

import "fmt"

// ErrNotFSEQ means the file's magic bytes are not a recognized FSEQ header.
var ErrNotFSEQ = fmt.Errorf("fseq: not an FSEQ file (bad magic)")

// ErrESEQUnsupported means the file is an ESEQ file. ESEQ shares FSEQ's
// entry point (magic byte 'E') but is a different 20-byte-header format
// with a hardcoded step time and 1-based channels; reading it as FSEQ
// produces garbage rather than an error, so it is rejected explicitly.
var ErrESEQUnsupported = fmt.Errorf("fseq: ESEQ format is not supported by this reader")

// ErrUnsupportedVersion means the file's major version is not 2. Only v2
// (any minor) is implemented.
type ErrUnsupportedVersion struct {
	Major, Minor byte
}

func (e *ErrUnsupportedVersion) Error() string {
	return fmt.Sprintf("fseq: unsupported version %d.%d (only major version 2 is supported)", e.Major, e.Minor)
}

// CompressionType is the compression scheme covering the file's channel
// data blocks. It's declared in the header as a 4-bit field.
type CompressionType byte

const (
	CompressionNone CompressionType = 0
	CompressionZstd CompressionType = 1
	CompressionZlib CompressionType = 2
)

func (c CompressionType) String() string {
	switch c {
	case CompressionNone:
		return "none"
	case CompressionZstd:
		return "zstd"
	case CompressionZlib:
		return "zlib"
	default:
		return fmt.Sprintf("unknown(%d)", byte(c))
	}
}

// ErrUnsupportedCompression means the file declares a compression type
// this reader will not attempt to decode. zlib is refused deliberately:
// RES-017 did not read FPP's zlib block framing and xLights does not write
// it by default, so guessing at the framing is not an option here. Any
// value above 2 is unknown to FPP itself and is refused for the same
// reason.
type ErrUnsupportedCompression struct {
	Type CompressionType
}

func (e *ErrUnsupportedCompression) Error() string {
	return fmt.Sprintf("fseq: unsupported compression type %s", e.Type)
}

// ErrMalformed means a structural invariant of the format was violated:
// a cross-check failed, a declared length doesn't fit the file, or a
// count is inconsistent with the data that follows it. FPP logs these and
// keeps going; this reader treats them as fatal, per RES-017 §4 and §10.
type ErrMalformed struct {
	Reason string
}

func (e *ErrMalformed) Error() string {
	return fmt.Sprintf("fseq: malformed file: %s", e.Reason)
}

// ErrFrameOutOfRange means the requested frame index is outside
// [0, FrameCount).
type ErrFrameOutOfRange struct {
	Frame      int
	FrameCount int
}

func (e *ErrFrameOutOfRange) Error() string {
	return fmt.Sprintf("fseq: frame %d out of range [0, %d)", e.Frame, e.FrameCount)
}

// ErrChannelRangeNotCovered means the requested absolute channel range is
// not fully present in this file's data. This is a refusal, not a partial
// read: a channel outside every sparse range is absent, not black, and
// returning zeros for it would be indistinguishable from real content.
type ErrChannelRangeNotCovered struct {
	RequestedStart uint32
	RequestedCount uint32
	// Covered lists the absolute channel ranges this file actually
	// carries, in file table order, for the caller to report against the
	// requested range.
	Covered []SparseRange
}

func (e *ErrChannelRangeNotCovered) Error() string {
	return fmt.Sprintf(
		"fseq: requested channel range [%d, %d) is not fully covered by this file's ranges %v",
		e.RequestedStart, e.RequestedStart+e.RequestedCount, e.Covered,
	)
}
