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

// This file is Track E seam E5's own test suite for the manifest HTTP
// surface (assetmanifest.go): GET /assets/manifest and
// GET /nodes/{nodeId}/assets. Every readiness verdict here is computed by
// internal/coordinator/assetsync — these tests exercise the HTTP mapping
// on top of it, not a second copy of its rules.

// assetManifestInventoryInterval is this file's own fixed inventory
// interval: 2 minutes, so [assetsync.StalenessWindow] is a known 6
// minutes and every "stale" fixture in this file can pick a report age
// unambiguously on either side of it.
const assetManifestInventoryInterval = 2 * time.Minute

// assetManifestTestDeps mirrors assetsTestDeps, additionally wiring
// AssetManifests — the one field seam E5 adds to Dependencies — against
// the same store.
func assetManifestTestDeps(t *testing.T, svc identity.Service, st *store.Store) Dependencies {
	t.Helper()
	deps := assetsTestDeps(t, svc, st)
	deps.AssetManifests = st
	deps.AssetInventoryInterval = assetManifestInventoryInterval
	return deps
}

// assetManifestAdminAPI mirrors assetsAdminAPI, wiring AssetManifests too.
func assetManifestAdminAPI(t *testing.T) (*API, *store.Store, map[string]string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(assetManifestTestDeps(t, svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	return api, st, map[string]string{"Authorization": "Bearer " + token}
}

func mustPutShowActive(t *testing.T, api *API, token, show string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.active", `{"show":"`+show+`"}`,
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.active(%s): status = %d, want 200; body: %s", show, resp.StatusCode, body)
	}
}

// --- local wire-decoding shapes, mirroring assets_test.go's v1AssetForTest pattern ---

type v1MissingAssetForTest struct {
	AssetID     string `json:"assetId"`
	Sequence    string `json:"sequence"`
	Filename    string `json:"filename"`
	ContentHash string `json:"contentHash"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type v1AssetGapForTest struct {
	Sequence string   `json:"sequence"`
	Surfaces []string `json:"surfaces"`
}

type v1ExtraAssetForTest struct {
	ContentHash string `json:"contentHash"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type v1NodeAssetManifestForTest struct {
	Node       string                  `json:"node"`
	State      string                  `json:"state"`
	Reason     *string                 `json:"reason"`
	Missing    []v1MissingAssetForTest `json:"missing"`
	Gaps       []v1AssetGapForTest     `json:"gaps"`
	Extra      []v1ExtraAssetForTest   `json:"extra"`
	ObservedAt *string                 `json:"observedAt"`
}

type v1NodeAssetManifestResponseForTest struct {
	ServerTime string                     `json:"serverTime"`
	Manifest   v1NodeAssetManifestForTest `json:"manifest"`
}

type v1AssetManifestResponseForTest struct {
	ServerTime string                       `json:"serverTime"`
	Nodes      []v1NodeAssetManifestForTest `json:"nodes"`
}

func getNodeAssetManifest(t *testing.T, api *API, auth map[string]string, nodeID string) (*http.Response, v1NodeAssetManifestResponseForTest, []byte) {
	t.Helper()
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/"+nodeID+"/assets", auth)
	var decoded v1NodeAssetManifestResponseForTest
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode node asset manifest: %v\nbody: %s", err, body)
		}
	}
	return resp, decoded, body
}

// --- undeclared / malformed node id ---

func TestGetNodeAssetManifestUndeclaredNodeIs404(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	resp, _, body := getNodeAssetManifest(t, api, auth, "no-such-node")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestGetNodeAssetManifestInvalidNodeIDIs400(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/Not_A_Valid_ID/assets", auth)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// --- the four unknown causes, each with its own test AND a combined
// distinctness assertion (spec: "a mapping that collapses all four to one
// string fails") ---

// TestNodeAssetManifestUnknownNoActiveShow is UnknownCauseNoActiveShow: no
// show.active has ever been PUT.
func TestNodeAssetManifestUnknownNoActiveShow(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "unknown" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.Manifest.State, "unknown", body)
	}
	if decoded.Manifest.Reason == nil || *decoded.Manifest.Reason == "" {
		t.Fatalf("reason is nil/empty for an unknown verdict; body: %s", body)
	}
	if decoded.Manifest.ObservedAt != nil {
		t.Errorf("observedAt = %v, want nil for an unknown verdict", *decoded.Manifest.ObservedAt)
	}
	if len(decoded.Manifest.Missing) != 0 || len(decoded.Manifest.Gaps) != 0 || len(decoded.Manifest.Extra) != 0 {
		t.Errorf("missing/gaps/extra should be empty for an unknown verdict, got %+v", decoded.Manifest)
	}
}

