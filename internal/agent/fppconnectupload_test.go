package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestHeldStore builds an fppConnectHeldStore rooted at a fresh
// t.TempDir(), backed by the real filesystem, returning both the store and
// the directory it is rooted at (so a test can seed files or inspect disk
// state directly).
func newTestHeldStore(t *testing.T) (*fppConnectHeldStore, string) {
	t.Helper()
	dir := t.TempDir()
	return newFPPConnectHeldStore(dir, discardLogger()), dir
}

// fakeENOSPCWriter always fails with syscall.ENOSPC, wrapped exactly the
// way a real os.File write on a full filesystem would be, so
// fppConnectIsDiskFull's errors.Is check exercises the same unwrap path a
// real ENOSPC would.
type fakeENOSPCWriter struct{}

func (fakeENOSPCWriter) WriteChunk(path string, offset int64, r io.Reader, n int64, truncate bool) (int64, error) {
	return 0, &os.PathError{Op: "write", Path: path, Err: syscall.ENOSPC}
}

// patchChunk sends one PATCH /api/file/{dir} chunk and returns the
// response (body already read and the response body closed).
func patchChunk(t *testing.T, srv *httptest.Server, dir, name string, offset, length int64, data []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/file/"+dir, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("building PATCH request: %v", err)
	}
	req.Header.Set("Upload-Name", name)
	req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	req.Header.Set("Upload-Length", strconv.FormatInt(length, 10))
	req.ContentLength = int64(len(data))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s/api/file/%s: %v", srv.URL, dir, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading PATCH response body: %v", err)
	}
	return resp, body
}

func postPlaylist(t *testing.T, srv *httptest.Server, name string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/playlist/"+name, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building POST request: %v", err)
	}
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s/api/playlist/%s: %v", srv.URL, name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading POST response body: %v", err)
	}
	return resp, respBody
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func findHeldRecord(t *testing.T, held *fppConnectHeldStore, dir, name string) (fppConnectHeldRecord, bool) {
	t.Helper()
	for _, rec := range held.Held() {
		if rec.Dir == dir && rec.Name == name {
			return rec, true
		}
	}
	return fppConnectHeldRecord{}, false
}

// hasEventKind reports whether held's evidence log contains an event of
// kind whose Name matches name ("" to match any name, used for events like
// "bad-dir" that carry no name).
func hasEventKind(held *fppConnectHeldStore, kind, name string) bool {
	for _, ev := range held.Events() {
		if ev.Kind != kind {
			continue
		}
		if name == "" || ev.Name == name {
			return true
		}
	}
	return false
}

// TestFPPConnectLogicalSequenceSlug is review round 1 finding 2's own
// regression test: the assets API's `sequence` field must satisfy
// config.ValidateShowObjectID's slug rule, not a raw file name stem.
func TestFPPConnectLogicalSequenceSlug(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Halloween Spooky.fseq", "halloween-spooky"},
		{"Test_File Name.fseq", "test-file-name"},
		{"ALLCAPS.fseq", "allcaps"},
		{"already-a-slug.fseq", "already-a-slug"},
		{"A--double--hyphen.fseq", "a-double-hyphen"},
		{"  leading and trailing  .fseq", "leading-and-trailing"},
		{"???.fseq", ""},
		{"no-extension", "no-extension"},
		{strings.Repeat("x", 100) + ".fseq", strings.Repeat("x", 64)},
		{strings.Repeat("x", 63) + "-.fseq", strings.Repeat("x", 63)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fppConnectLogicalSequenceSlug(tc.name)
			if got != tc.want {
				t.Fatalf("fppConnectLogicalSequenceSlug(%q) = %q, want %q", tc.name, got, tc.want)
			}
			if got != "" {
				if len(got) > 64 {
					t.Fatalf("slug %q exceeds 64 characters", got)
				}
				if got[0] == '-' || got[len(got)-1] == '-' {
					t.Fatalf("slug %q starts or ends with a hyphen", got)
				}
			}
		})
	}
}

// TestFPPConnectUploadThreeChunksCompletes is the seam's headline test: a
// full chunked upload lands one file in the held area whose bytes and
// SHA-256 match what was sent, and the record carries that hash.
func TestFPPConnectUploadThreeChunksCompletes(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	data := bytes.Repeat([]byte("A"), 10)
	data = append(data, bytes.Repeat([]byte("B"), 10)...)
	data = append(data, bytes.Repeat([]byte("C"), 10)...)
	total := int64(len(data))

	if resp, body := patchChunk(t, srv, "sequences", "Test.fseq", 0, total, data[0:10]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status = %d, body=%s", resp.StatusCode, body)
	}
	if resp, body := patchChunk(t, srv, "sequences", "Test.fseq", 10, total, data[10:20]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status = %d, body=%s", resp.StatusCode, body)
	}
	if resp, body := patchChunk(t, srv, "sequences", "Test.fseq", 20, total, data[20:30]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 3: status = %d, body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Test.fseq")
	if !ok {
		t.Fatal("no held record for sequences/Test.fseq")
	}
	if rec.SizeBytes != total {
		t.Fatalf("SizeBytes = %d, want %d", rec.SizeBytes, total)
	}
	wantHash := sha256Hex(data)
	if rec.ContentHash != wantHash {
		t.Fatalf("ContentHash = %q, want %q", rec.ContentHash, wantHash)
	}

	onDisk, err := os.ReadFile(held.heldFilePath("sequences", "Test.fseq"))
	if err != nil {
		t.Fatalf("reading held file: %v", err)
	}
	if !bytes.Equal(onDisk, data) {
		t.Fatalf("held file bytes do not match what was sent")
	}
}

// TestFPPConnectUploadInterruptedRegistersNothing covers ADR-044 decision
// 9: an upload that stops after chunk two leaves nothing in the held area,
// and a later upload of the same name starting at offset 0 succeeds
// cleanly (proving the stale fragment did not poison the retry).
func TestFPPConnectUploadInterruptedRegistersNothing(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	data := bytes.Repeat([]byte("X"), 30)

	if resp, body := patchChunk(t, srv, "sequences", "Partial.fseq", 0, 30, data[0:10]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status = %d, body=%s", resp.StatusCode, body)
	}
	if resp, body := patchChunk(t, srv, "sequences", "Partial.fseq", 10, 30, data[10:20]); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: status = %d, body=%s", resp.StatusCode, body)
	}
	// Never send chunk 3: the upload stops here.

	if _, ok := findHeldRecord(t, held, "sequences", "Partial.fseq"); ok {
		t.Fatal("a held record exists for an interrupted upload, want none")
	}
	if _, err := os.Stat(held.heldFilePath("sequences", "Partial.fseq")); !os.IsNotExist(err) {
		t.Fatalf("a held file exists for an interrupted upload, want none (stat err = %v)", err)
	}

	// A later upload of the same name at offset 0 discards the stale
	// fragment and succeeds cleanly.
	if resp, body := patchChunk(t, srv, "sequences", "Partial.fseq", 0, 30, data); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh single-chunk upload: status = %d, body=%s", resp.StatusCode, body)
	}
	rec, ok := findHeldRecord(t, held, "sequences", "Partial.fseq")
	if !ok {
		t.Fatal("no held record after the fresh upload")
	}
	if rec.SizeBytes != 30 {
		t.Fatalf("SizeBytes = %d, want 30", rec.SizeBytes)
	}
}

// TestFPPConnectUploadOffsetZeroDiscardsStaleFragment proves offset 0
// discards any stale fragment of the same name and starts over, even when
// the new attempt declares a different total length.
func TestFPPConnectUploadOffsetZeroDiscardsStaleFragment(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	if resp, body := patchChunk(t, srv, "sequences", "Restart.fseq", 0, 30, bytes.Repeat([]byte("Z"), 10)); resp.StatusCode != http.StatusOK {
		t.Fatalf("stale first attempt chunk: status = %d, body=%s", resp.StatusCode, body)
	}

	fresh := bytes.Repeat([]byte("Q"), 15)
	if resp, body := patchChunk(t, srv, "sequences", "Restart.fseq", 0, 15, fresh); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh restart at offset 0: status = %d, body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Restart.fseq")
	if !ok {
		t.Fatal("no held record after restart")
	}
	if rec.SizeBytes != 15 || rec.ContentHash != sha256Hex(fresh) {
		t.Fatalf("held record = %+v, want the fresh 15-byte content, not the stale fragment", rec)
	}
}

// TestFPPConnectUploadGapIsRefused proves a gap in offsets is refused
// (409) and the fragment is discarded.
func TestFPPConnectUploadGapIsRefused(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	if resp, body := patchChunk(t, srv, "sequences", "Gap.fseq", 0, 30, bytes.Repeat([]byte("A"), 10)); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status = %d, body=%s", resp.StatusCode, body)
	}
	// bytes received so far is 10; sending at offset 5 is a gap/overlap.
	resp, body := patchChunk(t, srv, "sequences", "Gap.fseq", 5, 30, bytes.Repeat([]byte("B"), 10))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", resp.StatusCode, body)
	}

	if _, err := os.Stat(held.stagingFilePath("sequences", "Gap.fseq")); !os.IsNotExist(err) {
		t.Fatalf("staging fragment survived a gap, stat err = %v", err)
	}
	if _, ok := findHeldRecord(t, held, "sequences", "Gap.fseq"); ok {
		t.Fatal("a held record exists after a gap, want none")
	}
	if !hasEventKind(held, "gap", "Gap.fseq") {
		t.Fatalf("no gap event recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectUploadDirectoryAllowlist proves "effects", "../sequences",
// and an Upload-Name containing "/" are all refused with nothing written
// (ADR-044 decision 4's first and second bounds).
func TestFPPConnectUploadDirectoryAllowlist(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	t.Run("effects directory refused", func(t *testing.T) {
		resp, body := patchChunk(t, srv, "effects", "Bad.eseq", 0, 3, []byte("abc"))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, body)
		}
		if _, ok := findHeldRecord(t, held, "effects", "Bad.eseq"); ok {
			t.Fatal("a held record exists for a refused directory")
		}
		if !hasEventKind(held, "bad-dir", "") {
			t.Fatalf("no bad-dir event recorded; events = %+v", held.Events())
		}
	})

	t.Run("traversal directory refused", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/file/../sequences", bytes.NewReader([]byte("abc")))
		if err != nil {
			t.Fatalf("building request: %v", err)
		}
		req.Header.Set("Upload-Name", "Bad.fseq")
		req.Header.Set("Upload-Offset", "0")
		req.Header.Set("Upload-Length", "3")
		req.ContentLength = 3
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("upload name containing a slash refused", func(t *testing.T) {
		resp, body := patchChunk(t, srv, "sequences", "sub/Escape.fseq", 0, 3, []byte("abc"))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, body)
		}
		if _, ok := findHeldRecord(t, held, "sequences", "sub/Escape.fseq"); ok {
			t.Fatal("a held record exists for a refused name")
		}
		if !hasEventKind(held, "bad-name", "sub/Escape.fseq") {
			t.Fatalf("no bad-name event recorded; events = %+v", held.Events())
		}
	})

	// "." is covered by TestFPPConnectUploadNameBoundCases below, along
	// with the empty and ".." cases review round 3 finding 6 asked be
	// turned into a table alongside it.
}

