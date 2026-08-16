package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
)

// This file is Track E seam E3/E4's own showmeshctl test suite: list,
// get, upload, and fetch. It follows cmd_resolume_composition_test.go's
// pattern of driving each subcommand against a real httptest.Server.

// TestAssetUploadBudgetMatchesServer is the mechanism cmd_assets.go's own
// doc comment promises: cmd/showmeshctl may never import a coordinator
// package in its PRODUCTION build (importgraph_test.go's TestNoForbiddenImports
// runs `go list -deps .`, which excludes _test.go files), so this test —
// and only this test — imports assetstore directly to prove the local
// restatement has not drifted from the server's own numbers.
func TestAssetUploadBudgetMatchesServer(t *testing.T) {
	if assetDefaultMaxUploadBytes != assetstore.DefaultMaxUploadBytes {
		t.Errorf("assetDefaultMaxUploadBytes = %d, want %d (assetstore.DefaultMaxUploadBytes)", assetDefaultMaxUploadBytes, assetstore.DefaultMaxUploadBytes)
	}
	if assetMinTransferBytesPerSecond != assetstore.MinTransferBytesPerSecond {
		t.Errorf("assetMinTransferBytesPerSecond = %d, want %d (assetstore.MinTransferBytesPerSecond)", assetMinTransferBytesPerSecond, assetstore.MinTransferBytesPerSecond)
	}
	for _, size := range []int64{0, 1, 1024, 100 * 1024 * 1024, assetstore.DefaultMaxUploadBytes, 10 * assetstore.DefaultMaxUploadBytes} {
		got, want := assetUploadBudget(size), assetstore.UploadBudget(size)
		if got != want {
			t.Errorf("assetUploadBudget(%d) = %s, want %s (assetstore.UploadBudget)", size, got, want)
		}
	}
}

// TestAssetUploadBudgetClientNeverBelowServer is spec section 6's own
// requirement stated as an assertion: "the client budget is >= the server
// budget" — here they are required to be EQUAL (this program restates the
// identical formula rather than a looser one), which is the strongest
// form of that guarantee.
func TestAssetUploadBudgetClientNeverBelowServer(t *testing.T) {
	for _, size := range []int64{0, 5000, 5 * 1024 * 1024 * 1024} {
		if assetUploadBudget(size) < assetstore.UploadBudget(size) {
			t.Errorf("client budget for %d bytes (%s) is LESS than the server's (%s)", size, assetUploadBudget(size), assetstore.UploadBudget(size))
		}
	}
}

func assetsListJSON(assets ...assetRecord) string {
	b, _ := json.Marshal(assetsListResponse{ServerTime: time.Now(), Assets: assets})
	return string(b)
}

func TestCmdAssetsListPrintsTable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assets" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, assetsListJSON(assetRecord{
			ID: "a1", Show: "halloween-2026", Sequence: "opening", TargetKind: "node", Target: "render-01",
			MediaType: "fseq", ContentHash: "sha256:abc", RuntimeFilename: "Thriller.fseq", SizeBytes: 4096,
			CreatedAt: mustParse(t, "2026-08-10T20:00:00Z"), Current: true,
		}))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"a1", "render-01", "Thriller.fseq", "fseq"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdAssetsListForwardsQueryFilters(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("show"); got != "halloween-2026" {
			t.Errorf("show query = %q, want halloween-2026", got)
		}
		if got := r.URL.Query().Get("node"); got != "render-01" {
			t.Errorf("node query = %q, want render-01", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, assetsListJSON())
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"list", "--server", ts.URL, "--show", "halloween-2026", "--node", "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

func TestCmdAssetsGetTextOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assets/a1" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		resp := assetResponse{ServerTime: time.Now(), Asset: assetRecord{
			ID: "a1", Show: "halloween-2026", Sequence: "opening", TargetKind: "node", Target: "render-01",
			MediaType: "fseq", ContentHash: "sha256:abc", RuntimeFilename: "Thriller.fseq", SizeBytes: 4096,
			CreatedAt: mustParse(t, "2026-08-10T20:00:00Z"), Current: true,
		}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"get", "--server", ts.URL, "a1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Thriller.fseq") || !strings.Contains(out, "sha256:abc") {
		t.Errorf("output missing expected fields:\n%s", out)
	}
}

func TestCmdAssetsGetNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no asset with id \"no-such\" exists","serverTime":"2026-08-10T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"get", "--server", ts.URL, "no-such"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitNotFound {
		t.Fatalf("exit code = %d, want exitNotFound; stderr=%s", code, stderr.String())
	}
}

// --- upload ---