// TestNodeAssetManifestUnknownNeverReported is UnknownCauseNeverReported:
// an active show exists, but this node has never submitted an inventory
// report.
func TestNodeAssetManifestUnknownNeverReported(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	mustPutShowActive(t, api, auth["Authorization"][len("Bearer "):], "halloween-2026")

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "unknown" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.Manifest.State, "unknown", body)
	}
	if decoded.Manifest.Reason == nil {
		t.Fatal("reason is nil for an unknown verdict")
	}
}

// TestNodeAssetManifestUnknownStaleReport is UnknownCauseStaleReport: a
// report exists but is older than the 6-minute staleness window
// (3 x assetManifestInventoryInterval). Also proves the load-bearing
// ordering rule: a stale report NEVER renders as not_ready, even though
// the expected asset is not in its (stale) inventory.
func TestNodeAssetManifestUnknownStaleReport(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	staleAt := testNow.Add(-7 * time.Minute) // older than the 6-minute window
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: staleAt, Complete: true, Reason: ""}); err != nil {
		t.Fatalf("seed stale report: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "unknown" {
		t.Fatalf("state = %q, want %q (a stale report must NEVER render as not_ready); body: %s", decoded.Manifest.State, "unknown", body)
	}
	if decoded.Manifest.Reason == nil {
		t.Fatal("reason is nil for an unknown verdict")
	}
}

// TestNodeAssetManifestUnknownReportIncomplete is
// UnknownCauseReportIncomplete: the node's own report says complete=false,
// carrying the agent's own reason text.
func TestNodeAssetManifestUnknownReportIncomplete(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: false, Reason: "asset directory could not be opened"}); err != nil {
		t.Fatalf("seed incomplete report: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "unknown" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.Manifest.State, "unknown", body)
	}
	if decoded.Manifest.Reason == nil || !containsAll(*decoded.Manifest.Reason, "asset directory could not be opened") {
		t.Errorf("reason should carry the agent's own text, got %v", decoded.Manifest.Reason)
	}
}

// TestNodeAssetManifestUnknownCausesRenderDistinctReasons is this seam's
// own mutation-resistant test: all four UnknownCause values render
// state=="unknown", but their Reason text must be four DIFFERENT strings.
// A mapping that collapsed assetsync.UnknownCause into one fixed message
// (or ignored it) would pass every single-case test above yet fail this
// one, because it asserts on the SET of reasons, not on any one string in
// isolation.
//
// Broken and confirmed to fail: changed mapNodeAssetManifest's Unknown
// case to set out.Reason to a fixed literal ("state is unknown") instead
// of m.Reason — this test's distinctness assertion failed (all four
// reasons collapsed to one). Restored afterward.
func TestNodeAssetManifestUnknownCausesRenderDistinctReasons(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	reasons := map[string]string{}

	// 1: no active show.
	_, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "unknown" || decoded.Manifest.Reason == nil {
		t.Fatalf("no_active_show: state/reason = %q/%v; body: %s", decoded.Manifest.State, decoded.Manifest.Reason, body)
	}
	reasons["no_active_show"] = *decoded.Manifest.Reason

	mustPutShowActive(t, api, token, "halloween-2026")

	// 2: never reported.
	_, decoded, body = getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "unknown" || decoded.Manifest.Reason == nil {
		t.Fatalf("never_reported: state/reason = %q/%v; body: %s", decoded.Manifest.State, decoded.Manifest.Reason, body)
	}
	reasons["never_reported"] = *decoded.Manifest.Reason

	// 3: stale report.
	staleAt := testNow.Add(-7 * time.Minute)
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: staleAt, Complete: true}); err != nil {
		t.Fatalf("seed stale report: %v", err)
	}
	_, decoded, body = getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "unknown" || decoded.Manifest.Reason == nil {
		t.Fatalf("stale_report: state/reason = %q/%v; body: %s", decoded.Manifest.State, decoded.Manifest.Reason, body)
	}
	reasons["stale_report"] = *decoded.Manifest.Reason

	// 4: report incomplete.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: false, Reason: "walk failed"}); err != nil {
		t.Fatalf("seed incomplete report: %v", err)
	}
	_, decoded, body = getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "unknown" || decoded.Manifest.Reason == nil {
		t.Fatalf("report_incomplete: state/reason = %q/%v; body: %s", decoded.Manifest.State, decoded.Manifest.Reason, body)
	}
	reasons["report_incomplete"] = *decoded.Manifest.Reason

	seen := map[string]string{}
	for cause, reason := range reasons {
		if other, ok := seen[reason]; ok {
			t.Errorf("cause %q and %q rendered the IDENTICAL reason %q; every UnknownCause must render distinctly", cause, other, reason)
		}
		seen[reason] = cause
	}
	if len(seen) != 4 {
		t.Errorf("expected 4 distinct reason strings across 4 UnknownCause values, got %d: %+v", len(seen), reasons)
	}
}