// TestFPPConnectBadDirEventDirIsBounded is review round 5 finding 2's
// regression test: a "bad-dir" refusal records dir, the raw {dir} URL
// segment, straight from route()'s decode, with no length limit of its
// own short of whatever the server accepts on a request line. Before this
// fix, fppConnectBoundEvent bounded Name and Reason but never Dir, so an
// oversized directory segment rode every subsequent render report at full
// length, on the one event field the round 4 fix missed.
func TestFPPConnectBadDirEventDirIsBounded(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	longDir := strings.Repeat("d", 4*fppConnectMaxEventStringBytes)
	resp, body := patchChunk(t, srv, longDir, "Bad.fseq", 0, 3, []byte("abc"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", resp.StatusCode, body)
	}

	var found bool
	for _, ev := range held.Events() {
		if ev.Kind != "bad-dir" {
			continue
		}
		found = true
		if got := len(ev.Dir); got > fppConnectMaxEventStringBytes {
			t.Fatalf("event Dir length = %d, want at most %d", got, fppConnectMaxEventStringBytes)
		}
		if !strings.HasSuffix(ev.Dir, fppConnectEventStringTruncatedSuffix) {
			t.Fatalf("event Dir = %q, want it to end with the truncation suffix", ev.Dir)
		}
	}
	if !found {
		t.Fatalf("no bad-dir event recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectUploadNameBoundCases is review round 3 finding 6's table:
// an empty, ".", or ".." Upload-Name must all take fppConnectValidPlaylistName's
// 403 bad-name path, with an event recorded, never the generic 400
// "headers required" path an empty name fell into before this fix (which
// also recorded no evidence at all).
func TestFPPConnectUploadNameBoundCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"exactly dot", "."},
		{"exactly dot-dot", ".."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			held, _ := newTestHeldStore(t)
			view := fakeFPPConnectView{enabled: true}
			srv := startFPPConnectTestServer(t, view, "node-1", held)

			req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/file/sequences", bytes.NewReader([]byte("abc")))
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Upload-Name", tc.in)
			req.Header.Set("Upload-Offset", "0")
			req.Header.Set("Upload-Length", "3")
			req.ContentLength = 3
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			if !hasEventKind(held, "bad-name", tc.in) {
				t.Fatalf("no bad-name event recorded for %q; events = %+v", tc.in, held.Events())
			}
			if _, ok := findHeldRecord(t, held, "sequences", tc.in); ok {
				t.Fatal("a held record exists for a refused name")
			}
		})
	}
}