// receivedUpload captures what the fake coordinator's POST /assets
// handler actually parsed, for assertions.
type receivedUpload struct {
	fields   map[string]string
	filename string
	content  []byte
	// fileArrivedAt is the 0-based index of the "file" part among every
	// part this handler saw — used to prove fields really do precede it.
	fileArrivedAt int
}

func fakeAssetUploadServer(t *testing.T, received *receivedUpload, respond func(w http.ResponseWriter, up receivedUpload)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assets" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		mr, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		received.fields = map[string]string{}
		idx := 0
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				t.Fatalf("NextPart: %v", perr)
			}
			if part.FormName() == "file" {
				received.fileArrivedAt = idx
				received.filename = part.FileName()
				content, rerr := io.ReadAll(part)
				if rerr != nil {
					t.Fatalf("reading file part: %v", rerr)
				}
				received.content = content
			} else {
				val, rerr := io.ReadAll(part)
				if rerr != nil {
					t.Fatalf("reading field part %s: %v", part.FormName(), rerr)
				}
				received.fields[part.FormName()] = string(val)
			}
			_ = part.Close()
			idx++
		}
		respond(w, *received)
	}))
}

func TestCmdAssetsUploadHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Thriller.fseq")
	content := []byte("fseq bytes for the opening number")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sum := sha256.Sum256(content)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])

	var received receivedUpload
	ts := fakeAssetUploadServer(t, &received, func(w http.ResponseWriter, up receivedUpload) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		resp := assetResponse{ServerTime: time.Now(), Asset: assetRecord{
			ID: "a1", Show: up.fields["show"], Sequence: up.fields["sequence"],
			TargetKind: up.fields["targetKind"], Target: up.fields["target"],
			MediaType: up.fields["mediaType"], ContentHash: wantHash,
			RuntimeFilename: up.filename, SizeBytes: int64(len(up.content)),
			CreatedAt: mustParse(t, "2026-08-10T20:00:00Z"), Current: true,
		}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{
		"upload", "--server", ts.URL,
		"--show", "halloween-2026", "--sequence", "opening", "--media-type", "fseq",
		"--target-kind", "node", "--target", "render-01", "--file", path,
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	if !bytes.Equal(received.content, content) {
		t.Errorf("server received %q, want %q", received.content, content)
	}
	if received.filename != "Thriller.fseq" {
		t.Errorf("server received filename %q, want Thriller.fseq", received.filename)
	}
	if received.fields["show"] != "halloween-2026" || received.fields["targetKind"] != "node" || received.fields["target"] != "render-01" {
		t.Errorf("server received fields %v", received.fields)
	}
	if received.fileArrivedAt == 0 {
		t.Error("the file part arrived FIRST — every field must precede it (the coordinator refuses this)")
	}

	out := stdout.String()
	if !strings.Contains(out, wantHash) {
		t.Errorf("output missing content hash %q:\n%s", wantHash, out)
	}
}

func TestCmdAssetsUploadMissingFlagsIsUsageError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.fseq")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	base := []string{
		"--show", "halloween-2026", "--sequence", "opening", "--media-type", "fseq",
		"--target-kind", "node", "--target", "render-01", "--file", path,
	}
	tests := []struct {
		name string
		omit string // flag name to drop from base
	}{
		{"missing show", "--show"},
		{"missing sequence", "--sequence"},
		{"missing media-type", "--media-type"},
		{"missing target-kind", "--target-kind"},
		{"missing file", "--file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string{"upload"}
			skip := false
			for _, a := range base {
				if skip {
					skip = false
					continue
				}
				if a == tt.omit {
					skip = true
					continue
				}
				args = append(args, a)
			}
			var stdout, stderr bytes.Buffer
			code := cmdAssets(args, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
			if code != exitUsage {
				t.Errorf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
			}
		})
	}
}

func TestCmdAssetsUploadNodeTargetKindWithoutTargetIsUsageError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.fseq")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{
		"upload", "--show", "halloween-2026", "--sequence", "opening", "--media-type", "fseq",
		"--target-kind", "node", "--file", path,
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdAssetsUploadNonexistentFileIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{
		"upload", "--server", "http://127.0.0.1:1", "--show", "halloween-2026", "--sequence", "opening",
		"--media-type", "fseq", "--target-kind", "show", "--file", "/no/such/file.fseq",
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

func TestCmdAssetsUploadNonOKPrintsProblemDetail(t *testing.T) {
	var received receivedUpload
	ts := fakeAssetUploadServer(t, &received, func(w http.ResponseWriter, up receivedUpload) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/asset-target-required","title":"Asset target required","status":400,"detail":"targetKind is node but target is empty","serverTime":"2026-08-10T21:00:00Z"}`)
	})
	defer ts.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.fseq")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{
		"upload", "--server", ts.URL, "--show", "halloween-2026", "--sequence", "opening",
		"--media-type", "fseq", "--target-kind", "show", "--file", path,
	}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (400 invalid-parameter class); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "targetKind is node but target is empty") {
		t.Errorf("stderr does not state the coordinator's own failure reason:\n%s", stderr.String())
	}
}