// --- not_ready ---

// uploadOneAsset is this file's own thin wrapper over doAssetUpload for
// the manifest tests, which care about the resulting asset's identity
// fields more than the upload response itself.
func uploadOneAsset(t *testing.T, api *API, auth map[string]string, targetNode, sequence, filename string, content []byte) v1AssetForTest {
	t.Helper()
	fields := map[string]string{
		"show": "halloween-2026", "sequence": sequence, "mediaType": "fseq",
		"targetKind": "node", "target": targetNode,
	}
	resp, body := doAssetUpload(t, api.Handler, fields, filename, content, auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload %s for %s: status = %d, body: %s", filename, targetNode, resp.StatusCode, body)
	}
	var got v1AssetResponseForTest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode upload response: %v\nbody: %s", err, body)
	}
	return got.Asset
}

// TestNodeAssetManifestNotReadyNamesMissingAsset is acceptance criterion 3:
// a node missing an expected asset reports not_ready, naming exactly what
// is missing by sequence, filename, and content hash.
func TestNodeAssetManifestNotReadyNamesMissingAsset(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	asset := uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	// render-01 reports IN, but holding nothing — the asset is missing.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "not_ready" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.Manifest.State, "not_ready", body)
	}
	if decoded.Manifest.Reason == nil || *decoded.Manifest.Reason == "" {
		t.Fatal("reason is nil/empty for a not_ready verdict (ADR-020: reason always present when state is not ready)")
	}
	if len(decoded.Manifest.Missing) != 1 {
		t.Fatalf("missing has %d entries, want exactly 1; body: %s", len(decoded.Manifest.Missing), body)
	}
	got := decoded.Manifest.Missing[0]
	if got.AssetID != asset.ID || got.Sequence != "opening" || got.Filename != "Thriller.fseq" || got.ContentHash != asset.ContentHash {
		t.Errorf("missing[0] = %+v, want assetId=%q sequence=opening filename=Thriller.fseq contentHash=%q", got, asset.ID, asset.ContentHash)
	}
	if decoded.Manifest.ObservedAt == nil {
		t.Error("observedAt is nil for a not_ready verdict, want the report's own ReportedAt")
	}
}

// --- P2: the sync-disabled reason reaches the manifest ---

// TestNodeAssetManifestNotReadyStatesSyncDisabled pins P2:
// config.Config.AssetContentBaseURL's own doc comment and
// assetsync/sync.go's startup log line both promise that an unset content
// base URL is stated as the reason no node can be confirmed ready — nothing
// plumbed that promise through until this fix. assetManifestTestDeps never
// sets AssetSyncEnabled, so its zero value (false) applies here, and a
// not_ready node's reason must name the disabled sync.
func TestNodeAssetManifestNotReadyStatesSyncDisabled(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}

	_, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "not_ready" {
		t.Fatalf("state = %q, want not_ready; body: %s", decoded.Manifest.State, body)
	}
	if decoded.Manifest.Reason == nil || !containsAll(*decoded.Manifest.Reason, "SHOWMESH_ASSET_CONTENT_BASE_URL") {
		t.Fatalf("reason = %v, want it to name the disabled asset sync (AssetSyncEnabled defaults to false)", decoded.Manifest.Reason)
	}
}

// TestNodeAssetManifestNotReadyOmitsSyncDisabledNoteWhenEnabled is
// TestNodeAssetManifestNotReadyStatesSyncDisabled's counterpart: with
// AssetSyncEnabled explicitly true, the not_ready reason must NOT claim
// sync is disabled — this fix states a fact, not a fixed suffix.
func TestNodeAssetManifestNotReadyOmitsSyncDisabledNoteWhenEnabled(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSyncEnabled = true
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	mustPutShowActive(t, api, token, "halloween-2026")
	uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}

	_, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if decoded.Manifest.State != "not_ready" {
		t.Fatalf("state = %q, want not_ready; body: %s", decoded.Manifest.State, body)
	}
	if decoded.Manifest.Reason != nil && containsAll(*decoded.Manifest.Reason, "SHOWMESH_ASSET_CONTENT_BASE_URL") {
		t.Fatalf("reason = %q, want it NOT to mention the disabled-sync note when AssetSyncEnabled is true", *decoded.Manifest.Reason)
	}
}