// TestFPPConnectUploadPerFileCap proves Upload-Length over maxFileBytes is
// refused with a message naming that cap.
func TestFPPConnectUploadPerFileCap(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, maxFileBytes: 5}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	resp, body := patchChunk(t, srv, "sequences", "TooBig.fseq", 0, 100, bytes.Repeat([]byte("A"), 10))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "5") || !strings.Contains(string(body), "100") {
		t.Fatalf("body = %s, want it to name both the cap (5) and the declared length (100)", body)
	}
	if _, ok := findHeldRecord(t, held, "sequences", "TooBig.fseq"); ok {
		t.Fatal("a held record exists for a refused oversized upload")
	}
	if !hasEventKind(held, "too-large", "TooBig.fseq") {
		t.Fatalf("no too-large event recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectUploadAssetDirCap proves a declared total over
// maxAssetDirBytes is refused with a message naming that cap, seeding the
// directory with a file of known size first.
func TestFPPConnectUploadAssetDirCap(t *testing.T) {
	held, assetDir := newTestHeldStore(t)
	if err := os.WriteFile(filepath.Join(assetDir, "seed.bin"), bytes.Repeat([]byte("S"), 15), 0o644); err != nil {
		t.Fatalf("seeding asset dir: %v", err)
	}
	view := fakeFPPConnectView{enabled: true, maxAssetDirBytes: 20}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	resp, body := patchChunk(t, srv, "sequences", "TooMuch.fseq", 0, 10, bytes.Repeat([]byte("A"), 10))
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "20") {
		t.Fatalf("body = %s, want it to name the cap (20)", body)
	}
	if _, ok := findHeldRecord(t, held, "sequences", "TooMuch.fseq"); ok {
		t.Fatal("a held record exists for a refused over-cap upload")
	}
	if _, err := os.Stat(filepath.Join(assetDir, "seed.bin")); err != nil {
		t.Fatalf("the pre-existing seed file was removed: %v", err)
	}
	if !hasEventKind(held, "dir-full", "TooMuch.fseq") {
		t.Fatalf("no dir-full event recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectUploadDiskFull proves ENOSPC while writing is classified
// as disk full (507), distinct from a generic write failure, via an
// injected writer.
func TestFPPConnectUploadDiskFull(t *testing.T) {
	dir := t.TempDir()
	held := newFPPConnectHeldStoreWithWriter(dir, fakeENOSPCWriter{}, discardLogger())
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	resp, body := patchChunk(t, srv, "sequences", "Full.fseq", 0, 10, bytes.Repeat([]byte("A"), 10))
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(string(body)), "disk") {
		t.Fatalf("body = %s, want it to say the disk is full", body)
	}
	if _, ok := findHeldRecord(t, held, "sequences", "Full.fseq"); ok {
		t.Fatal("a held record exists after a disk-full write")
	}
	if !hasEventKind(held, "disk-full", "Full.fseq") {
		t.Fatalf("no disk-full event recorded; events = %+v", held.Events())
	}
}

// closeFailsFile wraps a real *os.File, forwarding every call except
// Close, which returns closeErr instead: the real file still actually
// closes (no fd leak), but the caller observes a failure exactly as it
// would from a real Close-time error (delayed-allocation ENOSPC, a
// network mount's write-back failure surfacing only at Close).
type closeFailsFile struct {
	*os.File
	closeErr error
}

func (f *closeFailsFile) Close() error {
	_ = f.File.Close()
	return f.closeErr
}

// TestOSFPPConnectChunkWriterReturnsCloseError is review round 7 finding
// 2's own regression test: osFPPConnectChunkWriter.WriteChunk used to
// discard f.Close()'s own error entirely, so a write failure surfacing
// only at Close (delayed allocation ENOSPC, a network mount) was reported
// as a successful chunk, and a held record ended up with a content hash
// for bytes that were never actually committed to disk. This calls the
// real osFPPConnectChunkWriter directly, injecting a Close failure via
// fppConnectOpenChunkFile, and proves the error comes back from WriteChunk
// itself, classifiable by fppConnectIsDiskFull exactly like a write-time
// ENOSPC already is.
func TestOSFPPConnectChunkWriterReturnsCloseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chunk.bin")

	orig := fppConnectOpenChunkFile
	t.Cleanup(func() { fppConnectOpenChunkFile = orig })
	fppConnectOpenChunkFile = func(path string, flags int, perm os.FileMode) (fppConnectChunkFile, error) {
		f, err := os.OpenFile(path, flags, perm)
		if err != nil {
			return nil, err
		}
		return &closeFailsFile{File: f, closeErr: &os.PathError{Op: "close", Path: path, Err: syscall.ENOSPC}}, nil
	}

	written, err := osFPPConnectChunkWriter{}.WriteChunk(path, 0, strings.NewReader("abc"), 3, true)
	if err == nil {
		t.Fatal("WriteChunk returned a nil error despite Close failing, want the Close error surfaced")
	}
	if !fppConnectIsDiskFull(err) {
		t.Fatalf("WriteChunk error = %v, want it classified as disk-full (fppConnectIsDiskFull), the same way a write-time ENOSPC already is", err)
	}
	if written != 3 {
		t.Fatalf("written = %d, want 3 (io.Copy's own count, independent of the later Close failure)", written)
	}

	// The real file descriptor was actually closed by closeFailsFile
	// despite the injected error, so a real filesystem never leaks an fd
	// here; the fix is about the RETURNED error, not about skipping the
	// real close.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("the file was not actually created/closed: %v", statErr)
	}
}

// TestFPPConnectUploadCloseFailureIsTreatedAsWriteFailure is review round
// 7 finding 2's own end-to-end regression test: a chunk whose write
// succeeds but whose Close fails must never complete as a held record
// with a hash for bytes that were never actually committed. Uses the real
// osFPPConnectChunkWriter (via fppConnectOpenChunkFile injection), not a
// fake fppConnectChunkWriter, so this exercises the real fix's own code
// path end to end through the HTTP upload route.
func TestFPPConnectUploadCloseFailureIsTreatedAsWriteFailure(t *testing.T) {
	dir := t.TempDir()

	orig := fppConnectOpenChunkFile
	t.Cleanup(func() { fppConnectOpenChunkFile = orig })
	fppConnectOpenChunkFile = func(path string, flags int, perm os.FileMode) (fppConnectChunkFile, error) {
		f, err := os.OpenFile(path, flags, perm)
		if err != nil {
			return nil, err
		}
		return &closeFailsFile{File: f, closeErr: &os.PathError{Op: "close", Path: path, Err: syscall.ENOSPC}}, nil
	}

	held := newFPPConnectHeldStoreWithWriter(dir, osFPPConnectChunkWriter{}, discardLogger())
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	resp, body := patchChunk(t, srv, "sequences", "CloseFails.fseq", 0, 3, []byte("abc"))
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body=%s", resp.StatusCode, body)
	}
	if _, ok := findHeldRecord(t, held, "sequences", "CloseFails.fseq"); ok {
		t.Fatal("a held record exists after a chunk whose Close failed: its hash would cover bytes never actually committed to disk")
	}
	if !hasEventKind(held, "disk-full", "CloseFails.fseq") {
		t.Fatalf("no disk-full event recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectPlaylistPostBindsIdempotently proves POST
// /api/playlist/{show} twice with the same body binds once, and GET then
// lists the bound entries.
func TestFPPConnectPlaylistPostBindsIdempotently(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{
		enabled: true, showNames: []string{"Halloween"},
		shows: []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}},
	}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	// A file lands first, with no active show, so it starts unbound.
	if resp, body := patchChunk(t, srv, "sequences", "Show.fseq", 0, 5, []byte("hello")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	postBody := []byte(`{"mainPlaylist":[{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"Show.fseq","duration":12.5}]}`)

	for i := 0; i < 2; i++ {
		resp, body := postPlaylist(t, srv, "Halloween", postBody)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST attempt %d: status = %d, body=%s", i, resp.StatusCode, body)
		}
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Show.fseq")
	if !ok {
		t.Fatal("no held record for sequences/Show.fseq")
	}
	if !rec.Bound || rec.Show != "Halloween" || rec.ShowID != "halloween-2026" || rec.LogicalSequence != "show" {
		t.Fatalf("record = %+v, want bound to Halloween (id halloween-2026) with logical sequence show", rec)
	}

	resp, body := getBody(t, srv.URL+"/api/playlist/Halloween")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", resp.StatusCode, body)
	}
	var got fppConnectPlaylistResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	if len(got.MainPlaylist) != 1 || got.MainPlaylist[0].SequenceName != "Show.fseq" {
		t.Fatalf("mainPlaylist = %+v, want exactly one entry naming Show.fseq", got.MainPlaylist)
	}
}

// TestFPPConnectPlaylistPostBeforeFileExists proves a name posted before
// the file exists is remembered as a pending binding, and a file
// completing afterwards binds on completion.
func TestFPPConnectPlaylistPostBeforeFileExists(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{
		enabled: true, showNames: []string{"Halloween"},
		shows: []fppConnectShowIDName{{ID: "halloween-2026", Name: "Halloween"}},
	}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	postBody := []byte(`{"mainPlaylist":[{"type":"sequence","enabled":1,"playOnce":0,"sequenceName":"Later.fseq","duration":0}]}`)
	if resp, body := postPlaylist(t, srv, "Halloween", postBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST: status = %d, body=%s", resp.StatusCode, body)
	}

	if resp, body := patchChunk(t, srv, "sequences", "Later.fseq", 0, 4, []byte("late")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Later.fseq")
	if !ok {
		t.Fatal("no held record for sequences/Later.fseq")
	}
	if !rec.Bound || rec.Show != "Halloween" || rec.ShowID != "halloween-2026" {
		t.Fatalf("record = %+v, want bound to Halloween (id halloween-2026) from the pending playlist post", rec)
	}
}

// TestFPPConnectUploadActiveShowFallback covers ADR-044 decision 8's
// active-show fallback and its three distinct "no active show" states:
// with no playlist posted, a completed file binds to the active show when
// one is known; when none is known, it is held unbound, and the record's
// reason distinguishes "never pushed" from "pushed null."
func TestFPPConnectUploadActiveShowFallback(t *testing.T) {
	t.Run("known active show binds on completion", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		view := fakeFPPConnectView{
			enabled: true, activeShowName: "Christmas", activeShowKnown: true, activeShowEver: true,
			shows: []fppConnectShowIDName{{ID: "christmas-2026", Name: "Christmas"}},
		}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "Auto.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "Auto.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if !rec.Bound || rec.Show != "Christmas" || rec.ShowID != "christmas-2026" || rec.LogicalSequence != "auto" {
			t.Fatalf("record = %+v, want bound to Christmas (id christmas-2026) with logical sequence auto", rec)
		}
	})

	t.Run("active show name does not resolve to exactly one show leaves it held unbound", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		// activeShowName names a show, but the pushed shows list carries no
		// (or more than one) entry for it: a stale edge case FC3 must treat
		// as unbound rather than bind with no resolvable show id.
		view := fakeFPPConnectView{enabled: true, activeShowName: "Christmas", activeShowKnown: true, activeShowEver: true}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "NoShowID.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "NoShowID.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if rec.Bound {
			t.Fatalf("record = %+v, want unbound: the active show name does not resolve to exactly one show id", rec)
		}
		if !strings.Contains(rec.UnboundReason, "does not currently resolve") {
			t.Fatalf("UnboundReason = %q, want it to name the unresolved show id", rec.UnboundReason)
		}
	})

	t.Run("never pushed leaves it held unbound", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		view := fakeFPPConnectView{enabled: true}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "Orphan.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "Orphan.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if rec.Bound {
			t.Fatalf("record = %+v, want unbound", rec)
		}
		if !strings.Contains(rec.UnboundReason, "never") {
			t.Fatalf("UnboundReason = %q, want it to say this node was never pushed an active show", rec.UnboundReason)
		}
	})

	t.Run("pushed null leaves it held unbound with a distinct reason", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		view := fakeFPPConnectView{enabled: true, activeShowEver: true, activeShowKnown: false}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "Nulled.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "Nulled.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if rec.Bound {
			t.Fatalf("record = %+v, want unbound", rec)
		}
		if !strings.Contains(rec.UnboundReason, "null") {
			t.Fatalf("UnboundReason = %q, want it to say the coordinator pushed null", rec.UnboundReason)
		}
	})

	// Review round 1 finding 8: known==true with an empty show name bound
	// to the empty show, a silent wrong guess ADR-044 decision 8
	// forbids. A known-but-empty active show must leave the file unbound
	// with its own distinct reason, never the never-pushed or pushed-null
	// reasons above.
	t.Run("known but empty active show name leaves it held unbound with a distinct reason", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		view := fakeFPPConnectView{enabled: true, activeShowName: "", activeShowKnown: true, activeShowEver: true}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "EmptyName.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "EmptyName.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if rec.Bound {
			t.Fatalf("record = %+v, want unbound: an active show known but pushed with an empty name must never bind", rec)
		}
		if rec.Show != "" {
			t.Fatalf("record.Show = %q, want empty", rec.Show)
		}
		if !strings.Contains(rec.UnboundReason, "empty") {
			t.Fatalf("UnboundReason = %q, want it to name the empty-name case", rec.UnboundReason)
		}
		if strings.Contains(rec.UnboundReason, "never") || strings.Contains(rec.UnboundReason, "null") {
			t.Fatalf("UnboundReason = %q, want a reason distinct from the never-pushed and pushed-null cases", rec.UnboundReason)
		}
	})
}

