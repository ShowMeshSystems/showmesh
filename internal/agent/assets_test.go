package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file tests assets.go: filename safety, the asset.fetch operation
// end to end against an httptest server, downloadToStaging's Range resume,
// and enumerateAssets' completeness and caching rules — no real network
// involved, matching this package's established style.

func sha256Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestValidateAssetFilenameRejectsUnsafeNames proves a filename is rejected
// BEFORE it ever becomes a path: every case here would either escape dir or
// be a non-file, and none may reach os.Rename.
func TestValidateAssetFilenameRejectsUnsafeNames(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantErr  bool
	}{
		{name: "plain filename", filename: "Thriller.fseq", wantErr: false},
		{name: "empty", filename: "", wantErr: true},
		{name: "forward slash", filename: "a/b.fseq", wantErr: true},
		{name: "leading slash", filename: "/etc/passwd", wantErr: true},
		{name: "backslash", filename: `a\b.fseq`, wantErr: true},
		{name: "dot dot bare", filename: "..", wantErr: true},
		{name: "dot bare", filename: ".", wantErr: true},
		{name: "dot dot embedded via separator", filename: "../escape.fseq", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAssetFilename(tt.filename)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateAssetFilename(%q) error = %v, wantErr %v", tt.filename, err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrAssetFilenameUnsafe) {
				t.Fatalf("validateAssetFilename(%q) error = %v, want errors.Is(err, ErrAssetFilenameUnsafe)", tt.filename, err)
			}
		})
	}
}

// fetchParams builds a valid asset.fetch params map for content served at
// srv, so each test only has to override what it cares about.
func fetchParams(srv *httptest.Server, path, assetID, contentHash, filename string, sizeBytes int) map[string]any {
	return map[string]any{
		"assetId":     assetID,
		"contentHash": contentHash,
		"filename":    filename,
		"sizeBytes":   float64(sizeBytes), // matches how encoding/json decodes a number into map[string]any
		"url":         srv.URL + path,
	}
}

// TestAssetFetchOperationHappyPath proves the full stage-hash-verify-rename
// path: the file lands under dir with the requested filename, its bytes
// match what was served, no staging file is left behind, and Confirmed
// rests on a genuine post-write read-back (see readBackAsset).
func TestAssetFetchOperationHappyPath(t *testing.T) {
	content := []byte("this is a test show asset used to prove the happy path")
	hash := sha256Hash(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir}
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}

	params := fetchParams(srv, "/asset", "asset-1", hash, "Thriller.fseq", len(content))
	result, err := op.run(context.Background(), params, clock.now)
	if err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if !result.Confirmed {
		t.Fatalf("Confirmed = false, want true; result = %+v", result)
	}
	if result.Signal != "node.asset.held" {
		t.Fatalf("Signal = %q, want %q", result.Signal, "node.asset.held")
	}

	finalPath := filepath.Join(dir, "Thriller.fseq")
	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading final asset: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("final asset content = %q, want %q", got, content)
	}

	stagingEntries, err := os.ReadDir(filepath.Join(dir, ".staging"))
	if err != nil {
		t.Fatalf("reading staging dir: %v", err)
	}
	if len(stagingEntries) != 0 {
		t.Fatalf("staging dir has %d leftover entries, want 0 after a successful fetch", len(stagingEntries))
	}
}