// --- fetch ---

func TestCmdAssetsFetchHappyPath(t *testing.T) {
	content := []byte("fseq bytes to download")
	sum := sha256.Sum256(content)
	hash := "sha256:" + hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/assets/a1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		resp := assetResponse{ServerTime: time.Now(), Asset: assetRecord{
			ID: "a1", Show: "halloween-2026", Sequence: "opening", TargetKind: "node", Target: "render-01",
			MediaType: "fseq", ContentHash: hash, RuntimeFilename: "Thriller.fseq", SizeBytes: int64(len(content)),
			CreatedAt: mustParse(t, "2026-08-10T20:00:00Z"), Current: true,
		}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/v1/assets/a1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = w.Write(content)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	outPath := filepath.Join(t.TempDir(), "Thriller.fseq")
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"fetch", "--server", ts.URL, "--out", outPath, "a1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading --out: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("--out content = %q, want %q", got, content)
	}
}

func TestCmdAssetsFetchHashMismatchDoesNotWriteOut(t *testing.T) {
	realContent := []byte("real bytes")
	sum := sha256.Sum256(realContent)
	claimedHash := "sha256:" + hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/assets/a1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		resp := assetResponse{ServerTime: time.Now(), Asset: assetRecord{
			ID: "a1", Show: "halloween-2026", Sequence: "opening", TargetKind: "node", Target: "render-01",
			MediaType: "fseq", ContentHash: claimedHash, RuntimeFilename: "Thriller.fseq", SizeBytes: int64(len(realContent)),
			CreatedAt: mustParse(t, "2026-08-10T20:00:00Z"), Current: true,
		}}
		b, _ := json.Marshal(resp)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/v1/assets/a1/content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ShowMesh-API-Version", "1")
		// Deliberately wrong bytes: the server's own content disagrees with
		// the hash its OWN metadata reported, simulating corruption in
		// transit or a server-side defect this client must still catch.
		_, _ = w.Write([]byte("corrupted, different bytes entirely"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	outPath := filepath.Join(t.TempDir(), "Thriller.fseq")
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"fetch", "--server", ts.URL, "--out", outPath, "a1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = exitOK, want a failure on hash mismatch")
	}
	if !strings.Contains(stderr.String(), "hash") {
		t.Errorf("stderr does not name the hash mismatch:\n%s", stderr.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Error("--out was written despite a hash mismatch")
	}
}

func TestCmdAssetsFetchMissingOutIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"fetch", "--server", "http://127.0.0.1:1", "a1"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}

// --- top-level dispatch ---

func TestCmdAssetsUnknownSubcommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"bogus"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage", code)
	}
}

func TestCmdAssetsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"--help"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "upload") || !strings.Contains(stdout.String(), "fetch") {
		t.Errorf("help output missing subcommands:\n%s", stdout.String())
	}
}

func TestRunDispatchesAssets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"assets", "--help"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

// --- manifest ---

func strPtr(s string) *string { return &s }

func assetManifestJSON(nodes ...nodeAssetManifestRecord) string {
	b, _ := json.Marshal(assetManifestResponse{ServerTime: time.Now(), Nodes: nodes})
	return string(b)
}

func nodeAssetManifestJSON(m nodeAssetManifestRecord) string {
	b, _ := json.Marshal(nodeAssetManifestResponse{ServerTime: time.Now(), Manifest: m})
	return string(b)
}

func TestCmdAssetsManifestPrintsTable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/assets/manifest" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, assetManifestJSON(
			nodeAssetManifestRecord{
				Node: "render-01", State: "not_ready", Reason: strPtr("missing 1 expected asset(s)"),
				Missing: []missingAssetRecord{{AssetID: "a1", Sequence: "opening", Filename: "Thriller.fseq", ContentHash: "sha256:abc", SizeBytes: 100}},
				Gaps:    []assetGapRecord{},
				Extra:   []extraAssetRecord{},
			},
			nodeAssetManifestRecord{Node: "render-02", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
		))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"manifest", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (no --require-ready); stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"render-01", "not_ready", "render-02", "ready", "Thriller.fseq", "opening"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdAssetsManifestNodeFlagUsesSingleNodeRoute(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/render-01/assets" {
			t.Fatalf("unexpected path %s, want the single-node route", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, nodeAssetManifestJSON(nodeAssetManifestRecord{
			Node: "render-01", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{},
		}))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"manifest", "--node", "render-01", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "render-01") {
		t.Errorf("output missing render-01:\n%s", stdout.String())
	}
}