// TestFPPConnectPlaylistPostUnknownName proves posting an unknown playlist
// name returns 200, binds nothing, and leaves the evidence record.
func TestFPPConnectPlaylistPostUnknownName(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	postBody := []byte(`{"mainPlaylist":[{"sequenceName":"Ghost.fseq"}]}`)
	resp, body := postPlaylist(t, srv, "DoesNotExist", postBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	events := held.Events()
	var found bool
	for _, ev := range events {
		if ev.Kind == "unknown" && ev.Name == "DoesNotExist" {
			found = true
			if len(ev.Entries) != 1 || ev.Entries[0] != "Ghost.fseq" {
				t.Fatalf("event entries = %v, want [Ghost.fseq]", ev.Entries)
			}
		}
	}
	if !found {
		t.Fatalf("no unknown-playlist evidence recorded; events = %+v", events)
	}
}

// TestFPPConnectPlaylistPostAmbiguousName proves a name matching more than
// one show in ShowNames() (two shows sharing a display name) is
// unresolvable: nothing binds and the ambiguity is recorded as evidence.
func TestFPPConnectPlaylistPostAmbiguousName(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Shared", "Shared"}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	if resp, body := patchChunk(t, srv, "sequences", "Amb.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	postBody := []byte(`{"mainPlaylist":[{"sequenceName":"Amb.fseq"}]}`)
	resp, body := postPlaylist(t, srv, "Shared", postBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Amb.fseq")
	if !ok {
		t.Fatal("no held record")
	}
	if rec.Bound {
		t.Fatalf("record = %+v, want unbound (ambiguous show name)", rec)
	}

	var found bool
	for _, ev := range held.Events() {
		if ev.Kind == "ambiguous" && ev.Name == "Shared" && ev.MatchCount == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ambiguous-playlist evidence recorded; events = %+v", held.Events())
	}
}

// TestFPPConnectPlaylistPostShowIDNotPushedYet is review round 2 finding
// D's own regression test: a name that resolves to exactly one show by
// display name, but whose config object id has not been pushed yet
// (ShowNames has it, Shows does not), must record its own "show-id-not-
// pushed" evidence and its own distinct unbound reason, never the
// "ambiguous" event a genuine two-shows-share-a-name collision produces.
func TestFPPConnectPlaylistPostShowIDNotPushedYet(t *testing.T) {
	held, _ := newTestHeldStore(t)
	// showNames carries "Halloween" (an unambiguous match), but shows is
	// empty: this node's snapshot (or the coordinator push that reached
	// it) predates the additive shows id/name list.
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	if resp, body := patchChunk(t, srv, "sequences", "NoID.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	postBody := []byte(`{"mainPlaylist":[{"sequenceName":"NoID.fseq"}]}`)
	resp, body := postPlaylist(t, srv, "Halloween", postBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "NoID.fseq")
	if !ok {
		t.Fatal("no held record")
	}
	if rec.Bound {
		t.Fatalf("record = %+v, want unbound (show id not yet pushed)", rec)
	}
	if rec.UnboundReason != fppConnectUnboundReasonShowIDNotPushed {
		t.Fatalf("UnboundReason = %q, want %q", rec.UnboundReason, fppConnectUnboundReasonShowIDNotPushed)
	}
	if rec.Show != "Halloween" {
		t.Fatalf("Show = %q, want Halloween (remembered so a later push can rebind it)", rec.Show)
	}

	var found, sawAmbiguous bool
	for _, ev := range held.Events() {
		if ev.Kind == "show-id-not-pushed" && ev.Name == "Halloween" {
			found = true
		}
		if ev.Kind == "ambiguous" {
			sawAmbiguous = true
		}
	}
	if !found {
		t.Fatalf("no show-id-not-pushed evidence recorded; events = %+v", held.Events())
	}
	if sawAmbiguous {
		t.Fatalf("an ambiguous event was recorded, want show-id-not-pushed only; events = %+v", held.Events())
	}
}

// TestFPPConnectRebindPendingShowIDsOnLaterPush is review round 2 finding
// D's second regression test: once a later push resolves the show name a
// node already knew, every record held pending only for that reason binds
// automatically, whether it was already a completed held record, already
// a pending (not-yet-uploaded) binding, or still pending when the file
// finally completes after the rebind.
func TestFPPConnectRebindPendingShowIDsOnLaterPush(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	// "Existing.fseq" is already held when the playlist POST arrives;
	// "Later.fseq" completes afterward but before the rebind; "Pending.fseq"
	// is still a bare pending binding when the rebind itself runs.
	if resp, body := patchChunk(t, srv, "sequences", "Existing.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload Existing.fseq: status = %d, body=%s", resp.StatusCode, body)
	}
	postBody := []byte(`{"mainPlaylist":[
		{"sequenceName":"Existing.fseq"},
		{"sequenceName":"Later.fseq"},
		{"sequenceName":"Pending.fseq"}
	]}`)
	if resp, body := postPlaylist(t, srv, "Halloween", postBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST: status = %d, body=%s", resp.StatusCode, body)
	}
	if resp, body := patchChunk(t, srv, "sequences", "Later.fseq", 0, 4, []byte("late")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload Later.fseq: status = %d, body=%s", resp.StatusCode, body)
	}

	for _, name := range []string{"Existing.fseq", "Later.fseq"} {
		rec, ok := findHeldRecord(t, held, "sequences", name)
		if !ok || rec.Bound {
			t.Fatalf("%s: record = %+v (found=%v), want held and unbound before the rebind", name, rec, ok)
		}
	}

	// A later "fppconnect.configure" push resolves "Halloween" to an id
	// (agent.go wires this exact call to fppConnect.ShowID after every
	// applied push).
	held.RebindPendingShowIDs(func(name string) (string, bool) {
		if name == "Halloween" {
			return "halloween-2026", true
		}
		return "", false
	})

	for _, name := range []string{"Existing.fseq", "Later.fseq"} {
		rec, ok := findHeldRecord(t, held, "sequences", name)
		if !ok {
			t.Fatalf("%s: no held record after rebind", name)
		}
		if !rec.Bound || rec.Show != "Halloween" || rec.ShowID != "halloween-2026" {
			t.Fatalf("%s: record = %+v, want bound to Halloween (id halloween-2026)", name, rec)
		}
		if rec.UnboundReason != "" {
			t.Fatalf("%s: UnboundReason = %q, want empty after rebind", name, rec.UnboundReason)
		}
	}

	// Pending.fseq had no held record at rebind time; its pending entry's
	// ShowID should now be resolved, so completing it afterward binds
	// immediately with no further playlist POST needed.
	if resp, body := patchChunk(t, srv, "sequences", "Pending.fseq", 0, 7, []byte("pending")); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload Pending.fseq: status = %d, body=%s", resp.StatusCode, body)
	}
	rec, ok := findHeldRecord(t, held, "sequences", "Pending.fseq")
	if !ok || !rec.Bound || rec.ShowID != "halloween-2026" {
		t.Fatalf("Pending.fseq record = %+v (found=%v), want bound with id halloween-2026 after its pending entry was rebound", rec, ok)
	}
}

// TestFPPConnectBindPendingShowIDSkipsAlreadyBoundRecord is review round 3
// finding 5's own regression test: a record already bound with a
// non-empty ShowID must never be knocked back to unbound by
// BindPendingShowID, even when a later playlist POST names the identical
// file for a show whose id this node's current snapshot no longer (or not
// yet) resolves, e.g. a push that regressed shows back to empty.
func TestFPPConnectBindPendingShowIDSkipsAlreadyBoundRecord(t *testing.T) {
	held, _ := newTestHeldStore(t)

	if resp, body := (func() (*http.Response, []byte) {
		srv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", held)
		defer srv.Close()
		return patchChunk(t, srv, "sequences", "AlreadyBound.fseq", 0, 3, []byte("abc"))
	})(); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}

	held.BindShow("Halloween", "halloween-2026", []string{"AlreadyBound.fseq"}, time.Now())
	bound, ok := findHeldRecord(t, held, "sequences", "AlreadyBound.fseq")
	if !ok || !bound.Bound || bound.ShowID != "halloween-2026" {
		t.Fatalf("record after BindShow = %+v (found=%v), want bound to halloween-2026", bound, ok)
	}

	// A stale or regressed playlist POST names the same file for a show
	// whose id this node cannot currently resolve: the already-bound
	// record must be left exactly as it was.
	held.BindPendingShowID("Halloween", []string{"AlreadyBound.fseq"}, time.Now())

	after, ok := findHeldRecord(t, held, "sequences", "AlreadyBound.fseq")
	if !ok {
		t.Fatal("no held record after BindPendingShowID")
	}
	if after != bound {
		t.Fatalf("record after BindPendingShowID = %+v, want unchanged from %+v", after, bound)
	}
}

// TestFPPConnectBindShowToNewIdentityResetsRegistration is review round 5
// finding 1's own regression test: BindShow rebinding an already-registered
// record to a different show used to leave RegistrationState,
// RegistrationAssetID, and RegistrationReason exactly as they were, so a
// file registered under one show that a later playlist POST names into a
// different show kept reporting "registered" for the new show even though
// no asset exists there at all, and OnHeld's own terminal-state check
// (fppConnectRegistrationTerminal) would then treat the record as done and
// never even try to register it for real. BindShow must reset registration
// to unregistered ("") whenever the resolved ShowID changes on a record
// that already carries registration progress, keeping the superseded asset
// id in RegistrationReason as evidence.
func TestFPPConnectBindShowToNewIdentityResetsRegistration(t *testing.T) {
	held, _ := newTestHeldStore(t)

	srv := startFPPConnectTestServer(t, fakeFPPConnectView{enabled: true}, "node-1", held)
	if resp, body := patchChunk(t, srv, "sequences", "Rebound.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
		srv.Close()
		t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
	}
	srv.Close()

	held.BindShow("Halloween", "halloween-2026", []string{"Rebound.fseq"}, time.Now())
	bound, ok := findHeldRecord(t, held, "sequences", "Rebound.fseq")
	if !ok || !bound.Bound || bound.ShowID != "halloween-2026" {
		t.Fatalf("record after initial BindShow = %+v (found=%v), want bound to halloween-2026", bound, ok)
	}
	if !held.SetRegistrationRegistered("sequences", "Rebound.fseq", bound.ContentHash, bound.ShowID, bound.LogicalSequence, "asset-halloween", false) {
		t.Fatal("SetRegistrationRegistered was a no-op, want it to apply")
	}

	// A later playlist POST rebinds the SAME file to a DIFFERENT show
	// (xLights' own playlist naming changed, or an operator error): the
	// resolved ShowID changes even though the file itself did not.
	held.BindShow("Christmas", "christmas-2026", []string{"Rebound.fseq"}, time.Now())

	after, ok := findHeldRecord(t, held, "sequences", "Rebound.fseq")
	if !ok {
		t.Fatal("no held record after rebinding to a different show")
	}
	if after.ShowID != "christmas-2026" || after.Show != "Christmas" {
		t.Fatalf("record = %+v, want bound to Christmas/christmas-2026", after)
	}
	if after.RegistrationState != "" {
		t.Fatalf("RegistrationState = %q after a rebind to a new show, want empty so registration re-runs under the new identity", after.RegistrationState)
	}
	if after.RegistrationAssetID != "" {
		t.Fatalf("RegistrationAssetID = %q after a rebind to a new show, want cleared", after.RegistrationAssetID)
	}
	if after.RegistrationRolledBack {
		t.Fatal("RegistrationRolledBack = true after a rebind to a new show, want cleared")
	}
	if !strings.Contains(after.RegistrationReason, "asset-halloween") {
		t.Fatalf("RegistrationReason = %q, want it to keep the superseded asset id asset-halloween as evidence", after.RegistrationReason)
	}

	// Rebinding to the SAME show a second time must not disturb the reset
	// state further (BindShow's own idempotence, unaffected by this fix).
	held.BindShow("Christmas", "christmas-2026", []string{"Rebound.fseq"}, time.Now())
	still, ok := findHeldRecord(t, held, "sequences", "Rebound.fseq")
	if !ok || still.RegistrationState != "" || still.RegistrationReason != after.RegistrationReason {
		t.Fatalf("record after a same-show rebind = %+v (found=%v), want unchanged from %+v", still, ok, after)
	}
}