// TestAssetFetchOperationHashMismatchDiscardsAndFails proves a content hash
// mismatch never renames the staged file into place, discards it, and
// reports a definite negative rather than "unconfirmed": the node
// demonstrably does not hold this asset, and unconfirmed means no evidence
// either way. The agent keeps running, which is the ADR-025 shape applied
// to content hashing.
func TestAssetFetchOperationHashMismatchDiscardsAndFails(t *testing.T) {
	content := []byte("actual served bytes")
	wrongHash := sha256Hash([]byte("this is not what gets served"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	params := fetchParams(srv, "/asset", "asset-1", wrongHash, "Thriller.fseq", len(content))
	_, err := op.run(context.Background(), params, clock.now)
	if err == nil {
		t.Fatal("run() error = nil, want an error: a hash mismatch is a definite negative and must not report unconfirmed")
	}
	// The reason an operator reads names both hashes, so a wrong upload is
	// distinguishable from a corrupted transfer.
	if !strings.Contains(err.Error(), wrongHash) || !strings.Contains(err.Error(), sha256Hash(content)) {
		t.Fatalf("error does not name both the wanted and the received hash: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "Thriller.fseq")); !os.IsNotExist(err) {
		t.Fatalf("final asset path exists after a hash mismatch, want it absent: err = %v", err)
	}
	stagingEntries, err := os.ReadDir(filepath.Join(dir, ".staging"))
	if err != nil {
		t.Fatalf("reading staging dir: %v", err)
	}
	if len(stagingEntries) != 0 {
		t.Fatalf("staging dir has %d leftover entries, want 0 (mismatch must discard the staged file)", len(stagingEntries))
	}
}

// TestAssetFetchOperationSizeMismatchDiscardsAndFails proves a declared
// sizeBytes that disagrees with what was actually downloaded (even though
// the content hash of those actual bytes matches) is also caught, discarded,
// and reported as a definite negative.
func TestAssetFetchOperationSizeMismatchDiscardsAndFails(t *testing.T) {
	content := []byte("bytes whose hash will match but declared size will not")
	hash := sha256Hash(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	params := fetchParams(srv, "/asset", "asset-1", hash, "Thriller.fseq", len(content)+100)
	_, err := op.run(context.Background(), params, clock.now)
	if err == nil {
		t.Fatal("run() error = nil, want an error: a size mismatch is a definite negative")
	}
	if _, err := os.Stat(filepath.Join(dir, "Thriller.fseq")); !os.IsNotExist(err) {
		t.Fatalf("final asset path exists after a size mismatch, want it absent")
	}
}

// A sizeBytes of zero is refused rather than read as "size not asserted".
// Absent and zero are indistinguishable once params has been through
// map[string]any, and treating zero as unset would silently skip the
// transfer-length check on every command that omitted the field.
func TestAssetFetchOperationRefusesZeroSizeBytes(t *testing.T) {
	op := assetFetchOperation{dir: t.TempDir()}
	clock := &fakeClock{t: time.Now()}
	params := map[string]any{
		"assetId":     "asset-1",
		"contentHash": sha256Hash([]byte("x")),
		"filename":    "Thriller.fseq",
		"sizeBytes":   0,
		"url":         "http://127.0.0.1:1/asset",
	}
	if _, err := op.run(context.Background(), params, clock.now); err == nil {
		t.Fatal("a zero sizeBytes was accepted, want a refusal naming the field")
	} else if !strings.Contains(err.Error(), "sizeBytes") {
		t.Fatalf("error does not name sizeBytes: %v", err)
	}
}

// TestAssetFetchOperationUnreachableStoreKeepsExistingFileAndFails proves
// this seam's central resilience rule: when the store cannot be reached,
// nothing already on disk is touched, and the operation reports failure
// (via a non-nil error, becoming OutcomeFailed) rather than silently
// succeeding or corrupting existing state.
func TestAssetFetchOperationUnreachableStoreKeepsExistingFileAndFails(t *testing.T) {
	dir := t.TempDir()

	// Pre-place a file with the SAME filename asset.fetch will target, with
	// content that must survive untouched.
	existingContent := []byte("this file must never be removed by a failed fetch")
	finalPath := filepath.Join(dir, "Thriller.fseq")
	if err := os.WriteFile(finalPath, existingContent, 0o644); err != nil {
		t.Fatalf("seeding existing asset: %v", err)
	}

	op := assetFetchOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	// Port 1 on loopback: nothing listens there, so this connection fails
	// immediately without depending on any external network state.
	params := fetchParams(&httptest.Server{URL: "http://127.0.0.1:1"}, "/asset", "asset-1",
		sha256Hash([]byte("irrelevant, never reached")), "Thriller.fseq", 10)

	result, err := op.run(context.Background(), params, clock.now)
	if err == nil {
		t.Fatalf("run() error = nil, want an error for an unreachable store")
	}
	if result.Confirmed {
		t.Fatalf("Confirmed = true, want false")
	}

	got, readErr := os.ReadFile(finalPath)
	if readErr != nil {
		t.Fatalf("existing asset was removed or became unreadable: %v", readErr)
	}
	if string(got) != string(existingContent) {
		t.Fatalf("existing asset content changed: got %q, want %q", got, existingContent)
	}
}

// TestAssetFetchOperationRejectsNonHTTPScheme proves the URL scheme is
// checked BEFORE any request is made: a handler that would fail the test if
// invoked is wired up on the same host the (rejected) scheme would
// otherwise have to reach through, and this test asserts it is never
// called.
func TestAssetFetchOperationRejectsNonHTTPScheme(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	// Same host:port as srv, but with a scheme http.Client cannot even
	// dial as HTTP — proves rejection happens at scheme-check time, not
	// merely "the request failed for some other reason".
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", srv.URL, err)
	}
	badURL := "ftp://" + u.Host + "/asset"

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir}
	clock := &fakeClock{t: time.Now()}

	params := fetchParams(&httptest.Server{URL: strings.TrimSuffix(badURL, "/asset")}, "/asset",
		"asset-1", "sha256:whatever", "Thriller.fseq", 10)

	_, err = op.run(context.Background(), params, clock.now)
	if err == nil {
		t.Fatalf("run() error = nil, want an error for a non-http(s) scheme")
	}
	if !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("error = %q, want it to mention the scheme", err.Error())
	}
	if called {
		t.Fatalf("the HTTP handler was invoked, want the scheme check to reject the URL before any request")
	}
}

// TestAssetFetchOperationSendsBearerTokenWhenSet and its "not set"
// counterpart prove SHOWMESH_AGENT_API_TOKEN is threaded through to the
// download request only when configured.
func TestAssetFetchOperationSendsBearerTokenWhenSet(t *testing.T) {
	content := []byte("token-gated content")
	hash := sha256Hash(content)
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir, token: "s3cret-token"}
	clock := &fakeClock{t: time.Now()}

	params := fetchParams(srv, "/asset", "asset-1", hash, "Thriller.fseq", len(content))
	if _, err := op.run(context.Background(), params, clock.now); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if gotAuth != "Bearer s3cret-token" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer s3cret-token")
	}
}

