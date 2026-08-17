package fseq

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// knownSamplePath is a real, full (non-sparse-render) FSEQ the owner
// rendered and dropped outside every git worktree deliberately (the
// repository is public; show content is not). The owner independently
// decoded its header and sparse table by hand and reported the expected
// values below; this test checks pkg/fseq agrees with that independent
// reading, not just with itself.
const knownSamplePath = "kpop 2026 MH Test.fseq"

func knownSampleFile(t *testing.T) *File {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory: %v", err)
	}
	path := filepath.Join(home, "showmesh-fseq-samples", knownSamplePath)
	if _, err := os.Stat(path); err != nil {
		t.Skip("owner's known-good sample not present at ~/showmesh-fseq-samples/" + knownSamplePath + "; skipping known-value verification")
	}
	f, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestKnownSample_HeaderMatchesIndependentReading asserts pkg/fseq's
// header parse against values the owner decoded independently (not with
// this package). channelDataOffset=27992, fixedHeaderLen=27838,
// channelsPerFrame=698120, frames=13853, stepTime=25ms, zstd, 13 sparse
// ranges, 3466 compression blocks.
//
// 32 + 3466*8 + 13*6 = 27838 closes exactly against the reported
// fixedHeaderLen, which independently confirms the block-table entry
// size (8 bytes), the sparse-range entry size (6 bytes) and the 12-bit
// split block count on a real file, not just on this package's own
// synthetic fixtures.
func TestKnownSample_HeaderMatchesIndependentReading(t *testing.T) {
	f := knownSampleFile(t)

	if got, want := f.ChannelCount(), 698120; got != want {
		t.Errorf("ChannelCount() = %d, want %d", got, want)
	}
	if got, want := f.FrameCount(), 13853; got != want {
		t.Errorf("FrameCount() = %d, want %d", got, want)
	}
	if got, want := f.StepTimeMS(), byte(25); got != want {
		t.Errorf("StepTimeMS() = %d, want %d", got, want)
	}
	if got, want := f.Compression(), CompressionZstd; got != want {
		t.Errorf("Compression() = %s, want %s", got, want)
	}
	// 3466 is the *declared* block count from the header (bits packed
	// across h[20]/h[21], per RES-017 §5); this package only keeps
	// non-zero-length entries in f.blocks (RES-017 §5, §10.2: "the
	// declared block count exceeds the used count"), so the used count
	// is expected to be, and measured here as, smaller. This exact
	// 3466-declared/3464-used pair is the one RES-017 itself cites as a
	// measured example, on this same file.
	const declaredBlocks = 3466
	if got, want := len(f.blocks), 3464; got != want {
		t.Errorf("used (non-zero-length) compression block count = %d, want %d", got, want)
	}
	if len(f.blocks) >= declaredBlocks {
		t.Errorf("used block count %d should be less than declared %d", len(f.blocks), declaredBlocks)
	}

	const computedFixedHeaderLen = 32 + declaredBlocks*8 + 13*6
	if computedFixedHeaderLen != 27838 {
		t.Fatalf("test arithmetic itself is wrong: %d != 27838", computedFixedHeaderLen)
	}
	// Open() itself only succeeds if its own computed fixed-header length
	// (from the *declared* block/sparse counts) equals the header's own
	// declared fixedHeaderLen field (RES-017 §4, §10.11), so a successful
	// Open above is already evidence the 27838 figure was matched
	// internally, not just asserted here independently.
}

// TestKnownSample_SparseRangesMatchIndependentReading asserts every one
// of the file's 13 sparse ranges, and their derived frame-data offsets,
// against the owner's independent decode.
func TestKnownSample_SparseRangesMatchIndependentReading(t *testing.T) {
	f := knownSampleFile(t)

	want := []SparseRange{
		{0, 735}, {741, 114}, {867, 24}, {897, 114}, {1023, 39},
		{1068, 144}, {1224, 24}, {1260, 114}, {1398, 252}, {1665, 522},
		{2202, 22777}, {24980, 48}, {25393, 673213},
	}
	wantOffsets := []uint64{0, 735, 849, 873, 987, 1026, 1170, 1194, 1308, 1560, 2082, 24859, 24907}

	got := f.SparseRanges()
	if len(got) != len(want) {
		t.Fatalf("SparseRanges() has %d entries, want %d", len(got), len(want))
	}
	var sum uint32
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %v, want %v", i, got[i], want[i])
		}
		if f.sparseOffsets[i] != wantOffsets[i] {
			t.Errorf("range %d frame offset = %d, want %d", i, f.sparseOffsets[i], wantOffsets[i])
		}
		sum += want[i].Length
	}
	if sum != 698120 {
		t.Fatalf("test arithmetic itself is wrong: sum %d != 698120", sum)
	}
	if got, want := f.ChannelCount(), int(sum); got != want {
		t.Errorf("ChannelCount() = %d, want sparse-length sum %d", got, want)
	}
}

