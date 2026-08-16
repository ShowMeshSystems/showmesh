package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// assetHTTPClient is the HTTP client asset.fetch uses to download bytes. A
// package-level var (not a bare http.DefaultClient reference) so tests can
// swap it for one pointed at an httptest.Server without touching
// http.DefaultClient, which other packages or tests might also depend on.
var assetHTTPClient = &http.Client{}

// readBackAssetFunc is readBackAsset's package variable form, matching
// assetHTTPClient's pattern: a test can substitute it to prove the call
// site actually invokes a post-write read-back rather than trusting the
// in-memory download hash.
var readBackAssetFunc = readBackAsset

// assetFetchOperation is the OperationFunc for "asset.fetch": download a
// file from a coordinator-issued URL into this node's asset directory,
// verify its content hash, and only then make it available under its
// runtime filename. dir is the node's asset directory
// (config.Config.AssetDir); token, when non-empty, is sent as a bearer
// credential on the download request.
type assetFetchOperation struct {
	dir   string
	token string
}

// ErrAssetFilenameUnsafe is returned when a requested filename contains a
// path separator or a ".." segment, which would otherwise let a malicious or
// buggy coordinator response write outside dir.
var ErrAssetFilenameUnsafe = errors.New("asset.fetch: filename is not a safe bare filename")

