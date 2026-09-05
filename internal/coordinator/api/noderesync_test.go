package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the resync route's own test suite (noderesync.go):
// POST /nodes/{nodeId}/assets/resync. The dispatch mechanism itself
// (assetsync.Service's tick/maybeDispatch/dispatchFetch) already has its
// own test suite one package over; these tests exercise only this route's
// own contract: it accepts and returns promptly, names no outcome, and the
// evidence a caller later reads (GET /nodes/{nodeId}/assets) comes from the
// node's own report, never from anything this route claims at accept time.

func TestOpenAPINodeResyncDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"ResyncNodeAssetsResponse", "ResyncNodeAssetsResult"} {
		compileSchema(t, c, name)
	}
}

type v1ResyncNodeAssetsResponseForTest struct {
	ServerTime string `json:"serverTime"`
	Resync     struct {
		Node       string `json:"node"`
		AcceptedAt string `json:"acceptedAt"`
	} `json:"resync"`
}

func TestPostResyncNodeAssetsUndeclaredNodeIs404(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/no-such-node/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestPostResyncNodeAssetsInvalidNodeIDIs400(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/Not_A_Valid_ID/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestPostResyncNodeAssetsDisabledSyncRefused pins the "asset sync disabled"
// error path: assetManifestAdminAPI never sets a ContentBaseURL, so
// dispatching an asset.fetch would be accepted but never actually deliver
// anything - this route refuses before accepting rather than promising a
// re-sync it cannot perform.
func TestPostResyncNodeAssetsDisabledSyncRefused(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if !containsAll(problem.Detail, "contentBaseUrl") {
		t.Fatalf("problem detail = %q, want it to name the disabled asset sync setting", problem.Detail)
	}
}

// TestPostResyncNodeAssetsAcceptedThenEvidence is this route's own
// acceptance criterion 1: the response is a 202 naming only acceptance (no
// outcome field exists on the wire at all - see ResyncNodeAssetsResult),
// it nudges the SAME AssetSyncNudger hook the asset-upload handler already
// uses (never a second delivery path), and the node's readiness verdict
// immediately after acceptance is computed independently by
// GET /nodes/{nodeId}/assets from the node's own (still-missing) evidence
// - not_ready, exactly as it was before the POST. Only once a fresh report
// dated AFTER acceptance arrives does the SAME route's evidence flip to
// ready, proving the outcome came from that observation, never from the
// fact a command was dispatched.
func TestPostResyncNodeAssetsAcceptedThenEvidence(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	auth := map[string]string{"Authorization": "Bearer " + token}

	spy := &spyAssetSyncNudger{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	mustPutShowActive(t, api, token, "halloween-2026")
	asset := uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	// render-01 reports in, holding nothing - the expected asset is missing,
	// so GET .../assets already reads not_ready before this route is ever
	// called.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}

	spy.calls = 0 // uploadOneAsset above already nudges on upload; only this route's own call counts here.
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "ResyncNodeAssetsResponse", body)
	var decoded v1ResyncNodeAssetsResponseForTest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if decoded.Resync.Node != "render-01" {
		t.Errorf("resync.node = %q, want %q", decoded.Resync.Node, "render-01")
	}
	if decoded.Resync.AcceptedAt == "" {
		t.Error("resync.acceptedAt is empty")
	}
	if !containsAll(string(body), `"acceptedAt"`) || containsAll(string(body), `"outcome"`) {
		t.Errorf("body must carry acceptance only, never an outcome field; body: %s", body)
	}
	if spy.calls != 1 {
		t.Fatalf("spy.calls = %d, want 1: this route must reuse AssetSyncNudger, never a second delivery path", spy.calls)
	}

	// The acceptance claims nothing: the manifest, read right after, still
	// reflects the node's own (unchanged) evidence.
	_, manifestAfterAccept, manifestBody := getNodeAssetManifest(t, api, auth, "render-01")
	if manifestAfterAccept.Manifest.State != "not_ready" {
		t.Fatalf("state right after acceptance = %q, want %q (accepting a resync must never itself claim an outcome); body: %s",
			manifestAfterAccept.Manifest.State, "not_ready", manifestBody)
	}

	// Only a report dated AFTER acceptance, now actually holding the
	// expected content hash, is evidence of anything - the same
	// FetchConfirmed rule assetsync/sync.go already enforces.
	confirmedAt := testNow.Add(time.Minute)
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: asset.ContentHash, RuntimeFilename: asset.RuntimeFilename, SizeBytes: asset.SizeBytes, VerifiedAt: confirmedAt}},
		store.NodeAssetReportRecord{ReportedAt: confirmedAt, Complete: true}); err != nil {
		t.Fatalf("seed post-dispatch report: %v", err)
	}

	_, manifestAfterEvidence, manifestBody2 := getNodeAssetManifest(t, api, auth, "render-01")
	if manifestAfterEvidence.Manifest.State != "ready" {
		t.Fatalf("state after fresh confirming evidence = %q, want %q; body: %s",
			manifestAfterEvidence.Manifest.State, "ready", manifestBody2)
	}
}
