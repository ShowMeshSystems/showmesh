package api

import (
	"net/http"
	"testing"
)

// This file is Track E seam E3/E4's own conformance coverage, following
// resolumecomposition_test.go's pattern for the one multipart endpoint in
// this seam (POST /assets): only the RESPONSE body is checked against its
// schema, matching that precedent — requestBodySchemaRef (openapi_test.go)
// resolves only an "application/json" requestBody, and POST /assets'
// requestBody is multipart/form-data, so it is not applicable here.

// TestOpenAPIAssetsDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPIAssetsDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"Asset", "AssetResponse", "AssetsListResponse"} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIAssetsResponsesMatchRealResponses proves every response shape
// this seam's routes actually produce against a real coordinator wiring,
// per TRACK-E-SESSION-SPEC.md section 3.3.
func TestOpenAPIAssetsResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, _, auth := assetsAdminAPI(t)

	uploadResp, uploadBody := doAssetUpload(t, api.Handler, validAssetFields(), "Thriller.fseq", []byte("fseq content"), auth)
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /assets: status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}
	assertMatchesSchema(t, c, "AssetResponse", uploadBody)

	var uploaded v1AssetResponseForTest
	mustDecodeJSON(t, uploadBody, &uploaded)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/"+uploaded.Asset.ID, auth)
	assertMatchesSchema(t, c, "AssetResponse", getBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/assets", auth)
	assertMatchesSchema(t, c, "AssetsListResponse", listBody)

	// A rollback response (ADR-028 decision 10) against its own schema:
	// different bytes supersede the first upload, then re-uploading the
	// first upload's own bytes rolls it back.
	fields := validAssetFields()
	_, supersedeBody := doAssetUpload(t, api.Handler, fields, "Thriller.fseq", []byte("different bytes"), auth)
	assertMatchesSchema(t, c, "AssetResponse", supersedeBody)
	rollbackResp, rollbackBody := doAssetUpload(t, api.Handler, fields, "Thriller.fseq", []byte("fseq content"), auth)
	if rollbackResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /assets (rollback): status = %d, want 200; body: %s", rollbackResp.StatusCode, rollbackBody)
	}
	assertMatchesSchema(t, c, "AssetResponse", rollbackBody)
	var rolledBack v1AssetResponseForTest
	mustDecodeJSON(t, rollbackBody, &rolledBack)

	// A validation-error sample, to prove the shared Problem shape one
	// more time on this seam's own refusal path.
	badFields := validAssetFields()
	delete(badFields, "targetKind")
	badResp, badBody := doAssetUpload(t, api.Handler, badFields, "x.fseq", []byte("x"), auth)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /assets missing targetKind: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)

	// The asset-target-required problem, this seam's own minted type.
	targetReq := validAssetFields()
	delete(targetReq, "target")
	targetReqResp, targetReqBody := doAssetUpload(t, api.Handler, targetReq, "x.fseq", []byte("x"), auth)
	if targetReqResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /assets missing target: status = %d, want 400; body: %s", targetReqResp.StatusCode, targetReqBody)
	}
	assertMatchesSchema(t, c, "Problem", targetReqBody)

	notFoundResp, notFoundBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/no-such-asset", auth)
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /assets/no-such-asset: status = %d, want 404; body: %s", notFoundResp.StatusCode, notFoundBody)
	}
	assertMatchesSchema(t, c, "Problem", notFoundBody)

	// DELETE /assets/{id}: 204 (no body to check against a schema), a
	// missing confirm body's 400 Problem, and an unknown id's 404 Problem.
	delResp, delBody := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /assets/{id}: status = %d, want 204; body: %s", delResp.StatusCode, delBody)
	}

	delAgainResp, delAgainBody := doAssetDelete(t, api.Handler, uploaded.Asset.ID, auth)
	if delAgainResp.StatusCode != http.StatusNotFound {
		t.Fatalf("DELETE /assets/{id} (already deleted): status = %d, want 404; body: %s", delAgainResp.StatusCode, delAgainBody)
	}
	assertMatchesSchema(t, c, "Problem", delAgainBody)

	noConfirmResp, noConfirmBody := doRequest(t, api.Handler, "DELETE", "/api/v1/assets/"+rolledBack.Asset.ID, auth)
	if noConfirmResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("DELETE /assets/{id} with no body: status = %d, want 400; body: %s", noConfirmResp.StatusCode, noConfirmBody)
	}
	assertMatchesSchema(t, c, "Problem", noConfirmBody)

	// A pinned-asset 409, this seam's own minted type: proves the shape
	// api/openapi.yaml now documents for DELETE /assets/{id} against a
	// real refusal.
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowWeatherDelay(t, api, token, `{"alert":{"delayAssetId":"`+rolledBack.Asset.ID+`"}}`)
	pinnedResp, pinnedBody := doAssetDelete(t, api.Handler, rolledBack.Asset.ID, auth)
	if pinnedResp.StatusCode != http.StatusConflict {
		t.Fatalf("DELETE /assets/{id} pinned by show.weatherdelay: status = %d, want 409; body: %s", pinnedResp.StatusCode, pinnedBody)
	}
	assertMatchesSchema(t, c, "Problem", pinnedBody)
}

// mustDecodeJSON is a tiny local helper so this file does not need to
// import encoding/json solely for one call site.
func mustDecodeJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := jsonUnmarshalStrict(string(body), v); err != nil {
		t.Fatalf("decoding response: %v\nbody: %s", err, body)
	}
}
