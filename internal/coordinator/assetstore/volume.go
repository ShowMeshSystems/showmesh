package assetstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

// stagingDirName is the subdirectory Put stages uploads under before
// renaming them into their content-addressed final path. It sits inside
// the same root as the finished blobs so the rename is same-filesystem
// and atomic.
const stagingDirName = ".staging"

// sha256HexLen is the length of a sha256 digest written as lowercase hex.
const sha256HexLen = 64

// VolumeBackend is a Backend backed by a directory on the coordinator's
// own filesystem or a mounted volume: <root>/<hex[0:2]>/<hex> per blob,
// with uploads staged under <root>/.staging while in flight.
type VolumeBackend struct {
	root string
}

// NewVolumeBackend opens (creating if necessary) a volume-backed store
// rooted at root, and sweeps any staging files left behind by a process
// that crashed mid-upload. Those bytes were never renamed into a
// content-addressed path, so no asset ever referenced them; leaving them
// in place would leak disk forever.
func NewVolumeBackend(root string) (*VolumeBackend, error) {
	if root == "" {
		return nil, errors.New("assetstore: root must not be empty")
	}
	staging := filepath.Join(root, stagingDirName)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return nil, fmt.Errorf("assetstore: create staging dir: %w", err)
	}
	if err := sweepStaging(staging); err != nil {
		return nil, fmt.Errorf("assetstore: sweep staging dir: %w", err)
	}
	return &VolumeBackend{root: root}, nil
}

// sweepStaging removes every entry under staging and nothing outside it.
func sweepStaging(staging string) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(staging, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// openStagingFile creates the staging file Put writes to. It is a package
// variable rather than a direct os.OpenFile call solely so a test can
// substitute a writer that fails with ENOSPC/EDQUOT, proving Put's
// disk-full classification without writing to a real filesystem;
// production always uses this default.
var openStagingFile = func(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}

// ctxReader fails a Read once ctx is done, so Put's streaming copy stops
// promptly on caller cancellation instead of running to completion.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// Put implements Backend.
//
// It stages the stream under <root>/.staging, hashing while it writes,
// then renames into its final content-addressed path only once the whole
// stream has been read without error. Every failure return below removes
// the staging data first: there is no path from a read error, a
// size-limit breach, or a write error to a rename.
func (b *VolumeBackend) Put(ctx context.Context, r io.Reader, limit int64) (Blob, error) {
	if limit < 0 {
		return Blob{}, fmt.Errorf("assetstore: negative limit %d", limit)
	}

	stagingPath := filepath.Join(b.root, stagingDirName, uuid.NewString())
	w, err := openStagingFile(stagingPath)
	if err != nil {
		return Blob{}, fmt.Errorf("assetstore: create staging file: %w", err)
	}

	renamed := false
	defer func() {
		_ = w.Close()
		if !renamed {
			_ = os.Remove(stagingPath)
		}
	}()

	h := sha256.New()
	// Read at most limit+1 bytes: enough to detect an oversized stream
	// without buffering it and without reading past what the caller
	// bounded on purpose.
	src := io.LimitReader(ctxReader{ctx: ctx, r: r}, limit+1)
	n, copyErr := io.Copy(io.MultiWriter(w, h), src)
	if copyErr != nil {
		// ENOSPC/EDQUOT is a classified, expected failure mode
		// (ARCHITECTURE §11), not a generic write error: callers map
		// ErrNoSpace to a specific refusal instead of a bare 500.
		if errors.Is(copyErr, syscall.ENOSPC) || errors.Is(copyErr, syscall.EDQUOT) {
			return Blob{}, ErrNoSpace
		}
		return Blob{}, fmt.Errorf("assetstore: write staging file: %w", copyErr)
	}
	if n > limit {
		return Blob{}, ErrTooLarge
	}

	if err := w.Close(); err != nil {
		return Blob{}, fmt.Errorf("assetstore: close staging file: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	finalDir := filepath.Join(b.root, hash[:2])
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return Blob{}, fmt.Errorf("assetstore: create blob dir: %w", err)
	}
	// Identical bytes uploaded before means this path already exists;
	// renaming over it is a same-content overwrite, not a conflict, so
	// two uploads of one file always converge on one file.
	if err := os.Rename(stagingPath, filepath.Join(finalDir, hash)); err != nil {
		return Blob{}, fmt.Errorf("assetstore: rename into place: %w", err)
	}
	renamed = true

	return Blob{ContentHash: sha256Prefix + hash, SizeBytes: n}, nil
}

// pathForKey turns a Blob.ContentHash-shaped key into the file it names,
// rejecting anything that is not exactly a 64-character lowercase hex
// sha256 digest behind the "sha256:" prefix.
//
// This is a path-safety check, not only a format check: an unvalidated
// key becomes a filesystem path below, and this is what stops a key built
// from "../" or other unexpected bytes from ever reaching os.Open/os.Stat.
func (b *VolumeBackend) pathForKey(key string) (string, error) {
	hash, ok := strings.CutPrefix(key, sha256Prefix)
	if !ok || len(hash) != sha256HexLen || !isLowerHex(hash) {
		return "", fmt.Errorf("assetstore: invalid key %q", key)
	}
	return filepath.Join(b.root, hash[:2], hash), nil
}

func isLowerHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Open implements Backend.
func (b *VolumeBackend) Open(_ context.Context, key string) (io.ReadSeekCloser, int64, error) {
	path, err := b.pathForKey(key)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// Stat implements Backend.
func (b *VolumeBackend) Stat(_ context.Context, key string) (int64, error) {
	path, err := b.pathForKey(key)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return 0, err
	}
	return info.Size(), nil
}
