package assetstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// countFinalBlobs walks root, excluding the staging directory, and counts
// regular files — i.e. blobs actually committed to their content-addressed
// path.
func countFinalBlobs(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == stagingDirName {
				return filepath.SkipDir
			}
			return nil
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

func countStagingEntries(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, stagingDirName))
	if err != nil {
		t.Fatalf("read staging dir: %v", err)
	}
	return len(entries)
}

func newTestBackend(t *testing.T) *VolumeBackend {
	t.Helper()
	b, err := NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("NewVolumeBackend: %v", err)
	}
	return b
}

func TestVolumeBackendPutIsContentAddressed(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := []byte("the quick brown fox jumps over the lazy dog")

	first, err := b.Put(ctx, bytes.NewReader(payload), 1<<20)
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	second, err := b.Put(ctx, bytes.NewReader(payload), 1<<20)
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if first.ContentHash != second.ContentHash {
		t.Fatalf("identical bytes hashed differently: %q vs %q", first.ContentHash, second.ContentHash)
	}
	if !strings.HasPrefix(first.ContentHash, sha256Prefix) {
		t.Fatalf("ContentHash %q missing %q prefix", first.ContentHash, sha256Prefix)
	}
	if got, want := int(first.SizeBytes), len(payload); got != want {
		t.Fatalf("SizeBytes = %d, want %d", got, want)
	}

	// One file on disk despite two uploads of identical bytes.
	if got := countFinalBlobs(t, b.root); got != 1 {
		t.Fatalf("uploading identical bytes twice left %d files, want 1", got)
	}
}

func TestVolumeBackendDifferentContentDifferentFiles(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	a, err := b.Put(ctx, strings.NewReader("alpha"), 1<<20)
	if err != nil {
		t.Fatalf("Put alpha: %v", err)
	}
	z, err := b.Put(ctx, strings.NewReader("zeta"), 1<<20)
	if err != nil {
		t.Fatalf("Put zeta: %v", err)
	}
	if a.ContentHash == z.ContentHash {
		t.Fatalf("distinct content hashed the same: %q", a.ContentHash)
	}
	if got := countFinalBlobs(t, b.root); got != 2 {
		t.Fatalf("two distinct uploads left %d files, want 2", got)
	}
}

// erroringReader returns n bytes successfully and then a permanent read
// error, simulating a client that disconnects mid-upload.
type erroringReader struct {
	remaining []byte
	failAfter int
	read      int
}

func (e *erroringReader) Read(p []byte) (int, error) {
	if e.read >= e.failAfter {
		return 0, errors.New("simulated mid-upload disconnect")
	}
	n := copy(p, e.remaining[e.read:])
	if e.read+n > e.failAfter {
		n = e.failAfter - e.read
	}
	e.read += n
	return n, nil
}

// TestVolumeBackendInterruptedUploadRegistersNothing is acceptance
// criterion 5: an interrupted upload registers nothing. Breaking Put's
// defer-based cleanup (e.g. only removing the staging file on the
// size-limit path) turns this test red.
func TestVolumeBackendInterruptedUploadRegistersNothing(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("x"), 4096)
	r := &erroringReader{remaining: payload, failAfter: 1024}

	_, err := b.Put(ctx, r, int64(len(payload)))
	if err == nil {
		t.Fatal("Put succeeded despite a reader that errored mid-stream")
	}

	if got := countFinalBlobs(t, b.root); got != 0 {
		t.Fatalf("interrupted upload left %d committed blobs, want 0", got)
	}
	if got := countStagingEntries(t, b.root); got != 0 {
		t.Fatalf("interrupted upload left %d staging entries, want 0", got)
	}
}

// TestVolumeBackendEnforcesLimitWhileStreaming is acceptance criterion:
// the byte limit is enforced during the stream, not after buffering, and
// exceeding it removes the staging file and returns ErrTooLarge.
func TestVolumeBackendEnforcesLimitWhileStreaming(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("y"), 10_000)

	_, err := b.Put(ctx, bytes.NewReader(payload), 100)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put over the limit returned %v, want ErrTooLarge", err)
	}
	if got := countFinalBlobs(t, b.root); got != 0 {
		t.Fatalf("oversized upload left %d committed blobs, want 0", got)
	}
	if got := countStagingEntries(t, b.root); got != 0 {
		t.Fatalf("oversized upload left %d staging entries, want 0", got)
	}
}

func TestVolumeBackendPutAtExactlyTheLimitSucceeds(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("z"), 100)

	blob, err := b.Put(ctx, bytes.NewReader(payload), 100)
	if err != nil {
		t.Fatalf("Put at exactly the limit failed: %v", err)
	}
	if blob.SizeBytes != 100 {
		t.Fatalf("SizeBytes = %d, want 100", blob.SizeBytes)
	}
}

// enospcWriter fails every Write with syscall.ENOSPC, standing in for a
// full disk without touching a real filesystem.
type enospcWriter struct{}

func (enospcWriter) Write(p []byte) (int, error) { return 0, syscall.ENOSPC }
func (enospcWriter) Close() error                { return nil }

