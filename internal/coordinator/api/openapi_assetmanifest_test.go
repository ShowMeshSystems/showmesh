package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track E seam E5's own conformance coverage: every response
// shape GET /assets/manifest and GET /nodes/{nodeId}/assets actually
// produce, checked against api/openapi.yaml — matching
// openapi_assets_test.go's pattern one seam over.

// TestOpenAPIAssetManifestDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPIAssetManifestDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"NodeAssetManifest", "MissingAsset", "AssetGap", "ExtraAsset",
		"NodeAssetManifestResponse", "AssetManifestResponse",
	} {
		compileSchema(t, c, name)
	}
}

// TestOpenAPIAssetManifestResponsesMatchRealResponses proves every state
// this seam's mapping can render — unknown (no active show), not_ready
// (a named missing asset), and ready — against a real coordinator wiring,
// for both routes.
func TestOpenAPIAssetManifestResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, st, auth := assetManifestAdminAPI(t)

	// unknown: no active show configured yet.
	_, unknownBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/assets", auth)
	assertMatchesSchema(t, c, "NodeAssetManifestResponse", unknownBody)

	_, listBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/manifest", auth)
	assertMatchesSchema(t, c, "AssetManifestResponse", listBody)

	// not_ready: an active show with an expected asset render-01 does not
	// hold.
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}
	_, notReadyBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/assets", auth)
	assertMatchesSchema(t, c, "NodeAssetManifestResponse", notReadyBody)

	// ready: render-01 now reports holding it.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: contentHashOf([]byte("content")), RuntimeFilename: "Thriller.fseq", SizeBytes: int64(len("content")), VerifiedAt: testNow}},
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed satisfied report: %v", err)
	}
	_, readyBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/assets", auth)
	assertMatchesSchema(t, c, "NodeAssetManifestResponse", readyBody)

	// 404: nodeId does not name a declared node.
	notFoundResp, notFoundBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/no-such-node/assets", auth)
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /nodes/no-such-node/assets: status = %d, want 404; body: %s", notFoundResp.StatusCode, notFoundBody)
	}
	assertMatchesSchema(t, c, "Problem", notFoundBody)

	// 400: nodeId is not syntactically valid.
	badResp, badBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/Not_Valid/assets", auth)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET /nodes/Not_Valid/assets: status = %d, want 400; body: %s", badResp.StatusCode, badBody)
	}
	assertMatchesSchema(t, c, "Problem", badBody)
}
