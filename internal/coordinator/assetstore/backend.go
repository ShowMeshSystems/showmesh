package assetstore

import (
	"context"
	"errors"
	"io"
)

// sha256Prefix is how a content hash is written everywhere in this
// package and in the assets table's content_hash column: "sha256:<hex>".
const sha256Prefix = "sha256:"

// ErrNoSpace is returned by [Backend.Put] when the backend ran out of disk
// space or exceeded a filesystem quota while writing (ENOSPC/EDQUOT).
// Nothing is registered: the partially written staging data is discarded
// before this error is returned. ARCHITECTURE §11 names disk exhaustion as
// a failure mode this project has to classify rather than let surface as
// an opaque write error.
var ErrNoSpace = errors.New("assetstore: storage is full")

// ErrTooLarge is returned by [Backend.Put] when the stream exceeds the
// limit passed to it. The limit is enforced while streaming, never by
// buffering the whole upload first; the staging data is discarded before
// this error is returned.
var ErrTooLarge = errors.New("assetstore: upload exceeds the configured limit")

// ErrNotFound is returned by [Backend.Open] and [Backend.Stat] when key
// names no stored blob.
var ErrNotFound = errors.New("assetstore: blob not found")

// Blob is what [Backend.Put] returns: the content hash it stored the
// stream under, in "sha256:<hex>" form, and the exact number of bytes
// read.
type Blob struct {
	ContentHash string
	SizeBytes   int64
}

// Backend stores and serves asset bytes, content-addressed by sha256.
type Backend interface {
	// Put streams r into the backend, returning the content hash and byte
	// count. It writes to a temporary name and renames into place only
	// after the whole stream has been read and hashed.
	Put(ctx context.Context, r io.Reader, limit int64) (Blob, error)

	// Open returns the blob stored under key (a Blob.ContentHash value)
	// for reading, plus its true on-disk size, so a caller can serve it
	// via http.ServeContent with Range support. Open does not re-hash the
	// content — that would make every read O(file) — so a caller that
	// must detect truncation compares the returned size against an
	// expected size it already holds (e.g. the assets table's
	// size_bytes) and refuses to serve on a mismatch rather than trusting
	// a cached size.
	Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error)

	// Stat returns the size of the blob stored under key without opening
	// it.
	Stat(ctx context.Context, key string) (int64, error)
}
