package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-2a's own test suite: the Resolume
// composition upload API and its storage (ADR-032). It follows
// config_test.go's harness (newTestIdentityServiceWithStore,
// installFailAuditTrigger, configTestDeps) rather than inventing a
// parallel one — this seam reuses the SAME config_objects/config_revisions
// storage and the SAME identity.Service.AuditedWrite transaction pattern
// that suite already exercises for fpp.endpoints, so the harness for
// proving it is identical too.

// resolumeCompositionTestdataPath resolves a fixture under
// pkg/resolumecomp/testdata — this package's own tests run with a working
// directory of internal/coordinator/api, so three "up" levels reach the
// repository root, matching openapi_test.go's loadOpenAPIDocument's
// identical walk. Reusing pkg/resolumecomp's own SYNTHETIC fixtures
// (rather than inventing a duplicate one here) is deliberate: that
// package's testdata/README.md explains why no real operator composition
// file may ever enter this repository, and duplicating a second synthetic
// fixture here would only be a second place that rule could be forgotten.
func resolumeCompositionTestdataPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "pkg", "resolumecomp", "testdata", name)
}

func mustReadResolumeCompositionTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(resolumeCompositionTestdataPath(t, name))
	if err != nil {
		t.Fatalf("reading pkg/resolumecomp/testdata/%s: %v", name, err)
	}
	return data
}

// buildResolumeCompositionMultipartBody builds a real multipart/form-data
// body carrying content as a single file part named "file" — mirroring
// showmeshctl's own buildCompositionMultipartBody (cmd_resolume_composition.go),
// the client this endpoint's contract was built against.
func buildResolumeCompositionMultipartBody(t *testing.T, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("writing multipart file part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("closing multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// doResolumeCompositionUpload issues a real multipart POST against h and
// returns the raw response.
func doResolumeCompositionUpload(t *testing.T, h http.Handler, filename string, content []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	body, contentType := buildResolumeCompositionMultipartBody(t, filename, content)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/resolume/composition", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return resp, respBody
}

// resolumeCompositionAdminAPI builds a real *API with a real store and
// identity.Service (config_test.go's newTestIdentityServiceWithStore),
// and an admin credential (config:write is admin-only, matching
// fpp.endpoints' own test setup). Returns the API, the store, and a
// header map ready to authenticate as that admin.
func resolumeCompositionAdminAPI(t *testing.T) (*API, map[string]string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	return api, map[string]string{"Authorization": "Bearer " + token}
}

// --- 1: a valid upload creates a revision, activates it, writes one
// audit entry, and returns the summary. ---

func TestResolumeCompositionUploadCreatesRevisionActivatesAndAudits(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	before, err := svc.ListAudit(t.Context(), 0, 1000)
	if err != nil {
		t.Fatalf("ListAudit before upload: %v", err)
	}

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	resp, body := doResolumeCompositionUpload(t, api.Handler, "Holiday Test Show.avc", content,
		map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	if m["serverTime"] == nil || m["serverTime"] == "" {
		t.Errorf("serverTime missing from response: %s", body)
	}
	if m["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", m["revision"])
	}
	if m["activatedAt"] == nil || m["activatedAt"] == "" {
		t.Errorf("activatedAt missing from response: %s", body)
	}
	comp, ok := m["composition"].(map[string]any)
	if !ok {
		t.Fatalf("composition member missing or not an object: %s", body)
	}
	if comp["name"] != "Holiday Test Show" {
		t.Errorf("composition.name = %v, want %q", comp["name"], "Holiday Test Show")
	}
	if comp["sourceFilename"] != "Holiday Test Show.avc" {
		t.Errorf("composition.sourceFilename = %v, want %q", comp["sourceFilename"], "Holiday Test Show.avc")
	}
	hash, _ := comp["contentHash"].(string)
	if hash == "" || hash[:7] != "sha256:" {
		t.Errorf("composition.contentHash = %q, want an sha256: prefixed hash", hash)
	}
	if comp["clipCount"] != float64(3) {
		t.Errorf("composition.clipCount = %v, want 3", comp["clipCount"])
	}
	if comp["persistentClipCount"] != float64(2) {
		t.Errorf("composition.persistentClipCount = %v, want 2", comp["persistentClipCount"])
	}
	if comp["layerCount"] != float64(3) {
		t.Errorf("composition.layerCount = %v, want 3", comp["layerCount"])
	}
	decks, _ := comp["decks"].([]any)
	if len(decks) != 2 {
		t.Fatalf("composition.decks = %v, want 2 decks", decks)
	}

	revs, err := st.ListConfigRevisions(t.Context(), resolumeCompositionConfigKind, "resolume")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("len(ListConfigRevisions) = %d, want 1", len(revs))
	}
	obj, err := st.GetConfigObject(t.Context(), resolumeCompositionConfigKind, "resolume")
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}

	after, err := svc.ListAudit(t.Context(), 0, 1000)
	if err != nil {
		t.Fatalf("ListAudit after upload: %v", err)
	}
	if len(after) != len(before)+1 {
		t.Fatalf("audit entries: before=%d after=%d, want exactly one new entry", len(before), len(after))
	}
}

