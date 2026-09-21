package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetstore"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is DELETE /api/v1/assets/{id}'s own test suite, one seam
// after assets_test.go's upload/list/content coverage. It follows that
// file's own fixtures (assetsAdminAPI, validAssetFields, doAssetUpload)
// rather than duplicating them.

// doAssetDelete issues DELETE /api/v1/assets/{id} with a
// {"confirm":true} body, mirroring configdelete_test.go's own shape one
// kind over.
func doAssetDelete(t *testing.T, h http.Handler, id string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/assets/"+id, `{"confirm":true}`, headers)
	return doRawRequest(t, h, req)
}

func TestDeleteAssetUnknownIDIs404(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	resp, body := doAssetDelete(t, api.Handler, "no-such-asset", auth)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestDeleteAssetRequiresAssetWriteScope(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	deps := assetsTestDeps(t, svc, st)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, body := doAssetDelete(t, api.Handler, "whatever-id", auth)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator (no asset:write) delete: status = %d, want 403; body: %s", resp.StatusCode, body)
	}
}

func TestDeleteAssetMissingConfirmBodyRefused(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	// No body at all.
	resp, body := doRequest(t, api.Handler, "DELETE", "/api/v1/assets/"+uploaded.Asset.ID, auth)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no body: status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	// confirm:false is refused exactly like a missing body.
	req := newJSONRequest(t, http.MethodDelete, "/api/v1/assets/"+uploaded.Asset.ID, `{"confirm":false}`, auth)
	resp2, body2 := doRawRequest(t, api.Handler, req)
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("confirm:false: status = %d, want 400; body: %s", resp2.StatusCode, body2)
	}

	// The row is untouched by either refused attempt.
	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID, auth)
	var got v1AssetResponseForTest
	mustDecodeJSON(t, getBody, &got)
	if got.Asset.ID != uploaded.Asset.ID {
		t.Fatalf("asset %s should still exist after a refused delete", uploaded.Asset.ID)
	}
}

// TestDeleteAssetSupersededRowLeavesCurrentUnaffected is acceptance
// criterion 1: deleting a superseded row must not touch the identity's
// current asset.
func TestDeleteAssetSupersededRowLeavesCurrentUnaffected(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	fields := validAssetFields()
	_, firstBody := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("first bytes"), auth)
	var first v1AssetResponseForTest
	mustDecodeJSON(t, firstBody, &first)

	_, secondBody := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("second bytes"), auth)
	var second v1AssetResponseForTest
	mustDecodeJSON(t, secondBody, &second)
	if second.Asset.Current != true || first.Asset.ID == second.Asset.ID {
		t.Fatalf("setup: expected a distinct current row; first=%+v second=%+v", first.Asset, second.Asset)
	}

	resp, body := doAssetDelete(t, api.Handler, first.Asset.ID, auth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete superseded row: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	if _, err := st.GetAsset(context.Background(), first.Asset.ID); !errors.Is(err, store.ErrAssetNotFound) {
		t.Errorf("superseded row after delete: err = %v, want ErrAssetNotFound", err)
	}

	current, err := st.GetCurrentAssetForTuple(context.Background(), fields["show"], fields["sequence"], fields["targetKind"], fields["target"], fields["mediaType"])
	if err != nil {
		t.Fatalf("get current asset for tuple: %v", err)
	}
	if current.ID != second.Asset.ID {
		t.Fatalf("current asset after deleting the superseded row = %q, want it unchanged at %q", current.ID, second.Asset.ID)
	}
}

