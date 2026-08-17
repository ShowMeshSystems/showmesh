package fseq

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// naiveDecodeAll independently reconstructs every frame of a zstd file by
// decompressing each block in file (table) order and concatenating the
// results, with no block cache, no binary search, and no code shared with
// fseq.go's block-navigation path. It is deliberately the simplest
// correct implementation of "read the whole file" rather than an
// optimized one.
func naiveDecodeAll(path string) (channelCount int, frames [][]byte, err error) {
	raw, err := rawParse(path)
	if err != nil {
		return 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = f.Close() }()

	dec, err := zstd.NewReader(nil)
	if err != nil {
		return 0, nil, err
	}
	defer dec.Close()

	var all []byte
	for _, b := range raw.blocks {
		compressed := make([]byte, b.length)
		if _, err := io.ReadFull(io.NewSectionReader(f, int64(b.offset), int64(b.length)), compressed); err != nil {
			return 0, nil, err
		}
		decoded, err := dec.DecodeAll(compressed, nil)
		if err != nil {
			return 0, nil, err
		}
		all = append(all, decoded...)
	}

	cc := int(raw.channelCount)
	if cc == 0 || len(all)%cc != 0 {
		return cc, nil, nil
	}
	n := len(all) / cc
	frames = make([][]byte, n)
	for i := 0; i < n; i++ {
		frames[i] = all[i*cc : (i+1)*cc]
	}
	return cc, frames, nil
}

// TestRealFiles_DifferentialBlockWalk parses a handful of real zstd files
// two independent ways — pkg/fseq's own block-table walk (which caches
// one block and finds it by binary search) versus a naive whole-file
// concatenation with no cache and no search — and asserts they produce
// identical bytes for the first, a middle, and the last frame. This is
// the check this project's LESSONS.md calls for: duplication is what
// found the bug in the code that replaced it, and this is the seam most
// likely to hide one, since the block-table walk is "the single most
// important structural rule in the format" per RES-017 §5.
func TestRealFiles_DifferentialBlockWalk(t *testing.T) {
	files := findRealFseqFiles(t)
	if len(files) == 0 {
		t.Skip("no real .fseq files found under ~/Documents, ~/Downloads or ~/showmesh-fseq-samples; skipping differential verification")
	}

	// Bounded to a handful of files: this test decodes an entire file
	// twice, once frame by frame, which is materially more work than
	// TestRealFiles_StructuralInvariants' spot checks.
	const maxFiles = 8
	checked := 0
	for _, path := range files {
		if checked >= maxFiles {
			break
		}
		raw, err := rawParse(path)
		if err != nil || raw.compression != byte(CompressionZstd) || len(raw.sparse) == 0 {
			continue // this differential is written for the sparse/zstd shape B3 actually reads
		}
		checked++
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			cc, naiveFrames, err := naiveDecodeAll(path)
			if err != nil {
				t.Fatalf("naive decode: %v", err)
			}
			if naiveFrames == nil {
				t.Fatalf("naive decode produced no frames (channelCount=%d)", cc)
			}

			file, err := Open(path)
			if err != nil {
				t.Fatalf("fseq.Open: %v", err)
			}
			defer func() { _ = file.Close() }()

			if len(naiveFrames) != file.FrameCount() {
				t.Fatalf("naive decode found %d frames, fseq.Open reports %d", len(naiveFrames), file.FrameCount())
			}

			dst := make([]byte, file.ChannelCount())
			frameIdxs := []int{0, file.FrameCount() / 2, file.FrameCount() - 1}
			for _, fr := range frameIdxs {
				if err := file.ChannelRange(fr, 0, file.ChannelCount(), dst); err != nil {
					// Some real files' first sparse range does not start
					// at 0; ChannelRange over the full [0, ChannelCount)
					// window is only valid when it does. Fall back to
					// comparing the file's own first range instead.
					ranges := file.SparseRanges()
					if len(ranges) == 0 {
						t.Fatalf("ChannelRange(frame %d): %v", fr, err)
					}
					start, count := int(ranges[0].Start), int(ranges[0].Length)
					got := make([]byte, count)
					if err := file.ChannelRange(fr, start, count, got); err != nil {
						t.Fatalf("ChannelRange(frame %d, first range): %v", fr, err)
					}
					want := naiveFrames[fr][:count] // naive frame is packed in sparse-table order; first entry starts at offset 0
					if string(got) != string(want) {
						t.Fatalf("frame %d: block-walk and naive decode disagree on the file's first sparse range", fr)
					}
					continue
				}
				if string(dst) != string(naiveFrames[fr]) {
					t.Fatalf("frame %d: block-walk and naive decode disagree", fr)
				}
			}
		})
	}
	if checked == 0 {
		t.Skip("no sparse zstd real files found to run the differential check against")
	}
	t.Logf("differential-checked %d real files", checked)
}