// TestFPPConnectUploadBootSweepKeepsHeld proves the boot sweep removes
// stale partials and keeps held files.
func TestFPPConnectUploadBootSweepKeepsHeld(t *testing.T) {
	dir := t.TempDir()

	stalePartial := filepath.Join(dir, fppConnectUploadStateSubdir, "staging", "sequences", "Stale.fseq.partial")
	if err := os.MkdirAll(filepath.Dir(stalePartial), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(stalePartial, []byte("stale"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	heldFile := filepath.Join(dir, fppConnectUploadStateSubdir, "held", "sequences", "Real.fseq")
	if err := os.MkdirAll(filepath.Dir(heldFile), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(heldFile, []byte("real"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := sweepFPPConnectUploadStaging(dir); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(stalePartial); !os.IsNotExist(err) {
		t.Fatalf("stale partial survived the sweep, stat err = %v", err)
	}
	if _, err := os.Stat(heldFile); err != nil {
		t.Fatalf("held file did not survive the sweep: %v", err)
	}
}

// TestFPPConnectLoadTrimsOversizedPendingAndEvents is review round 3
// finding 9's regression test: a persisted index.json that already
// exceeds fppConnectMaxPending or fppConnectMaxEvents (predating either
// cap, or edited outside this store's own writes) must be trimmed on
// load, the same as addPendingLocked/appendEventLocked enforce for every
// mutation afterward, oldest evicted first.
func TestFPPConnectLoadTrimsOversizedPendingAndEvents(t *testing.T) {
	dir := t.TempDir()

	total := fppConnectMaxPending + 10
	pending := make(map[string]fppConnectPendingBinding, total)
	pendingOrder := make([]string, 0, total)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("File%04d.fseq", i)
		pending[name] = fppConnectPendingBinding{ShowName: "SomeShow", ShowID: "some-show"}
		pendingOrder = append(pendingOrder, name)
	}

	events := make([]fppConnectEvent, 0, fppConnectMaxEvents+10)
	for i := 0; i < fppConnectMaxEvents+10; i++ {
		events = append(events, fppConnectEvent{Kind: "unknown", Name: fmt.Sprintf("Show%04d", i), At: time.Now()})
	}

	idx := fppConnectIndex{
		Records:      map[string]fppConnectHeldRecord{},
		Pending:      pending,
		PendingOrder: pendingOrder,
		Events:       events,
	}
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	indexDir := filepath.Join(dir, fppConnectUploadStateSubdir)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, fppConnectIndexFileName), data, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	held := newFPPConnectHeldStore(dir, discardLogger())

	held.mu.Lock()
	gotPending := len(held.pending)
	gotPendingOrder := len(held.pendingOrder)
	gotEvents := len(held.events)
	_, newestSurvived := held.pending[fmt.Sprintf("File%04d.fseq", total-1)]
	_, oldestSurvived := held.pending["File0000.fseq"]
	held.mu.Unlock()

	if gotPending != fppConnectMaxPending {
		t.Fatalf("len(pending) = %d, want %d", gotPending, fppConnectMaxPending)
	}
	if gotPendingOrder != fppConnectMaxPending {
		t.Fatalf("len(pendingOrder) = %d, want %d", gotPendingOrder, fppConnectMaxPending)
	}
	if !newestSurvived {
		t.Fatal("the newest pending entry did not survive load's trim")
	}
	if oldestSurvived {
		t.Fatal("the oldest pending entry survived load's trim, want it evicted")
	}
	if gotEvents != fppConnectMaxEvents {
		t.Fatalf("len(events) = %d, want %d", gotEvents, fppConnectMaxEvents)
	}
}

// TestFPPConnectLoadToleratesOldShapePendingValues is review round 2
// finding E's own regression test: an index.json written by the pre-FC3
// build has "pending" as map[string]string (a bare show display name, no
// id); this build's own Pending type is map[string]fppConnectPendingBinding.
// Decoding the whole document strictly against the current shape used to
// fail the ENTIRE unmarshal on that one field, which meant Records was
// discarded too and the node started reporting as if it held nothing,
// while the actual files stayed on disk, orphaned from this store's own
// memory of them. Loading the old shape must keep Records intact and
// convert the legacy pending entries to the current shape with an empty
// ShowID (the same "not resolved yet" state a fresh show-id-not-pushed
// pending entry has).
func TestFPPConnectLoadToleratesOldShapePendingValues(t *testing.T) {
	dir := t.TempDir()

	oldShapeIndex := `{
		"records": {
			"sequences/Kept.fseq": {
				"dir": "sequences",
				"name": "Kept.fseq",
				"sizeBytes": 3,
				"contentHash": "sha256:deadbeef",
				"receivedAt": "2026-08-01T00:00:00Z",
				"bound": true,
				"show": "Halloween",
				"showId": "halloween-2026",
				"logicalSequence": "kept"
			}
		},
		"pending": {
			"Legacy.fseq": "SomeOldShow"
		},
		"pendingOrder": ["Legacy.fseq"],
		"events": []
	}`

	indexDir := filepath.Join(dir, fppConnectUploadStateSubdir)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, fppConnectIndexFileName), []byte(oldShapeIndex), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	held := newFPPConnectHeldStore(dir, discardLogger())

	rec, ok := findHeldRecord(t, held, "sequences", "Kept.fseq")
	if !ok {
		t.Fatal("Records was discarded: no held record for sequences/Kept.fseq after loading an old-shape index")
	}
	if !rec.Bound || rec.ShowID != "halloween-2026" {
		t.Fatalf("record = %+v, want the pre-existing bound record intact", rec)
	}

	held.mu.Lock()
	binding, exists := held.pending["Legacy.fseq"]
	held.mu.Unlock()
	if !exists {
		t.Fatal("the legacy pending entry did not survive loading")
	}
	if binding.ShowName != "SomeOldShow" || binding.ShowID != "" {
		t.Fatalf("pending binding = %+v, want ShowName=SomeOldShow ShowID=\"\"", binding)
	}
}

// TestFPPConnectLoadRepairsPreFC3BoundRecordWithNoShowID is review round 6
// finding 2's own regression test: a held index a pre-FC3 build persisted
// predates ShowID and LogicalSequence entirely (ADR-028 decision 8), so a
// bound record it wrote decodes with both at their zero value, "". Left
// alone, attemptRegister would send an empty show field forever, a
// request the coordinator can only ever refuse terminally, with nothing
// left to retry it: RebindPendingShowIDs only ever walks a record whose
// UnboundReason already names it as awaiting an id, and nothing rebinds an
// already-bound record on its own. load must repair such a record into
// that exact awaiting-id shape instead: unbound,
// fppConnectUnboundReasonShowIDNotPushed, Show (a pre-FC3 field, already
// correct) kept, LogicalSequence recomputed from the file name.
func TestFPPConnectLoadRepairsPreFC3BoundRecordWithNoShowID(t *testing.T) {
	dir := t.TempDir()

	preFC3Index := `{
		"records": {
			"sequences/PreFC3.fseq": {
				"dir": "sequences",
				"name": "PreFC3.fseq",
				"sizeBytes": 3,
				"contentHash": "sha256:deadbeef",
				"receivedAt": "2026-08-01T00:00:00Z",
				"bound": true,
				"show": "Halloween"
			}
		},
		"pending": {},
		"pendingOrder": [],
		"events": []
	}`

	indexDir := filepath.Join(dir, fppConnectUploadStateSubdir)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, fppConnectIndexFileName), []byte(preFC3Index), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	held := newFPPConnectHeldStore(dir, discardLogger())

	rec, ok := findHeldRecord(t, held, "sequences", "PreFC3.fseq")
	if !ok {
		t.Fatal("no held record for sequences/PreFC3.fseq after loading a pre-FC3 index")
	}
	if rec.Bound {
		t.Fatalf("record = %+v, want unbound: a pre-FC3 record with no ShowID must never register with an empty show field", rec)
	}
	if rec.UnboundReason != fppConnectUnboundReasonShowIDNotPushed {
		t.Fatalf("UnboundReason = %q, want %q", rec.UnboundReason, fppConnectUnboundReasonShowIDNotPushed)
	}
	if rec.Show != "Halloween" {
		t.Fatalf("Show = %q, want Halloween preserved so a later push can rebind it", rec.Show)
	}
	if rec.LogicalSequence != "prefc3" {
		t.Fatalf("LogicalSequence = %q, want prefc3 recomputed from the file name", rec.LogicalSequence)
	}

	// A later push carrying the show's id converges it automatically, the
	// same as any other awaiting-id record.
	held.RebindPendingShowIDs(func(name string) (string, bool) {
		if name == "Halloween" {
			return "halloween-2026", true
		}
		return "", false
	})
	registered, ok := findHeldRecord(t, held, "sequences", "PreFC3.fseq")
	if !ok || !registered.Bound || registered.ShowID != "halloween-2026" {
		t.Fatalf("record after RebindPendingShowIDs = %+v (found=%v), want bound to halloween-2026", registered, ok)
	}
}

// TestFPPConnectUploadRoutesNoProductIdentityLeak extends FC1b's identity
// sweep (fppconnecthttp_test.go's TestFPPConnectNoProductIdentityLeak) to
// FC2's new routes.
func TestFPPConnectUploadRoutesNoProductIdentityLeak(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	forbidden := []string{"Falcon", "Player", "FPP"}
	check := func(label string, resp *http.Response, body []byte) {
		for _, bad := range forbidden {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s: body contains forbidden identity string %q: %s", label, bad, body)
			}
		}
		for name, values := range resp.Header {
			for _, bad := range forbidden {
				if strings.Contains(name, bad) {
					t.Errorf("%s: header name %q contains forbidden identity string %q", label, name, bad)
				}
				for _, v := range values {
					if strings.Contains(v, bad) {
						t.Errorf("%s: header %q value %q contains forbidden identity string %q", label, name, v, bad)
					}
				}
			}
		}
	}

	resp, body := patchChunk(t, srv, "sequences", "Identity.fseq", 0, 5, []byte("hello"))
	check("PATCH /api/file/sequences", resp, body)

	resp2, body2 := postPlaylist(t, srv, "Halloween", []byte(`{"mainPlaylist":[]}`))
	check("POST /api/playlist/Halloween", resp2, body2)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/file/sequences", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("building POST request: %v", err)
	}
	req.Header.Set("Upload-Name", "Identity.fseq")
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/file/sequences: %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	body3, _ := io.ReadAll(resp3.Body)
	check("POST /api/file/sequences (no-op)", resp3, body3)
}