// TestCmdAssetsManifestWithoutRequireReadyAlwaysExitsOK is this
// subcommand's own "reporting is not failing" rule: a not_ready node
// changes the printed table but NEVER the exit code unless --require-ready
// is passed.
//
// Broken and confirmed to fail: removed the "if !requireReady { return
// exitOK }" early return in cmdAssetsManifest, so a not_ready node fell
// through to the exit-code switch even without the flag — this test's
// assertion failed (got exitAssetsNotReady, want exitOK). Restored
// afterward.
func TestCmdAssetsManifestWithoutRequireReadyAlwaysExitsOK(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, assetManifestJSON(
			nodeAssetManifestRecord{Node: "render-01", State: "not_ready", Reason: strPtr("missing"), Missing: []missingAssetRecord{{}}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
			nodeAssetManifestRecord{Node: "render-02", State: "unknown", Reason: strPtr("never reported"), Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
		))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"manifest", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK (reporting is not failing without --require-ready); stderr=%s", code, stderr.String())
	}
}

// TestCmdAssetsManifestRequireReadyExitCodes proves exit 20 and exit 21
// are reachable and distinct, and that "not_ready" always wins over
// "unknown" when both appear across the node set (spec: exit 21 fires
// only when NO node is not_ready).
func TestCmdAssetsManifestRequireReadyExitCodes(t *testing.T) {
	tests := []struct {
		name  string
		nodes []nodeAssetManifestRecord
		want  int
	}{
		{
			"all ready",
			[]nodeAssetManifestRecord{
				{Node: "render-01", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
				{Node: "render-02", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
			},
			exitOK,
		},
		{
			"one unknown, none not_ready",
			[]nodeAssetManifestRecord{
				{Node: "render-01", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
				{Node: "render-02", State: "unknown", Reason: strPtr("never reported"), Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
			},
			exitAssetsUnknown,
		},
		{
			"one not_ready, none unknown",
			[]nodeAssetManifestRecord{
				{Node: "render-01", State: "not_ready", Reason: strPtr("missing"), Missing: []missingAssetRecord{{}}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
				{Node: "render-02", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
			},
			exitAssetsNotReady,
		},
		{
			// not_ready MUST win over unknown: exit 21 fires only when NO
			// node is not_ready.
			"both not_ready and unknown present",
			[]nodeAssetManifestRecord{
				{Node: "render-01", State: "not_ready", Reason: strPtr("missing"), Missing: []missingAssetRecord{{}}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
				{Node: "render-02", State: "unknown", Reason: strPtr("never reported"), Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
			},
			exitAssetsNotReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprint(w, assetManifestJSON(tt.nodes...))
			}))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdAssets([]string{"manifest", "--require-ready", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
			if code != tt.want {
				t.Errorf("exit code = %d, want %d; stderr=%s", code, tt.want, stderr.String())
			}
		})
	}
}

func TestCmdAssetsManifestJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, assetManifestJSON(
			nodeAssetManifestRecord{Node: "render-01", State: "ready", Missing: []missingAssetRecord{}, Gaps: []assetGapRecord{}, Extra: []extraAssetRecord{}},
		))
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"manifest", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var decoded struct {
		Nodes []nodeAssetManifestRecord `json:"nodes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode --output json: %v\nstdout: %s", err, stdout.String())
	}
	if len(decoded.Nodes) != 1 || decoded.Nodes[0].Node != "render-01" {
		t.Errorf("decoded nodes = %+v, want one entry for render-01", decoded.Nodes)
	}
}

// TestCmdAssetsManifestHelpListsBothExitCodes checks stderr, not stdout:
// this subcommand's flag.FlagSet is built by newFlagSet with
// SetOutput(stderr), and cmdAssetsManifest's own Usage closure writes to
// the same stderr writer — matching every other subcommand's --help in
// this package.
func TestCmdAssetsManifestHelpListsBothExitCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"manifest", "--help"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	out := stderr.String()
	if !strings.Contains(out, "exit 20") || !strings.Contains(out, "exit 21") {
		t.Errorf("help output missing exit codes 20/21:\n%s", out)
	}
}

// TestMainHelpListsAssetsExitCodes proves the TOP-LEVEL "showmeshctl
// --help" exit-code table also names 20 and 21 (main.go's usage text) —
// distinct from the subcommand-level test above, which covers
// "assets manifest --help" only.
func TestMainHelpListsAssetsExitCodes(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	out := stdout.String() + stderr.String()
	if !strings.Contains(out, "20 assets not ready") || !strings.Contains(out, "21 assets unknown") {
		t.Errorf("top-level help missing exit codes 20/21:\n%s", out)
	}
}
