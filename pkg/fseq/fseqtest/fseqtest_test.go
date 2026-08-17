package fseqtest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
	"github.com/showmeshsystems/showmesh/pkg/fseq/fseqtest"
)

// TestBuildIsReadableByTheRealReader is the whole point of this package
// having a test: a builder that produces bytes the real reader rejects, or
// accepts and decodes differently, would silently break every test that
// depends on it rather than failing here.
func TestBuildIsReadableByTheRealReader(t *testing.T) {
	const channels, frames, stepTimeMS = 12, 5, 25

	path := filepath.Join(t.TempDir(), "built.fseq")
	if err := os.WriteFile(path, fseqtest.Build(channels, frames, stepTimeMS), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	f, err := fseq.Open(path)
	if err != nil {
		t.Fatalf("fseq.Open() on built bytes: %v", err)
	}
	defer func() { _ = f.Close() }()

	if got := f.FrameCount(); got != frames {
		t.Errorf("FrameCount() = %d, want %d", got, frames)
	}
	if got := f.StepTimeMS(); got != stepTimeMS {
		t.Errorf("StepTimeMS() = %d, want %d", got, stepTimeMS)
	}

	// Every frame, and a mid-file channel offset rather than 0, so an
	// off-by-one in the channel stride cannot pass.
	const start, count = 3, 6
	dst := make([]byte, count)
	for frame := 0; frame < frames; frame++ {
		if err := f.ChannelRange(frame, start, count, dst); err != nil {
			t.Fatalf("ChannelRange(frame=%d): %v", frame, err)
		}
		for i := range dst {
			want := fseqtest.FrameByte(frame, start+i)
			if dst[i] != want {
				t.Fatalf("frame %d channel %d = %d, want %d", frame, start+i, dst[i], want)
			}
		}
	}
}