// TestFPPConnectUploadConcurrentReservationsPreventDirCapOvercommit is
// review round 1 finding 5's regression test. Before this fix, the
// asset-directory cap was checked only against bytes already on disk at
// offset 0, so two uploads interleaved at the chunk level (A's first
// chunk lands, then B's offset-0 check runs before A finishes) could each
// individually pass that check and together exceed the cap: A's own
// still-undelivered remainder was invisible to B's check. WriteChunk is
// called directly, twice, with neither upload completing in between, to
// exercise exactly that interleaving deterministically (the store's own
// mutex would otherwise serialize two real concurrent HTTP requests
// anyway, making a goroutine-based test no more meaningful than this).
func TestFPPConnectUploadConcurrentReservationsPreventDirCapOvercommit(t *testing.T) {
	held, _ := newTestHeldStore(t)
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	const maxDir = int64(150)

	// Upload A declares 100 bytes total, but this call only delivers the
	// first 10: it is accepted and left in flight, with 90 bytes still
	// outstanding.
	outcome, reason, _ := held.WriteChunk("sequences", "A.fseq", 0, 100, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, maxDir, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkAccepted {
		t.Fatalf("upload A chunk 1: outcome = %v reason = %q, want accepted", outcome, reason)
	}

	// Upload B also declares 100 bytes, all in its first chunk. Bytes
	// actually on disk right now are only A's 10, which alone would fit
	// under 150 alongside B's 100 (110 total): the exact shape of the
	// bug. Correct behavior adds A's outstanding 90-byte remainder to the
	// check (10 + 90 + 100 = 200 > 150) and refuses B.
	outcome, reason, _ = held.WriteChunk("sequences", "B.fseq", 0, 100, strings.NewReader(strings.Repeat("B", 100)), 100, 1<<30, maxDir, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkDirFull {
		t.Fatalf("upload B: outcome = %v reason = %q, want dir-full (A's in-flight remainder must count against the cap)", outcome, reason)
	}
	if !strings.Contains(reason, "90") {
		t.Fatalf("reason = %q, want it to name the 90 bytes reserved by upload A", reason)
	}

	// Once A completes (freeing its reservation), B's own declared length
	// still exceeds what remains, so B stays refused for the same
	// fundamental reason; the point already proven above is that the
	// check considered A's reservation at all.
}

// TestFPPConnectPendingBindingsAreBoundedAndEvictOldestFirst is review
// round 1 finding 6's regression test: a single POST /api/playlist/{name}
// body can carry many sequenceName/mediaName entries with no held file to
// match yet, and every one becomes a pending binding. Without a cap that
// map (and the index.json it is persisted in) grows without bound.
func TestFPPConnectPendingBindingsAreBoundedAndEvictOldestFirst(t *testing.T) {
	held, _ := newTestHeldStore(t)

	total := fppConnectMaxPending + 10
	names := make([]string, total)
	for i := range names {
		names[i] = fmt.Sprintf("File%04d.fseq", i)
	}
	held.BindShow("SomeShow", "some-show", names, time.Now())

	held.mu.Lock()
	gotLen := len(held.pending)
	gotOrderLen := len(held.pendingOrder)
	_, oldestStillPending := held.pending[names[0]]
	_, newestStillPending := held.pending[names[total-1]]
	held.mu.Unlock()

	if gotLen != fppConnectMaxPending {
		t.Fatalf("len(pending) = %d, want %d", gotLen, fppConnectMaxPending)
	}
	if gotOrderLen != fppConnectMaxPending {
		t.Fatalf("len(pendingOrder) = %d, want %d", gotOrderLen, fppConnectMaxPending)
	}
	if oldestStillPending {
		t.Fatalf("the oldest pending entry %q survived eviction, want the oldest evicted first", names[0])
	}
	if !newestStillPending {
		t.Fatalf("the newest pending entry %q was evicted, want it kept", names[total-1])
	}
}

// TestFPPConnectPendingBindingKeyIsBoundedAndStillMatchesALaterUpload is
// review round 8 finding 1's own regression test: a pending binding's key
// comes straight from a playlist POST body's file name list, with no
// length bound of its own, unlike every other string this store persists
// (fppConnectBoundEventString's 256-byte cap). A single POST could name a
// file whose stem alone runs into the hundreds of kilobytes, and up to
// fppConnectMaxPending of those, each that large, would all land in
// index.json verbatim. addPendingLocked, deletePendingLocked, and
// completeLocked's own lookup all now apply the identical bound, so a
// later completion of the SAME (unbounded) name still finds and consumes
// the pending entry, exactly as any shorter name already does.
// completeLocked is called directly here (bypassing the real chunked
// upload path entirely): a name this long could never survive a real
// filesystem write, whose own path-component length limit is far below
// even fppConnectMaxHeaderBytes, let alone this test's 900 KiB.
func TestFPPConnectPendingBindingKeyIsBoundedAndStillMatchesALaterUpload(t *testing.T) {
	held, _ := newTestHeldStore(t)

	longName := strings.Repeat("N", 900*1024) + ".fseq"

	held.BindShow("Halloween", "halloween-2026", []string{longName}, time.Now())

	held.mu.Lock()
	if got := len(held.pending); got != 1 {
		held.mu.Unlock()
		t.Fatalf("pending entries = %d, want exactly 1", got)
	}
	var pendingKey string
	for k := range held.pending {
		pendingKey = k
	}
	held.mu.Unlock()
	if len(pendingKey) > fppConnectMaxEventStringBytes {
		t.Fatalf("pending key length = %d, want at most %d", len(pendingKey), fppConnectMaxEventStringBytes)
	}
	if !strings.HasSuffix(pendingKey, fppConnectEventStringTruncatedSuffix) {
		t.Fatalf("pending key = %q, want it to show truncation", pendingKey)
	}

	data, err := os.ReadFile(held.indexPath())
	if err != nil {
		t.Fatalf("reading persisted index: %v", err)
	}
	if len(data) > 4096 {
		t.Fatalf("persisted index length = %d bytes, want it small: the pending key must be bounded on disk, not just in memory", len(data))
	}

	held.mu.Lock()
	rec := held.completeLocked("sequences", longName, 3, "sha256:deadbeef", time.Now(),
		func() (string, bool, bool) { return "", false, false },
		func(string) (string, bool) { return "", false },
		func() []string { return nil })
	_, stillPending := held.pending[pendingKey]
	held.mu.Unlock()

	if !rec.Bound || rec.ShowID != "halloween-2026" {
		t.Fatalf("record = %+v, want bound to halloween-2026: the pending binding must still match a later completion of the identical name", rec)
	}
	if stillPending {
		t.Fatal("the pending entry survived a matching completion, want it consumed")
	}
}

// TestFPPConnectLoadBoundsOversizedPendingKeys is review round 8 finding
// 1's own load()-repair regression test: a persisted index written before
// addPendingLocked bounded its own key (or one edited or corrupted
// outside this store's own writes) can hold a pending key far longer than
// fppConnectMaxEventStringBytes. load must repair it into the identical
// bounded form addPendingLocked would already produce today, so a later
// completion of that same file still matches it.
func TestFPPConnectLoadBoundsOversizedPendingKeys(t *testing.T) {
	dir := t.TempDir()

	longName := strings.Repeat("Q", 900*1024) + ".fseq"
	idx := fppConnectIndexOnDisk{
		Records: map[string]fppConnectHeldRecord{},
		Pending: mustMarshalJSON(t, map[string]fppConnectPendingBinding{
			longName: {ShowName: "Halloween", ShowID: "halloween-2026"},
		}),
		PendingOrder: []string{longName},
		Events:       []fppConnectEvent{},
	}
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatalf("setup: encoding the oversized-key index: %v", err)
	}

	indexDir := filepath.Join(dir, fppConnectUploadStateSubdir)
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(indexDir, fppConnectIndexFileName), data, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	held := newFPPConnectHeldStore(dir, discardLogger())

	held.mu.Lock()
	if got := len(held.pending); got != 1 {
		held.mu.Unlock()
		t.Fatalf("pending entries after load = %d, want exactly 1", got)
	}
	var pendingKey string
	for k := range held.pending {
		pendingKey = k
	}
	orderMatches := len(held.pendingOrder) == 1 && held.pendingOrder[0] == pendingKey
	held.mu.Unlock()

	if len(pendingKey) > fppConnectMaxEventStringBytes {
		t.Fatalf("pending key length after load = %d, want at most %d", len(pendingKey), fppConnectMaxEventStringBytes)
	}
	if !orderMatches {
		t.Fatal("pendingOrder was not rebuilt to match the bounded pending key")
	}

	held.mu.Lock()
	rec := held.completeLocked("sequences", longName, 3, "sha256:deadbeef", time.Now(),
		func() (string, bool, bool) { return "", false, false },
		func(string) (string, bool) { return "", false },
		func() []string { return nil })
	held.mu.Unlock()
	if !rec.Bound || rec.ShowID != "halloween-2026" {
		t.Fatalf("record = %+v, want bound to halloween-2026 after loading an oversized pending key", rec)
	}
}

// mustMarshalJSON is a small json.RawMessage helper for tests that build
// an fppConnectIndexOnDisk directly, matching fppConnectIndexOnDisk's own
// Pending field's raw-decode contract.
func mustMarshalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding JSON: %v", err)
	}
	return data
}