// --- 1b: the stored composition's object id does not move when
// SHOWMESH_RESOLUME_ID (Dependencies.ResolumeID) changes. ---

// TestResolumeCompositionObjectIDIsIndependentOfResolumeID is review
// finding F: an earlier version of this handler derived the stored
// composition's config_objects id from Dependencies.ResolumeID (which
// plumbs SHOWMESH_RESOLUME_ID, the LIVE Resolume collector's own
// registration key), so renaming that env var for a reason that has
// nothing to do with the composition subsystem — e.g. disambiguating a
// second live Resolume instance — would silently orphan every stored
// revision: GET would report "nothing uploaded yet" while the rows sat
// intact under the old id. This test uploads a composition with
// Dependencies.ResolumeID set to a non-default value, changes it (as if
// the operator had edited SHOWMESH_RESOLUME_ID), and proves the SAME
// composition is still readable — i.e. the object id used to store and
// retrieve it never depended on that field at all.
func TestResolumeCompositionObjectIDIsIndependentOfResolumeID(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	headers := map[string]string{"Authorization": "Bearer " + token}

	deps := configTestDeps(svc, st)
	deps.ResolumeID = "warehouse-arena"
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "show.avc", content, headers)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	// Simulate the operator renaming SHOWMESH_RESOLUME_ID: a fresh API
	// instance over the SAME store, with Dependencies.ResolumeID now
	// pointing somewhere else entirely (and even the live collector
	// disabled, the "" a real coordinator boots with when
	// SHOWMESH_RESOLUME_URL is unset).
	deps2 := configTestDeps(svc, st)
	deps2.ResolumeID = ""
	api2 := New(deps2, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	getResp, getBody := doRequest(t, api2.Handler, "GET", "/api/v1/config/resolume/composition", headers)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET after ResolumeID changed: status = %d, want 200 (the stored composition must still be found); body: %s",
			getResp.StatusCode, getBody)
	}
	m := decodeMap(t, getBody)
	comp, ok := m["composition"].(map[string]any)
	if !ok || comp["name"] != "Holiday Test Show" {
		t.Fatalf("composition after ResolumeID changed = %v, want the same uploaded composition still readable", m["composition"])
	}
}

// --- 2: a malformed file returns a 400-class problem and persists
// nothing: zero revisions, no config object, no audit entry. ---