// --- ready ---

func TestNodeAssetManifestReady(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	asset := uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: asset.ContentHash, RuntimeFilename: asset.RuntimeFilename, SizeBytes: asset.SizeBytes, VerifiedAt: testNow}},
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed satisfied report: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "ready" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.Manifest.State, "ready", body)
	}
	if decoded.Manifest.Reason != nil {
		t.Errorf("reason = %q, want nil for a ready verdict", *decoded.Manifest.Reason)
	}
	if !containsAll(string(body), `"reason":null`) {
		t.Errorf("reason must render as a literal JSON null, not be omitted (ADR-020); body: %s", body)
	}
	if len(decoded.Manifest.Missing) != 0 {
		t.Errorf("missing should be empty for a ready verdict, got %+v", decoded.Manifest.Missing)
	}
	if decoded.Manifest.ObservedAt == nil {
		t.Error("observedAt is nil for a ready verdict, want the report's own ReportedAt")
	}
}

// --- extra: never an error ---

func TestNodeAssetManifestExtraAssetIsReportedNeverAnError(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	asset := uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", []store.NodeAssetInventoryRecord{
		{NodeID: "render-01", ContentHash: asset.ContentHash, RuntimeFilename: asset.RuntimeFilename, SizeBytes: asset.SizeBytes, VerifiedAt: testNow},
		{NodeID: "render-01", ContentHash: "sha256:deadbeef", RuntimeFilename: "leftover.fseq", SizeBytes: 42, VerifiedAt: testNow},
	}, store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed report with an extra asset: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an unexpected asset is never an error); body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "ready" {
		t.Fatalf("state = %q, want %q (an extra asset never blocks readiness); body: %s", decoded.Manifest.State, "ready", body)
	}
	if len(decoded.Manifest.Extra) != 1 || decoded.Manifest.Extra[0].ContentHash != "sha256:deadbeef" {
		t.Errorf("extra = %+v, want exactly one entry naming sha256:deadbeef", decoded.Manifest.Extra)
	}
}

// --- gaps: a surfaced node with zero coverage for a sequence the show
// already has assets for elsewhere ---

func TestNodeAssetManifestGapNamesUncoveredSequence(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	// render-01 carries a surface in the active show...
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", validSurfaceBodyNDI, auth)
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface: status = %d, body: %s", resp.StatusCode, body)
	}

	// ...but only render-02 has any asset for sequence "opening" — the
	// show's own asset rows are what makes "opening" a known sequence at
	// all (assetsync's showSequenceIDs), and render-01 has zero coverage
	// for it, node-targeted or show-wide.
	uploadOneAsset(t, api, auth, "render-02", "opening", "Thriller.fseq", []byte("content"))

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed render-01 report: %v", err)
	}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "not_ready" {
		t.Fatalf("state = %q, want %q (a gap alone makes a node not_ready even with zero missing assets); body: %s", decoded.Manifest.State, "not_ready", body)
	}
	if len(decoded.Manifest.Gaps) != 1 {
		t.Fatalf("gaps has %d entries, want exactly 1; body: %s", len(decoded.Manifest.Gaps), body)
	}
	gap := decoded.Manifest.Gaps[0]
	if gap.Sequence != "opening" {
		t.Errorf("gap.sequence = %q, want %q", gap.Sequence, "opening")
	}
	if len(gap.Surfaces) != 1 || gap.Surfaces[0] != "garage" {
		t.Errorf("gap.surfaces = %v, want [garage]", gap.Surfaces)
	}
}

// --- GET /assets/manifest: every declared node ---

func TestListAssetManifestReturnsEveryDeclaredNode(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/assets/manifest", auth)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var decoded v1AssetManifestResponseForTest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if len(decoded.Nodes) != 2 {
		t.Fatalf("nodes has %d entries, want 2 (render-01, render-02); body: %s", len(decoded.Nodes), body)
	}
	seen := map[string]bool{}
	for _, n := range decoded.Nodes {
		seen[n.Node] = true
		if n.State != "unknown" {
			t.Errorf("node %q: state = %q, want %q (no active show configured)", n.Node, n.State, "unknown")
		}
	}
	if !seen["render-01"] || !seen["render-02"] {
		t.Errorf("expected both render-01 and render-02, got %+v", decoded.Nodes)
	}
}

