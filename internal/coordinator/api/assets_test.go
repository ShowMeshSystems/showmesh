package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track E seam E3/E4's own test suite: asset upload, listing,
// and content retrieval (ADR-028). It follows showobjects_test.go's
// pattern one seam over: a real *store.Store, a real identity.Service, and
// (new to this seam) a real assetstore.VolumeBackend rooted at a temp
// directory, driven through the real route table.

// fakeAssetSettingsSource is a test-only, mutable [AssetSettingsSource] —
// Track G seam G-4 replaced the old plain-field AssetMaxUploadBytes/
// AssetInventoryInterval/AssetSyncEnabled dependencies with one live
// interface, so a test that used to assign a field now constructs one of
// these (or mutates the one assetsTestDeps already wired in).
type fakeAssetSettingsSource struct {
	contentBaseURL    string
	maxUploadBytes    int64
	inventoryInterval time.Duration
}

func (f *fakeAssetSettingsSource) ContentBaseURL() string           { return f.contentBaseURL }
func (f *fakeAssetSettingsSource) MaxUploadBytes() int64            { return f.maxUploadBytes }
func (f *fakeAssetSettingsSource) InventoryInterval() time.Duration { return f.inventoryInterval }

// assetsTestDeps mirrors showObjectsTestDeps, additionally wiring a real
// VolumeBackend and a generous upload limit so ordinary tests never trip
// the size bound; tests proving the bound itself override
// deps.AssetSettings explicitly.
func assetsTestDeps(t *testing.T, svc identity.Service, st *store.Store) Dependencies {
	t.Helper()
	backend, err := assetstore.NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("new volume backend: %v", err)
	}
	deps := showObjectsTestDeps(svc, st)
	deps.Assets = st
	deps.AssetBackend = backend
	deps.AssetSettings = &fakeAssetSettingsSource{maxUploadBytes: 1 << 20} // 1 MiB, generous for every test fixture in this file
	return deps
}

// assetsAdminAPI builds a real *API with a real store, identity.Service,
// and asset backend, plus an admin credential (asset:write is admin-only)
// and one declared node and one "show" object every test in this file can
// target — mirroring resolumeCompositionAdminAPI's identical shape one
// seam over.
func assetsAdminAPI(t *testing.T) (*API, *store.Store, map[string]string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(assetsTestDeps(t, svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	return api, st, map[string]string{"Authorization": "Bearer " + token}
}

// buildAssetUploadMultipartBody builds a real multipart/form-data body:
// fields first (mirroring showmeshctl's own required ordering — the file
// part must not arrive first), then one file part named "file". fields
// omitting a key sends no part for it at all, matching how showmeshctl
// itself would simply not write a field it has no value for.
func buildAssetUploadMultipartBody(t *testing.T, fields map[string]string, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, k := range []string{"show", "sequence", "mediaType", "targetKind", "target"} {
		v, ok := fields[k]
		if !ok {
			continue
		}
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("write field %q: %v", k, err)
		}
	}
	if filename != "" {
		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func doAssetUpload(t *testing.T, h http.Handler, fields map[string]string, filename string, content []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	body, contentType := buildAssetUploadMultipartBody(t, fields, filename, content)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return doRawRequest(t, h, req)
}

func validAssetFields() map[string]string {
	return map[string]string{
		"show": "halloween-2026", "sequence": "opening", "mediaType": "fseq",
		"targetKind": "node", "target": "render-01",
	}
}

func contentHashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// --- happy path ---

func TestPostAssetUploadHappyPath(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	content := []byte("fseq-bytes-for-the-opening-number")

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "Thriller.fseq", content, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /assets: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var uploaded v1AssetResponseForTest
	if err := json.Unmarshal(body, &uploaded); err != nil {
		t.Fatalf("decode upload response: %v\nbody: %s", err, body)
	}
	if uploaded.Asset.ID == "" {
		t.Fatal("asset.id is empty")
	}
	if uploaded.Asset.ContentHash != contentHashOf(content) {
		t.Errorf("contentHash = %q, want %q", uploaded.Asset.ContentHash, contentHashOf(content))
	}
	if uploaded.Asset.RuntimeFilename != "Thriller.fseq" {
		t.Errorf("runtimeFilename = %q, want %q", uploaded.Asset.RuntimeFilename, "Thriller.fseq")
	}
	if uploaded.Asset.SizeBytes != int64(len(content)) {
		t.Errorf("sizeBytes = %d, want %d", uploaded.Asset.SizeBytes, len(content))
	}
	if !uploaded.Asset.Current {
		t.Error("current = false on a freshly created asset, want true")
	}

	// GET /assets/{id}
	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID, auth)
	var got v1AssetResponseForTest
	if err := json.Unmarshal(getBody, &got); err != nil {
		t.Fatalf("decode get response: %v\nbody: %s", err, getBody)
	}
	if got.Asset.ID != uploaded.Asset.ID {
		t.Errorf("GET asset id = %q, want %q", got.Asset.ID, uploaded.Asset.ID)
	}

	// GET /assets/{id}/content
	contentResp, contentBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID+"/content", auth)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("GET content: status = %d, want 200", contentResp.StatusCode)
	}
	if !bytes.Equal(contentBody, content) {
		t.Errorf("content body = %q, want %q", contentBody, content)
	}
	wantETag := `"` + uploaded.Asset.ContentHash + `"`
	if got := contentResp.Header.Get("ETag"); got != wantETag {
		t.Errorf("ETag = %q, want %q", got, wantETag)
	}
}