func TestResolumeCompositionUploadMalformedFilePersistsNothing(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	before, err := svc.ListAudit(t.Context(), 0, 1000)
	if err != nil {
		t.Fatalf("ListAudit before upload: %v", err)
	}

	content := mustReadResolumeCompositionTestdata(t, "not-xml.txt")
	resp, body := doResolumeCompositionUpload(t, api.Handler, "not-xml.txt", content,
		map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("status = %d, want a 400-class status; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("problem type = %v, want %q", m["type"], ProblemTypeInvalidParameter)
	}
	detail, _ := m["detail"].(string)
	if detail == "" {
		t.Error("problem detail is empty; an operator needs to know what was wrong")
	}

	objs, err := st.ListConfigObjects(t.Context(), resolumeCompositionConfigKind)
	if err != nil {
		t.Fatalf("ListConfigObjects: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("ListConfigObjects = %+v, want none: a rejected file must create no config object", objs)
	}
	revs, err := st.ListConfigRevisions(t.Context(), resolumeCompositionConfigKind, "resolume")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 0 {
		t.Errorf("ListConfigRevisions = %+v, want none: a rejected file must create no revision", revs)
	}
	after, err := svc.ListAudit(t.Context(), 0, 1000)
	if err != nil {
		t.Fatalf("ListAudit after upload: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("audit entries: before=%d after=%d, want unchanged: a rejected file must write no audit entry", len(before), len(after))
	}
}

// TestResolumeCompositionUploadRejectionDetailNamesNoGoPackage is the
// regression guard for the defect the owner found by loading the real
// Operator UI: this endpoint's problem detail used to be built as
// `fmt.Sprintf("... could not be parsed as a Resolume composition: %v",
// err)`, and err is one of pkg/resolumecomp's own sentinel errors, whose
// text is package-qualified by that package's own convention — an
// operator was shown "resolumecomp: root element is not <Composition>:
// found <NotAComposition>" verbatim. This exercises all five of
// pkg/resolumecomp's documented parse-failure sentinels (the same five
// TestParse_MalformedInputs_ReturnNoPartialModel in
// pkg/resolumecomp/resolumecomp_test.go exercises against the parser
// directly) through the real HTTP handler, and checks two things for
// each: the detail names what was actually wrong with the file (so this
// fix has not merely made every rejection say the same generic sentence),
// and the detail carries no Go package name anywhere in it.
func TestResolumeCompositionUploadRejectionDetailNamesNoGoPackage(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	headers := map[string]string{"Authorization": "Bearer " + token}
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	cases := []struct {
		name          string
		file          string
		wantSubstring string
	}{
		{"not XML", "not-xml.txt", "not a valid XML file"},
		{"wrong root element", "wrong-root.avc", "root element is not <Composition>"},
		{"no composition info", "missing-compositioninfo.avc", "no composition information"},
		{"malformed layer index", "bad-layerindex.avc", "missing or invalid position"},
		{"deck with no unique id", "deck-missing-id.avc", "no unique ID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := mustReadResolumeCompositionTestdata(t, tc.file)
			resp, body := doResolumeCompositionUpload(t, api.Handler, tc.file, content, headers)
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Fatalf("status = %d, want a 400-class status; body: %s", resp.StatusCode, body)
			}
			m := decodeMap(t, body)
			detail, _ := m["detail"].(string)
			if detail == "" {
				t.Fatal("problem detail is empty; an operator needs to know what was wrong")
			}
			if !strings.Contains(detail, tc.wantSubstring) {
				t.Errorf("detail = %q, want it to contain %q (the reason this specific file was rejected)", detail, tc.wantSubstring)
			}
			// The literal defect: this coordinator's own internal Go
			// package name, which means nothing to an operator and never
			// should have reached them.
			if strings.Contains(detail, "resolumecomp") {
				t.Errorf("detail = %q, carries the Go package name %q", detail, "resolumecomp")
			}
			// Broader than the one package this test happens to exercise:
			// no repo-relative path segment of any kind belongs in an
			// operator-facing string, matching the standing rule this
			// package's own copy guard (fppcommand_copy_guard_test.go)
			// enforces across this seam's other files.
			if strings.Contains(detail, "/") {
				t.Errorf("detail = %q, carries a path-shaped substring", detail)
			}
		})
	}
}

// --- 2b: multipart part hygiene (review finding H). ---

// TestResolumeCompositionUploadRefusesSecondFilePart proves a request
// carrying TWO parts named "file" is refused with a 400-class problem and
// persists nothing — before this fix, the handler silently stored the
// FIRST part's bytes and discarded the second with no warning at all,
// which contradicts api/openapi.yaml's own description ("exactly one file
// part named file").
func TestResolumeCompositionUploadRefusesSecondFilePart(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	headers := map[string]string{"Authorization": "Bearer " + token}
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	first := mustReadResolumeCompositionTestdata(t, "complete.avc")
	second := mustReadResolumeCompositionTestdata(t, "missing-compositioninfo.avc")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	p1, err := mw.CreateFormFile("file", "first.avc")
	if err != nil {
		t.Fatalf("CreateFormFile 1: %v", err)
	}
	if _, err := p1.Write(first); err != nil {
		t.Fatalf("write part 1: %v", err)
	}
	p2, err := mw.CreateFormFile("file", "second.avc")
	if err != nil {
		t.Fatalf("CreateFormFile 2: %v", err)
	}
	if _, err := p2.Write(second); err != nil {
		t.Fatalf("write part 2: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/resolume/composition", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("status = %d, want a 400-class status; body: %s", resp.StatusCode, respBody)
	}

	objs, err := st.ListConfigObjects(t.Context(), resolumeCompositionConfigKind)
	if err != nil {
		t.Fatalf("ListConfigObjects: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("ListConfigObjects = %+v, want none: a request with two file parts must persist nothing", objs)
	}
}

// TestResolumeCompositionUploadRefusesFieldNamedFileWithNoFilename proves a
// plain multipart FORM FIELD named "file" (no filename — i.e. not
// [multipart.Writer.CreateFormFile], but the plain-field form
// [multipart.Writer.CreateFormField] produces) is refused rather than
// stored with an empty sourceFilename, which the UI would otherwise render
// as an unlabeled size with nothing before it. The field's VALUE is a
// perfectly valid composition document (complete.avc's own bytes) so the
// refusal can only be attributed to the missing filename, never to a parse
// failure the malformed-content tests already cover — a weaker version of
// this test using arbitrary non-XML text would still fail with a 400 even
// if the missing-filename guard were absent entirely, having accidentally
// tripped resolumecomp.ErrNotXML instead.
func TestResolumeCompositionUploadRefusesFieldNamedFileWithNoFilename(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	headers := map[string]string{"Authorization": "Bearer " + token}
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormField("file")
	if err != nil {
		t.Fatalf("CreateFormField: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/resolume/composition", bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("status = %d, want a 400-class status; body: %s", resp.StatusCode, respBody)
	}

	objs, err := st.ListConfigObjects(t.Context(), resolumeCompositionConfigKind)
	if err != nil {
		t.Fatalf("ListConfigObjects: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("ListConfigObjects = %+v, want none: a fieldless \"file\" part must not be stored as a composition", objs)
	}
}

// --- 3: a body over the limit is refused without being buffered
// whole. ---

// countingReader wraps r and records how many bytes were actually pulled
// from it, so a test can prove the server stopped reading well short of
// the full body rather than buffering it whole before rejecting it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestResolumeCompositionUploadOverLimitRefusedWithoutFullBuffering(t *testing.T) {
	api, headers := resolumeCompositionAdminAPI(t)

	oversize := bytes.Repeat([]byte("A"), maxResolumeCompositionUploadBytes*2)
	body, contentType := buildResolumeCompositionMultipartBody(t, "huge.avc", oversize)
	fullLen := body.Len()

	cr := &countingReader{r: bytes.NewReader(body.Bytes())}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/resolume/composition", cr)
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("problem type = %v, want %q", m["type"], ProblemTypeResolumeCompositionTooLarge)
	}

	// The load-bearing assertion: the server must not have read anywhere
	// near the full oversized body. http.MaxBytesReader stops erroring
	// once the handler's own reads cross maxResolumeCompositionUploadBytes,
	// so cr.n should land close to that bound, never anywhere near fullLen
	// (roughly 2x the bound plus multipart framing).
	if cr.n >= int64(fullLen) {
		t.Errorf("server read %d bytes, the ENTIRE oversized body (%d bytes); it must stop well short of the full body", cr.n, fullLen)
	}
	const slack = 1 << 20 // 1 MiB of multipart framing/boundary overhead
	if cr.n > maxResolumeCompositionUploadBytes+slack {
		t.Errorf("server read %d bytes, want at most ~%d (the upload bound plus framing slack)", cr.n, maxResolumeCompositionUploadBytes+slack)
	}
}

// --- 4: no credential, and a credential without config:write, are each
// refused, and persist nothing. ---

func TestResolumeCompositionUploadAuthAndScopePersistNothing(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := doResolumeCompositionUpload(t, api.Handler, "x.avc", content, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("viewer forbidden naming config:write", func(t *testing.T) {
		resp, body := doResolumeCompositionUpload(t, api.Handler, "x.avc", content,
			map[string]string{"Authorization": "Bearer " + viewerToken})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
		if !bytes.Contains(body, []byte("config:write")) {
			t.Errorf("body = %s, want it to name the missing scope config:write", body)
		}
	})

	objs, err := st.ListConfigObjects(t.Context(), resolumeCompositionConfigKind)
	if err != nil {
		t.Fatalf("ListConfigObjects: %v", err)
	}
	if len(objs) != 0 {
		t.Errorf("ListConfigObjects = %+v, want none: an unauthorized/forbidden request must persist nothing", objs)
	}
	after, err := svc.ListAudit(t.Context(), 0, 1000)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("audit entries = %d, want 0: an unauthorized/forbidden request must write no config-write audit entry", len(after))
	}
}

// --- 5: GET with nothing stored matches config get's existing empty
// answer. ---

func TestResolumeCompositionGetNothingStoredMatchesConfigGetEmptyAnswer(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// The reference answer: GET /config/fpp.endpoints with nothing
	// configured yet.
	fppResp, fppBody := doRequest(t, api.Handler, "GET", "/api/v1/config/fpp.endpoints",
		map[string]string{"Authorization": "Bearer " + token})
	fppMap := decodeMap(t, fppBody)

	// This endpoint's own answer, authenticated as the SAME admin: GET
	// /config/resolume/composition is gated behind config:write exactly
	// like GET /config/fpp.endpoints (see this route's registration
	// comment in api.go), so it needs the identical credential to reach
	// its own empty-store answer at all.
	compResp, compBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition",
		map[string]string{"Authorization": "Bearer " + token})
	compMap := decodeMap(t, compBody)

	if compResp.StatusCode != fppResp.StatusCode {
		t.Errorf("status = %d, want %d (GET /config/fpp.endpoints' own empty-store status)", compResp.StatusCode, fppResp.StatusCode)
	}
	if compResp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", compResp.StatusCode)
	}
	if compMap["type"] != fppMap["type"] {
		t.Errorf("problem type = %v, want %v (the same class GET /config/fpp.endpoints uses for its own empty-store case)", compMap["type"], fppMap["type"])
	}
	if compMap["type"] != ProblemTypeResourceNotFound {
		t.Errorf("problem type = %v, want %q", compMap["type"], ProblemTypeResourceNotFound)
	}
}