// validateAssetFilename rejects any filename that is not a single, safe path
// segment: no '/', no '\', and no ".." component. This check runs before
// filename is ever joined into a filesystem path, per this seam's own rule
// that a filename must be rejected before it becomes a path, not sanitized
// after.
func validateAssetFilename(filename string) error {
	if filename == "" {
		return fmt.Errorf("%w: empty", ErrAssetFilenameUnsafe)
	}
	if strings.ContainsAny(filename, `/\`) {
		return fmt.Errorf("%w: %q contains a path separator", ErrAssetFilenameUnsafe, filename)
	}
	if filename == "." || filename == ".." {
		return fmt.Errorf("%w: %q is not a bare filename", ErrAssetFilenameUnsafe, filename)
	}
	return nil
}

// parseAssetFetchParams extracts and type-checks asset.fetch's five
// required params. Every failure names the offending param.
func parseAssetFetchParams(params map[string]any) (assetID, contentHash, filename, rawURL string, sizeBytes int64, err error) {
	str := func(key string) (string, error) {
		raw, ok := params[key]
		if !ok {
			return "", fmt.Errorf("asset.fetch: params.%s is required", key)
		}
		v, ok := raw.(string)
		if !ok || v == "" {
			return "", fmt.Errorf("asset.fetch: params.%s must be a non-empty string, got %T", key, raw)
		}
		return v, nil
	}

	if assetID, err = str("assetId"); err != nil {
		return
	}
	if contentHash, err = str("contentHash"); err != nil {
		return
	}
	if filename, err = str("filename"); err != nil {
		return
	}
	if rawURL, err = str("url"); err != nil {
		return
	}

	rawSize, ok := params["sizeBytes"]
	if !ok {
		err = fmt.Errorf("asset.fetch: params.sizeBytes is required")
		return
	}
	// encoding/json decodes a numeric literal into a params map (map[string]any)
	// as float64, never int64; a caller constructing params directly (as this
	// package's own tests do) may reasonably use either, so both are accepted.
	switch v := rawSize.(type) {
	case float64:
		sizeBytes = int64(v)
	case int64:
		sizeBytes = v
	case int:
		sizeBytes = int64(v)
	default:
		err = fmt.Errorf("asset.fetch: params.sizeBytes must be a number, got %T", rawSize)
		return
	}
	// Must be positive, not merely non-negative. A zero here would be
	// indistinguishable from an unset numeric field once params has been
	// through map[string]any, and treating it as "size not asserted" would
	// silently skip the transfer-length check.
	if sizeBytes < 1 {
		err = fmt.Errorf("asset.fetch: params.sizeBytes must be at least 1, got %d", sizeBytes)
		return
	}

	return
}

// run is the OperationFunc for "asset.fetch". See this file's package-level
// doc comments and the seam spec for the full behavioural contract; the
// short version is: validate scheme, stage-hash-verify-rename, never remove
// what is already on disk on failure, and never let a hash mismatch or an
// unreachable store stop the agent.
func (o assetFetchOperation) run(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	assetID, contentHash, filename, rawURL, sizeBytes, err := parseAssetFetchParams(params)
	if err != nil {
		return OperationResult{}, err
	}
	if err := validateAssetFilename(filename); err != nil {
		return OperationResult{}, err
	}

	// Scheme is checked before any request is made, per this seam's explicit
	// requirement — a "file://" or other local-resource scheme must never
	// reach net/http at all.
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return OperationResult{}, fmt.Errorf("asset.fetch: params.url %q is not a valid URL: %w", rawURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return OperationResult{}, fmt.Errorf("asset.fetch: params.url scheme %q must be http or https", parsed.Scheme)
	}

	stagingPath, downloadedHash, downloadedSize, downloadErr := downloadToStaging(ctx, o.dir, o.token, rawURL, contentHash)
	if downloadErr != nil {
		// The store being unreachable (or any other transfer failure) means
		// this node keeps whatever it already has and the next sync tick
		// tries again — no local file is ever removed on this path, and
		// nothing below executes.
		return OperationResult{
			Confirmed: false,
			Signal:    "node.asset.fetch_failed",
			Value:     assetID,
		}, fmt.Errorf("asset.fetch: download failed: %w", downloadErr)
	}

	appliedAt := now()

	// Both mismatches below are a DEFINITE negative: the node demonstrably
	// does not hold this asset. They return an error so the outcome is
	// "failed" rather than "unconfirmed", which in this project's vocabulary
	// means no evidence either way and would tell an operator we could not
	// tell about a file we just measured as wrong.
	if downloadedHash != contentHash {
		// A verification failure is evidence, never a way to stop the agent
		// (ADR-025's rule applied to content hashing): discard the staged
		// file, never rename unverified content into place.
		_ = os.Remove(stagingPath)
		return OperationResult{}, fmt.Errorf(
			"asset.fetch: asset %s failed verification and was discarded: want %s, got %s",
			assetID, contentHash, downloadedHash)
	}
	if downloadedSize != sizeBytes {
		_ = os.Remove(stagingPath)
		return OperationResult{}, fmt.Errorf(
			"asset.fetch: asset %s transferred %d bytes but %d were expected; the partial content was discarded",
			assetID, downloadedSize, sizeBytes)
	}

	// Verification succeeded: only now does the content become visible under
	// its runtime filename. finalPath was already validated as a safe,
	// separator-free bare filename joined under dir.
	finalPath := filepath.Join(o.dir, filename)
	if err := os.Rename(stagingPath, finalPath); err != nil {
		return OperationResult{}, fmt.Errorf("asset.fetch: renaming verified content into place: %w", err)
	}

	// Genuine post-write read-back: stat the file we just renamed and
	// re-hash it from disk, rather than trusting the in-memory hash computed
	// during download — see OperationResult's doc comment on why Confirmed
	// must rest on evidence collected after the write.
	confirmed, readBackSize := readBackAssetFunc(finalPath, contentHash)

	return OperationResult{
		Confirmed:  confirmed,
		Signal:     "node.asset.held",
		Value:      map[string]any{"assetId": assetID, "filename": filename, "contentHash": contentHash, "sizeBytes": readBackSize},
		ExecutedAt: appliedAt,
		ObservedAt: now(),
	}, nil
}

// readBackAsset re-opens path from disk and re-hashes it, reporting whether
// the on-disk content still matches wantHash and its actual size. This is a
// distinct, separately-coded step from the hash computed during download
// (see [assetFetchOperation.run]'s call site), matching this package's
// existing agent.echo read-back pattern in command.go.
func readBackAsset(path, wantHash string) (confirmed bool, size int64) {
	f, err := os.Open(path)
	if err != nil {
		return false, 0
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return false, 0
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	return got == wantHash, n
}

// downloadToStaging downloads rawURL into <dir>/.staging/<name>, hashing
// while writing, and returns the staging file's path, the resulting
// "sha256:<hex>" hash, and the byte count. name is derived from
// contentHash so a retry against the same content hash resumes the same
// staging file via Range rather than starting over.
func downloadToStaging(ctx context.Context, dir, token, rawURL, contentHash string) (path, hash string, size int64, err error) {
	stagingDir := filepath.Join(dir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", "", 0, fmt.Errorf("creating staging directory: %w", err)
	}

	name := stagingFileName(contentHash)
	stagingPath := filepath.Join(stagingDir, name)

	var resumeFrom int64
	if fi, statErr := os.Stat(stagingPath); statErr == nil {
		resumeFrom = fi.Size()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", 0, fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if resumeFrom > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeFrom, 10)+"-")
	}

	resp, err := assetHTTPClient.Do(req)
	if err != nil {
		return "", "", 0, fmt.Errorf("requesting %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	openFlags := os.O_CREATE | os.O_WRONLY
	writeOffset := int64(0)
	switch resp.StatusCode {
	case http.StatusOK:
		// The server did not honor the Range request (or there was nothing
		// to resume): start the staging file over from scratch.
		openFlags |= os.O_TRUNC
	case http.StatusPartialContent:
		openFlags |= os.O_APPEND
		writeOffset = resumeFrom
	default:
		return "", "", 0, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, rawURL)
	}

	f, err := os.OpenFile(stagingPath, openFlags, 0o644)
	if err != nil {
		return "", "", 0, fmt.Errorf("opening staging file: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if writeOffset > 0 {
		// Re-hash what is already on disk from a prior attempt so the final
		// hash covers the whole file, not just the resumed tail.
		existing, err := os.Open(stagingPath)
		if err != nil {
			return "", "", 0, fmt.Errorf("re-reading staged bytes for resume hash: %w", err)
		}
		if _, err := io.CopyN(h, existing, writeOffset); err != nil {
			_ = existing.Close()
			return "", "", 0, fmt.Errorf("re-reading staged bytes for resume hash: %w", err)
		}
		_ = existing.Close()
	}

	written, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("downloading body: %w", err)
	}

	return stagingPath, "sha256:" + hex.EncodeToString(h.Sum(nil)), writeOffset + written, nil
}

// stagingFileName derives a staging filename from contentHash, stripping
// the "sha256:" prefix so the resulting name is a plain hex string safe to
// use directly as a filesystem path component.
func stagingFileName(contentHash string) string {
	return strings.ReplaceAll(contentHash, ":", "-")
}

// heldAsset is one entry this node reports holding, matching
// mqttproto.AssetInventoryEntry field-for-field; kept as this package's own
// internal type (rather than importing the wire type directly into every
// internal call site) purely for readability at call sites that do not care
// about JSON tags.
type heldAsset struct {
	ContentHash string
	Filename    string
	SizeBytes   int64
	VerifiedAt  time.Time
}

// hashCacheEntry is what enumerateAssets caches per file so an unchanged
// file is not re-hashed on every inventory tick. verifiedAt is the time the
// hash was actually computed, carried forward on a cache hit so VerifiedAt
// never advances without a real verification behind it.
type hashCacheEntry struct {
	size       int64
	modTime    time.Time
	hash       string
	verifiedAt time.Time
}

// enumerateAssets walks dir (non-recursively: assets live flat under dir,
// with only the reserved ".staging" subdirectory excluded) and reports every
// regular file's content hash, size, and hash-computation time, using cache
// to skip re-hashing a file whose (size, modTime) has not changed since the
// last call. complete is false, with a reason, whenever the directory could
// not be read or any file's hash could not be computed — this function never
// returns complete=true off a partial walk; see this package's own
// assetinventory.go for why that has to be earned rather than assumed.
func enumerateAssets(dir string, cache map[string]hashCacheEntry, now func() time.Time) (assets []heldAsset, complete bool, reason string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, fmt.Sprintf("asset directory %q does not exist", dir)
		}
		return nil, false, fmt.Sprintf("could not read asset directory %q: %v", dir, err)
	}

	assets = make([]heldAsset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue // includes .staging, which never holds a verified asset
		}
		info, err := entry.Info()
		if err != nil {
			return nil, false, fmt.Sprintf("could not stat %q: %v", entry.Name(), err)
		}

		path := filepath.Join(dir, entry.Name())
		if cached, ok := cache[path]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
			// Cache hit: no hash was computed this tick, so VerifiedAt must
			// carry forward the time of the last real verification, not now.
			assets = append(assets, heldAsset{
				ContentHash: cached.hash,
				Filename:    entry.Name(),
				SizeBytes:   info.Size(),
				VerifiedAt:  cached.verifiedAt,
			})
			continue
		}

		hash, err := hashFile(path)
		if err != nil {
			// A cache miss that cannot be resolved sets complete=false
			// rather than silently omitting this file from the report.
			return nil, false, fmt.Sprintf("could not hash %q: %v", entry.Name(), err)
		}
		verifiedAt := now()
		cache[path] = hashCacheEntry{size: info.Size(), modTime: info.ModTime(), hash: hash, verifiedAt: verifiedAt}
		assets = append(assets, heldAsset{
			ContentHash: hash,
			Filename:    entry.Name(),
			SizeBytes:   info.Size(),
			VerifiedAt:  verifiedAt,
		})
	}

	return assets, true, ""
}

// hashFile computes the "sha256:<hex>" content hash of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// sweepAssetStaging removes every entry under <dir>/.staging at startup, per
// the volume backend's own "the staging directory is swept on startup"
// rule applied here to the agent's local staging area: a staging file left
// behind by a previous, interrupted process run is never a partially-usable
// asset and must never be picked up by a resume attempt against different
// content. Missing dir or missing .staging is not an error.
func sweepAssetStaging(dir string) error {
	stagingDir := filepath.Join(dir, ".staging")
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(stagingDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
