package fseq

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realFileDirs are searched for real xLights-written .fseq artifacts.
// ~/Documents and ~/Downloads are where RES-017 found the 198 files this
// package cites as evidence; ~/showmesh-fseq-samples is a directory the
// track orchestrator asked builders to also check, deliberately outside
// every git worktree because this repository is public and show content
// must never be committed to it. None of these paths exist on another
// machine or in CI, so their absence is a clean skip, never a failure.
func realFileDirs(t *testing.T) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home directory to look for real .fseq files: %v", err)
	}
	return []string{
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Downloads"),
		filepath.Join(home, "showmesh-fseq-samples"),
	}
}

func findRealFseqFiles(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, dir := range realFileDirs(t) {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // permission errors etc: skip this entry, keep walking
			}
			if d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".fseq") {
				found = append(found, path)
			}
			return nil
		})
	}
	return found
}

// rawParse independently re-derives the header, block table (with
// computed offsets) and sparse table straight from the file's bytes,
// duplicating none of pkg/fseq's own parsing code. It exists so
// TestRealFiles_StructuralInvariants and the differential test in
// realfiles_differential_test.go check pkg/fseq's block-table walk
// against a second, independently written reading of the same rule —
// this project's own lesson is that duplication is what found the bug in
// the code that replaced it.
type rawBlock struct {
	firstFrame uint32
	offset     uint64
	length     uint64
}

type rawParsed struct {
	compression    byte
	channelCount   uint32
	frameCount     uint32
	chanDataOffset uint64
	fileSize       uint64
	blocks         []rawBlock // non-zero-length only, offsets accumulated
	sparse         []SparseRange
}

func rawParse(path string) (*rawParsed, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := uint64(fi.Size())
	hdr := make([]byte, 32)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return nil, err
	}
	if string(hdr[0:4]) != "PSEQ" && string(hdr[0:4]) != "FSEQ" {
		return nil, errors.New("not PSEQ/FSEQ")
	}
	chanDataOffset := uint64(binary.LittleEndian.Uint16(hdr[4:6]))
	channelCount := binary.LittleEndian.Uint32(hdr[10:14])
	frameCount := binary.LittleEndian.Uint32(hdr[14:18])
	compression := hdr[20] & 0x0F
	numBlocks := (uint32(hdr[20]&0xF0) << 4) | uint32(hdr[21])
	numSparse := uint32(hdr[22])

	table := make([]byte, uint64(numBlocks)*8+uint64(numSparse)*6)
	if _, err := io.ReadFull(f, table); err != nil {
		return nil, err
	}

	var blocks []rawBlock
	off := chanDataOffset
	for i := uint32(0); i < numBlocks; i++ {
		e := table[i*8 : i*8+8]
		length := binary.LittleEndian.Uint32(e[4:8])
		if length == 0 {
			continue
		}
		blocks = append(blocks, rawBlock{
			firstFrame: binary.LittleEndian.Uint32(e[0:4]),
			offset:     off,
			length:     uint64(length),
		})
		off += uint64(length)
	}

	sparseRaw := table[uint64(numBlocks)*8:]
	sparse := make([]SparseRange, numSparse)
	for i := range sparse {
		e := sparseRaw[i*6 : i*6+6]
		sparse[i] = SparseRange{
			Start:  uint32(e[0]) | uint32(e[1])<<8 | uint32(e[2])<<16,
			Length: uint32(e[3]) | uint32(e[4])<<8 | uint32(e[5])<<16,
		}
	}

	return &rawParsed{
		compression:    compression,
		channelCount:   channelCount,
		frameCount:     frameCount,
		chanDataOffset: chanDataOffset,
		fileSize:       size,
		blocks:         blocks,
		sparse:         sparse,
	}, nil
}

// zstdMagic is the standard zstd frame magic number, little-endian on
// disk. RES-017 §5 measured 600/600 computed block offsets in a real file
// beginning with these four bytes.
var zstdMagic = [4]byte{0x28, 0xB5, 0x2F, 0xFD}

func TestRealFiles_StructuralInvariants(t *testing.T) {
	files := findRealFseqFiles(t)
	if len(files) == 0 {
		t.Skip("no real .fseq files found under ~/Documents, ~/Downloads or ~/showmesh-fseq-samples; skipping real-file verification (this is expected on a machine other than the one RES-017 was researched on)")
	}
	t.Logf("found %d real .fseq files", len(files))

	var opened, zstdChecked int
	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := rawParse(path)
			if err != nil {
				t.Fatalf("independent raw parse failed: %v", err)
			}

			// Invariant: every computed block offset lands on the zstd
			// frame magic, for zstd files (RES-017 measured this 600/600
			// on a real file; §9 measured all 198 owner files as zstd).
			if raw.compression == byte(CompressionZstd) {
				zstdChecked++
				magic := make([]byte, 4)
				f, err := os.Open(path)
				if err != nil {
					t.Fatalf("open for magic check: %v", err)
				}
				defer func() { _ = f.Close() }()
				for _, b := range raw.blocks {
					if _, err := f.ReadAt(magic, int64(b.offset)); err != nil {
						t.Fatalf("reading block at computed offset %d: %v", b.offset, err)
					}
					if [4]byte(magic) != zstdMagic {
						t.Fatalf("block at computed offset %d does not start with zstd magic: % x", b.offset, magic)
					}
				}
			}

			// Invariant: cumulative block lengths never run past the file.
			if len(raw.blocks) > 0 {
				last := raw.blocks[len(raw.blocks)-1]
				if end := last.offset + last.length; end > raw.fileSize {
					t.Fatalf("cumulative block offsets reach %d, past file size %d", end, raw.fileSize)
				}
			}

			// Invariant: sparse ranges are within the declared channel
			// space (checked here independently of Open's own sum check).
			var sum uint64
			for _, r := range raw.sparse {
				sum += uint64(r.Length)
			}
			if len(raw.sparse) > 0 && sum != uint64(raw.channelCount) {
				t.Fatalf("sparse range lengths sum to %d, header channel count is %d", sum, raw.channelCount)
			}

			// Now open through the real package and confirm it agrees
			// this file is well-formed, and can actually serve a frame.
			file, err := Open(path)
			if err != nil {
				t.Fatalf("fseq.Open: %v", err)
			}
			defer func() { _ = file.Close() }()
			opened++

			if got := uint32(file.FrameCount()); got != raw.frameCount {
				t.Fatalf("FrameCount() = %d, raw header says %d", got, raw.frameCount)
			}
			if got := uint32(file.ChannelCount()); got != raw.channelCount {
				t.Fatalf("ChannelCount() = %d, raw header says %d", got, raw.channelCount)
			}

			if file.FrameCount() == 0 {
				return
			}
			ranges := file.SparseRanges()
			start, count := 0, file.ChannelCount()
			if len(ranges) > 0 {
				start, count = int(ranges[0].Start), int(ranges[0].Length)
			}
			dst := make([]byte, count)
			for _, frame := range []int{0, file.FrameCount() / 2, file.FrameCount() - 1} {
				if err := file.ChannelRange(frame, start, count, dst); err != nil {
					t.Fatalf("ChannelRange(frame %d): %v", frame, err)
				}
			}
		})
	}
	t.Logf("opened %d files through fseq.Open, %d checked for zstd block-magic alignment", opened, zstdChecked)
}