// --- 5b: GET is gated behind config:write, matching GET
// /config/fpp.endpoints exactly, and is NEVER open even though it is not
// itself a write. ---

// TestResolumeCompositionGetRequiresConfigWriteScope mirrors
// TestGetFPPEndpointsConfigRequiresConfigWriteScope (config_test.go)
// exactly: an earlier version of this route gated GET behind fpp:read
// instead, a different vendor integration's scope entirely, and would have
// let an FPP-scoped credential read a Resolume composition it was never
// granted access to.
func TestResolumeCompositionGetRequiresConfigWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	viewerToken := mustIssueToken(t, svc, viewer.ID)
	adminToken := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("unauthenticated", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
		}
	})
	t.Run("viewer forbidden", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition",
			map[string]string{"Authorization": "Bearer " + viewerToken})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
	})
	t.Run("admin allowed through the scope gate", func(t *testing.T) {
		resp, body := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition",
			map[string]string{"Authorization": "Bearer " + adminToken})
		// admin passes the scope gate; nothing has been uploaded in this
		// subtest's fresh store, so it 404s — still proves the gate was
		// reached and passed rather than short-circuited by some other
		// refusal.
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (scope gate passed, no composition uploaded yet); body: %s", resp.StatusCode, body)
		}
	})
}

