package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
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

func (fakeENOSPCWriter) WriteAt(path string, offset int64, data []byte) error {
	return &os.PathError{Op: "write", Path: path, Err: syscall.ENOSPC}
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
	})
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
