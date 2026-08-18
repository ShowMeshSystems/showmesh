package fseq

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzOpen asserts that Open never panics and never allocates unbounded
// memory from a declared length in the header or tables, on arbitrary
// bytes. This package reads a file that arrived over the network onto a
// node; a malformed or truncated FSEQ must always come back as an error,
// never a panic.
func FuzzOpen(f *testing.F) {
	// Seed with every fixture shape fixtures_test.go builds, since they
	// are the shapes this reader is actually meant to parse (sparse
	// offsets, multiple blocks, a short final block, a padded variable
	// header, overlapping sparse ranges).
	seedSpecs := []fixtureSpec{
		{compression: 0, channelCount: 7, frameCount: 3, stepTimeMS: 25,
			sparse: []SparseRange{{Start: 100, Length: 7}}},
		{compression: 1, channelCount: 8, frameCount: 4, stepTimeMS: 25,
			sparse: []SparseRange{{Start: 50, Length: 5}, {Start: 10, Length: 3}}},
		{compression: 1, channelCount: 35, frameCount: 3, stepTimeMS: 25,
			sparse: []SparseRange{{Start: 10, Length: 20}, {Start: 29, Length: 15}}},
		{compression: 1, channelCount: 12, frameCount: 6, stepTimeMS: 25,
			framesPerBlock: 3, declareExtraZeroBlocks: 2},
		{compression: 1, channelCount: 9, frameCount: 5, stepTimeMS: 25,
			framesPerBlock: 4},
		{compression: 1, channelCount: 15, frameCount: 3, stepTimeMS: 25,
			varHeaderCode: "sp", varHeaderData: "gen-fixture 1.0"},
	}
	for _, spec := range seedSpecs {
		f.Add(buildFixture(spec))
	}

	// Deliberately malformed inputs.
	f.Add([]byte{})         // empty
	f.Add([]byte{'P', 'S'}) // too short for the header
	f.Add(make([]byte, 32)) // all zero: bad magic
	f.Add([]byte("PSEQ" + string(make([]byte, 28))))
	f.Add([]byte("ESEQ" + string(make([]byte, 28))))
	// PSEQ header claiming an enormous block count and channel data
	// offset with no data behind it.
	f.Add([]byte{
		'P', 'S', 'E', 'Q', 0xff, 0xff, 2, 2, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		25, 0, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Open panicked on input (%d bytes): %v", len(data), r)
			}
		}()
		path := filepath.Join(t.TempDir(), "fuzz.fseq")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("writing fuzz input: %v", err)
		}
		file, err := Open(path)
		if err != nil {
			return
		}
		defer func() { _ = file.Close() }()

		// If it opened, exercise ChannelRange too — a crafted but
		// header-valid file with a corrupt block table is exactly the
		// case that should error, not panic, once frame data is touched.
		if file.FrameCount() > 0 && file.ChannelCount() > 0 {
			dst := make([]byte, 1)
			_ = file.ChannelRange(0, 0, 1, dst)
		}
	})
}