// --- 6: GET after an upload returns every clip with its deck, and
// persistent clips without one. ---

func TestResolumeCompositionGetAfterUploadReturnsClipsWithDecks(t *testing.T) {
	api, headers := resolumeCompositionAdminAPI(t)

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "show.avc", content, headers)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition", headers)
	m := decodeMap(t, getBody)

	clips, ok := m["clips"].([]any)
	if !ok || len(clips) != 3 {
		t.Fatalf("clips = %v, want 3 deck clips", m["clips"])
	}
	for _, raw := range clips {
		clip, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("clip element is not an object: %v", raw)
		}
		deckID, ok := clip["deckId"].(string)
		if !ok || deckID == "" {
			t.Errorf("deck clip %v has no non-empty deckId", clip)
		}
	}

	persistent, ok := m["persistentClips"].([]any)
	if !ok || len(persistent) != 2 {
		t.Fatalf("persistentClips = %v, want 2", m["persistentClips"])
	}
	for _, raw := range persistent {
		clip, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("persistent clip element is not an object: %v", raw)
		}
		if _, present := clip["deckId"]; present {
			t.Errorf("persistent clip %v carries a deckId; persistent clips must never carry one", clip)
		}
	}

	decks, ok := m["decks"].([]any)
	if !ok || len(decks) != 2 {
		t.Fatalf("decks = %v, want 2", m["decks"])
	}
}