// TestFPPConnectChunkWriterTruncatesStaleTailOnOffsetZero is review round
// 1 finding 7's regression test, exercised directly against
// osFPPConnectChunkWriter rather than through discardFragment's best-
// effort os.Remove (whose own error is deliberately ignored, so a test
// relying on it succeeding would not prove the fix): a longer stale file
// already at path, then a shorter offset-0, truncate=true write, must
// leave the file at exactly the new, shorter length, never the old
// longer one.
func TestFPPConnectChunkWriterTruncatesStaleTailOnOffsetZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.partial")
	stale := bytes.Repeat([]byte("L"), 50)
	if err := os.WriteFile(path, stale, 0o644); err != nil {
		t.Fatalf("setup: writing the stale partial: %v", err)
	}

	w := osFPPConnectChunkWriter{}
	fresh := bytes.Repeat([]byte("S"), 10)
	written, err := w.WriteChunk(path, 0, bytes.NewReader(fresh), int64(len(fresh)), true)
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	if written != int64(len(fresh)) {
		t.Fatalf("written = %d, want %d", written, len(fresh))
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the file back: %v", err)
	}
	if len(got) != len(fresh) || !bytes.Equal(got, fresh) {
		t.Fatalf("file is %d bytes (%q), want exactly %d bytes (%q): a stale tail from the earlier 50-byte file survived", len(got), got, len(fresh), fresh)
	}
}

// blockingChunkWriter blocks inside WriteChunk until release is closed,
// signaling started first so a test can synchronize on "the copy has
// actually begun." Used to prove the store's mutex is released during the
// copy (review round 3 finding 3).
type blockingChunkWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w *blockingChunkWriter) WriteChunk(path string, offset int64, r io.Reader, n int64, truncate bool) (int64, error) {
	close(w.started)
	<-w.release
	return osFPPConnectChunkWriter{}.WriteChunk(path, offset, r, n, truncate)
}

// TestFPPConnectWriteChunkReleasesLockDuringCopy is review round 3
// finding 3's regression test: before this fix, WriteChunk held the
// store's mutex across the whole network read, so a slow client blocked
// Held() and Events() (and so render report publication) for up to the
// file read deadline. Held()/Events() must complete promptly while a
// chunk copy is still blocked mid-write.
func TestFPPConnectWriteChunkReleasesLockDuringCopy(t *testing.T) {
	dir := t.TempDir()
	w := &blockingChunkWriter{started: make(chan struct{}), release: make(chan struct{})}
	held := newFPPConnectHeldStoreWithWriter(dir, w, discardLogger())
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	done := make(chan struct{})
	go func() {
		defer close(done)
		held.WriteChunk("sequences", "Slow.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	}()

	select {
	case <-w.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the chunk copy to start")
	}

	snapshotDone := make(chan struct{})
	go func() {
		held.Held()
		held.Events()
		close(snapshotDone)
	}()
	select {
	case <-snapshotDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Held()/Events() blocked while a chunk copy was still in flight; the store's mutex was not released during the copy")
	}

	close(w.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for WriteChunk to return after release")
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Slow.fseq")
	if !ok || rec.SizeBytes != 5 {
		t.Fatalf("record = %+v, ok=%v, want a completed 5-byte record", rec, ok)
	}
}

// TestFPPConnectWriteChunkSweepsIdleInFlightReservations is review round 3
// finding 4's regression test: an abandoned upload's asset-directory-cap
// reservation must not survive past fppConnectInFlightTTL, using a fake
// clock (the at parameter WriteChunk already takes) rather than a real
// sleep.
func TestFPPConnectWriteChunkSweepsIdleInFlightReservations(t *testing.T) {
	held, _ := newTestHeldStore(t)
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	start := time.Now()
	outcome, reason, _ := held.WriteChunk("sequences", "Abandoned.fseq", 0, 100, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, 1<<30, start, neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkAccepted {
		t.Fatalf("setup: outcome = %v reason = %q, want accepted", outcome, reason)
	}

	held.mu.Lock()
	if _, ok := held.inFlight["sequences/Abandoned.fseq"]; !ok {
		held.mu.Unlock()
		t.Fatal("setup: no in-flight entry recorded")
	}
	held.mu.Unlock()

	// A later, unrelated upload's own call to WriteChunk is what triggers
	// the sweep (sweepIdleInFlightLocked runs at the top of
	// prepareChunkLocked); the abandoned entry is long past the TTL by
	// the time this one arrives.
	later := start.Add(fppConnectInFlightTTL + time.Minute)
	if outcome, reason, _ := held.WriteChunk("sequences", "Fresh.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, later, neverActive, neverResolveShowID, neverShowNames); outcome != fppConnectChunkCompleted {
		t.Fatalf("fresh upload: outcome = %v reason = %q, want completed", outcome, reason)
	}

	held.mu.Lock()
	_, stillInFlight := held.inFlight["sequences/Abandoned.fseq"]
	held.mu.Unlock()
	if stillInFlight {
		t.Fatal("abandoned in-flight entry survived past its TTL, want it swept")
	}
	if _, err := os.Stat(held.stagingFilePath("sequences", "Abandoned.fseq")); !os.IsNotExist(err) {
		t.Fatalf("abandoned staging file survived the sweep, stat err = %v", err)
	}
}

// TestFPPConnectUploadLengthMustMatchOffsetZeroDeclaration is review round
// 3 finding 5's regression test for the reachable half of the fix: a
// later chunk declaring a different Upload-Length than offset 0's is
// refused (409) with a length-mismatch event, rather than silently
// trusted and left to corrupt the asset-directory-cap reservation.
func TestFPPConnectUploadLengthMustMatchOffsetZeroDeclaration(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	if resp, body := patchChunk(t, srv, "sequences", "Mismatch.fseq", 0, 100, bytes.Repeat([]byte("A"), 10)); resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 1: status = %d, body=%s", resp.StatusCode, body)
	}

	resp, body := patchChunk(t, srv, "sequences", "Mismatch.fseq", 10, 200, bytes.Repeat([]byte("B"), 10))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", resp.StatusCode, body)
	}
	if !hasEventKind(held, "length-mismatch", "Mismatch.fseq") {
		t.Fatalf("no length-mismatch event recorded; events = %+v", held.Events())
	}
	if _, ok := findHeldRecord(t, held, "sequences", "Mismatch.fseq"); ok {
		t.Fatal("a held record exists after a length mismatch, want none")
	}
	if _, err := os.Stat(held.stagingFilePath("sequences", "Mismatch.fseq")); !os.IsNotExist(err) {
		t.Fatalf("the fragment survived a length mismatch, stat err = %v", err)
	}
}

// TestFPPConnectReservationClampsAtZero is review round 3 finding 5's
// regression test for the defense-in-depth half of the fix: even if an
// in-flight entry's bytesReceived somehow exceeds its own uploadLength
// (the exact state the length-mismatch check above now prevents from
// arising through the public API), its contribution to another upload's
// asset-directory-cap check must clamp at zero rather than going
// negative and manufacturing headroom that is not really there.
func TestFPPConnectReservationClampsAtZero(t *testing.T) {
	held, _ := newTestHeldStore(t)
	held.mu.Lock()
	held.inFlight["sequences/Corrupt.fseq"] = &fppConnectInFlight{
		hash: sha256.New(), uploadLength: 10, bytesReceived: 50, lastChunkAt: time.Now(),
	}
	held.mu.Unlock()
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	// maxAssetDirBytes is 50. Corrupt.fseq's raw remainder is
	// 10-50 = -40. A fresh 51-byte upload: with the remainder wrongly
	// left negative, 0 (nothing really on disk) + (-40) + 51 = 11 <= 50
	// would be wrongly accepted. Clamped at zero, 0 + 0 + 51 = 51 > 50
	// must be refused.
	outcome, reason, _ := held.WriteChunk("sequences", "New.fseq", 0, 51, strings.NewReader(strings.Repeat("N", 10)), 10, 1<<30, 50, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkDirFull {
		t.Fatalf("outcome = %v reason = %q, want dir-full (a negative reservation must clamp to zero, not manufacture headroom)", outcome, reason)
	}
}

// TestFPPConnectInFlightReservationClearsOnCompletionAndDiscard is review
// round 1 finding 5's missing assertions (round 3 finding 7): the
// in-flight reservation must be gone, not merely inert, both after a
// clean completion and after a discarded (gapped) upload.
func TestFPPConnectInFlightReservationClearsOnCompletionAndDiscard(t *testing.T) {
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	t.Run("completion clears the reservation", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		outcome, reason, _ := held.WriteChunk("sequences", "Done.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
		if outcome != fppConnectChunkCompleted {
			t.Fatalf("outcome = %v reason = %q, want completed", outcome, reason)
		}
		held.mu.Lock()
		_, stillInFlight := held.inFlight["sequences/Done.fseq"]
		held.mu.Unlock()
		if stillInFlight {
			t.Fatal("in-flight reservation survived completion, want it cleared")
		}
	})

	t.Run("a gap discards the reservation", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		if outcome, _, _ := held.WriteChunk("sequences", "Gapped.fseq", 0, 30, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames); outcome != fppConnectChunkAccepted {
			t.Fatal("setup: chunk 1 not accepted")
		}
		if outcome, _, _ := held.WriteChunk("sequences", "Gapped.fseq", 5, 30, strings.NewReader(strings.Repeat("B", 10)), 10, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames); outcome != fppConnectChunkGap {
			t.Fatal("setup: expected a gap")
		}
		held.mu.Lock()
		_, stillInFlight := held.inFlight["sequences/Gapped.fseq"]
		held.mu.Unlock()
		if stillInFlight {
			t.Fatal("in-flight reservation survived a discard, want it cleared")
		}
	})
}

// panicOnceChunkWriter panics on its first call whose offset matches
// panicOffset, then delegates to inner for every call after that (and for
// every call at a different offset before that): a way of simulating a
// panic mid-copy (net/http recovers one per connection in production)
// without leaving every subsequent chunk on the same key unable to ever
// complete. panicOffset 0 (its zero value) matches
// TestFPPConnectWriteChunkClearsWritingFlagOnPanic's single offset-0
// chunk; TestFPPConnectWriteChunkDiscardsFragmentOnPanicMidUpload sets it
// to a later chunk's nonzero offset instead, so the FIRST chunk of a
// multi-chunk upload completes normally and the panic lands mid-upload.
type panicOnceChunkWriter struct {
	panicked    bool
	panicOffset int64
	inner       fppConnectChunkWriter
}

func (w *panicOnceChunkWriter) WriteChunk(path string, offset int64, r io.Reader, n int64, truncate bool) (int64, error) {
	if !w.panicked && offset == w.panicOffset {
		w.panicked = true
		panic("simulated panic mid chunk copy")
	}
	return w.inner.WriteChunk(path, offset, r, n, truncate)
}

// TestFPPConnectWriteChunkClearsWritingFlagOnPanic is review round 4
// finding 5's regression test: before this fix, a panic during the
// unlocked chunk copy (recovered per connection by net/http in
// production, so the process itself survives) left the in-flight entry's
// writing flag stuck true forever, since finishChunkLocked, the only
// place that ever cleared it, was never reached. Every later request for
// the same key, including a fresh offset-0 retry, would then be refused
// as "already in progress" with no way back short of a process restart.
func TestFPPConnectWriteChunkClearsWritingFlagOnPanic(t *testing.T) {
	dir := t.TempDir()
	writer := &panicOnceChunkWriter{inner: osFPPConnectChunkWriter{}}
	held := newFPPConnectHeldStoreWithWriter(dir, writer, discardLogger())
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("WriteChunk did not panic on the first call; test setup is broken")
			}
		}()
		held.WriteChunk("sequences", "Panicky.fseq", 0, 3, strings.NewReader("abc"), 3, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	}()

	// The deferred reset must have cleared writing before the panic
	// propagated, so this retry at the same offset is treated as an
	// ordinary retry, never refused as "another request ... already in
	// progress."
	outcome, reason, rec := held.WriteChunk("sequences", "Panicky.fseq", 0, 3, strings.NewReader("abc"), 3, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkCompleted {
		t.Fatalf("outcome = %v reason = %q, want completed (a retry after a panic must succeed normally, not stay refused as still in progress)", outcome, reason)
	}
	if rec.SizeBytes != 3 {
		t.Fatalf("rec.SizeBytes = %d, want 3", rec.SizeBytes)
	}
}

// TestFPPConnectWriteChunkDiscardsFragmentOnPanicMidUpload is review round
// 5 finding 1's regression test: a panic mid-copy is recovered only after
// TeeReader has already fed whatever bytes the writer read into the
// in-flight entry's running hash, but before finishChunkLocked ever runs
// to advance bytesReceived to match. Merely resetting writing back to
// false (round 4's fix) left that hash/offset mismatch in place, so a
// retry at the offset the client still believed was correct would be
// accepted as an ordinary continuation, feed the same bytes into the
// already-poisoned hash a second time, and complete with a ContentHash
// that does not match the bytes actually on disk. This proves the whole
// fragment is discarded instead: the retry at that same offset is refused
// as a gap, requiring the client to restart at offset 0, and a real
// offset-0 restart then completes with the correct hash.
func TestFPPConnectWriteChunkDiscardsFragmentOnPanicMidUpload(t *testing.T) {
	dir := t.TempDir()
	writer := &panicOnceChunkWriter{panicOffset: 2, inner: osFPPConnectChunkWriter{}}
	held := newFPPConnectHeldStoreWithWriter(dir, writer, discardLogger())
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }
	const key = "sequences/Panicky2.fseq"

	// First chunk (offset 0, "ab") completes normally: the writer only
	// panics at offset 2.
	outcome, reason, _ := held.WriteChunk("sequences", "Panicky2.fseq", 0, 5, strings.NewReader("ab"), 2, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkAccepted {
		t.Fatalf("chunk 1: outcome = %v reason = %q, want accepted", outcome, reason)
	}

	// Second chunk (offset 2, "cde") panics mid-copy.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("WriteChunk did not panic on the offset-2 call; test setup is broken")
			}
		}()
		held.WriteChunk("sequences", "Panicky2.fseq", 2, 5, strings.NewReader("cde"), 3, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	}()

	held.mu.Lock()
	_, stillInFlight := held.inFlight[key]
	held.mu.Unlock()
	if stillInFlight {
		t.Fatal("in-flight entry survived a panic mid-copy, want the fragment discarded")
	}
	if _, err := os.Stat(held.stagingFilePath("sequences", "Panicky2.fseq")); !os.IsNotExist(err) {
		t.Fatalf("staging fragment survived a panic mid-copy, stat err = %v", err)
	}

	// A retry at the same offset the client still believes is correct
	// must be refused as a gap, never accepted as a continuation of the
	// now-discarded (and hash-poisoned) fragment.
	outcome, reason, _ = held.WriteChunk("sequences", "Panicky2.fseq", 2, 5, strings.NewReader("cde"), 3, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkGap {
		t.Fatalf("retry at offset 2: outcome = %v reason = %q, want gap (offset-0 restart required)", outcome, reason)
	}

	// A real offset-0 restart succeeds with the correct hash: the whole
	// upload, not a hash double-counting the panicked attempt's bytes.
	sum := sha256.Sum256([]byte("abcde"))
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	outcome, reason, rec := held.WriteChunk("sequences", "Panicky2.fseq", 0, 5, strings.NewReader("abcde"), 5, 1<<30, 1<<30, time.Now(), neverActive, neverResolveShowID, neverShowNames)
	if outcome != fppConnectChunkCompleted {
		t.Fatalf("offset-0 restart: outcome = %v reason = %q, want completed", outcome, reason)
	}
	if rec.ContentHash != wantHash {
		t.Fatalf("rec.ContentHash = %q, want %q", rec.ContentHash, wantHash)
	}
}