// --- P1: Dependencies.AssetSyncNudger has a nil-safe default and a wired
// value is never silently overwritten ---

// spyAssetSyncNudger records every Nudge() call — this file's own stand-in
// for *assetsync.Service, which this package must not import for behavior
// (see assetmanifest.go's compile-time assertion for the real type's own
// pin).
type spyAssetSyncNudger struct{ calls int }

func (s *spyAssetSyncNudger) Nudge() { s.calls++ }

// TestDependenciesAssetSyncNudgerDefaultsToNoOp proves withDefaults gives a
// nil AssetSyncNudger a working, panic-free no-op — the same "an unwired
// dependency is not this API failing" posture every other field in this
// struct gets. Revert noAssetSyncNudger (or the nil check in withDefaults)
// and this panics on the nil method call instead of passing.
func TestDependenciesAssetSyncNudgerDefaultsToNoOp(t *testing.T) {
	deps := Dependencies{}.withDefaults()
	if deps.AssetSyncNudger == nil {
		t.Fatal("withDefaults() left AssetSyncNudger nil, want a no-op default")
	}
	deps.AssetSyncNudger.Nudge() // must not panic
}

// TestDependenciesAssetSyncNudgerWiredValuePreserved proves withDefaults
// never overwrites an already-wired AssetSyncNudger — this is what makes
// coordinator.go's "AssetSyncNudger: assetSync" wiring line actually reach
// a caller rather than being silently replaced by the no-op default.
func TestDependenciesAssetSyncNudgerWiredValuePreserved(t *testing.T) {
	spy := &spyAssetSyncNudger{}
	deps := Dependencies{AssetSyncNudger: spy}.withDefaults()
	deps.AssetSyncNudger.Nudge()
	if spy.calls != 1 {
		t.Fatalf("spy.calls = %d, want 1: withDefaults must preserve an already-wired AssetSyncNudger, not replace it with the no-op default", spy.calls)
	}
}

// --- an unwired AssetManifests dependency degrades honestly, never panics ---

// TestAssetManifestUnwiredStoreRendersUnknownNotPanic is P3's own
// regression test: before the fix, GET /assets/manifest with AssetManifests
// unwired rendered an EMPTY node list — "nothing is wrong" rather than
// "I cannot tell" — which made `showmeshctl assets manifest --require-ready`
// exit 0 for a coordinator that could not actually answer the question.
// Discovery is wired independently of AssetManifests (production always
// wires both against the same *store.Store, but this struct keeps them as
// two fields on purpose — see AssetManifests' own doc comment), so the
// fleet route can still enumerate declared nodes and must render each one
// "unknown" with a reason, the SAME verdict the single-node route already
// gives one node at a time.
func TestAssetManifestUnwiredStoreRendersUnknownNotPanic(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetsTestDeps(t, svc, st) // deliberately NOT assetManifestTestDeps: AssetManifests stays nil
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	auth := map[string]string{"Authorization": "Bearer " + token}

	resp, decoded, body := getNodeAssetManifest(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.Manifest.State != "unknown" {
		t.Errorf("state = %q, want %q with no AssetManifests store wired", decoded.Manifest.State, "unknown")
	}
	if decoded.Manifest.Reason == nil || *decoded.Manifest.Reason == "" {
		t.Error("reason is nil/empty with no AssetManifests store wired")
	}

	listResp, listBody := doRequest(t, api.Handler, "GET", "/api/v1/assets/manifest", auth)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d, want 200; body: %s", listResp.StatusCode, listBody)
	}
	var list v1AssetManifestResponseForTest
	if err := json.Unmarshal(listBody, &list); err != nil {
		t.Fatalf("decode list: %v\nbody: %s", err, listBody)
	}
	if len(list.Nodes) != 1 {
		t.Fatalf("nodes = %+v, want exactly 1 (render-01, from Discovery, which stays wired even though AssetManifests does not)", list.Nodes)
	}
	if list.Nodes[0].Node != "render-01" || list.Nodes[0].State != "unknown" {
		t.Errorf("nodes[0] = %+v, want node=render-01 state=unknown", list.Nodes[0])
	}
	if list.Nodes[0].Reason == nil || *list.Nodes[0].Reason == "" {
		t.Error("nodes[0].reason is nil/empty with no AssetManifests store wired — an unanswerable question must say so, never render as nothing wrong")
	}
}