// TestResolumeCompositionGetLayersCarryNamesOrAGeneratedLabel is ADR-037
// decision 7 (the parser reads layer names and the API shows them) and
// decision 4 (an unnamed layer gets a stable generated label, marked as
// generated, never a blank cell). complete.avc's first layer is named
// "Peak Only" via its own Params block; its second layer has no Params
// block at all and must come back with a non-blank, clearly-generated
// name instead. This fails if a layer's name is dropped on the way to the
// wire, and it fails if the unnamed layer renders as an empty string.
func TestResolumeCompositionGetLayersCarryNamesOrAGeneratedLabel(t *testing.T) {
	api, headers := resolumeCompositionAdminAPI(t)

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "show.avc", content, headers)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition", headers)
	m := decodeMap(t, getBody)

	layers, ok := m["layers"].([]any)
	if !ok || len(layers) != 3 {
		t.Fatalf("layers = %v, want 3", m["layers"])
	}

	first, ok := layers[0].(map[string]any)
	if !ok {
		t.Fatalf("layers[0] is not an object: %v", layers[0])
	}
	if name, _ := first["name"].(string); name != "Peak Only" {
		t.Errorf("layers[0].name = %v, want %q (the operator's own name must survive to the wire)", first["name"], "Peak Only")
	}
	if generated, _ := first["nameGenerated"].(bool); generated {
		t.Errorf("layers[0].nameGenerated = %v, want false (this layer has an authored name)", first["nameGenerated"])
	}

	second, ok := layers[1].(map[string]any)
	if !ok {
		t.Fatalf("layers[1] is not an object: %v", layers[1])
	}
	name, hasName := second["name"].(string)
	if !hasName || name == "" {
		t.Errorf("layers[1].name = %v, want a non-blank generated label (this layer has no Params block in the fixture)", second["name"])
	}
	if generated, _ := second["nameGenerated"].(bool); !generated {
		t.Errorf("layers[1].nameGenerated = %v, want true (this layer's name was invented, not authored)", second["nameGenerated"])
	}
}

