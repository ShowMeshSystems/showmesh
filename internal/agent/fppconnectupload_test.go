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

// TestFPPConnectPlaylistPostBindsIdempotently proves POST
// /api/playlist/{show} twice with the same body binds once, and GET then
// lists the bound entries.
func TestFPPConnectPlaylistPostBindsIdempotently(t *testing.T) {
	held, _ := newTestHeldStore(t)
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
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
	if !rec.Bound || rec.Show != "Halloween" || rec.LogicalSequence != "Show" {
		t.Fatalf("record = %+v, want bound to Halloween with logical sequence Show", rec)
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
	view := fakeFPPConnectView{enabled: true, showNames: []string{"Halloween"}}
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
	if !rec.Bound || rec.Show != "Halloween" {
		t.Fatalf("record = %+v, want bound to Halloween from the pending playlist post", rec)
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
		view := fakeFPPConnectView{enabled: true, activeShowName: "Christmas", activeShowKnown: true, activeShowEver: true}
		srv := startFPPConnectTestServer(t, view, "node-1", held)

		if resp, body := patchChunk(t, srv, "sequences", "Auto.fseq", 0, 3, []byte("abc")); resp.StatusCode != http.StatusOK {
			t.Fatalf("upload: status = %d, body=%s", resp.StatusCode, body)
		}
		rec, ok := findHeldRecord(t, held, "sequences", "Auto.fseq")
		if !ok {
			t.Fatal("no held record")
		}
		if !rec.Bound || rec.Show != "Christmas" || rec.LogicalSequence != "Auto" {
			t.Fatalf("record = %+v, want bound to Christmas with logical sequence Auto", rec)
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
	pending := make(map[string]string, total)
	pendingOrder := make([]string, 0, total)
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("File%04d.fseq", i)
		pending[name] = "SomeShow"
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

	const maxDir = int64(150)

	// Upload A declares 100 bytes total, but this call only delivers the
	// first 10: it is accepted and left in flight, with 90 bytes still
	// outstanding.
	outcome, reason, _ := held.WriteChunk("sequences", "A.fseq", 0, 100, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, maxDir, time.Now(), neverActive)
	if outcome != fppConnectChunkAccepted {
		t.Fatalf("upload A chunk 1: outcome = %v reason = %q, want accepted", outcome, reason)
	}

	// Upload B also declares 100 bytes, all in its first chunk. Bytes
	// actually on disk right now are only A's 10, which alone would fit
	// under 150 alongside B's 100 (110 total): the exact shape of the
	// bug. Correct behavior adds A's outstanding 90-byte remainder to the
	// check (10 + 90 + 100 = 200 > 150) and refuses B.
	outcome, reason, _ = held.WriteChunk("sequences", "B.fseq", 0, 100, strings.NewReader(strings.Repeat("B", 100)), 100, 1<<30, maxDir, time.Now(), neverActive)
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
	held.BindShow("SomeShow", names, time.Now())

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

	done := make(chan struct{})
	go func() {
		defer close(done)
		held.WriteChunk("sequences", "Slow.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, time.Now(), neverActive)
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

	start := time.Now()
	outcome, reason, _ := held.WriteChunk("sequences", "Abandoned.fseq", 0, 100, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, 1<<30, start, neverActive)
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
	if outcome, reason, _ := held.WriteChunk("sequences", "Fresh.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, later, neverActive); outcome != fppConnectChunkCompleted {
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

	// maxAssetDirBytes is 50. Corrupt.fseq's raw remainder is
	// 10-50 = -40. A fresh 51-byte upload: with the remainder wrongly
	// left negative, 0 (nothing really on disk) + (-40) + 51 = 11 <= 50
	// would be wrongly accepted. Clamped at zero, 0 + 0 + 51 = 51 > 50
	// must be refused.
	outcome, reason, _ := held.WriteChunk("sequences", "New.fseq", 0, 51, strings.NewReader(strings.Repeat("N", 10)), 10, 1<<30, 50, time.Now(), neverActive)
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

	t.Run("completion clears the reservation", func(t *testing.T) {
		held, _ := newTestHeldStore(t)
		outcome, reason, _ := held.WriteChunk("sequences", "Done.fseq", 0, 5, strings.NewReader("hello"), 5, 1<<30, 1<<30, time.Now(), neverActive)
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
		if outcome, _, _ := held.WriteChunk("sequences", "Gapped.fseq", 0, 30, strings.NewReader(strings.Repeat("A", 10)), 10, 1<<30, 1<<30, time.Now(), neverActive); outcome != fppConnectChunkAccepted {
			t.Fatal("setup: chunk 1 not accepted")
		}
		if outcome, _, _ := held.WriteChunk("sequences", "Gapped.fseq", 5, 30, strings.NewReader(strings.Repeat("B", 10)), 10, 1<<30, 1<<30, time.Now(), neverActive); outcome != fppConnectChunkGap {
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

// TestFPPConnectUploadDrippedOverTenSeconds is review round 3 finding 1's
// regression test: a chunk body that arrives slower than the old,
// removed, server-wide 10s WriteTimeout must still complete successfully
// against a server built the production way (the real http.Server field
// set runFPPConnectHTTPListener constructs, not the lighter httptest
// wrapper startFPPConnectTestServer's other tests use), proving the
// per-route write deadline (set only after WriteChunk returns) never
// overlaps, and is never armed early enough to be tripped by, the slow
// read.
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
	srv := &http.Server{
		Handler:           newFPPConnectHandler(view, "node-1", held, time.Now, discardLogger()),
		ReadHeaderTimeout: fppConnectReadHeaderTimeout,
		ReadTimeout:       fppConnectServerReadTimeoutFloor,
		WriteTimeout:      0,
		MaxHeaderBytes:    fppConnectMaxHeaderBytes,
		ConnContext:       fppConnectConnContext,
	}
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