// TestKnownSample_GapsAreRefusedNeverZero exercises the file's real gaps
// — this is a full, non-sparse render that still carries 13 sparse
// ranges, so channels between ranges (735..740, 855..866, 891..896, ...)
// are genuinely absent from the file, not just a hypothesised case.
func TestKnownSample_GapsAreRefusedNeverZero(t *testing.T) {
	f := knownSampleFile(t)

	gaps := []struct {
		start, count int
	}{
		{735, 6},  // between range 0 and range 1
		{855, 12}, // between range 1 and range 2
		{891, 6},  // between range 2 and range 3
	}
	for _, g := range gaps {
		dst := make([]byte, g.count)
		err := f.ChannelRange(0, g.start, g.count, dst)
		var notCovered *ErrChannelRangeNotCovered
		if !errors.As(err, &notCovered) {
			t.Errorf("ChannelRange(%d, %d) = %v, want *ErrChannelRangeNotCovered", g.start, g.count, err)
		}
	}
}

// TestKnownSample_Matrix1SurfaceRange is the end-to-end assertion the
// track orchestrator asked for: the owner's real surface start channel,
// xLights' 1-based "matrix 1 starts at 25410", converted to this
// package's 0-based space (25409) and read as 480000 channels, must
// succeed and must resolve to frame-data offset 24923
// (sparseOffsets[12]=24907, plus 25409-25393=16 into that range).
//
// pkg/fseq itself is purely 0-based, matching the file; the 1-based
// xLights UI number is the caller's conversion to make once, not
// pkg/fseq's problem — see the package doc comment.
func TestKnownSample_Matrix1SurfaceRange(t *testing.T) {
	f := knownSampleFile(t)

	const matrix1StartOneBased = 25410
	const matrix1StartZeroBased = matrix1StartOneBased - 1
	const matrix1Channels = 480000

	if matrix1StartZeroBased != 25409 {
		t.Fatalf("test arithmetic itself is wrong")
	}

	dst := make([]byte, matrix1Channels)
	if err := f.ChannelRange(0, matrix1StartZeroBased, matrix1Channels, dst); err != nil {
		t.Fatalf("ChannelRange(matrix 1): %v", err)
	}

	segs, ok := f.resolveSegments(uint64(matrix1StartZeroBased), uint64(matrix1StartZeroBased+matrix1Channels))
	if !ok || len(segs) == 0 {
		t.Fatalf("resolveSegments did not resolve matrix 1's range")
	}
	if got, want := segs[0].srcOffset, uint64(24923); got != want {
		t.Errorf("matrix 1's first byte resolves to frame-data offset %d, want %d", got, want)
	}
}

// TestKnownSample_Matrix2SurfaceRange is the same check for the second
// deployed surface: 1-based 505410 (0-based 505409), which the owner
// reports resolves to frame-data offset 504923.
func TestKnownSample_Matrix2SurfaceRange(t *testing.T) {
	f := knownSampleFile(t)

	const matrix2StartOneBased = 505410
	const matrix2StartZeroBased = matrix2StartOneBased - 1

	segs, ok := f.resolveSegments(uint64(matrix2StartZeroBased), uint64(matrix2StartZeroBased+1))
	if !ok || len(segs) == 0 {
		t.Fatalf("resolveSegments did not resolve matrix 2's start channel")
	}
	if got, want := segs[0].srcOffset, uint64(504923); got != want {
		t.Errorf("matrix 2's first byte resolves to frame-data offset %d, want %d", got, want)
	}
}