// --- 7: a second upload creates a second revision rather than mutating
// the first. ---

func TestResolumeCompositionSecondUploadCreatesSecondRevision(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(configTestDeps(svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	headers := map[string]string{"Authorization": "Bearer " + token}

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")

	first, firstBody := doResolumeCompositionUpload(t, api.Handler, "first.avc", content, headers)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d, want 200; body: %s", first.StatusCode, firstBody)
	}
	firstMap := decodeMap(t, firstBody)
	if firstMap["revision"] != float64(1) {
		t.Fatalf("first upload revision = %v, want 1", firstMap["revision"])
	}

	second, secondBody := doResolumeCompositionUpload(t, api.Handler, "second.avc", content, headers)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second upload status = %d, want 200; body: %s", second.StatusCode, secondBody)
	}
	secondMap := decodeMap(t, secondBody)
	if secondMap["revision"] != float64(2) {
		t.Fatalf("second upload revision = %v, want 2", secondMap["revision"])
	}
	comp, _ := secondMap["composition"].(map[string]any)
	if comp["sourceFilename"] != "second.avc" {
		t.Errorf("composition.sourceFilename = %v, want %q", comp["sourceFilename"], "second.avc")
	}

	revs, err := st.ListConfigRevisions(t.Context(), resolumeCompositionConfigKind, "resolume")
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("len(ListConfigRevisions) = %d, want 2 (the first revision must survive immutably, not be overwritten)", len(revs))
	}
	if revs[0].Revision != 1 || revs[0].PayloadJSON == revs[1].PayloadJSON {
		t.Errorf("revisions = %+v, want two distinct, immutable revisions", revs)
	}

	obj, err := st.GetConfigObject(t.Context(), resolumeCompositionConfigKind, "resolume")
	if err != nil {
		t.Fatalf("GetConfigObject: %v", err)
	}
	if obj.CurrentRevision != 2 {
		t.Errorf("CurrentRevision = %d, want 2 (the second, most recent upload active)", obj.CurrentRevision)
	}
}

// TestResolumeCompositionPerDeckClipCountsSumToTotal is review finding C's
// counts half: mapResolumeCompositionSummary buckets clips by DeckID into
// each deck's own ClipCount, and — before pkg/resolumecomp rejected a
// <Deck> with no uniqueId (ErrMissingDeckID) — a deck clip could carry an
// empty DeckID that bucketed under an id no deck in the response ever
// rendered, so the visible per-deck counts silently summed to LESS than
// composition.clipCount. This proves the invariant holds for a real parsed
// composition: every deck's ClipCount, summed, equals composition.clipCount
// exactly.
func TestResolumeCompositionPerDeckClipCountsSumToTotal(t *testing.T) {
	api, headers := resolumeCompositionAdminAPI(t)

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "show.avc", content, headers)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	m := decodeMap(t, uploadBody)
	comp, ok := m["composition"].(map[string]any)
	if !ok {
		t.Fatalf("composition member missing or not an object: %s", uploadBody)
	}
	totalClipCount, ok := comp["clipCount"].(float64)
	if !ok {
		t.Fatalf("composition.clipCount missing or not a number: %v", comp["clipCount"])
	}
	decks, ok := comp["decks"].([]any)
	if !ok {
		t.Fatalf("composition.decks missing or not an array: %v", comp["decks"])
	}

	var summedFromDecks float64
	for _, raw := range decks {
		deck, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("deck element is not an object: %v", raw)
		}
		clipCount, ok := deck["clipCount"].(float64)
		if !ok {
			t.Fatalf("deck.clipCount missing or not a number: %v", deck["clipCount"])
		}
		summedFromDecks += clipCount
	}

	if summedFromDecks != totalClipCount {
		t.Errorf("sum of per-deck clipCount = %v, want composition.clipCount = %v (every clip must bucket under a deck the response actually renders)",
			summedFromDecks, totalClipCount)
	}
	if totalClipCount != 3 {
		t.Fatalf("composition.clipCount = %v, want 3 (sanity check on the fixture itself)", totalClipCount)
	}
}