// TestFPPConnectSweepReclaimsStuckWritingEntry is review round 4 finding
// 5's regression test for its second, independent safety net: an
// in-flight entry stuck writing==true (its owning goroutine gone some way
// other than the panic the deferred reset above already covers) must
// still eventually be reclaimed once it is far older than any legitimate
// chunk transfer could still be in progress, but never reclaimed while it
// is merely a normal, still-in-flight write.
func TestFPPConnectSweepReclaimsStuckWritingEntry(t *testing.T) {
	held, _ := newTestHeldStore(t)
	stuckSince := time.Now()
	held.mu.Lock()
	held.inFlight["sequences/Stuck.fseq"] = &fppConnectInFlight{
		hash: sha256.New(), uploadLength: 5, bytesReceived: 0,
		writing: true, lastChunkAt: stuckSince,
	}
	held.mu.Unlock()
	neverActive := func() (string, bool, bool) { return "", false, false }
	neverResolveShowID := func(string) (string, bool) { return "", false }
	neverShowNames := func() []string { return nil }

	held.mu.Lock()
	held.sweepIdleInFlightLocked(stuckSince.Add(time.Minute))
	_, stillInFlight := held.inFlight["sequences/Stuck.fseq"]
	held.mu.Unlock()
	if !stillInFlight {
		t.Fatal("a merely slow writing entry was reclaimed too early")
	}

	// Past fppConnectStuckWritingTTL: this goroutine is gone, not slow.
	later := stuckSince.Add(fppConnectStuckWritingTTL + time.Minute)
	if outcome, reason, _ := held.WriteChunk("sequences", "Fresh.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, later, neverActive, neverResolveShowID, neverShowNames); outcome != fppConnectChunkCompleted {
		t.Fatalf("fresh upload: outcome = %v reason = %q, want completed", outcome, reason)
	}

	held.mu.Lock()
	_, stillInFlight = held.inFlight["sequences/Stuck.fseq"]
	held.mu.Unlock()
	if stillInFlight {
		t.Fatal("a permanently stuck writing entry survived past fppConnectStuckWritingTTL, want it swept")
	}
}

// TestFPPConnectUploadDrippedOverTenSeconds is review round 3 finding 1's
// regression test: a chunk body that arrives slower than the old,
// removed, server-wide 10s WriteTimeout must still complete successfully
// against a server built the production way, proving the per-route write
// deadline (set only after WriteChunk returns) never overlaps, and is
// never armed early enough to be tripped by, the slow read. Built through
// newFPPConnectProductionServer, the same constructor
// runFPPConnectHTTPListener itself calls (review round 4 finding 6), not
// a hand-copied field set of this test's own that a future change to the
// real one could silently drift out of sync with: a regression that
// re-adds a server-wide WriteTimeout to that one constructor fails this
// test directly.
func TestFPPConnectUploadDrippedOverTenSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping an eleven-second drip test in -short mode")
	}

	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	srv := newFPPConnectProductionServer(view, "node-1", held, discardLogger())
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	data := []byte("hello world!") // 12 bytes
	drip := &slowDripReader{data: data, delay: time.Second}

	req, err := http.NewRequest(http.MethodPatch, "http://"+ln.Addr().String()+"/api/file/sequences", drip)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Upload-Name", "Dripped.fseq")
	req.Header.Set("Upload-Offset", "0")
	req.Header.Set("Upload-Length", strconv.Itoa(len(data)))
	req.ContentLength = int64(len(data))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH with a slow body: %v (the old server-wide WriteTimeout bug delivers an EOF here instead of a response)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	rec, ok := findHeldRecord(t, held, "sequences", "Dripped.fseq")
	if !ok || rec.SizeBytes != int64(len(data)) {
		t.Fatalf("record = %+v, ok=%v, want a completed %d-byte record", rec, ok, len(data))
	}
}

// slowDripReader delivers data one byte at a time, sleeping delay before
// each byte, so a caller reading it to EOF takes roughly
// len(data)*delay: TestFPPConnectUploadDrippedOverTenSeconds's way of
// exceeding the old, removed, 10s WriteTimeout without depending on any
// real network conditions.
type slowDripReader struct {
	data  []byte
	pos   int
	delay time.Duration
}

func (r *slowDripReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := copy(p, r.data[r.pos:r.pos+1])
	r.pos += n
	return n, nil
}
