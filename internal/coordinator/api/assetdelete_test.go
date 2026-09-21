package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

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