// TestOpenAPIResolumeCompositionResponsesMatchRealResponses is this seam's
// own conformance test, following TestOpenAPIConfigResponsesMatchRealResponses's
// exact pattern (openapi_test.go): drives POST then GET through a real
// [API] and validates each real response body against
// api/openapi.yaml's schema for it, in both directions.
func TestOpenAPIResolumeCompositionResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, headers := resolumeCompositionAdminAPI(t)

	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "show.avc", content, headers)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}
	assertMatchesSchema(t, c, "ResolumeCompositionUploadResponse", uploadBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume/composition", headers)
	assertMatchesSchema(t, c, "ResolumeCompositionResponse", getBody)
}

// TestOpenAPIResolumeCompositionProblemsMatchSchema checks the two problem
// classes this seam adds/uses beyond what TestOpenAPIProblemSchemaMatchesEveryClass
// (openapi_test.go) already covers: the malformed-file 400 (already an
// existing class, invalid-parameter, but never previously produced from
// THIS handler) and the oversized-body 413 (a genuinely new class, this
// seam's own ProblemTypeResolumeCompositionTooLarge).
func TestOpenAPIResolumeCompositionProblemsMatchSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, headers := resolumeCompositionAdminAPI(t)

	_, malformedBody := doResolumeCompositionUpload(t, api.Handler, "bad.avc",
		mustReadResolumeCompositionTestdata(t, "not-xml.txt"), headers)
	assertMatchesSchema(t, c, "Problem", malformedBody)

	oversize := bytes.Repeat([]byte("A"), maxResolumeCompositionUploadBytes*2)
	_, tooLargeBody := doResolumeCompositionUpload(t, api.Handler, "huge.avc", oversize, headers)
	assertMatchesSchema(t, c, "Problem", tooLargeBody)
}

// TestResolumeCompositionPayloadRoundTrips proves
// encodeResolumeCompositionPayload/decodeResolumeCompositionPayload round
// trip a payload without loss on the one field type easy to get wrong
// (json.Marshal of a *resolumecomp.Composition pointer field) — a cheap,
// fast unit check underneath the full HTTP-level tests above.
func TestResolumeCompositionPayloadRoundTrips(t *testing.T) {
	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	comp, err := resolumecomp.Parse(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	payload := resolumeCompositionStoredPayload{
		SourceFilename: "show.avc",
		ContentHash:    "sha256:deadbeef",
		SizeBytes:      int64(len(content)),
		Composition:    comp,
	}
	raw, err := encodeResolumeCompositionPayload(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeResolumeCompositionPayload(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SourceFilename != payload.SourceFilename || got.ContentHash != payload.ContentHash || got.SizeBytes != payload.SizeBytes {
		t.Errorf("round trip lost metadata: got %+v, want %+v", got, payload)
	}
	if got.Composition == nil || got.Composition.Name != comp.Name {
		t.Errorf("round trip lost the parsed composition: got %+v", got.Composition)
	}
	if len(got.Composition.Clips) != len(comp.Clips) {
		t.Errorf("round trip changed clip count: got %d, want %d", len(got.Composition.Clips), len(comp.Clips))
	}
}