func TestAssetFetchOperationNoTokenSendsNoAuthHeader(t *testing.T) {
	content := []byte("no token needed")
	hash := sha256Hash(content)
	var gotAuth string
	sawRequest := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	op := assetFetchOperation{dir: dir} // no token
	clock := &fakeClock{t: time.Now()}

	params := fetchParams(srv, "/asset", "asset-1", hash, "Thriller.fseq", len(content))
	if _, err := op.run(context.Background(), params, clock.now); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !sawRequest {
		t.Fatalf("server never received a request")
	}
	if gotAuth != "" {
		t.Fatalf("Authorization header = %q, want empty when no token is configured", gotAuth)
	}
}

// TestDownloadToStagingResumesViaRange proves a staging file left over from
// a prior attempt (matched by content hash) is resumed with a Range
// request rather than re-downloaded from scratch, and that the final hash
// covers the WHOLE file (the pre-existing bytes plus the resumed tail), not
// just the newly-downloaded portion.
func TestDownloadToStagingResumesViaRange(t *testing.T) {
	full := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ-this-is-the-full-content-of-the-asset")
	wantHash := sha256Hash(full)
	splitAt := 20

	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		gotRange = rangeHeader
		if rangeHeader == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(full)
			return
		}
		// Parse "bytes=<offset>-"
		offsetStr := strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		offset, err := strconv.Atoi(offsetStr)
		if err != nil {
			t.Errorf("server: bad range header %q: %v", rangeHeader, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(full[offset:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	stagingDir := filepath.Join(dir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Pre-place a partial staging file matching this content hash's derived
	// name, simulating a prior interrupted attempt.
	stagingPath := filepath.Join(stagingDir, stagingFileName(wantHash))
	if err := os.WriteFile(stagingPath, full[:splitAt], 0o644); err != nil {
		t.Fatalf("seeding partial staging file: %v", err)
	}

	path, gotHash, gotSize, err := downloadToStaging(context.Background(), dir, "", srv.URL, wantHash)
	if err != nil {
		t.Fatalf("downloadToStaging() error = %v", err)
	}
	if gotRange == "" {
		t.Fatalf("server never received a Range header; resume did not happen")
	}
	if gotHash != wantHash {
		t.Fatalf("hash = %q, want %q (must cover the whole file, not just the resumed tail)", gotHash, wantHash)
	}
	if gotSize != int64(len(full)) {
		t.Fatalf("size = %d, want %d", gotSize, len(full))
	}
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading resumed staging file: %v", err)
	}
	if string(gotBytes) != string(full) {
		t.Fatalf("resumed staging file content = %q, want %q", gotBytes, full)
	}
}

// TestEnumerateAssetsMissingDirectoryIsIncomplete proves a nonexistent
// asset directory produces complete=false with a specific reason, never
// complete=true with an empty list — the coordinator must never read
// "the directory doesn't exist" as "this node holds nothing".
func TestEnumerateAssetsMissingDirectoryIsIncomplete(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	assets, complete, reason := enumerateAssets(missing, map[string]hashCacheEntry{}, time.Now)
	if complete {
		t.Fatalf("complete = true, want false for a missing directory")
	}
	if reason == "" {
		t.Fatalf("reason is empty, want a specific explanation")
	}
	if assets != nil {
		t.Fatalf("assets = %+v, want nil on an incomplete enumeration", assets)
	}
}

// TestEnumerateAssetsCompleteWithFiles proves a normal, fully-readable
// directory reports complete=true with every file's hash, filename, and
// size.
func TestEnumerateAssetsCompleteWithFiles(t *testing.T) {
	dir := t.TempDir()
	content := []byte("some show asset bytes")
	if err := os.WriteFile(filepath.Join(dir, "show.fseq"), content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// A .staging subdirectory must never contribute an entry, even if it
	// happens to contain files (a leftover from a crashed process, before
	// the next startup sweep runs).
	if err := os.MkdirAll(filepath.Join(dir, ".staging"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.staging): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".staging", "leftover"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("WriteFile(.staging/leftover): %v", err)
	}

	assets, complete, reason := enumerateAssets(dir, map[string]hashCacheEntry{}, time.Now)
	if !complete {
		t.Fatalf("complete = false (%q), want true", reason)
	}
	if len(assets) != 1 {
		t.Fatalf("len(assets) = %d, want 1 (the .staging entry must be excluded); assets = %+v", len(assets), assets)
	}
	if assets[0].Filename != "show.fseq" {
		t.Fatalf("Filename = %q, want %q", assets[0].Filename, "show.fseq")
	}
	if assets[0].ContentHash != sha256Hash(content) {
		t.Fatalf("ContentHash = %q, want %q", assets[0].ContentHash, sha256Hash(content))
	}
	if assets[0].SizeBytes != int64(len(content)) {
		t.Fatalf("SizeBytes = %d, want %d", assets[0].SizeBytes, len(content))
	}
}

// TestEnumerateAssetsCachesUnchangedFiles proves a file whose (size,
// modTime) is unchanged since the last call is NOT re-hashed: the file's
// bytes are corrupted in place without touching its size or modTime
// (os.Chtimes restores the original modTime after the write, which would
// otherwise bump it), and the cached, now-stale hash is still what comes
// back — the only way that can happen is if hashFile was never called a
// second time.
func TestEnumerateAssetsCachesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	original := []byte("original content, this hash gets cached")
	path := filepath.Join(dir, "show.fseq")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	originalModTime := info.ModTime()

	cache := map[string]hashCacheEntry{}
	first, complete, _ := enumerateAssets(dir, cache, time.Now)
	if !complete || len(first) != 1 {
		t.Fatalf("first enumerateAssets() = %+v, complete=%v, want 1 entry, complete=true", first, complete)
	}
	wantHash := first[0].ContentHash

	// Same length as `original` so Size() is unchanged; restore modTime
	// afterward so the cache key (size, modTime) looks identical to the
	// first call despite the content actually differing underneath it.
	corrupted := append([]byte(nil), original...)
	corrupted[0] ^= 0xFF // flip a byte; length is untouched by construction
	if err := os.WriteFile(path, corrupted, 0o644); err != nil {
		t.Fatalf("WriteFile(corrupted): %v", err)
	}
	if err := os.Chtimes(path, originalModTime, originalModTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	second, complete, _ := enumerateAssets(dir, cache, time.Now)
	if !complete || len(second) != 1 {
		t.Fatalf("second enumerateAssets() = %+v, complete=%v, want 1 entry, complete=true", second, complete)
	}
	if second[0].ContentHash != wantHash {
		t.Fatalf("ContentHash changed to %q (want the CACHED %q): the file was re-hashed despite an unchanged (size, modTime)", second[0].ContentHash, wantHash)
	}
}

// TestEnumerateAssetsRehashesOnChange is TestEnumerateAssetsCachesUnchangedFiles's
// counterpart: a real content AND size change must produce a different
// hash, proving the cache is keyed correctly rather than simply always
// returning the first hash it ever saw.
func TestEnumerateAssetsRehashesOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "show.fseq")
	if err := os.WriteFile(path, []byte("version one"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cache := map[string]hashCacheEntry{}
	first, _, _ := enumerateAssets(dir, cache, time.Now)
	firstHash := first[0].ContentHash

	if err := os.WriteFile(path, []byte("version two, deliberately a different length"), 0o644); err != nil {
		t.Fatalf("WriteFile(v2): %v", err)
	}

	second, _, _ := enumerateAssets(dir, cache, time.Now)
	if second[0].ContentHash == firstHash {
		t.Fatalf("ContentHash unchanged after a real content and size change, want it to differ")
	}
	if second[0].ContentHash != sha256Hash([]byte("version two, deliberately a different length")) {
		t.Fatalf("ContentHash = %q, want the freshly computed hash", second[0].ContentHash)
	}
}

// TestEnumerateAssetsHashFailureIsIncomplete proves a file that cannot be
// hashed (here: no read permission) sets complete=false with a reason,
// rather than silently omitting it from the report — a cache miss that
// cannot be resolved must never be treated as "this file does not exist".
func TestEnumerateAssetsHashFailureIsIncomplete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.fseq")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }() // let TempDir cleanup remove it

	_, complete, reason := enumerateAssets(dir, map[string]hashCacheEntry{}, time.Now)
	if complete {
		t.Fatalf("complete = true, want false when a file cannot be hashed")
	}
	if reason == "" {
		t.Fatalf("reason is empty, want an explanation naming the failure")
	}
}

// TestSweepAssetStagingRemovesLeftovers proves a startup sweep clears out
// anything left in .staging, and that a missing dir or missing .staging is
// not an error.
func TestSweepAssetStagingRemovesLeftovers(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, ".staging")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "leftover-1"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := sweepAssetStaging(dir); err != nil {
		t.Fatalf("sweepAssetStaging() error = %v", err)
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0 after sweep", len(entries))
	}
}

func TestSweepAssetStagingMissingDirIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := sweepAssetStaging(missing); err != nil {
		t.Fatalf("sweepAssetStaging(missing dir) error = %v, want nil", err)
	}
}