// v1AssetResponseForTest mirrors v1.AssetResponse for this file's own
// decoding — kept local rather than importing v1 into every test, matching
// this package's existing convention of decoding into generic maps or
// small local shapes.
type v1AssetResponseForTest struct {
	ServerTime string         `json:"serverTime"`
	Asset      v1AssetForTest `json:"asset"`
}

type v1AssetForTest struct {
	ID                     string  `json:"id"`
	Show                   string  `json:"show"`
	Sequence               string  `json:"sequence"`
	TargetKind             string  `json:"targetKind"`
	Target                 string  `json:"target"`
	MediaType              string  `json:"mediaType"`
	ContentHash            string  `json:"contentHash"`
	RuntimeFilename        string  `json:"runtimeFilename"`
	SizeBytes              int64   `json:"sizeBytes"`
	CreatedAt              string  `json:"createdAt"`
	CreatedByPrincipalID   *string `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string `json:"createdByPrincipalName"`
	SupersededAt           *string `json:"supersededAt"`
	Current                bool    `json:"current"`
}

// --- ADR-028's own load-bearing property: a filename is not an identity ---

// TestPostAssetUploadSameFilenameThreeNodesResolveDistinctly is acceptance
// criterion 2: three FSEQ files with the SAME filename, different content,
// uploaded for three different node targets; each node resolves to its own.
func TestPostAssetUploadSameFilenameThreeNodesResolveDistinctly(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	mustDeclareNode(t, st, "render-03")

	targets := []string{"render-01", "render-02", "render-03"}
	ids := make(map[string]string, 3)
	for i, node := range targets {
		content := fmt.Appendf(nil, "content for %s (%d)", node, i)
		fields := validAssetFields()
		fields["target"] = node
		resp, body := doAssetUpload(t, api.Handler, fields, "HalloweenOpening.fseq", content, auth)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload for %s: status = %d, body: %s", node, resp.StatusCode, body)
		}
		var got v1AssetResponseForTest
		_ = json.Unmarshal(body, &got)
		ids[node] = got.Asset.ID

		// Each node's own content resolves back to exactly that node's bytes.
		_, contentBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+got.Asset.ID+"/content", auth)
		if !bytes.Equal(contentBody, content) {
			t.Errorf("node %s: content = %q, want %q", node, contentBody, content)
		}
	}

	if ids["render-01"] == ids["render-02"] || ids["render-02"] == ids["render-03"] || ids["render-01"] == ids["render-03"] {
		t.Errorf("expected three distinct asset ids, got %v", ids)
	}
}

// --- idempotency and supersession ---

func TestPostAssetUploadIdenticalBytesIsIdempotent(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	content := []byte("identical bytes")

	resp1, body1 := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first upload: status = %d, body: %s", resp1.StatusCode, body1)
	}
	var first v1AssetResponseForTest
	_ = json.Unmarshal(body1, &first)

	resp2, body2 := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second (idempotent) upload: status = %d, body: %s", resp2.StatusCode, body2)
	}
	var second v1AssetResponseForTest
	_ = json.Unmarshal(body2, &second)

	if second.Asset.ID != first.Asset.ID {
		t.Errorf("idempotent re-upload created a NEW asset id %q, want the existing %q", second.Asset.ID, first.Asset.ID)
	}

	recs, err := st.ListAssets(context.Background(), store.AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 1 {
		t.Errorf("expected exactly one row after an idempotent re-upload, got %d", len(recs))
	}
}

func TestPostAssetUploadDifferentBytesSupersedesPreviousCurrent(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	resp1, body1 := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("version one"), auth)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first upload: status = %d, body: %s", resp1.StatusCode, body1)
	}
	var first v1AssetResponseForTest
	_ = json.Unmarshal(body1, &first)
	if !first.Asset.Current {
		t.Fatal("first upload: current = false, want true")
	}

	resp2, body2 := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("version two, different bytes"), auth)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second upload: status = %d, body: %s", resp2.StatusCode, body2)
	}
	var second v1AssetResponseForTest
	_ = json.Unmarshal(body2, &second)
	if second.Asset.ID == first.Asset.ID {
		t.Fatal("different bytes produced the SAME asset id, want a new one")
	}
	if !second.Asset.Current {
		t.Error("second upload: current = false, want true")
	}

	recs, err := st.ListAssets(context.Background(), store.AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("expected two rows (one superseded, one current), got %d", len(recs))
	}
	currentCount := 0
	for _, r := range recs {
		if r.SupersededAt == nil {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Errorf("expected exactly one CURRENT row after supersession, got %d", currentCount)
	}

	// Read supersession back through the WIRE, not only out of the store.
	// Both upload responses above describe freshly created rows, which are
	// always current, so they cannot tell a real `current` field from a
	// hardcoded true; only listing after the fact can.
	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/assets?show=halloween-2026&sequence=opening", auth)
	var listed struct {
		Assets []v1AssetForTest `json:"assets"`
	}
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode list response: %v\nbody: %s", err, listBody)
	}
	if len(listed.Assets) != 2 {
		t.Fatalf("listed %d assets, want 2", len(listed.Assets))
	}
	byID := map[string]v1AssetForTest{}
	for _, a := range listed.Assets {
		byID[a.ID] = a
	}
	if byID[first.Asset.ID].Current {
		t.Error("the superseded asset renders current = true on the wire, want false")
	}
	if !byID[second.Asset.ID].Current {
		t.Error("the current asset renders current = false on the wire, want true")
	}
	if byID[first.Asset.ID].SupersededAt == nil {
		t.Error("the superseded asset has a null supersededAt on the wire")
	}
}

// --- validation ---

func TestPostAssetUploadValidation(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	tests := []struct {
		name       string
		mutate     func(map[string]string)
		wantStatus int
		wantType   string
	}{
		{"missing show", func(f map[string]string) { delete(f, "show") }, http.StatusBadRequest, ""},
		{"show does not exist", func(f map[string]string) { f["show"] = "no-such-show" }, http.StatusBadRequest, ""},
		{"missing sequence", func(f map[string]string) { delete(f, "sequence") }, http.StatusBadRequest, ""},
		{"invalid sequence slug", func(f map[string]string) { f["sequence"] = "Not A Slug!" }, http.StatusBadRequest, ""},
		{"missing mediaType", func(f map[string]string) { delete(f, "mediaType") }, http.StatusBadRequest, ""},
		{"invalid mediaType", func(f map[string]string) { f["mediaType"] = "video" }, http.StatusBadRequest, ""},
		{
			"missing targetKind", func(f map[string]string) { delete(f, "targetKind") }, http.StatusBadRequest, "",
		},
		{
			"node target missing", func(f map[string]string) { delete(f, "target") }, http.StatusBadRequest,
			problemBaseURI + "asset-target-required",
		},
		{"target not declared", func(f map[string]string) { f["target"] = "no-such-node" }, http.StatusBadRequest, ""},
		{"show target with target set", func(f map[string]string) { f["targetKind"] = "show"; f["target"] = "render-01" }, http.StatusBadRequest, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := validAssetFields()
			tt.mutate(fields)
			resp, body := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("x"), auth)
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, tt.wantStatus, body)
			}
			if tt.wantType != "" {
				var p v1ProblemForTest
				if err := json.Unmarshal(body, &p); err != nil {
					t.Fatalf("decode problem: %v\nbody: %s", err, body)
				}
				if p.Type != tt.wantType {
					t.Errorf("problem type = %q, want %q", p.Type, tt.wantType)
				}
			}
		})
	}
}

type v1ProblemForTest struct {
	Type   string `json:"type"`
	Status int    `json:"status"`
}

// showTargetedAssetIsAccepted proves the "show" branch of targetKind is a
// real, reachable path, not merely rejected by every validation test above.
func TestPostAssetUploadShowTargetedAccepted(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	fields := validAssetFields()
	fields["targetKind"] = "show"
	delete(fields, "target")

	resp, body := doAssetUpload(t, api.Handler, fields, "common.mp3", []byte("shared audio"), auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var got v1AssetResponseForTest
	_ = json.Unmarshal(body, &got)
	if got.Asset.TargetKind != "show" || got.Asset.Target != "" {
		t.Errorf("targetKind/target = %q/%q, want \"show\"/\"\"", got.Asset.TargetKind, got.Asset.Target)
	}
}

// --- multipart mechanics (mirrors readResolumeCompositionFilePart's rules) ---

func TestPostAssetUploadFilePartFirstRefuses(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	// Build a body with the FILE part first, fields after — the one shape
	// buildAssetUploadMultipartBody cannot produce, so this is hand-built.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", "a.fseq")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = part.Write([]byte("x"))
	for k, v := range validAssetFields() {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	// Asserted on the SPECIFIC detail text, not merely the status: every
	// field also being empty (since none arrived before "file") would
	// independently produce its own 400 ("show" is required) even if the
	// file-arrived-first rule itself were removed, so a bare status check
	// would not actually guard this rule.
	var full struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &full)
	if !bytes.Contains([]byte(full.Detail), []byte("arrived first")) {
		t.Errorf("detail = %q, want it to name the file-arrived-first rule", full.Detail)
	}
}

func TestPostAssetUploadSecondFilePartRefuses(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range validAssetFields() {
		_ = mw.WriteField(k, v)
	}
	p1, err := mw.CreateFormFile("file", "first.fseq")
	if err != nil {
		t.Fatalf("CreateFormFile 1: %v", err)
	}
	_, _ = p1.Write([]byte("first"))
	p2, err := mw.CreateFormFile("file", "second.fseq")
	if err != nil {
		t.Fatalf("CreateFormFile 2: %v", err)
	}
	_, _ = p2.Write([]byte("second"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestPostAssetUploadFileFieldWithNoFilenameRefuses(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range validAssetFields() {
		_ = mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormField("file")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	_, _ = fw.Write([]byte("not a file"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	// The status alone is not enough: removing the guard produces a 500 that
	// this test would still read as "refused". The detail is what proves the
	// refusal named the right problem.
	if !bytes.Contains(body, []byte("must be an uploaded file with a filename")) {
		t.Fatalf("refusal does not name the missing filename; body: %s", body)
	}
}

func TestPostAssetUploadNoFilePartRefuses(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "", nil, auth)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// --- size and space ---

func TestPostAssetUploadTooLarge(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	deps.AssetSettings = &fakeAssetSettingsSource{maxUploadBytes: 4} // tiny, deliberately
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("this is more than four bytes"), auth)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", resp.StatusCode, body)
	}
	var p v1ProblemForTest
	_ = json.Unmarshal(body, &p)
	if p.Type != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("problem type = %q, want the shared payload-too-large type %q", p.Type, ProblemTypeResolumeCompositionTooLarge)
	}
}

// fakeNoSpaceBackend is an assetstore.Backend whose Put always reports
// ErrNoSpace, for TestPostAssetUploadNoSpace507 — triggering a REAL
// ENOSPC from VolumeBackend is not practical in a unit test, so this
// proves the handler's own translation of the sentinel instead.
type fakeNoSpaceBackend struct{}

func (fakeNoSpaceBackend) Put(context.Context, io.Reader, int64) (assetstore.Blob, error) {
	return assetstore.Blob{}, assetstore.ErrNoSpace
}
func (fakeNoSpaceBackend) Open(context.Context, string) (io.ReadSeekCloser, int64, error) {
	return nil, 0, assetstore.ErrNotFound
}
func (fakeNoSpaceBackend) Stat(context.Context, string) (int64, error) {
	return 0, assetstore.ErrNotFound
}

func TestPostAssetUploadNoSpace507(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	deps.AssetBackend = fakeNoSpaceBackend{}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("x"), auth)
	if resp.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body: %s", resp.StatusCode, body)
	}
	var p v1ProblemForTest
	_ = json.Unmarshal(body, &p)
	if p.Type != problemBaseURI+"storage-full" {
		t.Errorf("problem type = %q, want storage-full", p.Type)
	}

	recs, err := st.ListAssets(context.Background(), store.AssetFilter{})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected nothing registered after ErrNoSpace, got %d row(s)", len(recs))
	}
}

// --- interrupted upload registers nothing (acceptance criterion 5) ---

// truncatingReader returns n bytes from data and then a read error,
// simulating a client connection that dies mid-upload.
type truncatingReader struct {
	data []byte
	n    int
	err  error
}

func (r *truncatingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		if r.err == nil {
			r.err = errors.New("simulated connection failure")
		}
		return 0, r.err
	}
	take := r.n
	if take > len(p) {
		take = len(p)
	}
	if take > len(r.data) {
		take = len(r.data)
	}
	copy(p, r.data[:take])
	r.data = r.data[take:]
	r.n -= take
	if take == 0 {
		return 0, r.err
	}
	return take, nil
}

func TestPostAssetUploadInterruptedRegistersNothing(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	full, _ := buildAssetUploadMultipartBody(t, validAssetFields(), "a.fseq", bytes.Repeat([]byte("x"), 4096))
	fullBytes := full.Bytes()

	// Cut the body well before its end, mid-file-part, so the backend's
	// own Put() sees a read error partway through the stream.
	truncated := &truncatingReader{data: fullBytes, n: len(fullBytes) - 500}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", io.NopCloser(truncated))
	req.ContentLength = -1
	_, contentType := buildAssetUploadMultipartBody(t, validAssetFields(), "a.fseq", nil)
	req.Header.Set("Content-Type", contentType)
	for k, v := range auth {
		req.Header.Set(k, v)
	}

	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("interrupted upload reported 200, want a failure; body: %s", body)
	}

	recs, err := st.ListAssets(context.Background(), store.AssetFilter{})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("interrupted upload registered %d row(s), want 0", len(recs))
	}
}

// --- content: truncation is reported, never served ---

// TestGetAssetContentTruncatedBlobFailsLoudly is acceptance criterion 4: a
// corrupted/truncated on-disk blob is never served, even though the row's
// own metadata (size_bytes) is intact.
func TestGetAssetContentTruncatedBlobFailsLoudly(t *testing.T) {
	root := t.TempDir()
	backend, err := assetstore.NewVolumeBackend(root)
	if err != nil {
		t.Fatalf("new volume backend: %v", err)
	}
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showObjectsTestDeps(svc, st)
	deps.Assets = st
	deps.AssetBackend = backend
	deps.AssetSettings = &fakeAssetSettingsSource{maxUploadBytes: 1 << 20}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	content := bytes.Repeat([]byte("y"), 1000)
	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body: %s", resp.StatusCode, body)
	}
	var uploaded v1AssetResponseForTest
	_ = json.Unmarshal(body, &uploaded)

	// Truncate the blob on disk directly, below the store's own recorded
	// size_bytes, simulating corruption the metadata row does not know
	// about.
	hash := uploaded.Asset.ContentHash[len("sha256:"):]
	blobPath := filepath.Join(root, hash[:2], hash)
	if err := os.WriteFile(blobPath, content[:500], 0o644); err != nil {
		t.Fatalf("truncate blob on disk: %v", err)
	}

	contentResp, contentBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID+"/content", auth)
	if contentResp.StatusCode == http.StatusOK {
		t.Fatalf("truncated blob served as 200 with %d bytes, want a failure", len(contentBody))
	}
}

// --- content: Range support and ETag ---

func TestGetAssetContentSupportsRange(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	content := []byte("0123456789")
	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body: %s", resp.StatusCode, body)
	}
	var uploaded v1AssetResponseForTest
	_ = json.Unmarshal(body, &uploaded)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+uploaded.Asset.ID+"/content", nil)
	req.Header.Set("Range", "bytes=2-4")
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	rangeResp, rangeBody := doRawRequest(t, api.Handler, req)
	if rangeResp.StatusCode != http.StatusPartialContent {
		t.Fatalf("Range request: status = %d, want 206; body: %s", rangeResp.StatusCode, rangeBody)
	}
	if string(rangeBody) != "234" {
		t.Errorf("Range bytes=2-4 = %q, want %q", rangeBody, "234")
	}
}

// --- listing ---

func TestListAssetsNodeFilterExcludesShowTargeted(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	nodeFields := validAssetFields()
	if resp, body := doAssetUpload(t, api.Handler, nodeFields, "node.fseq", []byte("node bytes"), auth); resp.StatusCode != http.StatusOK {
		t.Fatalf("node upload: status = %d, body: %s", resp.StatusCode, body)
	}

	showFields := validAssetFields()
	showFields["targetKind"] = "show"
	delete(showFields, "target")
	showFields["sequence"] = "opening-audio"
	if resp, body := doAssetUpload(t, api.Handler, showFields, "show.mp3", []byte("show bytes"), auth); resp.StatusCode != http.StatusOK {
		t.Fatalf("show upload: status = %d, body: %s", resp.StatusCode, body)
	}

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/assets?node=render-01", auth)
	var list struct {
		Assets []v1AssetForTest `json:"assets"`
	}
	if err := json.Unmarshal(listBody, &list); err != nil {
		t.Fatalf("decode list: %v\nbody: %s", err, listBody)
	}
	if len(list.Assets) != 1 {
		t.Fatalf("?node=render-01 returned %d assets, want exactly 1 (never the show-targeted one)", len(list.Assets))
	}
	if list.Assets[0].TargetKind != "node" {
		t.Errorf("?node= returned a %q-targeted asset, want only node-targeted", list.Assets[0].TargetKind)
	}
}

// --- authorization ---

func TestPostAssetUploadRequiresAssetWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	deps := assetsTestDeps(t, svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	// No principal with config:write has created "halloween-2026" here —
	// irrelevant, since this request must be rejected on scope before
	// validation ever runs.
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("x"), auth)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator (no asset:write) upload: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

// --- the timeout budget: two sides of one contract (spec section 6) ---

// TestPostAssetUploadSurvivesServerReadAndWriteTimeouts is this seam's own
// version of stream.go's TestStreamSurvivesServerWriteTimeout: a real
// *http.Server with a short ReadTimeout AND a short WriteTimeout, and an
// upload paced slower than both, proving handlePostAssetUpload's own two
// deadline extensions are what keep the connection alive.
//
// WriteTimeout is set here deliberately. An earlier version of this test
// left it at zero, so it passed against a handler that extended only the
// read deadline, while a real coordinator (which does set WriteTimeout)
// staged, hashed, registered and audited the upload and then failed the
// response flush, telling the operator a transport error for a request
// that had fully succeeded.
func TestPostAssetUploadSurvivesServerReadAndWriteTimeouts(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	ts := httptest.NewUnstartedServer(api.Handler)
	// Both shorter than this upload's own pacing below, deliberately. The
	// write deadline is armed when the headers are read, so it expires
	// mid-body just as the read deadline does.
	ts.Config.ReadTimeout = 100 * time.Millisecond
	ts.Config.WriteTimeout = 100 * time.Millisecond
	ts.Start()
	defer ts.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()
	go func() {
		defer func() { _ = pw.Close() }()
		fields := validAssetFields()
		for _, k := range []string{"show", "sequence", "mediaType", "targetKind", "target"} {
			time.Sleep(40 * time.Millisecond)
			_ = mw.WriteField(k, fields[k])
		}
		time.Sleep(40 * time.Millisecond)
		fw, err := mw.CreateFormFile("file", "a.fseq")
		if err != nil {
			return
		}
		_, _ = fw.Write([]byte("slow upload bytes"))
		time.Sleep(40 * time.Millisecond)
		_ = mw.Close()
	}()

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/assets", pr)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("upload paced past the server read and write timeouts failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an upload slower than ReadTimeout must still succeed); body: %s", resp.StatusCode, respBody)
	}
}

// slowAssetBackend wraps a real assetstore.Backend, delaying every reader
// it opens so a real *http.Server's WriteTimeout can be forced to matter
// without a multi-megabyte body or racing an OS socket buffer to fill
// (LESSONS.md: "construct [an overflow] structurally; do not race a
// kernel"). Put and Stat are unmodified — only what
// handleGetAssetContent reads from is slow.
type slowAssetBackend struct {
	assetstore.Backend
	perReadDelay time.Duration
	chunkBytes   int
}

func (b slowAssetBackend) Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error) {
	rc, size, err := b.Backend.Open(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return &slowReadSeekCloser{ReadSeekCloser: rc, perReadDelay: b.perReadDelay, chunkBytes: b.chunkBytes}, size, nil
}

// slowReadSeekCloser sleeps before every Read and caps how many bytes it
// returns, so a small body still forces many slow Read calls regardless of
// the caller's own buffer size.
type slowReadSeekCloser struct {
	io.ReadSeekCloser
	perReadDelay time.Duration
	chunkBytes   int
}

func (s *slowReadSeekCloser) Read(p []byte) (int, error) {
	time.Sleep(s.perReadDelay)
	if len(p) > s.chunkBytes {
		p = p[:s.chunkBytes]
	}
	return s.ReadSeekCloser.Read(p)
}

// TestGetAssetContentSurvivesServerWriteTimeout is
// TestPostAssetUploadSurvivesServerReadAndWriteTimeouts' download twin: a
// real *http.Server with a short WriteTimeout, and a body served slower
// than it via slowAssetBackend, proving handleGetAssetContent's own write
// deadline extension is what keeps the connection alive. Before that
// extension existed, http.ServeContent had only httpapi's 10s WriteTimeout
// for the ENTIRE body regardless of size, so any transfer slower than that
// dropped the connection mid-body.
func TestGetAssetContentSurvivesServerWriteTimeout(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	backend, err := assetstore.NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("new volume backend: %v", err)
	}
	deps := showObjectsTestDeps(svc, st)
	deps.Assets = st
	deps.AssetBackend = slowAssetBackend{Backend: backend, perReadDelay: 40 * time.Millisecond, chunkBytes: 4}
	deps.AssetSettings = &fakeAssetSettingsSource{maxUploadBytes: 1 << 20}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// 40 bytes at 4 bytes/read and 40ms/read paces the download to roughly
	// 400ms — well past the 100ms WriteTimeout below, deliberately.
	content := bytes.Repeat([]byte("z"), 40)
	uploadResp, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body: %s", uploadResp.StatusCode, uploadBody)
	}
	var uploaded v1AssetResponseForTest
	if err := json.Unmarshal(uploadBody, &uploaded); err != nil {
		t.Fatalf("decode upload response: %v\nbody: %s", err, uploadBody)
	}

	ts := httptest.NewUnstartedServer(api.Handler)
	ts.Config.WriteTimeout = 100 * time.Millisecond
	ts.Start()
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/assets/"+uploaded.Asset.ID+"/content", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	getResp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("download paced past the server write timeout failed: %v", err)
	}
	defer func() { _ = getResp.Body.Close() }()
	got, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a download slower than WriteTimeout must still succeed); body: %s", getResp.StatusCode, got)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("body = %d bytes, want %d bytes matching the uploaded content", len(got), len(content))
	}
}

// countingNudger records how many times an out-of-band sync was requested.
type countingNudger struct{ n int }

func (c *countingNudger) Nudge() { c.n++ }

// The nudge call sites are the whole point of AssetSyncNudger existing: the
// method shipped once with a no-op default and no caller at all, so an
// upload or an activation waited out a full sync interval. These assert the
// call, not the field.
func TestAssetSyncIsNudgedOnUploadAndOnActivation(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	nudger := &countingNudger{}
	deps := assetsTestDeps(t, svc, st)
	deps.AssetSyncNudger = nudger
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	if resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("bytes"), auth); resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body: %s", resp.StatusCode, body)
	}
	if nudger.n != 1 {
		t.Fatalf("nudges after upload = %d, want 1: a new asset must not wait out a sync interval", nudger.n)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/show.active",
		bytes.NewReader([]byte(`{"show":"halloween-2026"}`)))
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate: status = %d, body: %s", resp.StatusCode, body)
	}
	if nudger.n != 2 {
		t.Fatalf("nudges after activation = %d, want 2: every node's expected set just changed", nudger.n)
	}
}

// --- rollback (ADR-028 decision 10) ---

// TestPostAssetUploadRollback proves upload A, B, then A again reports
// rolledBack=true as its own field, audits it, nudges sync, and the
// manifest expects A again.
func TestPostAssetUploadRollback(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	nudger := &countingNudger{}
	deps := assetsTestDeps(t, svc, st)
	deps.AssetSyncNudger = nudger
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	bytesA := []byte("version A")
	bytesB := []byte("version B, different bytes")

	respA1, bodyA1 := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", bytesA, auth)
	if respA1.StatusCode != http.StatusOK {
		t.Fatalf("upload A: status = %d, body: %s", respA1.StatusCode, bodyA1)
	}
	var firstA v1AssetResponseForTest
	_ = json.Unmarshal(bodyA1, &firstA)

	respB, bodyB := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", bytesB, auth)
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("upload B: status = %d, body: %s", respB.StatusCode, bodyB)
	}

	if nudger.n != 2 {
		t.Fatalf("nudges before rollback = %d, want 2 (one per upload so far)", nudger.n)
	}

	// Re-upload A: the rollback.
	respRollback, bodyRollback := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", bytesA, auth)
	if respRollback.StatusCode != http.StatusOK {
		t.Fatalf("rollback upload: status = %d, body: %s", respRollback.StatusCode, bodyRollback)
	}

	var rollbackResp struct {
		ServerTime string         `json:"serverTime"`
		Asset      v1AssetForTest `json:"asset"`
		RolledBack bool           `json:"rolledBack"`
	}
	if err := json.Unmarshal(bodyRollback, &rollbackResp); err != nil {
		t.Fatalf("decode rollback response: %v\nbody: %s", err, bodyRollback)
	}
	if !rollbackResp.RolledBack {
		t.Errorf("rolledBack = false, want true: the response must say a rollback occurred as its own field")
	}
	if rollbackResp.Asset.ID != firstA.Asset.ID {
		t.Errorf("rollback asset id = %q, want the original A id %q (no new row)", rollbackResp.Asset.ID, firstA.Asset.ID)
	}
	if !rollbackResp.Asset.Current {
		t.Error("rollback asset current = false, want true")
	}

	if nudger.n != 3 {
		t.Fatalf("nudges after rollback = %d, want 3: the manifest's expectation just changed", nudger.n)
	}

	// The store holds exactly two rows: A current, B superseded.
	recs, err := st.ListAssets(context.Background(), store.AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("row count after rollback = %d, want 2 (no third row)", len(recs))
	}

	// A distinct, auditable action was recorded for the rollback.
	audit, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	foundRollback := false
	for _, e := range audit {
		if e.Action == "asset.rollback" && e.Target == rollbackResp.Asset.ID {
			foundRollback = true

			var params struct {
				FromAssetID string `json:"fromAssetId"`
				ToAssetID   string `json:"toAssetId"`
			}
			if err := json.Unmarshal([]byte(e.ParamsJSON), &params); err != nil {
				t.Fatalf("decode rollback audit params: %v\nparams: %s", err, e.ParamsJSON)
			}
			// B was current when A's rollback ran, so B is what got
			// displaced — the "half the event" blocker 1 asked for.
			if params.ToAssetID != rollbackResp.Asset.ID {
				t.Errorf("audit toAssetId = %q, want the restored asset %q", params.ToAssetID, rollbackResp.Asset.ID)
			}
			if params.FromAssetID == "" || params.FromAssetID == params.ToAssetID {
				t.Errorf("audit fromAssetId = %q, want the displaced (B) asset id, distinct from toAssetId %q", params.FromAssetID, params.ToAssetID)
			}
		}
		if e.Action == "asset.rollback" && e.Kind != string(identity.AuditAdmin) {
			t.Errorf("asset.rollback audit kind = %q, want %q", e.Kind, identity.AuditAdmin)
		}
	}
	if !foundRollback {
		t.Errorf("no asset.rollback audit entry found for %s among %+v", rollbackResp.Asset.ID, audit)
	}

	// The manifest expects A's content hash again, through the existing
	// mechanism — no second sync path.
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/show.active",
		bytes.NewReader([]byte(`{"show":"halloween-2026"}`)))
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("activate show: status = %d, body: %s", resp.StatusCode, body)
	}

	current, err := st.ListCurrentAssetsForTarget(context.Background(), "halloween-2026", store.AssetTargetKindNode, "render-01")
	if err != nil {
		t.Fatalf("list current for target: %v", err)
	}
	if len(current) != 1 || current[0].ContentHash != contentHashOf(bytesA) {
		t.Fatalf("current assets for render-01 = %+v, want exactly A's content hash %q", current, contentHashOf(bytesA))
	}

	// A plain GET never reports a rollback that did not just happen.
	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+rollbackResp.Asset.ID, auth)
	var getResp struct {
		RolledBack bool `json:"rolledBack"`
	}
	if err := json.Unmarshal(getBody, &getResp); err != nil {
		t.Fatalf("decode get response: %v\nbody: %s", err, getBody)
	}
	if getResp.RolledBack {
		t.Error("GET /assets/{id} rolledBack = true, want false always")
	}
}

// clockAdvancingAssetBackend wraps a real assetstore.Backend and advances clock by
// delta inside Put, after the bytes are staged — modeling review blocker
// 2: a real upload's Put can take a long time, and the audit entry's
// timestamp must reflect when the write actually ran, not when the
// request started.
type clockAdvancingAssetBackend struct {
	assetstore.Backend
	clock *syncClock
	delta time.Duration
}

func (b *clockAdvancingAssetBackend) Put(ctx context.Context, r io.Reader, limit int64) (assetstore.Blob, error) {
	blob, err := b.Backend.Put(ctx, r, limit)
	b.clock.advance(b.delta)
	return blob, err
}

// TestPostAssetUploadAuditTimestampReflectsWriteNotUploadStart proves
// review blocker 2's fix: the audit entry's Timestamp is read inside the
// transaction, after the (possibly slow) upload stream, not once at
// request start. Before the fix this test's audit entry would carry
// requestStart rather than a time at or after it.
func TestPostAssetUploadAuditTimestampReflectsWriteNotUploadStart(t *testing.T) {
	requestStart := testNow
	clock := &syncClock{t: requestStart}

	svc, st, _ := newTestIdentityServiceWithStore(t, clock.now)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	backend, err := assetstore.NewVolumeBackend(t.TempDir())
	if err != nil {
		t.Fatalf("new volume backend: %v", err)
	}
	deps := assetsTestDeps(t, svc, st)
	deps.AssetBackend = &clockAdvancingAssetBackend{Backend: backend, clock: clock, delta: time.Hour}
	api := New(deps, Options{Clock: clock.now, Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("version A"), auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: status = %d, body: %s", resp.StatusCode, body)
	}

	writeTime := clock.now() // advanced by Put; this is what the write actually happened at
	if !writeTime.After(requestStart) {
		t.Fatalf("test setup: writeTime %v did not advance past requestStart %v", writeTime, requestStart)
	}

	audit, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	found := false
	for _, e := range audit {
		if e.Action != "asset.upload" {
			continue
		}
		found = true
		if !e.RecordedAt.Equal(writeTime) {
			t.Errorf("audit RecordedAt = %v, want %v (the write-time clock read, not requestStart %v)",
				e.RecordedAt, writeTime, requestStart)
		}
	}
	if !found {
		t.Fatalf("no asset.upload audit entry found among %+v", audit)
	}
}

// TestPostAssetUploadIdenticalCurrentBytesIsNotARollback proves the true
// idempotent case (re-uploading bytes that are STILL current) is NOT
// reported as a rollback and writes no new audit entry.
func TestPostAssetUploadIdenticalCurrentBytesIsNotARollback(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)

	content := []byte("identical bytes")
	_, _ = doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)

	before, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit before: %v", err)
	}

	resp, body := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", content, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second upload: status = %d, body: %s", resp.StatusCode, body)
	}
	var got struct {
		RolledBack bool `json:"rolledBack"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if got.RolledBack {
		t.Error("rolledBack = true for a re-upload of identical CURRENT bytes, want false")
	}

	after, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit after: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("audit entry count changed from %d to %d on a true no-op re-upload", len(before), len(after))
	}
}