// TestDeleteAssetCurrentRowLeavesNoCurrentAsset is acceptance criterion
// 2: deleting the current row must leave the identity with NO current
// asset (never promoting the superseded row back — that is rollback's
// job), and the list and manifest must agree.
func TestDeleteAssetCurrentRowLeavesNoCurrentAsset(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	fields := validAssetFields()
	_, firstBody := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("first bytes"), auth)
	var first v1AssetResponseForTest
	mustDecodeJSON(t, firstBody, &first)

	_, secondBody := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("second bytes"), auth)
	var second v1AssetResponseForTest
	mustDecodeJSON(t, secondBody, &second)

	resp, body := doAssetDelete(t, api.Handler, second.Asset.ID, auth)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete current row: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	ctx := context.Background()
	if _, err := st.GetCurrentAssetForTuple(ctx, fields["show"], fields["sequence"], fields["targetKind"], fields["target"], fields["mediaType"]); !errors.Is(err, store.ErrAssetNotFound) {
		t.Errorf("current asset for tuple after deleting the current row: err = %v, want ErrAssetNotFound (never promoted from the superseded row)", err)
	}

	// The superseded row (first) is untouched and still superseded.
	stillSuperseded, err := st.GetAsset(ctx, first.Asset.ID)
	if err != nil {
		t.Fatalf("get first asset: %v", err)
	}
	if stillSuperseded.SupersededAt == nil {
		t.Error("the superseded row must not have been promoted to current by deleting the current row")
	}

	// List agrees: no current row for this identity remains.
	current, err := st.ListCurrentAssetsForTarget(ctx, fields["show"], fields["targetKind"], fields["target"])
	if err != nil {
		t.Fatalf("list current for target: %v", err)
	}
	for _, rec := range current {
		if rec.SequenceID == fields["sequence"] && rec.MediaType == fields["mediaType"] {
			t.Errorf("ListCurrentAssetsForTarget still reports %+v as current after its delete", rec)
		}
	}
}

// TestDeleteAssetContentHashSharedRowKeepsBlobUntilLastReferenceGoes is
// acceptance criterion 3: two asset rows sharing one content hash (two
// different targets, identical bytes) keep the stored blob until BOTH
// rows are gone.
func TestDeleteAssetContentHashSharedRowKeepsBlobUntilLastReferenceGoes(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	backend := deps.AssetBackend
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	bytesA := []byte("shared content bytes")
	fieldsA := validAssetFields()
	fieldsA["target"] = "render-01"
	_, bodyA := doAssetUpload(t, api.Handler, fieldsA, "a.fseq", bytesA, auth)
	var a v1AssetResponseForTest
	mustDecodeJSON(t, bodyA, &a)

	fieldsB := validAssetFields()
	fieldsB["target"] = "render-02"
	_, bodyB := doAssetUpload(t, api.Handler, fieldsB, "a.fseq", bytesA, auth)
	var b v1AssetResponseForTest
	mustDecodeJSON(t, bodyB, &b)

	if a.Asset.ContentHash != b.Asset.ContentHash {
		t.Fatalf("setup: expected both uploads to share a content hash; a=%q b=%q", a.Asset.ContentHash, b.Asset.ContentHash)
	}
	hash := a.Asset.ContentHash

	// Deleting the first of the two rows must leave the blob in place.
	if resp, body := doAssetDelete(t, api.Handler, a.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete a: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if _, _, err := backend.Open(context.Background(), hash); err != nil {
		t.Fatalf("blob after deleting one of two referencing rows: Open = %v, want it to still exist", err)
	}

	// Deleting the second (last) row must remove the blob.
	if resp, body := doAssetDelete(t, api.Handler, b.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete b: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if _, _, err := backend.Open(context.Background(), hash); !errors.Is(err, assetstore.ErrNotFound) {
		t.Fatalf("blob after deleting the last referencing row: Open err = %v, want ErrNotFound", err)
	}
}

// TestDeleteAssetAuditEntryRecordsIdentity proves the required
// asset.delete audit entry, carrying the asset id and its identity
// fields.
func TestDeleteAssetAuditEntryRecordsIdentity(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)

	fields := validAssetFields()
	_, uploadBody := doAssetUpload(t, api.Handler, fields, "a.fseq", []byte("bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	if resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	audit, err := st.ListAuditEntries(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("list audit entries: %v", err)
	}
	found := false
	for _, e := range audit {
		if e.Action != "asset.delete" || e.Target != uploaded.Asset.ID {
			continue
		}
		found = true
		if e.Kind != string(identity.AuditAdmin) {
			t.Errorf("asset.delete audit kind = %q, want %q", e.Kind, identity.AuditAdmin)
		}
		var params struct {
			AssetID     string `json:"assetId"`
			Show        string `json:"show"`
			Sequence    string `json:"sequence"`
			TargetKind  string `json:"targetKind"`
			Target      string `json:"target"`
			MediaType   string `json:"mediaType"`
			ContentHash string `json:"contentHash"`
		}
		if err := json.Unmarshal([]byte(e.ParamsJSON), &params); err != nil {
			t.Fatalf("decode asset.delete audit params: %v\nparams: %s", err, e.ParamsJSON)
		}
		if params.AssetID != uploaded.Asset.ID || params.Show != fields["show"] || params.Sequence != fields["sequence"] ||
			params.TargetKind != fields["targetKind"] || params.Target != fields["target"] || params.MediaType != fields["mediaType"] ||
			params.ContentHash != uploaded.Asset.ContentHash {
			t.Errorf("asset.delete audit params = %+v, want the deleted asset's own identity fields", params)
		}
	}
	if !found {
		t.Errorf("no asset.delete audit entry found for %s among %+v", uploaded.Asset.ID, audit)
	}
}

// TestDeleteAssetTwiceIsSecond404 is acceptance criterion: a second
// delete of the same id is 404, never a silent success.
func TestDeleteAssetTwiceIsSecond404(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)

	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	if resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	resp2, body2 := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete: status = %d, want 404; body: %s", resp2.StatusCode, body2)
	}
}