// TestVolumeBackendClassifiesDiskFull is the ENOSPC/EDQUOT classification
// acceptance criterion. Breaking the errors.Is(copyErr, syscall.ENOSPC)
// check in Put (e.g. returning the raw wrapped error instead of
// ErrNoSpace) turns this test red.
func TestVolumeBackendClassifiesDiskFull(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	orig := openStagingFile
	openStagingFile = func(string) (io.WriteCloser, error) { return enospcWriter{}, nil }
	t.Cleanup(func() { openStagingFile = orig })

	_, err := b.Put(ctx, strings.NewReader("more bytes than an ENOSPC writer can hold"), 1<<20)
	if !errors.Is(err, ErrNoSpace) {
		t.Fatalf("Put against a full backend returned %v, want ErrNoSpace", err)
	}
	if got := countFinalBlobs(t, b.root); got != 0 {
		t.Fatalf("a full-disk Put left %d committed blobs, want 0", got)
	}
}

func TestVolumeBackendPutRespectsContextCancellation(t *testing.T) {
	b := newTestBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.Put(ctx, strings.NewReader("cancelled before it starts"), 1<<20)
	if err == nil {
		t.Fatal("Put succeeded against an already-cancelled context")
	}
	if got := countFinalBlobs(t, b.root); got != 0 {
		t.Fatalf("cancelled Put left %d committed blobs, want 0", got)
	}
}

func TestVolumeBackendSweepsStagingOnStartup(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, stagingDirName)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("seed staging dir: %v", err)
	}
	orphan := filepath.Join(staging, "orphaned-upload")
	if err := os.WriteFile(orphan, []byte("leftover from a crash"), 0o644); err != nil {
		t.Fatalf("seed orphaned staging file: %v", err)
	}

	if _, err := NewVolumeBackend(root); err != nil {
		t.Fatalf("NewVolumeBackend: %v", err)
	}

	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned staging file still present after NewVolumeBackend (err=%v)", err)
	}
}

func TestVolumeBackendOpenServesWhatWasPut(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := []byte("open me back up")

	blob, err := b.Put(ctx, bytes.NewReader(payload), 1<<20)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, size, err := b.Open(ctx, blob.ContentHash)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rc.Close() }()

	if size != blob.SizeBytes {
		t.Fatalf("Open size = %d, want %d", size, blob.SizeBytes)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read opened blob: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("opened content = %q, want %q", got, payload)
	}
}

func TestVolumeBackendOpenIsNotFoundForAnUnknownHash(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	unknown := sha256Prefix + strings.Repeat("a", sha256HexLen)

	_, _, err := b.Open(ctx, unknown)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open of an unknown hash returned %v, want ErrNotFound", err)
	}
}

// TestVolumeBackendRejectsPathTraversalKeys plants a real file next to
// (not under) the backend's root and crafts a key that would resolve onto
// it via "../" if pathForKey only checked the "sha256:" prefix. A weaker
// check that merely rejects a missing file would still pass this test
// against the broken code by coincidence; using a file that actually
// exists is what makes the assertion mean something.
func TestVolumeBackendRejectsPathTraversalKeys(t *testing.T) {
	root := t.TempDir()
	b, err := NewVolumeBackend(root)
	if err != nil {
		t.Fatalf("NewVolumeBackend: %v", err)
	}
	ctx := context.Background()

	secretName := "secret-outside-root.txt"
	secretPath := filepath.Join(filepath.Dir(root), secretName)
	if err := os.WriteFile(secretPath, []byte("must never be served"), 0o644); err != nil {
		t.Fatalf("plant file outside root: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	malicious := sha256Prefix + "../" + secretName
	if _, _, err := b.Open(ctx, malicious); err == nil {
		t.Fatal("Open accepted a path-traversal key and would have served a file outside root")
	}
	if _, err := b.Stat(ctx, malicious); err == nil {
		t.Fatal("Stat accepted a path-traversal key and would have described a file outside root")
	}
}

func TestVolumeBackendStatMatchesSize(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	payload := bytes.Repeat([]byte("s"), 12345)

	blob, err := b.Put(ctx, bytes.NewReader(payload), 1<<20)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	size, err := b.Stat(ctx, blob.ContentHash)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if size != blob.SizeBytes {
		t.Fatalf("Stat size = %d, want %d", size, blob.SizeBytes)
	}
}

func TestVolumeBackendStatIsNotFoundForAnUnknownHash(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	unknown := sha256Prefix + strings.Repeat("b", sha256HexLen)

	if _, err := b.Stat(ctx, unknown); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat of an unknown hash returned %v, want ErrNotFound", err)
	}
}

// TestVolumeBackendThreeSameFilenameDifferentTargets mirrors ADR-028
// decision 1 and this seam's acceptance criterion 2: three uploads
// sharing a filename (which this package never sees or keys on) but
// carrying different bytes each resolve to their own distinct blob.
func TestVolumeBackendThreeSameFilenameDifferentTargets(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	variants := map[string][]byte{
		"media-front":  []byte("front channels"),
		"media-side":   []byte("side channels"),
		"media-garage": []byte("garage channels"),
	}
	blobs := map[string]Blob{}
	for target, content := range variants {
		blob, err := b.Put(ctx, bytes.NewReader(content), 1<<20)
		if err != nil {
			t.Fatalf("Put for %s: %v", target, err)
		}
		blobs[target] = blob
	}

	for target, content := range variants {
		rc, _, err := b.Open(ctx, blobs[target].ContentHash)
		if err != nil {
			t.Fatalf("Open for %s: %v", target, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", target, err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("%s resolved to %q, want %q", target, got, content)
		}
	}
	if got := countFinalBlobs(t, b.root); got != 3 {
		t.Fatalf("three distinct-content uploads left %d blobs, want 3", got)
	}
}
