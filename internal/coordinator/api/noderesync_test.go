package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
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
		Node                      string `json:"node"`
		AcceptedAt                string `json:"acceptedAt"`
		InventoryRequestCommandID string `json:"inventoryRequestCommandId"`
	} `json:"resync"`
}

// spyInventoryRequester records every RequestNodeInventory call this
// route makes, so a test can prove the issuer it stamps is the
// authenticated caller, never a package constant - InventoryRequester's
// own doc comment (noderesync.go).
type spyInventoryRequester struct {
	calls []inventoryRequestCall
	err   error
}

type inventoryRequestCall struct {
	nodeID string
	issuer mqttproto.CmdIssuer
}

func (s *spyInventoryRequester) RequestNodeInventory(_ context.Context, nodeID string, issuer mqttproto.CmdIssuer) (string, error) {
	s.calls = append(s.calls, inventoryRequestCall{nodeID: nodeID, issuer: issuer})
	if s.err != nil {
		return "", s.err
	}
	return "cmd-" + nodeID, nil
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
// it requests exactly the named node through the existing AssetSyncNudger
// hook (never a second delivery path, and never every declared node), and
// the node's readiness verdict immediately after acceptance is computed
// independently by GET /nodes/{nodeId}/assets from the node's own
// (still-missing) evidence - not_ready, exactly as it was before the POST.
// Only once a fresh report dated AFTER acceptance arrives does the SAME
// route's evidence flip to ready, proving the outcome came from that
// observation, never from the fact a command was dispatched.
func TestPostResyncNodeAssetsAcceptedThenEvidence(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	auth := map[string]string{"Authorization": "Bearer " + token}

	spy := &spyAssetSyncNudger{}
	invReq := &spyInventoryRequester{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	deps.InventoryRequester = invReq
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
	if len(spy.requestedNode) != 0 {
		t.Fatalf("requestedNode = %v, want none: the route must no longer call RequestNode directly against a possibly-stale report, only RecordResyncIntent", spy.requestedNode)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-01" || !spy.recordedIntent[0].issuedAt.Equal(testNow) {
		t.Fatalf("recordedIntent = %+v, want exactly one intent for render-01 issued at %v", spy.recordedIntent, testNow)
	}
	if len(invReq.calls) != 1 || invReq.calls[0].nodeID != "render-01" {
		t.Fatalf("inventory request calls = %+v, want exactly one for render-01", invReq.calls)
	}
	if invReq.calls[0].issuer.PrincipalID != admin.ID || invReq.calls[0].issuer.PrincipalName != admin.Name {
		t.Fatalf("inventory request issuer = %+v, want the authenticated caller (%s/%s), never a package constant",
			invReq.calls[0].issuer, admin.ID, admin.Name)
	}
	if decoded.Resync.InventoryRequestCommandID != "cmd-render-01" {
		t.Fatalf("resync.inventoryRequestCommandId = %q, want %q", decoded.Resync.InventoryRequestCommandID, "cmd-render-01")
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

// TestPostResyncNodeAssetsAcceptsEvenWhenInventoryRequestFails proves the
// inventory push is best-effort, matching dispatchReconnectInventoryRequests'
// own "log and drop" posture one caller over (broker.go): a node the
// broker cannot currently reach must not turn an operator's press into a
// failed request. The intent is still recorded - that node's own next
// ordinary report still carries evidence fresh enough to trigger the
// repair, just later than a delivered push would have.
func TestPostResyncNodeAssetsAcceptsEvenWhenInventoryRequestFails(t *testing.T) {
	spy := &spyAssetSyncNudger{}
	invReq := &spyInventoryRequester{err: errors.New("simulated publish failure")}
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	deps.InventoryRequester = invReq
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "",
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 even when the inventory push fails; body: %s", resp.StatusCode, body)
	}
	var decoded v1ResyncNodeAssetsResponseForTest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if decoded.Resync.InventoryRequestCommandID != "" {
		t.Errorf("resync.inventoryRequestCommandId = %q, want empty: nothing was actually published", decoded.Resync.InventoryRequestCommandID)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-01" {
		t.Fatalf("recordedIntent = %+v, want the intent recorded even though the push failed", spy.recordedIntent)
	}
}

// TestPostResyncNodeAssetsRequestsOnlyTheNamedNode proves two operators
// resyncing two different nodes are distinguishable: each POST must name
// only its own node, never leak into the other's request.
func TestPostResyncNodeAssetsRequestsOnlyTheNamedNode(t *testing.T) {
	spy := &spyAssetSyncNudger{}
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-02/assets/resync", "",
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-02" {
		t.Fatalf("recordedIntent = %+v, want exactly one intent for render-02", spy.recordedIntent)
	}
}