// TestDeleteAssetNudgesAssetSync proves the manifest is told to notice,
// mirroring handlePostAssetUpload's own nudge.
func TestDeleteAssetNudgesAssetSync(t *testing.T) {
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

	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "a.fseq", []byte("bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)
	if nudger.n != 1 {
		t.Fatalf("nudges after upload = %d, want 1", nudger.n)
	}

	if resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if nudger.n != 2 {
		t.Fatalf("nudges after delete = %d, want 2: the manifest just changed", nudger.n)
	}
}

// TestDeleteAssetAudioRemovesRenditionBlob proves an audio asset's
// separately content-addressed rendition blob is removed alongside the
// original once nothing references it any longer.
func TestDeleteAssetAudioRemovesRenditionBlob(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	backend := deps.AssetBackend
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	fields := validAssetFields()
	fields["mediaType"] = "audio"
	_, uploadBody := doAssetUpload(t, api.Handler, fields, "a.wav", minimalTestWAV(1), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	renditionBlob, err := backend.Put(context.Background(), bytes.NewReader([]byte("rendered wav bytes")), 1<<20)
	if err != nil {
		t.Fatalf("stage rendition blob: %v", err)
	}
	if err := st.SetAudioRenditionReady(context.Background(), uploaded.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: renditionBlob.ContentHash, SizeBytes: renditionBlob.SizeBytes, DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("set audio rendition ready: %v", err)
	}

	if resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	if _, _, err := backend.Open(context.Background(), uploaded.Asset.ContentHash); !errors.Is(err, assetstore.ErrNotFound) {
		t.Errorf("original blob after delete: Open err = %v, want ErrNotFound", err)
	}
	if _, _, err := backend.Open(context.Background(), renditionBlob.ContentHash); !errors.Is(err, assetstore.ErrNotFound) {
		t.Errorf("rendition blob after delete: Open err = %v, want ErrNotFound", err)
	}
	if _, err := st.GetAudioRendition(context.Background(), uploaded.Asset.ContentHash); !errors.Is(err, store.ErrAudioRenditionNotFound) {
		t.Errorf("audio rendition row after delete: err = %v, want ErrAudioRenditionNotFound", err)
	}
}

// --- rendition blob reference counting (review finding 2) ---

// TestDeleteAssetKeepsRenditionBlobReferencedByAnotherRenditionRow proves
// a rendition BLOB (content-addressed like any other, ADR-028 decision 4)
// is not removed while a second audio_renditions row still names it,
// even though the asset row that triggered this delete's own original
// rendition row is gone.
func TestDeleteAssetKeepsRenditionBlobReferencedByAnotherRenditionRow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	backend := deps.AssetBackend
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	fields := validAssetFields()
	fields["mediaType"] = "audio"
	_, uploadBody := doAssetUpload(t, api.Handler, fields, "a.wav", minimalTestWAV(1), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	renditionBlob, err := backend.Put(context.Background(), bytes.NewReader([]byte("rendered wav bytes shared by two originals")), 1<<20)
	if err != nil {
		t.Fatalf("stage rendition blob: %v", err)
	}
	if err := st.SetAudioRenditionReady(context.Background(), uploaded.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: renditionBlob.ContentHash, SizeBytes: renditionBlob.SizeBytes, DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("set audio rendition ready for the deleted asset: %v", err)
	}
	// A SECOND original hash (never an asset row in this test; only the
	// audio_renditions row matters for this proof) transcodes to the SAME
	// bytes and still names renditionBlob's hash after the delete below.
	if err := st.SetAudioRenditionReady(context.Background(), "sha256:some-other-original", store.AudioRenditionReady{
		ContentHash: renditionBlob.ContentHash, SizeBytes: renditionBlob.SizeBytes, DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("set audio rendition ready for the surviving original: %v", err)
	}

	if resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	// This asset's own rendition ROW is gone (nothing resolves it by its
	// original hash any longer)...
	if _, err := st.GetAudioRendition(context.Background(), uploaded.Asset.ContentHash); !errors.Is(err, store.ErrAudioRenditionNotFound) {
		t.Errorf("deleted asset's own rendition row: err = %v, want ErrAudioRenditionNotFound", err)
	}
	// ...but the BLOB survives: the surviving row still names it.
	if _, _, err := backend.Open(context.Background(), renditionBlob.ContentHash); err != nil {
		t.Fatalf("rendition blob after delete: Open = %v, want it to still exist (another audio_renditions row references it)", err)
	}
	if _, err := st.GetAudioRendition(context.Background(), "sha256:some-other-original"); err != nil {
		t.Errorf("surviving rendition row: %v, want it untouched", err)
	}
}

// TestDeleteAssetKeepsRenditionBlobReferencedByAnAssetRow proves a
// rendition blob is not removed when an ASSET row's own content hash
// happens to equal it, even though no other audio_renditions row does.
func TestDeleteAssetKeepsRenditionBlobReferencedByAnAssetRow(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	backend := deps.AssetBackend
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// The bytes an unrelated fseq asset will carry ARE the rendition's own
	// content, uploaded first so its content hash is known.
	renditionBytes := []byte("this happens to be both a rendition and an fseq")
	fseqFields := validAssetFields()
	fseqFields["target"] = "render-02"
	_, fseqBody := doAssetUpload(t, api.Handler, fseqFields, "coincidence.fseq", renditionBytes, auth)
	var fseqAsset v1AssetResponseForTest
	mustDecodeJSON(t, fseqBody, &fseqAsset)

	audioFields := validAssetFields()
	audioFields["mediaType"] = "audio"
	audioFields["target"] = "render-01"
	_, audioBody := doAssetUpload(t, api.Handler, audioFields, "a.wav", minimalTestWAV(2), auth)
	var audioAsset v1AssetResponseForTest
	mustDecodeJSON(t, audioBody, &audioAsset)

	if err := st.SetAudioRenditionReady(context.Background(), audioAsset.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: fseqAsset.Asset.ContentHash, SizeBytes: int64(len(renditionBytes)), DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("set audio rendition ready: %v", err)
	}

	if resp, body := doAssetDelete(t, api.Handler, audioAsset.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204; body: %s", resp.StatusCode, body)
	}

	if _, _, err := backend.Open(context.Background(), fseqAsset.Asset.ContentHash); err != nil {
		t.Fatalf("blob after delete: Open = %v, want it to still exist (the fseq asset row still references it)", err)
	}
	if _, err := st.GetAsset(context.Background(), fseqAsset.Asset.ID); err != nil {
		t.Errorf("unrelated fseq asset: %v, want it untouched", err)
	}
}

// TestDeleteAssetCleansUpRenditionRegardlessOfDeletedRowsMediaType is
// review finding 4: two rows share one content hash, one "audio" and one
// "media"; deleting the audio row first, then the media row last, must
// still clean up the rendition once the LAST of the two goes, not only
// when the deleted row itself is "audio".
func TestDeleteAssetCleansUpRenditionRegardlessOfDeletedRowsMediaType(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	backend := deps.AssetBackend
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	content := minimalTestWAV(3)
	audioFields := validAssetFields()
	audioFields["mediaType"] = "audio"
	audioFields["sequence"] = "shared-audio-media"
	_, audioBody := doAssetUpload(t, api.Handler, audioFields, "a.wav", content, auth)
	var audioAsset v1AssetResponseForTest
	mustDecodeJSON(t, audioBody, &audioAsset)

	mediaFields := validAssetFields()
	mediaFields["mediaType"] = "media"
	mediaFields["sequence"] = "shared-audio-media"
	_, mediaBody := doAssetUpload(t, api.Handler, mediaFields, "a.wav", content, auth)
	var mediaAsset v1AssetResponseForTest
	mustDecodeJSON(t, mediaBody, &mediaAsset)

	if audioAsset.Asset.ContentHash != mediaAsset.Asset.ContentHash {
		t.Fatalf("setup: expected both uploads to share a content hash; audio=%q media=%q", audioAsset.Asset.ContentHash, mediaAsset.Asset.ContentHash)
	}

	renditionBlob, err := backend.Put(context.Background(), bytes.NewReader([]byte("rendered wav bytes")), 1<<20)
	if err != nil {
		t.Fatalf("stage rendition blob: %v", err)
	}
	if err := st.SetAudioRenditionReady(context.Background(), audioAsset.Asset.ContentHash, store.AudioRenditionReady{
		ContentHash: renditionBlob.ContentHash, SizeBytes: renditionBlob.SizeBytes, DurationMillis: 500, Format: "wav48k16s",
	}); err != nil {
		t.Fatalf("set audio rendition ready: %v", err)
	}

	// Delete the AUDIO row first. Its own mediaType matches "audio", but a
	// "media" row still shares the content hash, so nothing is orphaned yet.
	if resp, body := doAssetDelete(t, api.Handler, audioAsset.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete audio row: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if _, err := st.GetAudioRendition(context.Background(), audioAsset.Asset.ContentHash); err != nil {
		t.Fatalf("rendition row after deleting the audio row (media row still shares the hash): %v, want it to still exist", err)
	}
	if _, _, err := backend.Open(context.Background(), renditionBlob.ContentHash); err != nil {
		t.Fatalf("rendition blob after deleting the audio row: Open = %v, want it to still exist", err)
	}

	// Delete the MEDIA row last: the mediaType of THIS deleted row is
	// "media", never "audio", but it is the LAST row for that content
	// hash, so the rendition must be cleaned up now.
	if resp, body := doAssetDelete(t, api.Handler, mediaAsset.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete media row: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if _, err := st.GetAudioRendition(context.Background(), audioAsset.Asset.ContentHash); !errors.Is(err, store.ErrAudioRenditionNotFound) {
		t.Errorf("rendition row after deleting the last (media) row: err = %v, want ErrAudioRenditionNotFound", err)
	}
	if _, _, err := backend.Open(context.Background(), renditionBlob.ContentHash); !errors.Is(err, assetstore.ErrNotFound) {
		t.Errorf("rendition blob after deleting the last (media) row: Open err = %v, want ErrNotFound", err)
	}
}

// --- pinned assets (review finding 5) ---

func mustPutShowWeatherDelay(t *testing.T, api *API, token, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.weatherdelay", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.weatherdelay: status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}
}

func TestDeleteAssetRefusedWhenPinnedByWeatherDelayAlert(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "alert.fseq", []byte("alert bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	mustPutShowWeatherDelay(t, api, token, `{"alert":{"delayAssetId":"`+uploaded.Asset.ID+`"}}`)

	resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var p v1ProblemForTest
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v; body: %s", err, body)
	}
	const want = "This asset is the weather delay alert. Choose a different alert in the weather delay settings, then delete it."
	if p.Detail != want {
		t.Errorf("detail = %q, want %q", p.Detail, want)
	}

	if _, err := st.GetAsset(context.Background(), uploaded.Asset.ID); err != nil {
		t.Errorf("pinned asset after a refused delete: %v, want it untouched", err)
	}
}

func TestDeleteAssetRefusedWhenPinnedByWeatherDelayCancelNightAlert(t *testing.T) {
	api, _, auth := assetsAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "cancel.fseq", []byte("cancel bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	mustPutShowWeatherDelay(t, api, token, `{"alert":{"cancelNightAssetId":"`+uploaded.Asset.ID+`"}}`)

	resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var p v1ProblemForTest
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v; body: %s", err, body)
	}
	const want = "This asset is the weather delay cancel alert. Choose a different alert in the weather delay settings, then delete it."
	if p.Detail != want {
		t.Errorf("detail = %q, want %q", p.Detail, want)
	}
}

func TestDeleteAssetRefusedWhenPinnedByShowActionAnnouncementMedia(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	_, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "welcome.fseq", []byte("welcome bytes"), auth)
	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	mustPutAction(t, api, token, "start-announcement", `{
		"show": "halloween-2026",
		"label": "Welcome announcement",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "render-01",
			"audioSessionId": "announcement",
			"audioAction": "audio.session.apply",
			"params": {"media": {"assetId": "`+uploaded.Asset.ID+`", "contentHash": "`+uploaded.Asset.ContentHash+`", "filename": "welcome.fseq", "sizeBytes": 12}}
		}
	}`)

	resp, body := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var p v1ProblemForTest
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v; body: %s", err, body)
	}
	const want = `This asset is the announcement media for "Welcome announcement". Choose different media for that action, then delete it.`
	if p.Detail != want {
		t.Errorf("detail = %q, want %q", p.Detail, want)
	}

	if _, err := st.GetAsset(context.Background(), uploaded.Asset.ID); err != nil {
		t.Errorf("pinned asset after a refused delete: %v, want it untouched", err)
	}
}

// TestDeleteAssetUnpinnedAssetStillDeletesWithWeatherDelayAndShowActionConfigured
// proves the two pin checks are id-specific: a weather delay alert and an
// announcement both naming OTHER asset ids must never block deleting an
// unrelated asset.
func TestDeleteAssetUnpinnedAssetStillDeletesWithWeatherDelayAndShowActionConfigured(t *testing.T) {
	api, st, auth := assetsAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	_, pinnedBody := doAssetUpload(t, api.Handler, validAssetFields(), "pinned.fseq", []byte("pinned bytes"), auth)
	var pinned v1AssetResponseForTest
	mustDecodeJSON(t, pinnedBody, &pinned)
	mustPutShowWeatherDelay(t, api, token, `{"alert":{"delayAssetId":"`+pinned.Asset.ID+`"}}`)
	mustPutAction(t, api, token, "start-announcement", `{
		"show": "halloween-2026",
		"label": "Welcome announcement",
		"safetyClass": "none",
		"target": {
			"integration": "audio",
			"audioNodeId": "render-01",
			"audioSessionId": "announcement",
			"audioAction": "audio.session.apply",
			"params": {"media": {"assetId": "`+pinned.Asset.ID+`", "contentHash": "`+pinned.Asset.ContentHash+`", "filename": "pinned.fseq", "sizeBytes": 12}}
		}
	}`)

	otherFields := validAssetFields()
	otherFields["sequence"] = "unrelated"
	_, otherBody := doAssetUpload(t, api.Handler, otherFields, "other.fseq", []byte("other bytes"), auth)
	var other v1AssetResponseForTest
	mustDecodeJSON(t, otherBody, &other)

	if resp, body := doAssetDelete(t, api.Handler, other.Asset.ID, auth); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete unrelated asset: status = %d, want 204; body: %s", resp.StatusCode, body)
	}
	if _, err := st.GetAsset(context.Background(), other.Asset.ID); !errors.Is(err, store.ErrAssetNotFound) {
		t.Errorf("deleted asset still present: err = %v, want ErrAssetNotFound", err)
	}
	// The pinned asset is unaffected by any of this.
	if _, err := st.GetAsset(context.Background(), pinned.Asset.ID); err != nil {
		t.Errorf("pinned asset: %v, want it untouched", err)
	}
}

// --- the race (review finding 1) ---

// fakeBlockingDeleteBackend wraps a real assetstore.Backend and, when
// Delete is called for blockKey, signals started and then blocks on
// release before calling through - letting a test hold
// handleDeleteAsset inside its own post-commit blob removal while a
// second request runs concurrently.
type fakeBlockingDeleteBackend struct {
	assetstore.Backend
	blockKey string
	started  chan struct{}
	release  chan struct{}
}

func (b *fakeBlockingDeleteBackend) Delete(ctx context.Context, key string) error {
	if key == b.blockKey {
		close(b.started)
		<-b.release
	}
	return b.Backend.Delete(ctx, key)
}

// TestDeleteAssetBlobRemovalExcludesConcurrentUpload is review finding
// 1's own proof: without assetBlobMu, a delete's post-commit blob removal
// could run concurrently with an upload's Put/registration for the SAME
// content hash, ending with a current asset row whose bytes are gone.
// This forces exactly that window (blocks the delete inside its own
// backend.Delete call, after its metadata transaction has already
// committed) and proves a concurrent upload of the identical bytes for a
// different target does not proceed until the delete's write lock
// releases, and reads back cleanly once both finish. Run with -race.
func TestDeleteAssetBlobRemovalExcludesConcurrentUpload(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st)
	blocker := &fakeBlockingDeleteBackend{Backend: deps.AssetBackend, started: make(chan struct{}), release: make(chan struct{})}
	deps.AssetBackend = blocker
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}

	content := []byte("shared bytes for the race test")
	fieldsA := validAssetFields()
	fieldsA["target"] = "render-01"
	_, bodyA := doAssetUpload(t, api.Handler, fieldsA, "a.fseq", content, auth)
	var a v1AssetResponseForTest
	mustDecodeJSON(t, bodyA, &a)
	blocker.blockKey = a.Asset.ContentHash

	// Built on the test goroutine (uses t); only the already-encoded bytes
	// cross into the spawned goroutine below.
	fieldsB := validAssetFields()
	fieldsB["target"] = "render-02"
	uploadBodyBuf, uploadContentType := buildAssetUploadMultipartBody(t, fieldsB, "b.fseq", content)
	uploadBodyBytes := uploadBodyBuf.Bytes()

	deleteStatus := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/assets/"+a.Asset.ID, bytes.NewReader([]byte(`{"confirm":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		api.Handler.ServeHTTP(rec, req)
		deleteStatus <- rec.Result().StatusCode
	}()

	select {
	case <-blocker.started:
	case <-time.After(5 * time.Second):
		t.Fatal("delete never reached its own blob removal")
	}

	uploadStatus := make(chan int, 1)
	uploadBody := make(chan []byte, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assets", bytes.NewReader(uploadBodyBytes))
		req.Header.Set("Content-Type", uploadContentType)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		api.Handler.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Result().Body)
		uploadStatus <- rec.Result().StatusCode
		uploadBody <- b
	}()

	// The concurrent upload must NOT complete while the delete's write
	// lock is held (proving mutual exclusion, not merely eventual
	// correctness).
	select {
	case <-uploadStatus:
		t.Fatal("concurrent upload completed before the delete released its blob lock")
	case <-time.After(150 * time.Millisecond):
	}

	close(blocker.release)

	var deleteCode int
	select {
	case deleteCode = <-deleteStatus:
	case <-time.After(5 * time.Second):
		t.Fatal("delete never finished after its block was released")
	}
	if deleteCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteCode)
	}

	var uploadCode int
	var uploadRespBody []byte
	select {
	case uploadCode = <-uploadStatus:
		uploadRespBody = <-uploadBody
	case <-time.After(5 * time.Second):
		t.Fatal("upload never finished after the delete released its write lock")
	}
	if uploadCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200; body: %s", uploadCode, uploadRespBody)
	}

	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadRespBody, &uploaded)
	if uploaded.Asset.ContentHash != a.Asset.ContentHash {
		t.Fatalf("uploaded content hash = %q, want %q", uploaded.Asset.ContentHash, a.Asset.ContentHash)
	}

	// The bytes are actually readable: the delete never won the race
	// against this upload's own registration.
	contentResp, contentBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID+"/content", auth)
	if contentResp.StatusCode != http.StatusOK {
		t.Fatalf("GET content: status = %d, want 200; body: %s", contentResp.StatusCode, contentBody)
	}
	if !bytes.Equal(contentBody, content) {
		t.Fatalf("served content = %q, want %q", contentBody, content)
	}
}
